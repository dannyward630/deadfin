import hashlib
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO = Path(__file__).resolve().parents[3]
ORACLE = REPO / "tools" / "compat" / "itemnormoracle"
PROJECT = ORACLE / "ItemNormOracle.csproj"
VECTORS = ORACLE / "vectors.json"
BUILD_OUTPUT = ORACLE / "bin" / "Debug" / "net10.0"
NORMALIZATION_ORACLE_ARTIFACT = REPO / "deadfin" / "internal" / "item" / "testdata" / "normalization-oracle.json"
DIACRITICS_ARTIFACT = REPO / "deadfin" / "internal" / "item" / "diacritics_gen.go"
UNICODE_ARTIFACT = REPO / "deadfin" / "internal" / "item" / "unicode16_gen.go"
UPSTREAM = REPO / "upstream" / "jellyfin"
EXPECTED_UPSTREAM_COMMIT = "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb"


def copy_pinned_upstream(destination: Path) -> None:
    completed = subprocess.run(
        ["git", "clone", "--quiet", "--no-hardlinks", str(UPSTREAM), str(destination)],
        cwd=REPO,
        text=True,
        capture_output=True,
        check=False,
    )
    if completed.returncode != 0:
        raise RuntimeError(f"could not create isolated upstream copy:\n{completed.stdout}{completed.stderr}")


def loaded_runtime_assembly(name: str) -> Path:
    runtime_config = json.loads((BUILD_OUTPUT / "ItemNormOracle.runtimeconfig.json").read_text(encoding="utf-8"))
    framework = next(
        framework
        for framework in runtime_config["runtimeOptions"]["frameworks"]
        if framework["name"] == "Microsoft.NETCore.App"
    )
    framework_name = framework["name"]
    required_version = tuple(int(component) for component in framework["version"].split("."))
    completed = subprocess.run(
        ["dotnet", "--list-runtimes"],
        cwd=REPO,
        text=True,
        capture_output=True,
        check=False,
    )
    if completed.returncode != 0:
        raise RuntimeError(f"dotnet --list-runtimes failed:\n{completed.stdout}{completed.stderr}")

    candidates: list[tuple[tuple[int, ...], Path]] = []
    for line in completed.stdout.splitlines():
        try:
            identity, root = line.rsplit(" [", maxsplit=1)
            runtime_name, version = identity.rsplit(" ", maxsplit=1)
            version_parts = tuple(int(component) for component in version.split("."))
        except ValueError:
            continue
        if runtime_name == framework_name and version_parts[:2] == required_version[:2] and version_parts >= required_version:
            candidates.append((version_parts, Path(root.removesuffix("]")) / version / name))

    if not candidates:
        raise RuntimeError(f"no installed {framework_name} runtime satisfies {framework['version']}")
    return max(candidates, key=lambda candidate: candidate[0])[1]




def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(64 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()

def build_oracle_dependencies() -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["dotnet", "build", "--no-restore", str(PROJECT)],
        cwd=REPO,
        text=True,
        capture_output=True,
        check=False,
    )


def build_oracle() -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["dotnet", "build", "--no-restore", "--no-incremental", "--no-dependencies", str(PROJECT)],
        cwd=REPO,
        text=True,
        capture_output=True,
        check=False,
    )

def build_oracle_with_source_revision(
    source_revision_id: str, before_common_props: Path
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [
            "dotnet",
            "build",
            "--no-restore",
            "--no-incremental",
            "-p:BuildProjectReferences=false",
            str(PROJECT),
            "--configuration",
            "Debug",
            f"-p:CustomBeforeMicrosoftCommonProps={before_common_props}",
            f"-p:SourceRevisionId={source_revision_id}",
        ],
        cwd=REPO,
        text=True,
        capture_output=True,
        check=False,
    )


def source_revision_informational_version_enabled_after(
    before_common_props: Path,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [
            "dotnet",
            "msbuild",
            str(PROJECT),
            f"-p:CustomBeforeMicrosoftCommonProps={before_common_props}",
            "-getProperty:IncludeSourceRevisionInInformationalVersion",
        ],
        cwd=REPO,
        text=True,
        capture_output=True,
        check=False,
    )

