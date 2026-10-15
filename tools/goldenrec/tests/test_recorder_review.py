import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


REPO = Path(__file__).resolve().parents[3]
RECORDER_ROOT = REPO / "tools" / "goldenrec"
RECORDER = RECORDER_ROOT / "record.sh"
PROVENANCE = RECORDER_ROOT / "provenance.py"
PROJECT = RECORDER_ROOT / "GoldenRec.csproj"
UPSTREAM = REPO / "upstream" / "jellyfin"
DEADFIN = REPO / "deadfin"
PIN = "b04614e18d2aeed8d3d3de920c381c6bd5b97bdb"
DOTNET = shutil.which("dotnet")



def file_hashes(root):
    return {
        path.relative_to(root).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted(root.rglob("*"))
        if path.is_file()
    }


def load_json(path):
    return json.loads(path.read_text(encoding="utf-8"))

def install_fake_recorder_runtime(directory):
    """Install deterministic dotnet/python boundaries for record.sh integration tests."""
    binary = directory / "bin"
    binary.mkdir()
    dotnet = binary / "dotnet"
    dotnet.write_text(
        f"#!{sys.executable}\n"
        "import json, os, shutil, sys\n"
        "from pathlib import Path\n"
        "arguments = sys.argv[1:]\n"
        "Path(os.environ['GOLDENREC_DOTNET_CAPTURE']).write_text(json.dumps(arguments))\n"
        "separator = arguments.index('--')\n"
        "mode = arguments[separator + 1]\n"
        "root = Path(arguments[arguments.index('--root') + 1])\n"
        "subsystem = {'streambuilder': 'playback', 'encoding': 'encoding'}[mode]\n"
        "source = Path(os.environ['GOLDENREC_CORPUS_FIXTURES']) / 'internal' / subsystem / 'testdata'\n"
        "destination = root / 'internal' / subsystem / 'testdata'\n"
        "shutil.copytree(source, destination, dirs_exist_ok=True)\n",
        encoding="utf-8",
    )
    dotnet.chmod(0o755)
    python = binary / "python3"
    python.write_text(
        f"#!{sys.executable}\n"
        "import json, os, sys\n"
        "from pathlib import Path\n"
        "capture_path = os.environ.get('GOLDENREC_PYTHON_CAPTURE')\n"
        "if capture_path:\n"
        "    capture = Path(capture_path)\n"
        "    calls = json.loads(capture.read_text()) if capture.exists() else []\n"
        "    calls.append(sys.argv[1:])\n"
        "    capture.write_text(json.dumps(calls))\n"
        f"os.execv({sys.executable!r}, [{sys.executable!r}, *sys.argv[1:]])\n",
        encoding="utf-8",
    )
    python.chmod(0o755)
    return binary


