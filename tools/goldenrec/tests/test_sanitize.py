import importlib.util
import json
import sys
import tempfile
import subprocess
import types
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SANITIZE_PATH = ROOT / "mitmproxy" / "sanitize.py"


def load_sanitizer():
    ctx = types.SimpleNamespace(options=types.SimpleNamespace(goldenrec_capture_output=""))
    mitmproxy = types.ModuleType("mitmproxy")
    mitmproxy.ctx = ctx
    mitmproxy.http = types.SimpleNamespace(HTTPFlow=object)
    previous = {name: sys.modules.get(name) for name in ("mitmproxy", "mitmproxy.ctx", "mitmproxy.http")}
    sys.modules["mitmproxy"] = mitmproxy
    sys.modules["mitmproxy.ctx"] = ctx
    sys.modules["mitmproxy.http"] = mitmproxy.http
    try:
        spec = importlib.util.spec_from_file_location("goldenrec_sanitize_test", SANITIZE_PATH)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module, ctx
    finally:
        for name, value in previous.items():
            if value is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = value


class Message:
    def __init__(self, content, headers):
        self.content = content
        self.headers = headers
        self.status_code = 200

    def get_text(self, strict=False):
        return self.content.decode("utf-8")


class Request(Message):
    def __init__(self, url, content=b"", headers=None):
        super().__init__(content, headers or {})
        self.method = "GET"
        self.url = url


class Flow:
    def __init__(self, request, response):
        self.request = request
        self.response = response


class SanitizeTests(unittest.TestCase):
    def setUp(self):
        self.sanitize, self.ctx = load_sanitizer()

    def test_scrub_removes_jellyfin_credentials_and_tokenized_urls(self):
        payload = {
            "AccessToken": "access-token",
            "Secret": "quick-connect-secret",
            "safe": "value",
            "nested": {"api_key": "api-key", "url": "http://stock/Audio?ApiKey=url-token&safe=yes"},
            "TranscodingUrl": "/Videos/1/stream?access_token=embedded-token",
        }

        self.assertEqual(
            self.sanitize.scrub(payload),
            {
                "safe": "value",
                "nested": {"url": "http://stock/Audio?safe=yes"},
                "TranscodingUrl": "/Videos/1/stream",
            },
        )

    def test_query_token_marks_capture_authenticated_and_redacts_response_body(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "capture.sanitized.jsonl"
            self.ctx.options.goldenrec_capture_output = str(output)
            flow = Flow(
                Request("http://stock/Items?ApiKey=query-token&safe=yes", headers={"Accept": "application/json"}),
                Message(json.dumps({"AccessToken": "response-token"}).encode(), {"content-type": "application/json"}),
            )

            self.sanitize.GoldenrecSanitizer().response(flow)

            record = json.loads(output.read_text(encoding="utf-8"))
            self.assertTrue(record["requires_auth"])
            self.assertEqual(record["auth_role"], "captured-authenticated")
            self.assertEqual(record["path"], "/Items?safe=yes")
            self.assertEqual(record["response_body"], {"redacted": "authenticated-response-body"})

    def test_legacy_emby_authorization_marks_capture_authenticated_and_redacts_output(self):
        secret = "MediaBrowser Token=legacy-emby-token"
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "capture.sanitized.jsonl"
            self.ctx.options.goldenrec_capture_output = str(output)
            flow = Flow(
                Request(
                    "http://stock/Items",
                    headers={"Accept": "application/json", "X-Emby-Authorization": secret},
                ),
                Message(json.dumps({"AccessToken": "response-token"}).encode(), {"content-type": "application/json"}),
            )

            self.sanitize.GoldenrecSanitizer().response(flow)

            serialized = output.read_text(encoding="utf-8")
            record = json.loads(serialized)
            self.assertNotIn("X-Emby-Authorization", record["request_headers"])
            self.assertEqual(record["auth_role"], "captured-authenticated")
            self.assertEqual(record["response_body"], {"redacted": "authenticated-response-body"})
            self.assertNotIn(secret, serialized)

    def test_records_only_flows_from_configured_synthetic_origin(self):
        """Browser traffic to an external host must never become a stock-relative replay step."""
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "capture.sanitized.jsonl"
            self.ctx.options.goldenrec_capture_output = str(output)
            self.ctx.options.goldenrec_capture_target = "http://127.0.0.1:8096"
            sanitizer = self.sanitize.GoldenrecSanitizer()
            sanitizer.response(
                Flow(
                    Request("http://127.0.0.1:8096/System/Info"),
                    Message(b'{"ServerName":"stock"}', {"content-type": "application/json"}),
                )
            )
            sanitizer.response(
                Flow(
                    Request("https://www.google.com/time/1/current?source=chrome"),
                    Message(b'{"time":"external"}', {"content-type": "application/json"}),
                )
            )

            records = [json.loads(line) for line in output.read_text(encoding="utf-8").splitlines()]
            self.assertEqual([record["path"] for record in records], ["/System/Info"])

    def test_sanitized_websocket_upgrade_is_not_distilled_as_curl_replay(self):
        """Curl scenarios retain ordinary HTTP requests but never WebSocket handshakes."""
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            capture = directory / "capture.sanitized.jsonl"
            scenario = directory / "scenario.yaml"
            self.ctx.options.goldenrec_capture_output = str(capture)
            websocket_response = Message(b"forbidden", {"content-type": "text/plain"})
            websocket_response.status_code = 403
            sanitizer = self.sanitize.GoldenrecSanitizer()
            sanitizer.response(
                Flow(
                    Request("http://stock/socket", headers={"Upgrade": "websocket"}),
                    websocket_response,
                )
            )
            sanitizer.response(
                Flow(
                    Request("http://stock/System/Info", headers={"Accept": "application/json"}),
                    Message(b'{"ServerName":"stock"}', {"content-type": "application/json"}),
                )
            )

            websocket_record, ordinary_record = [json.loads(line) for line in capture.read_text(encoding="utf-8").splitlines()]
            self.assertEqual(websocket_record["request_headers"].get("Upgrade"), "websocket")

            result = subprocess.run(
                [sys.executable, str(ROOT / "distill.py"), str(capture), str(scenario)],
                capture_output=True,
                text=True,
                check=False,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            distilled = json.loads(scenario.read_text(encoding="utf-8"))
            self.assertEqual(
                distilled["steps"],
                [
                    {
                        "name": "captured-1",
                        "operation_id": "SHADOW:GET:/System/Info",
                        "method": ordinary_record["method"],
                        "path": ordinary_record["path"],
                        "headers": ordinary_record["request_headers"],
                        "expect": {"status": ordinary_record["status"]},
                    }
                ],
            )


if __name__ == "__main__":
    unittest.main()
