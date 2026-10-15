import json
import http.client
import os
import shutil
import subprocess
import tempfile
import threading
import sys
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlsplit
from urllib.request import urlopen


ROOT = Path(__file__).resolve().parents[1]
SEED = ROOT / "seed.sh"

# The production manifest is the canonical stock contract. Tests derive their
# fake items from it so a manifest update cannot silently desynchronize a
# second handwritten 43-entry fixture.
EXPECTED_STOCK_IDENTITIES = tuple(
    json.loads((ROOT / "fixtures" / "expected-inventory.json").read_text(encoding="utf-8"))
)
BOXSET_MEMBER_IDENTITIES = frozenset(
    identity for identity in EXPECTED_STOCK_IDENTITIES if "/Synthetic Collection [boxset]/" in identity
)
BOXSET_REPLACEMENT_IDENTITY = "BoxSet:/media/movies/Synthetic Collection [boxset]"
COMMON_MOVIE_IDENTITIES = frozenset(
    identity
    for identity in EXPECTED_STOCK_IDENTITIES
    if identity.startswith("Movie:") and identity not in BOXSET_MEMBER_IDENTITIES
)


LOCAL_LIBRARY_OPTIONS = {
    "EnableInternetProviders": False,
    "DisabledLocalMetadataReaders": [],
    "TypeOptions": [
        {
            "Type": "Movie",
            "MetadataFetchers": [],
            "ImageFetchers": [],
            "SimilarItemProviders": [],
        }
    ],
}
REMOTE_LIBRARY_OPTIONS = {
    "EnableInternetProviders": True,
    "DisabledLocalMetadataReaders": ["Nfo"],
    "TypeOptions": [
        {
            "Type": "Movie",
            "MetadataFetchers": ["The Movie Database"],
            "ImageFetchers": ["The Movie Database"],
            "SimilarItemProviders": ["The Movie Database"],
        }
    ],
}
LIBRARIES = (
    ("Golden Movies", "movies", "/media/movies"),
    ("Golden TV", "tvshows", "/media/tv"),
    ("Golden Music", "music", "/media/music"),
    ("Golden Photos", "homevideos", "/media/photos"),
)


def library(name, collection_type, path, options):
    return {
        "Name": name,
        "ItemId": f"library-{name.rsplit(' ', 1)[-1].lower()}",
        "CollectionType": collection_type,
        "Locations": [path],
        "RefreshStatus": "Idle",
        "LibraryOptions": options,
    }


def local_only(options):
    """The observable stock options must prohibit every remote provider family."""
    if options.get("EnableInternetProviders") is not False:
        return False
    if options.get("DisabledLocalMetadataReaders") not in ([], None):
        return False
    type_options = options.get("TypeOptions")
    if not type_options:
        return False
    return all(
        option.get(key) == []
        for option in type_options
        for key in ("MetadataFetchers", "ImageFetchers", "SimilarItemProviders")
    )


