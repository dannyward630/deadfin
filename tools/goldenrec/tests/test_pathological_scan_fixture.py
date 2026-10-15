#!/usr/bin/env python3
"""tools/goldenrec/tests/test_pathological_scan_fixture.py

Focused test for the pathological fixture shapes that exercise the
duplicate-credit and duplicate-value reconcile paths of the scanner.

The test runs the fixture generator with a fake ffmpeg seam, then inspects
the generated NFO files and the probe-metadata command. It proves all six
source shapes and proves that no new media identity exists.

Six source shapes:

1. One audio item credits the same person twice with the same kind (Actor).
   The NFO Actor has no role. The probe performer value maps to an Actor
   with a role. The assembled-plan pass collapses the duplicate and fills
   the empty role.

2. The retained credit takes the probe role. This is the role-fill path.

3. The NFO Director for the same person survives because the collapse key
   includes PersonKind. This proves "collapses duplicates" not "collapses
   people."

4. One movie lists the same studio twice, exactly.

5. One movie lists the same studio twice differing only in case. The first
   occurrence's casing survives.

6. One movie lists a duplicate genre (fold-equal, different casing) and a
   duplicate tag (identical).
"""

import json
import os
import shutil
import subprocess
import tempfile
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path


GOLDENREC = Path(__file__).resolve().parents[1]
PROJECT_ROOT = GOLDENREC.parents[1]
GENERATOR = GOLDENREC / "fixtures" / "gen.sh"
HLG_ARTIFACT = GOLDENREC / "fixtures" / "attested" / "hlg-sample.mkv"
SOURCE_MANIFEST = GOLDENREC / "fixtures" / "source-manifest.sha256"
EXPECTED_INVENTORY = GOLDENREC / "fixtures" / "expected-inventory.json"

MARKER_FORMAT = "goldenrec-fixtures-owner-v1"


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


