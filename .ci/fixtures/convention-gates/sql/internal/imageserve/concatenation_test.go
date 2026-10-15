// SPDX-License-Identifier: GPL-2.0-only

// Package imageserve_test is an isolated negative-control fixture for
// verify-convention-gate-fixtures. The string below is SQL-shaped but is
// not a direct testing diagnostic. It is the left literal of a
// concatenation, a direct argument to a non-testing operation. The
// exemption must not cover it, so the gate must reject it as raw SQL.
// This file holds exactly one raw-SQL report so its assertion fails when
// that report is absent.
package imageserve_test

// concatenation builds SQL from a concatenation. The left literal is
// not a direct testing diagnostic, so the gate must reject it.
func concatenation() string {
	return "SELECT * FROM items WHERE id = " + "1"
}
