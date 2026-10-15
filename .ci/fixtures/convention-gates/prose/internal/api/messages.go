// SPDX-License-Identifier: GPL-2.0-only

// Package api holds prose that merely begins with SQL-looking words. None of
// it is a statement; the gate must accept all of it (regression for the
// convention-gate false positive that flagged t.Fatalf("create view root: %v")).
package api

import "fmt"

func Messages() []error {
	return []error{
		fmt.Errorf("create view root: %w", nil),
		fmt.Errorf("create table directory missing"),
		fmt.Errorf("drop index file: %w", nil),
		fmt.Errorf("delete from cache: %w", nil),
		fmt.Errorf("alter table layout unsupported"),
		fmt.Errorf("insert into queue failed: %w", nil),
	}
}
