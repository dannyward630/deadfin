import hashlib
import importlib.util
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


GOLDENREC = Path(__file__).resolve().parents[1]
PROJECT_ROOT = GOLDENREC.parents[1]
GENERATOR = GOLDENREC / "fixtures" / "gen.sh"
FIXTURE_LOCK = GOLDENREC / "fixtures" / ".fixture-publish.lock"
HLG_ARTIFACT = GOLDENREC / "fixtures" / "attested" / "hlg-sample.mkv"
SOURCE_MANIFEST = GOLDENREC / "fixtures" / "source-manifest.sha256"
FIXTURE_WRAPPER = PROJECT_ROOT / "tools" / "verify-env" / "generate-fixtures.sh"
FIXTURE_COMPOSE = PROJECT_ROOT / "tools" / "verify-env" / "compose.yaml"
FIXTURE_ENTRYPOINT = PROJECT_ROOT / "tools" / "verify-env" / "fixture-generator-entrypoint.sh"
FIXTURE_PUBLISHER = PROJECT_ROOT / "tools" / "verify-env" / "publish-fixtures.py"

LEGACY_MARKER = b"goldenrec-fixtures-v1\n"
MARKER_FORMAT = "goldenrec-fixtures-owner-v1"
MARKER_NAME = ".goldenrec-fixtures-owner"
STABLE_MARKER_ROOT = "/work/tools/goldenrec/fixtures/out"


def manifest_entries(manifest):
    """Return each manifest's relative path and digest for concise failure output."""
    entries = {}
    for line in manifest.splitlines():
        digest, separator, relative_path = line.partition(b"  ")
        if not separator:
            raise AssertionError(f"unparseable manifest entry: {line!r}")
        entries[os.fsdecode(relative_path)] = digest
    return entries


