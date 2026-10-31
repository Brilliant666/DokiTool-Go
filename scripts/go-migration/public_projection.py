"""Strictly compare Go exports with frozen PHP plus reviewed field differences.

This is test infrastructure. It never changes actual Go responses or invokes
the production projection, and it keeps JSON integers and decimals exact.
"""
from __future__ import annotations

import copy
from decimal import Decimal
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DOUYIN = "backend/testdata/php-douyin/cases.json"
INTEGERS = "backend/internal/platform/douyin/testdata/php-integer-boundaries.json"
MANIFEST = "backend/internal/testsupport/publicoracle/differences.json"


def read_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8"), parse_float=Decimal)


def same_json(left, right):
    if isinstance(left, dict) and isinstance(right, dict):
        return left.keys() == right.keys() and all(same_json(left[key], right[key]) for key in left)
    if isinstance(left, list) and isinstance(right, list):
        return len(left) == len(right) and all(same_json(a, b) for a, b in zip(left, right))
    numbers = (int, Decimal)
    if type(left) in numbers and type(right) in numbers:
        return left == right
    return type(left) is type(right) and left == right


def load_differences(root):
    document = read_json(root / MANIFEST)
    if set(document) != {"format_version", "differences"} or document["format_version"] != 1:
        raise ValueError("Invalid public projection manifest")
    entries = document["differences"]
    if not isinstance(entries, list) or not entries:
        raise ValueError("Missing public projection declarations")
    seen = set()
    required = {"fixture", "case_id", "view", "path", "operation", "before", "reason"}
    for entry in entries:
        if not isinstance(entry, dict) or not required <= entry.keys() or entry.keys() - required - {"after"}:
            raise ValueError("Invalid public projection declaration fields")
        if entry["fixture"] not in (DOUYIN, INTEGERS) or not isinstance(entry["case_id"], str) or not entry["case_id"]:
            raise ValueError("Invalid public projection fixture identity")
        if entry["view"] not in ("normal", "debug") or not isinstance(entry["reason"], str) or not entry["reason"]:
            raise ValueError("Invalid public projection view/reason")
        if not isinstance(entry["path"], str) or not entry["path"].startswith("/") or entry["before"] is None:
            raise ValueError("Invalid public projection pointer/before")
        if entry["operation"] not in ("remove", "replace"):
            raise ValueError("Invalid public projection operation")
        if (entry["operation"] == "replace") != ("after" in entry) or entry.get("after", True) is None:
            raise ValueError("Invalid public projection after value")
        key = tuple(entry[field] for field in ("fixture", "case_id", "view", "path"))
        if key in seen:
            raise ValueError("Duplicate public projection declaration")
        seen.add(key)
    return entries


def apply_difference(expected, entry):
    tokens = entry["path"][1:].split("/")
    current = expected
    for index, token in enumerate(tokens):
        if not token or "~" in token:
            raise ValueError("Unsupported public projection pointer")
        if index == len(tokens) - 1:
            if not isinstance(current, dict) or token not in current:
                raise ValueError("Missing public projection target: " + entry["path"])
            if not same_json(current[token], entry["before"]):
                raise ValueError("Public projection before mismatch: " + entry["path"])
            if entry["operation"] == "remove":
                del current[token]
            else:
                current[token] = copy.deepcopy(entry["after"])
            return
        if isinstance(current, dict) and token in current:
            current = current[token]
        elif isinstance(current, list) and token.isascii() and token.isdecimal() and str(int(token)) == token and int(token) < len(current):
            current = current[int(token)]
        else:
            raise ValueError("Missing public projection path: " + entry["path"])


def case_index(document):
    cases = document.get("cases")
    if not isinstance(cases, list):
        raise ValueError("Missing public projection case inventory")
    result = {}
    for entry in cases:
        case_id = entry.get("id")
        if not isinstance(case_id, str) or not case_id or case_id in result:
            raise ValueError("Invalid or duplicate public projection case")
        result[case_id] = entry
    return result


def verify_exports(exports, *, root=ROOT):
    """Validate complete real exports; return the consumed, exact differences."""
    if set(exports) != {DOUYIN, INTEGERS}:
        raise ValueError("Both affected fixture groups must be compared")
    entries = load_differences(root)
    used = set()
    responses = 0
    for fixture, export in exports.items():
        frozen = case_index(read_json(root / fixture))
        actual = case_index(read_json(export))
        if frozen.keys() != actual.keys():
            raise ValueError("Public projection export inventory differs: " + fixture)
        for case_id, php_case in frozen.items():
            for view in ("normal", "debug"):
                expected = copy.deepcopy(php_case["expected"][view])
                for index, entry in enumerate(entries):
                    if (entry["fixture"], entry["case_id"], entry["view"]) == (fixture, case_id, view):
                        apply_difference(expected, entry)
                        used.add(index)
                if view not in actual[case_id] or not same_json(expected, actual[case_id][view]):
                    raise ValueError(f"Undeclared public response difference: {fixture} {case_id}/{view}")
                responses += 1
    unused = [entry for index, entry in enumerate(entries) if index not in used]
    if unused:
        raise ValueError("Unused public projection declarations: " + repr(unused))
    return {"passed": True, "compared_responses": responses, "consumed": len(used), "differences": entries}
