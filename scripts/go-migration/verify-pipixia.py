#!/usr/bin/env python3
"""Verify the frozen Pipixia product oracle, sources and exact request inventory."""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import pathlib
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[2]
DIRECTORY = ROOT / "backend/internal/platform/pipixia/testdata"


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
    spec = importlib.util.spec_from_file_location("pipixia_producer", ROOT / "scripts/go-migration/produce-pipixia.py")
    producer = importlib.util.module_from_spec(spec); spec.loader.exec_module(producer)
    directory = args.directory.resolve()
    manifest = json.loads((directory / "manifest.json").read_text(encoding="utf-8"))
    require((manifest["baseline_commit"], manifest["baseline_tree"], manifest["php_version"], manifest["entry_json_flags"]) ==
            (producer.BASE, producer.TREE, "8.3.6", producer.ENTRY_FLAGS), "Wrong PHP baseline/runtime/entry boundary")
    require(set(manifest["files"]) == {"php-baseline.json"}, "Fixture inventory changed")
    for path, expected in manifest["files"].items():
        require(sha(directory / path) == expected, "Fixture integrity mismatch: " + path)
    require([entry["path"] for entry in manifest["source_files"]] == producer.SOURCES, "Source inventory changed")
    require([entry["path"] for entry in manifest["producer_files"]] == producer.PRODUCERS, "Producer inventory changed")
    require([entry["path"] for entry in manifest["input_files"]] == [str((DIRECTORY / "inputs.json").relative_to(ROOT))], "Input inventory changed")
    for entry in manifest["producer_files"] + manifest["input_files"]:
        require(sha(ROOT / entry["path"]) == entry["sha256"], "Input/producer integrity mismatch: " + entry["path"])
    if args.check_git_source:
        tree = subprocess.check_output(["git", "rev-parse", producer.BASE + "^{tree}"], cwd=ROOT, text=True).strip()
        require(tree == producer.TREE, "Pinned source tree changed")
        for entry in manifest["source_files"]:
            content = subprocess.check_output(["git", "show", producer.BASE + ":" + entry["path"]], cwd=ROOT)
            require(hashlib.sha256(content).hexdigest() == entry["sha256"], "Pinned source mismatch: " + entry["path"])
    expected_inputs = producer.input_document()
    require((DIRECTORY / "inputs.json").read_bytes() == producer.dump(expected_inputs).encode(), "Reviewed inputs differ from producer")
    document = json.loads((directory / "php-baseline.json").read_text(encoding="utf-8"))
    require({k: v for k, v in document.items() if k != "cases"} == {k: v for k, v in expected_inputs.items() if k != "cases"}, "Fixture provenance changed")
    cases = document["cases"]
    require(len(cases) == len(expected_inputs["cases"]) and len({entry["id"] for entry in cases}) == len(cases), "Duplicate/missing cases")
    requests = success = 0
    for case, source in zip(cases, expected_inputs["cases"], strict=True):
        require({k: v for k, v in case.items() if k != "expected"} == source, "Input changed: " + case["id"])
        observed = case["expected"]
        require(set(observed) == {"parser_response", "legacy_response", "normal", "debug", "http_requests", "clock_calls"}, "Observation fields changed")
        tape = case["input"]["http_responses"]
        require(len(tape) == len(observed["http_requests"]), "Unconsumed request tape: " + case["id"])
        for recorded, step in zip(observed["http_requests"], tape, strict=True):
            require(all(recorded[key] == step[key] for key in ("method", "url", "follow_redirects")), "Request sequence changed")
            require(1 <= recorded["timeout_seconds"] <= 20 and not recorded["follow_redirects"], "Request bounds changed")
            require("authorization" not in recorded["headers"], "Unexpected authentication")
            require("cookie" not in recorded["headers"] and recorded["headers"].get("accept") == "text/html,application/json" and recorded["headers"].get("referer") == "https://h5.pipix.com/", "Request headers escaped fixed anonymous policy")
        requests += len(tape)
        require(observed["normal"]["code"] == observed["debug"]["code"], "Public view status differs")
        for view in ("normal", "debug"):
            envelope = observed[view]
            require(envelope["schema_version"] == "1.0", "Schema version changed")
            if envelope["code"] == "OK":
                require(envelope["data"]["generated_at"] == producer.CLOCK, "Fixed public clock changed")
        success += observed["normal"]["code"] == "OK"
    require(manifest["fixed_clock"] == expected_inputs["fixed_clock"], "Manifest clock changed")
    require(manifest["parse_case_count"] == len(cases) and manifest["public_envelope_count"] == len(cases) * 2, "Manifest counts changed")
    print(json.dumps({"passed": True, "baseline": producer.BASE, "php_version": manifest["php_version"],
                      "cases": len(cases), "parse_cases": len(cases), "public_envelopes": 2 * len(cases),
                      "success_cases": success, "http_requests": requests, "git_source_checked": args.check_git_source}))


if __name__ == "__main__":
    main()
