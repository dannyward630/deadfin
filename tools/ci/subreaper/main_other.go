// SPDX-License-Identifier: GPL-2.0-only

//go:build !linux

package main

import (
	"fmt"
	"os"
)

// PR_SET_CHILD_SUBREAPER is Linux-only, and so is the CI runner this wrapper
// exists for. This stub keeps `go build ./...` and `go vet ./...` working on a
// developer Darwin checkout: without a file for the non-Linux case, the
// package has no buildable sources and ./... fails to load.
func main() {
	fmt.Fprintln(os.Stderr, "subreaper: Linux-only (PR_SET_CHILD_SUBREAPER); run the command directly on this platform")
	os.Exit(1)
}
