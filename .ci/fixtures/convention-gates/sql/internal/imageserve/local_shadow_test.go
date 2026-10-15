// SPDX-License-Identifier: GPL-2.0-only

// Package imageserve_test is an isolated negative-control fixture for
// verify-convention-gate-fixtures. It guards the lexical-block shadowing
// boundary of the testing-diagnostic exemption. An outer testing receiver
// named t must not be exempted inside a block where a local non-testing
// variable of the same name shadows it. The function takes t as *testing.T.
// Inside a block, t := fakeT{} shadows it. The SQL-shaped Fatalf must be
// rejected as raw SQL. This file holds exactly one raw-SQL report so its
// assertion fails when that report is absent.
package imageserve_test

import "testing"

type fakeShadow struct{}

func (fakeShadow) Fatalf(format string, args ...any) {}

// localShadow has an outer *testing.T named t. Inside the block, a local
// fakeShadow shadows it. The inner t is not a testing receiver.
func localShadow(t *testing.T) {
	{
		t := fakeShadow{}
		// t is fakeShadow here, not *testing.T. The exemption must not
		// apply, so this SQL-shaped string must be rejected.
		t.Fatalf("SELECT * FROM users")
	}
}