class SeedServer:
    """A loopback Jellyfin seam whose /Items pagination is capped at 200 results."""

    def __init__(self, libraries, items, *, boxset_member_ids=None):
        self.libraries = libraries
        self.items = items
        self.boxset_member_ids = (
            {item["Id"] for item in items if f"{item.get('Type')}:{item.get('Path')}" in BOXSET_MEMBER_IDENTITIES}
            if boxset_member_ids is None
            else set(boxset_member_ids)
        )
        self.collection_members = set()
        self.item_response_indexes = set()
        self.calls = []
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                parsed = urlsplit(self.path)
                outer.calls.append(("GET", parsed.path, parsed.query, None))
                if parsed.path == "/System/Info/Public":
                    self.send_json(200, {"Version": "12.0.0", "StartupWizardCompleted": True})
                elif parsed.path == "/Startup/User":
                    self.send_json(200, {})
                elif parsed.path == "/Library/VirtualFolders":
                    self.send_json(200, outer.libraries)
                elif parsed.path == "/Items":
                    self.send_json(200, outer.items_response(parsed.query))
                else:
                    self.send_json(404, {"error": parsed.path})

            def do_POST(self):
                parsed = urlsplit(self.path)
                size = int(self.headers.get("Content-Length", "0"))
                payload = json.loads(self.rfile.read(size) or b"{}")
                outer.calls.append(("POST", parsed.path, parsed.query, payload))
                if parsed.path == "/Users/AuthenticateByName":
                    self.send_json(200, {"AccessToken": "seed-token", "User": {"Id": "seed-user"}})
                elif parsed.path == "/Library/VirtualFolders/LibraryOptions":
                    identifier = payload.get("Id")
                    match = next((entry for entry in outer.libraries if entry.get("ItemId") == identifier), None)
                    if match is None:
                        self.send_json(404, {"error": "unknown library"})
                    else:
                        match["LibraryOptions"] = payload.get("LibraryOptions")
                        self.send_json(204, None)
                elif parsed.path == "/Library/VirtualFolders":
                    # The hardening contract permits creation, but requires a later explicit options update.
                    name = parsed.query.split("name=", 1)[1].split("&", 1)[0].replace("%20", " ")
                    collection_type = parsed.query.split("collectionType=", 1)[1].split("&", 1)[0]
                    path = parsed.query.split("paths=", 1)[1].split("&", 1)[0]
                    outer.libraries.append(library(name, collection_type, path, {}))
                    self.send_json(204, None)
                elif parsed.path == "/Library/Refresh":
                    if all(local_only(entry.get("LibraryOptions") or {}) for entry in outer.libraries):
                        self.send_json(204, None)
                    else:
                        self.send_json(409, {"error": "scan attempted before local-only provider policy"})
                elif parsed.path.startswith("/Collections"):
                    outer.record_collection_membership(parsed.query, payload)
                    self.send_json(204, None)
                else:
                    self.send_json(404, {"error": parsed.path})

            def send_json(self, status, payload):
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                if payload is not None:
                    self.wfile.write(json.dumps(payload).encode("utf-8"))

            def log_message(self, format, *args):
                pass

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_):
        self.httpd.shutdown()
        self.thread.join()
        self.httpd.server_close()

    @property
    def base_url(self):
        host, port = self.httpd.server_address
        return f"http://{host}:{port}"

    def items_response(self, query):
        params = parse_qs(query)
        requested_types = {
            item_type
            for raw_types in params.get("IncludeItemTypes", [])
            for item_type in raw_types.split(",")
            if item_type
        }
        items = [item for item in self.items if not requested_types or item.get("Type") in requested_types]
        collapse = next(
            (
                values[0].casefold() == "true"
                for key, values in params.items()
                if key.casefold() == "collapseboxsetitems" and values
            ),
            False,
        )
        if collapse and self.collection_members:
            items = [item for item in items if item.get("Id") not in self.collection_members]
            if "Movie" in requested_types and "BoxSet" not in requested_types:
                items.extend(
                    item
                    for item in self.items
                    if f"{item.get('Type')}:{item.get('Path')}" == BOXSET_REPLACEMENT_IDENTITY
                )
        start = int(next((values[0] for key, values in params.items() if key.casefold() == "startindex"), "0"))
        requested_limit = int(next((values[0] for key, values in params.items() if key.casefold() == "limit"), "200"))
        page = items[start : start + min(requested_limit, 200)]
        self.item_response_indexes.update(range(start, start + len(page)))
        return {"Items": page, "StartIndex": start, "TotalRecordCount": len(items)}

    def record_collection_membership(self, query, payload):
        """Accept public collection APIs without coupling the test to one request encoding."""
        candidates = []
        for key, values in parse_qs(query).items():
            if key.casefold() in {"ids", "itemids"}:
                candidates.extend(value for raw in values for value in raw.split(","))
        if isinstance(payload, dict):
            for key in ("Ids", "ItemIds"):
                value = payload.get(key)
                if isinstance(value, str):
                    candidates.extend(value.split(","))
                elif isinstance(value, list):
                    candidates.extend(value for value in value if isinstance(value, str))
        self.collection_members.update(set(candidates) & self.boxset_member_ids)


