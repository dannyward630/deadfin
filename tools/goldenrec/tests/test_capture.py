import http.client
import json
import os
import shutil
import signal
import socket
import subprocess
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


REPO = Path(__file__).resolve().parents[3]
CAPTURE = REPO / "tools" / "goldenrec" / "capture.sh"
PROXY_HOST = "127.0.0.1"
PROXY_PORT = 8080
SECRET = "goldenrec-capture-test-secret"


class FixtureHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = b'{"fixture":"loopback"}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass


class CaptureLauncherConfigurationTests(unittest.TestCase):
    def test_jellyfin_base_url_override_becomes_sanitizer_target(self):
        configured_target = "https://synthetic.jellyfin.test:9443"
        with tempfile.TemporaryDirectory(prefix="goldenrec-capture-config-") as directory:
            directory = Path(directory)
            executable_directory = directory / "bin"
            executable_directory.mkdir()
            arguments = directory / "mitmdump.arguments"
            mitmdump = executable_directory / "mitmdump"
            mitmdump.write_text('#!/bin/sh\nprintf "%s\\n" "$@" > "$MITMDUMP_ARGUMENTS"\n', encoding="utf-8")
            mitmdump.chmod(0o755)

            result = subprocess.run(
                [str(CAPTURE), str(directory / "capture.mitm")],
                capture_output=True,
                text=True,
                env=os.environ
                | {
                    "JELLYFIN_BASE_URL": configured_target,
                    "MITMDUMP_ARGUMENTS": str(arguments),
                    "PATH": f"{executable_directory}:{os.environ['PATH']}",
                },
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(
                f"goldenrec_capture_target={configured_target}",
                arguments.read_text(encoding="utf-8").splitlines(),
            )


class CaptureLauncherBoundaryTests(unittest.TestCase):
    def setUp(self):
        if shutil.which("nix-shell") is None:
            self.skipTest("nix-shell is required to exercise the pinned mitmdump boundary")
        self.fixture = ThreadingHTTPServer((PROXY_HOST, 0), FixtureHandler)
        self.fixture_thread = threading.Thread(target=self.fixture.serve_forever, daemon=True)
        self.fixture_thread.start()
        self.process = None

    def tearDown(self):
        try:
            self._stop_launcher()
        finally:
            self.fixture.shutdown()
            self.fixture.server_close()
            self.fixture_thread.join(timeout=5)

    def test_regular_proxy_captures_and_sanitizes_loopback_traffic(self):
        with tempfile.TemporaryDirectory(prefix="goldenrec-capture-") as directory:
            raw_capture = Path(directory) / "loopback.mitm"
            sanitized_capture = Path(directory) / "loopback.sanitized.jsonl"
            self._start_launcher(raw_capture)
            self._wait_for_proxy()

            destination = f"http://{PROXY_HOST}:{self.fixture.server_port}/stock?visible=yes&api_key={SECRET}"
            connection = http.client.HTTPConnection(PROXY_HOST, PROXY_PORT, timeout=5)
            try:
                connection.request(
                    "GET",
                    destination,
                    headers={
                        "Accept": "application/json",
                        "Authorization": f"Bearer {SECRET}",
                        "Host": f"{PROXY_HOST}:{self.fixture.server_port}",
                    },
                )
                response = connection.getresponse()
                self.assertEqual(response.status, 200)
                self.assertEqual(response.read(), b'{"fixture":"loopback"}')
            finally:
                connection.close()

            self._wait_for_file(sanitized_capture)
            self._stop_launcher()

            self.assertTrue(raw_capture.is_file(), "launcher must write the requested raw capture")
            raw_bytes = raw_capture.read_bytes()
            self.assertGreater(len(raw_bytes), 0, "raw capture must contain proxied traffic")
            self.assertIn(SECRET.encode(), raw_bytes, "raw capture must preserve the captured request")

            records = [json.loads(line) for line in sanitized_capture.read_text(encoding="utf-8").splitlines()]
            self.assertEqual(len(records), 1)
            self.assertEqual(records[0]["path"], "/stock?visible=yes")
            self.assertTrue(records[0]["requires_auth"])
            self.assertEqual(records[0]["response_body"], {"redacted": "authenticated-response-body"})
            self.assertNotIn(SECRET, sanitized_capture.read_text(encoding="utf-8"))

            self._assert_proxy_stopped()

    def _start_launcher(self, raw_capture):
        command = ["nix-shell", "--run", f"exec {CAPTURE} {raw_capture}"]
        self.process = subprocess.Popen(
            command,
            cwd=REPO,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            env=os.environ | {"JELLYFIN_BASE_URL": f"http://{PROXY_HOST}:{self.fixture.server_port}"},
            start_new_session=True,
        )

    def _wait_for_proxy(self):
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                stdout, stderr = self.process.communicate()
                self.fail(
                    "capture launcher exited before proxying traffic "
                    f"(status {self.process.returncode}):\nstdout:\n{stdout}\nstderr:\n{stderr}"
                )
            try:
                with socket.create_connection((PROXY_HOST, PROXY_PORT), timeout=0.2):
                    return
            except OSError:
                time.sleep(0.1)
        self.fail("capture launcher did not listen on 127.0.0.1:8080 within 45 seconds")

    def _wait_for_file(self, path):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if path.is_file() and path.stat().st_size:
                return
            time.sleep(0.05)
        self.fail(f"sanitized capture was not written to {path}")

    def _stop_launcher(self):
        if self.process is None:
            return
        process, self.process = self.process, None
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGTERM)
        try:
            process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)
            self.fail("capture launcher did not stop after SIGTERM")
        stdout, stderr = process.communicate()
        self.assertEqual(
            process.returncode,
            0,
            f"capture launcher must stop cleanly after SIGTERM; stdout:\n{stdout}\nstderr:\n{stderr}",
        )
        self._assert_process_group_stopped(process.pid)

    def _assert_process_group_stopped(self, process_group):
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            try:
                os.killpg(process_group, 0)
            except ProcessLookupError:
                return
            time.sleep(0.1)
        self.fail(f"capture launcher left processes in group {process_group} after termination")

    def _assert_proxy_stopped(self):
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            try:
                with socket.create_connection((PROXY_HOST, PROXY_PORT), timeout=0.2):
                    time.sleep(0.1)
            except OSError:
                return
        self.fail("proxy listener remained on 127.0.0.1:8080 after launcher termination")


if __name__ == "__main__":
    unittest.main()
