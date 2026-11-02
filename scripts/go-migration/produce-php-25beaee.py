#!/usr/bin/env python3
"""Versioned PHP 25beaee oracle; preserve 665bd55 fixtures and their producers."""
from __future__ import annotations

import argparse
import base64
import hashlib
import io
import json
import pathlib
import subprocess
import tarfile
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASE = "25beaee107f8c1a208ced9deb3c637068445e316"
TREE = "e4b9d40f5e6c12a86fff629c105553886b434dcd"
OLD = "665bd55134982b9fe107aa1cd00373ba8da8ff3c"
OLD_TREE = "93744d4d718c3299f82640d2888e93252106db70"
NOW = 1793361600
DISABLED = "curl_init,curl_setopt_array,curl_setopt,curl_exec,curl_getinfo,curl_close,curl_error,curl_errno,get_headers"
HERE = "scripts/go-migration/produce-php-25beaee"


def dump(value):
    return json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_json(path, value):
    path.write_bytes(dump(value).encode('utf-8'))  # Git canonical LF on every OS.


def require(condition, message):
    if not condition:
        raise ValueError(message)


def inputs(cases):
    return [{k: v for k, v in case.items() if k not in ('expected', 'php_error')} for case in cases]


def archive(commit, expected_tree, target):
    actual = subprocess.check_output(["git", "rev-parse", commit + "^{tree}"], cwd=ROOT, text=True).strip()
    if actual != expected_tree:
        raise ValueError("Pinned PHP tree mismatch: " + commit)
    data = subprocess.check_output(["git", "archive", commit], cwd=ROOT)
    with tarfile.open(fileobj=io.BytesIO(data)) as stream:
        for member in stream.getmembers():
            if not (member.isfile() or member.isdir()) or not (target / member.name).resolve().is_relative_to(target.resolve()):
                raise ValueError("Unsafe archive entry: " + member.name)
        stream.extractall(target)


def differences(before, after, path="$"):
    if isinstance(before, dict) and isinstance(after, dict):
        return [p for k in sorted(before.keys() | after.keys()) for p in (
            differences(before[k], after[k], path + "." + k) if k in before and k in after else [path + "." + k])]
    if isinstance(before, list) and isinstance(after, list) and len(before) == len(after):
        return [p for i, (a, b) in enumerate(zip(before, after)) for p in differences(a, b, f"{path}[{i}]")]
    if type(before) is type(after) and before == after:
        return []
    if type(before) in (int, float) and type(after) in (int, float) and before == after:
        return []
    return [path]


