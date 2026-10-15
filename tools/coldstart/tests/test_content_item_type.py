#!/usr/bin/env python3
"""tools/coldstart/tests/test_content_item_type.py

Content item type assertion test for fetch_item_detail.

The verify client must require a content item type (Movie, Series, Episode,
etc.) rather than any nonempty Type. A Folder type must fail the assertion.
A content item type must pass.
"""

import importlib.util
import os
import sys
import unittest

# Load verify.py as a module without running main().
verify_path = os.path.join(os.path.dirname(__file__), "..", "verify.py")
spec = importlib.util.spec_from_file_location("coldstart_verify_type", verify_path)
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class MockClient:
    """A mock client that returns a scripted /Items/{id} response."""

    def __init__(self, item_data):
        self._data = item_data

    def get_json(self, path, expected=200, auth=True):
        return self._data


class TestContentItemType(unittest.TestCase):

    def test_folder_type_fails(self):
        """A Folder Type must fail the content item type assertion."""
        client = MockClient({"Id": "item-1", "Type": "Folder"})
        with self.assertRaises(SystemExit):
            verify.fetch_item_detail(client, "item-1")

    def test_empty_type_fails(self):
        """An empty Type must fail."""
        client = MockClient({"Id": "item-1", "Type": ""})
        with self.assertRaises(SystemExit):
            verify.fetch_item_detail(client, "item-1")

    def test_missing_type_fails(self):
        """A missing Type must fail."""
        client = MockClient({"Id": "item-1"})
        with self.assertRaises(SystemExit):
            verify.fetch_item_detail(client, "item-1")

    def test_movie_type_passes(self):
        """A Movie Type must pass the content item type assertion."""
        client = MockClient({"Id": "item-1", "Type": "Movie"})
        data = verify.fetch_item_detail(client, "item-1")
        self.assertEqual(data["Type"], "Movie")

    def test_episode_type_passes(self):
        """An Episode Type must pass."""
        client = MockClient({"Id": "item-1", "Type": "Episode"})
        data = verify.fetch_item_detail(client, "item-1")
        self.assertEqual(data["Type"], "Episode")

    def test_id_mismatch_fails(self):
        """An Id mismatch must fail regardless of the Type."""
        client = MockClient({"Id": "wrong-id", "Type": "Movie"})
        with self.assertRaises(SystemExit):
            verify.fetch_item_detail(client, "item-1")


if __name__ == "__main__":
    unittest.main()
