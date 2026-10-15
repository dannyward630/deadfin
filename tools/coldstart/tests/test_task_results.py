#!/usr/bin/env python3
"""tools/coldstart/tests/test_task_results.py

Focused coverage for the RefreshLibrary task-result assertion and the
two-phase runner contract.

The cold-start gate must read GET /ScheduledTasks, select exactly one task
whose Key equals RefreshLibrary, and assert that LastExecutionResult.Status
is Completed with an empty ErrorMessage on the positive run. The negative
run records Failed with a nonempty ErrorMessage, and the gate exits nonzero
on the success-status assertion.

These tests cover:

  1. Task selection: zero matches, two or more matches, and one match.
  2. Malformed responses: a non-array body, a non-object task entry, and a
     LastExecutionResult that is not an object.
  3. Stale results: the wait function rejects a result with the same
     StartTimeUtc and EndTimeUtc as the prior snapshot.
  4. Timeout: the wait function fails when no new result arrives.
  5. Success: a Completed result with an empty error passes.
  6. Failure: a Failed result with a nonempty error fails the
     success-status assertion.
  7. Two-phase runner: the phase argument selects configure, scan, or both.
  8. Negative runner subprocess: run.sh executes the negative branch
     through fake command seams. The fake scan fails, and run.sh returns
     nonzero without swallowing the failure.
"""

import importlib.util
import io
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

# Load verify.py as a module without running main().
verify_path = os.path.join(os.path.dirname(__file__), "..", "verify.py")
spec = importlib.util.spec_from_file_location("coldstart_verify_task", verify_path)
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class TaskClient:
    """A mock client whose get_json returns scripted /ScheduledTasks responses.

    The mock supplies a fixed list of scheduled tasks. Each task is a dict
    with a Key, State, and LastExecutionResult. The _index attribute tracks
    which /ScheduledTasks call is in progress so the mock can return different
    results across consecutive polls.
    """

    def __init__(self, tasks_sequences):
        """Store a list of task-list snapshots to return in order.

        tasks_sequences is a list of lists. Each inner list is one
        /ScheduledTasks response. The mock returns each inner list in turn.
        """
        self._sequences = list(tasks_sequences)
        self._idx = 0

    def get_json(self, path, expected=200, auth=True):
        if path == "/ScheduledTasks":
            if self._idx < len(self._sequences):
                result = self._sequences[self._idx]
                self._idx += 1
                return result
            # After the scripted sequence, keep returning the last snapshot.
            return self._sequences[-1] if self._sequences else []
        raise AssertionError(f"unexpected path: {path}")


def make_task(key="RefreshLibrary", state="Idle", result=None):
    """Build one scheduled task dict in the pinned wire shape."""
    task = {"Key": key, "State": state}
    if result is not None:
        task["LastExecutionResult"] = result
    return task


def make_result(status="Completed", error="", start="2026-01-01T00:00:00Z",
                end="2026-01-01T00:01:00Z"):
    """Build one LastExecutionResult dict."""
    return {
        "StartTimeUtc": start,
        "EndTimeUtc": end,
        "Status": status,
        "ErrorMessage": error,
    }


class TestTaskSelection(unittest.TestCase):
    """select_refresh_task must find exactly one RefreshLibrary task."""

    def test_one_match_returns_task(self):
        tasks = [
            make_task(key="RefreshPeople"),
            make_task(key="RefreshLibrary"),
            make_task(key="OptimizeDatabaseTask"),
        ]
        task = verify.select_refresh_task(tasks)
        self.assertEqual(task["Key"], "RefreshLibrary")

    def test_zero_matches_fails(self):
        tasks = [
            make_task(key="RefreshPeople"),
            make_task(key="OptimizeDatabaseTask"),
        ]
        with self.assertRaises(SystemExit):
            verify.select_refresh_task(tasks)

    def test_two_matches_fails(self):
        tasks = [
            make_task(key="RefreshLibrary"),
            make_task(key="RefreshLibrary"),
        ]
        with self.assertRaises(SystemExit):
            verify.select_refresh_task(tasks)

    def test_non_object_task_entry_fails(self):
        tasks = ["not a dict"]
        with self.assertRaises(SystemExit):
            verify.select_refresh_task(tasks)


