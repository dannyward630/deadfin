import importlib.util
import sys
import sqlite3
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
RECORDER_PATH = ROOT / "record-itemquery.py"


def load_recorder():
    spec = importlib.util.spec_from_file_location("goldenrec_snapshot_merge_test", RECORDER_PATH)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class SnapshotMergeTests(unittest.TestCase):
    def setUp(self):
        self.recorder = load_recorder()

    def _snapshot(self, directory: Path, rows: list[tuple[str, str, str, int | None]]) -> Path:
        database = directory / "jellyfin.db"
        connection = sqlite3.connect(database)
        try:
            connection.execute(
                "CREATE TABLE BaseItems (Id TEXT NOT NULL, Path TEXT NOT NULL, DateLastSaved TEXT, TotalBitrate INTEGER)"
            )
            connection.executemany(
                "INSERT INTO BaseItems (Id, Path, DateLastSaved, TotalBitrate) VALUES (?, ?, ?, ?)",
                rows,
            )
            connection.commit()
        finally:
            connection.close()
        return database

    def test_merge_stock_snapshot_normalizes_timestamp_and_copies_bitrate_including_null(self):
        movie_identity = "Movie:/media/movies/Example.mkv"
        episode_identity = "Episode:/media/tv/Example S01E01.mkv"
        fixtures = {
            "items": [
                {"identity": movie_identity, "scalars": {"Path": "/media/movies/Example.mkv"}},
                {"identity": episode_identity, "scalars": {"Path": "/media/tv/Example S01E01.mkv"}},
            ]
        }
        item_map = {movie_identity: "movie-id", episode_identity: "episode-id"}

        with tempfile.TemporaryDirectory() as temporary:
            snapshot = self._snapshot(
                Path(temporary),
                [
                    ("movie-id", "/media/movies/Example.mkv", "2026-07-26 21:10:16.2448232", 8_765_432),
                    ("episode-id", "/media/tv/Example S01E01.mkv", "2026-07-26 21:10:17.0000000", None),
                ],
            )
            self.recorder.Recorder._merge_stock_snapshot(fixtures, snapshot, item_map)

        movie, episode = fixtures["items"]
        self.assertEqual(movie["scalars"]["DateLastSaved"], "2026-07-26T21:10:16.2448232Z")
        self.assertEqual(movie["scalars"]["TotalBitrate"], 8_765_432)
        self.assertEqual(episode["scalars"]["DateLastSaved"], "2026-07-26T21:10:17.0000000Z")
        self.assertIsNone(episode["scalars"]["TotalBitrate"])

    def test_merge_stock_snapshot_rejects_missing_fixture_path_or_source_row(self):
        identity = "Movie:/media/movies/Example.mkv"
        item_map = {identity: "movie-id"}

        for name, fixtures, rows in (
            (
                "fixture-path",
                {"items": [{"identity": identity, "scalars": {}}]},
                [("movie-id", "/media/movies/Example.mkv", "2026-07-26 21:10:16.2448232", 8_765_432)],
            ),
            (
                "source-row",
                {"items": [{"identity": identity, "scalars": {"Path": "/media/movies/Example.mkv"}}]},
                [("movie-id", "/media/movies/Other.mkv", "2026-07-26 21:10:16.2448232", 8_765_432)],
            ),
        ):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                snapshot = self._snapshot(Path(temporary), rows)
                with self.assertRaises(self.recorder.RecorderError):
                    self.recorder.Recorder._merge_stock_snapshot(fixtures, snapshot, item_map)


if __name__ == "__main__":
    unittest.main()
