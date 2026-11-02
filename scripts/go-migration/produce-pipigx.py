#!/usr/bin/env python3
"""Freeze original Pipigx PHP parser, request wire and public views without network."""
from __future__ import annotations

import argparse
import copy
import hashlib
import io
import json
import pathlib
import subprocess
import tarfile
import tempfile
import urllib.parse

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASE = "25beaee107f8c1a208ced9deb3c637068445e316"
TREE = "e4b9d40f5e6c12a86fff629c105553886b434dcd"
CLOCK, NOW = "2026-10-30T12:00:00Z", 1793361600
PID, MID = "1234567890123456789", "9876543210"
URL = f"https://h5.pipigx.com/pp/post/{PID}?pid={PID}&mid={MID}"
ENDPOINT = "https://h5.pipigx.com/ppapi/share/fetch_content"
DIRECTORY = ROOT / "backend/internal/platform/pipigx/testdata"
DISABLED = "curl_init,curl_setopt_array,curl_setopt,curl_exec,curl_getinfo,curl_close,curl_error,curl_errno,get_headers"
ENTRY_FLAGS = 480  # PRETTY_PRINT | NUMERIC_CHECK | UNESCAPED_UNICODE | UNESCAPED_SLASHES
PRODUCERS = ["scripts/go-migration/produce-pipigx.py", "scripts/go-migration/produce-pipigx.php"]
SOURCES = ["backend/api/pipigx.php", "backend/lib/runtime.php", "backend/lib/gateway.php",
           "backend/lib/media-adapters.php", "backend/lib/media-contract.php", "backend/lib/media-metadata.php",
           "backend/lib/media-policy.php", "backend/lib/platforms.php", "backend/lib/weibo-media.php",
           "backend/config/platforms.json",
           "docs/schema/unified-v1/unified-v1.schema.json"]


def dump(value):
    return json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n"


