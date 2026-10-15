import json
import os
import sys
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


class ReferenceContractsTests(unittest.TestCase):
    def test_capture_truncates_existing_sanitized_output_before_mitmdump(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            capture = directory / "trace.mitm"
            sanitized = directory / "trace.sanitized.jsonl"
            sanitized.write_text("stale secret-bearing record\n", encoding="utf-8")
            executable_directory = directory / "bin"
            executable_directory.mkdir()
            mitmdump = executable_directory / "mitmdump"
            mitmdump.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
            mitmdump.chmod(0o755)
            env = os.environ | {"PATH": f"{executable_directory}:{os.environ['PATH']}"}

            result = subprocess.run([str(ROOT / "capture.sh"), str(capture)], capture_output=True, text=True, env=env)

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(sanitized.read_text(encoding="utf-8"), "")

    def test_replay_uses_scenario_base_url_environment(self):
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(204)
                self.end_headers()

            def log_message(self, format, *args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join)
        self.addCleanup(server.shutdown)

        with tempfile.TemporaryDirectory() as directory:
            scenario = Path(directory) / "scenario.yaml"
            scenario.write_text(
                json.dumps(
                    {
                        "format": "goldenrec-scenario/v1",
                        "base_url_env": "TEST_GOLDENREC_BASE_URL",
                        "steps": [{"name": "configured endpoint", "method": "GET", "path": "/health", "expect": {"status": 204}}],
                    }
                ),
                encoding="utf-8",
            )
            host, port = server.server_address
            env = os.environ | {
                "TEST_GOLDENREC_BASE_URL": f"http://{host}:{port}",
                "JELLYFIN_BASE_URL": "http://127.0.0.1:1",
            }
            result = subprocess.run([str(ROOT / "replay.sh"), str(scenario)], capture_output=True, text=True, env=env)

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("ok 1/1: configured endpoint", result.stdout)

    def test_distill_assigns_queryless_shadow_operation_ids_to_eligible_requests(self):
        """Every mechanically replayable request must identify its queryless shadow route."""
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            capture = directory / "capture.sanitized.jsonl"
            scenario = directory / "scenario.yaml"
            capture.write_text(
                "\n".join(
                    json.dumps(record)
                    for record in (
                        {"method": "GET", "path": "/System/Info?view=full", "request_headers": {}, "status": 200},
                        {"method": "HEAD", "path": "/Items/42?Fields=Overview", "request_headers": {}, "status": 204},
                        {"method": "GET", "path": "/System/Info?view=summary", "request_headers": {}, "status": 200},
                        {
                            "method": "GET",
                            "path": "/socket?client=browser",
                            "request_headers": {"Upgrade": "websocket"},
                            "status": 101,
                        },
                        {"method": "GET", "path": "/Users/Authenticate?username=admin", "request_headers": {}, "status": 200},
                        {
                            "method": "GET",
                            "path": "/Library/VirtualFolders?includeHidden=true",
                            "request_headers": {},
                            "requires_auth": True,
                            "status": 200,
                        },
                    )
                )
                + "\n",
                encoding="utf-8",
            )

            result = subprocess.run(
                [sys.executable, str(ROOT / "distill.py"), str(capture), str(scenario)],
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            steps = json.loads(scenario.read_text(encoding="utf-8"))["steps"]
            self.assertEqual(
                [(step["method"], step["path"]) for step in steps],
                [
                    ("GET", "/System/Info?view=full"),
                    ("HEAD", "/Items/42?Fields=Overview"),
                    ("GET", "/System/Info?view=summary"),
                ],
            )
            self.assertEqual(
                [step["operation_id"] for step in steps],
                [
                    "SHADOW:GET:/System/Info",
                    "SHADOW:HEAD:/Items/42",
                    "SHADOW:GET:/System/Info",
                ],
            )

    def test_distill_replay_uses_only_captured_request_headers(self):
        class Handler(BaseHTTPRequestHandler):
            received_accept_headers = {}

            def do_GET(self):
                Handler.received_accept_headers[self.path] = self.headers.get("Accept")
                if self.path == "/socket":
                    self.send_response(404 if self.headers.get("Accept") == "application/json" else 403)
                elif self.path == "/System/Info":
                    self.send_response(200 if self.headers.get("Accept") == "application/json" else 406)
                else:
                    self.send_response(500)
                self.end_headers()

            def log_message(self, format, *args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join)
        self.addCleanup(server.shutdown)

        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            capture = directory / "capture.sanitized.jsonl"
            scenario = directory / "scenario.yaml"
            capture.write_text(
                "\n".join(
                    json.dumps(record)
                    for record in (
                        {
                            "method": "GET",
                            "path": "/socket",
                            "request_headers": {},
                            "status": 403,
                        },
                        {
                            "method": "GET",
                            "path": "/System/Info",
                            "request_headers": {"Accept": "application/json"},
                            "status": 200,
                        },
                    )
                )
                + "\n",
                encoding="utf-8",
            )
            distill = subprocess.run(
                [sys.executable, str(ROOT / "distill.py"), str(capture), str(scenario)],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(distill.returncode, 0, distill.stderr)
            self.assertEqual(
                json.loads(scenario.read_text(encoding="utf-8"))["steps"],
                [
                    {"name": "captured-1", "operation_id": "SHADOW:GET:/socket", "method": "GET", "path": "/socket", "expect": {"status": 403}},
                    {
                        "name": "captured-2",
                        "method": "GET",
                        "operation_id": "SHADOW:GET:/System/Info",
                        "path": "/System/Info",
                        "headers": {"Accept": "application/json"},
                        "expect": {"status": 200},
                    },
                ],
            )

            host, port = server.server_address
            replay = subprocess.run(
                [str(ROOT / "replay.sh"), str(scenario)],
                capture_output=True,
                text=True,
                env=os.environ | {"JELLYFIN_BASE_URL": f"http://{host}:{port}"},
            )

        self.assertEqual(replay.returncode, 0, replay.stderr)
        self.assertIn("ok 1/2: captured-1", replay.stdout)
        self.assertIn("ok 2/2: captured-2", replay.stdout)
        self.assertEqual(
            Handler.received_accept_headers,
            {"/socket": "*/*", "/System/Info": "application/json"},
        )

    def test_seed_waits_for_stable_complete_library_scan_before_snapshot(self):
        """Snapshotting follows a stable canonical-inventory scan and BoxSet topology checks."""
        seed = (ROOT / "seed.sh").read_text(encoding="utf-8")
        start = seed.index("wait_for_scan() {")
        end = seed.index("\nsnapshot() {", start)
        readiness = seed[start:end]

        self.assertIn('"$INVENTORY"', readiness)
        for marker in ("missing=", "extra=", "type_changed="):
            with self.subTest(marker=marker):
                self.assertIn(marker, readiness)
        self.assertRegex(readiness, r"stable\s*>=\s*2")

        wait_index = seed.rindex("\nwait_for_scan\n")
        membership_index = seed.rindex("\nensure_boxset_membership\n")
        collapse_index = seed.rindex("\nverify_boxset_collapse\n")
        snapshot_index = seed.rindex("\nsnapshot\n")
        self.assertLess(wait_index, membership_index)
        self.assertLess(membership_index, collapse_index)
        self.assertLess(collapse_index, snapshot_index)

    def test_named_volume_static_media_and_bluray_stub_are_declared(self):
        compose = (ROOT / "compose.yaml").read_text(encoding="utf-8")
        generator = (ROOT / "fixtures" / "gen.sh").read_text(encoding="utf-8")
        self.assertIn("goldenrec-config:/config", compose)
        self.assertIn("./fixtures/out:/media:ro", compose)
        self.assertIn("volumes:\n  goldenrec-config:", compose)
        self.assertIn("Synthetic Disc.bluray.disc", generator)


if __name__ == "__main__":
    unittest.main()
