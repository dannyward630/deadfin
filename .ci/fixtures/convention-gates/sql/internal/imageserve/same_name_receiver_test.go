// SPDX-License-Identifier: GPL-2.0-only

// Package imageserve_test is an isolated negative-control fixture for
// verify-convention-gate-fixtures. It guards the lexical-scope boundary
// of the testing-diagnostic exemption. A receiver recognized in one
// function must not be exempted in an unrelated function. The unrelated
// function has a parameter with the same name but a different type. Here
// real's t is *testing.T, so its diagnostic is exempt. Fake's t is fakeT,
// so its SQL-shaped Fatalf must be rejected as raw SQL. This file holds
// exactly one raw-SQL report so its assertion fails when that report is
// absent.
package imageserve_test

import "testing"

func real(t *testing.T) {
	// A genuine testing receiver: this diagnostic is exempt, not SQL.
	t.Fatalf("select backdrop index 1: %v", nil)
}

type fakeT struct{}

func (fakeT) Fatalf(format string, args ...any) {}

func fake(t fakeT) {
	// Same name, different type: t is fakeT, not *testing.T. The exemption
	// must not apply, so this SQL-shaped string must be rejected.
	t.Fatalf("SELECT * FROM users")
}