class PathologicalScanFixtureTests(unittest.TestCase):
    """Prove the six pathological source shapes and the 52-identity inventory."""

    @classmethod
    def setUpClass(cls):
        cls._tmpdir = tempfile.TemporaryDirectory(prefix="pathological-fixture-")
        cls._root = Path(cls._tmpdir.name)
        cls._tools = cls._root / "tools"
        cls._tools.mkdir()
        cls._ffmpeg = write_fixture_ffmpeg(cls._tools)
        cls._argv_log = cls._root / "ffmpeg-argv.log"

        fixtures = cls._root / "tools" / "goldenrec" / "fixtures"
        fixtures.mkdir(parents=True)
        cls._generator = fixtures / "gen.sh"
        shutil.copy2(GENERATOR, cls._generator)
        shutil.copy2(SOURCE_MANIFEST, fixtures / "source-manifest.sha256")
        attested = fixtures / "attested"
        attested.mkdir()
        shutil.copy2(HLG_ARTIFACT, attested / "hlg-sample.mkv")

        cls._output = cls._root / "out"
        environment = os.environ.copy()
        environment["GOLDENREC_FIXTURES_DIR"] = str(cls._output)
        environment["FFMPEG"] = str(cls._ffmpeg)
        environment["GOLDENREC_FFMPEG_ARGV_LOG"] = str(cls._argv_log)
        cls._result = subprocess.run(
            [str(cls._generator)],
            cwd=cls._root,
            capture_output=True,
            text=True,
            env=environment,
        )

    @classmethod
    def tearDownClass(cls):
        cls._tmpdir.cleanup()

    def test_generator_succeeds(self):
        """The fixture generator must exit zero with the fake ffmpeg seam."""
        self.assertEqual(self._result.returncode, 0, self._result.stderr or self._result.stdout)

    def test_audio_track_nfo_has_actor_without_role(self):
        """Shape 1: the audio track NFO has an Actor element with no role child."""
        nfo_path = self._output / "music" / "Goldenrec Generator" / "Synthetic Album" / "1 - Synthetic Track 1.nfo"
        self.assertTrue(nfo_path.is_file(), f"audio track NFO missing at {nfo_path}")
        root = ET.parse(nfo_path).getroot()
        actors = root.findall("actor")
        self.assertEqual(len(actors), 1, "audio track NFO must have exactly one Actor")
        name = actors[0].find("name")
        self.assertIsNotNone(name, "Actor must have a name child")
        self.assertEqual(name.text, "Synthetic Performer")
        role = actors[0].find("role")
        self.assertIsNone(role, "Actor must not have a role child (the probe fills the role)")

    def test_audio_track_nfo_has_director(self):
        """Shape 3: the audio track NFO has a Director for the same person."""
        nfo_path = self._output / "music" / "Goldenrec Generator" / "Synthetic Album" / "1 - Synthetic Track 1.nfo"
        root = ET.parse(nfo_path).getroot()
        directors = root.findall("director")
        self.assertEqual(len(directors), 1, "audio track NFO must have exactly one Director")
        name = directors[0].find("name")
        self.assertIsNotNone(name, "Director must have a name child")
        self.assertEqual(name.text, "Synthetic Performer",
                         "Director must credit the same person as the Actor")

    def test_probe_metadata_has_performer_with_role(self):
        """Shape 1+2: the probe performer tag carries the person name and a role."""
        self.assertTrue(self._argv_log.is_file(), "ffmpeg argv log was not written")
        raw = self._argv_log.read_bytes()
        groups = [chunk for chunk in raw.split(b"\0\0") if chunk]
        commands = [chunk.decode("utf-8", "replace") for chunk in groups]
        performer_commands = [c for c in commands if "performer=" in c]
        self.assertEqual(len(performer_commands), 1,
                         "exactly one ffmpeg command must carry a performer tag")
        command = performer_commands[0]
        self.assertIn("performer=Synthetic Performer (Lead Vocalist)", command,
                      "performer tag must use the Name (Role) format")
        self.assertIn("1 - Synthetic Track 1.mp3", command,
                      "performer tag must target the first audio track")

    def test_movie_nfo_has_duplicate_identical_studios(self):
        """Shape 4: one movie lists the same studio twice, exactly."""
        nfo_path = self._output / "movies" / "Standalone Movie (2023)" / "movie.nfo"
        root = ET.parse(nfo_path).getroot()
        studios = [s.text for s in root.findall("studio")]
        self.assertIn("Duplicate Studio", studios)
        self.assertEqual(studios.count("Duplicate Studio"), 2,
                         "movie NFO must list the identical studio twice")

    def test_movie_nfo_has_fold_equal_studios_different_casing(self):
        """Shape 5: one movie lists the same studio twice differing only in case."""
        nfo_path = self._output / "movies" / "Standalone Movie (2023)" / "movie.nfo"
        root = ET.parse(nfo_path).getroot()
        studios = [s.text for s in root.findall("studio")]
        self.assertIn("Case Studio", studios)
        self.assertIn("case studio", studios,
                      "movie NFO must list the fold-equal studio with different casing")
        case_index = studios.index("Case Studio")
        lower_index = studios.index("case studio")
        self.assertLess(case_index, lower_index,
                        "the first-occurrence casing must precede the second")

    def test_movie_nfo_has_duplicate_genre(self):
        """Shape 6 (genre): one movie lists a fold-equal genre twice."""
        nfo_path = self._output / "movies" / "Standalone Movie (2023)" / "movie.nfo"
        root = ET.parse(nfo_path).getroot()
        genres = [g.text for g in root.findall("genre")]
        self.assertIn("Synth Genre", genres)
        self.assertIn("synth genre", genres,
                       "movie NFO must list the fold-equal genre with different casing")
        upper_index = genres.index("Synth Genre")
        lower_index = genres.index("synth genre")
        self.assertLess(upper_index, lower_index,
                        "the first-occurrence casing must precede the second")

    def test_movie_nfo_has_duplicate_tag(self):
        """Shape 6 (tag): one movie lists the same tag twice, exactly."""
        nfo_path = self._output / "movies" / "Standalone Movie (2023)" / "movie.nfo"
        root = ET.parse(nfo_path).getroot()
        tags = [t.text for t in root.findall("tag")]
        self.assertEqual(tags.count("Duplicate Tag"), 2,
                         "movie NFO must list the identical tag twice")

    def test_value_names_are_distinct(self):
        """Each value group uses a distinct name so the shapes do not collide."""
        nfo_path = self._output / "movies" / "Standalone Movie (2023)" / "movie.nfo"
        root = ET.parse(nfo_path).getroot()
        studios = {s.text for s in root.findall("studio")}
        genres = {g.text for g in root.findall("genre")}
        tags = {t.text for t in root.findall("tag")}
        self.assertTrue(studios.isdisjoint(genres), "studio and genre names must not overlap")
        self.assertTrue(studios.isdisjoint(tags), "studio and tag names must not overlap")
        self.assertTrue(genres.isdisjoint(tags), "genre and tag names must not overlap")

    def test_no_new_media_identity(self):
        """The fixture tree must still have 30 media files and 52 inventory identities."""
        media_files = [
            p for p in self._output.rglob("*")
            if p.is_file() and p.suffix in (".mkv", ".mp3")
        ]
        self.assertEqual(len(media_files), 30,
                         f"media file count changed: expected 30, got {len(media_files)}")

        expected = json.loads(EXPECTED_INVENTORY.read_text(encoding="utf-8"))
        self.assertIsInstance(expected, list)
        self.assertEqual(len(expected), 52,
                         f"expected inventory must contain 52 identities, got {len(expected)}")
        self.assertEqual(len(set(expected)), 52,
                         "expected inventory must not repeat an identity")


if __name__ == "__main__":
    unittest.main()
