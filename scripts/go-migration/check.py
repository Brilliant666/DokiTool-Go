#!/usr/bin/env python3
"""Run the Go migration's offline gates without a PHP runtime or platform access."""
from __future__ import annotations

import argparse
import collections
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from public_projection import DOUYIN, INTEGERS, case_index, read_json, same_json, verify_exports

ROOT = Path(__file__).resolve().parents[2]
BILIBILI = ROOT / "backend/internal/platform/bilibili/testdata/php-baseline.json"
WEIBO = ROOT / "backend/internal/platform/weibo/testdata/php-signed-baseline.json"
TOUTIAO = ROOT / "backend/internal/platform/toutiao/testdata/php-baseline.json"
PLATFORM_ORACLES = {
    "weibo": (WEIBO, "verify-weibo-signed.py"),
    "toutiao": (TOUTIAO, "verify-toutiao.py"),
    "xiaohongshu_wire": (ROOT / "backend/internal/platform/xiaohongshu/testdata/php-wire-baseline.json", "verify-xiaohongshu-wire.py"),
    **{name: (ROOT / "backend/internal/platform" / name / "testdata/php-baseline.json", f"verify-{name}.py")
       for name in ("kuaishou", "pipixia", "pipigx", "zuiyou", "xiaohongshu")},
}


def run(command, *, cwd=ROOT, env=None):
    result = subprocess.run(command, cwd=cwd, env=env, text=True, capture_output=True)
    if result.returncode:
        # Inputs in this gate are committed synthetic fixtures only.
        raise RuntimeError(f"{' '.join(map(str, command))} failed\n{result.stdout[-20000:]}\n{result.stderr[-8000:]}")
    return result.stdout


def reported_check(command, report):
    """Retain a validator's failed evidence and continue other independent gates."""
    process = subprocess.run(command, cwd=ROOT, text=True, capture_output=True)
    if process.returncode not in (0, 1) or not report.is_file():
        raise RuntimeError(f"Validation did not produce its report\n{process.stdout[-8000:]}\n{process.stderr[-8000:]}")
    return json.loads(report.read_text())


def legacy_provenance(check_source):
    """Run the original verifiers against their own PHP revision, not newer main."""
    verify = [sys.executable, "-X", "utf8", str(ROOT / "scripts/go-migration/verify.py")]
    with tempfile.TemporaryDirectory(prefix="dokitool-old-php-") as directory:
        if check_source:
            common, douyin = [json.loads((ROOT / "backend/testdata" / group / "manifest.json").read_text(encoding="utf-8"))
                              for group in ("php-baseline", "php-douyin")]
            integers = json.loads((ROOT / "backend/internal/platform/douyin/testdata/php-integer-boundaries.json").read_text(encoding="utf-8"))
            commit = common["baseline_commit"]
            if not commit == douyin["baseline_commit"] == integers["baseline_commit"]:
                raise ValueError("Legacy fixture groups must refer to the same PHP commit")
            paths = {entry["path"] for manifest in (common, douyin) for entry in manifest["source_files"]} | set(integers["source_sha256"])
            archive = Path(directory)
            for name in sorted(paths):
                path = (archive / name).resolve()
                if not path.is_relative_to(archive.resolve()):
                    raise ValueError("Invalid frozen source path: " + name)
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(subprocess.check_output(["git", "cat-file", "blob", f"{commit}:{name}"], cwd=ROOT))
            verify += ["--source-root", str(archive)]
        return [json.loads(run(verify + flags)) for flags in ([], ["--douyin"], ["--integers"])]