class TransientScanPaginationServer(SeedServer):
    """Returns one inconsistent scan page before exposing the canonical corpus."""

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.inconsistent_responses = 0
        self.stable_recursive_responses = 0

    def items_response(self, query):
        response = super().items_response(query)
        if self.inconsistent_responses == 0:
            self.inconsistent_responses += 1
            incomplete_items = response["Items"][:17]
            return response | {"Items": incomplete_items, "TotalRecordCount": len(incomplete_items)}
        if "recursive=true" in query.casefold():
            self.stable_recursive_responses += 1
        return response


class TransientPaginationOverrunServer(SeedServer):
    """Returns one page that overruns TotalRecordCount before the corpus settles."""

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.inconsistent_responses = 0
        self.stable_recursive_responses = 0

    def items_response(self, query):
        response = super().items_response(query)
        if self.inconsistent_responses == 0:
            self.inconsistent_responses += 1
            return response | {"TotalRecordCount": 17}
        if "recursive=true" in query.casefold():
            self.stable_recursive_responses += 1
        return response


class SnapshotPaginationFailureServer(SeedServer):
    """Keeps polling stable but makes the post-boxset snapshot page inconsistent."""

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.boxset_queries = 0
        self.snapshot_inconsistent_responses = 0

    def items_response(self, query):
        response = super().items_response(query)
        normalized_query = query.casefold()
        if "collapseboxsetitems=" in normalized_query:
            self.boxset_queries += 1
        elif self.boxset_queries == 2 and "recursive=true" in normalized_query:
            self.snapshot_inconsistent_responses += 1
            return response | {"TotalRecordCount": 17}
        return response


class SentinelProxy:
    """A forwarding loopback proxy that records requests which escaped a client bypass."""

    def __init__(self):
        self.calls = []
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def _forward(self):
                size = int(self.headers.get("Content-Length", "0"))
                body = self.rfile.read(size)
                outer.calls.append(
                    {
                        "method": self.command,
                        "target": self.path,
                        "authorization": self.headers.get("Authorization"),
                        "body": body,
                    }
                )
                target = urlsplit(self.path)
                if target.scheme != "http" or not target.hostname:
                    self.send_error(502, "sentinel received a non-HTTP proxy request")
                    return
                # The sentinel forwards only to the loopback test servers.
                if target.hostname not in ("127.0.0.1", "::1", "localhost"):
                    self.send_error(502, "sentinel forwards only to loopback")
                    return
                connection = http.client.HTTPConnection(target.hostname, target.port or 80, timeout=2)
                try:
                    headers = {
                        key: value
                        for key, value in self.headers.items()
                        if key.lower() not in {"host", "proxy-connection", "connection"}
                    }
                    path = target.path or "/"
                    if target.query:
                        path += f"?{target.query}"
                    connection.request(self.command, path, body=body, headers=headers)
                    response = connection.getresponse()
                    response_body = response.read()
                    self.send_response(response.status)
                    for key, value in response.getheaders():
                        if key.lower() not in {"connection", "transfer-encoding"}:
                            self.send_header(key, value)
                    self.end_headers()
                    self.wfile.write(response_body)
                finally:
                    connection.close()

            do_GET = _forward
            do_POST = _forward

            def do_CONNECT(self):
                outer.calls.append(
                    {
                        "method": "CONNECT",
                        "target": self.path,
                        "authorization": self.headers.get("Proxy-Authorization"),
                        "body": b"",
                    }
                )
                self.send_error(502, "HTTPS proxying is not part of this loopback test")

            def log_message(self, format, *args):
                pass

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_):
        self.httpd.shutdown()
        self.thread.join()
        self.httpd.server_close()

    @property
    def url(self):
        host, port = self.httpd.server_address
        return f"http://{host}:{port}"

