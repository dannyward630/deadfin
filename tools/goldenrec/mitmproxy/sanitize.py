"""mitmproxy addon that persists a minimally replayable, secret-free JSONL trace.

Run only against the local synthetic stock reference. It removes credentials and
cookies before writing; it does not make a real-library capture safe to commit.
"""
import json
import os
from pathlib import Path
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

from mitmproxy import ctx, http

_SECRET_KEY_NAMES = {"accesstoken", "apikey", "token", "password", "pw", "authorization", "xembytoken", "xmediabrowsertoken", "cookie", "setcookie", "secret"}
_KEEP_HEADERS = {"content-type", "accept", "upgrade"}


def normalize_key(key):
    return "".join(character for character in str(key).lower() if character.isalnum())


def is_secret_key(key):
    return normalize_key(key) in _SECRET_KEY_NAMES


def scrub_url_scalar(value):
    parsed = urlsplit(value)
    query = parse_qsl(parsed.query, keep_blank_values=True)
    if not any(is_secret_key(key) for key, _ in query):
        return value
    cleaned = [(key, item) for key, item in query if not is_secret_key(key)]
    return urlunsplit((parsed.scheme, parsed.netloc, parsed.path, urlencode(cleaned), parsed.fragment))


def scrub(value):
    if isinstance(value, dict):
        return {str(key): scrub(item) for key, item in value.items() if not is_secret_key(key)}
    if isinstance(value, list):
        return [scrub(item) for item in value]
    if isinstance(value, str):
        return scrub_url_scalar(value)
    return value


def clean_body(message):
    if not message.content:
        return None
    content_type = message.headers.get("content-type", "").lower()
    if "json" in content_type:
        try:
            return scrub(json.loads(message.get_text(strict=False)))
        except (UnicodeDecodeError, json.JSONDecodeError):
            return {"redacted": "invalid-json-body"}
    return {"redacted": "non-json-body", "bytes": len(message.content)}


def query_has_secret(url):
    return any(is_secret_key(key) for key, _ in parse_qsl(urlsplit(url).query, keep_blank_values=True))


def clean_path(url):
    parsed = urlsplit(url)
    query = [(key, value) for key, value in parse_qsl(parsed.query, keep_blank_values=True) if not is_secret_key(key)]
    return parsed.path + (("?" + urlencode(query)) if query else "")


def request_has_auth(request):
    return any(normalize_key(key) in {"authorization", "xembyauthorization", "xembytoken", "xmediabrowsertoken", "cookie"} for key in request.headers.keys()) or query_has_secret(request.url)


def is_authentication_payload(request):
    return "/authenticate" in urlsplit(request.url).path.lower()


def origin(url):
    parsed = urlsplit(url)
    if parsed.scheme not in {"http", "https"} or parsed.hostname is None:
        raise ValueError(f"capture target must be an absolute HTTP(S) URL: {url!r}")
    return parsed.scheme, parsed.hostname.lower(), parsed.port or (443 if parsed.scheme == "https" else 80)


class GoldenrecSanitizer:
    def load(self, loader):
        loader.add_option("goldenrec_capture_output", str, "", "Path to sanitized goldenrec JSONL output")
        loader.add_option("goldenrec_capture_target", str, "http://127.0.0.1:8096", "Synthetic Jellyfin origin to sanitize")

    def response(self, flow: http.HTTPFlow):
        output = ctx.options.goldenrec_capture_output
        if not output:
            return
        target = getattr(ctx.options, "goldenrec_capture_target", "")
        if target and origin(flow.request.url) != origin(target):
            return
        requires_auth = request_has_auth(flow.request)
        authentication_payload = is_authentication_payload(flow.request)
        response_body = clean_body(flow.response)
        if requires_auth:
            response_body = {"redacted": "authenticated-response-body"}
        elif authentication_payload:
            response_body = {"redacted": "authentication-response-body"}
        record = {
            "method": flow.request.method,
            "requires_auth": requires_auth,
            "auth_role": "captured-authenticated" if requires_auth else None,
            "path": clean_path(flow.request.url),
            "request_headers": {key: value for key, value in flow.request.headers.items() if key.lower() in _KEEP_HEADERS},
            "request_body": clean_body(flow.request),
            "status": flow.response.status_code,
            "response_headers": {key: value for key, value in flow.response.headers.items() if key.lower() in _KEEP_HEADERS},
            "response_body": response_body,
        }
        path = Path(output)
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(record, sort_keys=True, separators=(",", ":")) + "\n")


addons = [GoldenrecSanitizer()]