class FixtureDeterminismTests(unittest.TestCase):
    @staticmethod
    def run_generator(output, **environment_overrides):
        environment = os.environ.copy()
        environment.update(environment_overrides)
        environment["GOLDENREC_FIXTURES_DIR"] = str(output)
        return subprocess.run(
            [str(GENERATOR)],
            cwd=PROJECT_ROOT,
            capture_output=True,
            text=True,
            env=environment,
        )

    @staticmethod
    def run_copied_generator(generator, output, **environment_overrides):
        environment = os.environ.copy()
        environment.update(environment_overrides)
        environment["GOLDENREC_FIXTURES_DIR"] = str(output)
        return subprocess.run(
            [str(generator)],
            cwd=generator.parents[3],
            capture_output=True,
            text=True,
            env=environment,
        )

    @staticmethod
    def run_default_generator(generator, **environment_overrides):
        environment = os.environ.copy()
        environment.pop("GOLDENREC_FIXTURES_DIR", None)
        environment.pop("GOLDENREC_CANONICAL_GENERATOR", None)
        environment.pop("GOLDENREC_ATTEST_SOURCE_MANIFEST", None)
        environment.update(environment_overrides)
        return subprocess.run(
            [str(generator)],
            cwd=generator.parents[3],
            capture_output=True,
            text=True,
            env=environment,
        )

    @staticmethod
    def copy_generator_tree(temporary_root):
        fixtures = temporary_root / "tools" / "goldenrec" / "fixtures"
        fixtures.mkdir(parents=True)
        generator = fixtures / "gen.sh"
        shutil.copy2(GENERATOR, generator)
        shutil.copy2(SOURCE_MANIFEST, fixtures / "source-manifest.sha256")
        attested = fixtures / "attested"
        attested.mkdir()
        shutil.copy2(HLG_ARTIFACT, attested / "hlg-sample.mkv")
        return generator, fixtures / "out"

    @staticmethod
    def output_snapshot(output):
        return {
            path.relative_to(output).as_posix(): path.read_bytes()
            for path in sorted(output.rglob("*"))
            if path.is_file()
        }

    @staticmethod
    def write_failing_ffmpeg(bin_directory):
        """Create an ffmpeg seam that reaches generation then aborts cheaply."""
        executable = bin_directory / "ffmpeg"
        executable.write_text(
            "#!/bin/sh\n"
            "for argument do\n"
            "  if [ \"$argument\" = \"-version\" ]; then\n"
            "    printf '%s\\n' 'ffmpeg fixture-test seam'\n"
            "    exit 0\n"
            "  fi\n"
            "done\n"
            "printf '%s\\n' 'intentional ffmpeg fixture-test failure' >&2\n"
            "exit 97\n"
        )
        executable.chmod(0o700)
        return executable

    @staticmethod
    def write_fixture_ffmpeg(bin_directory, version="ffmpeg fixture-test seam"):
        """Create a tiny ffmpeg seam that satisfies the fixture generator's file contract."""
        executable = bin_directory / "ffmpeg"
        executable.write_text(
            "#!/bin/sh\n"
            "for argument do\n"
            f"  if [ \"$argument\" = \"-version\" ]; then printf '%s\\n' '{version}'; exit 0; fi\n"
            "done\n"
            "if [ -n \"${GOLDENREC_FFMPEG_ARGV_LOG:-}\" ]; then\n"
            "  printf '%s\\0' \"$0\" \"$@\" >> \"$GOLDENREC_FFMPEG_ARGV_LOG\"\n"
            "  printf '\\0' >> \"$GOLDENREC_FFMPEG_ARGV_LOG\"\n"
            "fi\n"
            "target=\n"
            "for argument do target=$argument; done\n"
            "case \"$target\" in\n"
            "  *.jpg) printf '\\377\\330fixture-jpeg' > \"$target\" ;;\n"
            "  *) printf '%s\\n' 'fake generated media' > \"$target\" ;;\n"
            "esac\n"
        )
        executable.chmod(0o700)
        return executable

    @staticmethod
    def expected_manifest_bytes(output):
        excluded = {".goldenrec-fixtures-owner", "MANIFEST.sha256"}
        return b"".join(
            (
                f"{hashlib.sha256(path.read_bytes()).hexdigest()}  "
                f"{path.relative_to(output).as_posix()}\n"
            ).encode()
            for path in sorted(output.rglob("*"))
            if path.is_file() and path.name not in excluded
        )

    @staticmethod
    def write_removal_tripwire(bin_directory, log):
        """Make an accidental shell rm observable without risking test data."""
        executable = bin_directory / "rm"
        executable.write_text(
            "#!/bin/sh\n"
            "printf '%s\\n' \"$*\" >> \"$GOLDENREC_RM_TRIPWIRE\"\n"
            "exit 91\n"
        )
        executable.chmod(0o700)
        return executable

    @staticmethod
    def assert_owned_marker(test_case, output):
        marker = output / ".goldenrec-fixtures-owner"
        test_case.assertTrue(output.is_dir(), "generator did not create its output root")
        test_case.assertFalse(output.is_symlink(), "output root must not be a symlink")
        test_case.assertEqual(output.stat().st_uid, os.getuid())
        test_case.assertEqual(output.stat().st_mode & 0o777, 0o700)
        test_case.assertTrue(marker.is_file(), "generator did not create its ownership marker")
        test_case.assertFalse(marker.is_symlink(), "ownership marker must not be a symlink")
        test_case.assertEqual(marker.stat().st_uid, os.getuid())
        test_case.assertEqual(marker.stat().st_mode & 0o777, 0o600)
        test_case.assertEqual(
            marker.read_bytes(),
            (
                json.dumps(
                    {"format": MARKER_FORMAT, "root": str(output.resolve()), "uid": os.getuid()},
                    sort_keys=True,
                    separators=(",", ":"),
                )
                + "\n"
            ).encode(),
        )

    def test_new_override_reaches_generation_after_claiming_ownership(self):
        """A fresh, non-dangerous override can be claimed before a cheap generator failure."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-fixture-safe-new-") as directory:
            temporary_root = Path(directory)
            tools = temporary_root / "tools"
            tools.mkdir()
            ffmpeg = self.write_failing_ffmpeg(tools)
            output = temporary_root / "new" / "fixtures"

            result = self.run_generator(output, FFMPEG=str(ffmpeg))

            self.assertEqual(result.returncode, 97, result.stderr or result.stdout)
            self.assert_owned_marker(self, output)
            self.assertEqual((output / "FFMPEG.version").read_bytes(), b"ffmpeg fixture-test seam\n")

    def test_default_output_rejects_direct_host_generation_before_mutation(self):
        """Direct generation cannot select the committed output."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-fixture-canonical-") as directory:
            temporary_root = Path(directory)
            generator, output = self.copy_generator_tree(temporary_root)
            tools = temporary_root / "bin"
            tools.mkdir()
            ffmpeg = self.write_failing_ffmpeg(tools)

            result = self.run_default_generator(generator, FFMPEG=str(ffmpeg))

            self.assertNotEqual(result.returncode, 0, result.stderr or result.stdout)
            self.assertIn("GOLDENREC_FIXTURES_DIR", result.stderr)
            self.assertFalse(output.exists(), "direct host generation created the default output")

    def test_default_output_rejects_forged_publication_environment(self):
        """Caller-set canonical and attest variables cannot admit default output."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-fixture-canonical-version-") as directory:
            temporary_root = Path(directory)
            generator, output = self.copy_generator_tree(temporary_root)
            tools = temporary_root / "bin"
            tools.mkdir()
            ffmpeg = self.write_failing_ffmpeg(tools)
            output.mkdir(mode=0o700)
            output.chmod(0o700)
            marker = output / ".goldenrec-fixtures-owner"
            marker.write_bytes(
                (
                    json.dumps(
                        {"format": MARKER_FORMAT, "root": str(output.resolve()), "uid": os.getuid()},
                        sort_keys=True,
                        separators=(",", ":"),
                    )
                    + "\n"
                ).encode()
            )
            marker.chmod(0o600)
            sentinel = output / "do-not-replace.bin"
            sentinel.write_bytes(b"existing committed projection\n")

            result = self.run_default_generator(
                generator,
                FFMPEG=str(ffmpeg),
                GOLDENREC_CANONICAL_GENERATOR="verify-env-linux-amd64-v1",
                GOLDENREC_ATTEST_SOURCE_MANIFEST="1",
                GOLDENREC_FIXTURES_DIR=str(output),
            )

            self.assertNotEqual(result.returncode, 0, result.stderr or result.stdout)
            self.assertIn("committed output requires", result.stderr)
            self.assertEqual(sentinel.read_bytes(), b"existing committed projection\n")

    def test_generator_owned_override_is_rerunnable(self):
        """Only a safe, attested output root is replaced on a subsequent run."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-fixture-owned-rerun-") as directory:
            temporary_root = Path(directory)
            tools = temporary_root / "tools"
            tools.mkdir()
            ffmpeg = self.write_fixture_ffmpeg(tools)
            output = temporary_root / "fixtures"

            claimed = self.run_generator(output, FFMPEG=str(ffmpeg))

            self.assertEqual(claimed.returncode, 0, claimed.stderr or claimed.stdout)
            self.assert_owned_marker(self, output)
            stale = output / "partial-fixture"
            stale.write_bytes(b"stale generated content")

            result = self.run_generator(output, FFMPEG=str(ffmpeg))

            self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
            self.assertFalse(stale.exists(), "safe owned partial output was not replaced")
            self.assert_owned_marker(self, output)

    def test_ownership_marker_is_excluded_from_media_attestation(self):
        """Ownership metadata never changes the attested generated-media manifest."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-fixture-manifest-") as directory:
            temporary_root = Path(directory)
            tools = temporary_root / "tools"
            tools.mkdir()
            ffmpeg = self.write_fixture_ffmpeg(tools)
            output = temporary_root / "fixtures"

            result = self.run_generator(output, FFMPEG=str(ffmpeg))

            self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
            self.assert_owned_marker(self, output)
            manifest = (output / "MANIFEST.sha256").read_bytes()
            self.assertNotIn(b".goldenrec-fixtures-owner", manifest)
            self.assertEqual(manifest, self.expected_manifest_bytes(output))

    def test_existing_unsafe_override_rejects_without_touching_sentinel(self):
        """Only a root-bound, private marker authorizes replacement of an existing output."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-fixture-unowned-") as directory:
            temporary_root = Path(directory)
            tools = temporary_root / "tools"
            tools.mkdir()
            ffmpeg = self.write_failing_ffmpeg(tools)
            sentinel_bytes = b"unrelated user data\x00must survive"

            def claim(output):
                claimed = self.run_generator(output, FFMPEG=str(ffmpeg))
                self.assertEqual(claimed.returncode, 97, claimed.stderr or claimed.stdout)
                self.assert_owned_marker(self, output)

            def reject(name, output, sentinel):
                removal_log = temporary_root / f"unexpected-{name}-rm.log"
                self.write_removal_tripwire(tools, removal_log)
                result = self.run_generator(
                    output,
                    FFMPEG=str(ffmpeg),
                    GOLDENREC_RM_TRIPWIRE=str(removal_log),
                    PATH=f"{tools}{os.pathsep}{os.environ['PATH']}",
                )
                self.assertNotEqual(result.returncode, 0, result.stderr or result.stdout)
                self.assertFalse(removal_log.exists(), f"{name} output reached rm before rejection")
                self.assertEqual(sentinel.read_bytes(), sentinel_bytes, f"{name} sentinel was changed")

            with self.subTest(destination="unowned"):
                output = temporary_root / "user-library"
                output.mkdir(mode=0o700)
                output.chmod(0o700)
                sentinel = output / "do-not-delete.bin"
                sentinel.write_bytes(sentinel_bytes)
                reject("unowned", output, sentinel)

            with self.subTest(destination="legacy_plain_marker"):
                output = temporary_root / "legacy-marker"
                output.mkdir(mode=0o700)
                output.chmod(0o700)
                marker = output / ".goldenrec-fixtures-owner"
                marker.write_bytes(LEGACY_MARKER)
                marker.chmod(0o600)
                sentinel = output / "do-not-delete.bin"
                sentinel.write_bytes(sentinel_bytes)
                reject("legacy", output, sentinel)

            source = temporary_root / "attested-source"
            claim(source)
            source_marker = source / ".goldenrec-fixtures-owner"

            with self.subTest(destination="copied_marker"):
                output = temporary_root / "copied-marker"
                output.mkdir(mode=0o700)
                output.chmod(0o700)
                marker = output / ".goldenrec-fixtures-owner"
                marker.write_bytes(source_marker.read_bytes())
                marker.chmod(source_marker.stat().st_mode & 0o777)
                sentinel = output / "do-not-delete.bin"
                sentinel.write_bytes(sentinel_bytes)
                reject("copied-marker", output, sentinel)

            with self.subTest(destination="marker_mode"):
                output = temporary_root / "marker-mode"
                claim(output)
                marker = output / ".goldenrec-fixtures-owner"
                marker.chmod(0o644)
                sentinel = output / "do-not-delete.bin"
                sentinel.write_bytes(sentinel_bytes)
                reject("marker-mode", output, sentinel)

            with self.subTest(destination="marker_uid"):
                output = temporary_root / "marker-uid"
                claim(output)
                marker = output / ".goldenrec-fixtures-owner"
                marker_payload = json.loads(marker.read_bytes())
                marker_payload["uid"] = os.getuid() + 1
                marker.write_text(json.dumps(marker_payload, sort_keys=True, separators=(",", ":")) + "\n")
                sentinel = output / "do-not-delete.bin"
                sentinel.write_bytes(sentinel_bytes)
                reject("marker-uid", output, sentinel)

            with self.subTest(destination="group_writable_root"):
                output = temporary_root / "group-writable"
                claim(output)
                output.chmod(0o770)
                sentinel = output / "do-not-delete.bin"
                sentinel.write_bytes(sentinel_bytes)
                reject("group-writable", output, sentinel)

            with self.subTest(destination="symlink_root"):
                target = temporary_root / "symlink-target"
                target.mkdir(mode=0o700)
                target.chmod(0o700)
                sentinel = target / "do-not-delete.bin"
                sentinel.write_bytes(sentinel_bytes)
                output = temporary_root / "symlink-root"
                output.symlink_to(target, target_is_directory=True)
                reject("symlink-root", output, sentinel)

            if os.geteuid() == 0:
                with self.subTest(destination="foreign_marker_owner"):
                    output = temporary_root / "foreign-marker-owner"
                    claim(output)
                    marker = output / ".goldenrec-fixtures-owner"
                    os.chown(marker, 1, -1)
                    sentinel = output / "do-not-delete.bin"
                    sentinel.write_bytes(sentinel_bytes)
                    reject("foreign-marker-owner", output, sentinel)

    def test_home_repo_and_root_shaped_overrides_reject_before_mutation(self):
        """Destructive destination shapes are rejected before invoking rm or ffmpeg."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-fixture-dangerous-") as directory:
            temporary_root = Path(directory)
            tools = temporary_root / "tools"
            tools.mkdir()
            ffmpeg = self.write_failing_ffmpeg(tools)
            home = temporary_root / "home"
            home.mkdir()
            home_sentinel = home / "home-sentinel"
            home_sentinel_bytes = b"home must not be cleared"
            home_sentinel.write_bytes(home_sentinel_bytes)
            cases = (
                ("home", home, home_sentinel, home_sentinel_bytes),
                ("repository", PROJECT_ROOT, GENERATOR, GENERATOR.read_bytes()),
                ("root", Path(os.path.sep), None, None),
            )

            for name, output, sentinel, sentinel_bytes in cases:
                with self.subTest(destination=name):
                    removal_log = temporary_root / f"unexpected-{name}-rm.log"
                    self.write_removal_tripwire(tools, removal_log)
                    result = self.run_generator(
                        output,
                        FFMPEG=str(ffmpeg),
                        GOLDENREC_RM_TRIPWIRE=str(removal_log),
                        HOME=str(home),
                        PATH=f"{tools}{os.pathsep}{os.environ['PATH']}",
                    )

                    self.assertNotEqual(result.returncode, 0, result.stderr or result.stdout)
                    self.assertFalse(removal_log.exists(), f"{name} output reached rm before rejection")
                    if sentinel is not None:
                        self.assertEqual(sentinel.read_bytes(), sentinel_bytes)

    def test_same_ffmpeg_build_generates_identical_manifest_bytes(self):
        """A clean rebuild with one ffmpeg build preserves fixture provenance exactly."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-fixtures-first-") as first_directory, tempfile.TemporaryDirectory(
            prefix="goldenrec-fixtures-second-"
        ) as second_directory:
            first_output = Path(first_directory) / "fixtures"
            second_output = Path(second_directory) / "fixtures"
            first_result = self.run_generator(first_output)
            second_result = self.run_generator(second_output)

            failures = []
            for name, result in (("first", first_result), ("second", second_result)):
                if result.returncode:
                    failures.append(
                        f"{name} generator exit {result.returncode}: {result.stderr or result.stdout}"
                    )
            self.assertFalse(failures, "\n".join(failures))

            first_version = (first_output / "FFMPEG.version").read_bytes()
            second_version = (second_output / "FFMPEG.version").read_bytes()
            if first_version != second_version:
                self.fail(
                    "FFMPEG.version differs between generator runs "
                    f"({hashlib.sha256(first_version).hexdigest()} != "
                    f"{hashlib.sha256(second_version).hexdigest()})"
                )

            first_manifest = (first_output / "MANIFEST.sha256").read_bytes()
            second_manifest = (second_output / "MANIFEST.sha256").read_bytes()
            if first_manifest != second_manifest:
                first_entries = manifest_entries(first_manifest)
                second_entries = manifest_entries(second_manifest)
                differing_paths = sorted(
                    path
                    for path in first_entries.keys() | second_entries.keys()
                    if first_entries.get(path) != second_entries.get(path)
                )
                self.fail(
                    "MANIFEST.sha256 differs across runs using the same ffmpeg build; "
                    "differing relative paths:\n"
                    + "\n".join(differing_paths)
                )

    def test_mkv_commands_disable_simd_dispatch(self):
        """The 18 mpeg4 MKV commands force a fixed scalar single-threaded profile and the HLG sample is copied, not encoded."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-fixture-cpuflags-") as directory:
            temporary_root = Path(directory)
            tools = temporary_root / "tools"
            tools.mkdir()
            ffmpeg = self.write_fixture_ffmpeg(tools)
            output = temporary_root / "fixtures"
            argv_log = temporary_root / "ffmpeg-argv.log"

            result = self.run_generator(
                output,
                FFMPEG=str(ffmpeg),
                GOLDENREC_FFMPEG_ARGV_LOG=str(argv_log),
            )

            self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
            invocations = [
                tuple(chunk.decode() for chunk in record.split(b"\0") if chunk)
                for record in argv_log.read_bytes().split(b"\0\0")
                if record
            ]

            mkv_commands = [argv for argv in invocations if argv and argv[-1].endswith(".mkv")]
            self.assertEqual(len(mkv_commands), 18, f"expected 18 mpeg4 MKV commands, got {len(mkv_commands)}")

            mpeg4_commands = [argv for argv in mkv_commands if "-c:v" in argv and argv[argv.index("-c:v") + 1] == "mpeg4"]
            self.assertEqual(len(mpeg4_commands), 18, f"expected 18 mpeg4 MKV commands, got {len(mpeg4_commands)}")
            for argv in mpeg4_commands:
                cpuflags_pairs = sum(1 for index, argument in enumerate(argv) if argument == "-cpuflags" and index + 1 < len(argv) and argv[index + 1] == "0")
                self.assertEqual(cpuflags_pairs, 1, f"mpeg4 command has {cpuflags_pairs} -cpuflags 0 pairs, expected 1: {argv}")
                threads_pairs = sum(1 for index, argument in enumerate(argv) if argument == "-threads" and index + 1 < len(argv) and argv[index + 1] == "1")
                self.assertEqual(threads_pairs, 1, f"mpeg4 command has {threads_pairs} -threads 1 pairs, expected 1: {argv}")

            libx265_commands = [argv for argv in invocations if "-c:v" in argv and argv[argv.index("-c:v") + 1] == "libx265"]
            self.assertEqual(len(libx265_commands), 0, f"expected zero libx265 commands, got {len(libx265_commands)}")

    def test_hlg_artifact_digest_agreement(self):
        """The committed artifact, the pinned digest in gen.sh, and the manifest entry all agree."""
        artifact_digest = hashlib.sha256(HLG_ARTIFACT.read_bytes()).hexdigest()

        gen_sh = GENERATOR.read_text()
        marker = "HLG_ARTIFACT_SHA256='"
        start = gen_sh.index(marker) + len(marker)
        end = gen_sh.index("'", start)
        pinned_digest = gen_sh[start:end]

        manifest = SOURCE_MANIFEST.read_text()
        manifest_digest = None
        for line in manifest.splitlines():
            if line.endswith("movies/HLG Sample (2024)/HLG Sample (2024).mkv"):
                manifest_digest = line.split("  ")[0]
                break
        self.assertIsNotNone(manifest_digest, "HLG MKV entry not found in source-manifest.sha256")

        self.assertEqual(artifact_digest, pinned_digest, "artifact sha256 does not match the pinned digest in gen.sh")
        self.assertEqual(artifact_digest, manifest_digest, "artifact sha256 does not match the source-manifest.sha256 entry")

    def test_missing_hlg_artifact_fails_without_writing_target(self):
        """A missing attested artifact stops generation before the target file is written."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-fixture-missing-hlg-") as directory:
            temporary_root = Path(directory)
            generator, accepted_output = self.copy_generator_tree(temporary_root)
            tools = temporary_root / "tools"
            ffmpeg = self.write_fixture_ffmpeg(tools)
            output = temporary_root / "fixture-out"
            artifact = (temporary_root / "tools" / "goldenrec" / "fixtures" / "attested" / "hlg-sample.mkv").resolve()

            accepted_output.mkdir(parents=True)
            sentinel = accepted_output / "sentinel"
            sentinel.write_bytes(b"accepted output must survive a missing artifact\n")
            before = self.output_snapshot(accepted_output)

            artifact.unlink()

            result = self.run_copied_generator(
                generator,
                output,
                FFMPEG=str(ffmpeg),
            )

            self.assertNotEqual(result.returncode, 0, result.stdout)
            expected_error = f"goldenrec fixtures: attested HLG artifact missing at {artifact}; see tools/goldenrec/REGEN.md"
            self.assertIn(expected_error, result.stderr.splitlines())
            target = output / "movies" / "HLG Sample (2024)" / "HLG Sample (2024).mkv"
            self.assertFalse(target.exists(), "missing-artifact failure wrote the target MKV")
            after = self.output_snapshot(accepted_output)
            self.assertEqual(after, before, "missing-artifact failure changed accepted output")

    def test_corrupt_hlg_artifact_fails_without_writing_target(self):
        """A corrupt attested artifact with a wrong digest stops generation before the target file is written."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-fixture-corrupt-hlg-") as directory:
            temporary_root = Path(directory)
            generator, accepted_output = self.copy_generator_tree(temporary_root)
            tools = temporary_root / "tools"
            ffmpeg = self.write_fixture_ffmpeg(tools)
            output = temporary_root / "fixture-out"
            artifact = temporary_root / "tools" / "goldenrec" / "fixtures" / "attested" / "hlg-sample.mkv"

            accepted_output.mkdir(parents=True)
            sentinel = accepted_output / "sentinel"
            sentinel.write_bytes(b"accepted output must survive a corrupt artifact\n")
            before = self.output_snapshot(accepted_output)

            artifact.write_bytes(b"corrupt artifact bytes")

            result = self.run_copied_generator(
                generator,
                output,
                FFMPEG=str(ffmpeg),
            )

            self.assertNotEqual(result.returncode, 0, result.stdout)
            expected_error = "goldenrec fixtures: attested HLG artifact does not match pinned digest; see tools/goldenrec/REGEN.md"
            self.assertIn(expected_error, result.stderr.splitlines())
            target = output / "movies" / "HLG Sample (2024)" / "HLG Sample (2024).mkv"
            self.assertFalse(target.exists(), "corrupt-artifact failure wrote the target MKV")
            after = self.output_snapshot(accepted_output)
            self.assertEqual(after, before, "corrupt-artifact failure changed accepted output")


class FixtureWrapperTests(unittest.TestCase):
    @staticmethod
    def write_fake_docker(path):
        path.write_text(
            "#!/bin/sh\n"
            "set -eu\n"
            "printf '%s\\n' \"$*\" >> \"$VERIFY_ENV_TEST_DOCKER_LOG\"\n"
            "[ \"${1-}\" = compose ] || exit 96\n"
            "shift\n"
            "case \" $* \" in\n"
            "  *' version '*) exit 0 ;;\n"
            "esac\n"
            "while [ \"$#\" -gt 0 ] && [ \"$1\" != run ] && [ \"$1\" != build ] && [ \"$1\" != down ]; do shift; done\n"
            "case ${1-} in\n"
            "  build|down) exit 0 ;;\n"
            "  run) shift ;;\n"
            "  *) exit 95 ;;\n"
            "esac\n"
            "case \" $* \" in\n"
            "  *' --entrypoint '*)\n"
            "    # Inspect the production mount-probe command, do not self-fulfill it.\n"
            "    entrypoint_value=\n"
            "    probe_script=\n"
            "    while [ \"$#\" -gt 0 ]; do\n"
            "      case \"$1\" in\n"
            "        --entrypoint) entrypoint_value=${2:-}; shift 2 ;;\n"
            "        -c) probe_script=${2:-}; shift 2 ;;\n"
            "        *) shift ;;\n"
            "      esac\n"
            "    done\n"
            "    [ \"$entrypoint_value\" = /bin/sh ] || { echo \"fake-docker: mount probe must use --entrypoint /bin/sh\" >&2; exit 92; }\n"
            "    [ -n \"$probe_script\" ] || { echo \"fake-docker: mount probe has no -c script\" >&2; exit 92; }\n"
            "    # Validate that the probe script checks both required paths and prints the exact error.\n"
            "    probe_error='generate-fixtures.sh: fixture container cannot read the Jellyport checkout at /work; check the Docker bind-mount or Docker Desktop file sharing'\n"
            "    probe_path_a='tools/verify-env/fixture-generator-entrypoint.sh'\n"
            "    probe_path_b='tools/goldenrec/fixtures/source-manifest.sha256'\n"
            "    case \"$probe_script\" in *\"$probe_path_a\"*) ;; *) echo \"fake-docker: probe script does not check $probe_path_a\" >&2; exit 92 ;; esac\n"
            "    case \"$probe_script\" in *\"$probe_path_b\"*) ;; *) echo \"fake-docker: probe script does not check $probe_path_b\" >&2; exit 92 ;; esac\n"
            "    case \"$probe_script\" in *\"$probe_error\"*) ;; *) echo \"fake-docker: probe script does not contain the required error\" >&2; exit 92 ;; esac\n"
            "    # Execute the production probe script so it produces the error, not this fake.\n"
            "    # Map /work to the real repo (readable) or an empty temp dir (unreadable).\n"
            "    work_substitute=$VERIFY_ENV_REPO\n"
            "    if [ -n \"${VERIFY_ENV_TEST_MOUNT_UNREADABLE:-}\" ]; then\n"
            "      work_substitute=$(mktemp -d)\n"
            "    fi\n"
            "    adjusted_script=$(printf '%s' \"$probe_script\" | sed \"s|/work/|$work_substitute/|g\")\n"
            "    /bin/sh -c \"$adjusted_script\"\n"
            "    probe_status=$?\n"
            "    if [ -n \"${VERIFY_ENV_TEST_MOUNT_UNREADABLE:-}\" ]; then rm -rf \"$work_substitute\"; fi\n"
            "    exit $probe_status\n"
            "    ;;\n"
            "esac\n"
            "while [ \"$#\" -gt 0 ] && [ \"$1\" != fixture-generator ]; do shift; done\n"
            "[ \"${1-}\" = fixture-generator ] || exit 93\n"
            "shift\n"
            "exec python3 \"$VERIFY_ENV_TEST_PUBLISHER_RUNNER\" \\\n"
            "  \"$VERIFY_ENV_REPO\" \"$VERIFY_ENV_TEST_FFMPEG\" \"$@\"\n"
        )
        path.chmod(0o700)

    @staticmethod
    def write_fake_publisher_runner(path):
        path.write_text(
            "import importlib.util\n"
            "import pathlib\n"
            "import sys\n"
            "\n"
            "repository = pathlib.Path(sys.argv[1]).resolve()\n"
            "ffmpeg = pathlib.Path(sys.argv[2]).resolve()\n"
            "publisher_path = repository / 'tools' / 'verify-env' / 'publish-fixtures.py'\n"
            "spec = importlib.util.spec_from_file_location('fixture_publisher_test', publisher_path)\n"
            "publisher = importlib.util.module_from_spec(spec)\n"
            "spec.loader.exec_module(publisher)\n"
            "publisher.main(\n"
            "    [*sys.argv[3:], '--repository', str(repository)],\n"
            "    verify_environment=lambda: None,\n"
            "    canonical_ffmpeg=ffmpeg,\n"
            ")\n"
        )

    @staticmethod
    def write_failing_ffmpeg(path):
        path.write_text(
            "#!/bin/sh\n"
            "for argument do\n"
            "  if [ \"$argument\" = -version ]; then printf '%s\\n' 'ffmpeg wrapper-test seam'; exit 0; fi\n"
            "done\n"
            "printf '%s\\n' 'intentional wrapper generation failure' >&2\n"
            "exit 97\n"
        )
        path.chmod(0o700)

    @staticmethod
    def copy_wrapper_tree(destination):
        fixtures = destination / "tools" / "goldenrec" / "fixtures"
        verify_env = destination / "tools" / "verify-env"
        fixtures.mkdir(parents=True)
        verify_env.mkdir(parents=True)
        shutil.copy2(GENERATOR, fixtures / "gen.sh")
        shutil.copy2(FIXTURE_LOCK, fixtures / ".fixture-publish.lock")
        shutil.copy2(SOURCE_MANIFEST, fixtures / "source-manifest.sha256")
        attested = fixtures / "attested"
        attested.mkdir()
        shutil.copy2(HLG_ARTIFACT, attested / "hlg-sample.mkv")
        for source in (FIXTURE_WRAPPER, FIXTURE_COMPOSE, FIXTURE_ENTRYPOINT, FIXTURE_PUBLISHER):
            shutil.copy2(source, verify_env / source.name)

    @staticmethod
    def output_snapshot(output):
        return {
            path.relative_to(output).as_posix(): path.read_bytes()
            for path in sorted(output.rglob("*"))
            if path.is_file()
        }

    @staticmethod
    def project_names(log):
        names = set()
        lines = log.read_text().splitlines()
        for line in lines:
            arguments = line.split()
            for index, argument in enumerate(arguments):
                if argument == "--project-name":
                    names.add(arguments[index + 1])
        return names

    def make_wrapper_fixture(self, temporary_root):
        template = temporary_root / "template"
        self.copy_wrapper_tree(template)
        tools = temporary_root / "tools"
        tools.mkdir()
        fake_ffmpeg = FixtureDeterminismTests.write_fixture_ffmpeg(tools)
        bootstrap = temporary_root / "bootstrap"
        environment = os.environ.copy()
        environment["GOLDENREC_FIXTURES_DIR"] = str(bootstrap)
        environment["FFMPEG"] = str(fake_ffmpeg)
        result = subprocess.run(
            [str(template / "tools" / "goldenrec" / "fixtures" / "gen.sh")],
            cwd=template,
            capture_output=True,
            text=True,
            env=environment,
        )
        self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
        shutil.copy2(
            bootstrap / "MANIFEST.sha256",
            template / "tools" / "goldenrec" / "fixtures" / "source-manifest.sha256",
        )
        publisher_runner = temporary_root / "publisher-runner.py"
        self.write_fake_publisher_runner(publisher_runner)
        return template, fake_ffmpeg, publisher_runner

    def run_wrapper(self, repository, docker, ffmpeg, publisher_runner, log, *arguments, environment_overrides=None):
        environment = os.environ.copy()
        environment.update(
            {
                "DOCKER": str(docker),
                "VERIFY_ENV_REPO": repository.name,
                "VERIFY_ENV_TEST_DOCKER_LOG": str(log),
                "VERIFY_ENV_TEST_FFMPEG": str(ffmpeg),
                "VERIFY_ENV_TEST_PUBLISHER_RUNNER": str(publisher_runner),
            }
        )
        if environment_overrides:
            environment.update(environment_overrides)
        return subprocess.run(
            [str(repository / "tools" / "verify-env" / "generate-fixtures.sh"), *arguments],
            cwd=repository.parent,
            capture_output=True,
            text=True,
            env=environment,
        )

    def run_entrypoint(self, temporary_root, uname_body, package_version):
        tools = temporary_root / "entrypoint-tools"
        tools.mkdir()
        uname = tools / "uname"
        uname.write_text(f"#!/bin/sh\n{uname_body}\n")
        uname.chmod(0o700)
        dpkg_query = tools / "dpkg-query"
        dpkg_query.write_text(f"#!/bin/sh\nprintf '%s\\n' '{package_version}'\n")
        dpkg_query.chmod(0o700)
        environment = os.environ.copy()
        environment.update(
            {
                "GOLDENREC_FIXTURES_DIR": "/work/tools/goldenrec/fixtures/.fixture-generation.test/out",
                "PATH": f"{tools}{os.pathsep}{os.environ['PATH']}",
            }
        )
        return subprocess.run(
            [str(FIXTURE_ENTRYPOINT)],
            cwd=PROJECT_ROOT,
            capture_output=True,
            text=True,
            env=environment,
        )

    def test_entrypoint_rejects_wrong_platform_and_package(self):
        """The image entrypoint enforces platform and ffmpeg before generation."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-entrypoint-") as directory:
            temporary_root = Path(directory)
            wrong_platform = self.run_entrypoint(
                temporary_root,
                "case ${1-} in -s) echo Darwin ;; -m) echo arm64 ;; esac",
                "7:5.1.9-0+deb12u1",
            )
            self.assertNotEqual(wrong_platform.returncode, 0)
            self.assertIn("Linux/x86_64", wrong_platform.stderr)

        with tempfile.TemporaryDirectory(prefix="goldenrec-entrypoint-") as directory:
            temporary_root = Path(directory)
            wrong_package = self.run_entrypoint(
                temporary_root,
                "case ${1-} in -s) echo Linux ;; -m) echo x86_64 ;; esac",
                "7:9.9.9-review1",
            )
            self.assertNotEqual(wrong_package.returncode, 0)
            self.assertIn("want 7:5.1.9-0+deb12u1", wrong_package.stderr)

    def test_wrapper_generates_identical_isolated_projections(self):
        """The production wrapper is deterministic across isolated checkouts."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-wrapper-copies-") as directory:
            temporary_root = Path(directory)
            template, fake_ffmpeg, publisher_runner = self.make_wrapper_fixture(temporary_root)
            first = temporary_root / "first"
            second = temporary_root / "second"
            shutil.copytree(template, first)
            shutil.copytree(template, second)
            fake_docker = temporary_root / "docker"
            self.write_fake_docker(fake_docker)
            first_log = temporary_root / "first-docker.log"
            second_log = temporary_root / "second-docker.log"

            first_result = self.run_wrapper(
                first, fake_docker, fake_ffmpeg, publisher_runner, first_log
            )
            second_result = self.run_wrapper(
                second, fake_docker, fake_ffmpeg, publisher_runner, second_log
            )

            self.assertEqual(first_result.returncode, 0, first_result.stderr or first_result.stdout)
            self.assertEqual(second_result.returncode, 0, second_result.stderr or second_result.stdout)
            first_output = first / "tools" / "goldenrec" / "fixtures" / "out"
            second_output = second / "tools" / "goldenrec" / "fixtures" / "out"
            self.assertEqual(self.output_snapshot(first_output), self.output_snapshot(second_output))
            self.assertTrue(self.project_names(first_log).isdisjoint(self.project_names(second_log)))

    def test_wrapper_rejections_leave_accepted_output_unchanged(self):
        """Generation and manifest failures cannot replace accepted output."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-wrapper-reject-") as directory:
            temporary_root = Path(directory)
            template, fake_ffmpeg, publisher_runner = self.make_wrapper_fixture(temporary_root)
            repository = temporary_root / "repository"
            shutil.copytree(template, repository)
            fake_docker = temporary_root / "docker"
            self.write_fake_docker(fake_docker)
            failing_ffmpeg = temporary_root / "failing-ffmpeg"
            self.write_failing_ffmpeg(failing_ffmpeg)
            log = temporary_root / "docker.log"

            accepted = self.run_wrapper(
                repository, fake_docker, fake_ffmpeg, publisher_runner, log
            )
            self.assertEqual(accepted.returncode, 0, accepted.stderr or accepted.stdout)
            output = repository / "tools" / "goldenrec" / "fixtures" / "out"
            sentinel = output / "review-sentinel"
            sentinel.write_bytes(b"accepted output must survive\n")
            before = self.output_snapshot(output)

            failed_generation = self.run_wrapper(
                repository, fake_docker, failing_ffmpeg, publisher_runner, log
            )
            self.assertEqual(failed_generation.returncode, 97, failed_generation.stderr or failed_generation.stdout)
            self.assertEqual(self.output_snapshot(output), before)

            source_manifest = repository / "tools" / "goldenrec" / "fixtures" / "source-manifest.sha256"
            lines = source_manifest.read_text().splitlines()
            lines[1] = "0" * 64 + lines[1][64:]
            source_manifest.write_text("\n".join(lines) + "\n")
            rejected_candidate = self.run_wrapper(
                repository, fake_docker, fake_ffmpeg, publisher_runner, log
            )
            self.assertNotEqual(rejected_candidate.returncode, 0, rejected_candidate.stdout)
            self.assertIn("candidate differs", rejected_candidate.stderr)
            self.assertEqual(self.output_snapshot(output), before)

            attested = self.run_wrapper(
                repository,
                fake_docker,
                fake_ffmpeg,
                publisher_runner,
                log,
                "--attest",
            )
            self.assertEqual(attested.returncode, 0, attested.stderr or attested.stdout)
            self.assertEqual(
                source_manifest.read_bytes(),
                (output / "MANIFEST.sha256").read_bytes(),
            )
            self.assertFalse(sentinel.exists())
            self.assertFalse(
                list(source_manifest.parent.glob(".source-manifest.sha256.*.tmp")),
                "atomic manifest update left a temporary file",
            )

    def test_mount_probe_failure_leaves_accepted_output_unchanged(self):
        """An unreadable bind mount fails before publication and preserves accepted output."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-wrapper-mount-probe-") as directory:
            temporary_root = Path(directory)
            template, fake_ffmpeg, publisher_runner = self.make_wrapper_fixture(temporary_root)
            repository = temporary_root / "repository"
            shutil.copytree(template, repository)
            fake_docker = temporary_root / "docker"
            self.write_fake_docker(fake_docker)
            log = temporary_root / "docker.log"

            accepted = self.run_wrapper(
                repository, fake_docker, fake_ffmpeg, publisher_runner, log
            )
            self.assertEqual(accepted.returncode, 0, accepted.stderr or accepted.stdout)
            output = repository / "tools" / "goldenrec" / "fixtures" / "out"
            sentinel = output / "mount-sentinel"
            sentinel.write_bytes(b"accepted output must survive the mount probe\n")
            before = self.output_snapshot(output)
            source_manifest = repository / "tools" / "goldenrec" / "fixtures" / "source-manifest.sha256"
            source_before = source_manifest.read_bytes()

            failed_log = temporary_root / "failed-docker.log"
            failed = self.run_wrapper(
                repository,
                fake_docker,
                fake_ffmpeg,
                publisher_runner,
                failed_log,
                environment_overrides={"VERIFY_ENV_TEST_MOUNT_UNREADABLE": "1"},
            )

            self.assertNotEqual(failed.returncode, 0, failed.stdout)
            self.assertIn("fixture container cannot read the Jellyport checkout at /work", failed.stderr)
            self.assertIn("check the Docker bind-mount or Docker Desktop file sharing", failed.stderr)
            self.assertEqual(self.output_snapshot(output), before)
            self.assertEqual(source_manifest.read_bytes(), source_before)
            self._assert_no_publisher_invocation(failed_log)

        with tempfile.TemporaryDirectory(prefix="goldenrec-wrapper-mount-probe-bad-") as directory:
            temporary_root = Path(directory)
            template, fake_ffmpeg, publisher_runner = self.make_wrapper_fixture(temporary_root)

            with self.subTest(mutation="wrong_probe_path"):
                self._assert_fake_docker_rejects_bad_probe(
                    temporary_root, template, fake_ffmpeg, publisher_runner,
                    old="tools/goldenrec/fixtures/source-manifest.sha256",
                    new="tools/goldenrec/fixtures/wrong-manifest.sha256",
                )

            with self.subTest(mutation="wrong_error"):
                self._assert_fake_docker_rejects_bad_probe(
                    temporary_root, template, fake_ffmpeg, publisher_runner,
                    old="check the Docker bind-mount",
                    new="check the wrong bind-mount",
                )

    @staticmethod
    def _assert_no_publisher_invocation(log):
        """The mount-probe failure must not reach the publisher run."""
        log_text = log.read_text()
        if "--candidate-name" in log_text:
            raise AssertionError(
                "publisher was invoked after mount-probe failure"
            )

    def _assert_fake_docker_rejects_bad_probe(
        self, temporary_root, template, fake_ffmpeg, publisher_runner, old, new
    ):
        """The fake docker must reject a production probe that has the wrong path or error."""
        label = hashlib.sha256(new.encode()).hexdigest()[:8]
        repository = temporary_root / f"repo-{label}"
        shutil.copytree(template, repository)
        wrapper = repository / "tools" / "verify-env" / "generate-fixtures.sh"
        wrapper.write_text(wrapper.read_text().replace(old, new))
        fake_docker = temporary_root / "docker"
        self.write_fake_docker(fake_docker)
        log = temporary_root / f"bad-probe-{label}.log"
        result = self.run_wrapper(
            repository,
            fake_docker,
            fake_ffmpeg,
            publisher_runner,
            log,
            environment_overrides={"VERIFY_ENV_TEST_MOUNT_UNREADABLE": "1"},
        )
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("fake-docker", result.stderr)

    def test_direct_host_candidate_cannot_reach_attesting_publisher(self):
        """A direct publisher call cannot promote a caller-generated candidate."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-publisher-bypass-") as directory:
            temporary_root = Path(directory)
            template, fake_ffmpeg, publisher_runner = self.make_wrapper_fixture(temporary_root)
            repository = temporary_root / "repository"
            shutil.copytree(template, repository)
            fake_docker = temporary_root / "docker"
            self.write_fake_docker(fake_docker)
            log = temporary_root / "docker.log"

            accepted = self.run_wrapper(
                repository,
                fake_docker,
                fake_ffmpeg,
                publisher_runner,
                log,
                "--attest",
            )
            self.assertEqual(accepted.returncode, 0, accepted.stderr or accepted.stdout)

            fixtures = repository / "tools" / "goldenrec" / "fixtures"
            output = fixtures / "out"
            source_manifest = fixtures / "source-manifest.sha256"
            output_before = self.output_snapshot(output)
            source_before = source_manifest.read_bytes()

            host_tools = temporary_root / "host-tools"
            host_tools.mkdir()
            host_ffmpeg = FixtureDeterminismTests.write_fixture_ffmpeg(
                host_tools,
                version="ffmpeg host-forge seam",
            )
            host_run = fixtures / ".fixture-generation.HOST"
            host_run.mkdir(mode=0o700)
            host_run.chmod(0o700)
            host_candidate = host_run / "out"
            generated = FixtureDeterminismTests.run_generator(
                host_candidate,
                FFMPEG=str(host_ffmpeg),
            )
            self.assertEqual(generated.returncode, 0, generated.stderr or generated.stdout)
            self.assertNotEqual(
                (host_candidate / "MANIFEST.sha256").read_bytes(),
                source_before,
            )

            environment = os.environ.copy()
            environment.update(
                {
                    "FFMPEG": str(host_ffmpeg),
                    "GOLDENREC_ATTEST_SOURCE_MANIFEST": "1",
                    "GOLDENREC_CANONICAL_GENERATOR": "verify-env-linux-amd64-v1",
                    "GOLDENREC_FIXTURES_DIR": str(host_candidate),
                }
            )
            bypass = subprocess.run(
                [
                    "python3",
                    str(repository / "tools" / "verify-env" / "publish-fixtures.py"),
                    "--repository",
                    str(repository),
                    "--candidate-name",
                    host_run.name,
                    "--attest",
                ],
                cwd=repository,
                capture_output=True,
                text=True,
                env=environment,
            )

            self.assertNotEqual(bypass.returncode, 0, bypass.stdout)
            self.assertIn("canonical", bypass.stderr)
            self.assertEqual(self.output_snapshot(output), output_before)
            self.assertEqual(source_manifest.read_bytes(), source_before)