class TestFetchScheduledTasks(unittest.TestCase):
    """fetch_scheduled_tasks must reject a non-array body."""

    def test_non_array_body_fails(self):
        client = TaskClient([{"not": "an array"}])
        with self.assertRaises(SystemExit):
            verify.fetch_scheduled_tasks(client)


class TestCapturePriorTaskResult(unittest.TestCase):
    """capture_prior_task_result reads the current result before refresh."""

    def setUp(self):
        self._real_sleep = verify.time.sleep
        verify.time.sleep = lambda _s: None

    def tearDown(self):
        verify.time.sleep = self._real_sleep

    def test_returns_result_when_present(self):
        result = make_result(status="Completed")
        tasks = [make_task(result=result)]
        client = TaskClient([tasks])
        prior = verify.capture_prior_task_result(client)
        self.assertEqual(prior["Status"], "Completed")

    def test_returns_none_when_absent(self):
        tasks = [make_task(result=None)]
        client = TaskClient([tasks])
        prior = verify.capture_prior_task_result(client)
        self.assertIsNone(prior)

    def test_non_object_result_fails(self):
        tasks = [make_task(result="not a dict")]
        client = TaskClient([tasks])
        with self.assertRaises(SystemExit):
            verify.capture_prior_task_result(client)

    def test_missing_start_timestamp_fails(self):
        """A prior result without StartTimeUtc is malformed."""
        result = {"EndTimeUtc": "2026-01-01T00:01:00Z", "Status": "Completed", "ErrorMessage": ""}
        tasks = [make_task(result=result)]
        client = TaskClient([tasks])
        with self.assertRaises(SystemExit):
            verify.capture_prior_task_result(client)

    def test_missing_end_timestamp_fails(self):
        """A prior result without EndTimeUtc is malformed."""
        result = {"StartTimeUtc": "2026-01-01T00:00:00Z", "Status": "Completed", "ErrorMessage": ""}
        tasks = [make_task(result=result)]
        client = TaskClient([tasks])
        with self.assertRaises(SystemExit):
            verify.capture_prior_task_result(client)

    def test_empty_start_timestamp_fails(self):
        """A prior result with an empty StartTimeUtc is malformed."""
        result = make_result(start="", end="2026-01-01T00:01:00Z")
        tasks = [make_task(result=result)]
        client = TaskClient([tasks])
        with self.assertRaises(SystemExit):
            verify.capture_prior_task_result(client)

    def test_non_string_start_timestamp_fails(self):
        """A prior result with a non-string StartTimeUtc is malformed."""
        result = {"StartTimeUtc": 12345, "EndTimeUtc": "2026-01-01T00:01:00Z",
                  "Status": "Completed", "ErrorMessage": ""}
        tasks = [make_task(result=result)]
        client = TaskClient([tasks])
        with self.assertRaises(SystemExit):
            verify.capture_prior_task_result(client)