def run_oracle(*arguments: str, environment: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["dotnet", "run", "--no-build", "--project", str(PROJECT), "--", *arguments],
        cwd=REPO,
        env=environment,
        text=True,
        capture_output=True,
        check=False,
    )


def run_copied_oracle(
    assembly_directory: Path, *arguments: str, environment: dict[str, str] | None = None
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["dotnet", str(assembly_directory / "ItemNormOracle.dll"), *arguments],
        cwd=REPO,
        env=environment,
        text=True,
        capture_output=True,
        check=False,
    )


class ItemNormOracleProvenanceTests(unittest.TestCase):
    maxDiff = None

    @classmethod
    def setUpClass(cls):
        super().setUpClass()
        dependencies = build_oracle_dependencies()
        if dependencies.returncode != 0:
            raise RuntimeError(f"dotnet build --no-restore failed:\n{dependencies.stdout}{dependencies.stderr}")

        completed = build_oracle()
        if completed.returncode != 0:
            raise RuntimeError(
                f"dotnet build --no-restore --no-incremental --no-dependencies failed:\n{completed.stdout}{completed.stderr}"
            )

    def test_project_explicitly_excludes_source_revision_from_informational_version(self):
        """The project overrides inherited revision embedding rather than depending on an SDK default."""
        with tempfile.TemporaryDirectory(dir=REPO) as directory:
            before_common_props = Path(directory) / "before-common.props"
            before_common_props.write_text(
                "<Project><PropertyGroup>"
                "<IncludeSourceRevisionInInformationalVersion>true</IncludeSourceRevisionInInformationalVersion>"
                "</PropertyGroup></Project>",
                encoding="utf-8",
            )
            completed = source_revision_informational_version_enabled_after(before_common_props)

        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(
            completed.stdout.strip(),
            "false",
            "ItemNormOracle.csproj must explicitly override injected source revision embedding",
        )

    def test_executing_assembly_is_identical_across_source_revisions(self):
        """The oracle's self-hash is stable when the enclosing repository commit changes."""
        revisions = (
            "1111111111111111111111111111111111111111",
            "2222222222222222222222222222222222222222",
        )
        with tempfile.TemporaryDirectory(dir=REPO) as directory:
            build_root = Path(directory)
            assemblies = []
            for index, source_revision_id in enumerate(revisions):
                build_directory = build_root / str(index)
                build_directory.mkdir()
                output = build_directory / "bin"
                before_common_props = build_directory / "before-common.props"
                before_common_props.write_text(
                    "<Project><PropertyGroup Condition=\"'$(MSBuildProjectName)' == 'ItemNormOracle'\">"
                    f"<OutputPath>{output}{os.sep}</OutputPath>"
                    "</PropertyGroup></Project>",
                    encoding="utf-8",
                )
                completed = build_oracle_with_source_revision(source_revision_id, before_common_props)
                self.assertEqual(
                    completed.returncode,
                    0,
                    f"build with SourceRevisionId={source_revision_id} failed:\n{completed.stdout}{completed.stderr}",
                )
                assembly = output / "net10.0" / "ItemNormOracle.dll"
                self.assertTrue(assembly.is_file(), f"missing rebuilt oracle assembly for {source_revision_id}")
                assemblies.append(assembly.read_bytes())

        first_hash = hashlib.sha256(assemblies[0]).hexdigest()
        second_hash = hashlib.sha256(assemblies[1]).hexdigest()
        self.assertTrue(
            assemblies[0] == assemblies[1] and first_hash == second_hash,
            "SourceRevisionId changed ItemNormOracle.dll: "
            f"bytes_equal={assemblies[0] == assemblies[1]}, "
            f"SHA-256({revisions[0]})={first_hash}, SHA-256({revisions[1]})={second_hash}",
        )

    def test_generated_oracle_hashes_live_sources_and_executing_assemblies(self):
        """Oracle provenance binds source inputs and the executable that produced the document."""
        with tempfile.TemporaryDirectory(dir=REPO) as directory:
            output = Path(directory) / "normalization-oracle.json"
            completed = run_oracle("--vectors", str(VECTORS), "--output", str(output))
            self.assertEqual(completed.returncode, 0, completed.stderr)
            document = json.loads(output.read_text(encoding="utf-8"))
            self.assert_lf_terminated(output)
            self.assert_signed_document(output, document)

        live_inputs = {
            "vectors": VECTORS,
            "tool": ORACLE / "Program.cs",
            "project": PROJECT,
            "oracle-assembly": BUILD_OUTPUT / "ItemNormOracle.dll",
            "loaded-diacritics-4.1.8": BUILD_OUTPUT / "Diacritics.dll",
            "loaded-icu4n-60.1.0-alpha.356": BUILD_OUTPUT / "ICU4N.dll",
            "loaded-icu4n-transliterator-60.1.0-alpha.356": BUILD_OUTPUT / "ICU4N.Transliterator.dll",
            "loaded-jellyfin-extensions": BUILD_OUTPUT / "Jellyfin.Extensions.dll",
            "loaded-mediabrowser-controller": BUILD_OUTPUT / "MediaBrowser.Controller.dll",
            "loaded-mediabrowser-model": BUILD_OUTPUT / "MediaBrowser.Model.dll",
            "loaded-system-private-corelib": loaded_runtime_assembly("System.Private.CoreLib.dll"),
            "loaded-system-text-regular-expressions": loaded_runtime_assembly("System.Text.RegularExpressions.dll"),
            "loaded-system-text-json": loaded_runtime_assembly("System.Text.Json.dll"),
            "loaded-system-text-encodings-web": loaded_runtime_assembly("System.Text.Encodings.Web.dll"),
            "loaded-system-io-compression": loaded_runtime_assembly("System.IO.Compression.dll"),
            "loaded-system-security-cryptography": loaded_runtime_assembly("System.Security.Cryptography.dll"),
        }
        hashes = document["provenance"]["sourceHashes"]
        for name, path in live_inputs.items():
            with self.subTest(input=name):
                self.assertTrue(path.is_file(), f"oracle did not produce executable input {path}")
                self.assertEqual(hashes.get(name), sha256(path), name)

    def test_oracle_rejects_same_head_authoritative_source_drift_before_generation(self):
        """A dirty source checkout is not authoritative even when its HEAD remains pinned."""
        with tempfile.TemporaryDirectory(dir=REPO) as directory:
            temporary_root = Path(directory)
            copied_upstream = temporary_root / "jellyfin"
            copy_pinned_upstream(copied_upstream)

            head = subprocess.run(
                ["git", "rev-parse", "HEAD"],
                cwd=copied_upstream,
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(head.returncode, 0, head.stderr)
            self.assertEqual(head.stdout.strip(), EXPECTED_UPSTREAM_COMMIT)

            drifted_source = copied_upstream / "src" / "Jellyfin.Extensions" / "StringExtensions.cs"
            drifted_source.write_bytes(drifted_source.read_bytes() + b"\n// temporary authoritative-source drift\n")
            output = temporary_root / "normalization-oracle.json"
            completed = run_oracle(
                "--vectors",
                str(VECTORS),
                "--output",
                str(output),
                environment={**os.environ, "ITEMNORMORACLE_UPSTREAM_ROOT": str(copied_upstream)},
            )
            output_exists = output.exists()

        self.assertNotEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("upstream worktree is dirty", completed.stderr)
        self.assertFalse(output_exists, "oracle wrote output from a same-HEAD but dirty authoritative source")

    def test_oracle_rejects_tampered_loaded_behavior_assemblies_before_output(self):
        """Every behavior assembly must match its trusted bytes before the oracle writes output."""
        assemblies = (
            ("jellyfin-extensions", BUILD_OUTPUT / "Jellyfin.Extensions.dll"),
            ("mediabrowser-controller", BUILD_OUTPUT / "MediaBrowser.Controller.dll"),
            ("mediabrowser-model", BUILD_OUTPUT / "MediaBrowser.Model.dll"),
            ("system-private-corelib", loaded_runtime_assembly("System.Private.CoreLib.dll")),
            ("system-text-regular-expressions", loaded_runtime_assembly("System.Text.RegularExpressions.dll")),
            ("system-text-json", loaded_runtime_assembly("System.Text.Json.dll")),
            ("system-text-encodings-web", loaded_runtime_assembly("System.Text.Encodings.Web.dll")),
            ("system-io-compression", loaded_runtime_assembly("System.IO.Compression.dll")),
            ("system-security-cryptography", loaded_runtime_assembly("System.Security.Cryptography.dll")),
        )

        with tempfile.TemporaryDirectory(dir=REPO) as directory:
            temporary_root = Path(directory)
            for key, source_assembly in assemblies:
                with self.subTest(assembly=key):
                    copied_oracle = temporary_root / f"oracle-{key}"
                    shutil.copytree(BUILD_OUTPUT, copied_oracle)
                    environment = dict(os.environ)
                    tampered_assembly = copied_oracle / source_assembly.name

                    if source_assembly.parent != BUILD_OUTPUT:
                        copied_runtime_root = temporary_root / f"runtime-{key}"
                        copied_runtime = (
                            copied_runtime_root
                            / "shared"
                            / source_assembly.parent.parent.name
                            / source_assembly.parent.name
                        )
                        shutil.copytree(source_assembly.parents[2], copied_runtime_root / "shared")
                        dotnet_host = source_assembly.parents[3] / "dotnet"
                        self.assertTrue(dotnet_host.is_file(), "runtime dotnet host is unavailable")
                        shutil.copy2(dotnet_host, copied_runtime_root / "dotnet")
                        shutil.copytree(source_assembly.parents[3] / "host", copied_runtime_root / "host")
                        environment["PATH"] = f"{copied_runtime_root}{os.pathsep}{environment['PATH']}"
                        environment["DOTNET_ROOT"] = str(copied_runtime_root)
                        environment["DOTNET_ROOT_ARM64"] = str(copied_runtime_root)
                        environment["DOTNET_MULTILEVEL_LOOKUP"] = "0"
                        tampered_assembly = copied_runtime / source_assembly.name

                    self.assertTrue(tampered_assembly.is_file(), f"oracle did not load {tampered_assembly.name}")
                    bytes_to_tamper = bytearray(tampered_assembly.read_bytes())
                    self.assertTrue(bytes_to_tamper, f"{tampered_assembly} is empty")
                    bytes_to_tamper[-1] ^= 0x01
                    tampered_assembly.write_bytes(bytes_to_tamper)

                    output = temporary_root / f"normalization-oracle-{key}.json"
                    completed = run_copied_oracle(
                        copied_oracle,
                        "--vectors",
                        str(VECTORS),
                        "--output",
                        str(output),
                        environment=environment,
                    )
                    self.assertNotEqual(completed.returncode, 0, completed.stderr)
                    self.assertIn(f"loaded/source byte-hash mismatch for {key}", completed.stderr)
                    self.assertFalse(output.exists(), f"oracle wrote output from tampered {key}")


    def test_committed_artifacts_match_fresh_oracle_regeneration(self):
        """Committed Deadfin artifacts are exact outputs of the freshly built pinned oracle."""
        with tempfile.TemporaryDirectory(dir=REPO) as directory:
            temporary_root = Path(directory)
            artifacts = {
                "normalization oracle": (
                    NORMALIZATION_ORACLE_ARTIFACT,
                    temporary_root / "normalization-oracle.json",
                    ("--vectors", str(VECTORS)),
                ),
                "diacritics Go table": (
                    DIACRITICS_ARTIFACT,
                    temporary_root / "diacritics_gen.go",
                    ("--generate-diacritics-go",),
                ),
                "Unicode Go table": (
                    UNICODE_ARTIFACT,
                    temporary_root / "unicode16_gen.go",
                    ("--generate-unicode-go",),
                ),
            }
            for _, (_, generated, arguments) in artifacts.items():
                completed = run_oracle(*arguments, "--output", str(generated))
                self.assertEqual(completed.returncode, 0, completed.stderr)

            for name, (committed, generated, _) in artifacts.items():
                with self.subTest(artifact=name):
                    self.assertTrue(committed.is_file(), f"missing committed {name} artifact")
                    self.assertTrue(generated.is_file(), f"oracle did not generate {name} artifact")
                    committed_bytes = committed.read_bytes()
                    generated_bytes = generated.read_bytes()
                    self.assert_lf_terminated(committed, committed_bytes)
                    self.assert_lf_terminated(generated, generated_bytes)
                    if generated_bytes != committed_bytes:
                        self.fail(f"{name} artifact is not byte-for-byte identical to fresh oracle regeneration")

    def test_diacritics_generator_rejects_corrupt_pinned_archive(self):
        """A changed trusted archive fails before the diacritics generator writes bytes."""
        configured_package_root = os.environ.get("NUGET_PACKAGES")
        package_root = Path(configured_package_root) if configured_package_root else Path.home() / ".nuget" / "packages"
        cached_package = package_root / "diacritics" / "4.1.8"
        self.assertTrue(cached_package.is_dir(), f"pinned Diacritics package is not cached at {cached_package}")

        with tempfile.TemporaryDirectory(dir=REPO) as directory:
            temporary_root = Path(directory)
            copied_package = temporary_root / "packages" / "diacritics" / "4.1.8"
            shutil.copytree(cached_package, copied_package)
            copied_archive = copied_package / "diacritics.4.1.8.nupkg"
            archive_bytes = bytearray(copied_archive.read_bytes())
            self.assertTrue(archive_bytes, "pinned Diacritics archive is empty")
            archive_bytes[len(archive_bytes) // 2] ^= 0x01
            copied_archive.write_bytes(archive_bytes)

            output = temporary_root / "diacritics_gen.go"
            completed = run_oracle(
                "--generate-diacritics-go",
                "--output",
                str(output),
                environment={**os.environ, "NUGET_PACKAGES": str(temporary_root / "packages")},
            )

            self.assertNotEqual(completed.returncode, 0, completed.stderr)
            self.assertIn("NuGet archive hash mismatch for diacritics-4.1.8", completed.stderr)
            self.assertFalse(output.exists(), "diacritics generator wrote output from a corrupt archive")

    def test_diacritics_generator_rejects_tampered_loaded_assembly_before_loading(self):
        """A corrupted copied dependency is rejected against the intact trusted archive before .NET loads it."""
        configured_package_root = os.environ.get("NUGET_PACKAGES")
        package_root = Path(configured_package_root) if configured_package_root else Path.home() / ".nuget" / "packages"
        trusted_archive = package_root / "diacritics" / "4.1.8" / "diacritics.4.1.8.nupkg"
        self.assertTrue(trusted_archive.is_file(), f"pinned Diacritics archive is not cached at {trusted_archive}")

        with tempfile.TemporaryDirectory(dir=REPO) as directory:
            temporary_root = Path(directory)
            copied_oracle = temporary_root / "oracle"
            shutil.copytree(BUILD_OUTPUT, copied_oracle)
            tampered_assembly = copied_oracle / "Diacritics.dll"
            self.assertTrue(tampered_assembly.is_file(), f"oracle did not load {tampered_assembly.name}")
            tampered_assembly.write_bytes(b"not a managed assembly")

            output = temporary_root / "diacritics_gen.go"
            completed = run_copied_oracle(
                copied_oracle,
                "--generate-diacritics-go",
                "--output",
                str(output),
            )

        self.assertNotEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("loaded/archive byte-hash mismatch for diacritics-4.1.8", completed.stderr)
        self.assertFalse(output.exists(), "diacritics generator wrote output from a tampered loaded assembly")

    def assert_signed_document(self, artifact: Path, document: dict[str, object]):
        signature = document.get("sha256")
        self.assertIsInstance(signature, str, f"{artifact} has no SHA-256 signature")
        if not isinstance(signature, str):
            return
        signed_suffix = f',\n  "sha256": "{signature}"\n}}\n'.encode("ascii")
        rendered = artifact.read_bytes()
        self.assertTrue(rendered.endswith(signed_suffix), f"{artifact} does not end with its SHA-256 signature")
        unsigned = rendered[: -len(signed_suffix)] + b"\n}"
        self.assertEqual(signature, hashlib.sha256(unsigned).hexdigest(), f"{artifact} SHA-256 signature is invalid")

    def assert_lf_terminated(self, artifact: Path, content: bytes | None = None):
        content = artifact.read_bytes() if content is None else content
        self.assertNotIn(b"\r", content, f"{artifact} contains CR bytes")
        self.assertTrue(content.endswith(b"\n"), f"{artifact} does not end in LF")
        self.assertFalse(content.endswith(b"\n\n"), f"{artifact} ends in more than one LF")


if __name__ == "__main__":
    unittest.main()
