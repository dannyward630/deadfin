// SPDX-License-Identifier: GPL-2.0-only

// Package imageserve_test is a prose fixture for
// verify-convention-gate-fixtures. Every string below is a direct test
// diagnostic. Each is the first argument to a diagnostic method on a
// receiver declared as *testing.T or testing.TB. The methods are Error,
// Errorf, Fatal, Fatalf, Log, Logf, Skip, and Skipf.
//
// Some begin with a SQL keyword yet are not statements. The gate must
// accept them without weakening its detection of real SQL outside
// internal/store. These mirror the three diagnostic forms in
// internal/imageserve/service_test.go (Fatalf once, Errorf twice) plus
// the remaining direct-diagnostic controls.
package imageserve_test

import "testing"

func TestDiagnosticsAccepted(t *testing.T) {
	// The three internal/imageserve/service_test.go forms, verbatim in shape.
	t.Fatalf("Select backdrop index 1: %v", nil)
	t.Errorf("Select missing type error = %v, want ErrImageNotFound", nil)
	t.Errorf("Select missing index error = %v, want ErrImageNotFound", nil)

	// Every advertised direct-diagnostic method accepts SQL-looking prose.
	t.Error("select all the rows")
	t.Errorf("select %q from cache", "x")
	t.Fatal("delete from cache")
	t.Fatalf("update items set name = %q", "x")
	t.Log("insert into queue")
	t.Logf("create table %s", "t")
	t.Skip("drop table orphan")
	t.Skipf("alter table %s add column c", "t")
}

func helperTB(tb testing.TB) {
	// A testing.TB receiver (non-pointer) is recognized too.
	tb.Errorf("select backdrop %d", 1)
	tb.Fatalf("create view root: %v", nil)
}

func subtest(t *testing.T) {
	// A receiver declared inside a t.Run closure must be recognized.
	t.Run("nested", func(t *testing.T) {
		t.Fatalf("select backdrop index %d: %v", 1, nil)
	})
}
