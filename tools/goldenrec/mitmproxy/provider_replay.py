"""mitmproxy addon that records and replays provider (external API) flows.

Record mode captures flows to provider hosts into a JSONL cassette. Replay
mode answers every request locally so no flow opens an upstream connection.
Matched provider requests receive cassette responses. Unmatched provider
requests receive 502 and one unmatched-report entry. Non-provider requests
receive 502 with no unmatched-report entry and no cassette record. The gate
wrapper fails when the report contains any flow to a provider host. This
proves the gate cannot reach a live provider API. Non-provider flows do not
count toward the gate verdict.

Cassette format (one JSON object per line):
  - First line: provenance header (type "provenance").
  - Flow records: {"method", "host", "path", "query", "status",
    "response_headers", "response_body"}.
  - Query and path values are normalized at record and at match time: the
    TMDb api_key query value and the AudioDB path key segment are replaced
    with a fixed redaction token. Headers are not part of the match key.
  - Credential redaction: the sanitizer removes cookies and auth headers
    and replaces every API key with the redaction token.
  - Image bytes: the sanitizer replaces image bodies with one deterministic
    small JPEG. Both servers replay identical bytes, so derived metadata
    stays comparable.
"""
import base64
import hashlib
import json
import re
from pathlib import Path
from urllib.parse import parse_qsl, urlencode, urlsplit

from mitmproxy import ctx, http

REDACTION_TOKEN = "<redacted>"
TMDB_API_KEY_PARAMS = {"api_key", "apikey"}
AUDIODB_KEY_SEGMENT_PATTERN = re.compile(r"/api/v1/json/[^/]+/")

# Provider hosts that the cassette records and the gate counts. Non-provider
# egress (plugin repository, telemetry) is neither recorded nor counted.
PROVIDER_HOSTS = frozenset({
    "api.themoviedb.org",
    "image.tmdb.org",
    "musicbrainz.org",
    "www.theaudiodb.com",
    "www.omdbapi.com",
})
CREDENTIAL_PATTERNS = [
    re.compile(r"Bearer\s+[A-Za-z0-9._\-]+", re.IGNORECASE),
    re.compile(r"[?&]api_key=[A-Za-z0-9._\-]+", re.IGNORECASE),
    re.compile(r'\\"?api_key\\"?\s*:\s*\\"?[A-Za-z0-9._\-]+\\"?', re.IGNORECASE),
    re.compile(r"/json/[A-Za-z0-9]{16,}/", re.IGNORECASE),
]

# Minimal 1x1 white JPEG (base64). Both hosts receive identical bytes, so
# derived metadata (dimensions, aspect ratio, blurhash) stays comparable.
DETERMINISTIC_JPEG = base64.b64decode(
    "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P0D/wAAKCAABAAEBARH/xAAFAAEA/9oABQEAAAD/2Q=="
)


def is_provider_host(host):
    """Return True if the host is a known provider API host. Non-provider
    egress (plugin repository, telemetry) is excluded so it cannot make the
    gate fail."""
    return (host or "").lower() in PROVIDER_HOSTS


def normalize_query(url):
    """Replace the TMDb api_key query value with the redaction token. Return
    the normalized query string."""
    parsed = urlsplit(url)
    pairs = []
    for key, value in parse_qsl(parsed.query, keep_blank_values=True):
        if key.lower() in TMDB_API_KEY_PARAMS:
            value = REDACTION_TOKEN
        pairs.append((key, value))
    return urlencode(pairs, safe="<>")


def normalize_path(url):
    """Replace the AudioDB API key path segment with the redaction token."""
    parsed = urlsplit(url)
    return AUDIODB_KEY_SEGMENT_PATTERN.sub(
        f"/api/v1/json/{REDACTION_TOKEN}/", parsed.path
    )


