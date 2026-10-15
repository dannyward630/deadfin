"""Focused tests for the route-omission reconciliation tool."""

import io
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(
    0,
    os.path.join(os.path.dirname(__file__), ".."),
)

import reconcile  # noqa: E402


def _write_tmp(text, suffix):
    """Write text to a temporary file and return its path."""
    handle = tempfile.NamedTemporaryFile(
        mode="w",
        suffix=suffix,
        delete=False,
        encoding="utf-8",
    )
    handle.write(text)
    handle.close()
    return handle.name


def _run_main(requests_text, operations, omissions, evidence):
    """Run the tool with string inputs and return (stdout, stderr, exit)."""
    requests_path = _write_tmp(requests_text, ".jsonl")
    operations_path = _write_tmp(json.dumps(operations), ".json")
    omissions_path = _write_tmp(json.dumps(omissions), ".json")
    stdout = io.StringIO()
    stderr = io.StringIO()
    original_stdout = sys.stdout
    original_stderr = sys.stderr
    original_exit = sys.exit
    sys.stdout = stdout
    sys.stderr = stderr
    exit_code = 0

    def fake_exit(code=0):
        raise SystemExit(code)

    sys.exit = fake_exit
    try:
        reconcile.main(
            [
                "--requests",
                requests_path,
                "--operations",
                operations_path,
                "--omissions",
                omissions_path,
                "--evidence",
                evidence,
            ],
        )
    except SystemExit as exc:
        exit_code = exc.code
    finally:
        sys.stdout = original_stdout
        sys.stderr = original_stderr
        sys.exit = original_exit
        os.remove(requests_path)
        os.remove(operations_path)
        os.remove(omissions_path)
    return stdout.getvalue(), stderr.getvalue(), exit_code


def _operations(ops):
    """Wrap an operations list in the manifest shape."""
    return {"header": {"specVersion": "test"}, "operations": ops}


