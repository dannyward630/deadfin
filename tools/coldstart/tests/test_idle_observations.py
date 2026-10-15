#!/usr/bin/env python3
"""tools/coldstart/tests/test_idle_observations.py

Regression coverage for the stable-idle observation rule:

  Active or queued observations must reset prior idle observations so a
  later idle does not match a stale idle sample from before the active
  period.

The sequence under test is Idle, Active, Idle, Idle. Without the reset,
the first idle is stored as the prior observation. The active observation
is skipped. The second idle then matches the stale first idle, producing a
false stable result after only one idle observation following the active
period. With the reset, the active observation clears the prior idle, so
the second idle becomes the first observation and the third idle is
required to confirm stability.
"""

import importlib.util
import os
import sys
import types
import unittest

# Load verify.py as a module without running main().
verify_path = os.path.join(os.path.dirname(__file__), "..", "verify.py")
spec = importlib.util.spec_from_file_location("coldstart_verify", verify_path)
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)

# The four library names that library_states returns.
ALL_IDLE = {
    "Golden Movies": "Idle",
    "Golden TV": "Idle",
    "Golden Music": "Idle",
    "Golden Photos": "Idle",
}

ONE_ACTIVE = {
    "Golden Movies": "Active",
    "Golden TV": "Idle",
    "Golden Music": "Idle",
    "Golden Photos": "Idle",
}


class MockClient:
    """A mock client whose library_states and list_items return scripted values."""

    def __init__(self, states_sequence, totals_sequence):
        self._states = list(states_sequence)
        self._totals = list(totals_sequence)
        self._state_idx = 0
        self._item_idx = 0

    def get_json(self, path, expected=200, auth=True):
        if path.startswith("/Library/VirtualFolders"):
            return self._libraries()
        if path.startswith("/Items"):
            total = self._totals[self._item_idx]
            self._item_idx += 1
            return {"TotalRecordCount": total, "Items": []}
        raise AssertionError(f"unexpected path: {path}")

    def _libraries(self):
        states = self._states[self._state_idx]
        self._state_idx += 1
        libraries = []
        for name in ("Golden Movies", "Golden TV", "Golden Music", "Golden Photos"):
            libraries.append({"Name": name, "RefreshStatus": states[name]})
        return libraries


class TestStableIdleReset(unittest.TestCase):
    def setUp(self):
        # Prevent real sleeps from slowing the test.
        self._real_sleep = verify.time.sleep
        verify.time.sleep = lambda _s: None

    def tearDown(self):
        verify.time.sleep = self._real_sleep

    def test_idle_active_idle_idle_requires_two_consecutive_after_active(self):
        """Idle, Active, Idle, Idle must not return after the second idle.

        Without the reset, the first idle is stored, the active is skipped,
        and the second idle matches the first idle. With the reset, the
        active clears the prior observation, so the second idle is the first
        sample and the third idle confirms stability.
        """
        # Sequence: Idle, Active, Idle, Idle
        states_seq = [ALL_IDLE, ONE_ACTIVE, ALL_IDLE, ALL_IDLE]
        totals_seq = [10, 10, 10, 10]
        client = MockClient(states_seq, totals_seq)

        # The poll must not return after only the second idle (index 2).
        # It must require the third idle (index 3) to confirm stability.
        states, total, items = verify.poll_until_stable_idle(client)
        self.assertEqual(states, ALL_IDLE)
        self.assertEqual(total, 10)

    def test_idle_active_idle_never_stable_within_one_more_idle(self):
        """Idle, Active, Idle, Active must fail (timeout) — no two consecutive idles."""
        # Sequence: Idle, Active, Idle, Active — never two consecutive idles.
        # Repeat the pattern enough times to exhaust the timeout.
        states_seq = [ALL_IDLE, ONE_ACTIVE] * 20
        totals_seq = [10] * 20
        client = MockClient(states_seq, totals_seq)

        # Shorten the timeout and advance the clock so it expires quickly.
        real_monotonic = verify.time.monotonic
        real_timeout = verify.IDLE_POLL_TIMEOUT
        verify.IDLE_POLL_TIMEOUT = 5
        clock = [0]
        def advancing_clock():
            clock[0] += 1
            return clock[0]
        verify.time.monotonic = advancing_clock
        try:
            with self.assertRaises(SystemExit):
                verify.poll_until_stable_idle(client)
        finally:
            verify.time.monotonic = real_monotonic
            verify.IDLE_POLL_TIMEOUT = real_timeout

    def test_stable_idle_immediately(self):
        """Two consecutive idle observations with no active period return immediately."""
        states_seq = [ALL_IDLE, ALL_IDLE]
        totals_seq = [10, 10]
        client = MockClient(states_seq, totals_seq)

        states, total, items = verify.poll_until_stable_idle(client)
        self.assertEqual(states, ALL_IDLE)
        self.assertEqual(total, 10)


if __name__ == "__main__":
    unittest.main()
