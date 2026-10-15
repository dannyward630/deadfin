import json
import os
import sqlite3
import subprocess
import tempfile
import threading
import shutil
import sys
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit


REPO = Path(__file__).resolve().parents[3]
PREFLIGHT = REPO / "tools" / "goldenrec" / "final-replay-preflight.sh"
INVENTORY = json.loads((REPO / "tools" / "goldenrec" / "fixtures" / "expected-inventory.json").read_text(encoding="utf-8"))
ITEM_CASES = REPO / "tools" / "goldenrec" / "itemquery-cases.json"


class PreflightServer:
    """Stock- and Deadfin-shaped endpoint seam for preflight validation, without Docker."""

    def __init__(self, identities, *, require_media_browser_token=False, item_rows=None):
        self.calls = []
        self.authenticated_request_headers = []
        self.authentication_payloads = []
        self.require_media_browser_token = require_media_browser_token
        self.items = [dict(zip(("Type", "Path"), identity.split(":", 1))) for identity in identities] if item_rows is None else item_rows
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                outer.calls.append(("POST", urlsplit(self.path).path))
                outer.authentication_payloads.append(json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0")) or 0)))
                if urlsplit(self.path).path != "/Users/AuthenticateByName":
                    self.send_json(404, {"error": self.path})
                    return
                self.send_json(
                    200,
                    {
                        "AccessToken": "preflight-test-token",
                        "User": {
                            "Policy": {
                                "IsAdministrator": True,
                                "IsHidden": True,
                                "IsDisabled": False,
                                "EnableContentDeletion": True,
                                "EnableRemoteControlOfOtherUsers": True,
                            },
                            "Configuration": {},
                        },
                        "SessionInfo": {"Capabilities": {}},
                    },
                )

            def do_GET(self):
                path = urlsplit(self.path).path
                outer.calls.append(("GET", path))
                authorization = self.headers.get("Authorization")
                emby_token = self.headers.get("X-Emby-Token")
                outer.authenticated_request_headers.append((path, authorization, emby_token))
                expected_authorization = 'MediaBrowser Client="Goldenrec preflight", Device="Goldenrec preflight", DeviceId="goldenrec-preflight-v1", Version="1.0.0", Token="preflight-test-token"'
                if outer.require_media_browser_token and (authorization != expected_authorization or emby_token is not None):
                    self.send_json(401, {"error": "post-login requests require MediaBrowser Token authorization"})
                    return
                if path == "/DisplayPreferences/usersettings":
                    self.send_json(200, {})
                    return
                if path == "/Items":
                    self.send_json(200, {"Items": outer.items})
                    return
                self.send_json(404, {"error": self.path})

            def send_json(self, status, payload):
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(payload).encode("utf-8"))

            def log_message(self, *_):
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
    def port(self):
        return self.httpd.server_address[1]


