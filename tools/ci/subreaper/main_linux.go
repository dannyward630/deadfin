// SPDX-License-Identifier: GPL-2.0-only

// Command subreaper runs a command with orphan reaping, for CI runners whose
// PID 1 is not an init.
//
// Why this exists. The jailed Actions runner starts its job container with
// `/bin/sleep 10800` as PID 1. A sleep never calls wait(), so any process
// orphaned during a job reparents to it and stays a zombie for the life of the
// job — measured on a self-hosted runner, where `git` and `go` from earlier
// steps were already zombies before any test ran.
//
// That breaks the internal/ffmpeg reaping tests, which decide a process is
// gone with syscall.Kill(pid, 0) == ESRCH (see waitForProcessExit in
// internal/ffmpeg/prober_test.go). kill(pid, 0) SUCCEEDS against a zombie: the
// PID stays in the table until someone collects its exit status. The tests
// were therefore reporting "process N survived cleanup" about a corpse.
// Deadfin's own cleanup is correct — the group leader is reaped and every
// survivor is in state Z — so the fix belongs in the job, not in
// internal/ffmpeg, and emphatically not in relaxing the tests to tolerate
// zombies.
//
// What it does: sets PR_SET_CHILD_SUBREAPER so orphaned descendants reparent
// to this process rather than to PID 1, then reaps them. Both halves are
// required. Setting the flag alone only changes *where* the corpses pile up —
// without the wait() loop below they are still zombies and the tests still
// fail, just parented somewhere else.
//
// A CI runner whose nspawn guest runs systemd as PID 1 reaps orphans itself;
// the subreaper is harmless there.
//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// PR_SET_CHILD_SUBREAPER. Not exposed by the syscall package.
const prSetChildSubreaper = 36

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: subreaper <command> [args...]")
		os.Exit(2)
	}

	// Register for SIGCHLD before starting the child, so an exit that happens
	// immediately cannot be missed.
	sigchld := make(chan os.Signal, 128)
	signal.Notify(sigchld, syscall.SIGCHLD)

	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		// Not fatal. On a runner that already has a real init this changes
		// nothing, and failing the job here would hide the actual test result.
		fmt.Fprintf(os.Stderr, "subreaper: PR_SET_CHILD_SUBREAPER failed: %v (continuing)\n", errno)
	}

	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "subreaper: %v\n", err)
		os.Exit(127)
	}
	target := cmd.Process.Pid

	// Reap everything, not just the target: orphans are the entire point. Note
	// cmd.Wait() is deliberately never called — it would race this loop for the
	// target's exit status, and whichever lost would report an error.
	orphans := 0
	for range sigchld {
		for {
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if err == syscall.EINTR {
				continue
			}
			// pid 0 = children exist but none have exited yet; -1 with ECHILD =
			// none left. Either way, wait for the next SIGCHLD.
			if pid <= 0 || err != nil {
				break
			}
			if pid != target {
				orphans++
				continue
			}
			if orphans > 0 {
				fmt.Fprintf(os.Stderr, "subreaper: reaped %d orphan(s)\n", orphans)
			}
			switch {
			case status.Signaled():
				os.Exit(128 + int(status.Signal()))
			default:
				os.Exit(status.ExitStatus())
			}
		}
	}
}