class FixturePublisherDiagnosticsTests(unittest.TestCase):
    """Focused regression cases for fixture publisher diagnostics and rollback."""

    @staticmethod
    def load_publisher():
        spec = importlib.util.spec_from_file_location("fixture_publisher_test", FIXTURE_PUBLISHER)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    @staticmethod
    def stable_marker_bytes(uid):
        return (
            json.dumps(
                {"format": MARKER_FORMAT, "root": STABLE_MARKER_ROOT, "uid": uid},
                separators=(",", ":"),
                sort_keys=True,
            )
            + "\n"
        ).encode()

    @staticmethod
    def copy_publisher_tree(destination):
        fixtures = destination / "tools" / "goldenrec" / "fixtures"
        verify_env = destination / "tools" / "verify-env"
        fixtures.mkdir(parents=True)
        verify_env.mkdir(parents=True)
        shutil.copy2(GENERATOR, fixtures / "gen.sh")
        shutil.copy2(FIXTURE_LOCK, fixtures / ".fixture-publish.lock")
        shutil.copy2(SOURCE_MANIFEST, fixtures / "source-manifest.sha256")
        attested = fixtures / "attested"
        attested.mkdir()
        shutil.copy2(HLG_ARTIFACT, attested / "hlg-sample.mkv")
        shutil.copy2(FIXTURE_PUBLISHER, verify_env / "publish-fixtures.py")
        return fixtures

    def make_accepted_output(self, output, uid, sentinel_name="original-content.bin"):
        output.mkdir(mode=0o700)
        output.chmod(0o700)
        marker = output / MARKER_NAME
        marker.write_bytes(self.stable_marker_bytes(uid))
        marker.chmod(0o600)
        sentinel = output / sentinel_name
        sentinel.write_bytes(b"accepted output must survive rollback\n")
        return self.output_snapshot(output)

    @staticmethod
    def output_snapshot(output):
        return {
            path.relative_to(output).as_posix(): path.read_bytes()
            for path in sorted(output.rglob("*"))
            if path.is_file()
        }

    def test_unsafe_mode_output_rejects_before_generation(self):
        """An existing out with mode 0755 fails before the generator starts."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-publisher-diag-") as directory:
            temporary_root = Path(directory)
            repository = temporary_root / "repository"
            fixtures = self.copy_publisher_tree(repository)
            output = fixtures / "out"
            uid = os.getuid()

            output.mkdir(mode=0o755)
            output.chmod(0o755)
            marker = output / MARKER_NAME
            marker.write_bytes(self.stable_marker_bytes(uid))
            marker.chmod(0o600)

            tools = temporary_root / "tools"
            tools.mkdir()
            ffmpeg = FixtureDeterminismTests.write_fixture_ffmpeg(tools)

            run_root = fixtures / ".fixture-generation.diag"
            run_root.mkdir(mode=0o700)
            run_root.chmod(0o700)

            publisher = self.load_publisher()
            with self.assertRaises(SystemExit) as caught:
                publisher.main(
                    ["--repository", str(repository), "--candidate-name", run_root.name],
                    verify_environment=lambda: None,
                    canonical_ffmpeg=ffmpeg,
                )
            message = str(caught.exception)
            self.assertIn("accepted output", message)
            self.assertIn(str(output), message)
            self.assertIn("mode 0755", message)
            self.assertIn("want 0700", message)
            self.assertIn(f"owner {uid}", message)
            self.assertIn(f"want {uid}", message)
            self.assertFalse(
                (run_root / "out").exists(),
                "generator started before the unsafe output was rejected",
            )

    def test_no_prior_output_rollback_removes_installed_candidate(self):
        """A post-installation failure with no prior output leaves no out and no run root."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-publisher-rollback-") as directory:
            temporary_root = Path(directory)
            repository = temporary_root / "repository"
            fixtures = self.copy_publisher_tree(repository)
            output = fixtures / "out"
            source_manifest = fixtures / "source-manifest.sha256"
            uid = os.getuid()

            tools = temporary_root / "tools"
            tools.mkdir()
            ffmpeg = FixtureDeterminismTests.write_fixture_ffmpeg(tools)

            run_root = fixtures / ".fixture-generation.rollback"
            run_root.mkdir(mode=0o700)
            run_root.chmod(0o700)

            source_manifest.unlink()
            source_manifest.mkdir()

            publisher = self.load_publisher()
            with self.assertRaises((SystemExit, OSError)) as caught:
                publisher.main(
                    [
                        "--repository",
                        str(repository),
                        "--candidate-name",
                        run_root.name,
                        "--attest",
                    ],
                    verify_environment=lambda: None,
                    canonical_ffmpeg=ffmpeg,
                )

            self.assertFalse(output.exists(), "installed candidate was not removed after rollback")
            self.assertFalse(
                run_root.exists(),
                "private generation directory was not removed after rollback",
            )

    def test_prior_output_rollback_preserves_accepted_output(self):
        """A post-installation failure with a prior accepted output preserves the original marker and contents."""
        with tempfile.TemporaryDirectory(prefix="goldenrec-publisher-prior-") as directory:
            temporary_root = Path(directory)
            repository = temporary_root / "repository"
            fixtures = self.copy_publisher_tree(repository)
            output = fixtures / "out"
            source_manifest = fixtures / "source-manifest.sha256"
            uid = os.getuid()

            before = self.make_accepted_output(output, uid)

            tools = temporary_root / "tools"
            tools.mkdir()
            ffmpeg = FixtureDeterminismTests.write_fixture_ffmpeg(tools)

            run_root = fixtures / ".fixture-generation.prior"
            run_root.mkdir(mode=0o700)
            run_root.chmod(0o700)

            source_manifest.unlink()
            source_manifest.mkdir()

            publisher = self.load_publisher()
            with self.assertRaises((SystemExit, OSError)) as caught:
                publisher.main(
                    [
                        "--repository",
                        str(repository),
                        "--candidate-name",
                        run_root.name,
                        "--attest",
                    ],
                    verify_environment=lambda: None,
                    canonical_ffmpeg=ffmpeg,
                )

            self.assertTrue(output.exists(), "accepted output was removed during rollback")
            self.assertTrue(output.is_dir(), "accepted output is not a directory")
            self.assertFalse(output.is_symlink(), "accepted output became a symlink")
            marker = output / MARKER_NAME
            self.assertEqual(marker.read_bytes(), self.stable_marker_bytes(uid))
            self.assertEqual(self.output_snapshot(output), before)
            self.assertFalse(
                run_root.exists(),
                "private generation directory was not removed after rollback",
            )


if __name__ == "__main__":
    unittest.main()
