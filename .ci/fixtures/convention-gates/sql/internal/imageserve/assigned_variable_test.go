// SPDX-License-Identifier: GPL-2.0-only

// Package imageserve_test is an isolated negative-control fixture for
// verify-convention-gate-fixtures. The string below is SQL-shaped but is not a
// direct diagnostic: it is assigned to a named constant. The testing-diagnostic
// exemption must not cover it, so the gate must reject it as raw SQL. This file
// holds exactly one raw-SQL report so its assertion fails when that report is
// absent.
package imageserve_test

// assignedVariable is SQL assigned to a named constant. It is not a
// direct diagnostic, so the gate must reject it.
const assignedVariable = "SELECT 1"
