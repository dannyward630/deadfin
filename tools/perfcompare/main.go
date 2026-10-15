// SPDX-License-Identifier: GPL-2.0-only

// Command perfcompare compares the latency and the result counts of a
// Jellyfin server and a Deadfin server that serve the same libraries.
//
// It discovers a user and representative items on Jellyfin, finds the same
// items on Deadfin (by id, else by path), and replays a fixed set of client
// requests modeled on jellyfin-web and Swiftfin: home rows, library browse,
// item detail, seasons and episodes, search, PlaybackInfo, a card image, and
// one HLS transcode start that it stops at once. It writes a Markdown table
// and, on request, the JSON result or a JSONL append.
//
// The run only reads, except for the transcode start and stop. Each server
// takes its own token, or both take PERFCOMPARE_TOKEN when one imported token
// works on both:
//
//	PERFCOMPARE_TOKEN=xxxx go run ./tools/perfcompare \
//	    -jellyfin https://jellyfin.example \
//	    -deadfin https://deadfin.example \
//	    -jellyfin-host "node-a (i3-8300)" -deadfin-host "node-b" \
//	    -json run.json -jsonl history.jsonl
//
// The report subcommand renders a static HTML page from the JSONL history:
//
//	go run ./tools/perfcompare report -in history.jsonl -out index.html
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if len(os.Args) > 1 && os.Args[1] == "report" {
		err = reportMain(os.Args[2:])
	} else {
		err = runMain(ctx, os.Args[1:], os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "perfcompare:", err)
		os.Exit(1)
	}
}

func runMain(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("perfcompare", flag.ContinueOnError)
	jfURL := fs.String("jellyfin", "", "Jellyfin base URL")
	dfURL := fs.String("deadfin", "", "Deadfin base URL")
	n := fs.Int("n", 10, "measured iterations per scenario")
	warmup := fs.Int("warmup", 2, "warm-up iterations per scenario, not measured")
	hlsRuns := fs.Int("hls", 1, "HLS transcode starts per server (0 to skip)")
	user := fs.String("user", "", "user name (default: the token's user, or the first administrator)")
	jfHost := fs.String("jellyfin-host", "", "where the Jellyfin pod runs, for the report header")
	dfHost := fs.String("deadfin-host", "", "where the Deadfin pod runs, for the report header")
	timeout := fs.Duration("timeout", 60*time.Second, "per-request timeout")
	jsonOut := fs.String("json", "", "write the run as JSON to this file")
	jsonlOut := fs.String("jsonl", "", "append the run as one JSON line to this file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *jfURL == "" || *dfURL == "" {
		return errors.New("-jellyfin and -deadfin are required")
	}
	if *n < 1 || *warmup < 0 || *hlsRuns < 0 {
		return errors.New("-n must be at least 1; -warmup and -hls must not be negative")
	}
	jfToken, dfToken := tokenFor("JELLYFIN_TOKEN"), tokenFor("DEADFIN_TOKEN")
	if jfToken == "" || dfToken == "" {
		return errors.New("set PERFCOMPARE_TOKEN, or JELLYFIN_TOKEN and DEADFIN_TOKEN")
	}

	jf := newServer("jellyfin", *jfURL, jfToken, *timeout)
	df := newServer("deadfin", *dfURL, dfToken, *timeout)
	run, err := compare(ctx, jf, df, options{
		iterations: *n, warmup: *warmup, hlsRuns: *hlsRuns, user: *user,
		jellyfinHost: *jfHost, deadfinHost: *dfHost, now: time.Now,
	})
	if err != nil {
		return err
	}
	writeTable(stdout, run)

	if *jsonOut != "" {
		b, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*jsonOut, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	if *jsonlOut != "" {
		b, err := json.Marshal(run)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*jsonlOut, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	return nil
}

// tokenFor reads the per-server token, falling back to PERFCOMPARE_TOKEN.
func tokenFor(env string) string {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("PERFCOMPARE_TOKEN"))
}

func reportMain(args []string) error {
	fs := flag.NewFlagSet("perfcompare report", flag.ContinueOnError)
	in := fs.String("in", "", "JSONL history to read")
	out := fs.String("out", "", "HTML file to write")
	limit := fs.Int("runs", 30, "runs in the trend table")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return errors.New("-in and -out are required")
	}
	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	runs, err := readRuns(f)
	f.Close()
	if err != nil {
		return err
	}
	tmp := *out + ".tmp"
	w, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := writeHTML(w, runs, *limit); err != nil {
		w.Close()
		os.Remove(tmp)
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, *out)
}