class TestWaitForTaskResult(unittest.TestCase):
    """wait_for_task_result must reject stale results and timeout."""

    def setUp(self):
        self._real_sleep = verify.time.sleep
        verify.time.sleep = lambda _s: None
        self._real_timeout = verify.TASK_RESULT_TIMEOUT
        self._real_monotonic = verify.time.monotonic

    def tearDown(self):
        verify.time.sleep = self._real_sleep
        verify.TASK_RESULT_TIMEOUT = self._real_timeout
        verify.time.monotonic = self._real_monotonic

    def test_new_result_returns(self):
        prior = make_result(status="Completed", start="old-start", end="old-end")
        new_result = make_result(status="Completed", start="new-start", end="new-end")
        tasks = [make_task(state="Idle", result=new_result)]
        client = TaskClient([tasks])
        got = verify.wait_for_task_result(client, prior)
        self.assertEqual(got["StartTimeUtc"], "new-start")

    def test_stale_result_rejected_then_new_returns(self):
        prior = make_result(status="Completed", start="same-start", end="same-end")
        stale = make_result(status="Completed", start="same-start", end="same-end")
        fresh = make_result(status="Completed", start="fresh-start", end="fresh-end")
        tasks_stale = [make_task(state="Idle", result=stale)]
        tasks_fresh = [make_task(state="Idle", result=fresh)]
        client = TaskClient([tasks_stale, tasks_fresh])
        got = verify.wait_for_task_result(client, prior)
        self.assertEqual(got["StartTimeUtc"], "fresh-start")

    def test_stale_result_with_running_state_rejected(self):
        """A result with new timestamps but Running state must not return."""
        prior = make_result(status="Completed", start="old", end="old")
        new_running = make_result(status="Running", start="new", end="new")
        tasks = [make_task(state="Running", result=new_running)]
        client = TaskClient([tasks])
        # The poll returns Running and the timeout fires.
        clock = [0]
        def advancing():
            clock[0] += 1
            return clock[0]
        verify.time.monotonic = advancing
        verify.TASK_RESULT_TIMEOUT = 3
        with self.assertRaises(SystemExit):
            verify.wait_for_task_result(client, prior)

    def test_timeout_when_result_never_changes(self):
        prior = make_result(status="Completed", start="same", end="same")
        same = make_result(status="Completed", start="same", end="same")
        tasks = [make_task(state="Idle", result=same)]
        client = TaskClient([tasks])
        # Advance the clock past the timeout on every call.
        clock = [0]
        def advancing():
            clock[0] += 10
            return clock[0]
        verify.time.monotonic = advancing
        verify.TASK_RESULT_TIMEOUT = 5
        with self.assertRaises(SystemExit):
            verify.wait_for_task_result(client, prior)

    def test_none_prior_with_new_result_returns(self):
        """A None prior (no prior result) must accept any result."""
        new_result = make_result(status="Completed", start="start", end="end")
        tasks = [make_task(state="Idle", result=new_result)]
        client = TaskClient([tasks])
        got = verify.wait_for_task_result(client, None)
        self.assertEqual(got["Status"], "Completed")

    def test_none_prior_with_no_result_times_out(self):
        """A None prior with no result at all must time out."""
        tasks = [make_task(state="Idle", result=None)]
        client = TaskClient([tasks])
        clock = [0]
        def advancing():
            clock[0] += 10
            return clock[0]
        verify.time.monotonic = advancing
        verify.TASK_RESULT_TIMEOUT = 5
        with self.assertRaises(SystemExit):
            verify.wait_for_task_result(client, None)

    def test_missing_timestamp_passes_freshness_rejected(self):
        """A result without StartTimeUtc must not pass freshness."""
        prior = make_result(status="Completed", start="old", end="old")
        malformed = {"EndTimeUtc": "new", "Status": "Completed", "ErrorMessage": ""}
        tasks = [make_task(state="Idle", result=malformed)]
        client = TaskClient([tasks])
        with self.assertRaises(SystemExit):
            verify.wait_for_task_result(client, prior)

    def test_missing_end_timestamp_in_result_rejected(self):
        """A result without EndTimeUtc must not pass freshness."""
        prior = make_result(status="Completed", start="old", end="old")
        malformed = {"StartTimeUtc": "new", "Status": "Completed", "ErrorMessage": ""}
        tasks = [make_task(state="Idle", result=malformed)]
        client = TaskClient([tasks])
        with self.assertRaises(SystemExit):
            verify.wait_for_task_result(client, prior)

    def test_empty_timestamp_in_result_rejected(self):
        """A result with an empty StartTimeUtc must not pass freshness."""
        prior = make_result(status="Completed", start="old", end="old")
        malformed = make_result(start="", end="new")
        tasks = [make_task(state="Idle", result=malformed)]
        client = TaskClient([tasks])
        with self.assertRaises(SystemExit):
            verify.wait_for_task_result(client, prior)

    def test_non_string_timestamp_in_result_rejected(self):
        """A result with a non-string EndTimeUtc must not pass freshness."""
        prior = make_result(status="Completed", start="old", end="old")
        malformed = {"StartTimeUtc": "new", "EndTimeUtc": 12345,
                      "Status": "Completed", "ErrorMessage": ""}
        tasks = [make_task(state="Idle", result=malformed)]
        client = TaskClient([tasks])
        with self.assertRaises(SystemExit):
            verify.wait_for_task_result(client, prior)