class TestJsonlErrors(unittest.TestCase):
    """Cover JSONL parse errors and invalid records (mechanism 4)."""

    def test_invalid_json_reports_file_and_line(self):
        operations = _operations(
            [{"operationId": "Op", "method": "GET", "path": "/Foo"}],
        )
        omissions = []
        _, stderr, exit_code = _run_main(
            '{"method": "GET", "path": "/Foo"}\nnot json\n',
            operations,
            omissions,
            "ev",
        )
        self.assertNotEqual(exit_code, 0)
        self.assertIn(":2: invalid JSON", stderr)

    def test_non_object_record_reports_file_and_line(self):
        operations = _operations(
            [{"operationId": "Op", "method": "GET", "path": "/Foo"}],
        )
        omissions = []
        _, stderr, exit_code = _run_main(
            '["not an object"]\n',
            operations,
            omissions,
            "ev",
        )
        self.assertNotEqual(exit_code, 0)
        self.assertIn(":1: record is not an object", stderr)

    def test_missing_method_reports_file_and_line(self):
        operations = _operations(
            [{"operationId": "Op", "method": "GET", "path": "/Foo"}],
        )
        omissions = []
        _, stderr, exit_code = _run_main(
            '{"path": "/Foo"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertNotEqual(exit_code, 0)
        self.assertIn(":1: record lacks a string method field", stderr)

    def test_missing_path_reports_file_and_line(self):
        operations = _operations(
            [{"operationId": "Op", "method": "GET", "path": "/Foo"}],
        )
        omissions = []
        _, stderr, exit_code = _run_main(
            '{"method": "GET"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertNotEqual(exit_code, 0)
        self.assertIn(":1: record lacks a string path field", stderr)

    def test_blank_lines_are_ignored(self):
        operations = _operations(
            [{"operationId": "Op", "method": "GET", "path": "/Foo"}],
        )
        omissions = [{"operationId": "Op", "reason": "deferred"}]
        stdout, _, exit_code = _run_main(
            '\n  \n{"method": "GET", "path": "/Foo"}\n\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(len(data), 1)


class TestQueryRemoval(unittest.TestCase):
    """Cover query and fragment removal (mechanism 8)."""

    def test_query_is_removed(self):
        operations = _operations(
            [
                {
                    "operationId": "Op",
                    "method": "GET",
                    "path": "/Items/{itemId}",
                }
            ],
        )
        omissions = [{"operationId": "Op", "reason": "deferred"}]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Items/42?fields=Basic"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["operationId"], "Op")

    def test_fragment_is_removed(self):
        operations = _operations(
            [
                {
                    "operationId": "Op",
                    "method": "GET",
                    "path": "/Items/{itemId}",
                }
            ],
        )
        omissions = [{"operationId": "Op", "reason": "deferred"}]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Items/42#section"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["operationId"], "Op")


class TestMethodMatching(unittest.TestCase):
    """Cover method normalization and exact method match (mechanism 8, 11)."""

    def test_lowercase_request_method_matches_uppercase_operation(self):
        operations = _operations(
            [{"operationId": "Op", "method": "GET", "path": "/Foo"}],
        )
        omissions = [{"operationId": "Op", "reason": "deferred"}]
        stdout, _, exit_code = _run_main(
            '{"method": "get", "path": "/Foo"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(len(data), 1)

    def test_head_request_does_not_match_get_operation(self):
        operations = _operations(
            [
                {"operationId": "Get", "method": "GET", "path": "/Foo"},
                {"operationId": "Head", "method": "HEAD", "path": "/Foo"},
            ],
        )
        omissions = [
            {"operationId": "Get", "reason": "deferred"},
            {"operationId": "Head", "reason": "deferred"},
        ]
        stdout, _, exit_code = _run_main(
            '{"method": "HEAD", "path": "/Foo"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["operationId"], "Head")

    def test_wrong_method_request_excluded(self):
        operations = _operations(
            [{"operationId": "Get", "method": "GET", "path": "/Foo"}],
        )
        omissions = [{"operationId": "Get", "reason": "deferred"}]
        stdout, _, exit_code = _run_main(
            '{"method": "POST", "path": "/Foo"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(data, [])


class TestEmbeddedTokens(unittest.TestCase):
    """Cover tokens embedded inside a path segment (mechanism 9)."""

    def test_stream_container_token(self):
        operations = _operations(
            [
                {
                    "operationId": "Stream",
                    "method": "GET",
                    "path": "/Audio/{itemId}/stream.{container}",
                }
            ],
        )
        omissions = [{"operationId": "Stream", "reason": "deferred"}]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Audio/abc/stream.mp3"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["operationId"], "Stream")

    def test_index_jpg_token(self):
        operations = _operations(
            [
                {
                    "operationId": "Tile",
                    "method": "GET",
                    "path": "/Videos/{itemId}/Tiles/{index}.jpg",
                }
            ],
        )
        omissions = [{"operationId": "Tile", "reason": "deferred"}]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Videos/abc/Tiles/42.jpg"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["operationId"], "Tile")

    def test_token_does_not_span_slash(self):
        operations = _operations(
            [
                {
                    "operationId": "Op",
                    "method": "GET",
                    "path": "/Foo/{name}",
                }
            ],
        )
        omissions = [{"operationId": "Op", "reason": "deferred"}]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Foo/a/b"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(data, [])


class TestStaticPrecedence(unittest.TestCase):
    """Cover the exact literal over parameter route rule (mechanism 10)."""

    def test_static_route_preferred_over_parameter(self):
        operations = _operations(
            [
                {
                    "operationId": "Counts",
                    "method": "GET",
                    "path": "/Items/Counts",
                },
                {
                    "operationId": "GetItem",
                    "method": "GET",
                    "path": "/Items/{itemId}",
                },
            ],
        )
        omissions = [
            {"operationId": "Counts", "reason": "deferred"},
            {"operationId": "GetItem", "reason": "deferred"},
        ]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Items/Counts"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["operationId"], "Counts")

    def test_parameter_route_matches_when_no_static_match(self):
        operations = _operations(
            [
                {
                    "operationId": "Counts",
                    "method": "GET",
                    "path": "/Items/Counts",
                },
                {
                    "operationId": "GetItem",
                    "method": "GET",
                    "path": "/Items/{itemId}",
                },
            ],
        )
        omissions = [
            {"operationId": "Counts", "reason": "deferred"},
            {"operationId": "GetItem", "reason": "deferred"},
        ]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Items/42"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["operationId"], "GetItem")


class TestAmbiguity(unittest.TestCase):
    """Cover the equal-specificity ambiguity failure (mechanism 10)."""

    def test_equal_specificity_parameter_routes_are_ambiguous(self):
        operations = _operations(
            [
                {
                    "operationId": "OpA",
                    "method": "GET",
                    "path": "/Foo/{a}/Bar",
                },
                {
                    "operationId": "OpB",
                    "method": "GET",
                    "path": "/Foo/{b}/Bar",
                },
            ],
        )
        omissions = []
        _, stderr, exit_code = _run_main(
            '{"method": "GET", "path": "/Foo/x/Bar"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertNotEqual(exit_code, 0)
        self.assertIn("ambiguous match", stderr)
        self.assertIn("OpA", stderr)
        self.assertIn("OpB", stderr)

    def test_more_literals_wins_with_equal_segment_counts(self):
        operations = _operations(
            [
                {
                    "operationId": "More",
                    "method": "GET",
                    "path": "/Foo/{a}/Bar",
                },
                {
                    "operationId": "Fewer",
                    "method": "GET",
                    "path": "/Foo/x/{b}",
                },
            ],
        )
        omissions = [
            {"operationId": "More", "reason": "deferred"},
            {"operationId": "Fewer", "reason": "deferred"},
        ]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Foo/x/Bar"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["operationId"], "More")


class TestDeduplication(unittest.TestCase):
    """Cover repeated request deduplication (mechanism 13)."""

    def test_duplicate_requests_produce_one_candidate(self):
        operations = _operations(
            [{"operationId": "Op", "method": "GET", "path": "/Foo"}],
        )
        omissions = [{"operationId": "Op", "reason": "deferred"}]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Foo"}\n'
            '{"method": "GET", "path": "/Foo"}\n'
            '{"method": "GET", "path": "/Foo"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(len(data), 1)


class TestOmissionFiltering(unittest.TestCase):
    """Cover omission intersection and observed filtering (mechanism 7, 12)."""

    def test_registered_operation_not_in_omissions_is_excluded(self):
        operations = _operations(
            [{"operationId": "Op", "method": "GET", "path": "/Foo"}],
        )
        omissions = []
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Foo"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(data, [])

    def test_omission_with_observed_is_excluded(self):
        operations = _operations(
            [{"operationId": "Op", "method": "GET", "path": "/Foo"}],
        )
        omissions = [
            {
                "operationId": "Op",
                "reason": "deferred",
                "observed": {
                    "evidence": "prior",
                    "issue": "issue-160",
                },
            }
        ]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Foo"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(data, [])

    def test_candidates_sorted_by_operation_id(self):
        operations = _operations(
            [
                {"operationId": "Zeta", "method": "GET", "path": "/Z"},
                {"operationId": "Alpha", "method": "GET", "path": "/A"},
                {"operationId": "Mid", "method": "GET", "path": "/M"},
            ],
        )
        omissions = [
            {"operationId": "Zeta", "reason": "deferred"},
            {"operationId": "Alpha", "reason": "deferred"},
            {"operationId": "Mid", "reason": "deferred"},
        ]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Z"}\n'
            '{"method": "GET", "path": "/A"}\n'
            '{"method": "GET", "path": "/M"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        ids = [entry["operationId"] for entry in data]
        self.assertEqual(ids, ["Alpha", "Mid", "Zeta"])

    def test_observed_evidence_and_issue_fields(self):
        operations = _operations(
            [{"operationId": "Op", "method": "GET", "path": "/Foo"}],
        )
        omissions = [{"operationId": "Op", "reason": "deferred"}]
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/Foo"}\n',
            operations,
            omissions,
            "capture 2026-08-15",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(data[0]["observed"]["evidence"], "capture 2026-08-15")
        self.assertEqual(data[0]["observed"]["issue"], "")


class TestEmptyResult(unittest.TestCase):
    """Cover zero exit on an empty result (mechanism 15)."""

    def test_empty_result_exits_zero(self):
        operations = _operations(
            [{"operationId": "Op", "method": "GET", "path": "/Foo"}],
        )
        omissions = []
        stdout, _, exit_code = _run_main(
            '{"method": "GET", "path": "/NoMatch"}\n',
            operations,
            omissions,
            "ev",
        )
        self.assertEqual(exit_code, 0)
        data = json.loads(stdout)
        self.assertEqual(data, [])


class TestSyntheticCase(unittest.TestCase):
    """Run the committed synthetic case and compare byte for byte."""

    def test_synthetic_case_output_matches_expected(self):
        root = os.path.join(os.path.dirname(__file__), "..", "testdata")
        requests_path = os.path.join(root, "requests.jsonl")
        operations_path = os.path.join(root, "required-operations.json")
        omissions_path = os.path.join(root, "route-omissions.json")
        expected_path = os.path.join(root, "expected.json")
        stdout = io.StringIO()
        original_stdout = sys.stdout
        sys.stdout = stdout
        exit_code = 0
        try:
            reconcile.main(
                [
                    "--requests",
                    requests_path,
                    "--operations",
                    operations_path,
                    "--omissions",
                    omissions_path,
                    "--evidence",
                    "synthetic capture 2026-08-15",
                ],
            )
        except SystemExit as exc:
            exit_code = exc.code
        finally:
            sys.stdout = original_stdout
        self.assertEqual(exit_code, 0)
        with open(expected_path, "r", encoding="utf-8") as handle:
            expected = handle.read()
        self.assertEqual(stdout.getvalue(), expected)


if __name__ == "__main__":
    unittest.main()
