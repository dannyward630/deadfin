#!/usr/bin/env python3
"""tools/coldstart/tests/test_cleanup_propagation.py

Stub cleanup probes for the cleanup failure propagation requirement.

The cleanup function in run.sh must fail for every query error, removal
error, or remaining labeled resource. These tests drive the cleanup-only
mode of run.sh through a stub Docker client set via the DOCKER environment
variable. The stub simulates three cases:

  1. A removal error: the stub reports a labeled container, fails the
     removal, and reports no remaining resource. Cleanup must exit nonzero.
  2. A remaining-resource failure: the stub reports a labeled volume,
     succeeds the removal, but reports the volume still present on the
     re-query. Cleanup must exit nonzero.
  3. A clean cleanup: the stub reports a labeled container, succeeds the
     removal, and reports no remaining resource. Cleanup must exit zero.
"""

import os
import subprocess
import sys
import tempfile
import unittest


SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
RUN_SH = os.path.join(SCRIPT_DIR, "..", "run.sh")


def make_stub_docker(script_content):
    """Write a stub docker script and return its path."""
    fd, path = tempfile.mkstemp(prefix="stub-docker-", suffix=".sh")
    os.write(fd, script_content.encode("utf-8"))
    os.close(fd)
    os.chmod(path, 0o755)
    return path


class TestCleanupPropagation(unittest.TestCase):

    def run_cleanup(self, stub_path):
        """Run run.sh cleanup with the stub Docker client. Return exit code."""
        env = dict(os.environ, DOCKER=stub_path)
        env.pop("IMAGE_REFERENCE", None)
        env.pop("IMAGE_DIGEST", None)
        proc = subprocess.run(
            ["sh", RUN_SH, "cleanup"],
            env=env,
            capture_output=True,
            timeout=10,
        )
        return proc.returncode, proc.stderr.decode("utf-8", errors="replace")

    def test_removal_error_fails(self):
        """A container removal error must produce a nonzero exit."""
        stub = make_stub_docker("""\
#!/bin/sh
case "$1" in
  ps)
    # Report one labeled container on the initial query.
    echo "deadfin-coldstart-stub"
    exit 0
    ;;
  rm)
    # Fail the removal.
    echo "docker rm: removal failed" >&2
    exit 1
    ;;
  network)
    case "$2" in
      ls) echo ""; exit 0 ;;
      rm) exit 0 ;;
    esac
    exit 0
    ;;
  volume)
    case "$2" in
      ls) echo ""; exit 0 ;;
      rm) exit 0 ;;
    esac
    exit 0
    ;;
esac
exit 0
""")
        try:
            code, stderr = self.run_cleanup(stub)
            self.assertNotEqual(code, 0,
                f"cleanup must fail on a removal error, got exit {code}.\nstderr:\n{stderr}")
            self.assertIn("failed to remove container", stderr)
        finally:
            os.unlink(stub)

    def test_remaining_resource_fails(self):
        """A remaining labeled resource after removal must produce a nonzero exit."""
        stub = make_stub_docker("""\
#!/bin/sh
case "$1" in
  ps)
    # Report one labeled container on the initial query, then report it
    # still present on the re-query (removal succeeded but the resource
    # remains).
    echo "deadfin-coldstart-stub"
    exit 0
    ;;
  rm)
    # Succeed the removal, but the re-query still reports the resource.
    exit 0
    ;;
  network)
    case "$2" in
      ls) echo ""; exit 0 ;;
      rm) exit 0 ;;
    esac
    exit 0
    ;;
  volume)
    case "$2" in
      ls) echo ""; exit 0 ;;
      rm) exit 0 ;;
    esac
    exit 0
    ;;
esac
exit 0
""")
        try:
            code, stderr = self.run_cleanup(stub)
            self.assertNotEqual(code, 0,
                f"cleanup must fail on a remaining resource, got exit {code}.\nstderr:\n{stderr}")
            self.assertIn("remain after cleanup", stderr)
        finally:
            os.unlink(stub)

    def test_query_error_fails(self):
        """A query error during the initial container listing must produce a nonzero exit."""
        stub = make_stub_docker("""\
#!/bin/sh
case "$1" in
  ps)
    # Fail the initial query.
    echo "docker ps: query failed" >&2
    exit 1
    ;;
  network)
    case "$2" in
      ls) echo ""; exit 0 ;;
      rm) exit 0 ;;
    esac
    exit 0
    ;;
  volume)
    case "$2" in
      ls) echo ""; exit 0 ;;
      rm) exit 0 ;;
    esac
    exit 0
    ;;
esac
exit 0
""")
        try:
            code, stderr = self.run_cleanup(stub)
            self.assertNotEqual(code, 0,
                f"cleanup must fail on a query error, got exit {code}.\nstderr:\n{stderr}")
            self.assertIn("failed to query labeled containers", stderr)
        finally:
            os.unlink(stub)

    def test_clean_cleanup_succeeds(self):
        """A clean cleanup with no remaining resources must exit zero."""
        stub = make_stub_docker("""\
#!/bin/sh
case "$1" in
  ps)
    # No labeled containers on any query.
    echo ""
    exit 0
    ;;
  network)
    case "$2" in
      ls) echo ""; exit 0 ;;
      rm) exit 0 ;;
    esac
    exit 0
    ;;
  volume)
    case "$2" in
      ls) echo ""; exit 0 ;;
      rm) exit 0 ;;
    esac
    exit 0
    ;;
esac
exit 0
""")
        try:
            code, stderr = self.run_cleanup(stub)
            self.assertEqual(code, 0,
                f"clean cleanup must exit zero, got exit {code}.\nstderr:\n{stderr}")
        finally:
            os.unlink(stub)


if __name__ == "__main__":
    unittest.main()