class ProviderHardeningTests(unittest.TestCase):
    maxDiff = None

    @staticmethod
    def canonical_identity(item):
        return f"{item['Type']}:{item['Path']}"

    def stock_items(self):
        return [
            {"Id": f"stock-{number}", "Type": identity.split(":", 1)[0], "Path": identity.split(":", 1)[1]}
            for number, identity in enumerate(EXPECTED_STOCK_IDENTITIES)
        ]

    def media_items(self, remote_provider=False):
        items = self.stock_items()
        if remote_provider:
            items[0]["ProviderIds"] = {"Tmdb": "remote-metadata-evidence"}
        return items

    def run_seed(self, server, *, base_url=None, admin=None, password=None, environment=None, capture_child_argv=False, snapshot_victims=False):
        with tempfile.TemporaryDirectory() as temporary:
            temporary = Path(temporary)
            goldenrec = temporary / "goldenrec"
            goldenrec.mkdir()
            shutil.copy2(SEED, goldenrec / "seed.sh")
            shutil.copy2(ROOT / "compose.yaml", goldenrec / "compose.yaml")
            fixtures = goldenrec / "fixtures"
            fixtures.mkdir()
            (fixtures / "expected-inventory.json").write_text(
                json.dumps(EXPECTED_STOCK_IDENTITIES, ensure_ascii=False) + "\n", encoding="utf-8"
            )
            if (ROOT / "provider-policy.json").exists():
                shutil.copy2(ROOT / "provider-policy.json", goldenrec / "provider-policy.json")
            victims = {}
            if snapshot_victims:
                snapshots = goldenrec / "state" / "snapshots"
                snapshots.mkdir(parents=True)
                for name in ("id-map.json", "libraries.json", "metadata.json", "DATABASES.sha256"):
                    victim = temporary / f"victim-{name}"
                    victim.write_text(f"preserve {name}", encoding="utf-8")
                    (snapshots / name).symlink_to(victim)
                    victims[name] = victim
            fake_bin = temporary / "bin"
            fake_bin.mkdir()
            child_argv_log = temporary / "child-argv.jsonl"
            python = fake_bin / "python3"
            python.write_text(
                f"#!{sys.executable}\n"
                "import json, os, sys\n"
                "with open(os.environ['GOLDENREC_CHILD_ARGV_LOG'], 'a', encoding='utf-8') as output:\n"
                "    output.write(json.dumps({'tool': 'python3', 'args': sys.argv[1:], 'environment': {name: os.environ.get(name) for name in ('JELLYFIN_ADMIN', 'JELLYFIN_PASSWORD', 'ADMIN', 'PASSWORD')}}) + '\\n')\n"
                "os.execv(os.environ['GOLDENREC_REAL_PYTHON'], [os.environ['GOLDENREC_REAL_PYTHON'], *sys.argv[1:]])\n",
                encoding="utf-8",
            )
            python.chmod(0o755)
            curl = fake_bin / "curl"
            curl.write_text(
                f"#!{sys.executable}\n"
                "import json, os, sys\n"
                "with open(os.environ['GOLDENREC_CHILD_ARGV_LOG'], 'a', encoding='utf-8') as output:\n"
                "    output.write(json.dumps({'tool': 'curl', 'args': sys.argv[1:], 'environment': {name: os.environ.get(name) for name in ('JELLYFIN_ADMIN', 'JELLYFIN_PASSWORD', 'ADMIN', 'PASSWORD')}}) + '\\n')\n"
                "os.execv(os.environ['GOLDENREC_REAL_CURL'], [os.environ['GOLDENREC_REAL_CURL'], *sys.argv[1:]])\n",
                encoding="utf-8",
            )
            curl.chmod(0o755)
            docker = fake_bin / "docker"
            docker.write_text(
                f"#!{sys.executable}\n"
                "import json, os, sys\n"
                "args = sys.argv[1:]\n"
                "with open(os.environ['GOLDENREC_CHILD_ARGV_LOG'], 'a', encoding='utf-8') as output:\n"
                "    output.write(json.dumps({'tool': 'docker', 'args': args, 'environment': {name: os.environ.get(name) for name in ('JELLYFIN_ADMIN', 'JELLYFIN_PASSWORD', 'ADMIN', 'PASSWORD')}}) + '\\n')\n"
                "if args == ['compose', 'version']:\n"
                "    raise SystemExit(0)\n"
                "if 'cp' in args:\n"
                "    destination = args[-1]\n"
                "    os.makedirs(destination, exist_ok=True)\n"
                "    open(os.path.join(destination, 'jellyfin.db'), 'wb').close()\n"
                "raise SystemExit(0)\n",
                encoding="utf-8",
            )
            docker.chmod(0o755)
            result = subprocess.run(
                [str(goldenrec / "seed.sh")],
                cwd=goldenrec,
                capture_output=True,
                text=True,
                env=(
                    os.environ
                    | {
                        "JELLYFIN_BASE_URL": base_url or server.base_url,
                        "JELLYFIN_ADMIN": admin or "GoldenrecAdmin",
                        "JELLYFIN_PASSWORD": password or "goldenrec-change-me",
                        "PATH": f"{fake_bin}{os.pathsep}{os.environ['PATH']}",
                        "GOLDENREC_CHILD_ARGV_LOG": str(child_argv_log),
                        "GOLDENREC_REAL_PYTHON": sys.executable,
                        "GOLDENREC_REAL_CURL": shutil.which("curl") or "/usr/bin/curl",
                    }
                    | (environment or {})
                ),
            )
            if capture_child_argv:
                result.child_argv = [json.loads(line) for line in child_argv_log.read_text(encoding="utf-8").splitlines()]
            if snapshot_victims:
                result.snapshot_victims = {name: victim.read_text(encoding="utf-8") for name, victim in victims.items()}
            return result

    @staticmethod
    def movie_query(server, collapse):
        with urlopen(
            f"{server.base_url}/Items?IncludeItemTypes=Movie&CollapseBoxSetItems={str(collapse).lower()}", timeout=2
        ) as response:
            return json.load(response)

    def test_seed_rejects_hostname_origin_before_credentials_leave_process(self):
        """Administrator secrets are permitted only for literal loopback origins, never localhost."""
        sentinel_admin = "seed-hostname-admin-sentinel"
        sentinel_password = "seed-hostname-password-sentinel"
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SeedServer(existing, self.media_items()) as server:
            localhost_origin = server.base_url.replace("127.0.0.1", "localhost")
            result = self.run_seed(
                server,
                base_url=localhost_origin,
                admin=sentinel_admin,
                password=sentinel_password,
            )
            calls = list(server.calls)

        combined = result.stdout + result.stderr
        self.assertEqual(len(calls), 0, "seed must reject a hostname before it can make any HTTP request")
        self.assertNotEqual(result.returncode, 0, "seed accepted localhost instead of a literal loopback origin")
        self.assertRegex(combined, r"(?i)(literal.*loopback|loopback.*(?:literal|origin)|origin.*loopback)")
        self.assertNotIn(sentinel_admin, combined)
        self.assertNotIn(sentinel_password, combined)

    def test_seed_bypasses_environment_proxies_for_every_stock_request(self):
        """A hostile proxy must not observe credentials for a validated literal-loopback stock peer."""
        sentinel_admin = "seed-proxy-admin-sentinel"
        sentinel_password = "seed-proxy-password-sentinel"
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SeedServer(existing, self.media_items()) as server, SentinelProxy() as proxy:
            result = self.run_seed(
                server,
                admin=sentinel_admin,
                password=sentinel_password,
                environment={
                    "HTTP_PROXY": proxy.url,
                    "http_proxy": proxy.url,
                    "NO_PROXY": "",
                    "no_proxy": "",
                },
            )
            proxy_calls = list(proxy.calls)
            stock_calls = list(server.calls)

        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        self.assertEqual(proxy_calls, [], "all seed curl paths must bypass environment proxies for the literal stock peer")
        authentication = [call for call in stock_calls if call[0:2] == ("POST", "/Users/AuthenticateByName")]
        self.assertEqual(len(authentication), 1, "the separately bound stock listener must receive the one authentication request")
        self.assertEqual(authentication[0][3], {"Username": sentinel_admin, "Pw": sentinel_password})
    def test_seed_delivers_exact_auth_bytes_without_exposing_credentials_in_child_argv(self):
        """Authentication bodies use stdin/file plumbing; no child command receives a secret argument."""
        sentinel_admin = "seed-argv-admin-sentinel"
        sentinel_password = "seed-argv-password-sentinel"
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SeedServer(existing, self.media_items()) as server:
            result = self.run_seed(
                server,
                admin=sentinel_admin,
                password=sentinel_password,
                capture_child_argv=True,
                environment={"ADMIN": "generic-admin-sentinel", "PASSWORD": "generic-password-sentinel"},
            )
            authentication = [call for call in server.calls if call[0:2] == ("POST", "/Users/AuthenticateByName")]

        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        self.assertEqual(len(authentication), 1)
        self.assertEqual(authentication[0][3], {"Username": sentinel_admin, "Pw": sentinel_password})
        logged_argv = "\n".join("\0".join(event["args"]) for event in result.child_argv)
        self.assertTrue(result.child_argv, "the fake python/curl/docker tools did not record child argv")
        self.assertNotIn(sentinel_admin, logged_argv)
        self.assertNotIn(sentinel_password, logged_argv)
        for event in result.child_argv:
            self.assertEqual(event["environment"], {"JELLYFIN_ADMIN": None, "JELLYFIN_PASSWORD": None, "ADMIN": None, "PASSWORD": None})
    def test_seed_snapshot_replaces_symlink_destinations_without_touching_victims(self):
        """Snapshot publication must not follow old id-map, library, metadata, or checksum symlinks."""
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SeedServer(existing, self.media_items()) as server:
            result = self.run_seed(server, snapshot_victims=True)

        self.assertNotEqual(result.returncode, 0, "seed accepted a symlink snapshot destination")
        self.assertEqual(
            result.snapshot_victims,
            {name: f"preserve {name}" for name in ("id-map.json", "libraries.json", "metadata.json", "DATABASES.sha256")},
        )

    def test_existing_matching_libraries_are_repaired_to_local_only_before_scan(self):
        """Name/path/type matches are insufficient; every library must be read back and repaired."""
        existing = [library(name, kind, path, REMOTE_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SeedServer(existing, self.media_items()) as server:
            result = self.run_seed(server)

        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        updates = [call for call in server.calls if call[1] == "/Library/VirtualFolders/LibraryOptions"]
        self.assertEqual(len(updates), len(LIBRARIES), "each existing matching library needs an options repair")
        self.assertTrue(all(local_only(entry["LibraryOptions"]) for entry in existing))
        refresh_index = next(index for index, call in enumerate(server.calls) if call[1] == "/Library/Refresh")
        self.assertTrue(all(server.calls.index(call) < refresh_index for call in updates))

    def test_seed_rejects_remote_provider_evidence_after_scan(self):
        """A stable item count is not a clean reference corpus when stock reports remote metadata."""
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SeedServer(existing, self.media_items(remote_provider=True)) as server:
            result = self.run_seed(server)

        self.assertNotEqual(result.returncode, 0, "seed accepted Tmdb provider evidence in a local-only corpus")
        combined = result.stdout + result.stderr
        self.assertRegex(combined, r"(?i)(remote|provider|Tmdb)")

    def test_seed_paginates_to_total_record_count_and_rejects_later_page_provider_evidence(self):
        """Every advertised item is validation input, including provider evidence at index 200."""
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        items = [
            {"Id": f"padding-{number}", "Type": "Movie", "Path": f"/media/movies/Padding {number}.mkv"}
            for number in range(200)
        ]
        items.append(
            {
                "Id": "provider-after-page-one",
                "Type": "Movie",
                "Path": "/media/movies/Remote Evidence.mkv",
                "ProviderIds": {"Tmdb": "remote-metadata-evidence"},
            }
        )
        with SeedServer(existing, items) as server:
            result = self.run_seed(server)

        with self.subTest("all pages through TotalRecordCount are read"):
            self.assertIn(200, server.item_response_indexes, "seed never consumed the item advertised after its first 200 results")
        with self.subTest("remote provider evidence from the later page is rejected"):
            self.assertNotEqual(result.returncode, 0, "seed accepted remote provider evidence beyond its first item page")

    def test_seed_retries_one_transient_scan_pagination_inconsistency(self):
        """A changing scan may briefly over-report its items before the canonical inventory settles."""
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with TransientPaginationOverrunServer(existing, self.stock_items()) as server:
            result = self.run_seed(server)

        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        self.assertEqual(server.inconsistent_responses, 1, "fixture did not inject the transient first scan page")
        self.assertGreaterEqual(
            server.stable_recursive_responses,
            3,
            "seed did not retry the scan and complete stable polling plus the final snapshot",
        )

    def test_seed_rejects_pagination_inconsistency_during_final_snapshot(self):
        """Only scan polling may retry inconsistent pagination; snapshot evidence remains strict."""
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SnapshotPaginationFailureServer(existing, self.stock_items()) as server:
            result = self.run_seed(server)

        self.assertEqual(server.boxset_queries, 2, "fixture did not reach final validation before snapshotting")
        self.assertEqual(server.snapshot_inconsistent_responses, 1, "fixture did not corrupt the final snapshot page")
        self.assertNotEqual(result.returncode, 0, "seed accepted inconsistent final snapshot pagination")
        self.assertRegex(result.stdout + result.stderr, r"item pagination.*overran TotalRecordCount 17")

    def test_seed_rejects_any_deviation_from_the_exact_canonical_stock_inventory(self):
        """The seed gate protects the named stock identities, not merely a minimum item count."""
        expected_items = self.stock_items()
        replaced = [item for item in expected_items if item["Id"] != "stock-0"] + [
            {"Id": "unexpected-replacement", "Type": "Movie", "Path": "/media/movies/Unexpected Replacement.mkv"}
        ]
        extra = expected_items + [{"Id": "unexpected-extra", "Type": "Movie", "Path": "/media/movies/Unexpected Extra.mkv"}]
        changed_type = [
            item | {"Type": "Movie" if item["Type"] != "Movie" else "Episode"} if item["Id"] == "stock-0" else item
            for item in expected_items
        ]
        cases = {
            "missing expected identity while retaining thirty media items": replaced,
            "additional canonical identity": extra,
            "type changed at an expected path": changed_type,
        }
        for name, items in cases.items():
            with self.subTest(name):
                existing = [library(library_name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for library_name, kind, path in LIBRARIES]
                with SeedServer(existing, items) as server:
                    result = self.run_seed(server)
                self.assertNotEqual(result.returncode, 0, f"seed accepted canonical inventory mutation: {name}")

    def test_seed_establishes_boxset_membership_that_replaces_members_when_movies_collapse(self):
        """Collapsed movies replace the three collection members with their physical BoxSet row."""
        items = self.stock_items()
        member_ids = {
            item["Id"] for item in items if self.canonical_identity(item) in BOXSET_MEMBER_IDENTITIES
        }
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SeedServer(existing, items, boxset_member_ids=member_ids) as server:
            result = self.run_seed(server)
            collapsed_response = self.movie_query(server, collapse=True)
            uncollapsed_response = self.movie_query(server, collapse=False)

        collapsed = {self.canonical_identity(item) for item in collapsed_response["Items"]}
        uncollapsed = {self.canonical_identity(item) for item in uncollapsed_response["Items"]}
        expected_collapsed = COMMON_MOVIE_IDENTITIES | {BOXSET_REPLACEMENT_IDENTITY}
        expected_uncollapsed = COMMON_MOVIE_IDENTITIES | BOXSET_MEMBER_IDENTITIES
        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        self.assertEqual(collapsed_response["TotalRecordCount"], len(expected_collapsed))
        self.assertEqual(uncollapsed_response["TotalRecordCount"], len(expected_uncollapsed))
        self.assertEqual(collapsed, expected_collapsed)
        self.assertEqual(uncollapsed, expected_uncollapsed)

    def test_default_seed_performs_one_snapshot_stop_copy_restart(self):
        """The default seed performs one snapshot stop, copy, and restart sequence."""
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SeedServer(existing, self.media_items()) as server:
            result = self.run_seed(server, capture_child_argv=True)
        docker_calls = [event["args"] for event in result.child_argv if event["tool"] == "docker"]
        snapshot_stops = [args for args in docker_calls if "stop" in args]
        snapshot_copies = [args for args in docker_calls if "cp" in args]
        snapshot_starts = [args for args in docker_calls if "start" in args]

        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        self.assertEqual(len(snapshot_stops), 1, "the default seed must stop the container exactly once for the snapshot")
        self.assertEqual(len(snapshot_copies), 1, "the default seed must copy the database exactly once for the snapshot")
        self.assertEqual(len(snapshot_starts), 1, "the default seed must restart the container exactly once for the snapshot")

    def test_snapshot_disabled_performs_no_stop_copy_restart(self):
        """GOLDENREC_SNAPSHOT=0 skips the snapshot stop, copy, and restart cycle."""
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SeedServer(existing, self.media_items()) as server:
            result = self.run_seed(server, capture_child_argv=True, environment={"GOLDENREC_SNAPSHOT": "0"})
        docker_calls = [event["args"] for event in result.child_argv if event["tool"] == "docker"]
        snapshot_ops = [args for args in docker_calls if any(op in args for op in ("stop", "cp", "start"))]

        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        self.assertEqual(snapshot_ops, [], "GOLDENREC_SNAPSHOT=0 must not stop, copy, or restart the container")

    def test_snapshot_empty_value_is_rejected(self):
        """An empty GOLDENREC_SNAPSHOT value is rejected with a clear error."""
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SeedServer(existing, self.media_items()) as server:
            result = self.run_seed(server, environment={"GOLDENREC_SNAPSHOT": ""})

        self.assertNotEqual(result.returncode, 0, "seed accepted an empty GOLDENREC_SNAPSHOT value")
        self.assertIn("GOLDENREC_SNAPSHOT must be 0 or 1, got an empty value", result.stderr)

    def test_transient_inventory_converges_without_mismatch_on_stderr(self):
        """A temporary incomplete inventory can converge without mismatch text on stderr."""
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with TransientScanPaginationServer(existing, self.stock_items()) as server:
            result = self.run_seed(server, environment={"GOLDENREC_SNAPSHOT": "0"})

        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        self.assertNotIn(
            "stock inventory differs from expected manifest",
            result.stderr,
            "a temporary inventory mismatch must not leak to stderr after convergence",
        )
        self.assertEqual(
            result.stdout.count("item_count=52"), 1,
            "the seed must print the final stable inventory evidence exactly once",
        )

    def test_persistent_mismatch_exits_nonzero_with_final_inventory_difference(self):
        """A persistent mismatch exits nonzero and includes the final inventory difference."""
        expected_items = self.stock_items()
        replaced = [item for item in expected_items if item["Id"] != "stock-0"] + [
            {"Id": "unexpected-replacement", "Type": "Movie", "Path": "/media/movies/Unexpected Replacement.mkv"}
        ]
        existing = [library(name, kind, path, LOCAL_LIBRARY_OPTIONS.copy()) for name, kind, path in LIBRARIES]
        with SeedServer(existing, replaced) as server:
            result = self.run_seed(server, environment={"GOLDENREC_SNAPSHOT": "0"})

        combined = result.stdout + result.stderr
        self.assertNotEqual(result.returncode, 0, "seed accepted a persistent inventory mismatch")
        self.assertIn("stock inventory differs from expected manifest", combined)
        self.assertIn("missing=", combined)


if __name__ == "__main__":
    unittest.main()
