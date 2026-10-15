#!/usr/bin/env python3
"""Route-omission reconciliation tool.

Diff captured client traffic against Deadfin route coverage.

The tool reads sanitized JSONL requests, a pinned operations manifest, and a
route-omissions list. It reports the omitted operations that the capture
requested, formatted as candidate observed blocks.

Usage:
    python3 reconcile.py \
        --requests requests.jsonl \
        --operations required-operations.json \
        --omissions route-omissions.json \
        --evidence 'synthetic capture 2026-08-15'
"""

import argparse
import json
import sys


def _error(message):
    """Print an error to stderr and exit nonzero."""
    sys.stderr.write("error: " + message + "\n")
    raise SystemExit(1)


def _parse_args(argv):
    """Parse the command-line arguments."""
    parser = argparse.ArgumentParser(
        description="Diff captured client traffic against Deadfin route coverage.",
    )
    parser.add_argument(
        "--requests",
        required=True,
        help="sanitized JSONL file with method and path fields per record",
    )
    parser.add_argument(
        "--operations",
        required=True,
        help="required-operations.json manifest (header plus operations array)",
    )
    parser.add_argument(
        "--omissions",
        required=True,
        help="route-omissions.json array keyed by operationId",
    )
    parser.add_argument(
        "--evidence",
        required=True,
        help="evidence string written into each observed block",
    )
    return parser.parse_args(argv)


def _load_json_file(path, kind):
    """Load and parse a JSON file. Return the parsed object."""
    try:
        with open(path, "r", encoding="utf-8") as handle:
            return json.load(handle)
    except FileNotFoundError:
        _error(kind + " file not found: " + path)
    except json.JSONDecodeError as exc:
        _error(kind + " file is not valid JSON: " + path + ": " + str(exc))


def _load_operations(path):
    """Load the operations manifest and return a list of operation dicts."""
    data = _load_json_file(path, "operations")
    if not isinstance(data, dict):
        _error("operations file must be a JSON object: " + path)
    operations = data.get("operations")
    if not isinstance(operations, list):
        _error("operations file must contain an operations array: " + path)
    parsed = []
    for index, entry in enumerate(operations):
        if not isinstance(entry, dict):
            _error(
                "operations entry " + str(index) + " is not an object: " + path,
            )
        operation_id = entry.get("operationId")
        method = entry.get("method")
        op_path = entry.get("path")
        if not isinstance(operation_id, str) or not operation_id:
            _error(
                "operations entry " + str(index) + " lacks operationId: " + path,
            )
        if not isinstance(method, str) or not method:
            _error(
                "operations entry " + str(index) + " lacks method: " + path,
            )
        if not isinstance(op_path, str) or not op_path:
            _error(
                "operations entry " + str(index) + " lacks path: " + path,
            )
        parsed.append(
            {
                "operationId": operation_id,
                "method": method.upper(),
                "path": op_path,
            }
        )
    return parsed


def _load_omissions(path):
    """Load the omissions array and return a dict keyed by operationId."""
    data = _load_json_file(path, "omissions")
    if not isinstance(data, list):
        _error("omissions file must be a JSON array: " + path)
    omissions = {}
    for index, entry in enumerate(data):
        if not isinstance(entry, dict):
            _error(
                "omissions entry " + str(index) + " is not an object: " + path,
            )
        operation_id = entry.get("operationId")
        if not isinstance(operation_id, str) or not operation_id:
            _error(
                "omissions entry " + str(index) + " lacks operationId: " + path,
            )
        omissions[operation_id] = entry
    return omissions


def _load_requests(path):
    """Load sanitized JSONL requests and return a list of record dicts."""
    try:
        with open(path, "r", encoding="utf-8") as handle:
            text = handle.read()
    except FileNotFoundError:
        _error("requests file not found: " + path)
    records = []
    for line_number, raw in enumerate(text.splitlines(), 1):
        if not raw.strip():
            continue
        try:
            record = json.loads(raw)
        except json.JSONDecodeError as exc:
            _error(
                path
                + ":"
                + str(line_number)
                + ": invalid JSON: "
                + str(exc),
            )
        if not isinstance(record, dict):
            _error(
                path
                + ":"
                + str(line_number)
                + ": record is not an object",
            )
        method = record.get("method")
        req_path = record.get("path")
        if not isinstance(method, str) or not method:
            _error(
                path
                + ":"
                + str(line_number)
                + ": record lacks a string method field",
            )
        if not isinstance(req_path, str) or not req_path:
            _error(
                path
                + ":"
                + str(line_number)
                + ": record lacks a string path field",
            )
        records.append(record)
    return records


def _strip_query_fragment(path):
    """Remove the query and fragment from a request path."""
    for separator in ("#", "?"):
        index = path.find(separator)
        if index != -1:
            path = path[:index]
    return path