class FinalReplayPreflightTests(unittest.TestCase):
    maxDiff = None

    def test_reference_env_removes_only_its_stock_volume_before_regenerating_and_seeding(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            log_path, completed = self.run_reference_env(directory)

            self.assertEqual(completed.returncode, 0, completed.stderr)
            events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
            down, generated, up, seeded = events
            self.assertEqual(down["args"][-3:], ["down", "--volumes", "--remove-orphans"])
            self.assertEqual(generated, {"event": "fixtures-generated"})
            self.assertEqual(up["args"][-2:], ["up", "-d"])
            self.assertEqual(seeded, {"event": "stock-seeded"})


    def test_preflight_forks_clean_state_and_bootstraps_both_hosts_with_canonical_media(self):
        admin = "preflight-argv-admin-sentinel"
        password = "preflight-argv-password-sentinel"
        disabled_password = "preflight-argv-disabled-sentinel"
        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            state_root, log_path, completed = self.run_preflight(
                directory,
                stock,
                deadfin,
                admin=admin,
                password=password,
                disabled_password=disabled_password,
            )

            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertFalse((state_root / "deadfin-data" / "stale-session").exists(), "preflight reused mutable Deadfin state")
            self.assertGreaterEqual(stock.calls.count(("POST", "/Users/AuthenticateByName")), 2)
            self.assertGreaterEqual(deadfin.calls.count(("POST", "/Users/AuthenticateByName")), 2)
            self.assertEqual(stock.authentication_payloads, [{"Username": admin, "Pw": password}] * len(stock.authentication_payloads))
            self.assertEqual(deadfin.authentication_payloads, [{"Username": admin, "Pw": password}] * len(deadfin.authentication_payloads))
            self.assertIn(("GET", "/DisplayPreferences/usersettings"), stock.calls)
            self.assertIn(("GET", "/DisplayPreferences/usersettings"), deadfin.calls)
            self.assertIn(("GET", "/Items"), stock.calls)
            self.assertIn(("GET", "/Items"), deadfin.calls)

            events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
            reference_index = next(index for index, event in enumerate(events) if event["event"] == "reference-env")
            bootstrap_index = next(index for index, event in enumerate(events) if event["event"] == "docker" and event["args"][:1] == ["run"] and "finalreplaybootstrap" in " ".join(event["args"]))
            self.assertLess(reference_index, bootstrap_index, "stock reset must complete before Deadfin state bootstrap")
            docker_runs = [event["args"] for event in events if event["event"] == "docker" and event["args"][:1] == ["run"]]
            self.assertEqual(len(docker_runs), 3, f"expected bootstrap, seed, and server containers; got {docker_runs!r}")
            state_mount_events = [event["state_mount"] for event in events if event["event"] == "docker" and event["args"][:1] == ["run"] and event.get("state_mount")]
            self.assertEqual(state_mount_events, [{"is_directory": True, "is_symlink": False, "mode": 0o700, "uid": os.geteuid()}] * 3)

            fixture_mount = f"type=bind,src={(directory / 'fixtures').resolve()},dst=/media,readonly"
            state_mount = f"type=bind,src={(state_root / 'deadfin-data').resolve()},dst=/state"
            bootstrap, seed, server = docker_runs
            self.assertIn(fixture_mount, bootstrap, "bootstrap did not receive the canonical read-only /media fixture tree")
            self.assertIn(state_mount, bootstrap)
            self.assertEqual(seed[-4:], ["-program-data", "/state", "-credentials-file", "/run/secrets/goldenrec.json"])
            self.assertIn(state_mount, seed)
            self.assertIn(fixture_mount, server, "Deadfin server did not receive the canonical read-only /media fixture tree")
            self.assertIn(state_mount, server)

            credential_events = [event for event in events if event.get("credential_file")]
            self.assertEqual(len(credential_events), 1, "only the seed helper may receive the credential file")
            credential_file = credential_events[0]["credential_file"]
            self.assertEqual(credential_file["mode"], 0o600)
            self.assertTrue(credential_file["readonly"])
            self.assertEqual(
                credential_file["payload"],
                {
                    "admin_name": admin,
                    "admin_password": password,
                    "disabled_name": "GoldenrecDisabled",
                    "disabled_password": disabled_password,
                },
            )
            self.assertFalse(Path(credential_file["source"]).exists(), "preflight did not promptly delete its ephemeral credential file")
            logged_argv = "\n".join("\0".join(event["args"]) for event in events if event["event"] in {"python3", "docker"})
            for secret in (admin, password, disabled_password):
                with self.subTest(secret=secret):
                    self.assertNotIn(secret, logged_argv)

    def test_preflight_stops_before_server_use_when_generated_fixture_attestation_rejects_tampering(self):
        """A regenerated fixture tree must be attested before corpus checks, Docker, or either server is used."""
        for failure in ("tampered fixture bytes", "manifest bytes mismatch"):
            with self.subTest(fixture_failure=failure), PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                _, log_path, completed = self.run_preflight(
                    directory,
                    stock,
                    deadfin,
                    fixture_validation_failure=failure,
                )

                self.assertNotEqual(completed.returncode, 0, f"preflight accepted {failure}")
                self.assertIn(failure, completed.stderr)
                self.assertEqual(stock.calls, [], f"preflight contacted stock after {failure}")
                self.assertEqual(deadfin.calls, [], f"preflight contacted Deadfin after {failure}")
                events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
                reference_index = next(index for index, event in enumerate(events) if event["event"] == "reference-env")
                validation_index = next(
                    index
                    for index, event in enumerate(events)
                    if event["event"] == "python3"
                    and Path(event["args"][0]).name == "record-itemquery.py"
                    and event["args"][1:2] == ["validate-fixtures"]
                )
                self.assertLess(reference_index, validation_index, "preflight validated fixtures before regenerating them")
                self.assertFalse(
                    any(
                        event["event"] == "python3"
                        and Path(event["args"][0]).name in {"record-itemquery.py", "emit-item-corpus.py"}
                        and event["args"][1:2] != ["validate-fixtures"]
                        for event in events
                    ),
                    f"preflight continued offline corpus work after {failure}",
                )
                self.assertFalse(
                    any(index > validation_index and event["event"] == "docker" for index, event in enumerate(events)),
                    f"preflight invoked Docker after rejecting {failure}",
                )

    def test_preflight_rejects_unowned_state_root_before_deleting_prior_state(self):
        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            root = directory / "unowned-root"
            victim = root / "deadfin-data" / "must-survive"
            victim.parent.mkdir(parents=True)
            victim.write_text("do not delete", encoding="utf-8")
            _, log_path, completed = self.run_preflight(directory, stock, deadfin, state_root_override=root, claim_state_root=False)

            self.assertNotEqual(completed.returncode, 0, "preflight accepted an unowned existing state root")
            self.assertEqual(victim.read_text(encoding="utf-8"), "do not delete")
            events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
            self.assertFalse(any(event["event"] == "docker" and event["args"][:2] == ["rm", "-f"] for event in events))
    def test_generated_root_ignores_hostile_tmpdir_and_explicit_symlink_root_is_rejected(self):
        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            hostile_tmp = directory / "private-tmp"
            hostile_tmp.mkdir()
            tmp_alias = directory / "tmp"
            tmp_alias.symlink_to(hostile_tmp, target_is_directory=True)
            hostile_tmp.chmod(0o000)
            generated_root = None
            try:
                fixture_root = directory / "fixtures"
                snapshots = directory / "stock-state" / "snapshots"
                self.assertFalse(fixture_root.exists(), "clean-root preflight fixture tree must begin absent")
                self.assertFalse(snapshots.exists(), "clean-root preflight stock snapshot directory must begin absent")
                _, log_path, generated = self.run_preflight(
                    directory,
                    stock,
                    deadfin,
                    use_generated_root=True,
                    initial_generated_inputs=False,
                    reference_env_generates_inputs=True,
                    extra_environment={"TMPDIR": str(tmp_alias)},
                )
                self.assertEqual(generated.returncode, 0, generated.stderr)
                generated_root = Path(generated.stdout.rsplit("state=", 1)[1].strip())
                default_parent = (REPO / "tools" / "goldenrec" / "state").resolve()
                self.assertEqual(generated_root.parent, default_parent)
                events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
                reference_index = next(index for index, event in enumerate(events) if event["event"] == "reference-env")
                reference_event = events[reference_index]
                self.assertTrue(reference_event["fixtures_created"], "fake reference environment did not create the fixture tree")
                self.assertTrue(reference_event["id_map_created"], "fake reference environment did not create the stock identifier map")
                verifier_index = next(index for index, event in enumerate(events) if event["event"] == "python3" and Path(event["args"][0]).name == "record-itemquery.py" and event["args"][1:2] == ["verify"])
                emitter_index = next(index for index, event in enumerate(events) if event["event"] == "python3" and Path(event["args"][0]).name == "emit-item-corpus.py" and event["args"][1:2] == ["--check"])
                self.assertLess(reference_index, verifier_index, "recorder verification ran before reference regeneration")
                self.assertLess(reference_index, emitter_index, "item corpus emission check ran before reference regeneration")
                state_mount = f"type=bind,src={generated_root / 'deadfin-data'},dst=/state"
                self.assertTrue(all(state_mount in event["args"] for event in events if event["event"] == "docker" and event["args"][:1] == ["run"]))
            finally:
                hostile_tmp.chmod(0o700)
                if generated_root is not None:
                    shutil.rmtree(generated_root, ignore_errors=True)

        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            fixture_root = directory / "fixtures"
            snapshots = directory / "stock-state" / "snapshots"
            self.assertFalse(fixture_root.exists(), "missing-output preflight fixture tree must begin absent")
            self.assertFalse(snapshots.exists(), "missing-output preflight stock snapshot directory must begin absent")
            _, log_path, missing = self.run_preflight(directory, stock, deadfin, initial_generated_inputs=False)

            self.assertNotEqual(missing.returncode, 0, "preflight accepted a reference environment that returned without fixtures")
            self.assertIn("fixture tree is not a directory", missing.stderr)
            events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
            self.assertTrue(any(event["event"] == "reference-env" for event in events), "preflight rejected fixture absence before invoking reference-env")
            self.assertFalse(
                any(
                    event["event"] == "python3"
                    and event["args"]
                    and Path(event["args"][0]).name in {"record-itemquery.py", "emit-item-corpus.py"}
                    for event in events
                ),
                "preflight ran offline corpus checks after reference-env returned without fixtures",
            )

        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            target = directory / "target"
            victim = target / "deadfin-data" / "victim"
            victim.parent.mkdir(parents=True)
            victim.write_text("preserve", encoding="utf-8")
            alias = directory / "explicit-link"
            alias.symlink_to(target, target_is_directory=True)
            _, _, explicit = self.run_preflight(directory, stock, deadfin, state_root_override=alias, prepare_state=False)
            self.assertNotEqual(explicit.returncode, 0)
            self.assertEqual(victim.read_text(encoding="utf-8"), "preserve")
    def test_preflight_replaces_only_the_prior_container_for_its_port_and_state(self):
        with (
            PreflightServer(INVENTORY) as stock,
            PreflightServer(INVENTORY) as deadfin_a,
            PreflightServer(INVENTORY) as deadfin_b,
            tempfile.TemporaryDirectory() as directory,
        ):
            directory = Path(directory)
            lifecycle = self.create_rerunnable_preflight_harness(directory)
            state_a = directory / "state-a"
            state_b = directory / "state-b"
            name_a = f"goldenrec-final-replay-deadfin-{deadfin_a.port}"
            name_b = f"goldenrec-final-replay-deadfin-{deadfin_b.port}"

            first_a = self.run_rerunnable_preflight(lifecycle, "a-first", state_a, stock, deadfin_a)
            self.assertEqual(first_a.returncode, 0, first_a.stderr)
            first_b = self.run_rerunnable_preflight(lifecycle, "b-first", state_b, stock, deadfin_b)
            self.assertEqual(first_b.returncode, 0, first_b.stderr)

            replacement_a = self.run_rerunnable_preflight(lifecycle, "a-replacement", state_a, stock, deadfin_a)
            self.assertEqual(replacement_a.returncode, 0, replacement_a.stderr)
            containers = self.read_rerunnable_containers(lifecycle)
            self.assertEqual(set(containers), {name_a, name_b})
            self.assertEqual(containers[name_a]["state"], str((state_a / "deadfin-data").resolve()))
            self.assertEqual(containers[name_b]["state"], str((state_b / "deadfin-data").resolve()))
            self.assertNotEqual(name_a, name_b, "different Deadfin ports must own isolated containers")

            events = self.read_rerunnable_events(lifecycle, "a-replacement")
            prior_removal = next(
                index
                for index, event in enumerate(events)
                if event["event"] == "docker" and event["args"] == ["rm", "-f", name_a]
            )
            state_deletion = next(
                index
                for index, event in enumerate(events)
                if event["event"] == "rm" and event["args"][-1] == str((state_a / "deadfin-data").resolve())
            )
            bootstrap = next(
                index
                for index, event in enumerate(events)
                if event["event"] == "docker"
                and event["args"][:1] == ["run"]
                and "finalreplaybootstrap" in " ".join(event["args"])
            )
            self.assertLess(prior_removal, state_deletion)
            self.assertLess(state_deletion, bootstrap)

            deadfin_a.items.append({"Type": "Movie", "Path": "/media/movies/stale-replay-leak.mkv"})
            failed_a = self.run_rerunnable_preflight(lifecycle, "a-failure", state_a, stock, deadfin_a)
            self.assertNotEqual(failed_a.returncode, 0, "stale inventory must fail after starting the current container")
            self.assertIn("item inventory response must contain exactly 52 rows", failed_a.stderr)
            self.assertEqual(self.read_rerunnable_containers(lifecycle), {name_b: containers[name_b]})
            failed_removals = [
                event["args"]
                for event in self.read_rerunnable_events(lifecycle, "a-failure")
                if event["event"] == "docker" and event["args"][:2] == ["rm", "-f"]
            ]
            self.assertEqual(failed_removals, [["rm", "-f", name_a], ["rm", "-f", name_a]])
    def test_preflight_rejects_a_host_with_stale_or_mismatched_inventory(self):
        stale_inventory = [*INVENTORY, "Movie:/media/movies/stale-replay-leak.mkv"]
        with PreflightServer(INVENTORY) as stock, PreflightServer(stale_inventory) as deadfin, tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            _, log_path, completed = self.run_preflight(directory, stock, deadfin)

            self.assertNotEqual(completed.returncode, 0, "preflight accepted a Deadfin host with a stale item outside the canonical manifest")
            self.assertIn("item inventory response must contain exactly 52 rows", completed.stderr)
            events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
            self.assertTrue(
                any(event["event"] == "docker" and event["args"][:2] == ["rm", "-f"] for event in events),
                "failed preflight left the started Deadfin container behind",
            )

    def test_preflight_rejects_duplicate_and_malformed_inventory_rows(self):
        canonical_rows = [dict(zip(("Type", "Path"), identity.split(":", 1))) for identity in INVENTORY]
        variants = {
            "duplicate identity after all 52 canonical rows": [*canonical_rows, dict(canonical_rows[0])],
            "malformed row after all 52 canonical rows": [*canonical_rows, None],
        }
        for name, item_rows in variants.items():
            with self.subTest(name=name), PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY, item_rows=item_rows) as deadfin, tempfile.TemporaryDirectory() as directory:
                _, log_path, completed = self.run_preflight(Path(directory), stock, deadfin)

                self.assertNotEqual(completed.returncode, 0, f"preflight accepted {name}")
                self.assertRegex(completed.stderr, r"(?i)(inventory|item).*?(52|unique|canonical|row|malformed)")
                events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
                self.assertTrue(
                    any(event["event"] == "docker" and event["args"][:2] == ["rm", "-f"] for event in events),
                    "failed preflight left the started Deadfin container behind",
                )
    def test_preflight_rejects_insufficient_duplicate_and_nondeterministic_item_manifests_before_replay(self):
        canonical = json.loads(ITEM_CASES.read_text(encoding="utf-8"))
        canonical_cases = canonical["cases"]
        canonical_semantics = {
            (case["operation_id"], json.dumps(case["params"], sort_keys=True, separators=(",", ":")))
            for case in canonical_cases
        }
        self.assertGreaterEqual(len(canonical_cases), 200, "the deterministic request manifest must contain at least 200 cases")
        self.assertEqual(
            len(canonical_semantics),
            len(canonical_cases),
            "the deterministic request manifest must contain unique semantic GET /Items requests",
        )
        self.assertNotIn(
            "semantic-duplicate-witness",
            {case["name"] for case in canonical_cases},
            "the semantic-duplicate fixture must retain unique case names",
        )

        insufficient_cases = json.loads(json.dumps(canonical))
        insufficient_cases["cases"] = insufficient_cases["cases"][:199]
        duplicate_names = json.loads(json.dumps(canonical))
        duplicate_names["cases"][1]["name"] = duplicate_names["cases"][0]["name"]
        duplicate_semantic_request = json.loads(json.dumps(canonical))
        duplicate_semantic_request["cases"][1] = {
            **duplicate_semantic_request["cases"][0],
            "name": "semantic-duplicate-witness",
        }
        nondeterministic_random = json.loads(json.dumps(canonical))
        nondeterministic_random["cases"][0]["params"]["SortBy"] = ["Random"]

        variants = {
            "fewer than 200 cases": (insufficient_cases, r"(?i)(at least|minimum).*200"),
            "duplicate names": (duplicate_names, r"(?i)(duplicate|unique).*name"),
            "duplicate semantic GET /Items request": (duplicate_semantic_request, r"(?i)duplicate.*semantic.*request"),
            "nondeterministic Random sorting": (nondeterministic_random, r"nondeterministic Random"),
        }
        for name, (manifest, error) in variants.items():
            with self.subTest(name=name), PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
                directory = Path(directory)
                path = directory / "invalid-itemquery-cases.json"
                path.write_text(json.dumps(manifest), encoding="utf-8")
                _, log_path, completed = self.run_preflight(directory, stock, deadfin, item_cases=path)

                self.assertNotEqual(completed.returncode, 0, f"preflight accepted an item manifest with {name}")
                self.assertRegex(completed.stderr, error)
                commands = [event["args"] for event in (json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()) if event["event"] == "python3"]
                self.assertFalse(any(Path(args[0]).name == "record-itemquery.py" for args in commands), "invalid manifest reached offline verification")
                self.assertEqual(stock.calls, [], "manifest rejection must occur before stock login")
                self.assertEqual(deadfin.calls, [], "manifest rejection must occur before Deadfin login")

        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            stale_cases = json.loads(json.dumps(canonical))
            stale_cases["cases"][0]["params"] = dict(stale_cases["cases"][0]["params"], StartIndex=1)
            stale_path = directory / "stale-itemquery-cases.json"
            stale_path.write_text(json.dumps(stale_cases), encoding="utf-8")
            self.assertGreaterEqual(len(stale_cases["cases"]), 200, "the stale fixture must retain the minimum request-matrix size")
            _, stale_log, stale = self.run_preflight(directory, stock, deadfin, item_cases=stale_path)

            self.assertNotEqual(stale.returncode, 0, "preflight accepted a stale item manifest")
            self.assertIn("fake offline verifier rejected stale request-manifest provenance", stale.stderr)
            recorder_commands = [
                event["args"]
                for event in (json.loads(line) for line in stale_log.read_text(encoding="utf-8").splitlines())
                if event["event"] == "python3" and Path(event["args"][0]).name == "record-itemquery.py"
            ]
            verifier = next(args for args in recorder_commands if args[1:2] == ["verify"])
            self.assertEqual(verifier[:2], [str(REPO / "tools" / "goldenrec" / "record-itemquery.py"), "verify"])
            self.assertLess(
                recorder_commands.index(next(args for args in recorder_commands if args[1:2] == ["validate-fixtures"])),
                recorder_commands.index(verifier),
                "fixture attestation must precede stale-request verification",
            )
            self.assertEqual(verifier[verifier.index("--cases") + 1], str(stale_path))
            self.assertEqual(verifier[verifier.index("--output-root") + 1], str(REPO / "deadfin"))
            self.assertEqual(stock.calls, [], "offline verify must fail before stock login")
            self.assertEqual(deadfin.calls, [], "offline verify must fail before Deadfin login")

    def test_preflight_rejects_stale_golden_provenance_through_offline_verify(self):
        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            _, log_path, completed = self.run_preflight(Path(directory), stock, deadfin, fake_offline_failure="provenance")

            self.assertNotEqual(completed.returncode, 0, "preflight accepted stale golden provenance")
            self.assertIn("fake offline verifier rejected stale golden provenance", completed.stderr)
            commands = [event["args"] for event in (json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()) if event["event"] == "python3"]
            self.assertTrue(any(Path(args[0]).name == "record-itemquery.py" and args[1:2] == ["verify"] for args in commands))
            self.assertFalse(any(Path(args[0]).name == "emit-item-corpus.py" for args in commands), "emitter ran after verifier rejected stale provenance")
            self.assertEqual(stock.calls, [])
            self.assertEqual(deadfin.calls, [])

    def test_preflight_rejects_stale_items_yaml_through_emitter_check(self):
        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            _, log_path, completed = self.run_preflight(Path(directory), stock, deadfin, fake_offline_failure="items")

            self.assertNotEqual(completed.returncode, 0, "preflight accepted stale corpora/items.yaml")
            self.assertIn("fake item emitter rejected stale corpora/items.yaml", completed.stderr)
            commands = [event["args"] for event in (json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()) if event["event"] == "python3"]
            self.assertTrue(any(Path(args[0]).name == "record-itemquery.py" and args[1:2] == ["verify"] for args in commands))
            emitter = next(args for args in commands if Path(args[0]).name == "emit-item-corpus.py")
            self.assertIn("--check", emitter)
            self.assertEqual(emitter[emitter.index("--requests") + 1], str(ITEM_CASES))
            self.assertEqual(emitter[emitter.index("--output") + 1], str(REPO / "corpora" / "items.yaml"))
            self.assertEqual(stock.calls, [])
            self.assertEqual(deadfin.calls, [])

    def test_preflight_builds_and_uses_native_replay_tools_for_each_selected_linux_image_architecture(self):
        for architecture in ("arm64", "amd64"):
            with self.subTest(architecture=architecture), PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
                image = f"deadfin:preflight-{architecture}"
                _, log_path, completed = self.run_preflight(
                    Path(directory),
                    stock,
                    deadfin,
                    build_default_helpers=True,
                    deadfin_image=image,
                    image_architecture=architecture,
                )

                self.assertEqual(completed.returncode, 0, completed.stderr)
                events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
                inspect_index = next(
                    index
                    for index, event in enumerate(events)
                    if event["event"] == "docker"
                    and event["args"][:2] == ["image", "inspect"]
                    and "--format" in event["args"]
                )
                inspect = events[inspect_index]["args"]
                self.assertIn(image, inspect)
                self.assertEqual(inspect[inspect.index("--format") + 1], "{{.Architecture}}")

                native_builds = [event for event in events if event["event"] == "docker" and event["args"][:1] == ["build"] and "replay-tools" in event["args"]]
                self.assertEqual(len(native_builds), 1, "default preflight must build exactly one native replay-tools image")
                native_build = native_builds[0]["args"]
                self.assertIn("--target", native_build)
                self.assertEqual(native_build[native_build.index("--target") + 1], "replay-tools")
                self.assertIn("--platform", native_build)
                self.assertEqual(native_build[native_build.index("--platform") + 1], f"linux/{architecture}")
                self.assertLess(inspect_index, events.index(native_builds[0]), "image architecture must be resolved before building native replay tools")
                self.assertFalse(any(event["event"] == "go" and event["args"][:1] == ["build"] for event in events), "default preflight must not build CGO-disabled host helpers")

                helper_runs = [event["args"] for event in events if event["event"] == "docker" and event["args"][:1] == ["run"] and "--detach" not in event["args"]]
                self.assertEqual(len(helper_runs), 2, helper_runs)
                bootstrap, seed = helper_runs
                self.assertIn("--entrypoint", bootstrap)
                self.assertEqual(bootstrap[bootstrap.index("--entrypoint") + 1], "/usr/local/bin/finalreplaybootstrap")
                self.assertIn("--entrypoint", seed)
                self.assertEqual(seed[seed.index("--entrypoint") + 1], "/usr/local/bin/goldenrec-seed-user")
                self.assertNotIn("dst=/usr/local/bin/finalreplaybootstrap,readonly", " ".join(bootstrap))
                self.assertNotIn("dst=/usr/local/bin/goldenrec-seed-user,readonly", " ".join(seed))
    def test_preflight_rebuilds_a_preexisting_runtime_tag_before_inspection_or_run(self):
        image = "deadfin:stale-runtime-tag"
        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            _, log_path, completed = self.run_preflight(Path(directory), stock, deadfin, deadfin_image=image)

            self.assertEqual(completed.returncode, 0, completed.stderr)
            events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
            runtime_builds = [
                (index, event["args"])
                for index, event in enumerate(events)
                if event["event"] == "docker"
                and event["args"][:1] == ["build"]
                and "replay-tools" not in event["args"]
            ]
            self.assertEqual(len(runtime_builds), 1, "preflight must replace a locally present runtime tag from source")
            runtime_build_index, runtime_build = runtime_builds[0]
            self.assertEqual(runtime_build[-1], str(REPO / "deadfin"), "runtime image must be built from the nested Deadfin checkout")
            self.assertIn(image, runtime_build)
            inspect_index = next(
                index
                for index, event in enumerate(events)
                if event["event"] == "docker" and event["args"][:2] == ["image", "inspect"] and image in event["args"]
            )
            run_index = next(index for index, event in enumerate(events) if event["event"] == "docker" and event["args"][:1] == ["run"])
            self.assertLess(runtime_build_index, inspect_index, "a preexisting tag must not be trusted before rebuilding from source")
            self.assertLess(runtime_build_index, run_index, "preflight must not run a stale runtime image")

    def test_preflight_rebuilds_an_explicit_runtime_tag_from_nested_checkout(self):
        image = "example.invalid/deadfin:explicit-review-tag"
        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            _, log_path, completed = self.run_preflight(Path(directory), stock, deadfin, deadfin_image=image)

            self.assertEqual(completed.returncode, 0, completed.stderr)
            events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
            runtime_build = next(
                event["args"]
                for event in events
                if event["event"] == "docker" and event["args"][:1] == ["build"] and "replay-tools" not in event["args"]
            )
            self.assertEqual(runtime_build[-1], str(REPO / "deadfin"))
            self.assertIn(image, runtime_build, "DEADFIN_IMAGE must select the tag rebuilt from the nested checkout")
            server = next(
                event["args"]
                for event in events
                if event["event"] == "docker" and event["args"][:1] == ["run"] and "--detach" in event["args"]
            )
            self.assertIn(image, server, "the rebuilt explicit tag must be the final runtime image")

    def test_preflight_keeps_explicit_helper_overrides_as_deliberate_escape_hatches(self):
        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            _, log_path, completed = self.run_preflight(Path(directory), stock, deadfin)

            self.assertEqual(completed.returncode, 0, completed.stderr)
            events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
            native_builds = [event for event in events if event["event"] == "docker" and event["args"][:1] == ["build"] and "replay-tools" in event["args"]]
            self.assertEqual(len(native_builds), 1, "the canonical native helper runtime remains available when an override is mounted")
            helper_runs = [event["args"] for event in events if event["event"] == "docker" and event["args"][:1] == ["run"] and "--detach" not in event["args"]]
            self.assertIn("dst=/usr/local/bin/finalreplaybootstrap,readonly", " ".join(helper_runs[0]))
            self.assertIn("dst=/usr/local/bin/goldenrec-seed-user,readonly", " ".join(helper_runs[1]))

    def test_replay_tools_are_named_but_absent_from_the_final_runtime_stage(self):
        dockerfile = (REPO / "deadfin" / "Dockerfile").read_text(encoding="utf-8")
        stages = [line.strip() for line in dockerfile.splitlines() if line.startswith("FROM ")]
        replay_tools = next((index for index, stage in enumerate(stages) if stage.endswith(" AS replay-tools")), None)
        self.assertIsNotNone(replay_tools, "Dockerfile must expose a named replay-tools build target")
        final_stage = stages[-1]
        self.assertEqual(final_stage, "FROM runtime", "final production image must start from the clean runtime stage")
        final_stage_source = dockerfile[dockerfile.rindex(final_stage):]
        self.assertNotIn("finalreplaybootstrap", final_stage_source)
        self.assertNotIn("goldenrec-seed-user", final_stage_source)

    def test_preflight_fails_closed_when_image_architecture_cannot_be_inspected(self):
        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            _, log_path, completed = self.run_preflight(
                Path(directory),
                stock,
                deadfin,
                build_default_helpers=True,
                image_inspect_fails=True,
            )

            self.assertNotEqual(completed.returncode, 0, "preflight continued without a resolved image architecture")
            events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
            inspections = [event for event in events if event["event"] == "docker" and event["args"][:2] == ["image", "inspect"]]
            self.assertEqual(len(inspections), 1, "preflight must fail closed after inspecting the freshly rebuilt runtime image")
            self.assertFalse(any(event["event"] == "go" for event in events), "preflight built helpers without a resolved image architecture")
            self.assertFalse(any(event["event"] == "docker" and event["args"][:1] == ["run"] for event in events), "preflight ran an image with an unresolved architecture")

    def test_preflight_rejects_an_unsupported_image_architecture_before_building_helpers(self):
        with PreflightServer(INVENTORY) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            _, log_path, completed = self.run_preflight(
                Path(directory),
                stock,
                deadfin,
                build_default_helpers=True,
                image_architecture="ppc64le",
            )

            self.assertNotEqual(completed.returncode, 0, "preflight accepted an unsupported Deadfin image architecture")
            events = [json.loads(line) for line in log_path.read_text(encoding="utf-8").splitlines()]
            self.assertEqual(
                sum(event["event"] == "docker" and event["args"][:2] == ["image", "inspect"] for event in events),
                1,
            )
            self.assertEqual(
                sum(event["event"] == "docker" and event["args"][:1] == ["build"] and "replay-tools" not in event["args"] for event in events),
                1,
                "preflight must rebuild the runtime before rejecting its unsupported architecture",
            )
            self.assertFalse(
                any(event["event"] == "docker" and event["args"][:1] == ["build"] and "replay-tools" in event["args"] for event in events),
                "preflight built replay helpers for an unsupported image architecture",
            )
            self.assertFalse(any(event["event"] == "go" for event in events), "preflight built helpers for an unsupported image architecture")
            self.assertFalse(any(event["event"] == "docker" and event["args"][:1] == ["run"] for event in events), "preflight ran an unsupported image architecture")

    def test_preflight_uses_media_browser_token_authorization_for_post_login_stock_requests(self):
        expected_authorization = 'MediaBrowser Client="Goldenrec preflight", Device="Goldenrec preflight", DeviceId="goldenrec-preflight-v1", Version="1.0.0", Token="preflight-test-token"'
        with PreflightServer(INVENTORY, require_media_browser_token=True) as stock, PreflightServer(INVENTORY) as deadfin, tempfile.TemporaryDirectory() as directory:
            _, _, completed = self.run_preflight(Path(directory), stock, deadfin)

            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertEqual(
                stock.authenticated_request_headers,
                [
                    ("/DisplayPreferences/usersettings", expected_authorization, None),
                    ("/Items", expected_authorization, None),
                ],
            )

    def run_reference_env(self, directory):
        root = directory / "subject"
        goldenrec = root / "tools" / "goldenrec"
        fixtures = goldenrec / "fixtures"
        fixtures.mkdir(parents=True)
        reference = goldenrec / "reference-env.sh"
        reference.write_bytes((PREFLIGHT.parent / "reference-env.sh").read_bytes())
        reference.chmod(0o755)
        log_path = directory / "reference-env-log.jsonl"
        for path, event in ((fixtures / "gen.sh", "fixtures-generated"), (goldenrec / "seed.sh", "stock-seeded")):
            path.write_text(
                "#!/usr/bin/env python3\n"
                "import json, os\n"
                "with open(os.environ['REFERENCE_LOG'], 'a', encoding='utf-8') as output:\n"
                f"    output.write(json.dumps({{'event': {event!r}}}) + '\\n')\n",
                encoding="utf-8",
            )
            path.chmod(0o755)
        fake_bin = directory / "reference-bin"
        fake_bin.mkdir()
        docker = fake_bin / "docker"
        docker.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, sys\n"
            "if sys.argv[1:] == ['compose', 'version']:\n"
            "    raise SystemExit(0)\n"
            "with open(os.environ['REFERENCE_LOG'], 'a', encoding='utf-8') as output:\n"
            "    output.write(json.dumps({'event': 'docker', 'args': sys.argv[1:]}) + '\\n')\n",
            encoding="utf-8",
        )
        docker.chmod(0o755)
        for command in ("ffmpeg", "curl"):
            executable = fake_bin / command
            executable.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
            executable.chmod(0o755)
        environment = os.environ | {
            "PATH": f"{fake_bin}{os.pathsep}{os.environ['PATH']}",
            "REFERENCE_LOG": str(log_path),
        }
        completed = subprocess.run([reference], cwd=root, env=environment, text=True, capture_output=True, timeout=10)
        return log_path, completed

    def run_preflight(
        self,
        directory,
        stock,
        deadfin,
        *,
        item_cases=ITEM_CASES,
        build_default_helpers=False,
        deadfin_image=None,
        image_architecture="amd64",
        image_inspect_fails=False,
        admin="ReplayAdmin",
        password="replay-admin-password",
        disabled_password="replay-disabled-password",
        fake_offline_failure=None,
        fixture_validation_failure=None,
        initial_generated_inputs=True,
        reference_env_generates_inputs=False,
        state_root_override=None,
        prepare_state=True,
        use_generated_root=False,
        extra_environment=None,
        claim_state_root=True,
    ):
        fake_bin = directory / "bin"
        fake_bin.mkdir()
        log_path = directory / "command-log.jsonl"
        fixtures = directory / "fixtures"
        snapshots = directory / "stock-state" / "snapshots"
        if initial_generated_inputs:
            fixtures.mkdir()
            snapshots.mkdir(parents=True)
            (snapshots / "id-map.json").write_text("[]\n", encoding="utf-8")
        reference = fake_bin / "reference-env"
        reference.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os\n"
            "from pathlib import Path\n"
            "fixtures = Path(os.environ['GOLDENREC_FIXTURES_DIR'])\n"
            "snapshots = Path(os.environ['PREFLIGHT_STOCK_SNAPSHOTS'])\n"
            "id_map = snapshots / 'id-map.json'\n"
            "if os.environ['PREFLIGHT_REFERENCE_GENERATES_INPUTS'] == '1':\n"
            "    fixtures.mkdir(parents=True, exist_ok=True)\n"
            "    snapshots.mkdir(parents=True, exist_ok=True)\n"
            "    id_map.write_text('[]\\n', encoding='utf-8')\n"
            "with open(os.environ['PREFLIGHT_LOG'], 'a', encoding='utf-8') as output:\n"
            "    output.write(json.dumps({'event': 'reference-env', 'port': os.environ['GOLDENREC_PORT'], 'base': os.environ['JELLYFIN_BASE_URL'], 'fixtures_created': fixtures.is_dir(), 'id_map_created': id_map.is_file()}) + '\\n')\n",
            encoding="utf-8",
        )
        reference.chmod(0o755)
        python = fake_bin / "python3"
        python.write_text(
            f"#!{sys.executable}\n"
            "import json, os, sys\n"
            "from pathlib import Path\n"
            "args = sys.argv[1:]\n"
            "def require_reference_outputs():\n"
            "    fixtures = Path(os.environ['GOLDENREC_FIXTURES_DIR'])\n"
            "    id_map = Path(os.environ['PREFLIGHT_STOCK_SNAPSHOTS']) / 'id-map.json'\n"
            "    if not fixtures.is_dir() or not id_map.is_file():\n"
            "        raise SystemExit('fake offline corpus check ran before reference environment generated fixtures and snapshot id-map')\n"
            "with open(os.environ['PREFLIGHT_LOG'], 'a', encoding='utf-8') as output:\n"
            "    output.write(json.dumps({'event': 'python3', 'args': args}) + '\\n')\n"
            "tool = Path(args[0]).name if args else ''\n"
            "if tool == 'record-itemquery.py':\n"
            "    require_reference_outputs()\n"
            "    if args[1:2] == ['validate-fixtures']:\n"
            "        if args[2:] != ['--fixtures', str(Path(os.environ['GOLDENREC_FIXTURES_DIR']).resolve())]:\n"
            "            raise SystemExit('fake fixture validator received an incomplete or noncanonical command')\n"
            "        failure = os.environ.get('PREFLIGHT_FIXTURE_VALIDATION_FAILURE', '')\n"
            "        if failure:\n"
            "            raise SystemExit('fake fixture validator rejected ' + failure)\n"
            "        raise SystemExit(0)\n"
            "    if args[1:2] != ['verify'] or '--cases' not in args or '--output-root' not in args:\n"
            "        raise SystemExit('fake offline verifier received an incomplete command')\n"
            "    cases = Path(args[args.index('--cases') + 1])\n"
            "    canonical = Path(os.environ['PREFLIGHT_CANONICAL_CASES'])\n"
            "    if cases.read_bytes() != canonical.read_bytes():\n"
            "        raise SystemExit('fake offline verifier rejected stale request-manifest provenance')\n"
            "    if os.environ.get('PREFLIGHT_FAKE_OFFLINE_FAILURE') == 'provenance':\n"
            "        raise SystemExit('fake offline verifier rejected stale golden provenance')\n"
            "    raise SystemExit(0)\n"
            "if tool == 'emit-item-corpus.py':\n"
            "    require_reference_outputs()\n"
            "    required = {'--check', '--requests', '--output'}\n"
            "    if not required.issubset(args[1:]):\n"
            "        raise SystemExit('fake item emitter received an incomplete command')\n"
            "    if os.environ.get('PREFLIGHT_FAKE_OFFLINE_FAILURE') == 'items':\n"
            "        raise SystemExit('fake item emitter rejected stale corpora/items.yaml')\n"
            "    raise SystemExit(0)\n"
            "os.execv(os.environ['PREFLIGHT_REAL_PYTHON'], [os.environ['PREFLIGHT_REAL_PYTHON'], *args])\n",
            encoding="utf-8",
        )
        python.chmod(0o755)
        docker = fake_bin / "docker"
        docker.write_text(
            f"#!{sys.executable}\n"
            "import json, os, stat, sys\n"
            "from pathlib import Path\n"
            "args = sys.argv[1:]\n"
            "event = {'event': 'docker', 'args': args}\n"
            "for index, argument in enumerate(args[:-1]):\n"
            "    if argument != '--mount':\n"
            "        continue\n"
            "    mount = dict(part.split('=', 1) for part in args[index + 1].split(',') if '=' in part)\n"
            "    if mount.get('dst') == '/state':\n"
            "        state_path = Path(mount['src'])\n"
            "        state_stat = state_path.stat()\n"
            "        event['state_mount'] = {'is_directory': state_path.is_dir(), 'is_symlink': state_path.is_symlink(), 'mode': stat.S_IMODE(state_stat.st_mode), 'uid': state_stat.st_uid}\n"
            "        if not state_path.is_dir() or state_path.is_symlink() or stat.S_IMODE(state_stat.st_mode) != 0o700 or state_stat.st_uid != os.geteuid():\n"
            "            raise SystemExit('fake docker rejected an unsafe /state bind source')\n"
            "    if mount.get('dst') == '/run/secrets/goldenrec.json':\n"
            "        credential_path = Path(mount['src'])\n"
            "        event['credential_file'] = {'source': str(credential_path), 'mode': stat.S_IMODE(credential_path.stat().st_mode), 'payload': json.loads(credential_path.read_text(encoding='utf-8')), 'readonly': 'readonly' in args[index + 1].split(',')}\n"
            "with open(os.environ['PREFLIGHT_LOG'], 'a', encoding='utf-8') as output:\n"
            "    output.write(json.dumps(event) + '\\n')\n"
            "if args[:2] == ['image', 'inspect']:\n"
            "    if os.environ.get('PREFLIGHT_IMAGE_INSPECT_FAILS') == '1':\n"
            "        raise SystemExit(42)\n"
            "    if '--format' in args:\n"
            "        print(os.environ['PREFLIGHT_IMAGE_ARCHITECTURE'])\n"
            "raise SystemExit(0)\n",
            encoding="utf-8",
        )
        docker.chmod(0o755)
        go = fake_bin / "go"
        go.write_text(
            f"#!{sys.executable}\n"
            "import json, os, sys\n"
            "from pathlib import Path\n"
            "args = sys.argv[1:]\n"
            "if args[:1] == ['build']:\n"
            "    with open(os.environ['PREFLIGHT_LOG'], 'a', encoding='utf-8') as output:\n"
            "        output.write(json.dumps({'event': 'go', 'args': args, 'environment': {name: os.environ.get(name) for name in ('GOOS', 'GOARCH', 'CGO_ENABLED')}}) + '\\n')\n"
            "    destination = Path(args[args.index('-o') + 1])\n"
            "    destination.write_text('#!/bin/sh\\nexit 0\\n', encoding='utf-8')\n"
            "    destination.chmod(0o755)\n",
            encoding="utf-8",
        )
        if not build_default_helpers:
            bootstrap = fake_bin / "finalreplaybootstrap"
            seed_user = fake_bin / "goldenrec-seed-user"
            for executable in (bootstrap, seed_user):
                executable.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
                executable.chmod(0o755)

        state_root = state_root_override or directory / "replay-state"
        stale_state = state_root / "deadfin-data"
        if prepare_state and not use_generated_root:
            state_root.mkdir(parents=True, exist_ok=True)
            marker = state_root / ".goldenrec-final-replay-owner"
            if claim_state_root:
                marker.write_text(json.dumps({"format": "goldenrec-final-replay-owner/v1", "uid": os.geteuid(), "root": str(state_root.resolve())}), encoding="utf-8")
                marker.chmod(0o600)
            stale_state.mkdir(parents=True, exist_ok=True)
            (stale_state / "stale-session").write_text("must be discarded", encoding="utf-8")
        environment = os.environ | {
            "PATH": f"{fake_bin}{os.pathsep}{os.environ['PATH']}",
            "PREFLIGHT_LOG": str(log_path),
            "PREFLIGHT_REAL_PYTHON": sys.executable,
            "PREFLIGHT_CANONICAL_CASES": str(ITEM_CASES),
            "PREFLIGHT_FAKE_OFFLINE_FAILURE": fake_offline_failure or "",
            "PREFLIGHT_FIXTURE_VALIDATION_FAILURE": fixture_validation_failure or "",
            "PREFLIGHT_IMAGE_ARCHITECTURE": image_architecture,
            "PREFLIGHT_IMAGE_INSPECT_FAILS": "1" if image_inspect_fails else "0",
            "PREFLIGHT_STOCK_SNAPSHOTS": str(snapshots),
            "PREFLIGHT_REFERENCE_GENERATES_INPUTS": "1" if reference_env_generates_inputs else "0",
            "GOLDENREC_REFERENCE_ENV_SCRIPT": str(reference),
            "GOLDENREC_FIXTURES_DIR": str(fixtures),
            "GOLDENREC_FINAL_REPLAY_ROOT": str(state_root),
            "GOLDENREC_PORT": str(stock.port),
            "DEADFIN_PORT": str(deadfin.port),
            "JELLYFIN_ADMIN": admin,
            "JELLYFIN_PASSWORD": password,
            "GOLDENREC_DISABLED_PASSWORD": disabled_password,
            "GOLDENREC_ITEM_CASES": str(item_cases),
        }
        if use_generated_root:
            environment.pop("GOLDENREC_FINAL_REPLAY_ROOT")
        if extra_environment:
            environment |= extra_environment
        if not build_default_helpers:
            environment |= {
                "GOLDENREC_BOOTSTRAP_BIN": str(bootstrap),
                "GOLDENREC_SEED_USER_BIN": str(seed_user),
            }
        if deadfin_image is not None:
            environment["DEADFIN_IMAGE"] = deadfin_image
        completed = subprocess.run([PREFLIGHT], cwd=REPO, env=environment, text=True, capture_output=True, timeout=20)
        return state_root, log_path, completed

    def create_rerunnable_preflight_harness(self, directory):
        fake_bin = directory / "rerunnable-bin"
        fake_bin.mkdir()
        log_path = directory / "rerunnable-log.jsonl"
        registry_path = directory / "rerunnable-containers.json"
        registry_path.write_text("{}", encoding="utf-8")
        fixtures = directory / "rerunnable-fixtures"
        snapshots = directory / "rerunnable-stock-state" / "snapshots"
        reference = fake_bin / "reference-env"
        reference.write_text(
            "#!/usr/bin/env python3\n"
            "import os\n"
            "from pathlib import Path\n"
            "Path(os.environ['GOLDENREC_FIXTURES_DIR']).mkdir(parents=True, exist_ok=True)\n"
            "snapshots = Path(os.environ['PREFLIGHT_RERUNNABLE_SNAPSHOTS'])\n"
            "snapshots.mkdir(parents=True, exist_ok=True)\n"
            "(snapshots / 'id-map.json').write_text('[]\\n', encoding='utf-8')\n",
            encoding="utf-8",
        )
        reference.chmod(0o755)
        python = fake_bin / "python3"
        python.write_text(
            f"#!{sys.executable}\n"
            "import os, sys\n"
            "from pathlib import Path\n"
            "args = sys.argv[1:]\n"
            "if args and Path(args[0]).name in {'record-itemquery.py', 'emit-item-corpus.py'}:\n"
            "    raise SystemExit(0)\n"
            "os.execv(os.environ['PREFLIGHT_REAL_PYTHON'], [os.environ['PREFLIGHT_REAL_PYTHON'], *args])\n",
            encoding="utf-8",
        )
        python.chmod(0o755)
        for executable_name in ("finalreplaybootstrap", "goldenrec-seed-user", "go"):
            executable = fake_bin / executable_name
            executable.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
            executable.chmod(0o755)
        docker = fake_bin / "docker"
        docker.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, sys\n"
            "from pathlib import Path\n"
            "args = sys.argv[1:]\n"
            "registry_path = Path(os.environ['PREFLIGHT_DOCKER_REGISTRY'])\n"
            "registry = json.loads(registry_path.read_text(encoding='utf-8'))\n"
            "def log():\n"
            "    with open(os.environ['PREFLIGHT_LOG'], 'a', encoding='utf-8') as output:\n"
            "        output.write(json.dumps({'event': 'docker', 'run': os.environ['PREFLIGHT_RUN_ID'], 'args': args}) + '\\n')\n"
            "def state_mount():\n"
            "    for index, argument in enumerate(args[:-1]):\n"
            "        if argument == '--mount' and 'dst=/state' in args[index + 1]:\n"
            "            return dict(part.split('=', 1) for part in args[index + 1].split(',')).get('src')\n"
            "    raise SystemExit('missing /state mount')\n"
            "log()\n"
            "if args[:2] == ['image', 'inspect']:\n"
            "    if '--format' in args:\n"
            "        print('amd64')\n"
            "elif args[:2] == ['rm', '-f']:\n"
            "    registry.pop(args[2], None)\n"
            "    registry_path.write_text(json.dumps(registry), encoding='utf-8')\n"
            "elif args[:1] == ['run'] and '--detach' in args:\n"
            "    name = args[args.index('--name') + 1]\n"
            "    registry[name] = {'state': state_mount(), 'publish': args[args.index('--publish') + 1]}\n"
            "    registry_path.write_text(json.dumps(registry), encoding='utf-8')\n"
            "elif args[:1] == ['run'] and 'finalreplaybootstrap' in ' '.join(args):\n"
            "    state = state_mount()\n"
            "    if any(container['state'] == state for container in registry.values()):\n"
            "        raise SystemExit('SQLITE_BUSY: prior container still mounts state')\n",
            encoding="utf-8",
        )
        docker.chmod(0o755)
        rm = fake_bin / "rm"
        rm.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, shutil, sys\n"
            "from pathlib import Path\n"
            "args = sys.argv[1:]\n"
            "target = next(argument for argument in args if not argument.startswith('-'))\n"
            "with open(os.environ['PREFLIGHT_LOG'], 'a', encoding='utf-8') as output:\n"
            "    output.write(json.dumps({'event': 'rm', 'run': os.environ['PREFLIGHT_RUN_ID'], 'args': args}) + '\\n')\n"
            "registry = json.loads(Path(os.environ['PREFLIGHT_DOCKER_REGISTRY']).read_text(encoding='utf-8'))\n"
            "if any(container['state'] == target for container in registry.values()):\n"
            "    raise SystemExit('state deletion while an existing container still mounts it')\n"
            "shutil.rmtree(target, ignore_errors=True)\n",
            encoding="utf-8",
        )
        rm.chmod(0o755)
        return {"bin": fake_bin, "log": log_path, "registry": registry_path, "reference": reference, "fixtures": fixtures, "snapshots": snapshots}

    def run_rerunnable_preflight(self, lifecycle, run_id, state_root, stock, deadfin):
        state_root.mkdir(parents=True, exist_ok=True)
        marker = state_root / ".goldenrec-final-replay-owner"
        if not marker.exists():
            marker.write_text(json.dumps({"format": "goldenrec-final-replay-owner/v1", "uid": os.geteuid(), "root": str(state_root.resolve())}), encoding="utf-8")
            marker.chmod(0o600)
        stale_state = state_root / "deadfin-data"
        stale_state.mkdir(parents=True, exist_ok=True)
        (stale_state / "stale-session").write_text("must be discarded", encoding="utf-8")
        environment = os.environ | {
            "PATH": f"{lifecycle['bin']}{os.pathsep}{os.environ['PATH']}",
            "PREFLIGHT_LOG": str(lifecycle["log"]),
            "PREFLIGHT_DOCKER_REGISTRY": str(lifecycle["registry"]),
            "PREFLIGHT_RUN_ID": run_id,
            "PREFLIGHT_REAL_PYTHON": sys.executable,
            "GOLDENREC_REFERENCE_ENV_SCRIPT": str(lifecycle["reference"]),
            "GOLDENREC_FIXTURES_DIR": str(lifecycle["fixtures"]),
            "PREFLIGHT_RERUNNABLE_SNAPSHOTS": str(lifecycle["snapshots"]),
            "GOLDENREC_FINAL_REPLAY_ROOT": str(state_root),
            "GOLDENREC_PORT": str(stock.port),
            "DEADFIN_PORT": str(deadfin.port),
            "JELLYFIN_ADMIN": "ReplayAdmin",
            "JELLYFIN_PASSWORD": "replay-admin-password",
            "GOLDENREC_DISABLED_PASSWORD": "replay-disabled-password",
            "GOLDENREC_BOOTSTRAP_BIN": str(lifecycle["bin"] / "finalreplaybootstrap"),
            "GOLDENREC_SEED_USER_BIN": str(lifecycle["bin"] / "goldenrec-seed-user"),
        }
        return subprocess.run([PREFLIGHT], cwd=REPO, env=environment, text=True, capture_output=True, timeout=20)

    def read_rerunnable_containers(self, lifecycle):
        return json.loads(lifecycle["registry"].read_text(encoding="utf-8"))

    def read_rerunnable_events(self, lifecycle, run_id):
        return [
            event
            for event in (json.loads(line) for line in lifecycle["log"].read_text(encoding="utf-8").splitlines())
            if event["run"] == run_id
        ]


