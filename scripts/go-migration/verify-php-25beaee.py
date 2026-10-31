#!/usr/bin/env python3
"""Verify the separate PHP 25beaee oracle without PHP, Go or media requests."""
from __future__ import annotations

import argparse
import base64
import hashlib
import importlib.util
import json
import pathlib
import subprocess

ROOT = pathlib.Path(__file__).resolve().parents[2]
DIRECTORY = ROOT / "backend/testdata/php-25beaee"
BASE = "25beaee107f8c1a208ced9deb3c637068445e316"
TREE = "e4b9d40f5e6c12a86fff629c105553886b434dcd"
OLD = "665bd55134982b9fe107aa1cd00373ba8da8ff3c"


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--check-git-source", action="store_true", help="Verify pinned Git object bytes, independent of checkout line endings")
    args = ap.parse_args()
    manifest = json.loads((DIRECTORY / "manifest.json").read_text(encoding="utf-8"))
    if (manifest["baseline_commit"], manifest["baseline_tree"], manifest["previous_commit"], manifest["php_version"]) != (BASE, TREE, OLD, "8.3.6"):
        raise ValueError("Unexpected versioned baseline or comparison runtime")
    if set(manifest["files"]) != {"cases.json", "differences.json"}:
        raise ValueError("Fixture inventory changed")
    for path, expected in manifest["files"].items():
        if sha(DIRECTORY / path) != expected:
            raise ValueError("Fixture integrity mismatch: " + path)
    for entry in manifest["producer_files"] + manifest["reused"]:
        if sha(ROOT / entry["path"]) != entry["sha256"]:
            raise ValueError("Producer/reused fixture integrity mismatch: " + entry["path"])
    if args.check_git_source:
        for entry in manifest["source_files"]:
            for commit, field in ((BASE, "sha256"), (OLD, "previous_sha256")):
                data = subprocess.check_output(["git", "show", f"{commit}:{entry['path']}"], cwd=ROOT)
                if hashlib.sha256(data).hexdigest() != entry[field]:
                    raise ValueError("Pinned source mismatch: " + entry["path"])
    fixture = json.loads((DIRECTORY / "cases.json").read_text(encoding="utf-8"))
    cases = fixture["cases"]
    bodies = fixture["body_base64_by_sha256"]
    used = {step['response']['body_sha256'] for case in cases for version in ('expected', 'previous_expected')
            for step in case[version]['transport'] if step['response'] is not None}
    if used != set(bodies) or any(hashlib.sha256(base64.b64decode(body, validate=True)).hexdigest() != key for key, body in bodies.items()):
        raise ValueError('Missing, unused or corrupt frozen transport body')
    changes = json.loads((DIRECTORY / "differences.json").read_text(encoding="utf-8"))
    spec = importlib.util.spec_from_file_location("php25_producer", ROOT / "scripts/go-migration/produce-php-25beaee.py")
    producer = importlib.util.module_from_spec(spec); spec.loader.exec_module(producer)
    expected_ids = [entry["id"] for entry in producer.specs()]
    if len(cases) != 12 or [case["id"] for case in cases] != expected_ids or len(set(expected_ids)) != len(cases):
        raise ValueError("Product-path inventory differs")
    if [entry["id"] for entry in changes["cases"]] != expected_ids or changes["reused"] != manifest["reused"]:
        raise ValueError("Difference/reuse inventory differs")
    changed = 0
    for case, change in zip(cases, changes["cases"], strict=True):
        if case["input"]["now"] != manifest["fixed_clock"]["unix_seconds"]:
            raise ValueError("Clock changed: " + case["id"])
        fields = producer.differences(case["previous_expected"], case["expected"])
        if fields != change["fields"] or bool(fields) != change["changed"]:
            raise ValueError("Difference summary mismatch: " + case["id"])
        changed += bool(fields)
        for view in ("normal", "debug"):
            if case["expected"][view]["data"]["generated_at"] != manifest["fixed_clock"]["rfc3339"]:
                raise ValueError("Public output clock differs: " + case["id"])
    if changed != manifest["changed_case_count"] or changed != 6 or manifest["public_envelope_count"] != 24:
        raise ValueError("Changed-result inventory differs")
    print(json.dumps({"passed": True, "baseline": BASE, "runtime": manifest["php_version"], "cases": 12,
                      "public_envelopes": 24, "changed_cases": changed, "reused_groups": len(manifest["reused"]),
                      "git_source_checked": args.check_git_source, "known_contract_conflicts": manifest["known_contract_conflicts"]}))


if __name__ == "__main__":
    main()
