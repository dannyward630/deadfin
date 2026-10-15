#!/usr/bin/env python3
"""tools/coldstart/tests/test_pathological_metadata.py

Focused unit coverage for the pathological metadata assertions in verify.py.

The cold-start gate reads detail DTOs for two fixture items after the
stable-idle scan: an audio track with person shapes and a movie with value
shapes. These tests exercise the assertion logic against scripted responses
to prove that wrong count, wrong kind, missing role, wrong order, and wrong
casing all fail the gate.
"""

import importlib.util
import os
import unittest

# Load verify.py as a module without running main().
verify_path = os.path.join(os.path.dirname(__file__), "..", "verify.py")
spec = importlib.util.spec_from_file_location("coldstart_verify_pathological", verify_path)
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class MockClient:
    """A mock client that returns a scripted /Items/{id} response."""

    def __init__(self, item_data):
        self._data = item_data

    def get_json(self, path, expected=200, auth=True):
        return self._data


def make_items(path, item_id="item-1", item_type="Audio"):
    """Return an items list with one item at the given media path."""
    return [{"Id": item_id, "Path": path, "Type": item_type}]


# The person shapes need an Audio item at the pathological audio path.
AUDIO_ITEMS = make_items(
    verify.PATHOLOGICAL_AUDIO_PATH,
    item_id="audio-1",
    item_type="Audio",
)

# The value shapes need a Movie item at the pathological movie path.
MOVIE_ITEMS = make_items(
    verify.PATHOLOGICAL_MOVIE_PATH,
    item_id="movie-1",
    item_type="Movie",
)


class TestPathologicalPerson(unittest.TestCase):
    """Person shape assertions: one Actor + one Director, role filled from probe."""

    def test_correct_person_shapes_pass(self):
        """One Actor with the probe role and one Director must pass."""
        detail = {
            "Id": "audio-1",
            "Type": "Audio",
            "People": [
                {"Type": "Actor", "Name": "Synthetic Performer", "Role": "Lead Vocalist"},
                {"Type": "Director", "Name": "Synthetic Performer"},
            ],
        }
        client = MockClient(detail)
        item_id = verify.verify_pathological_person(client, AUDIO_ITEMS)
        self.assertEqual(item_id, "audio-1")

    def test_missing_item_fails(self):
        """An absent audio track must fail."""
        client = MockClient({})
        with self.assertRaises(SystemExit):
            verify.verify_pathological_person(client, [])

    def test_zero_actors_fails(self):
        """No Actor for the person must fail (wrong count)."""
        detail = {
            "Id": "audio-1",
            "Type": "Audio",
            "People": [
                {"Type": "Director", "Name": "Synthetic Performer"},
            ],
        }
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_person(client, AUDIO_ITEMS)

    def test_two_actors_fails(self):
        """Two Actors for the same person must fail (wrong count)."""
        detail = {
            "Id": "audio-1",
            "Type": "Audio",
            "People": [
                {"Type": "Actor", "Name": "Synthetic Performer", "Role": "Lead Vocalist"},
                {"Type": "Actor", "Name": "Synthetic Performer", "Role": "Backup"},
            ],
        }
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_person(client, AUDIO_ITEMS)

    def test_zero_directors_fails(self):
        """No Director for the person must fail (wrong kind)."""
        detail = {
            "Id": "audio-1",
            "Type": "Audio",
            "People": [
                {"Type": "Actor", "Name": "Synthetic Performer", "Role": "Lead Vocalist"},
            ],
        }
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_person(client, AUDIO_ITEMS)

    def test_missing_role_fails(self):
        """An Actor without the probe role must fail (missing role)."""
        detail = {
            "Id": "audio-1",
            "Type": "Audio",
            "People": [
                {"Type": "Actor", "Name": "Synthetic Performer", "Role": ""},
                {"Type": "Director", "Name": "Synthetic Performer"},
            ],
        }
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_person(client, AUDIO_ITEMS)

    def test_wrong_role_fails(self):
        """An Actor with the wrong role must fail (missing role)."""
        detail = {
            "Id": "audio-1",
            "Type": "Audio",
            "People": [
                {"Type": "Actor", "Name": "Synthetic Performer", "Role": "Guitarist"},
                {"Type": "Director", "Name": "Synthetic Performer"},
            ],
        }
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_person(client, AUDIO_ITEMS)

    def test_missing_people_field_fails(self):
        """A missing People field must fail."""
        detail = {"Id": "audio-1", "Type": "Audio"}
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_person(client, AUDIO_ITEMS)


