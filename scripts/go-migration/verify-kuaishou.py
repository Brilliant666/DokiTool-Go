#!/usr/bin/env python3
"""Verify frozen Kuaishou provenance, real PHP outputs and request inventory."""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import pathlib
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[2]
DIRECTORY = ROOT / "backend/internal/platform/kuaishou/testdata"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check-git-source", action="store_true")
    parser.add_argument("--directory", type=pathlib.Path, default=DIRECTORY)
    args = parser.parse_args()
    spec = importlib.util.spec_from_file_location("kuaishou_producer", ROOT / "scripts/go-migration/produce-kuaishou.py")
    producer = importlib.util.module_from_spec(spec); spec.loader.exec_module(producer)
    directory = args.directory.resolve()
    manifest = json.loads((directory / "manifest.json").read_text(encoding="utf-8"))
    require((manifest["baseline_commit"], manifest["baseline_tree"], manifest["php_version"], manifest["entry_json_flags"]) ==
            (producer.BASE, producer.TREE, "8.3.6", 480), "Wrong PHP provenance or entry boundary")
    require(set(manifest["files"]) == {"php-baseline.json"}, "Fixture inventory changed")
    for path, expected in manifest["files"].items():
        require(sha(directory / path) == expected, "Fixture integrity mismatch: " + path)
    require([x["path"] for x in manifest["source_files"]] == producer.SOURCES, "Source inventory changed")
    require([x["path"] for x in manifest["producer_files"]] == producer.PRODUCERS, "Producer inventory changed")
    require([x["path"] for x in manifest["input_files"]] == [str((DIRECTORY / "inputs.json").relative_to(ROOT))], "Input inventory changed")
    for entry in manifest["input_files"] + manifest["producer_files"]:
        require(sha(ROOT / entry["path"]) == entry["sha256"], "Producer/input hash mismatch: " + entry["path"])
    if args.check_git_source:
        tree = subprocess.check_output(["git", "rev-parse", producer.BASE + "^{tree}"], cwd=ROOT, text=True).strip()
        require(tree == producer.TREE, "Pinned source tree changed")
        for entry in manifest["source_files"]:
            data = subprocess.check_output(["git", "show", producer.BASE + ":" + entry["path"]], cwd=ROOT)
            require(hashlib.sha256(data).hexdigest() == entry["sha256"], "Pinned source hash mismatch: " + entry["path"])
    inputs = producer.input_document()
    require((DIRECTORY / "inputs.json").read_bytes() == producer.dump(inputs).encode(), "Reviewed inputs differ from generator")
    document = json.loads((directory / "php-baseline.json").read_text(encoding="utf-8"))
    require({k: v for k, v in document.items() if k != "cases"} == {k: v for k, v in inputs.items() if k != "cases"}, "Provenance changed")
    cases = document["cases"]
    require(len(cases) == len(inputs["cases"]) and len({x["id"] for x in cases}) == len(cases), "Missing or repeated cases")
    requests = success = 0
    for case, original in zip(cases, inputs["cases"], strict=True):
        require({k: v for k, v in case.items() if k != "expected"} == original, "Input changed: " + case["id"])
        expected = case["expected"]
        require(set(expected) == {"parser_response", "legacy_response", "normal", "debug", "http_requests"}, "Observation inventory changed")
        tape = case["input"]["http_responses"]
        require(len(tape) == len(expected["http_requests"]), "Unconsumed HTTP tape")
        for request, step in zip(expected["http_requests"], tape, strict=True):
            require(all(request[k] == step[k] for k in ("method", "url", "follow_redirects")), "HTTP request sequence changed")
            require(request["timeout_seconds"] == 20 and request["max_redirects"] == 5 and request["follow_redirects"], "Original bounded request options changed")
            require(request["auto_referer"] == (request["method"] == "GET"), "Automatic Referer policy changed")
            headers = request["headers"]
            require("authorization" not in headers and "user-agent" in headers, "Unexpected request authentication")
            if request["method"] == "GET":
                require(headers.get("cookie") == "", "Original empty configured Cookie changed")
            else:
                require("cookie" not in headers, "HEAD acquired a Cookie")
        requests += len(tape)
        require(expected["normal"]["code"] == expected["debug"]["code"], "Public views disagree")
        for view in ("normal", "debug"):
            envelope = expected[view]
            require(envelope["schema_version"] == "1.0", "Schema version changed")
            if envelope["code"] == "OK":
                require(envelope["data"]["generated_at"] == producer.CLOCK, "Fixed public clock changed")
        success += expected["normal"]["code"] == "OK"
    require(manifest["fixed_clock"] == inputs["fixed_clock"], "Manifest clock changed")
    require(manifest["parse_case_count"] == len(cases) and manifest["public_envelope_count"] == 2 * len(cases), "Manifest counts changed")
    print(json.dumps({"passed": True, "baseline": producer.BASE, "php_version": manifest["php_version"],
                      "cases": len(cases), "parse_cases": len(cases), "public_envelopes": 2 * len(cases),
                      "success_cases": success, "http_requests": requests, "git_source_checked": args.check_git_source}))


if __name__ == "__main__":
    main()
