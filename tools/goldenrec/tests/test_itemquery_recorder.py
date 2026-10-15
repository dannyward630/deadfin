import hashlib
import http.client
import json
import os
import subprocess
import shutil
import socket
import sqlite3
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlsplit


REPO = Path(__file__).resolve().parents[3]
RECORDER = REPO / "tools" / "goldenrec" / "record-itemquery.py"
GOLDEN_RELATIVE = Path("internal/store/storetest/testdata/golden")
RECORDER_IDENTITY = 'MediaBrowser Client="Goldenrec", Device="Goldenrec Seed", DeviceId="goldenrec-seed-v1", Version="1.0.0"'

EXPECTED_INVENTORY = tuple(
    json.loads((RECORDER.parent / "fixtures" / "expected-inventory.json").read_text(encoding="utf-8"))
)
PROVIDER_POLICY = json.loads((RECORDER.parent / "provider-policy.json").read_text(encoding="utf-8"))

# Isolated recorder tests use a real, minimal manifest/file projection so live
# fixture attestation validates both manifest bytes and fixture bytes.
TEST_LIVE_FIXTURE_FILES = {"fixture.bin": b"attested isolated fixture bytes\n"}
FIXTURE_OWNER_MARKER = ".goldenrec-fixtures-owner"
FIXTURE_OWNER_MARKER_FORMAT = "goldenrec-fixtures-owner-v1"
DOCKER_FORBIDDEN_CREDENTIAL_ENV = (
    "JELLYFIN_ADMIN",
    "JELLYFIN_PASSWORD",
    "GOLDENREC_ADMIN",
    "GOLDENREC_PASSWORD",
    "GOLDENREC_DISABLED_PASSWORD",
    "ADMIN",
    "PASSWORD",
    "DISABLED_PASSWORD",
)


def fixture_manifest_bytes(files):
    return "".join(
        f"{hashlib.sha256(contents).hexdigest()}  {relative}\n"
        for relative, contents in sorted(files.items())
    ).encode("utf-8")

def fixture_owner_marker_bytes(output, *, root=None, uid=None, marker_format=FIXTURE_OWNER_MARKER_FORMAT):
    return (
        json.dumps(
            {
                "format": marker_format,
                "root": str(output.resolve()) if root is None else root,
                "uid": os.getuid() if uid is None else uid,
            },
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
        + b"\n"
    )


PINNED_STOCK_IMAGE_DIGEST = "sha256:2d1a69be5aa2695d0c2df90ebeb76bbcc6efc0a798cb09bfa65906680e1d785a"
PINNED_STOCK_IMAGE_REFERENCE = f"jellyfin/jellyfin@{PINNED_STOCK_IMAGE_DIGEST}"
PINNED_STOCK_IMAGE_CONFIG_ID = "sha256:" + "c" * 64
GOLDEN_LIBRARIES = (
    ("Golden Movies", "movies", "/media/movies"),
    ("Golden TV", "tvshows", "/media/tv"),
    ("Golden Music", "music", "/media/music"),
    ("Golden Photos", "homevideos", "/media/photos"),
)
SEASON_ONE = "Season:/media/tv/Synthetic Show (2024)/Season 01"
EPISODE_ONE = "Episode:/media/tv/Synthetic Show (2024)/Season 01/Synthetic Show S01E01 - First Signal.mkv"
EPISODE_TWO = "Episode:/media/tv/Synthetic Show (2024)/Season 01/Synthetic Show S01E02 - Second Signal.mkv"
RUNTIME_IDS = {
    SEASON_ONE: "stock-season-guid",
    EPISODE_ONE: "stock-episode-one-guid",
    EPISODE_TWO: "stock-episode-two-guid",
}
SOURCE_DATE_CREATED = "2024-01-01T00:00:00.0000000Z"


def stock_items():
    return [
        {
            "Id": RUNTIME_IDS.get(identity, f"stock-{number}"),
            "Type": identity.split(":", 1)[0],
            "Path": identity.split(":", 1)[1],
            "DateCreated": SOURCE_DATE_CREATED,
        }
        for number, identity in enumerate(EXPECTED_INVENTORY)
    ]


def stock_libraries():
    return [
        {
            "Name": name,
            "ItemId": f"library-{name.rsplit(' ', 1)[-1].lower()}",
            "CollectionType": collection_type,
            "Locations": [location],
            "RefreshStatus": "Idle",
            "LibraryOptions": json.loads(json.dumps(PROVIDER_POLICY)),
        }
        for name, collection_type, location in GOLDEN_LIBRARIES
    ]


def stock_query_responses():
    by_identity = {f"{item['Type']}:{item['Path']}": item for item in stock_items()}
    return {
        "children": {
            # Intentional reverse lexical order: recorder must retain stock response order.
            "Items": [by_identity[EPISODE_TWO], by_identity[EPISODE_ONE]],
            "StartIndex": 7,
            "TotalRecordCount": 23,
        },
        "adjacent": {
            "Items": [by_identity[EPISODE_ONE]],
            "StartIndex": 3,
            "TotalRecordCount": 23,
        },
    }



class ItemQueryServer:
    """A local stock-shaped HTTP seam for the recorder; no Docker or network access."""

    def __init__(
        self,
        item_map=None,
        responses=None,
        *,
        libraries=None,
        library_snapshots=None,
        boxset_responses=None,
        public_info=None,
        recorder_identity=None,
        host="127.0.0.1",
    ):
        self.item_map = stock_items() if item_map is None else item_map
        self.responses = stock_query_responses() if responses is None else responses
        self.libraries = stock_libraries() if libraries is None else libraries
        self.library_snapshots = [self.libraries] if library_snapshots is None else library_snapshots
        self.boxset_responses = boxset_responses or {}
        self.virtual_folder_requests = 0
        self.recorder_identity = recorder_identity
        self.calls = []
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                parsed = urlsplit(self.path)
                query = parse_qs(parsed.query, keep_blank_values=True)
                outer.calls.append(("GET", parsed.path, query, self.headers.get("Authorization")))
                if parsed.path == "/System/Info/Public":
                    self.send_json(200, outer.public_info)
                    return
                if parsed.path == "/Library/VirtualFolders":
                    snapshot = min(outer.virtual_folder_requests, len(outer.library_snapshots) - 1)
                    outer.virtual_folder_requests += 1
                    self.send_json(200, outer.library_snapshots[snapshot])
                    return
                if parsed.path != "/Items":
                    self.send_json(404, {"error": parsed.path})
                    return
                if outer.recorder_identity is not None:
                    authorization = self.headers.get("Authorization", "")
                    token_prefix = outer.recorder_identity + ', Token="'
                    if not (authorization.startswith(token_prefix) and authorization.endswith('"') and len(authorization) > len(token_prefix)):
                        self.send_json(400, {"error": "missing recorder MediaBrowser identity or token"})
                        return
                if "ParentId" in query or "parentId" in query:
                    self.send_json(200, outer.responses["children"])
                elif "AdjacentTo" in query or "adjacentTo" in query:
                    self.send_json(200, outer.responses["adjacent"])
                elif "CollapseBoxSetItems" in query and query["CollapseBoxSetItems"] == ["true"] and "collapsed" in outer.boxset_responses:
                    self.send_json(200, outer.boxset_responses["collapsed"])
                elif "CollapseBoxSetItems" in query and query["CollapseBoxSetItems"] == ["false"] and "uncollapsed" in outer.boxset_responses:
                    self.send_json(200, outer.boxset_responses["uncollapsed"])
                else:
                    self.send_json(200, {"Items": outer.item_map, "StartIndex": 0, "TotalRecordCount": len(outer.item_map)})

            def do_POST(self):
                parsed = urlsplit(self.path)
                size = int(self.headers.get("Content-Length", "0"))
                body = self.rfile.read(size)
                outer.calls.append(("POST", parsed.path, parse_qs(parsed.query), body.decode("utf-8"), self.headers.get("Authorization")))
                if parsed.path != "/Users/AuthenticateByName":
                    self.send_json(404, {"error": parsed.path})
                    return
                if outer.recorder_identity is not None and self.headers.get("Authorization") != outer.recorder_identity:
                    self.send_json(400, {"error": "missing recorder MediaBrowser identity"})
                    return
                self.send_json(200, {"AccessToken": "stock-token-that-must-not-be-written", "User": {"Id": "stock-user-guid"}})

            def send_json(self, status, payload):
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(payload).encode("utf-8"))

            def log_message(self, format, *args):
                pass

        self.public_info = {"Version": "12.0.0"} if public_info is None else public_info
        if ":" in host:
            class IPv6ThreadingHTTPServer(ThreadingHTTPServer):
                address_family = socket.AF_INET6

            server_type = IPv6ThreadingHTTPServer
        else:
            server_type = ThreadingHTTPServer
        self.httpd = server_type((host, 0), Handler)
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
        host, port = self.httpd.server_address[:2]
        return f"http://{'[' + host + ']' if ':' in host else host}:{port}"

    def item_calls(self):
        return [call for call in self.calls if call[0] == "GET" and call[1] == "/Items"]



class SentinelProxy:
    """A forwarding loopback proxy that records requests which escaped urllib's bypass."""

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


