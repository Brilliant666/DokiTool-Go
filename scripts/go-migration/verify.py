#!/usr/bin/env python3
"""Verify frozen oracle provenance and integrity without PHP or platform access."""
from __future__ import annotations

import argparse
import collections
import hashlib
import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASE = "665bd55134982b9fe107aa1cd00373ba8da8ff3c"
TREE = "93744d4d718c3299f82640d2888e93252106db70"


def sha(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def verify(directory: pathlib.Path, source_root: pathlib.Path | None = None) -> dict:
    manifest=json.loads((directory/"manifest.json").read_text())
    if manifest.get("format_version")!=1 or manifest.get("baseline_commit")!=BASE or manifest.get("baseline_tree")!=TREE:
        raise ValueError("Approved baseline version mismatch")
    for path,expected in manifest["files"].items():
        if pathlib.PurePosixPath(path).name!=path or sha(directory/path)!=expected:
            raise ValueError("Frozen data integrity mismatch: "+path)
    cases=json.loads((directory/"cases.json").read_text())["cases"]
    ids=[case["id"] for case in cases]
    if len(ids)!=len(set(ids)) or len(cases)!=manifest["case_count"]:
        raise ValueError("Case inventory mismatch")
    counts=dict(collections.Counter(case["operation"] for case in cases))
    if counts!=manifest["operation_counts"]: raise ValueError("Operation inventory mismatch")
    tickets=json.loads((directory/"tickets.json").read_text())["cases"]
    if len(tickets)!=manifest["ticket_case_count"] or len({c['id'] for c in tickets})!=len(tickets): raise ValueError("Ticket inventory mismatch")
    catalog=json.loads((directory/"catalog.json").read_text())
    if catalog["operation"]!="platform_catalog" or len(catalog["expected"]["data"]["platforms"])!=9: raise ValueError("PHP catalog inventory mismatch")
    if sum(name.startswith("design/") for name in ids)!=29: raise ValueError("All 29 approved design fixtures are required")
    for case in cases:
        if "input" not in case or "expected" not in case or not case.get("scenario"):
            raise ValueError("Incomplete input/output provenance: "+case["id"])
        if "now" in case["input"] and case["input"]["now"]!=manifest["fixed_clock"]["unix_seconds"]:
            raise ValueError("Unexpected case clock: "+case["id"])
        if case["operation"]=="validate_envelope" and case["expected"]["valid"]!=case["input"]["must_accept"]:
            raise ValueError("Invalid PHP validation outcome: "+case["id"])
    for entry in manifest["producer_files"]:
        if sha(ROOT/entry["path"])!=entry["sha256"]:
            raise ValueError("Producer changed; review and regenerate explicitly: "+entry["path"])
    if source_root:
        for entry in manifest["source_files"]:
            if sha(source_root/entry["path"])!=entry["sha256"]:
                raise ValueError("Approved PHP source mismatch: "+entry["path"])
    return {"passed":True,"cases":len(cases),"tickets":len(tickets),"catalog":1,"source_files":len(manifest["source_files"]),"source_bytes_checked":source_root is not None,"baseline_commit":BASE}


def verify_douyin(directory: pathlib.Path, source_root: pathlib.Path | None = None) -> dict:
    manifest = json.loads((directory / "manifest.json").read_text())
    if manifest.get("format_version") != 1 or manifest.get("baseline_commit") != BASE or manifest.get("baseline_tree") != TREE:
        raise ValueError("Approved Douyin baseline version mismatch")
    if set(manifest["files"]) != {"cases.json", "probe.json", "numeric.json"}:
        raise ValueError("Douyin frozen payload inventory mismatch")
    for name, expected in manifest["files"].items():
        if sha(directory / name) != expected:
            raise ValueError("Douyin frozen data integrity mismatch: " + name)
    groups = {}
    for name in manifest["files"]:
        document = json.loads((directory / name).read_text())
        cases = document["cases"]
        ids = [case["id"] for case in cases]
        if document.get("format_version") != 1 or len(ids) != len(set(ids)) or not all(ids):
            raise ValueError("Douyin case inventory mismatch: " + name)
        if any("input" not in case or "expected" not in case for case in cases):
            raise ValueError("Douyin input/output provenance missing: " + name)
        groups[name] = cases
    parse = groups["cases.json"]
    if len(parse) != manifest["parse_case_count"] or len(parse) * 2 != manifest["public_envelope_count"]:
        raise ValueError("Douyin Parse/public inventory mismatch")
    for case in parse:
        if not case.get("scenario") or case["input"]["now"] != manifest["fixed_clock"]["unix_seconds"]:
            raise ValueError("Douyin fixed input provenance mismatch: " + case["id"])
        if not {"normal", "debug", "http_requests", "probe_requests"}.issubset(case["expected"]):
            raise ValueError("Douyin product path outcome missing: " + case["id"])
    ids = {case["id"] for case in parse}
    for group in manifest["renewal_groups"]:
        if group["before"] not in ids or group["after"] not in ids:
            raise ValueError("Douyin renewal provenance missing")
    probe_counts = collections.Counter(case["operation"] for case in groups["probe.json"])
    if probe_counts != {"metadata": manifest["probe_metadata_case_count"], "probe": manifest["probe_transport_case_count"]}:
        raise ValueError("Douyin probe inventory mismatch")
    if len(groups["numeric.json"]) != manifest["numeric_round_case_count"]:
        raise ValueError("PHP rounding inventory mismatch")
    for entry in manifest["producer_files"]:
        if sha(ROOT / entry["path"]) != entry["sha256"]:
            raise ValueError("Douyin producer changed; review and regenerate explicitly: " + entry["path"])
    if source_root:
        for entry in manifest["source_files"]:
            if sha(source_root / entry["path"]) != entry["sha256"]:
                raise ValueError("Approved Douyin PHP source mismatch: " + entry["path"])
    return {"passed": True, "parse_cases": len(parse), "public_envelopes": len(parse) * 2,
            "probe_metadata_cases": probe_counts["metadata"], "probe_transport_cases": probe_counts["probe"],
            "numeric_round_cases": len(groups["numeric.json"]), "source_files": len(manifest["source_files"]),
            "source_bytes_checked": source_root is not None, "baseline_commit": BASE}


def verify_integers(directory: pathlib.Path, source_root: pathlib.Path | None = None) -> dict:
    path = directory / "php-integer-boundaries.json"
    fixture = json.loads(path.read_text())
    if fixture.get("format_version") != 1 or fixture.get("baseline_commit") != BASE or fixture.get("baseline_tree") != TREE:
        raise ValueError("Approved integer baseline version mismatch")
    if len(fixture["cases"]) != 13 or len(fixture["direct_cases"]) != 12 or len(fixture["source_sha256"]) != 12:
        raise ValueError("PHP integer boundary inventory mismatch")
    cases = fixture["cases"] + fixture["direct_cases"]
    if len({case["id"] for case in cases}) != len(cases) or any("input" not in case or "expected" not in case for case in cases):
        raise ValueError("PHP integer boundary input/output provenance mismatch")
    for name, expected in fixture["producer_sha256"].items():
        if sha(ROOT / name) != expected:
            raise ValueError("PHP integer producer changed: " + name)
    if source_root:
        for name, expected in fixture["source_sha256"].items():
            if sha(source_root / name) != expected:
                raise ValueError("Approved integer PHP source mismatch: " + name)
    # The Go oracle test additionally pins the complete fixture bytes by SHA-256.
    return {"passed": True, "parse_cases": len(fixture["cases"]), "public_envelopes": len(fixture["cases"]) * 2,
            "private_cases": len(fixture["direct_cases"]), "source_files": len(fixture["source_sha256"]),
            "source_bytes_checked": source_root is not None, "baseline_commit": BASE, "fixture_sha256": sha(path)}


def main() -> None:
    ap=argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--directory",type=pathlib.Path)
    group=ap.add_mutually_exclusive_group()
    group.add_argument("--douyin",action="store_true",help="Verify the separate full Douyin Parse/probe baseline")
    group.add_argument("--integers",action="store_true",help="Verify the separate PHP integer-boundary oracle")
    ap.add_argument("--source-root",type=pathlib.Path,help="Optional approved PHP archive/current unchanged PHP checkout")
    args=ap.parse_args()
    default = ROOT / "backend/internal/platform/douyin/testdata" if args.integers else ROOT / "backend/testdata" / ("php-douyin" if args.douyin else "php-baseline")
    directory = args.directory or default
    verifier = verify_integers if args.integers else verify_douyin if args.douyin else verify
    print(json.dumps(verifier(directory,args.source_root),sort_keys=True))


if __name__=="__main__": main()
