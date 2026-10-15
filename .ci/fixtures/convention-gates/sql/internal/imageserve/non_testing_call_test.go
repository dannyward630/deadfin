// SPDX-License-Identifier: GPL-2.0-only

// Package imageserve_test is an isolated negative-control fixture for
// verify-convention-gate-fixtures. The string below is SQL-shaped but is not a
// direct testing diagnostic: it is passed to an unrelated local helper (not a
// testing.T/TB method). The exemption must not cover it, so the gate must
// reject it as raw SQL. This file holds exactly one raw-SQL report so its
// assertion fails when that report is absent.
package imageserve_test

// nonTestingCall passes SQL-shaped prose to errorf, an unrelated local helper,
// not testing.Fatalf. A SQL-shaped first argument to it is not a test
// diagnostic and must be rejected.
func nonTestingCall() error {
	return errorf("SELECT id FROM cache")
}

// errorf is an unrelated local helper, not testing.Fatalf.
func errorf(format string, args ...any) error { return nil }
