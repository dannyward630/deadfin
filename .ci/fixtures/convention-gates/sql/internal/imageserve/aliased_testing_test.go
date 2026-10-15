// SPDX-License-Identifier: GPL-2.0-only

// Package imageserve_test is an isolated negative-control fixture for
// verify-convention-gate-fixtures. It guards the import-path boundary of the
// testing-diagnostic exemption. A fake package aliased as "testing" must not
// receive the exemption. The import path behind the identifier must be
// "testing", not a fake package. Here the import is
// `testing "example.com/fake"`. The SQL-shaped Fatalf must be rejected as raw
// SQL. This file holds exactly one raw-SQL report so its assertion fails when
// that report is absent.
package imageserve_test

import (
	testing "example.com/fake"
)

// aliasedTesting uses a fake package aliased as testing. The type
// testing.T refers to example.com/fake.T, not the real testing package.
// The exemption must not apply, so the SQL-shaped string must be rejected.
func aliasedTesting(t testing.T) {
	t.Fatalf("SELECT * FROM users")
}