def specs():
    return [
        {"id": "probe/mp4-known", "mime": "video/mp4"},
        {"id": "probe/quicktime-known", "mime": "video/quicktime"},
        {"id": "probe/quicktime-case-parameters", "mime": "Video/QuickTime; charset=binary"},
        {"id": "probe/html-rejected", "mime": "text/html"},
        {"id": "probe/new-cdn-mp4", "mime": "video/mp4", "destination": "https://edge-a.example.net/original.mov"},
        {"id": "probe/new-cdn-quicktime", "mime": "video/quicktime", "destination": "https://edge-a.example.net/original.mov"},
        {"id": "probe/later-new-cdn", "mime": "video/mp4", "destination": "https://edge-b.example.org/original.mp4", "later": True},
        {"id": "probe/http-location-upgraded", "mime": "video/mp4", "first_location": "http://edge-a.example.net/original.mp4"},
        {"id": "probe/unsafe-first", "mime": "video/mp4", "first_location": "https://127.0.0.1/private"},
        {"id": "probe/unsafe-later", "mime": "video/mp4", "destination": "https://127.0.0.1/private", "later": True},
        {"id": "probe/unavailable-later", "mime": "video/mp4", "destination": "https://v7-test.douyinvod.com/unavailable.mp4", "later": True, "unavailable": True},
        {"id": "probe/unauthorized-initial-cdn", "mime": "video/mp4", "entry": "https://edge-a.example.net/original.mp4"},
    ]


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--php", required=True, type=pathlib.Path)
    ap.add_argument("--output", type=pathlib.Path, default=ROOT / "backend/testdata/php-25beaee")
    args = ap.parse_args()
    php = str(args.php.resolve())
    runtime = subprocess.check_output([php, "-r", "echo PHP_VERSION;"], text=True).strip()
    require(runtime == "8.3.6", "Both code baselines require PHP 8.3.6; a different round() runtime is a separate comparison")

    def run(script, source, payload=None):
        cmd = [php, "-d", "disable_functions=" + DISABLED, "-d", "allow_url_fopen=0", str(script), str(source)]
        proc = subprocess.run(cmd, input=dump(payload or {}), text=True, encoding="utf-8", capture_output=True)
        if proc.returncode or proc.stderr.strip():
            raise RuntimeError("PHP oracle failed: " + proc.stderr + proc.stdout[:2000])
        return json.loads(proc.stdout)

    with tempfile.TemporaryDirectory(prefix="dokitool-php25-") as folder:
        temp = pathlib.Path(folder)
        previous, current = temp / "old", temp / "new"
        archive(OLD, OLD_TREE, previous); archive(BASE, TREE, current)
        payload = {"now": NOW, "cases": specs()}
        before = run(ROOT / (HERE + ".php"), previous, payload)
        after = run(ROOT / (HERE + ".php"), current, payload)
        # Many traces request the same 64 KiB prefix of synthetic mdat padding.
        # Keep each distinct response body once, losslessly, keyed by its digest.
        bodies = {}
        for document in (before, after):
            for case in document["cases"]:
                for step in case["expected"]["transport"]:
                    if step["response"] is not None:
                        body = step["response"].pop("body_base64")
                        key = hashlib.sha256(base64.b64decode(body, validate=True)).hexdigest()
                        bodies[key] = body
                        step["response"]["body_sha256"] = key
        after["body_base64_by_sha256"] = bodies
        changes = []
        reasons = {
            'probe/quicktime-known': 'QuickTime MIME now permits inspection of a valid ftyp/moov; measured complete original becomes default.',
            'probe/quicktime-case-parameters': 'Parameterized mixed-case QuickTime is normalized before the same bounded container check.',
            'probe/new-cdn-mp4': 'A trusted play entry redirects to a previously unlisted public CDN; the old host gate discarded this original.',
            'probe/new-cdn-quicktime': 'Both the new public CDN and its valid QuickTime content type must pass to measure the original.',
            'probe/later-new-cdn': 'A later moov Range can move to another public CDN; the successful final URL and measured facts are retained.',
            'probe/http-location-upgraded': 'An HTTP Location is upgraded before connecting to its authorized public delivery host, with no plaintext request.',
        }
        for old, new in zip(before["cases"], after["cases"], strict=True):
            require(old["id"] == new["id"] and old["input"] == new["input"], "Input changed between archives")
            new["previous_expected"] = old["expected"]
            paths = differences(old["expected"], new["expected"])
            require(bool(paths) == (new["id"] in reasons), 'Unexplained behavior change: ' + new["id"])
            changes.append({"id": new["id"], "changed": bool(paths), "fields": paths,
                            "reason": reasons.get(new["id"], 'Unchanged safety or supported-container behavior')})

        # Re-execute the established fixed inputs against the new PHP archive.
        # Reference original bytes only after every observation still agrees.
        references = []
        def reuse(path, observed_cases):
            stored = json.loads((ROOT / path).read_text(encoding="utf-8"))
            original = stored["cases"]
            require(len(original) == len(observed_cases), path + ': incomplete replay')
            for a, b in zip(original, observed_cases, strict=True):
                require(a["id"] == b["id"] and not differences(a["expected"], b["expected"]) and a.get('php_error') == b.get('php_error'), path + ":" + a["id"])
            references.append({"path": path, "sha256": digest(ROOT / path), "cases": len(original), "replayed_php_commit": BASE, "result": "identical"})
        for name in ("cases", "tickets"):
            path = f"backend/testdata/php-baseline/{name}.json"
            original = json.loads((ROOT / path).read_text(encoding="utf-8"))
            produced = run(ROOT / "scripts/go-migration/produce.php", current, {"cases": inputs(original["cases"])})
            reuse(path, produced["cases"])
        catalog_path = "backend/testdata/php-baseline/catalog.json"
        catalog = run(current / "backend/bin/catalog.php", current)
        require(not differences(catalog, json.loads((ROOT / catalog_path).read_text(encoding="utf-8"))["expected"]), 'Platform catalog changed')
        references.append({"path": catalog_path, "sha256": digest(ROOT / catalog_path), "cases": 1, "replayed_php_commit": BASE, "result": "identical"})
        original = json.loads((ROOT / "backend/testdata/php-douyin/cases.json").read_text(encoding="utf-8"))
        numeric = json.loads((ROOT / "backend/testdata/php-douyin/numeric.json").read_text(encoding="utf-8"))
        produced = run(ROOT / "scripts/go-migration/produce-douyin.php", current, {"cases": inputs(original["cases"]), "numeric_cases": inputs(numeric["cases"])})
        reuse("backend/testdata/php-douyin/cases.json", produced["cases"])
        reuse("backend/testdata/php-douyin/probe.json", produced["probe_cases"])
        reuse("backend/testdata/php-douyin/numeric.json", produced["numeric_cases"])
        integer_path = "backend/internal/platform/douyin/testdata/php-integer-boundaries.json"
        integer = json.loads((ROOT / integer_path).read_text(encoding="utf-8"))
        observed = run(ROOT / "scripts/go-migration/produce-douyin.php", current, {"cases": inputs(integer["cases"])})
        reuse(integer_path, observed["cases"])
        direct = run(ROOT / (HERE + ".php"), current, {"integer_direct": inputs(integer["direct_cases"])})
        for a, b in zip(integer["direct_cases"], direct["cases"], strict=True):
            require(a["id"] == b["id"] and not differences(a["expected"], b["expected"]), a["id"])
        references[-1]["direct_cases"] = len(direct["cases"])

        output = args.output.resolve(); output.mkdir(parents=True, exist_ok=True)
        write_json(output / "cases.json", after)
        write_json(output / "differences.json", {"previous_commit": OLD, "baseline_commit": BASE, "cases": changes, "reused": references})
        sources = sorted({entry["path"] for name in ("php-baseline", "php-douyin") for entry in json.loads((ROOT / f"backend/testdata/{name}/manifest.json").read_text(encoding="utf-8"))["source_files"]})
        sources = sorted(set(sources) | set(integer["source_sha256"]))
        producers = [HERE + ".py", HERE + ".php", "scripts/go-migration/produce.php", "scripts/go-migration/produce-douyin.php"]
        manifest = {"format_version": 1, "baseline_commit": BASE, "baseline_tree": TREE, "previous_commit": OLD, "previous_tree": OLD_TREE,
            "fixed_clock": {"unix_seconds": NOW, "rfc3339": "2026-10-30T12:00:00Z"},
            "php_version": runtime,
            "evidence_scope": "Fixed synthetic input through unmodified Git-archived PHP probe, formatter, adapter, default selector and public views; no live network or Go-derived expectations. Previous fixtures remain byte-identical.",
            "case_count": len(after["cases"]), "public_envelope_count": len(after["cases"]) * 2,
            "changed_case_count": sum(x["changed"] for x in changes), "disabled_native_functions": DISABLED.split(","),
            "files": {name: digest(output / name) for name in ("cases.json", "differences.json")},
            "producer_files": [{"path": path, "sha256": digest(ROOT / path)} for path in producers],
            "source_files": [{"path": path, "sha256": digest(current / path), "previous_sha256": digest(previous / path)} for path in sources],
            "reused": references,
            "known_contract_conflicts": ["live/direct-and-still fragment (normal/debug)", "integer size_bytes above JavaScript safe integer (8 public responses)"],
            "comparison_rules": "Compare every public field and array order; JSON object key order and equal number spellings only are nonsemantic. Private unknown probe metadata [] may be represented by Go empty object. No URL/ID/expiry stripping."}
        write_json(output / "manifest.json", manifest)
        print(dump({"baseline": BASE, "cases": len(after["cases"]), "changed": manifest["changed_case_count"], "reused": references}))


if __name__ == "__main__":
    main()