def runtime_docker_state(
    base_url,
    *,
    running=True,
    image_reference=PINNED_STOCK_IMAGE_REFERENCE,
    image_config_id=PINNED_STOCK_IMAGE_CONFIG_ID,
    pinned_image_config_id=PINNED_STOCK_IMAGE_CONFIG_ID,
    repo_digests=(PINNED_STOCK_IMAGE_REFERENCE,),
    host_ip=None,
    host_port=None,
    present=True,
    mounts=None,
    command_log=None,
    environment_log=None,
    copy_failure=False,
    corrupt_copy=False,
):
    base = urlsplit(base_url)
    state = {
        "ps": "goldenrec-stock\n" if present else "",
        "inspect": [
            {
                "Id": "goldenrec-stock",
                "State": {"Running": running},
                "Config": {"Image": image_reference},
                # Docker container inspect reports an image config ID here, not
                # the OCI manifest digest from the digest-pinned image reference.
                "Image": image_config_id,
                "NetworkSettings": {
                    "Ports": {
                        "8096/tcp": [
                            {
                                "HostIp": base.hostname if host_ip is None else host_ip,
                                "HostPort": str(base.port if host_port is None else host_port),
                            }
                        ]
                    }
                },
            }
        ] if present else [],
        "pinned_image_config_id": pinned_image_config_id,
        "repo_digests": list(repo_digests),
        "command_log": None if command_log is None else str(command_log),
        "environment_log": None if environment_log is None else str(environment_log),
        "copy_failure": copy_failure,
        "corrupt_copy": corrupt_copy,
    }
    if mounts is not None and present:
        state["inspect"][0]["Mounts"] = mounts
    return state


def install_fake_docker(directory, state):
    """Install the runtime-attestation, lifecycle, and scalar-capture Docker seam."""
    binary = Path(directory) / "bin"
    binary.mkdir()
    snapshot = Path(directory) / "jellyfin.db"
    connection = sqlite3.connect(snapshot)
    try:
        connection.execute(
            "CREATE TABLE BaseItems (Id TEXT NOT NULL, Path TEXT NOT NULL, DateLastSaved TEXT, TotalBitrate INTEGER)"
        )
        connection.executemany(
            "INSERT INTO BaseItems (Id, Path, DateLastSaved, TotalBitrate) VALUES (?, ?, ?, ?)",
            [(item["Id"], item["Path"], None, None) for item in stock_items()],
        )
        connection.commit()
    finally:
        connection.close()
    docker = binary / "docker"
    docker.write_text(
        f"#!{sys.executable}\n"
        "import json, os, shutil, sys\n"
        f"state = json.loads({json.dumps(json.dumps(state))})\n"
        f"snapshot = {json.dumps(str(snapshot))}\n"
        "arguments = sys.argv[1:]\n"
        "if state.get('command_log'):\n"
        "    with open(state['command_log'], 'a', encoding='utf-8') as log:\n"
        "        log.write(json.dumps(arguments) + '\\n')\n"
        "if state.get('environment_log'):\n"
        "    present = [name for name in " + json.dumps(("JELLYFIN_ADMIN", "JELLYFIN_PASSWORD", "GOLDENREC_ADMIN", "GOLDENREC_PASSWORD", "GOLDENREC_DISABLED_PASSWORD", "ADMIN", "PASSWORD", "DISABLED_PASSWORD")) + " if name in os.environ]\n"
        "    with open(state['environment_log'], 'a', encoding='utf-8') as log:\n"
        "        log.write(json.dumps({'forbidden': present, 'control': os.environ.get('GOLDENREC_RECORDER_CONTROL')}) + '\\n')\n"
        "if arguments and arguments[0] == 'ps':\n"
        "    sys.stdout.write(state['ps'])\n"
        "elif arguments == ['image', 'inspect', " + json.dumps(PINNED_STOCK_IMAGE_REFERENCE) + ", '--format', '{{.Id}}']:\n"
        "    sys.stdout.write(state['pinned_image_config_id'] + '\\n')\n"
        "elif arguments == ['image', 'inspect', state['pinned_image_config_id'], '--format', '{{json .RepoDigests}}']:\n"
        "    print(json.dumps(state['repo_digests']))\n"
        "elif 'inspect' in arguments:\n"
        "    print(json.dumps(state['inspect']))\n"
        "elif arguments[:1] in (['stop'], ['start']) and arguments[1:] == ['goldenrec-stock']:\n"
        "    pass\n"
        "elif arguments[:1] == ['cp'] and len(arguments) == 3:\n"
        "    if state.get('copy_failure'):\n"
        "        raise SystemExit(1)\n"
        "    source, destination = arguments[1:]\n"
        "    if source.endswith(':/config/data/data'):\n"
        "        database = os.path.join(destination, 'data', 'jellyfin.db')\n"
        "        os.makedirs(os.path.dirname(database), exist_ok=True)\n"
        "        if state.get('corrupt_copy'):\n"
        "            with open(database, 'wb') as output:\n"
        "                output.write(b'not a SQLite database')\n"
        "        else:\n"
        "            shutil.copyfile(snapshot, database)\n"
        "            with open(database + '-wal', 'wb') as output:\n"
        "                output.write(b'wal sidecar')\n"
        "    elif state.get('corrupt_copy'):\n"
        "        with open(destination, 'wb') as output:\n"
        "            output.write(b'not a SQLite database')\n"
        "    else:\n"
        "        shutil.copyfile(snapshot, destination)\n"
        "else:\n"
        "    raise SystemExit(64)\n",
        encoding="utf-8",
    )
    docker.chmod(0o755)
    return binary