def compact(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def video(name="main", **extra):
    return {"url": "https://media.example/" + name + ".mp4?keep=a%2Bb+z&token=fixed", "thumb": "1234567890", "width": 1920, "height": 1080, "size": 12000000, "duration": 12.345, **extra}


def body(videos=None, **extra):
    return compact({"data": {"post": {"content": "皮皮搞笑 A+B %25 中文😀", "videos": [video()] if videos is None else videos, **extra}}})


def response(*, status=200, text=None, **extra):
    return {"method": "POST", "url": ENDPOINT, "status": status, "headers": {}, "body": body() if text is None else text, "follow_redirects": False, **extra}


def build_cases():
    cases = []
    def add(name, scenario, *, url=URL, videos=None, post=None, final=None, fetch=True):
        tape = [final if final is not None else response(text=body(videos, **(post or {})))] if fetch else []
        cases.append({"id": "pipigx/" + name, "scenario": scenario, "input": {"url": url, "now": NOW, "clock_ticks": [100], "http_responses": tape}})
    add("video/minimal", "Single complete video stays simple and request bytes remain fixed")
    add("video/three-with-invalid-middle", "Invalid URL leaves an index gap while all private video observations survive", videos=[video("one"),video("invalid",url="javascript:alert(1)"),video("three",width=1280,height=720)])
    add("video/object-insertion-order", "Object videos preserve upstream JSON insertion order, never sorted Go map keys", videos={"z-first":video("z-first"),"a-second":video("a-second")})
    add("video/nonarray-skipped-before-indexing", "Non-array entries are discarded before the PHP private_videos list is indexed", videos=["not-an-array",video("first"),None,video("second")])
    add("video/first-url-invalid", "Invalid first private video makes an explicit failure even if a later video is usable", videos=[video("bad",url="javascript:alert(1)"),video("later")])
    add("video/preserves-duplicate-entries", "Private video entries remain distinct items even when media URL repeats", videos=[video("same"),video("same")])
    add("video/upstream-private-identity", "Explicit upstream private_video_id uses the shared identity boundary", videos=[video("a",private_video_id="resource-z"),video("b",private_video_id="resource-a")])
    add("video/no-invented-album", "Unrelated image fields never become an unsupported image album",post={"images":["https://images.example/a.jpg"],"imgs":["https://images.example/b.jpg"]})
    add("wire/numeric-text", "Flags480 convert numeric title, dimensions and metadata before adaptation",videos=[video(width="1920",height="1080",size="12000000",duration="1.5005",quality="720")],post={"content":"000123"})
    add("wire/exponent-title", "Numeric exponent becomes double; PHP snapText does not invent a title from a float",post={"content":"1e3"})
    add("wire/large-json-token", "Default PHP JSON decoding keeps int64 tokens exact and uses doubles beyond its range",videos=[video(thumb=9007199254740993,width=1920,height=1080,private_metadata_id=9223372036854775808)])
    add("wire/large-numeric-string", "JSON_NUMERIC_CHECK parses oversized numeric strings at the entry boundary",videos=[video(private_metadata_id="9223372036854775808",quality="001080")])
    add("entry/mid-zero", "A zero mid is valid and remains zero in POST",url="https://h5.pipigx.com/share?pid=1&mid=0")
    add("entry/mid-leading-zero", "Leading zero mid casts exactly once to integer",url="https://h5.pipigx.com/share?pid=123&mid=0000123")
    add("entry/duplicate-last-scalar", "PHP parse_str last scalar wins without double URL decoding",url="https://h5.pipigx.com/share?pid=1&mid=2&pid="+PID+"&mid="+MID)
    add("entry/array-then-scalar", "A later scalar replaces an earlier query array",url="https://h5.pipigx.com/share?pid[]=1&pid="+PID+"&mid="+MID)
    add("entry/ippzone-domain", "The registered alternate share domain uses the same fixed API",url="https://ippzone.com/share?pid="+PID+"&mid="+MID)
    for name,value in [("int64-max","9223372036854775807"),("one-past-int64","9223372036854775808"),("float-rounded-above-int64","9223372036854776831")]:
        add("integer/"+name,"Preserve PHP float-boundary validation then exact numeric-string int conversion",url="https://h5.pipigx.com/share?pid="+value+"&mid="+value)
    for name,query in [("leading-zero-pid","pid=01&mid=1"),("zero-pid","pid=0&mid=1"),("negative","pid=-1&mid=1"),("escaped-plus","pid=%2B1&mid=1"),("array","pid[]=1&mid=1"),("missing-mid","pid=1"),("float","pid=1.0&mid=1"),("exponent","pid=1e3&mid=1"),("whitespace","pid=%201&mid=1"),("int64-too-large","pid=9223372036854777856&mid=1"),("twenty-digits","pid=10000000000000000000&mid=1")]:
        add("entry/reject-"+name,"Invalid parameter shape never sends POST",url="https://h5.pipigx.com/share?"+query,fetch=False)
    for status in (403,404,429,500): add("http/"+str(status),"Legacy HTTP error mapping preserves retryability and public status",final=response(status=status,text="not JSON"))
    add("http/transport-failure","cURL errno becomes original HTTP500 wrapper before public error mapping",final=response(status=0,error="Synthetic transport failure",errno=6))
    for name,text in [("invalid-json","{broken"),("null-json","null"),("missing-post",compact({"data":{}})),("empty-post",compact({"data":{"post":{}}})),("empty-videos",body([])),("scalar-videos",body("bad"))]:
        add("response/"+name,"Malformed or empty upstream payload remains explicit failure",final=response(text=text))
    return cases


def input_document():
    return {"format_version": 1, "baseline_commit": BASE, "baseline_tree": TREE,
            "fixed_clock": {"rfc3339": CLOCK, "unix_seconds": NOW}, "cases": build_cases()}


def archive(repo, target):
    tree = subprocess.check_output(["git", "rev-parse", BASE + "^{tree}"], cwd=repo, text=True).strip()
    require(tree == TREE, "Pinned PHP commit/tree mismatch")
    data = subprocess.check_output(["git", "archive", BASE], cwd=repo)
    with tarfile.open(fileobj=io.BytesIO(data)) as stream:
        for member in stream.getmembers():
            require((member.isfile() or member.isdir()) and (target / member.name).resolve().is_relative_to(target.resolve()), "Unsafe archive entry")
        stream.extractall(target, filter="data")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--php", type=pathlib.Path)
    parser.add_argument("--repo", type=pathlib.Path, default=ROOT)
    parser.add_argument("--output", type=pathlib.Path, default=DIRECTORY)
    parser.add_argument("--write-inputs", action="store_true")
    args = parser.parse_args()
    document = input_document()
    output = args.output.resolve(); output.mkdir(parents=True, exist_ok=True)
    if args.write_inputs:
        (output / "inputs.json").write_bytes(dump(document).encode())
        print(compact({"inputs_only": True, "cases": len(document["cases"])})); return
    require(args.php is not None, "--php is required to produce expectations")
    frozen_inputs = DIRECTORY / "inputs.json"
    require(frozen_inputs.read_bytes() == dump(document).encode(), "Frozen inputs differ from reviewed producer")
    runtime = subprocess.check_output([str(args.php.resolve()), "-r", "echo PHP_VERSION;"], text=True).strip()
    require(runtime == "8.3.6", "PHP 8.3.6 required")
    with tempfile.TemporaryDirectory(prefix="dokitool-pipigx-php-") as folder:
        baseline = pathlib.Path(folder); archive(args.repo.resolve(), baseline)
        command = [str(args.php.resolve()), "-d", "disable_functions=" + DISABLED, "-d", "allow_url_fopen=0",
                   str(ROOT / PRODUCERS[1]), str(baseline)]
        process = subprocess.run(command, input=dump(document), text=True, encoding="utf-8", capture_output=True)
        require(process.returncode == 0 and not process.stderr.strip(), "PHP Pipigx oracle failed:\n" + process.stderr + process.stdout[:3000])
        observed = json.loads(process.stdout)
        require([c["id"] for c in observed["cases"]] == [c["id"] for c in document["cases"]], "PHP case inventory/order mismatch")
        cases = []
        for original, actual in zip(document["cases"], observed["cases"], strict=True):
            entry = copy.deepcopy(original); entry["expected"] = actual["expected"]; cases.append(entry)
        produced = {k: v for k, v in document.items() if k != "cases"}; produced["cases"] = cases
        (output / "php-baseline.json").write_bytes(dump(produced).encode())
        manifest = {"format_version": 1, "baseline_commit": BASE, "baseline_tree": TREE, "php_version": runtime,
                    "fixed_clock": document["fixed_clock"], "parse_case_count": len(cases), "public_envelope_count": len(cases) * 2,
                    "entry_json_flags": ENTRY_FLAGS,
                    "evidence_scope": "Synthetic metadata responses through immutable PHP pipigx/pipigxRequest/parser/entry-wire/adapter/public functions. No Go-generated expectations or live media evidence.",
                    "files": {"php-baseline.json": sha(output / "php-baseline.json")},
                    "input_files": [{"path": str(frozen_inputs.relative_to(ROOT)), "sha256": sha(frozen_inputs)}],
                    "source_files": [{"path": path, "sha256": sha(baseline / path)} for path in SOURCES],
                    "producer_files": [{"path": path, "sha256": sha(ROOT / path)} for path in PRODUCERS],
                    "transport_substitution": {"disabled_native_functions": DISABLED.split(","),
                        "mechanism": "Only native cURL IO replaced; original parameter validation, int conversion, JSON POST encoder and bounded response writer execute. Unknown/reordered/unused requests fail. Single POST retains a 20-second timeout.",
                        "captured_request_fields": ["method", "url", "headers", "body", "follow_redirects", "timeout_seconds"]},
                    "comparison_rules": ["Entry flags 480 include JSON_NUMERIC_CHECK: numeric-looking strings pass through the original PHP wire conversion before the original adapter. Preserve private_videos and its original filtered positions.",
                        "Successful data re-finalized at the explicit public fixture clock; synthetic URLs expire far in 2100, not near wall clock.",
                        "Only JSON object key order and equivalent number spelling are non-semantic; no metadata/identity/URL exclusions.",
                        "Request header casing is normalized; fixed POST bytes, explicit Content-Length and 20-second timeout stay exact.",
                        "Only explicit PHP headers are recorded; Go suppresses its library User-Agent and declares libcurl implicit Accept */*. These exact transport-default distinctions are tested without modifying the oracle."]}
        (output / "manifest.json").write_bytes(dump(manifest).encode())
    print(compact({"baseline": BASE, "php_version": runtime, "cases": len(cases), "public_envelopes": len(cases) * 2}))


if __name__ == "__main__":
    main()
