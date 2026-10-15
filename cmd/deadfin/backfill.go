// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/deadfin-org/deadfin/internal/backfill"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/store"
)

// runBackfill runs one native-model backfill pass against the program-data
// store. It opens the store, runs the pass, prints a JSON run report to
// stdout, and exits. Exit 0 when every unit succeeds. Exit 1 when any unit
// fails, with the partial report printed.
//
// Usage: deadfin backfill --program-data <root> [--database-url <url>]
//
// Stop the server before you run this command. The command fails fast when
// the SQLite writer is locked. PostgreSQL uses normal writer serialization.
func runBackfill(args []string) {
	flags := flag.NewFlagSet("backfill", flag.ExitOnError)
	programData := flags.String("program-data", "", programDataUsage)
	databaseURL := flags.String("database-url", "", databaseURLUsage)
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}
	if err := runBackfillCmd(context.Background(), resolveProgramData(*programData, os.Getenv), resolveDatabaseURL(*databaseURL, os.Getenv(databaseURLEnv))); err != nil {
		log.Fatal(err)
	}
}

func runBackfillCmd(ctx context.Context, programData, databaseURL string) (err error) {
	paths, err := config.NewPaths(programData)
	if err != nil {
		return fmt.Errorf("derive program-data paths: %w", err)
	}
	if err := os.MkdirAll(paths.Root, 0o700); err != nil {
		return fmt.Errorf("create root: %w", err)
	}
	if err := os.MkdirAll(paths.Data, 0o700); err != nil {
		return fmt.Errorf("create data: %w", err)
	}
	stores, _, err := openProgramDatabase(ctx, databaseURL, paths.Data)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := stores.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close stores: %w", closeErr)
		}
	}()
	items := store.NewItemStore(stores)
	native := store.NewNativeStore(stores, clock.NewReal())
	pass := backfill.New(backfill.Deps{Items: items, Native: native}, clock.NewReal())
	if err := pass.Run(ctx, os.Stdout); err != nil {
		return fmt.Errorf("backfill: %w", err)
	}
	return nil
}

// backfillReport captures the minimal run report shape for tests that verify
// the CLI prints valid JSON with zero counters on an empty database.
type backfillReport struct {
	UnitsUpserted int `json:"units_upserted"`
	Failed        int `json:"failed"`
}

// parseBackfillReport decodes a JSON run report from raw bytes.
func parseBackfillReport(data []byte) (backfillReport, error) {
	var r backfillReport
	if err := json.Unmarshal(data, &r); err != nil {
		return backfillReport{}, err
	}
	return r, nil
}
