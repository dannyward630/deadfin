#!/usr/bin/env python3
"""tools/coldstart/tests/test_detail_item_selection.py

Focused coverage for the detail-fetch item selection.

The recursive /Items listing carries the physical Folder item for each
library root. The gate previously selected the first item of
any type and then asserted a content type against it, so a listing that
began with a Folder failed on a correct server. The selection must filter
on the same set the assertion uses.
"""

import contextlib
import importlib.util
import io
import os
import unittest

# Load verify.py as a module without running main().
verify_path = os.path.join(os.path.dirname(__file__), "..", "verify.py")
spec = importlib.util.spec_from_file_location("coldstart_verify_select", verify_path)
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


class TestDetailItemSelection(unittest.TestCase):

    def test_skips_leading_folder_and_returns_content_item(self):
        """A listing that begins with a Folder still selects a content item.

        This is the ordering that broke the gate on Jellyport run 2863. A
        fixture beginning with a Movie passes both before and after the fix
        and proves nothing, so the Folder must come first here.
        """
        items = [
            {"Id": "folder-movies", "Type": "Folder"},
            {"Id": "folder-tv", "Type": "Folder"},
            {"Id": "movie-1", "Type": "Movie"},
        ]
        self.assertEqual(verify.select_content_item_id(items), "movie-1")

    def test_selects_first_content_item_when_several_follow(self):
        """Selection stops at the first content item, not the last."""
        items = [
            {"Id": "folder-movies", "Type": "Folder"},
            {"Id": "episode-1", "Type": "Episode"},
            {"Id": "movie-1", "Type": "Movie"},
        ]
        self.assertEqual(verify.select_content_item_id(items), "episode-1")

    def test_no_content_item_fails(self):
        """A listing with no content item fails rather than selecting nothing.

        Negative control. Without it the fix could silently select nothing
        and skip the detail assertion, turning a real check into a no-op.
        """
        items = [
            {"Id": "folder-movies", "Type": "Folder"},
            {"Id": "folder-tv", "Type": "Folder"},
        ]
        sink = io.StringIO()
        with self.assertRaises(SystemExit), contextlib.redirect_stderr(sink):
            verify.select_content_item_id(items)
        # The diagnostic must name the content item types the function
        # looked for. Without this assertion, removing the diagnostic
        # message from fail() leaves the test green and hides the fault.
        output = sink.getvalue()
        self.assertIn("content item type", output)
        for content_type in verify.CONTENT_ITEM_TYPES:
            self.assertIn(content_type, output)

    def test_empty_listing_fails(self):
        """An empty listing fails for the same reason."""
        with self.assertRaises(SystemExit):
            verify.select_content_item_id([])

    def test_item_without_stable_id_is_skipped(self):
        """An item missing a usable Id is skipped, not selected."""
        items = [
            {"Type": "Movie"},
            {"Id": "", "Type": "Movie"},
            {"Id": "movie-2", "Type": "Movie"},
        ]
        self.assertEqual(verify.select_content_item_id(items), "movie-2")

    def test_non_object_entries_are_skipped(self):
        """Non-object entries do not abort the scan."""
        items = ["not-an-object", None, {"Id": "movie-3", "Type": "Movie"}]
        self.assertEqual(verify.select_content_item_id(items), "movie-3")


if __name__ == "__main__":
    unittest.main()
