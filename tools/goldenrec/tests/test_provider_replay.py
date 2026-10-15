import importlib.util
import json
import os
import sys
import tempfile
import types
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
ADDON_PATH = ROOT / "mitmproxy" / "provider_replay.py"
ITEMLOOKUP_CASSETTE_PATH = ROOT.parents[1] / "corpora" / "provider-cassettes" / "itemlookup.jsonl"


def load_addon():
    log = types.SimpleNamespace(
        info=lambda *a: None,
        warn=lambda *a: None,
    )
    ctx = types.SimpleNamespace(
        options=types.SimpleNamespace(
            provider_replay_mode="replay",
            provider_replay_cassette="",
            provider_replay_unmatched_report="",
        ),
        log=log,
    )
    mitmproxy = types.ModuleType("mitmproxy")
    mitmproxy.ctx = ctx
    http_module = types.ModuleType("mitmproxy.http")
    http_module.HTTPFlow = object

    class MockResponse:
        """Minimal stand-in for mitmproxy.http.Response.make."""
        @staticmethod
        def make(status_code, content, headers=None):
            r = Response(content if isinstance(content, bytes) else str(content).encode(),
                         headers or {})
            r.status_code = status_code
            return r
    http_module.Response = MockResponse
    mitmproxy.http = http_module
    previous = {name: sys.modules.get(name) for name in ("mitmproxy", "mitmproxy.ctx", "mitmproxy.http")}
    sys.modules["mitmproxy"] = mitmproxy
    sys.modules["mitmproxy.ctx"] = ctx
    sys.modules["mitmproxy.http"] = http_module
    try:
        spec = importlib.util.spec_from_file_location("provider_replay_test", ADDON_PATH)
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
    def __init__(self, content, headers=None):
        self.content = content
        self.headers = headers or {}
        self.status_code = 200

    def get_text(self, strict=False):
        return self.content.decode("utf-8") if self.content else ""

    def set_text(self, text):
        self.content = text.encode("utf-8")


class Request(Message):
    def __init__(self, url, method="GET", content=b"", headers=None):
        super().__init__(content, headers or {})
        self.method = method
        self.url = url


class Response(Message):
    def __init__(self, content=b"", headers=None, status_code=200):
        super().__init__(content, headers or {})
        self.status_code = status_code


class Flow:
    def __init__(self, request, response):
        self.request = request
        self.response = response