class TestPathologicalValues(unittest.TestCase):
    """Value shape assertions: studio, genre, and tag lists match exactly."""

    def test_correct_value_shapes_pass(self):
        """The expected studios, genres, and tags must pass."""
        detail = {
            "Id": "movie-1",
            "Type": "Movie",
            "Studios": [{"Name": "Duplicate Studio"}, {"Name": "Case Studio"}],
            "Genres": ["Synth Genre"],
            "Tags": ["Duplicate Tag"],
        }
        client = MockClient(detail)
        item_id = verify.verify_pathological_values(client, MOVIE_ITEMS)
        self.assertEqual(item_id, "movie-1")

    def test_missing_item_fails(self):
        """An absent movie must fail."""
        client = MockClient({})
        with self.assertRaises(SystemExit):
            verify.verify_pathological_values(client, [])

    def test_wrong_studio_order_fails(self):
        """Studios in the wrong order must fail (wrong order)."""
        detail = {
            "Id": "movie-1",
            "Type": "Movie",
            "Studios": [{"Name": "Case Studio"}, {"Name": "Duplicate Studio"}],
            "Genres": ["Synth Genre"],
            "Tags": ["Duplicate Tag"],
        }
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_values(client, MOVIE_ITEMS)

    def test_wrong_studio_casing_fails(self):
        """A studio with the wrong casing must fail (wrong casing)."""
        detail = {
            "Id": "movie-1",
            "Type": "Movie",
            "Studios": [{"Name": "Duplicate Studio"}, {"Name": "case studio"}],
            "Genres": ["Synth Genre"],
            "Tags": ["Duplicate Tag"],
        }
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_values(client, MOVIE_ITEMS)

    def test_extra_studio_fails(self):
        """An extra studio that the normalizer should have collapsed must fail."""
        detail = {
            "Id": "movie-1",
            "Type": "Movie",
            "Studios": [
                {"Name": "Duplicate Studio"},
                {"Name": "Duplicate Studio"},
                {"Name": "Case Studio"},
            ],
            "Genres": ["Synth Genre"],
            "Tags": ["Duplicate Tag"],
        }
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_values(client, MOVIE_ITEMS)

    def test_wrong_genre_casing_fails(self):
        """A genre with the wrong casing must fail (wrong casing)."""
        detail = {
            "Id": "movie-1",
            "Type": "Movie",
            "Studios": [{"Name": "Duplicate Studio"}, {"Name": "Case Studio"}],
            "Genres": ["synth genre"],
            "Tags": ["Duplicate Tag"],
        }
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_values(client, MOVIE_ITEMS)

    def test_extra_genre_fails(self):
        """An extra genre that the normalizer should have collapsed must fail."""
        detail = {
            "Id": "movie-1",
            "Type": "Movie",
            "Studios": [{"Name": "Duplicate Studio"}, {"Name": "Case Studio"}],
            "Genres": ["Synth Genre", "synth genre"],
            "Tags": ["Duplicate Tag"],
        }
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_values(client, MOVIE_ITEMS)

    def test_extra_tag_fails(self):
        """An extra tag that the normalizer should have collapsed must fail."""
        detail = {
            "Id": "movie-1",
            "Type": "Movie",
            "Studios": [{"Name": "Duplicate Studio"}, {"Name": "Case Studio"}],
            "Genres": ["Synth Genre"],
            "Tags": ["Duplicate Tag", "Duplicate Tag"],
        }
        client = MockClient(detail)
        with self.assertRaises(SystemExit):
            verify.verify_pathological_values(client, MOVIE_ITEMS)

    def test_studios_as_strings_passes(self):
        """Studios as plain strings must also pass the assertion."""
        detail = {
            "Id": "movie-1",
            "Type": "Movie",
            "Studios": ["Duplicate Studio", "Case Studio"],
            "Genres": ["Synth Genre"],
            "Tags": ["Duplicate Tag"],
        }
        client = MockClient(detail)
        item_id = verify.verify_pathological_values(client, MOVIE_ITEMS)
        self.assertEqual(item_id, "movie-1")


if __name__ == "__main__":
    unittest.main()