class NativeReplayImageTests(unittest.TestCase):
    """Opt-in checks against images built by the focused Docker commands below."""

    @unittest.skipUnless(os.environ.get("GOLDENREC_NATIVE_REPLAY_IMAGE"), "set GOLDENREC_NATIVE_REPLAY_IMAGE to a replay-tools image")
    def test_packaged_replay_tools_persist_the_pinned_stock_blurhash(self):
        image = os.environ["GOLDENREC_NATIVE_REPLAY_IMAGE"]
        fixtures = REPO / "tools" / "goldenrec" / "fixtures" / "out"
        expected_jpeg_path = "/media/movies/Standalone Movie (2023)/Standalone Movie (2023).jpg"
        expected_jpeg_hash = "W8G+RE%NbF%MbZx]?coXn#k9oIoebMSQbFS2bZOA?dbTnzbXn~k9"
        expected_webp_artwork = "/media/movies/Subtitle Sample (2024)/Subtitle Sample (2024).webp"
        expected_webp_hash = "W9G+H;?GkC%MR:-p~CVfcUpDr^ovw3#pi$SeWG$Nu4xZmtR*VznQ"
        expected_webp_items = {
            "/media/movies/Subtitle Sample (2024)",
            "/media/movies/Subtitle Sample (2024)/Subtitle Sample (2024).mkv",
        }

        with tempfile.TemporaryDirectory(prefix=".native-replay-witness-", dir=REPO) as directory:
            state = Path(directory) / "state"
            state.mkdir()
            completed = subprocess.run(
                [
                    "docker", "run", "--rm", "--user", f"{os.getuid()}:{os.getgid()}",
                    "--mount", f"type=bind,src={state},dst=/state",
                    "--mount", f"type=bind,src={fixtures},dst=/media,readonly",
                    "--entrypoint", "/usr/local/bin/finalreplaybootstrap", image, "-state", "/state",
                ],
                cwd=REPO,
                text=True,
                capture_output=True,
                timeout=180,
            )
            self.assertEqual(completed.returncode, 0, completed.stderr)
            with sqlite3.connect(state / "data" / "deadfin.db") as database:
                persisted_jpeg_hashes = {
                    row[0]
                    for row in database.execute(
                        "SELECT blurhash FROM item_images WHERE path = ? AND blurhash IS NOT NULL",
                        (expected_jpeg_path,),
                    )
                }
                persisted_webp_rows = set(
                    database.execute(
                        """
                        SELECT items.path, item_images.path, item_images.blurhash
                        FROM item_images
                        JOIN items ON items.id = item_images.item_id
                        WHERE item_images.path = ? AND items.path IN (?, ?)
                        """,
                        (expected_webp_artwork, *sorted(expected_webp_items)),
                    )
                )
            self.assertEqual(persisted_jpeg_hashes, {expected_jpeg_hash})
            self.assertEqual(
                persisted_webp_rows,
                {(path, expected_webp_artwork, expected_webp_hash) for path in expected_webp_items},
            )

    @unittest.skipUnless(os.environ.get("GOLDENREC_FINAL_IMAGE"), "set GOLDENREC_FINAL_IMAGE to the production Deadfin image")
    def test_final_image_excludes_replay_only_helpers(self):
        image = os.environ["GOLDENREC_FINAL_IMAGE"]
        completed = subprocess.run(
            [
                "docker", "run", "--rm", "--entrypoint", "/bin/sh", image,
                "-ec", "test ! -e /usr/local/bin/finalreplaybootstrap && test ! -e /usr/local/bin/goldenrec-seed-user",
            ],
            cwd=REPO,
            text=True,
            capture_output=True,
            timeout=30,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)

if __name__ == "__main__":
    unittest.main()