def verify_platform_exports(path, frozen_path, name):
    """Compare every new platform view without a production-code oracle."""
    frozen, actual = case_index(read_json(frozen_path)), case_index(read_json(path))
    if frozen.keys() != actual.keys():
        raise ValueError(f"Actual Go {name} export inventory mismatch")
    for case_id, case in frozen.items():
        for view in ("normal", "debug"):
            if view not in actual[case_id] or not same_json(case["expected"][view], actual[case_id][view]):
                raise ValueError(f"Undeclared {name} public response difference: {case_id}/{view}")
    return {"passed": True, "cases": len(frozen), "public_envelopes": 2 * len(frozen), "declared_differences": 0}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable")
    parser.add_argument("--race", action="store_true")
    parser.add_argument("--check-php-source", action="store_true", help="Verify both versions' sources from the pinned Git objects, without executing PHP")
    parser.add_argument("--report", type=Path)
    parser.add_argument("--frontend", action="store_true", help="Check all actual Go outputs and the existing four Douyin plus four Weibo renewal tests (requires frontend npm dependencies)")
    parser.add_argument("--node", default="node", help="Node executable for --frontend")
    args = parser.parse_args()
    provenance, douyin_provenance, integer_provenance = legacy_provenance(args.check_php_source)
    verify = [sys.executable, "-X", "utf8", str(ROOT / "scripts/go-migration/verify-php-25beaee.py")]
    if args.check_php_source:
        verify.append("--check-git-source")
    php25_provenance = json.loads(run(verify))
    verify = [sys.executable, "-X", "utf8", str(ROOT / "scripts/go-migration/verify-bilibili.py")]
    if args.check_php_source:
        verify.append("--check-git-source")
    bilibili_provenance = json.loads(run(verify))
    platform_provenance = {}
    for name, (_, script) in PLATFORM_ORACLES.items():
        command = [sys.executable, "-X", "utf8", str(ROOT / "scripts/go-migration" / script)]
        if args.check_php_source:
            command.append("--check-git-source")
        platform_provenance[name] = json.loads(run(command))
    with tempfile.TemporaryDirectory(prefix="dokitool-go-check-") as directory:
        temp = Path(directory)
        outputs = temp / "actual-go-responses.json"
        douyin_outputs = temp / "actual-go-douyin.json"
        integer_outputs = temp / "actual-go-integers.json"
        php25_outputs = temp / "actual-go-php25.json"
        bilibili_outputs = temp / "actual-go-bilibili.json"
        platform_outputs = {name: temp / f"actual-go-{name}.json" for name in PLATFORM_ORACLES}
        environment = dict(os.environ, DOKITOOL_GO_BASELINE_OUTPUT=str(outputs), DOKITOOL_GO_DOUYIN_OUTPUT=str(douyin_outputs),
                           DOKITOOL_GO_INTEGER_OUTPUT=str(integer_outputs), DOKITOOL_GO_PHP25_OUTPUT=str(php25_outputs),
                           DOKITOOL_GO_BILIBILI_OUTPUT=str(bilibili_outputs))
        environment.update({f"DOKITOOL_GO_{name.upper()}_OUTPUT": str(path) for name, path in platform_outputs.items()})
        command = [args.go, "test", "-json", "-count=1"]
        if args.race:
            command.append("-race")
        command.append("./...")
        events = [json.loads(line) for line in run(command, cwd=ROOT / "backend", env=environment).splitlines() if line]
        failures = [event for event in events if event.get("Action") == "fail"]
        if failures:
            raise RuntimeError("Go reported a failed test/package")
        run([args.go, "vet", "./..."], cwd=ROOT / "backend")
        actual = json.loads(outputs.read_text())
        labels = [f"shared/{index}" for index in range(len(actual))]
        douyin = json.loads(douyin_outputs.read_text())
        if len(douyin["cases"]) != douyin_provenance["parse_cases"]:
            raise RuntimeError("Actual Go Douyin export inventory mismatch")
        integers = json.loads(integer_outputs.read_text())
        if len(integers["cases"]) != integer_provenance["parse_cases"]:
            raise RuntimeError("Actual Go integer export inventory mismatch")
        intentional_differences = verify_exports({DOUYIN: douyin_outputs, INTEGERS: integer_outputs})
        douyin["cases"].extend(integers["cases"])
        php25 = json.loads(php25_outputs.read_text(encoding="utf-8"))
        if len(php25["cases"]) != php25_provenance["cases"]:
            raise RuntimeError("Actual Go PHP 25beaee export inventory mismatch")
        douyin["cases"].extend(php25["cases"])
        bilibili_comparison = verify_platform_exports(bilibili_outputs, BILIBILI, "Bilibili")
        bilibili = json.loads(bilibili_outputs.read_text(encoding="utf-8"))
        if len(bilibili["cases"]) != bilibili_provenance["parse_cases"]:
            raise RuntimeError("Actual Go Bilibili provenance count mismatch")
        douyin["cases"].extend(bilibili["cases"])
        platform_comparisons = {}
        for name, (frozen_path, _) in PLATFORM_ORACLES.items():
            path = platform_outputs[name]
            platform_comparisons[name] = verify_platform_exports(path, frozen_path, name)
            # Exact numeric comparison already completed above. Aggregate the
            # original JSON values like the existing groups; Decimal objects
            # from the comparison reader are not a JSON serializer input.
            exported = json.loads(path.read_text(encoding="utf-8"))
            if len(exported["cases"]) != platform_provenance[name]["parse_cases"]:
                raise RuntimeError(f"Actual Go {name} provenance count mismatch")
            douyin["cases"].extend(exported["cases"])
        douyin_outputs.write_text(json.dumps(douyin, ensure_ascii=False) + "\n")
        for case in douyin["cases"]:
            actual.extend([case["normal"], case["debug"]])
            labels.extend([case["id"] + "/normal", case["id"] + "/debug"])
        outputs.write_text(json.dumps(actual, ensure_ascii=False) + "\n")
        schema_report = temp / "independent.json"
        independent = reported_check([sys.executable, str(ROOT / "scripts/validate-unified-schema.py"),
                                      str(outputs), "--output", str(schema_report)], schema_report)
        passed = [event for event in events if event.get("Action") == "pass" and "Test" in event]
        by_package = collections.defaultdict(set)
        for event in passed:
            by_package[event["Package"]].add(event["Test"])
        leaves = sum(sum(not any(other.startswith(name+"/") for other in names) for name in names) for names in by_package.values())
        result = {
            "scope": "Offline frozen PHP inputs through actual Go code; no real platform or browser evidence",
            "passed": independent["total"] == independent["passed"],
            "toolchain": run([args.go, "version"], cwd=ROOT / "backend").strip(),
            "php_baseline": provenance,
            "php_douyin_baseline": douyin_provenance,
            "php_integer_baseline": integer_provenance,
            "php_25beaee_baseline": php25_provenance,
            "php_bilibili_baseline": bilibili_provenance,
            "bilibili_exact_public_comparison": bilibili_comparison,
            "php_weibo_signed_baseline": platform_provenance["weibo"],
            "weibo_signed_exact_public_comparison": platform_comparisons["weibo"],
            "php_toutiao_baseline": platform_provenance["toutiao"],
            "toutiao_exact_public_comparison": platform_comparisons["toutiao"],
            "intentional_public_differences": intentional_differences,
            "go": {"leaf_scenarios": leaves, "passed_test_events": len(passed), "race": args.race, "vet": True},
            "independent_responses": {"total": independent["total"], "passed": independent["passed"],
                                      "failures": [{"id": labels[entry["index"]], "reason": entry["reason"]}
                                                   for entry in independent["checks"] if not entry["passed"]]},
        }
        for name in ("kuaishou", "pipixia", "pipigx", "zuiyou", "xiaohongshu", "xiaohongshu_wire"):
            result[f"php_{name}_baseline"] = platform_provenance[name]
            result[f"{name}_exact_public_comparison"] = platform_comparisons[name]
        if args.frontend:
            frontend_report = temp / "frontend.json"
            frontend = reported_check([args.node, str(ROOT / "frontend/scripts/test-go-media-integration.mjs"),
                                       str(douyin_outputs), str(frontend_report)], frontend_report)
            result["frontend_go_integration"] = frontend
            result["passed"] = result["passed"] and frontend["passed"]
        if args.report:
            args.report.parent.mkdir(parents=True, exist_ok=True)
            args.report.write_text(json.dumps(result, ensure_ascii=False, indent=2)+"\n")
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0 if result["passed"] else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, RuntimeError) as error:
        print(error, file=sys.stderr)
        raise SystemExit(1)
