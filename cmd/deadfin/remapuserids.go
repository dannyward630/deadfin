// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// remapUserIDs gives every user a new random id and rewrites every reference
// in one transaction (store.Identity.RemapUserIDs). import-jellyfin once kept
// each Jellyfin user id; Swiftfin keys saved users by id alone, so it confused
// the two servers. Access tokens stay valid, but each client sees a new user
// id: Swiftfin needs one new sign-in on Deadfin. A running server keeps live
// sessions under the old ids in memory, so stop the server first or restart
// it at once after the run, while nothing plays. A second run does nothing.
//
// Usage: deadfin remap-user-ids [--program-data <root>] [--database-url <url>] [--dry-run]
func remapUserIDs(args []string) {
	flags := flag.NewFlagSet("remap-user-ids", flag.ExitOnError)
	dryRun := flags.Bool("dry-run", false, "report what would change and write nothing")
	programData := flags.String("program-data", "", programDataUsage)
	databaseURL := flags.String("database-url", "", databaseURLUsage)
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}
	if err := runRemapUserIDs(context.Background(), os.Stdout, resolveProgramData(*programData, os.Getenv), resolveDatabaseURL(*databaseURL, os.Getenv(databaseURLEnv)), clock.NewReal(), *dryRun); err != nil {
		log.Fatal(err)
	}
}

func runRemapUserIDs(ctx context.Context, out io.Writer, programData, databaseURL string, c clock.Clock, dryRun bool) (err error) {
	paths, err := config.NewPaths(programData)
	if err != nil {
		return fmt.Errorf("derive program-data paths: %w", err)
	}
	selected, err := parseDatabase(databaseURL, paths.Data)
	if err != nil {
		return err
	}
	// The command rewrites an existing installation. Opening a missing SQLite
	// file would create an empty store instead.
	if selected.postgresDSN == "" {
		if _, err := os.Stat(selected.sqlitePath); err != nil {
			return fmt.Errorf("open database: %w", err)
		}
	}
	stores, err := selected.open(ctx)
	if err != nil {
		return fmt.Errorf("open %s: %w", selected, err)
	}
	defer func() {
		if closeErr := stores.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close stores: %w", closeErr)
		}
	}()
	report, err := store.NewIdentity(stores).RemapUserIDs(ctx, newRandomUserID, c.Now(), dryRun)
	if err != nil {
		return err
	}
	if report.AlreadyRemapped {
		fmt.Fprintf(out, "user ids were remapped at %s; nothing to do\n", report.RemappedAt)
		return nil
	}
	if dryRun {
		fmt.Fprintln(out, "dry run: nothing was written")
	}
	for _, change := range report.Users {
		fmt.Fprintf(out, "user %q: %s -> %s\n", change.Name, change.Old, change.New)
	}
	refs := make([]string, 0, len(report.Rows))
	for ref := range report.Rows {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		fmt.Fprintf(out, "%s: %d rows\n", ref, report.Rows[ref])
	}
	if !dryRun {
		fmt.Fprintln(out, "access tokens are unchanged; clients that key users by id (Swiftfin) need one new sign-in")
	}
	return nil
}

// newRandomUserID returns a random version-4 UUID.
func newRandomUserID() (dto.UUID, error) {
	var id dto.UUID
	if _, err := rand.Read(id[:]); err != nil {
		return dto.UUID{}, err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id, nil
}