def match_key(request):
    """Build the match key for a request: method + host + path + normalized
    query. The TMDb api_key query value and AudioDB path key segment are
    replaced with the redaction token at match time."""
    parsed = urlsplit(request.url)
    host = (parsed.hostname or "").lower()
    path = normalize_path(request.url)
    query = normalize_query(request.url)
    return f"{request.method}|{host}|{path}|{query}"


def redact_credentials(text):
    """Replace every credential pattern in text with the redaction token."""
    text = re.compile(r"Bearer\s+[A-Za-z0-9._\-]+", re.IGNORECASE).sub(
        lambda m: "Bearer " + REDACTION_TOKEN, text)
    text = re.compile(r"[?&]api_key=[A-Za-z0-9._\-]+", re.IGNORECASE).sub(
        lambda m: m.group()[:m.group().index("=") + 1] + REDACTION_TOKEN, text)
    text = re.compile(r'"api_key"\s*:\s*"[A-Za-z0-9._\-]+"', re.IGNORECASE).sub(
        lambda m: '"api_key":"' + REDACTION_TOKEN + '"', text)
    text = re.compile(r"/json/[A-Za-z0-9]{16,}/", re.IGNORECASE).sub(
        f"/json/{REDACTION_TOKEN}/", text)
    return text


def is_image_response(flow):
    """Return True if the response content type is an image."""
    content_type = flow.response.headers.get("content-type", "")
    return content_type.lower().startswith("image/")


def sanitize_flow(flow):
    """Remove cookies and auth headers and replace image bodies with the
    deterministic JPEG. Apply credential redaction to text bodies."""
    for header in list(flow.request.headers.keys()):
        lower = header.lower()
        if lower in ("cookie", "authorization", "x-emby-authorization",
                      "x-emby-token", "x-mediabrowser-token"):
            del flow.request.headers[header]
    for header in list(flow.response.headers.keys()):
        if header.lower() in ("set-cookie",):
            del flow.response.headers[header]
    if is_image_response(flow):
        flow.response.content = DETERMINISTIC_JPEG
        flow.response.headers["content-type"] = "image/jpeg"
        flow.response.headers["content-length"] = str(len(DETERMINISTIC_JPEG))
    elif flow.response.content:
        try:
            text = flow.response.get_text(strict=False)
            redacted = redact_credentials(text)
            if redacted != text:
                flow.response.set_text(redacted)
        except Exception:
            pass


def encode_body(response):
    """Encode a response body for cassette storage."""
    content_type = response.headers.get("content-type", "")
    if content_type.lower().startswith("image/"):
        return {"type": "image"}
    if response.content:
        try:
            return {"type": "text", "data": response.get_text(strict=False)}
        except Exception:
            return {"type": "bytes", "sha256": hashlib.sha256(response.content).hexdigest()}
    return {"type": "empty"}


def decode_body(stored):
    """Decode a stored body back to bytes."""
    if not stored:
        return b""
    kind = stored.get("type")
    if kind == "image":
        return DETERMINISTIC_JPEG
    if kind == "text":
        return stored.get("data", "").encode("utf-8")
    return b""


def load_cassette(path):
    """Load a cassette file and return a dict keyed by match key. Skip the
    provenance header line (type 'provenance')."""
    flows = {}
    with open(path, encoding="utf-8") as handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            record = json.loads(line)
            if record.get("type") == "provenance":
                continue
            key = f"{record['method']}|{record['host']}|{record['path']}|{record.get('query', '')}"
            flows[key] = record
    return flows


def scan_cassette_for_credentials(path):
    """Scan a cassette file for credential material. Return a list of
    findings as (line number, pattern string, snippet) tuples."""
    findings = []
    with open(path, encoding="utf-8") as handle:
        for number, line in enumerate(handle, 1):
            for pattern in CREDENTIAL_PATTERNS:
                for match in pattern.finditer(line):
                    if REDACTION_TOKEN in match.group():
                        continue
                    findings.append((number, pattern.pattern, match.group()[:80]))
    return findings


