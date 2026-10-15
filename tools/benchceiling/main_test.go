// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ceilingsFile = `# comment
BenchmarkScale/sqlite/page 6ms
BenchmarkScale/postgres/page 50ms
`

func runCheck(t *testing.T, ceilings, output string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ceilings.txt")
	if err := os.WriteFile(path, []byte(ceilings), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run([]string{"-ceilings", path}, strings.NewReader(output), &out)
	return out.String(), err
}

func TestBenchCeilingPassesUnderCeiling(t *testing.T) {
	out, err := runCheck(t, ceilingsFile, `goos: linux
BenchmarkScale/sqlite/page-16     	      20	    597169 ns/op
BenchmarkScale/postgres/page-16   	      20	   5246994 ns/op
PASS
`)
	if err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	for _, want := range []string{"| BenchmarkScale/sqlite/page | 597µs | 6ms | 10% | ok |", "| BenchmarkScale/postgres/page | 5.247ms | 50ms | 10% | ok |"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
}

func TestBenchCeilingFailures(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
	}{
		{"over", "BenchmarkScale/sqlite/page-16 20 7000000 ns/op\nBenchmarkScale/postgres/page-16 20 1 ns/op\n", "exceeds the ceiling 6ms"},
		{"slowest repeat counts", "BenchmarkScale/sqlite/page-16 20 1 ns/op\nBenchmarkScale/sqlite/page-16 20 7000000 ns/op\nBenchmarkScale/postgres/page-16 20 1 ns/op\n", "exceeds the ceiling 6ms"},
		{"missing", "BenchmarkScale/sqlite/page-16 20 1 ns/op\n", "BenchmarkScale/postgres/page: no result"},
		{"unlisted", "BenchmarkScale/sqlite/page-16 20 1 ns/op\nBenchmarkScale/postgres/page-16 20 1 ns/op\nBenchmarkScale/sqlite/new-16 20 1 ns/op\n", "BenchmarkScale/sqlite/new: no ceiling"},
		{"run failure", "BenchmarkScale/sqlite/page-16 20 1 ns/op\nBenchmarkScale/postgres/page-16 20 1 ns/op\n--- FAIL: BenchmarkScale/other\nFAIL\n", "reported a failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runCheck(t, ceilingsFile, tc.output)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q\n%s", err, tc.want, out)
			}
		})
	}
}

func TestBenchCeilingRejectsBadCeilings(t *testing.T) {
	for _, ceilings := range []string{"BenchmarkX\n", "BenchmarkX soon\n", "BenchmarkX 0s\n", "BenchmarkX 1ms\nBenchmarkX 2ms\n"} {
		if _, err := runCheck(t, ceilings, ""); err == nil {
			t.Errorf("ceilings %q: want an error", ceilings)
		}
	}
}

// TestScaleBenchCeilingsParse keeps the checked-in ceilings file readable.
func TestScaleBenchCeilingsParse(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "internal", "store", "storetest", "testdata", "scale-bench-ceilings.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	_, order, err := parseCeilings(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) == 0 {
		t.Fatal("no ceilings")
	}
}