class ProviderReplayTests(unittest.TestCase):
    def setUp(self):
        self.addon, self.ctx = load_addon()

    def test_match_key_normalizes_tmdb_api_key(self):
        req = Request("https://api.themoviedb.org/3/search/movie?api_key=secret123&query=hello")
        key = self.addon.match_key(req)
        self.assertIn("<redacted>", key)
        self.assertNotIn("secret123", key)

    def test_match_key_normalizes_audiodb_path_segment(self):
        req = Request("https://www.theaudiodb.com/api/v1/json/abc123def456/search.php?s=artist")
        key = self.addon.match_key(req)
        self.assertIn("<redacted>", key)
        self.assertNotIn("abc123def456", key)

    def test_match_key_includes_method_host_path(self):
        req = Request("https://api.themoviedb.org/3/search/movie?query=hello")
        key = self.addon.match_key(req)
        self.assertTrue(key.startswith("GET|api.themoviedb.org|/3/search/movie|"))

    def test_normalize_query_replaces_only_api_key(self):
        url = "https://api.themoviedb.org/3/search/movie?api_key=secret&query=hello"
        result = self.addon.normalize_query(url)
        self.assertIn("query=hello", result)
        self.assertIn("<redacted>", result)
        self.assertNotIn("secret", result)

    def test_redact_credentials_replaces_bearer_token(self):
        text = 'Authorization: Bearer abc123secret456'
        result = self.addon.redact_credentials(text)
        self.assertNotIn("abc123secret456", result)
        self.assertIn("<redacted>", result)

    def test_redact_credentials_replaces_api_key_in_query(self):
        text = "https://api.example.com/search?api_key=mysecretkey&q=test"
        result = self.addon.redact_credentials(text)
        self.assertNotIn("mysecretkey", result)
        self.assertIn("<redacted>", result)

    def test_redact_credentials_replaces_audiodb_path_key(self):
        text = "https://www.theaudiodb.com/api/v1/json/1234567890abcdef/search.php"
        result = self.addon.redact_credentials(text)
        self.assertNotIn("1234567890abcdef", result)

    def test_deterministic_jpeg_is_valid_jpeg(self):
        jpeg = self.addon.DETERMINISTIC_JPEG
        self.assertTrue(jpeg.startswith(b"\xff\xd8"))
        self.assertTrue(jpeg.endswith(b"\xff\xd9"))
        self.assertLess(len(jpeg), 500)

    def test_sanitize_flow_replaces_image_body(self):
        flow = Flow(
            Request("https://image.tmdb.org/t/p/original/test.jpg"),
            Response(b"\x89PNG\r\n\x1a\n", {"content-type": "image/jpeg"}),
        )
        self.addon.sanitize_flow(flow)
        self.assertEqual(flow.response.content, self.addon.DETERMINISTIC_JPEG)
        self.assertEqual(flow.response.headers["content-type"], "image/jpeg")

    def test_sanitize_flow_redacts_text_credentials(self):
        flow = Flow(
            Request("https://api.themoviedb.org/3/search/movie?api_key=secret"),
            Response(b'{"results":[{"api_key":"secret123"}]}', {"content-type": "application/json"}),
        )
        self.addon.sanitize_flow(flow)
        text = flow.response.get_text()
        self.assertNotIn("secret123", text)

    def test_sanitize_flow_removes_auth_headers(self):
        flow = Flow(
            Request("https://api.example.com/test", headers={
                "Authorization": "Bearer xyz",
                "Cookie": "session=abc",
                "X-Emby-Token": "token123",
                "Accept": "application/json",
            }),
            Response(b"{}", {"content-type": "application/json", "Set-Cookie": "foo=bar"}),
        )
        self.addon.sanitize_flow(flow)
        self.assertNotIn("authorization", flow.request.headers)
        self.assertNotIn("cookie", flow.request.headers)
        self.assertNotIn("x-emby-token", flow.request.headers)
        self.assertIn("Accept", flow.request.headers)
        self.assertNotIn("Set-Cookie", flow.response.headers)

    def test_load_cassette_skips_provenance_header(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "test.jsonl"
            path.write_text(
                json.dumps({"type": "provenance", "date": "2026-08-11"}) + "\n" +
                json.dumps({
                    "method": "GET", "host": "api.example.com",
                    "path": "/test", "query": "", "status": 200,
                    "response_headers": {"content-type": "application/json"},
                    "response_body": {"type": "text", "data": "{}"}
                }) + "\n"
            )
            flows = self.addon.load_cassette(str(path))
            self.assertEqual(len(flows), 1)
            key = "GET|api.example.com|/test|"
            self.assertIn(key, flows)
            self.assertEqual(flows[key]["status"], 200)

    def test_scan_cassette_for_credentials_finds_unredacted_keys(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "test.jsonl"
            path.write_text(
                json.dumps({"type": "provenance"}) + "\n" +
                json.dumps({"method": "GET", "host": "api.example.com",
                            "path": "/test", "query": "",
                            "status": 200,
                            "response_body": {"type": "text", "data": '{"api_key":"secret123"}'}}) + "\n"
            )
            findings = self.addon.scan_cassette_for_credentials(str(path))
            self.assertTrue(len(findings) > 0)
            self.assertEqual(findings[0][0], 2)

    def test_scan_cassette_finds_nothing_in_redacted_cassette(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "test.jsonl"
            path.write_text(
                json.dumps({"type": "provenance"}) + "\n" +
                json.dumps({"method": "GET", "host": "api.example.com",
                            "path": "/test", "query": "api_key=<redacted>",
                            "status": 200, "response_body": {"type": "text", "data": "{}"}}) + "\n"
            )
            findings = self.addon.scan_cassette_for_credentials(str(path))
            self.assertEqual(len(findings), 0)

    def test_committed_itemlookup_cassette_has_no_credentials(self):
        findings = self.addon.scan_cassette_for_credentials(str(ITEMLOOKUP_CASSETTE_PATH))
        self.assertEqual([], findings)

    def test_encode_body_image_type(self):
        response = Response(b"\xff\xd8\xff", {"content-type": "image/jpeg"})
        result = self.addon.encode_body(response)
        self.assertEqual(result["type"], "image")

    def test_encode_body_text_type(self):
        response = Response(b'{"key": "value"}', {"content-type": "application/json"})
        result = self.addon.encode_body(response)
        self.assertEqual(result["type"], "text")
        self.assertEqual(result["data"], '{"key": "value"}')

    def test_decode_body_image_returns_deterministic_jpeg(self):
        result = self.addon.decode_body({"type": "image"})
        self.assertEqual(result, self.addon.DETERMINISTIC_JPEG)

    def test_decode_body_text_returns_text_bytes(self):
        result = self.addon.decode_body({"type": "text", "data": "hello"})
        self.assertEqual(result, b"hello")

    def test_decode_body_empty_returns_empty(self):
        result = self.addon.decode_body({"type": "empty"})
        self.assertEqual(result, b"")


class ProviderReplayReplayModeTests(unittest.TestCase):
    def setUp(self):
        self.addon, self.ctx = load_addon()
        self.ctx.options.provider_replay_mode = "replay"

    def test_replay_request_hook_serves_matched_flow(self):
        """The request() hook serves a cassette-matched response before any
        upstream connection, preventing live provider traffic."""
        with tempfile.TemporaryDirectory() as directory:
            cassette = Path(directory) / "cassette.jsonl"
            report = Path(directory) / "unmatched.txt"
            cassette.write_text(
                json.dumps({"type": "provenance"}) + "\n" +
                json.dumps({
                    "method": "GET", "host": "api.themoviedb.org",
                    "path": "/test", "query": "", "status": 200,
                    "response_headers": {"content-type": "application/json"},
                    "response_body": {"type": "text", "data": '{"ok": true}'}
                }) + "\n"
            )
            self.ctx.options.provider_replay_cassette = str(cassette)
            self.ctx.options.provider_replay_unmatched_report = str(report)
            instance = self.addon.ProviderReplay()
            instance._load_flows()
            self.assertEqual(len(instance.flows), 1)
            request = Request("https://api.themoviedb.org/test", method="GET")
            flow = Flow(request, None)
            instance.request(flow)
            self.assertIsNotNone(flow.response)
            self.assertEqual(flow.response.status_code, 200)

    def test_replay_request_hook_kills_unmatched(self):
        """The request() hook kills an unmatched provider request with 502
        and writes it to the unmatched report before any upstream connection."""
        with tempfile.TemporaryDirectory() as directory:
            cassette = Path(directory) / "cassette.jsonl"
            report = Path(directory) / "unmatched.txt"
            cassette.write_text(json.dumps({"type": "provenance"}) + "\n")
            self.ctx.options.provider_replay_cassette = str(cassette)
            self.ctx.options.provider_replay_unmatched_report = str(report)
            instance = self.addon.ProviderReplay()
            instance._load_flows()
            self.assertEqual(len(instance.flows), 0)
            request = Request("https://api.themoviedb.org/unmatched", method="GET")
            flow = Flow(request, None)
            instance.request(flow)
            self.assertIsNotNone(flow.response)
            self.assertEqual(flow.response.status_code, 502)
            self.assertTrue(report.exists())
            self.assertIn("api.themoviedb.org", report.read_text())

    def test_replay_request_hook_kills_omdb_probe(self):
        """The request() hook kills an unmatched OMDb provider request with
        502, writes the unmatched report, and makes no upstream request.
        This covers the security finding: OMDb is a provider host, so the
        replay proxy must block it instead of allowing live egress."""
        with tempfile.TemporaryDirectory() as directory:
            cassette = Path(directory) / "cassette.jsonl"
            report = Path(directory) / "unmatched.txt"
            cassette.write_text(json.dumps({"type": "provenance"}) + "\n")
            self.ctx.options.provider_replay_cassette = str(cassette)
            self.ctx.options.provider_replay_unmatched_report = str(report)
            instance = self.addon.ProviderReplay()
            instance._load_flows()
            self.assertEqual(len(instance.flows), 0)
            request = Request("https://www.omdbapi.com/", method="GET")
            flow = Flow(request, None)
            instance.request(flow)
            self.assertIsNotNone(flow.response)
            self.assertEqual(flow.response.status_code, 502)
            self.assertTrue(report.exists())
            self.assertIn("www.omdbapi.com", report.read_text())

    def test_replay_request_hook_blocks_non_provider_host(self):
        """The request() hook blocks a non-provider request with 502 so no
        upstream connection opens. No unmatched-report entry is written and
        no cassette record is created. Non-provider flows do not count
        toward the gate verdict."""
        with tempfile.TemporaryDirectory() as directory:
            cassette = Path(directory) / "cassette.jsonl"
            report = Path(directory) / "unmatched.txt"
            cassette.write_text(json.dumps({"type": "provenance"}) + "\n")
            self.ctx.options.provider_replay_cassette = str(cassette)
            self.ctx.options.provider_replay_unmatched_report = str(report)
            instance = self.addon.ProviderReplay()
            instance._load_flows()
            request = Request("https://repo.jellyfin.org/files/plugin/manifest.json", method="GET")
            flow = Flow(request, None)
            instance.request(flow)
            self.assertIsNotNone(flow.response)
            self.assertEqual(flow.response.status_code, 502)
            self.assertFalse(report.exists())
            lines = cassette.read_text().strip().splitlines()
            self.assertEqual(len(lines), 1)

    def test_replay_response_hook_does_nothing(self):
        """In replay mode, the response() hook does not record or replay."""
        with tempfile.TemporaryDirectory() as directory:
            cassette = Path(directory) / "cassette.jsonl"
            self.ctx.options.provider_replay_cassette = str(cassette)
            instance = self.addon.ProviderReplay()
            request = Request("https://api.themoviedb.org/test")
            response = Response(b'{"ok": true}', {"content-type": "application/json"})
            flow = Flow(request, response)
            instance.response(flow)
            self.assertFalse(cassette.exists())

    def test_record_response_hook_captures_flow(self):
        """In record mode, the response() hook captures the flow."""
        with tempfile.TemporaryDirectory() as directory:
            cassette = Path(directory) / "cassette.jsonl"
            self.ctx.options.provider_replay_mode = "record"
            self.ctx.options.provider_replay_cassette = str(cassette)
            instance = self.addon.ProviderReplay()
            request = Request("https://api.themoviedb.org/test")
            response = Response(b'{"ok": true}', {"content-type": "application/json"})
            flow = Flow(request, response)
            instance.response(flow)
            self.assertTrue(cassette.exists())
            lines = cassette.read_text().strip().splitlines()
            self.assertEqual(len(lines), 1)
            record = json.loads(lines[0])
            self.assertEqual(record["host"], "api.themoviedb.org")
            self.assertEqual(record["status"], 200)

    def test_record_response_hook_skips_non_provider_host(self):
        """In record mode, the response() hook does not capture flows to
        non-provider hosts."""
        with tempfile.TemporaryDirectory() as directory:
            cassette = Path(directory) / "cassette.jsonl"
            self.ctx.options.provider_replay_mode = "record"
            self.ctx.options.provider_replay_cassette = str(cassette)
            instance = self.addon.ProviderReplay()
            request = Request("https://repo.jellyfin.org/files/plugin/manifest.json")
            response = Response(b'{"ok": true}', {"content-type": "application/json"})
            flow = Flow(request, response)
            instance.response(flow)
            self.assertFalse(cassette.exists())

    def test_record_request_hook_does_nothing(self):
        """In record mode, the request() hook does not replay."""
        with tempfile.TemporaryDirectory() as directory:
            cassette = Path(directory) / "cassette.jsonl"
            self.ctx.options.provider_replay_mode = "record"
            self.ctx.options.provider_replay_cassette = str(cassette)
            instance = self.addon.ProviderReplay()
            request = Request("https://api.themoviedb.org/test")
            flow = Flow(request, None)
            instance.request(flow)
            self.assertIsNone(flow.response)

    def test_is_provider_host_known_host(self):
        """is_provider_host returns True for known provider hosts."""
        self.assertTrue(self.addon.is_provider_host("api.themoviedb.org"))
        self.assertTrue(self.addon.is_provider_host("image.tmdb.org"))
        self.assertTrue(self.addon.is_provider_host("musicbrainz.org"))
        self.assertTrue(self.addon.is_provider_host("www.theaudiodb.com"))
        self.assertTrue(self.addon.is_provider_host("www.omdbapi.com"))

    def test_is_provider_host_unknown_host(self):
        """is_provider_host returns False for non-provider hosts."""
        self.assertFalse(self.addon.is_provider_host("repo.jellyfin.org"))
        self.assertFalse(self.addon.is_provider_host("nyc1.mirror.jellyfin.org"))
        self.assertFalse(self.addon.is_provider_host("example.com"))


if __name__ == "__main__":
    unittest.main()