class TestAssertTaskSuccess(unittest.TestCase):
    """assert_task_success must require Completed with an empty error."""

    def test_completed_empty_error_passes(self):
        result = make_result(status="Completed", error="")
        verify.assert_task_success(result)

    def test_failed_nonempty_error_fails(self):
        result = make_result(status="Failed", error="scan failed")
        with self.assertRaises(SystemExit):
            verify.assert_task_success(result)

    def test_failed_empty_error_fails(self):
        result = make_result(status="Failed", error="")
        with self.assertRaises(SystemExit):
            verify.assert_task_success(result)

    def test_completed_nonempty_error_fails(self):
        result = make_result(status="Completed", error="unexpected error")
        with self.assertRaises(SystemExit):
            verify.assert_task_success(result)

    def test_unknown_status_fails(self):
        result = make_result(status="Cancelling", error="")
        with self.assertRaises(SystemExit):
            verify.assert_task_success(result)

    def test_missing_status_fails(self):
        result = {"ErrorMessage": ""}
        with self.assertRaises(SystemExit):
            verify.assert_task_success(result)

    def test_non_string_status_fails(self):
        result = {"Status": 42, "ErrorMessage": ""}
        with self.assertRaises(SystemExit):
            verify.assert_task_success(result)

    def test_non_string_error_fails(self):
        result = {"Status": "Completed", "ErrorMessage": 42}
        with self.assertRaises(SystemExit):
            verify.assert_task_success(result)


class TestWriteSummaryTaskResult(unittest.TestCase):
    """write_summary must print the task status and error."""

    def setUp(self):
        self._real_stderr = sys.stderr
        self._real_stdout = sys.stdout
        sys.stderr = io.StringIO()
        sys.stdout = io.StringIO()

    def tearDown(self):
        sys.stderr = self._real_stderr
        sys.stdout = self._real_stdout

    def test_summary_includes_task_status_and_error(self):
        states = {
            "Golden Movies": "Idle",
            "Golden TV": "Idle",
            "Golden Music": "Idle",
            "Golden Photos": "Idle",
        }
        task_result = make_result(status="Completed", error="")
        verify.write_summary("sha256:abc", "deadbeef", 42, states, task_result)
        output = sys.stdout.getvalue()
        self.assertIn("RefreshLibrary Status: Completed", output)
        self.assertIn("RefreshLibrary ErrorMessage: ''", output)

    def test_summary_shows_failed_status_and_error(self):
        states = {
            "Golden Movies": "Idle",
            "Golden TV": "Idle",
            "Golden Music": "Idle",
            "Golden Photos": "Idle",
        }
        task_result = make_result(status="Failed", error="scan failed: missing source")
        verify.write_summary("sha256:abc", "", 48, states, task_result)
        output = sys.stdout.getvalue()
        self.assertIn("RefreshLibrary Status: Failed", output)
        self.assertIn("RefreshLibrary ErrorMessage: 'scan failed: missing source'", output)


class TestTwoPhaseRunner(unittest.TestCase):
    """The phase argument must select configure, scan, or both.

    These tests drive main() through a mock that intercepts the phase
    dispatch. They prove the two-phase contract without Docker.
    """

    def _run_main_with_mock(self, phase):
        """Run main() with run_configuration and run_scan replaced.

        Return a list of phase names in call order. Each entry is
        "configure" or "scan". The list proves the two-phase contract.
        """
        calls = []
        old_argv = sys.argv
        old_stderr = sys.stderr
        old_config = verify.run_configuration
        old_scan = verify.run_scan
        sys.argv = ["verify.py", "http://localhost:8096", "container", "label",
                    "policy.json", phase]
        sys.stderr = io.StringIO()
        verify.run_configuration = lambda _url, _path: calls.append("configure")
        verify.run_scan = lambda _url, _path, _d, _c: calls.append("scan")
        try:
            verify.main()
        finally:
            sys.argv = old_argv
            sys.stderr = old_stderr
            verify.run_configuration = old_config
            verify.run_scan = old_scan
        return calls

    def test_unknown_phase_fails(self):
        """An unknown phase value must fail before any network call."""
        old_argv = sys.argv
        old_stderr = sys.stderr
        sys.argv = ["verify.py", "http://localhost:8096", "container", "label",
                    "policy.json", "invalid-phase"]
        sys.stderr = io.StringIO()
        try:
            with self.assertRaises(SystemExit):
                verify.main()
        finally:
            sys.argv = old_argv
            sys.stderr = old_stderr

    def test_configure_phase_calls_only_configure(self):
        """The configure phase must call run_configuration and not run_scan."""
        calls = self._run_main_with_mock("configure")
        self.assertEqual(calls, ["configure"])

    def test_scan_phase_calls_only_scan(self):
        """The scan phase must call run_scan and not run_configuration."""
        calls = self._run_main_with_mock("scan")
        self.assertEqual(calls, ["scan"])

    def test_both_phase_calls_configure_then_scan(self):
        """The both phase must call run_configuration before run_scan."""
        calls = self._run_main_with_mock("both")
        self.assertEqual(calls, ["configure", "scan"])

    def test_default_phase_is_both(self):
        """When no phase argument is given, main() must run both phases."""
        calls = []
        old_argv = sys.argv
        old_stderr = sys.stderr
        old_config = verify.run_configuration
        old_scan = verify.run_scan
        sys.argv = ["verify.py", "http://localhost:8096", "container", "label",
                    "policy.json"]
        sys.stderr = io.StringIO()
        verify.run_configuration = lambda _url, _path: calls.append("configure")
        verify.run_scan = lambda _url, _path, _d, _c: calls.append("scan")
        try:
            verify.main()
        finally:
            sys.argv = old_argv
            sys.stderr = old_stderr
            verify.run_configuration = old_config
            verify.run_scan = old_scan
        self.assertEqual(calls, ["configure", "scan"])