class ItemQueryRecorderTests(unittest.TestCase):
    maxDiff = None

    def setUp(self):
        self._isolated_source = tempfile.TemporaryDirectory()
        self.recorder = self.isolated_recorder(Path(self._isolated_source.name))

    def tearDown(self):
        self._isolated_source.cleanup()

    @staticmethod
    def mapped_items():
        return stock_items()

    @staticmethod
    def query_responses():
        return stock_query_responses()

    @staticmethod
    def write_cases(path):
        path.write_text(
            json.dumps(
                {
                    "format": "goldenrec-itemquery-requests/v1",
                    "cases": [
                        {
                            "name": "children-in-declared-order",
                            "operation_id": "GetItems",
                            "family": "translator-direct",
                            "params": {
                                "parentId": {"$item": SEASON_ONE},
                                "includeItemTypes": ["Episode"],
                                "fields": ["Path"],
                            },
                        },
                        {
                            "name": "adjacent-after-children",
                            "operation_id": "GetItems",
                            "family": "translator-direct",
                            "params": {
                                "adjacentTo": {"$item": EPISODE_TWO},
                                "fields": ["Path"],
                            },
                        },
                    ],
                },
                indent=2,
            )
            + "\n",
            encoding="utf-8",
        )


    def run_recorder(self, command, *, output_root, cases=None, base_url=None, recorder=None, environment=None, docker_state=None):
        recorder = self.recorder if recorder is None else recorder
        self.assertTrue(
            recorder.is_file(),
            "expected tools/goldenrec/record-itemquery.py with record and verify subcommands",
        )
        arguments = [sys.executable, str(recorder), command]
        if base_url is not None:
            arguments.extend(["--base-url", base_url])
        if cases is not None:
            arguments.extend(["--cases", str(cases)])
        arguments.extend(["--output-root", str(output_root)])
        env = os.environ | {
            "JELLYFIN_ADMIN": "GoldenrecAdmin",
            "JELLYFIN_PASSWORD": "s3cr3t",
            "HTTP_PROXY": "",
            "http_proxy": "",
            "NO_PROXY": "*",
            "no_proxy": "*",
        } | (environment or {})
        if command != "record":
            return subprocess.run(arguments, cwd=REPO, capture_output=True, text=True, env=env)
        self.assertIsNotNone(base_url, "record invocations must expose a stock base URL for runtime attestation")
        state = runtime_docker_state(base_url) if docker_state is None else docker_state
        for inspected in state.get("inspect", []):
            if "Mounts" not in inspected:
                inspected["Mounts"] = [
                    {
                        "Type": "bind",
                        "Source": str((recorder.parent / "fixtures" / "out").resolve()),
                        "Destination": "/media",
                        "RW": False,
                    }
                ]
        with tempfile.TemporaryDirectory() as fake_runtime:
            fake_bin = install_fake_docker(fake_runtime, state)
            return subprocess.run(
                arguments,
                cwd=REPO,
                capture_output=True,
                text=True,
                env=env | {"PATH": f"{fake_bin}{os.pathsep}{env['PATH']}"},
            )

    def isolated_recorder(self, directory, *, missing=()):
        root = Path(directory) / "tools" / "goldenrec"
        root.mkdir(parents=True)
        recorder = root / RECORDER.name
        shutil.copy2(RECORDER, recorder)
        if "seed" not in missing:
            shutil.copy2(RECORDER.parent / "seed.sh", root / "seed.sh")
        if "provider-policy" not in missing:
            shutil.copy2(RECORDER.parent / "provider-policy.json", root / "provider-policy.json")
        fixtures = root / "fixtures"
        fixtures.mkdir()
        if "expected-inventory" not in missing:
            shutil.copy2(RECORDER.parent / "fixtures" / "expected-inventory.json", fixtures / "expected-inventory.json")
        if "fixture-manifest" not in missing:
            # Normal tests provide a real, matching committed/generated manifest
            # and the one live file it attests. The clean-checkout regression
            # below removes generated output and exercises the repository copy.
            manifest = fixture_manifest_bytes(TEST_LIVE_FIXTURE_FILES)
            (fixtures / "source-manifest.sha256").write_bytes(manifest)
            output_fixtures = fixtures / "out"
            output_fixtures.mkdir(mode=0o700)
            for relative, contents in TEST_LIVE_FIXTURE_FILES.items():
                path = output_fixtures / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(contents)
            (output_fixtures / "MANIFEST.sha256").write_bytes(manifest)
            marker = output_fixtures / FIXTURE_OWNER_MARKER
            marker.write_bytes(fixture_owner_marker_bytes(output_fixtures))
            marker.chmod(0o600)
        return recorder
    def record_fixture(self, output_root):
        cases = output_root.parent / "cases.json"
        self.write_cases(cases)
        with ItemQueryServer() as server:
            result = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)
            calls = list(server.calls)
        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        return calls

    def golden(self, output_root):
        return output_root / GOLDEN_RELATIVE

    def test_record_resolves_symbolic_refs_preserves_manifest_and_stock_order_and_omits_sensitive_values(self):
        with tempfile.TemporaryDirectory() as directory:
            output_root = Path(directory) / "deadfin"
            calls = self.record_fixture(output_root)
            corpus = json.loads((self.golden(output_root) / "itemquery.json").read_text(encoding="utf-8"))
            serialized = json.dumps(corpus, sort_keys=True)

        self.assertEqual(corpus["format"], "goldenrec-itemquery-expectations/v1")
        self.assertEqual([case["name"] for case in corpus["cases"]], ["children-in-declared-order", "adjacent-after-children"])
        self.assertEqual(
            corpus["cases"][0]["expect"],
            {
                "status": 200,
                "start_index": 7,
                "total_record_count": 23,
                "items": [
                    EPISODE_TWO,
                    EPISODE_ONE,
                ],
            },
        )
        self.assertEqual(corpus["cases"][1]["expect"]["start_index"], 3)
        self.assertEqual(corpus["cases"][1]["expect"]["total_record_count"], 23)
        children_call = next(call for call in calls if call[0] == "GET" and call[1] == "/Items" and ("ParentId" in call[2] or "parentId" in call[2]))
        parent_values = children_call[2].get("ParentId", children_call[2].get("parentId"))
        self.assertEqual(parent_values, ["stock-season-guid"], "symbolic $item must resolve only at request time")
        item_calls = [call for call in calls if call[0] == "GET" and call[1] == "/Items"]
        self.assertTrue(all(call[3] and "Token=\"stock-token-that-must-not-be-written\"" in call[3] for call in item_calls))
        for forbidden in (
            "stock-token-that-must-not-be-written",
            "stock-user-guid",
            "s3cr3t",
            "stock-season-guid",
            "stock-episode-one-guid",
            "stock-episode-two-guid",
            "http://127.0.0.1",
        ):
            self.assertNotIn(forbidden, serialized)

    def test_record_rejects_duplicate_semantic_requests_without_replacing_valid_named_witnesses(self):
        """Names identify distinct witnesses; they cannot mask the same GET /Items request."""
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            output_root = directory / "deadfin"
            cases = directory / "cases.json"
            self.write_cases(cases)

            with ItemQueryServer() as server:
                baseline = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)
            self.assertEqual(baseline.returncode, 0, baseline.stderr or baseline.stdout)
            corpus = json.loads((self.golden(output_root) / "itemquery.json").read_text(encoding="utf-8"))
            self.assertEqual(
                [case["name"] for case in corpus["cases"]],
                ["children-in-declared-order", "adjacent-after-children"],
                "differently named requests with different parameters remain valid witnesses",
            )
            before = {
                path.name: hashlib.sha256(path.read_bytes()).hexdigest()
                for path in self.golden(output_root).iterdir()
                if path.is_file()
            }

            manifest = json.loads(cases.read_text(encoding="utf-8"))
            manifest["cases"].append({**manifest["cases"][0], "name": "duplicate-children-witness"})
            cases.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
            with ItemQueryServer() as server:
                rejected = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)
                calls = list(server.calls)
            after = {
                path.name: hashlib.sha256(path.read_bytes()).hexdigest()
                for path in self.golden(output_root).iterdir()
                if path.is_file()
            }

        self.assertNotEqual(rejected.returncode, 0, "record accepted differently named cases with the same GET /Items parameters")
        self.assertRegex(
            rejected.stderr,
            r"(?i)duplicate semantic request: children-in-declared-order, duplicate-children-witness",
        )
        self.assertEqual(calls, [], "semantic request validation must finish before sending credentials or querying stock")
        self.assertEqual(after, before, "a duplicate semantic request replaced a previously valid published corpus")

    def test_record_rejects_missing_and_duplicate_canonical_identities(self):
        variants = {
            "missing-path": [
                {"Id": "stock-season-guid", "Type": "Season", "Path": "/media/tv/Synthetic Show/Season 01"},
                {"Id": "stock-episode-guid", "Type": "Episode"},

            ],
            "duplicate-canonical": [
                {"Id": "stock-season-guid", "Type": "Season", "Path": "/media/tv/Synthetic Show/Season 01"},
                {"Id": "stock-episode-a", "Type": "Episode", "Path": "/media/tv/Synthetic Show/Season 01/One.mkv"},
                {"Id": "stock-episode-b", "Type": "Episode", "Path": "/media/tv/Synthetic Show/Season 01/One.mkv"},
            ],
        }
        for name, item_map in variants.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                output_root = Path(directory) / "deadfin"
                cases = Path(directory) / "cases.json"
                self.write_cases(cases)
                with ItemQueryServer(item_map, self.query_responses()) as server:
                    result = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)
                self.assertNotEqual(result.returncode, 0, f"record accepted {name} item map")
                self.assertRegex(result.stdout + result.stderr, r"(?i)(path|canonical|duplicate|identity)")
                self.assertFalse(self.golden(output_root).exists(), "rejected map must not leave a partial corpus")

    def test_record_requires_exact_seeded_inventory_and_local_library_policy_before_publish(self):
        """Recording is allowed only from the clean seed contract, not a merely queryable server."""
        baseline_items = stock_items()
        remotely_enabled_libraries = stock_libraries()
        remotely_enabled_options = remotely_enabled_libraries[0]["LibraryOptions"]
        remotely_enabled_options["EnableInternetProviders"] = True
        next(option for option in remotely_enabled_options["TypeOptions"] if option["Type"] == "Movie")["MetadataFetchers"] = ["Remote Provider"]
        variants = {
            "missing canonical identity": (baseline_items[1:], stock_libraries()),
            "extra canonical identity": (
                baseline_items
                + [{"Id": "unexpected-stock-item", "Type": "Movie", "Path": "/media/movies/Unexpected Stock Item.mkv"}],
                stock_libraries(),
            ),
            "type changed at a canonical path": (
                [item | {"Type": "Movie"} if item["Id"] == baseline_items[0]["Id"] else item for item in baseline_items],
                stock_libraries(),
            ),
            "remotely enabled provider policy": (baseline_items, remotely_enabled_libraries),
        }
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            output_root = directory / "deadfin"
            self.record_fixture(output_root)
            golden = self.golden(output_root)
            before = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in golden.iterdir() if path.is_file()}
            cases = directory / "cases.json"

            for mutation, (item_map, libraries) in variants.items():
                with self.subTest(mutation=mutation):
                    with ItemQueryServer(item_map, self.query_responses(), libraries=libraries) as server:
                        rejected = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)
                    self.assertNotEqual(rejected.returncode, 0, f"record published a corpus from {mutation}")
                    after = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in golden.iterdir() if path.is_file()}
                    self.assertEqual(after, before, f"rejected {mutation} replaced the previously published corpus")

    def test_record_authenticates_and_queries_items_with_required_mediabrowser_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            output_root = Path(directory) / "deadfin"
            cases = Path(directory) / "cases.json"
            self.write_cases(cases)
            with ItemQueryServer(self.mapped_items(), self.query_responses(), recorder_identity=RECORDER_IDENTITY) as server:
                result = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)

        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
    def test_record_keeps_credentials_in_memory_and_scrubs_every_docker_child_environment(self):
        """Docker children retain non-secret controls but never inherit recorder credentials or aliases."""
        credentials = {
            "JELLYFIN_ADMIN": "recorder-admin-sentinel",
            "JELLYFIN_PASSWORD": "recorder-password-sentinel",
            "GOLDENREC_ADMIN": "goldenrec-admin-alias-sentinel",
            "GOLDENREC_PASSWORD": "goldenrec-password-alias-sentinel",
            "GOLDENREC_DISABLED_PASSWORD": "goldenrec-disabled-password-sentinel",
            "ADMIN": "admin-alias-sentinel",
            "PASSWORD": "password-alias-sentinel",
            "DISABLED_PASSWORD": "disabled-password-alias-sentinel",
            "GOLDENREC_RECORDER_CONTROL": "non-secret-control-sentinel",
        }
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            cases = directory / "cases.json"
            output_root = directory / "deadfin"
            environment_log = directory / "docker-environments.jsonl"
            self.write_cases(cases)
            with ItemQueryServer() as stock:
                result = self.run_recorder(
                    "record",
                    output_root=output_root,
                    cases=cases,
                    base_url=stock.base_url,
                    environment=credentials,
                    docker_state=runtime_docker_state(stock.base_url, environment_log=environment_log),
                )
                stock_calls = list(stock.calls)

            child_environments = [json.loads(line) for line in environment_log.read_text(encoding="utf-8").splitlines()]

        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        self.assertGreaterEqual(len(child_environments), 7, "recording must exercise every Docker attestation and snapshot child")
        self.assertEqual(
            child_environments,
            [{"forbidden": [], "control": "non-secret-control-sentinel"}] * len(child_environments),
            "each Docker child must receive a scrubbed credential environment without dropping unrelated controls",
        )
        authentication = [call for call in stock_calls if call[0:2] == ("POST", "/Users/AuthenticateByName")]
        self.assertEqual(len(authentication), 1)
        self.assertEqual(
            json.loads(authentication[0][3]),
            {"Username": credentials["JELLYFIN_ADMIN"], "Pw": credentials["JELLYFIN_PASSWORD"]},
            "the recorder must retain the exact credentials in memory for stock authentication",
        )

    def test_verify_is_offline_deterministic_and_validates_hashes_and_atomic_output(self):
        with tempfile.TemporaryDirectory() as directory:
            output_root = Path(directory) / "deadfin"
            self.record_fixture(output_root)
            golden = self.golden(output_root)
            before = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in golden.iterdir() if path.is_file()}
            cases = Path(directory) / "cases.json"

            # There is no server after record_fixture returns: verify must not contact stock.
            verify = self.run_recorder("verify", output_root=output_root, cases=cases)
            self.assertEqual(verify.returncode, 0, verify.stderr or verify.stdout)

            # A failed subsequent recording must leave the previously valid corpus byte-for-byte intact.
            with ItemQueryServer(self.mapped_items(), {"children": {"Items": [{"Id": "unmapped-stock-guid", "Type": "Episode", "Path": "/media/tv/missing.mkv"}], "StartIndex": 0, "TotalRecordCount": 1}, "adjacent": self.query_responses()["adjacent"]}) as server:
                failed_record = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)
            self.assertNotEqual(failed_record.returncode, 0, "record accepted an item absent from the canonical map")
            after_failed_record = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in golden.iterdir() if path.is_file()}
            self.assertEqual(after_failed_record, before, "record failure replaced committed files instead of publishing atomically")

            corpus_path = golden / "itemquery.json"
            corpus_path.write_text(corpus_path.read_text(encoding="utf-8") + "\n", encoding="utf-8")
            tampered = self.run_recorder("verify", output_root=output_root, cases=cases)
            self.assertNotEqual(tampered.returncode, 0, "offline verify accepted a hash-mismatched corpus")
            self.assertRegex(tampered.stdout + tampered.stderr, r"(?i)(hash|sha|integrity)")


    def test_record_rejects_resolvable_hostnames_before_credentials_and_keeps_literal_loopback_supported(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            for host in ("127.0.0.1", "::1"):
                with self.subTest(literal_loopback=host), ItemQueryServer(self.mapped_items(), self.query_responses(), host=host) as server:
                    output_root = directory / host.replace(":", "ipv6")
                    cases = directory / f"{host.replace(':', 'ipv6')}.json"
                    self.write_cases(cases)
                    result = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)
                    self.assertEqual(result.returncode, 0, result.stderr or result.stdout)

            cases = directory / "symbolic.json"
            self.write_cases(cases)
            with ItemQueryServer(self.mapped_items(), self.query_responses()) as server:
                symbolic_url = server.base_url.replace("127.0.0.1", "localhost")
                rejected = self.run_recorder("record", output_root=directory / "symbolic", cases=cases, base_url=symbolic_url)
                calls = list(server.calls)

        self.assertNotEqual(rejected.returncode, 0, "record accepted a hostname that could resolve differently after validation")
        self.assertRegex(rejected.stderr, r"(?i)(literal|IP address|hostname)")
        self.assertEqual(calls, [], "hostname validation must reject before it can send administrator credentials")

    def test_record_rejects_non_pinned_stock_version_before_authentication(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            cases = directory / "cases.json"
            self.write_cases(cases)
            with ItemQueryServer(self.mapped_items(), self.query_responses(), public_info={"Version": "12.0.7"}) as server:
                rejected = self.run_recorder("record", output_root=directory / "deadfin", cases=cases, base_url=server.base_url)
                calls = list(server.calls)

        self.assertNotEqual(rejected.returncode, 0, "record accepted Jellyfin 12.0.7 instead of the pinned 12.0.0 stock build")
        self.assertRegex(rejected.stderr, r"(?i)(12\.0\.0|pinned|stock)")
        self.assertEqual([call[:2] for call in calls], [("GET", "/System/Info/Public")], "version validation must happen before authentication")

    def test_record_emits_exact_pinned_stock_oci_and_upstream_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            output_root = Path(directory) / "deadfin"
            self.record_fixture(output_root)
            provenance = json.loads((self.golden(output_root) / "provenance.json").read_text(encoding="utf-8"))

        expected = {
            "stock_version": "12.0.0",
            "stock_image_digest": "sha256:2d1a69be5aa2695d0c2df90ebeb76bbcc6efc0a798cb09bfa65906680e1d785a",
            "upstream_commit": "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb",
            "seed_sha256": hashlib.sha256((self.recorder.parent / "seed.sh").read_bytes()).hexdigest(),
            "expected_inventory_sha256": hashlib.sha256(
                (self.recorder.parent / "fixtures" / "expected-inventory.json").read_bytes()
            ).hexdigest(),
        }
        self.assertEqual({key: provenance.get(key) for key in expected}, expected, "recorded provenance must bind the corpus to the pinned stock OCI image and upstream source without a registry lookup")
    def test_offline_verify_requires_the_exact_pinned_upstream_commit_even_when_rehashed(self):
        def rewrite_sums(golden):
            (golden / "SHA256SUMS").write_text(
                "".join(
                    f"{hashlib.sha256((golden / name).read_bytes()).hexdigest()}  {name}\n"
                    for name in ("itemquery-fixtures.json", "itemquery.json", "provenance.json")
                ),
                encoding="utf-8",
            )

        mutations = {
            "wrong-shaped commit": "not-a-40-hex-commit",
            "different 40-hex commit": "0" * 40,
        }
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            output_root = directory / "deadfin"
            self.record_fixture(output_root)
            golden = self.golden(output_root)
            cases = directory / "cases.json"

            for name, upstream_commit in mutations.items():
                with self.subTest(name=name):
                    provenance_path = golden / "provenance.json"
                    provenance = json.loads(provenance_path.read_text(encoding="utf-8"))
                    provenance["upstream_commit"] = upstream_commit
                    provenance_path.write_text(json.dumps(provenance, indent=2) + "\n", encoding="utf-8")
                    rewrite_sums(golden)

                    verified = self.run_recorder("verify", output_root=output_root, cases=cases)

                    self.assertNotEqual(verified.returncode, 0, f"offline verify accepted a {name}")
                    self.assertRegex(verified.stderr, r"(?i)(upstream.*commit|commit.*provenance|pinned)")

    def test_record_rejects_missing_required_provenance_inputs(self):
        for missing in ("seed", "provider-policy", "expected-inventory", "fixture-manifest"):
            with self.subTest(missing=missing), tempfile.TemporaryDirectory() as directory:
                directory = Path(directory)
                recorder = self.isolated_recorder(directory, missing=(missing,))
                cases = directory / "cases.json"
                self.write_cases(cases)
                with ItemQueryServer(self.mapped_items(), self.query_responses()) as server:
                    rejected = self.run_recorder("record", output_root=directory / "deadfin", cases=cases, base_url=server.base_url, recorder=recorder)

                self.assertNotEqual(rejected.returncode, 0, f"record treated missing {missing} as optional provenance")
                self.assertRegex(rejected.stderr, r"(?i)(provenance|required|provider|fixture|manifest|seed|inventory)")
                self.assertFalse((directory / "deadfin" / GOLDEN_RELATIVE).exists(), "a record without all provenance inputs must not publish a corpus")

    def test_offline_verify_recomputes_each_source_bound_provenance_hash(self):
        mutations = {
            "recorder": lambda recorder, cases: recorder.write_text(recorder.read_text(encoding="utf-8") + "\n# stale recorder source\n", encoding="utf-8"),
            "request manifest": lambda recorder, cases: cases.write_text(cases.read_text(encoding="utf-8") + "\n", encoding="utf-8"),
            "seed": lambda recorder, cases: (recorder.parent / "seed.sh").write_text((recorder.parent / "seed.sh").read_text(encoding="utf-8") + "\n# stale seed source\n", encoding="utf-8"),
            "provider policy": lambda recorder, cases: (recorder.parent / "provider-policy.json").write_text((recorder.parent / "provider-policy.json").read_text(encoding="utf-8") + "\n", encoding="utf-8"),
            "expected inventory": lambda recorder, cases: (recorder.parent / "fixtures" / "expected-inventory.json").write_text((recorder.parent / "fixtures" / "expected-inventory.json").read_text(encoding="utf-8") + "\n", encoding="utf-8"),
            "fixture manifest": lambda recorder, cases: (recorder.parent / "fixtures" / "source-manifest.sha256").write_text(
                (recorder.parent / "fixtures" / "source-manifest.sha256").read_text(encoding="utf-8") + "# stale fixture manifest source\n",
                encoding="utf-8",
            ),
        }
        for input_name, mutate in mutations.items():
            with self.subTest(input=input_name), tempfile.TemporaryDirectory() as directory:
                directory = Path(directory)
                recorder = self.isolated_recorder(directory)
                cases = directory / "cases.json"
                self.write_cases(cases)
                output_root = directory / "deadfin"
                with ItemQueryServer(self.mapped_items(), self.query_responses()) as server:
                    recorded = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url, recorder=recorder)
                self.assertEqual(recorded.returncode, 0, recorded.stderr or recorded.stdout)

                mutate(recorder, cases)
                verified = self.run_recorder("verify", output_root=output_root, cases=cases, recorder=recorder)

                self.assertNotEqual(verified.returncode, 0, f"offline verify accepted stale {input_name} provenance")
                self.assertRegex(verified.stderr, rf"(?i){input_name.replace(' ', '.*')}.*(hash|sha|mismatch)")


    def test_record_requires_two_idle_topology_snapshots_before_items_cases_and_publication(self):
        """A library scan must be idle and stable across two exact topology observations."""
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            output_root = directory / "deadfin"
            cases = directory / "cases.json"
            self.write_cases(cases)
            idle = stock_libraries()
            with ItemQueryServer(library_snapshots=[idle, idle]) as server:
                baseline = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)
                topology_reads = server.virtual_folder_requests
            self.assertEqual(baseline.returncode, 0, baseline.stderr or baseline.stdout)

            golden = self.golden(output_root)
            before = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in golden.iterdir()}
            violations = []
            if topology_reads != 2:
                violations.append(f"stable topology was read {topology_reads} times instead of twice")
            for snapshot_name, active_snapshot_index in (("active first", 0), ("unstable second", 1)):
                for library_index, library in enumerate(idle):
                    active = json.loads(json.dumps(idle))
                    active[library_index]["RefreshStatus"] = "Active"
                    snapshots = [idle, idle]
                    snapshots[active_snapshot_index] = active
                    changed_cases = self.query_responses()
                    changed_cases["children"]["Items"].reverse()
                    with ItemQueryServer(responses=changed_cases, library_snapshots=snapshots) as server:
                        rejected = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)
                        case_calls = [call for call in server.item_calls() if "Recursive" not in call[2]]
                    after = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in golden.iterdir()}
                    if rejected.returncode == 0 or case_calls or after != before:
                        violations.append(f"{snapshot_name} snapshot for {library['Name']} was accepted or published")
            self.assertEqual(violations, [], "; ".join(violations))

    def test_record_requires_boxset_queries_to_physically_replace_exact_collection_members(self):
        """Collapsed Movie results replace exactly the collection-member Movies with the physical BoxSet."""
        members = (
            "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film One (2020).mkv",
            "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Three (2022).mkv",
            "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Two (2021).mkv",
        )
        boxset = "BoxSet:/media/movies/Synthetic Collection [boxset]"
        other_movies = tuple(identity for identity in EXPECTED_INVENTORY if identity.startswith("Movie:") and identity not in members)
        by_identity = {f"{item['Type']}:{item['Path']}": item for item in stock_items()}

        def response(identities):
            return {"Items": [by_identity[identity] for identity in identities], "StartIndex": 0, "TotalRecordCount": len(identities)}

        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            output_root = directory / "deadfin"
            cases = RECORDER.parent / "itemquery-cases.json"
            expected_responses = {
                "collapsed": response((*other_movies, boxset)),
                "uncollapsed": response((*other_movies, *members)),
            }
            with ItemQueryServer(boxset_responses=expected_responses) as server:
                baseline = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)
            self.assertEqual(baseline.returncode, 0, baseline.stderr or baseline.stdout)

            corpus = json.loads((self.golden(output_root) / "itemquery.json").read_text(encoding="utf-8"))
            observed = {case["name"]: set(case["expect"]["items"]) for case in corpus["cases"]}
            collapsed = observed["movie-collapse-boxset"]
            uncollapsed = observed["movie-no-collapse-boxset"]
            self.assertEqual(uncollapsed - collapsed, set(members))
            self.assertEqual(collapsed - uncollapsed, {boxset})
            self.assertEqual(collapsed & uncollapsed, set(other_movies))

            golden = self.golden(output_root)
            before = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in golden.iterdir()}
            missing_membership = response(other_movies)
            with ItemQueryServer(boxset_responses={"collapsed": missing_membership, "uncollapsed": missing_membership}) as server:
                rejected = self.run_recorder("record", output_root=output_root, cases=cases, base_url=server.base_url)
            after = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in golden.iterdir()}

        self.assertNotEqual(rejected.returncode, 0, "record accepted identical collapsed and uncollapsed BoxSet results")
        self.assertRegex(rejected.stderr, r"(?i)(boxset|collection|collapse|membership)")
        self.assertEqual(after, before, "missing BoxSet membership evidence replaced the prior corpus")

    def test_record_bypasses_environment_proxies_for_literal_loopback_stock(self):
        """A validated loopback peer must receive recorder auth directly, never through urllib's proxy settings."""
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            cases = directory / "cases.json"
            self.write_cases(cases)
            with ItemQueryServer(recorder_identity=RECORDER_IDENTITY) as stock, SentinelProxy() as proxy:
                result = self.run_recorder(
                    "record",
                    output_root=directory / "deadfin",
                    cases=cases,
                    base_url=stock.base_url,
                    environment={
                        "HTTP_PROXY": proxy.url,
                        "http_proxy": proxy.url,
                        "NO_PROXY": "",
                        "no_proxy": "",
                    },
                )
                proxy_calls = list(proxy.calls)
                stock_calls = list(stock.calls)

        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        self.assertEqual(proxy_calls, [], "urllib must explicitly bypass every environment proxy for the validated stock origin")
        authentication = [call for call in stock_calls if call[0:2] == ("POST", "/Users/AuthenticateByName")]
        self.assertEqual(len(authentication), 1, "the separately bound stock listener must receive the authentication request")
        self.assertEqual(json.loads(authentication[0][3]), {"Username": "GoldenrecAdmin", "Pw": "s3cr3t"})

    def test_record_resolves_pinned_manifest_to_config_id_and_rejects_unattested_runtime_before_authentication(self):
        """Docker's container .Image is a config ID, so it must be resolved back to the pinned manifest."""
        def state_for(name, base_url):
            if name == "valid config ID resolved from pinned manifest":
                return runtime_docker_state(base_url)
            if name == "container config ID differs from pinned reference config ID":
                return runtime_docker_state(base_url, image_config_id="sha256:" + "d" * 64)
            if name == "resolved config has no pinned manifest repo digest":
                return runtime_docker_state(base_url, repo_digests=())
            if name == "resolved config has a different manifest repo digest":
                return runtime_docker_state(base_url, repo_digests=("jellyfin/jellyfin@sha256:" + "0" * 64,))
            if name == "mutable configured image reference":
                return runtime_docker_state(base_url, image_reference="jellyfin/jellyfin:12.0.0")
            if name == "wrong configured image digest":
                return runtime_docker_state(base_url, image_reference="jellyfin/jellyfin@sha256:" + "1" * 64)
            if name == "different published port":
                return runtime_docker_state(base_url, host_port=urlsplit(base_url).port + 1)
            if name == "non-loopback publication":
                return runtime_docker_state(base_url, host_ip="0.0.0.0")
            if name == "stopped container":
                return runtime_docker_state(base_url, running=False)
            if name == "missing container":
                return runtime_docker_state(base_url, present=False)
            raise AssertionError(name)

        variants = (
            "valid config ID resolved from pinned manifest",
            "container config ID differs from pinned reference config ID",
            "resolved config has no pinned manifest repo digest",
            "resolved config has a different manifest repo digest",
            "mutable configured image reference",
            "wrong configured image digest",
            "different published port",
            "non-loopback publication",
            "stopped container",
            "missing container",
        )
        for name in variants:
            with self.subTest(runtime=name), tempfile.TemporaryDirectory() as directory:
                directory = Path(directory)
                cases = directory / "cases.json"
                command_log = directory / "docker-commands.jsonl"
                self.write_cases(cases)
                output_root = directory / "deadfin"
                with ItemQueryServer() as stock:
                    state = state_for(name, stock.base_url)
                    state["command_log"] = str(command_log)
                    result = self.run_recorder(
                        "record",
                        output_root=output_root,
                        cases=cases,
                        base_url=stock.base_url,
                        docker_state=state,
                    )
                    stock_calls = list(stock.calls)

                if name == "valid config ID resolved from pinned manifest":
                    self.assertNotEqual(PINNED_STOCK_IMAGE_CONFIG_ID, PINNED_STOCK_IMAGE_DIGEST)
                    self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
                    commands = [json.loads(line) for line in command_log.read_text(encoding="utf-8").splitlines()]
                    self.assertEqual(
                        commands[:4],
                        [
                            ["image", "inspect", PINNED_STOCK_IMAGE_REFERENCE, "--format", "{{.Id}}"],
                            ["image", "inspect", PINNED_STOCK_IMAGE_CONFIG_ID, "--format", "{{json .RepoDigests}}"],
                            ["ps", "--all", "--format", "{{.ID}}"],
                            ["inspect", "--", "goldenrec-stock"],
                        ],
                        "runtime attestation must resolve the pinned manifest to its config ID before matching container .Image",
                    )
                    continue
                self.assertNotEqual(result.returncode, 0, f"record accepted {name}")
                self.assertEqual(
                    [call for call in stock_calls if call[0:2] == ("POST", "/Users/AuthenticateByName")],
                    [],
                    f"{name} was rejected only after credentials reached stock",
                )
                self.assertEqual(
                    [call for call in stock_calls if call[0:2] == ("GET", "/Items")],
                    [],
                    f"{name} was rejected only after recording case data",
                )
                self.assertFalse(self.golden(output_root).exists(), f"{name} published a corpus before runtime attestation")

    def test_record_accepts_only_the_attested_read_only_fixture_media_bind(self):
        """The bound HTTP peer is usable only when its inspected /media mount is the verified fixture tree."""
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            cases = directory / "cases.json"
            self.write_cases(cases)
            fixture_root = (self.recorder.parent / "fixtures" / "out").resolve()
            wrong_root = directory / "wrong-media"
            wrong_root.mkdir()
            fixture_alias = directory / "fixture-alias"
            fixture_alias.symlink_to(fixture_root, target_is_directory=True)

            valid_mount = {
                "Type": "bind",
                "Source": str(fixture_root),
                "Destination": "/media",
                "RW": False,
            }
            cases_by_mount = (
                ("exact verified read-only bind", [valid_mount], True),
                ("missing media mount", [], False),
                ("writable media bind", [valid_mount | {"RW": True}], False),
                ("wrong media bind source", [valid_mount | {"Source": str(wrong_root)}], False),
                ("duplicate media binds", [valid_mount, valid_mount.copy()], False),
                (
                    "named volume media mount",
                    [{"Type": "volume", "Name": "goldenrec-media", "Destination": "/media", "RW": False}],
                    False,
                ),
                ("symlink media bind source", [valid_mount | {"Source": str(fixture_alias)}], False),
            )

            for name, mounts, accepted in cases_by_mount:
                with self.subTest(mount=name), ItemQueryServer() as stock:
                    output_root = directory / name.replace(" ", "-")
                    recorded = self.run_recorder(
                        "record",
                        output_root=output_root,
                        cases=cases,
                        base_url=stock.base_url,
                        docker_state=runtime_docker_state(stock.base_url, mounts=mounts),
                    )
                    stock_calls = list(stock.calls)

                if accepted:
                    self.assertEqual(recorded.returncode, 0, recorded.stderr or recorded.stdout)
                    continue
                self.assertNotEqual(recorded.returncode, 0, f"record accepted {name}")
                self.assertRegex(recorded.stderr, r"(?i)(media|mount|fixture|bind|source)")
                self.assertEqual(
                    [call for call in stock_calls if call[0:2] == ("POST", "/Users/AuthenticateByName")],
                    [],
                    f"{name} was rejected only after credentials reached stock",
                )
                self.assertFalse(self.golden(output_root).exists(), f"{name} published a corpus before mount attestation")

    def test_record_runtime_attestation_binds_published_loopback_address_to_base_url(self):
        """The inspected pinned container must publish the exact literal loopback origin being recorded."""
        cases_by_binding = (
            ("exact IPv4 loopback address", "127.0.0.1", "127.0.0.1", True),
            ("exact IPv6 loopback address", "::1", "::1", True),
            ("IPv4 base URL with IPv6 publication on the same port", "127.0.0.1", "::1", False),
            ("IPv6 base URL with IPv4 publication on the same port", "::1", "127.0.0.1", False),
        )
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            for name, base_host, published_host, accepted in cases_by_binding:
                with self.subTest(binding=name):
                    cases = directory / f"{base_host.replace(':', 'ipv6')}-{published_host.replace(':', 'ipv6')}.json"
                    output_root = directory / f"output-{base_host.replace(':', 'ipv6')}-{published_host.replace(':', 'ipv6')}"
                    self.write_cases(cases)
                    with ItemQueryServer(host=base_host) as stock:
                        state = runtime_docker_state(
                            stock.base_url,
                            host_ip=published_host,
                            host_port=urlsplit(stock.base_url).port,
                        )
                        result = self.run_recorder(
                            "record",
                            output_root=output_root,
                            cases=cases,
                            base_url=stock.base_url,
                            docker_state=state,
                        )
                        stock_calls = list(stock.calls)

                    if accepted:
                        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
                        continue
                    self.assertNotEqual(result.returncode, 0, f"record accepted {name}")
                    self.assertRegex(result.stderr, r"(?i)(host|address|port|bind|publish)")
                    self.assertEqual(
                        [call for call in stock_calls if call[0:2] == ("POST", "/Users/AuthenticateByName")],
                        [],
                        f"{name} was rejected only after credentials reached stock",
                    )
                    self.assertEqual(
                        [call for call in stock_calls if call[0:2] == ("GET", "/Items")],
                        [],
                        f"{name} was rejected only after recording case data",
                    )
                    self.assertFalse(self.golden(output_root).exists(), f"{name} published a corpus before runtime attestation")

    def test_record_quiesces_attested_container_before_copying_complete_stock_data_directory(self):
        """Scalar capture stops the attested runtime, snapshots its data directory, then restarts it."""
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            cases = directory / "cases.json"
            output_root = directory / "deadfin"
            command_log = directory / "docker-commands.jsonl"
            self.write_cases(cases)

            with ItemQueryServer() as stock:
                recorded = self.run_recorder(
                    "record",
                    output_root=output_root,
                    cases=cases,
                    base_url=stock.base_url,
                    docker_state=runtime_docker_state(stock.base_url, command_log=command_log),
                )

            commands = [json.loads(line) for line in command_log.read_text(encoding="utf-8").splitlines()]

        self.assertEqual(recorded.returncode, 0, recorded.stderr or recorded.stdout)
        stop = ["stop", "goldenrec-stock"]
        start = ["start", "goldenrec-stock"]
        copy_index, copy = next((index, command) for index, command in enumerate(commands) if command[:1] == ["cp"])
        self.assertEqual(copy[1], "goldenrec-stock:/config/data/data", "scalar capture must copy the complete SQLite data directory, never the live bare database file")
        self.assertNotIn("jellyfin.db", copy[1], "the copied source must retain WAL-related state alongside jellyfin.db")
        self.assertLess(commands.index(stop), copy_index, "Docker must stop the runtime before copying its SQLite data directory")
        self.assertLess(copy_index, commands.index(start), "Docker must restart the runtime only after successful snapshot capture")

    def test_record_restarts_runtime_and_keeps_prior_corpus_after_snapshot_copy_or_merge_failure(self):
        """A failed stopped-runtime snapshot always restarts stock and cannot publish partial output."""
        failure_modes = (
            ("copy failure", {"copy_failure": True}),
            ("merge failure", {"corrupt_copy": True}),
        )
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            cases = directory / "cases.json"
            output_root = directory / "deadfin"
            self.write_cases(cases)
            self.record_fixture(output_root)
            golden = self.golden(output_root)
            before = {
                path.relative_to(golden).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
                for path in golden.rglob("*")
                if path.is_file()
            }

            for failure_name, failure_state in failure_modes:
                with self.subTest(snapshot_capture=failure_name):
                    command_log = directory / f"docker-{failure_name.replace(' ', '-')}.jsonl"
                    with ItemQueryServer() as stock:
                        failed = self.run_recorder(
                            "record",
                            output_root=output_root,
                            cases=cases,
                            base_url=stock.base_url,
                            docker_state=runtime_docker_state(stock.base_url, command_log=command_log, **failure_state),
                        )
                    commands = [json.loads(line) for line in command_log.read_text(encoding="utf-8").splitlines()]
                    after = {
                        path.relative_to(golden).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
                        for path in golden.rglob("*")
                        if path.is_file()
                    }

                    self.assertNotEqual(failed.returncode, 0, f"record succeeded despite forced snapshot {failure_name}")
                    self.assertEqual(after, before, f"a {failure_name} published over the prior corpus")
                    stop = ["stop", "goldenrec-stock"]
                    start = ["start", "goldenrec-stock"]
                    copy_index, copy = next((index, command) for index, command in enumerate(commands) if command[:1] == ["cp"])
                    self.assertEqual(copy[1], "goldenrec-stock:/config/data/data")
                    self.assertLess(commands.index(stop), copy_index, f"Docker did not stop before the forced {failure_name}")
                    self.assertLess(copy_index, commands.index(start), f"Docker did not restart after the forced {failure_name}")

    def test_recorded_provenance_labels_upstream_commit_as_api_parity_source(self):
        """The behavioral source commit and independently attested runtime OCI image have distinct authority labels."""
        with tempfile.TemporaryDirectory() as directory:
            output_root = Path(directory) / "deadfin"
            self.record_fixture(output_root)
            provenance = json.loads((self.golden(output_root) / "provenance.json").read_text(encoding="utf-8"))

        self.assertEqual(provenance.get("upstream_commit_authority"), "api-parity-source")
        self.assertEqual(provenance.get("stock_image_digest"), PINNED_STOCK_IMAGE_DIGEST)

    def test_record_rejects_missing_or_nonexact_generated_fixture_output_before_authentication(self):
        """Runtime recording accepts only the exact committed fixture-byte projection."""
        def mutate_live_fixture(name, output):
            fixture = output / "fixture.bin"
            if name == "missing fixtures/out directory":
                shutil.rmtree(output)
            elif name == "modified listed file":
                fixture.write_bytes(b"modified without a matching manifest update\n")
            elif name == "missing listed file":
                fixture.unlink()
            elif name == "fixtures/out has non-private mode":
                output.chmod(0o755)
            elif name == "unexpected extra file":
                (output / "unexpected.bin").write_bytes(b"not listed by the attestation\n")
            elif name == "owner marker has wrong root":
                (output / FIXTURE_OWNER_MARKER).write_bytes(fixture_owner_marker_bytes(output, root=str(output.parent.resolve())))
            elif name == "owner marker has wrong UID":
                (output / FIXTURE_OWNER_MARKER).write_bytes(fixture_owner_marker_bytes(output, uid=os.getuid() + 1))
            elif name == "owner marker has invalid schema":
                (output / FIXTURE_OWNER_MARKER).write_bytes(
                    json.dumps(
                        {"format": FIXTURE_OWNER_MARKER_FORMAT, "root": str(output.resolve()), "uid": os.getuid(), "extra": True},
                        sort_keys=True,
                        separators=(",", ":"),
                    ).encode("utf-8")
                    + b"\n"
                )
            elif name == "owner marker has non-private mode":
                (output / FIXTURE_OWNER_MARKER).chmod(0o644)
            elif name == "owner marker is not at fixture root":
                (output / "nested").mkdir()
                (output / "nested" / FIXTURE_OWNER_MARKER).write_bytes(fixture_owner_marker_bytes(output / "nested"))
                (output / "nested" / FIXTURE_OWNER_MARKER).chmod(0o600)
            else:
                raise AssertionError(name)

        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            clean_recorder = self.isolated_recorder(directory / "clean")
            clean_cases = directory / "clean-cases.json"
            self.write_cases(clean_cases)
            with ItemQueryServer() as stock:
                clean = self.run_recorder(
                    "record",
                    output_root=directory / "clean-deadfin",
                    cases=clean_cases,
                    base_url=stock.base_url,
                    recorder=clean_recorder,
                )
            self.assertEqual(clean.returncode, 0, clean.stderr or clean.stdout)

            for name in (
                "missing fixtures/out directory",
                "modified listed file",
                "fixtures/out has non-private mode",
                "missing listed file",
                "unexpected extra file",
                "owner marker has wrong root",
                "owner marker has wrong UID",
                "owner marker has invalid schema",
                "owner marker has non-private mode",
                "owner marker is not at fixture root",
            ):
                with self.subTest(live_fixture=name):
                    recorder = self.isolated_recorder(directory / name.replace(" ", "-"))
                    live_output = recorder.parent / "fixtures" / "out"
                    mutate_live_fixture(name, live_output)
                    output_root = directory / f"{name.replace(' ', '-')}-deadfin"
                    cases = directory / f"{name.replace(' ', '-')}-cases.json"
                    self.write_cases(cases)
                    with ItemQueryServer() as stock:
                        rejected = self.run_recorder(
                            "record",
                            output_root=output_root,
                            cases=cases,
                            base_url=stock.base_url,
                            recorder=recorder,
                        )
                        stock_calls = list(stock.calls)
                    self.assertNotEqual(rejected.returncode, 0, f"record accepted {name} despite a matching manifest file")
                    self.assertRegex(rejected.stderr, r"(?i)fixture.*(output|file|manifest|attestation|owner|marker)")
                    self.assertEqual(
                        [call for call in stock_calls if call[0:2] == ("POST", "/Users/AuthenticateByName")],
                        [],
                        f"{name} was rejected only after administrator credentials reached stock",
                    )
                    self.assertFalse(self.golden(output_root).exists(), f"{name} published a corpus before fixture-byte attestation")

    def test_offline_verify_requires_exact_projection_of_bound_request_manifest_and_boxset_proof(self):
        """Rehashed corpus bytes still cannot rename, reshape, reorder, or weaken the source-bound public request contract."""
        members = (
            "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film One (2020).mkv",
            "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Three (2022).mkv",
            "Movie:/media/movies/Synthetic Collection [boxset]/Collection Film Two (2021).mkv",
        )
        boxset = "BoxSet:/media/movies/Synthetic Collection [boxset]"
        other_movies = tuple(identity for identity in EXPECTED_INVENTORY if identity.startswith("Movie:") and identity not in members)
        by_identity = {f"{item['Type']}:{item['Path']}": item for item in stock_items()}

        def response(identities):
            return {"Items": [by_identity[identity] for identity in identities], "StartIndex": 0, "TotalRecordCount": len(identities)}

        def rewrite_sums(golden):
            (golden / "SHA256SUMS").write_text(
                "".join(
                    f"{hashlib.sha256((golden / name).read_bytes()).hexdigest()}  {name}\n"
                    for name in ("itemquery-fixtures.json", "itemquery.json", "provenance.json")
                ),
                encoding="utf-8",
            )

        def mutate(corpus, name):
            cases = corpus["cases"]
            if name == "case name":
                cases[0]["name"] = "renamed-public-query"
            elif name == "operation and family":
                cases[0]["operation_id"] = "GetItemsAlternative"
                cases[0]["family"] = "controller-only"
            elif name == "method and path":
                cases[0]["request"]["method"] = "POST"
                cases[0]["request"]["path"] = "/Users"
            elif name == "request params":
                cases[0]["request"]["params"]["limit"] = 1
            elif name == "removed case":
                cases.pop()
            elif name == "added case":
                additional = json.loads(json.dumps(cases[0]))
                additional["name"] = "additional-projection-case"
                cases.append(additional)
            elif name == "reordered cases":
                cases.reverse()
            elif name == "weakened boxset replacement":
                collapsed = next(case for case in cases if case["name"] == "movie-collapse-boxset")
                collapsed["expect"]["items"].remove(boxset)
            else:
                raise AssertionError(name)

        mutations = (
            "case name",
            "operation and family",
            "method and path",
            "request params",
            "removed case",
            "added case",
            "reordered cases",
            "weakened boxset replacement",
        )
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            output_root = directory / "deadfin"
            cases_path = RECORDER.parent / "itemquery-cases.json"
            with ItemQueryServer(
                boxset_responses={
                    "collapsed": response((*other_movies, boxset)),
                    "uncollapsed": response((*other_movies, *members)),
                }
            ) as stock:
                recorded = self.run_recorder("record", output_root=output_root, cases=cases_path, base_url=stock.base_url)
            self.assertEqual(recorded.returncode, 0, recorded.stderr or recorded.stdout)

            golden = self.golden(output_root)
            original = json.loads((golden / "itemquery.json").read_text(encoding="utf-8"))
            for name in mutations:
                with self.subTest(corpus_mutation=name):
                    corpus = json.loads(json.dumps(original))
                    mutate(corpus, name)
                    (golden / "itemquery.json").write_text(json.dumps(corpus, indent=2) + "\n", encoding="utf-8")
                    rewrite_sums(golden)
                    rejected = self.run_recorder("verify", output_root=output_root, cases=cases_path)
                    self.assertNotEqual(rejected.returncode, 0, f"offline verify accepted a rehashed {name} mutation")

            (golden / "itemquery.json").write_text(json.dumps(original, indent=2) + "\n", encoding="utf-8")
            rewrite_sums(golden)
            recovered = self.run_recorder("verify", output_root=output_root, cases=cases_path)
            self.assertEqual(recovered.returncode, 0, recovered.stderr or recovered.stdout)

    def test_offline_verify_recovers_a_lone_valid_previous_publish(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            output_root = directory / "deadfin"
            cases = directory / "cases.json"
            self.record_fixture(output_root)
            golden = self.golden(output_root)
            previous = golden.parent / f"{golden.name}.previous"
            golden.replace(previous)

            recovered = self.run_recorder("verify", output_root=output_root, cases=cases)

            self.assertEqual(recovered.returncode, 0, recovered.stderr or recovered.stdout)
            self.assertTrue(golden.is_dir(), "verify must restore the lone valid previous corpus after interrupted publication")
            self.assertFalse(previous.exists(), "recovery must promote rather than discard the only valid previous corpus")
            self.assertEqual(self.run_recorder("verify", output_root=output_root, cases=cases).returncode, 0, "the recovered corpus must remain offline-verifiable")


class ItemCorpusEmitterTests(unittest.TestCase):
    """The generated /Items scenario is an exact projection of checked-in provenance."""

    maxDiff = None

    _TRANSPORT_NORMALIZERS = (
        "response-time-headers",
        "response-time-ms-header",
        "process-time-headers",
        "date-header",
        "server-stack-header",
        "content-length-header",
        "content-language-header",
        "vary-header",
    )

    @staticmethod
    def replace_item_references(value, identifiers):
        if isinstance(value, dict):
            if set(value) == {"$item"}:
                return identifiers[value["$item"]]
            return {key: ItemCorpusEmitterTests.replace_item_references(item, identifiers) for key, item in value.items()}
        if isinstance(value, list):
            return [ItemCorpusEmitterTests.replace_item_references(item, identifiers) for item in value]
        return value

    @staticmethod
    def item_references(value):
        if isinstance(value, dict):
            if set(value) == {"$item"}:
                yield value["$item"]
                return
            for item in value.values():
                yield from ItemCorpusEmitterTests.item_references(item)
        elif isinstance(value, list):
            for item in value:
                yield from ItemCorpusEmitterTests.item_references(item)

    @staticmethod
    def contains_item_reference(value):
        if isinstance(value, dict):
            return "$item" in value or any(ItemCorpusEmitterTests.contains_item_reference(item) for item in value.values())
        if isinstance(value, list):
            return any(ItemCorpusEmitterTests.contains_item_reference(item) for item in value)
        return False

    def run_emitter(self, *arguments, emitter=None):
        emitter = RECORDER.parent / "emit-item-corpus.py" if emitter is None else emitter
        return subprocess.run(
            [sys.executable, str(emitter), *arguments],
            cwd=REPO,
            capture_output=True,
            text=True,
        )
    def run_emitter_without_ignored_state(self, *arguments, emitter=None):
        state = RECORDER.parent / "state"
        with tempfile.TemporaryDirectory(dir=state.parent, prefix=".item-corpus-emitter-state-") as relocation:
            relocated_state = Path(relocation) / "state"
            moved_state = state.exists()
            if moved_state:
                state.rename(relocated_state)
            try:
                self.assertFalse(state.exists(), "the production goldenrec state must be absent during emission")
                return self.run_emitter(*arguments, emitter=emitter)
            finally:
                if moved_state:
                    relocated_state.rename(state)


    def test_emitter_projects_all_provenance_cases_and_checks_exact_output(self):
        request_manifest = json.loads((RECORDER.parent / "itemquery-cases.json").read_text(encoding="utf-8"))

        golden_expectations = json.loads(
            (REPO / "deadfin" / "internal" / "store" / "storetest" / "testdata" / "golden" / "itemquery.json").read_text(
                encoding="utf-8"
            )
        )

        self.assertEqual(request_manifest["format"], "goldenrec-itemquery-requests/v1")
        self.assertEqual(golden_expectations["format"], "goldenrec-itemquery-expectations/v1")
        self.assertEqual(
            len(request_manifest["cases"]),
            236,
            "the committed source-bound request manifest must retain all 236 semantic witnesses",
        )
        semantic_requests = {
            json.dumps(
                {"method": "GET", "path": "/Items", "params": case["params"]},
                sort_keys=True,
                separators=(",", ":"),
            )
            for case in request_manifest["cases"]
        }
        self.assertEqual(
            len(semantic_requests),
            len(request_manifest["cases"]),
            "the attested request manifest must not contain duplicate semantic requests",
        )
        self.assertEqual(
            len(golden_expectations["cases"]),
            len(request_manifest["cases"]),
            "the expectation manifest must cover every attested request witness",
        )
        self.assertEqual(
            [
                {
                    "name": case["name"],
                    "operation_id": case["operation_id"],
                    "family": case["family"],
                    "request": {"method": "GET", "path": "/Items", "params": case["params"]},
                }
                for case in request_manifest["cases"]
            ],
            [
                {key: case[key] for key in ("name", "operation_id", "family", "request")}
                for case in golden_expectations["cases"]
            ],
            "the request and expectation manifests must remain the same ordered public contract",
        )

        referenced_identities = sorted(
            {
                identity
                for case in request_manifest["cases"]
                for identity in self.item_references(case["params"])
            }
        )
        item_identifiers = {identity: f"synthetic-clean-clone-{index}" for index, identity in enumerate(referenced_identities)}
        self.assertTrue(item_identifiers, "the request manifest must retain symbolic item identities")
        normalizers = (
            "item-id-map",
            "server-id",
            "entity-tags",
            "item-created-dates",
            "user-data-keys",
            *self._TRANSPORT_NORMALIZERS,
        )

        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "clean-clone"
            goldenrec = directory / "tools" / "goldenrec"
            goldenrec.mkdir(parents=True)
            emitter = goldenrec / "emit-item-corpus.py"
            requests = goldenrec / "itemquery-cases.json"
            expectations = directory / "deadfin" / "internal" / "store" / "storetest" / "testdata" / "golden" / "itemquery.json"
            item_map = directory / "attested-inputs" / "synthetic-id-map.json"
            output = directory / "corpora" / "items.yaml"
            shutil.copy2(RECORDER.parent / "emit-item-corpus.py", emitter)
            shutil.copy2(RECORDER.parent / "itemquery-cases.json", requests)
            expectations.parent.mkdir(parents=True)
            shutil.copy2(REPO / "deadfin" / "internal" / "store" / "storetest" / "testdata" / "golden" / "itemquery.json", expectations)
            item_map.parent.mkdir(parents=True)
            item_map.write_text(
                json.dumps(
                    [
                        {"Type": identity.split(":", 1)[0], "Path": identity.split(":", 1)[1], "Id": item_id}
                        for identity, item_id in item_identifiers.items()
                    ]
                ),
                encoding="utf-8",
            )
            self.assertFalse((goldenrec / "state").exists(), "the simulated clean clone must not contain ignored recorder state")
            emitter_arguments = (
                "--requests",
                str(requests),
                "--item-map",
                str(item_map),
                "--expectations",
                str(expectations),
                "--output",
                str(output),
            )
            missing = self.run_emitter_without_ignored_state(*emitter_arguments, "--check", emitter=emitter)
            self.assertNotEqual(missing.returncode, 0, "--check accepted an output that was never emitted")

            emitted = self.run_emitter_without_ignored_state(*emitter_arguments, emitter=emitter)
            self.assertEqual(emitted.returncode, 0, emitted.stderr or emitted.stdout)
            self.assertFalse((goldenrec / "state").exists(), "emission read or recreated ignored recorder state")
            emitted_bytes = output.read_bytes()
            scenario = json.loads(emitted_bytes)

            self.assertEqual(scenario["format"], "goldenrec-scenario/v1")
            steps = scenario["steps"]
            self.assertEqual(len(steps), len(request_manifest["cases"]))
            self.assertEqual([step["name"] for step in steps], [case["name"] for case in request_manifest["cases"]])
            self.assertEqual(len({step["name"] for step in steps}), len(steps), "emitted step names must be unique")

            for source_case, expectation_case, step in zip(request_manifest["cases"], golden_expectations["cases"], steps, strict=True):
                with self.subTest(case=source_case["name"]):
                    self.assertEqual(step["name"], source_case["name"])
                    self.assertEqual(step["method"], "GET")
                    self.assertEqual(step["path"], "/Items")
                    self.assertEqual(step["headers"], {"Accept": "application/json"})
                    self.assertEqual(step["auth_role"], "admin")
                    self.assertEqual(step["operation_id"], "GetItems")
                    self.assertEqual(step["normalizers"], list(normalizers))
                    self.assertEqual(step["params"], self.replace_item_references(source_case["params"], item_identifiers))
                    self.assertFalse(self.contains_item_reference(step["params"]), "symbolic item reference survived emission")
                    self.assertEqual(
                        {
                            "name": source_case["name"],
                            "operation_id": source_case["operation_id"],
                            "family": source_case["family"],
                            "request": {"method": step["method"], "path": step["path"], "params": source_case["params"]},
                        },
                        {key: expectation_case[key] for key in ("name", "operation_id", "family", "request")},
                    )

            checked = self.run_emitter_without_ignored_state(*emitter_arguments, "--check", emitter=emitter)
            self.assertEqual(checked.returncode, 0, checked.stderr or checked.stdout)
            output.write_bytes(emitted_bytes + b" ")
            corrupted = self.run_emitter_without_ignored_state(*emitter_arguments, "--check", emitter=emitter)
            self.assertNotEqual(corrupted.returncode, 0, "--check accepted a corrupted corpus")
            self.assertFalse((goldenrec / "state").exists(), "all emission modes must be independent of ignored recorder state")

if __name__ == "__main__":
    unittest.main()
