#!/usr/bin/env python3
"""Verify frozen XHS provenance and request inventory without invoking PHP."""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
DIRECTORY = ROOT / "backend/internal/platform/xiaohongshu/testdata"


def require(condition, message):
    if not condition: raise ValueError(message)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, default=DIRECTORY)
    parser.add_argument("--check-git-source", action="store_true")
    args = parser.parse_args()
    spec = importlib.util.spec_from_file_location("xhs_producer", ROOT / "scripts/go-migration/produce-xiaohongshu.py")
    producer = importlib.util.module_from_spec(spec); spec.loader.exec_module(producer)
    directory = args.directory.resolve()
    manifest = json.loads((directory / "manifest.json").read_text())
    require((manifest["baseline_commit"], manifest["baseline_tree"], manifest["php_version"], manifest["entry_json_flags"]) ==
            (producer.BASE, producer.TREE, "8.3.6", producer.ENTRY_FLAGS), "PHP source/runtime boundary changed")
    require(set(manifest["files"]) == {"php-baseline.json"}, "Fixture inventory changed")
    for name, digest in manifest["files"].items():
        require(hashlib.sha256((directory / name).read_bytes()).hexdigest() == digest, "Expected output hash mismatch")
    require([e["path"] for e in manifest["source_files"]] == producer.SOURCES, "Frozen source inventory changed")
    require([e["path"] for e in manifest["producer_files"]] == producer.PRODUCERS, "Producer inventory changed")
    require([e["path"] for e in manifest["input_files"]] == [str((DIRECTORY / "inputs.json").relative_to(ROOT))], "Input inventory changed")
    for entry in manifest["input_files"] + manifest["producer_files"]:
        require(hashlib.sha256((ROOT / entry["path"]).read_bytes()).hexdigest() == entry["sha256"], "Producer/input integrity mismatch")
    if args.check_git_source:
        require(subprocess.check_output(["git", "rev-parse", producer.BASE + "^{tree}"], cwd=ROOT, text=True).strip() == producer.TREE, "Pinned source tree mismatch")
        for entry in manifest["source_files"]:
            content = subprocess.check_output(["git", "show", producer.BASE + ":" + entry["path"]], cwd=ROOT)
            require(hashlib.sha256(content).hexdigest() == entry["sha256"], "Frozen PHP source hash mismatch")
    reviewed = producer.input_document()
    require((DIRECTORY / "inputs.json").read_bytes() == producer.dump(reviewed).encode(), "Reviewed input changed")
    actual = json.loads((directory / "php-baseline.json").read_text())
    require({k:v for k,v in actual.items() if k != "cases"} == {k:v for k,v in reviewed.items() if k != "cases"}, "Fixture provenance changed")
    cases = actual["cases"]
    require(len(cases) == len(reviewed["cases"]) == len({c["id"] for c in cases}), "Missing/duplicate cases")
    require(manifest["parse_case_count"] == len(cases) and manifest["public_envelope_count"] == len(cases)*2, "Manifest count mismatch")
    requests = probes = success = 0
    for case, source in zip(cases, reviewed["cases"], strict=True):
        require({k:v for k,v in case.items() if k != "expected"} == source, "Input differs")
        observed = case["expected"]
        require(set(observed) == {"legacy_response", "normal", "debug", "http_requests"}, "Observation fields changed")
        tape = case["input"]["http_responses"]
        require(len(tape) == len(observed["http_requests"]), "Request tape not consumed")
        for recorded, step in zip(observed["http_requests"], tape, strict=True):
            require(all(recorded[k] == step[k] for k in ("method", "url", "range")), "Request order/target/range mismatch")
            require(recorded["method"] == "GET" and not recorded["follow_redirects"] and recorded["body"] == "", "Request protocol changed")
            require(1 <= recorded["timeout_ms"] <= (6000 if recorded["range"] else 10000), "Request budget missing")
            require("cookie" not in recorded["headers"] and "authorization" not in recorded["headers"], "Unexpected credentials")
            probes += bool(recorded["range"])
        requests += len(tape)
        require(observed["normal"]["code"] == observed["debug"]["code"], "Normal/debug status differs")
        for view in ("normal", "debug"):
            require(observed[view]["schema_version"] == "1.0", "Schema version differs")
            if observed[view]["code"] == "OK":
                require(observed[view]["data"]["generated_at"] == producer.CLOCK, "Fixed clock changed")
        success += observed["normal"]["code"] == "OK"
    print(json.dumps({"passed": True, "baseline": producer.BASE, "php_version": "8.3.6", "cases": len(cases),
                      "parse_cases": len(cases), "public_envelopes": len(cases)*2, "success_cases": success,
                      "http_requests": requests, "media_range_requests": probes, "git_source_checked": args.check_git_source}))


if __name__ == "__main__": main()