def _compile_segment(template_segment):
    """Compile one template segment into parts and count its literal chars.

    Each {name} token becomes one token part. A token matches one nonempty
    run of characters inside the segment. This supports tokens embedded in a
    larger segment, such as stream.{container} or {index}.jpg.
    """
    parts = []
    literal_chars = 0
    index = 0
    length = len(template_segment)
    while index < length:
        start = template_segment.find("{", index)
        if start == -1:
            chunk = template_segment[index:]
            parts.append(("literal", chunk))
            literal_chars += len(chunk)
            break
        if start > index:
            chunk = template_segment[index:start]
            parts.append(("literal", chunk))
            literal_chars += len(chunk)
        end = template_segment.find("}", start)
        if end == -1:
            _error("route template has an unclosed token: " + template_segment)
        token_name = template_segment[start + 1 : end]
        if not token_name:
            _error("route template has an empty token name: " + template_segment)
        parts.append(("token", token_name))
        index = end + 1
    return parts, literal_chars


def _compile_template(template):
    """Compile a route template into per-segment parts and a literal count.

    The template is split on the slash. Each segment becomes a list of parts.
    A token matches exactly one nonempty segment, so the number of segments in
    the request path must equal the number of segments in the template.
    """
    segments = []
    total_literal = 0
    for template_segment in template.split("/"):
        parts, literal_chars = _compile_segment(template_segment)
        segments.append(parts)
        total_literal += literal_chars
    return {"segments": segments, "literals": total_literal}


def _match_segment_parts(parts, text):
    """Match one segment's parts against the segment text with backtracking.

    A literal must match exactly. A token matches one or more characters that
    remain in the segment.
    """
    if not parts:
        return text == ""
    kind, value = parts[0]
    rest = parts[1:]
    if kind == "literal":
        if not text.startswith(value):
            return False
        return _match_segment_parts(rest, text[len(value):])
    # token: match one or more characters, then try the rest.
    for end in range(1, len(text) + 1):
        if _match_segment_parts(rest, text[end:]):
            return True
    return False


def _match_template(compiled, path):
    """Match a compiled template against a full path. Return True or False."""
    template_segments = compiled["segments"]
    path_segments = path.split("/")
    if len(path_segments) != len(template_segments):
        return False
    for template_parts, path_segment in zip(template_segments, path_segments):
        if not _match_segment_parts(template_parts, path_segment):
            return False
    return True


def _match_request(request, operations):
    """Match a single request against the operations.

    Return the matched operation dict, or None if no operation matches. Fail
    nonzero on an ambiguous match between equal-specificity templates.
    """
    method = request["method"].upper()
    path = _strip_query_fragment(request["path"])
    matches = []
    for operation in operations:
        if operation["method"] != method:
            continue
        compiled = _compile_template(operation["path"])
        if _match_template(compiled, path):
            matches.append(operation)
    if not matches:
        return None
    # Prefer an exact literal route over a parameter route.
    exact = [op for op in matches if "{" not in op["path"]]
    if exact:
        if len(exact) > 1:
            _error(
                "ambiguous exact match for "
                + method
                + " "
                + path
                + ": "
                + ", ".join(sorted(op["operationId"] for op in exact)),
            )
        return exact[0]
    if len(matches) > 1:
        # Select the template with the most literal characters. If two
        # templates tie, the match is ambiguous.
        best = max(
            matches, key=lambda op: _compile_template(op["path"])["literals"]
        )
        best_literals = _compile_template(best["path"])["literals"]
        tied = [
            op
            for op in matches
            if _compile_template(op["path"])["literals"] == best_literals
        ]
        if len(tied) > 1:
            _error(
                "ambiguous match for "
                + method
                + " "
                + path
                + ": "
                + ", ".join(sorted(op["operationId"] for op in tied)),
            )
        return best
    return matches[0]


def _reconcile(requests, operations, omissions, evidence):
    """Reconcile requests against operations and omissions.

    Return a sorted list of candidate observed dicts.
    """
    matched = {}
    for request in requests:
        operation = _match_request(request, operations)
        if operation is None:
            continue
        matched[operation["operationId"]] = operation
    candidates = []
    for operation_id in sorted(matched):
        omission = omissions.get(operation_id)
        if omission is None:
            continue
        if "observed" in omission:
            continue
        operation = matched[operation_id]
        candidates.append(
            {
                "operationId": operation_id,
                "method": operation["method"],
                "path": operation["path"],
                "observed": {
                    "evidence": evidence,
                    "issue": "",
                },
            }
        )
    return candidates


def _load_inputs(args):
    """Load all three inputs and return (requests, operations, omissions)."""
    operations = _load_operations(args.operations)
    omissions = _load_omissions(args.omissions)
    requests = _load_requests(args.requests)
    return requests, operations, omissions


def main(argv=None):
    """Entry point for the reconciliation tool."""
    args = _parse_args(argv if argv is not None else sys.argv[1:])
    requests, operations, omissions = _load_inputs(args)
    candidates = _reconcile(requests, operations, omissions, args.evidence)
    json.dump(candidates, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
