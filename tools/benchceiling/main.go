// SPDX-License-Identifier: GPL-2.0-only

// Command benchceiling checks `go test -bench` output against absolute
// per-benchmark ceilings. It prints a Markdown table of the results and
// exits 1 when a benchmark exceeds its ceiling, when a benchmark has no
// ceiling, when a ceiling has no result, or when the run reports a failure.
//
// The ceilings are absolute, not a history, so the check needs no state
// store. A ceiling with no result fails the check: a skipped backend or a
// renamed scenario must not pass silently.
//
//	go test -run '^$' -bench AtLibraryScale -benchtime 20x ./internal/store/storetest/ |
//	    go run ./tools/benchceiling -ceilings internal/store/storetest/testdata/scale-bench-ceilings.txt
//
// The ceilings file has one "<benchmark name> <duration>" pair a line, for
// example "BenchmarkX/sqlite/page 6ms". The name omits the -GOMAXPROCS
// suffix. Blank lines and lines that start with "#" are ignored.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "benchceiling:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("benchceiling", flag.ContinueOnError)
	path := fs.String("ceilings", "", "ceilings file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("-ceilings is required")
	}
	file, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	ceilings, order, err := parseCeilings(file)
	if err != nil {
		return fmt.Errorf("%s: %w", *path, err)
	}
	results, failed, err := parseResults(stdin, stdout)
	if err != nil {
		return err
	}
	return check(ceilings, order, results, failed, stdout)
}

func parseCeilings(r io.Reader) (map[string]time.Duration, []string, error) {
	ceilings := map[string]time.Duration{}
	var order []string
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 {
			return nil, nil, fmt.Errorf("line %d: want \"<benchmark> <duration>\", got %q", line, text)
		}
		ceiling, err := time.ParseDuration(fields[1])
		if err != nil || ceiling <= 0 {
			return nil, nil, fmt.Errorf("line %d: bad duration %q", line, fields[1])
		}
		if _, seen := ceilings[fields[0]]; seen {
			return nil, nil, fmt.Errorf("line %d: duplicate ceiling for %s", line, fields[0])
		}
		ceilings[fields[0]] = ceiling
		order = append(order, fields[0])
	}
	return ceilings, order, scanner.Err()
}

// resultLine matches a benchmark result: name, iterations, ns/op.
var resultLine = regexp.MustCompile(`^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+([0-9.]+) ns/op`)

// parseResults copies the run output to log and returns the slowest ns/op
// for each benchmark, plus whether the run reported a failure.
func parseResults(r io.Reader, log io.Writer) (map[string]float64, bool, error) {
	results := map[string]float64{}
	failed := false
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		text := scanner.Text()
		if _, err := fmt.Fprintln(log, text); err != nil {
			return nil, false, err
		}
		if strings.HasPrefix(text, "--- FAIL") || text == "FAIL" || strings.HasPrefix(text, "FAIL\t") || strings.HasPrefix(text, "panic:") {
			failed = true
		}
		match := resultLine.FindStringSubmatch(text)
		if match == nil {
			continue
		}
		ns, err := strconv.ParseFloat(match[2], 64)
		if err != nil {
			return nil, false, fmt.Errorf("parse %q: %w", text, err)
		}
		if ns > results[match[1]] {
			results[match[1]] = ns
		}
	}
	return results, failed, scanner.Err()
}

func check(ceilings map[string]time.Duration, order []string, results map[string]float64, failed bool, out io.Writer) error {
	var problems []string
	if failed {
		problems = append(problems, "the benchmark run reported a failure")
	}
	var b strings.Builder
	b.WriteString("\n| Benchmark | Result | Ceiling | Share | Status |\n|---|--:|--:|--:|---|\n")
	for _, name := range order {
		ceiling := ceilings[name]
		ns, ok := results[name]
		if !ok {
			fmt.Fprintf(&b, "| %s | — | %s | — | MISSING |\n", name, ceiling)
			problems = append(problems, name+": no result")
			continue
		}
		status := "ok"
		if ns > float64(ceiling) {
			status = "OVER"
			problems = append(problems, fmt.Sprintf("%s: %s exceeds the ceiling %s", name, duration(ns), ceiling))
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %.0f%% | %s |\n", name, duration(ns), ceiling, 100*ns/float64(ceiling), status)
	}
	var unlisted []string
	for name := range results {
		if _, ok := ceilings[name]; !ok {
			unlisted = append(unlisted, name)
		}
	}
	sort.Strings(unlisted)
	for _, name := range unlisted {
		fmt.Fprintf(&b, "| %s | %s | — | — | NO CEILING |\n", name, duration(results[name]))
		problems = append(problems, name+": no ceiling")
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return err
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func duration(ns float64) string {
	return time.Duration(ns).Round(time.Microsecond).String()
}