class GoldenRecorderReviewRegressionTests(unittest.TestCase):
    maxDiff = None

    def test_encoding_corpus_has_deterministic_input_and_seek_arguments(self):
        """Every recorded ffmpeg vector names its fixture path and an offset value."""
        cases = load_json(DEADFIN / "internal" / "encoding" / "testdata" / "golden" / "encoding.json")["cases"]
        failures = []
        for case in cases:
            argv = case["argv"]
            extension = "mkv" if case["fixture"].startswith("mkv-") else "mp4"
            expected_input = f' -i file:"/media/goldenrec-input.{extension}"'
            if argv["input"] != expected_input:
                failures.append(f'{case["id"]}: input was {argv["input"]!r}, expected {expected_input!r}')
            for command_name in ("progressive", "hls"):
                command = argv[command_name]
                if expected_input not in command:
                    failures.append(f'{case["id"]}: {command_name} omits {expected_input!r}')
                if case["seek"] == "offset":
                    if "-ss 00:01:30.000" not in command:
                        failures.append(f'{case["id"]}: {command_name} omits the 90-second seek offset')
                elif "-ss " in command:
                    failures.append(f'{case["id"]}: {command_name} unexpectedly seeks a start request')
        self.assertFalse(
            failures,
            "EncodingHelper corpus must retain deterministic media paths and offset values:\n"
            + "\n".join(failures[:12]),
        )

    def test_relative_alternate_root_is_canonical_for_every_recorder_boundary(self):
        """A relative alternate root reaches precheck, MSBuild, recorder, and provenance as one absolute tree."""
        with tempfile.TemporaryDirectory() as temporary:
            temporary = Path(temporary)
            caller = temporary / "controlled-cwd"
            caller.mkdir()
            alternate = caller / "alternate-jellyfin"
            alternate.symlink_to(UPSTREAM, target_is_directory=True)
            output = temporary / "output"
            shutil.copytree(DEADFIN / "internal", output / "internal")
            dotnet_capture = temporary / "dotnet-arguments.json"
            python_capture = temporary / "python-arguments.json"
            binary = install_fake_recorder_runtime(temporary)
            expected_upstream = str(alternate.resolve())
            env = os.environ | {
                "GOLDENREC_UPSTREAM_ROOT": "alternate-jellyfin",
                "GOLDENREC_DOTNET_CAPTURE": str(dotnet_capture),
                "GOLDENREC_PYTHON_CAPTURE": str(python_capture),
                "GOLDENREC_CORPUS_FIXTURES": str(DEADFIN),
                "PATH": f"{binary}{os.pathsep}{os.environ['PATH']}",
            }
            result = subprocess.run(
                [str(RECORDER), "streambuilder", "--output-root", str(output)],
                cwd=caller,
                capture_output=True,
                text=True,
                env=env,
            )

            self.assertEqual(result.returncode, 0, result.stderr or result.stdout)
            recorder_args = load_json(dotnet_capture)
            self.assertEqual(
                [argument for argument in recorder_args if argument.startswith("-p:UpstreamRoot=")],
                [f"-p:UpstreamRoot={expected_upstream}"],
                "record.sh passed a relative GOLDENREC_UPSTREAM_ROOT to MSBuild instead of one canonical tree",
            )
            application_separator = recorder_args.index("--")
            self.assertEqual(
                recorder_args[application_separator + 1 : application_separator + 6],
                ["streambuilder", "--root", str(output), "--upstream", expected_upstream],
                "record.sh passed a different upstream path to the recorder application",
            )
            provenance_calls = [
                arguments
                for arguments in load_json(python_capture)
                if Path(arguments[0]).name == "provenance.py"
            ]
            self.assertEqual(len(provenance_calls), 2, "record.sh did not write and verify provenance")
            for arguments in provenance_calls:
                with self.subTest(provenance_command=arguments[1]):
                    self.assertEqual(
                        arguments[arguments.index("--upstream") + 1],
                        expected_upstream,
                        "record.sh passed a relative or divergent upstream path to provenance",
                    )

    def test_named_modes_succeed_without_the_unselected_sibling_corpus(self):
        """Each named recorder regenerates only its selected corpus and leaves an absent sibling absent."""
        for mode, selected, sibling in (
            ("streambuilder", "playback", "encoding"),
            ("encoding", "encoding", "playback"),
        ):
            with self.subTest(mode=mode):
                with tempfile.TemporaryDirectory() as temporary:
                    temporary = Path(temporary)
                    output = temporary / "deadfin"
                    dotnet_capture = temporary / "dotnet-arguments.json"
                    binary = install_fake_recorder_runtime(temporary)
                    env = os.environ | {
                        "GOLDENREC_UPSTREAM_ROOT": str(UPSTREAM),
                        "GOLDENREC_DOTNET_CAPTURE": str(dotnet_capture),
                        "GOLDENREC_CORPUS_FIXTURES": str(DEADFIN),
                        "PATH": f"{binary}{os.pathsep}{os.environ['PATH']}",
                    }
                    result = subprocess.run(
                        [str(RECORDER), mode, "--output-root", str(output)],
                        cwd=REPO,
                        capture_output=True,
                        text=True,
                        env=env,
                    )

                    self.assertEqual(
                        result.returncode,
                        0,
                        f"{mode} must verify its selected corpus without {sibling}; recorder emitted: "
                        f"{result.stderr.strip() or result.stdout.strip()}",
                    )
                    self.assertTrue(
                        (output / "internal" / selected / "testdata" / "golden").is_dir(),
                        f"{mode} did not preserve its generated {selected} corpus",
                    )
                    self.assertFalse(
                        (output / "internal" / sibling).exists(),
                        f"{mode} recreated the intentionally absent {sibling} sibling corpus",
                    )

    def test_verify_rejects_changed_recorder_source_and_command(self):
        """Provenance verification binds a corpus to its recorder implementation and command."""
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            recorder = directory / "repo" / "tools" / "goldenrec"
            recorder.mkdir(parents=True)
            for name in ("GoldenRec.csproj", "Program.cs", "record.sh", "provenance.py"):
                shutil.copy2(RECORDER_ROOT / name, recorder / name)
            root = directory / "repo" / "deadfin"
            shutil.copytree(DEADFIN / "internal", root / "internal")

            def provenance(mode):
                return subprocess.run(
                    [sys.executable, str(recorder / "provenance.py"), mode, "--root", str(root), "--upstream", str(UPSTREAM)],
                    capture_output=True,
                    text=True,
                )

            written = provenance("write")
            self.assertEqual(written.returncode, 0, written.stderr)
            (recorder / "Program.cs").write_text((recorder / "Program.cs").read_text(encoding="utf-8") + "\n// modified fixture recorder\n", encoding="utf-8")
            source_changed = provenance("verify")

            shutil.copy2(RECORDER_ROOT / "Program.cs", recorder / "Program.cs")
            rewritten = provenance("write")
            self.assertEqual(rewritten.returncode, 0, rewritten.stderr)
            command_path = root / "internal" / "encoding" / "testdata" / "golden" / "provenance.json"
            command_provenance = load_json(command_path)
            command_provenance["recorder"]["command"] = "tools/goldenrec/record.sh all --output-root deadfin"
            command_path.write_text(json.dumps(command_provenance, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            command_changed = provenance("verify")

            failures = []
            if source_changed.returncode == 0:
                failures.append("verify accepted a corpus after Program.cs changed")
            elif "goldenrec: recorder source hash mismatch for streambuilder" not in source_changed.stderr:
                failures.append(f"source mutation emitted {source_changed.stderr!r}, not the recorder-source mismatch diagnostic")
            if command_changed.returncode == 0:
                failures.append("verify accepted a corpus after its recorder command changed")
            elif "goldenrec: recorder command hash mismatch for encoding" not in command_changed.stderr:
                failures.append(f"command mutation emitted {command_changed.stderr!r}, not the recorder-command mismatch diagnostic")
            self.assertFalse(failures, "Provenance must reject recorder identity drift:\n" + "\n".join(failures))

    def test_git_ignores_goldenrec_build_outputs(self):
        """Representative GoldenRec build artifacts must stay out of repository status."""
        for artifact in (
            "tools/goldenrec/bin/Release/net10.0/GoldenRec.dll",
            "tools/goldenrec/obj/Release/net10.0/GoldenRec.AssemblyInfo.cs",
        ):
            with self.subTest(artifact=artifact):
                ignored = subprocess.run(
                    ["git", "check-ignore", "--no-index", "--quiet", artifact],
                    cwd=REPO,
                    capture_output=True,
                    text=True,
                )
                self.assertEqual(
                    ignored.returncode,
                    0,
                    f"Git would report generated GoldenRec artifact {artifact} as unignored: "
                    f"{ignored.stderr.strip() or ignored.stdout.strip()}",
                )

    def test_moq_version_is_supplied_by_central_package_management(self):
        """GoldenRec's Moq dependency must inherit its version from the pinned upstream catalog."""
        with tempfile.TemporaryDirectory() as directory:
            probe = Path(directory) / "package-probe.targets"
            probe.write_text(
                "<Project>\n"
                "  <Target Name=\"InspectGoldenRecMoq\">\n"
                "    <Message Importance=\"high\" Text=\"GoldenRecMoq=%(PackageReference.Identity)|%(PackageReference.Version)\" Condition=\"'%(PackageReference.Identity)' == 'Moq'\" />\n"
                "  </Target>\n"
                "</Project>\n",
                encoding="utf-8",
            )
            result = subprocess.run(
                [
                    DOTNET,
                    "msbuild",
                    str(PROJECT),
                    "-t:InspectGoldenRecMoq",
                    f"-p:CustomAfterMicrosoftCommonTargets={probe}",
                    "-nologo",
                ],
                cwd=REPO,
                capture_output=True,
                text=True,
            )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            [line.strip() for line in result.stdout.splitlines() if "GoldenRecMoq=" in line],
            ["GoldenRecMoq=Moq|"],
            "GoldenRec declares a Moq Version directly instead of inheriting the upstream central package version",
        )


if __name__ == "__main__":
    unittest.main()