class ProviderReplay:
    """mitmproxy addon: record provider flows to a cassette, or replay from
    a cassette and kill unmatched requests. Set by mitmproxy options:
    provider_replay_mode (record or replay), provider_replay_cassette (path),
    provider_replay_unmatched_report (path, replay mode only)."""

    def load(self, loader):
        loader.add_option("provider_replay_mode", str, "replay",
                          "Mode: record or replay")
        loader.add_option("provider_replay_cassette", str, "",
                          "Path to the provider cassette JSONL file")
        loader.add_option("provider_replay_unmatched_report", str, "",
                          "Path to append unmatched flow reports (replay mode)")

    def configure(self, updated):
        if "provider_replay_cassette" in updated:
            self._load_flows()

    def request(self, flow: http.HTTPFlow):
        """In replay mode, serve the cassette response before any upstream
        connection. This prevents live provider traffic entirely."""
        mode = ctx.options.provider_replay_mode
        if mode == "replay":
            self._replay(flow)

    def response(self, flow: http.HTTPFlow):
        """In record mode, capture the flow after the upstream responds."""
        mode = ctx.options.provider_replay_mode
        if mode == "record":
            self._record(flow)

    def _load_flows(self):
        cassette = ctx.options.provider_replay_cassette
        if not cassette or not Path(cassette).exists():
            ctx.log.warn(f"provider_replay: cassette not found: {cassette}")
            self.flows = {}
            return
        self.flows = load_cassette(cassette)
        ctx.log.info(f"provider_replay: loaded {len(self.flows)} flows from {cassette}")

    def _record(self, flow):
        cassette = ctx.options.provider_replay_cassette
        if not cassette:
            return
        parsed = urlsplit(flow.request.url)
        host = (parsed.hostname or "").lower()
        if not is_provider_host(host):
            return
        sanitize_flow(flow)
        record = {
            "method": flow.request.method,
            "host": host,
            "path": normalize_path(flow.request.url),
            "query": normalize_query(flow.request.url),
            "status": flow.response.status_code,
            "response_headers": {
                k: v for k, v in flow.response.headers.items()
                if k.lower() in ("content-type",)
            },
            "response_body": encode_body(flow.response),
        }
        Path(cassette).parent.mkdir(parents=True, exist_ok=True)
        with open(cassette, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(record, sort_keys=True, separators=(",", ":")) + "\n")

    def _replay(self, flow):
        if not hasattr(self, "flows"):
            self._load_flows()
        parsed = urlsplit(flow.request.url)
        host = (parsed.hostname or "").lower()
        if not is_provider_host(host):
            self._kill_non_provider(flow)
            return
        key = match_key(flow.request)
        record = self.flows.get(key)
        if record is None:
            self._kill_unmatched(flow)
            return
        flow.response = http.Response.make(
            record["status"],
            decode_body(record.get("response_body")),
            record.get("response_headers", {"content-type": "application/json"}),
        )

    def _kill_non_provider(self, flow):
        """Block a non-provider request with 502 so no upstream connection
        opens. Write no unmatched-report entry and no cassette record. Non-
        provider flows do not count toward the gate verdict."""
        flow.response = http.Response.make(
            502, b"non-provider request blocked in replay mode",
            {"content-type": "text/plain"}
        )

    def _kill_unmatched(self, flow):
        report = ctx.options.provider_replay_unmatched_report
        parsed = urlsplit(flow.request.url)
        host = (parsed.hostname or "").lower()
        if report:
            with open(report, "a", encoding="utf-8") as handle:
                handle.write(json.dumps({
                    "method": flow.request.method,
                    "host": host,
                    "path": normalize_path(flow.request.url),
                    "query": normalize_query(flow.request.url),
                }, sort_keys=True) + "\n")
        flow.response = http.Response.make(
            502, b"unmatched provider request", {"content-type": "text/plain"}
        )


addons = [ProviderReplay()]