class TestNegativeRunnerSubprocess(unittest.TestCase):
    """Execute the negative branch of run.sh through fake command seams.

    This test runs run.sh as a real subprocess with deterministic fake
    binaries for docker, python3, tar, sha256sum, and find. The fake
    python3 logs each verify.py invocation and exits zero for the
    configure phase and nonzero for the scan phase. The test asserts that
    run.sh returns nonzero and does not swallow the scan failure.

    The fake docker logs each invocation so the test can assert the exact
    command order: configure, remove /media/photos, source probes, scan.
    """

    def setUp(self):
        self._repo_root = Path(__file__).resolve().parents[3]
        self._tmpdir = tempfile.TemporaryDirectory(
            prefix="coldstart-negative-runner-")
        self._work = Path(self._tmpdir.name)
        self._fake_bin = self._work / "bin"
        self._fake_bin.mkdir()
        self._log = self._work / "command-log"
        self._log.touch()
        # Copy run.sh to a temp directory structure so repo_root resolves
        # to the temp dir. This isolates the test from the real fixture
        # tree, which is absent on Apple Silicon.
        self._coldstart_dir = self._work / "tools" / "coldstart"
        self._coldstart_dir.mkdir(parents=True)
        self._run_sh = self._coldstart_dir / "run.sh"
        shutil.copy2(
            self._repo_root / "tools" / "coldstart" / "run.sh",
            self._run_sh)
        self._install_fake_fixture_tree()
        self._install_fake_docker()
        self._install_fake_python3()
        self._install_fake_tar()
        self._install_fake_sha256sum()
        self._install_fake_find()

    def tearDown(self):
        self._tmpdir.cleanup()

    def _install_fake_fixture_tree(self):
        """Create the fixture tree that require_fixture_tree checks."""
        fixtures = self._work / "tools" / "goldenrec" / "fixtures"
        fixtures.mkdir(parents=True)
        out = fixtures / "out"
        out.mkdir()
        for name in ("movies", "tv", "music", "photos"):
            (out / name).mkdir()
        manifest = fixtures / "source-manifest.sha256"
        # Write one manifest entry so manifest_count equals actual_count.
        manifest.write_text(
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  "
            "fake-file\n")

    def _install_fake_docker(self):
        """Install a fake docker that simulates the full container lifecycle.

        The fake handles every docker command that run.sh issues: network
        create, network inspect (Internal, Networks), volume create, run -d,
        inspect (State.Running, Id, IPAddress, Networks), exec (stat, tar,
        rm, test, alive check), rm, ps, network ls/rm, volume ls/rm. Each
        exec that the negative branch depends on logs a marker token to the
        shared command log.
        """
        docker = self._fake_bin / "docker"
        log = self._log
        docker.write_text(
            "#!/bin/sh\n"
            "set -eu\n"
            f'log="{log}"\n'
            'cmd="${1-}"\n'
            '[ $# -gt 0 ] && shift\n'
            'case "$cmd" in\n'
            '  network)\n'
            '    sub="${1-}"\n'
            '    [ $# -gt 0 ] && shift\n'
            '    case "$sub" in\n'
            '      create) exit 0 ;;\n'
            '      inspect)\n'
            '        if printf "%s" "$*" | grep -q "Internal"; then\n'
            '          printf "true\\n"; exit 0\n'
            '        fi\n'
            '        if printf "%s" "$*" | grep -q "IPAddress"; then\n'
            '          printf "10.0.0.2\\n"; exit 0\n'
            '        fi\n'
            '        if printf "%s" "$*" | grep -q "State.Running"; then\n'
            '          printf "true\\n"; exit 0\n'
            '        fi\n'
            '        if printf "%s" "$*" | grep -q "Id}}"; then\n'
            '          printf "%s\\n" "$(printf \'%064d\' 0)"; exit 0\n'
            '        fi\n'
            '        if printf "%s" "$*" | grep -q "Networks}}"; then\n'
            '          printf "deadfin-coldstart-net\\n"; exit 0\n'
            '        fi\n'
            '        printf "\\n"; exit 0 ;;\n'
            '      ls) exit 0 ;;\n'
            '      rm) exit 0 ;;\n'
            '    esac\n'
            '    ;;\n'
            '  volume)\n'
            '    sub="${1-}"\n'
            '    [ $# -gt 0 ] && shift\n'
            '    case "$sub" in\n'
            '      create) exit 0 ;;\n'
            '      ls) exit 0 ;;\n'
            '      rm) exit 0 ;;\n'
            '    esac\n'
            '    ;;\n'
            '  inspect)\n'
            '    if printf "%s" "$*" | grep -q "State.Running"; then\n'
            '      printf "true\\n"; exit 0\n'
            '    fi\n'
            '    if printf "%s" "$*" | grep -q "Id}}"; then\n'
            '      printf "%s\\n" "$(printf \'%064d\' 0)"; exit 0\n'
            '    fi\n'
            '    if printf "%s" "$*" | grep -q "IPAddress"; then\n'
            '      printf "10.0.0.2\\n"; exit 0\n'
            '    fi\n'
            '    if printf "%s" "$*" | grep -q "Networks}}"; then\n'
            '      printf "deadfin-coldstart-net\\n"; exit 0\n'
            '    fi\n'
            '    printf "\\n"; exit 0 ;;\n'
            '  run) printf "%s\\n" "$(printf \'%064d\' 0)"; exit 0 ;;\n'
            '  exec)\n'
            '    # Skip the container name argument.\n'
            '    [ $# -gt 0 ] && shift\n'
            '    # Skip -i if present (used for tar streaming).\n'
            '    if [ "${1-}" = "-i" ]; then shift; fi\n'
            '    if [ "${1-}" = "/usr/bin/tar" ]; then exit 0; fi\n'
            '    if [ "${1-}" = "/usr/bin/stat" ]; then\n'
            '      printf "65532:65532 drwx------\\n"; exit 0\n'
            '    fi\n'
            '    if [ "${1-}" = "/bin/sh" ]; then\n'
            '      shift\n'
            '      if [ "${1-}" = "-c" ]; then shift; fi\n'
            '      script="${1-}"\n'
            '      if printf "%s" "$script" | grep -q "rm -rf /media/photos"; then\n'
            '        printf "rm-photos\\0" >> "$log"; exit 0\n'
            '      fi\n'
            '      if printf "%s" "$script" | grep -q "test -d /media/photos"; then\n'
            '        printf "probe-absent-photos\\0" >> "$log"; exit 1\n'
            '      fi\n'
            '      if printf "%s" "$script" | grep -q "test -d /media/movies"; then\n'
            '        printf "probe-source-movies\\0" >> "$log"; exit 0\n'
            '      fi\n'
            '      if printf "%s" "$script" | grep -q "test -d /media/tv"; then\n'
            '        printf "probe-source-tv\\0" >> "$log"; exit 0\n'
            '      fi\n'
            '      if printf "%s" "$script" | grep -q "test -d /media/music"; then\n'
            '        printf "probe-source-music\\0" >> "$log"; exit 0\n'
            '      fi\n'
            '      printf "alive\\n"; exit 0\n'
            '    fi\n'
            '    exit 0\n'
            '    ;;\n'
            '  rm) exit 0 ;;\n'
            '  ps) exit 0 ;;\n'
            'esac\n'
            'exit 0\n'
        )
        docker.chmod(0o755)

    def _install_fake_python3(self):
        """Install a fake python3 that simulates verify.py.

        The fake reads the last argument (the phase) and exits zero for
        configure and nonzero for scan. Each invocation is logged with the
        phase name.
        """
        python3 = self._fake_bin / "python3"
        log = self._log
        python3.write_text(
            "#!/bin/sh\n"
            "set -eu\n"
            f'log="{log}"\n'
            '# The script path is the first argument; the phase is the last.\n'
            'for phase do :; done\n'
            'printf \'python3\\0%s\\0\' "$phase" >> "$log"\n'
            'if [ "$phase" = "configure" ]; then exit 0; fi\n'
            'if [ "$phase" = "scan" ]; then\n'
            "  echo \"cold-start verify: RefreshLibrary success-status \"\n"
            "  echo \"assertion: Status is 'Failed', expected 'Completed'\" >&2\n"
            '  exit 1\n'
            'fi\n'
            '# both phase: configure succeeds, scan fails.\n'
            'exit 1\n'
        )
        python3.chmod(0o755)

    def _install_fake_tar(self):
        """Install a fake tar that streams no bytes."""
        tar = self._fake_bin / "tar"
        tar.write_text("#!/bin/sh\nexit 0\n")
        tar.chmod(0o755)

    def _install_fake_sha256sum(self):
        """Install a fake sha256sum that accepts the manifest."""
        sha256sum = self._fake_bin / "sha256sum"
        sha256sum.write_text("#!/bin/sh\nexit 0\n")
        sha256sum.chmod(0o755)

    def _install_fake_find(self):
        """Install a fake find that reports one file.

        The count must match the one manifest entry so the staged-file
        count check passes.
        """
        find = self._fake_bin / "find"
        find.write_text("#!/bin/sh\nprintf 'fake-file\\n'\nexit 0\n")
        find.chmod(0o755)

    def _run_negative(self):
        """Run run.sh with CONTROL=negative as a subprocess."""
        env = os.environ.copy()
        env["PATH"] = str(self._fake_bin) + os.pathsep + env["PATH"]
        env["DOCKER"] = str(self._fake_bin / "docker")
        env["IMAGE_REFERENCE"] = "fake-image:latest"
        env["IMAGE_DIGEST"] = "sha256:" + "0" * 64
        env["CONTROL"] = "negative"
        result = subprocess.run(
            ["/bin/sh", str(self._run_sh)],
            capture_output=True,
            text=True,
            env=env,
            timeout=30,
        )
        return result

    def _read_log(self):
        """Read the command log and return a flat list of NUL-delimited tokens."""
        data = self._log.read_bytes()
        tokens = []
        for chunk in data.split(b"\x00"):
            if chunk:
                tokens.append(chunk.decode("utf-8", "replace"))
        return tokens

    @staticmethod
    def _first_index(tokens, value):
        """Return the index of the first occurrence of value in tokens."""
        for i, token in enumerate(tokens):
            if token == value:
                return i
        return -1

    def test_negative_runner_returns_nonzero(self):
        """run.sh must return nonzero when the scan phase fails.

        The fake scan exits nonzero. run.sh must not swallow that failure.
        The exit code must be nonzero.
        """
        result = self._run_negative()
        self.assertNotEqual(result.returncode, 0,
            "run.sh must fail when the scan phase fails. "
            f"stdout={result.stdout} stderr={result.stderr}")

    def test_negative_runner_executes_configure_phase(self):
        """run.sh must invoke verify.py with the configure phase."""
        result = self._run_negative()
        tokens = self._read_log()
        self.assertIn("python3", tokens,
            "configure phase not found in log. "
            f"stdout={result.stdout} stderr={result.stderr}")
        self.assertIn("configure", tokens,
            "configure phase not found in log. "
            f"stdout={result.stdout} stderr={result.stderr}")

    def test_negative_runner_removes_photos(self):
        """run.sh must remove /media/photos through the keeper container."""
        result = self._run_negative()
        tokens = self._read_log()
        self.assertIn("rm-photos", tokens,
            "rm-photos not found in log. "
            f"stdout={result.stdout} stderr={result.stderr}")

    def test_negative_runner_probes_photos_absent(self):
        """run.sh must probe that /media/photos is absent after removal."""
        result = self._run_negative()
        tokens = self._read_log()
        self.assertIn("probe-absent-photos", tokens,
            "probe-absent-photos not found in log. "
            f"stdout={result.stdout} stderr={result.stderr}")

    def test_negative_runner_probes_remaining_sources(self):
        """run.sh must probe that /media/movies, /media/tv, /media/music remain.

        The loop in run.sh probes three sources in order. Each probe logs
        one path-specific marker: probe-source-movies, probe-source-tv,
        probe-source-music. A missing source probe must fail this test.
        """
        result = self._run_negative()
        tokens = self._read_log()
        for marker in ("probe-source-movies", "probe-source-tv",
                        "probe-source-music"):
            self.assertIn(marker, tokens,
                f"{marker} not found in log. "
                f"stdout={result.stdout} stderr={result.stderr}")

    def test_negative_runner_executes_scan_phase(self):
        """run.sh must invoke verify.py with the scan phase after removal."""
        result = self._run_negative()
        tokens = self._read_log()
        self.assertIn("scan", tokens,
            "scan phase not found in log. "
            f"stdout={result.stdout} stderr={result.stderr}")

    def test_negative_runner_command_order(self):
        """The negative branch must execute in exact order.

        The log must record configure, remove /media/photos, probe absent,
        probe-source-movies, probe-source-tv, probe-source-music, and scan,
        in that exact order. The test extracts the first occurrence of each
        event from the token list and asserts the indices increase. A
        missing source probe or a reordered probe fails this test.
        """
        result = self._run_negative()
        tokens = self._read_log()
        configure_idx = self._first_index(tokens, "configure")
        rm_idx = self._first_index(tokens, "rm-photos")
        probe_absent_idx = self._first_index(tokens, "probe-absent-photos")
        movies_idx = self._first_index(tokens, "probe-source-movies")
        tv_idx = self._first_index(tokens, "probe-source-tv")
        music_idx = self._first_index(tokens, "probe-source-music")
        scan_idx = self._first_index(tokens, "scan")
        self.assertNotEqual(configure_idx, -1,
            f"configure not found in log. stdout={result.stdout}")
        self.assertNotEqual(rm_idx, -1,
            f"rm-photos not found in log. stdout={result.stdout}")
        self.assertNotEqual(probe_absent_idx, -1,
            f"probe-absent-photos not found in log. stdout={result.stdout}")
        self.assertNotEqual(movies_idx, -1,
            "probe-source-movies not found in log. "
            f"stdout={result.stdout}")
        self.assertNotEqual(tv_idx, -1,
            "probe-source-tv not found in log. "
            f"stdout={result.stdout}")
        self.assertNotEqual(music_idx, -1,
            "probe-source-music not found in log. "
            f"stdout={result.stdout}")
        self.assertNotEqual(scan_idx, -1,
            f"scan not found in log. stdout={result.stdout}")
        self.assertLess(configure_idx, rm_idx,
            "configure must appear before the removal of /media/photos")
        self.assertLess(rm_idx, probe_absent_idx,
            "removal must appear before the probe of /media/photos absent")
        self.assertLess(probe_absent_idx, movies_idx,
            "probe-absent must appear before probe-source-movies")
        self.assertLess(movies_idx, tv_idx,
            "probe-source-movies must appear before probe-source-tv")
        self.assertLess(tv_idx, music_idx,
            "probe-source-tv must appear before probe-source-music")
        self.assertLess(music_idx, scan_idx,
            "probe-source-music must appear before the scan phase")

    def test_negative_runner_does_not_swallow_failure(self):
        """run.sh must not swallow the scan failure.

        The exit code must be nonzero and the output must carry the
        cold-start failure message, not a success message.
        """
        result = self._run_negative()
        self.assertNotEqual(result.returncode, 0,
            "run.sh must return nonzero when the scan phase fails")
        combined = result.stdout + result.stderr
        self.assertIn("cold-start", combined,
            "run.sh must emit a cold-start message")


if __name__ == "__main__":
    unittest.main()
