#!/usr/bin/env python3
"""Freeze original Pipixia PHP parser, request wire and public views without network."""
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
ID = "6879718018532776200"
URL = f"https://h5.pipix.com/item/{ID}"
PAGE = f"https://h5.pipix.com/ppx/item/{ID}"
DIRECTORY = ROOT / "backend/internal/platform/pipixia/testdata"
DISABLED = "curl_init,curl_setopt_array,curl_setopt,curl_exec,curl_getinfo,curl_close,curl_error,curl_errno,get_headers,microtime"
ENTRY_FLAGS = 320  # UNESCAPED_UNICODE | UNESCAPED_SLASHES, without NUMERIC_CHECK
PRODUCERS = ["scripts/go-migration/produce-pipixia.py", "scripts/go-migration/produce-pipixia.php"]
SOURCES = ["backend/api/ppxia.php", "backend/lib/runtime.php", "backend/lib/gateway.php",
           "backend/lib/media-adapters.php", "backend/lib/media-contract.php", "backend/lib/media-metadata.php",
           "backend/lib/media-policy.php", "backend/lib/platforms.php", "backend/lib/weibo-media.php",
           "backend/config/platforms.json", "backend/tests/pipixia-contract.php",
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


def media(name, width=1920, height=1080, **extra):
    return {"width": width, "height": height, "url_list": [{"url": f"https://media.example/{name}.mp4?keep=a%2Bb+z&token=fixed"}], **extra}


def item(**extra):
    return {"item_id_str": ID, "content": "皮皮虾 A+B %25 中文😀", "status": 0,
            "author": {"name": "合成作者", "avatar": {"download_list": [{"url": "//images.example/avatar.jpg"}]}},
            "create_time": 1601234567, "stats": {"like_count": 0, "comment_count": "4", "share_count": 2},
            "video": {"duration": 11.355, "video_download": media("main")}, **extra}


def page(value=None, encoded=False, extra=None):
    state = {"ppxItemDetail": {"item": item() if value is None else value}}
    state.update(extra or {})
    raw = compact(state)
    if encoded: raw = urllib.parse.quote(raw, safe="")
    return '<html><script type="application/json">' + raw + '</script></html>'


def response(url=PAGE, *, status=200, body="", headers=None, **extra):
    return {"method": "GET", "url": url, "status": status, "headers": headers or {}, "body": body,
            "follow_redirects": False, **extra}


def build_cases():
    cases = []
    def add(name, scenario, *, url=URL, html=None, value=None, prefix=None, fetch_page=True, target=ID, final=None, ticks=None):
        tape = copy.deepcopy(prefix or [])
        if fetch_page: tape.append(final if final is not None else response("https://h5.pipix.com/ppx/item/" + target, body=html if html is not None else page(value)))
        cases.append({"id": "pipixia/" + name, "scenario": scenario,
                      "input": {"url": url, "now": NOW, "clock_ticks": ticks or [100], "http_responses": tape}})
    add("video/minimal", "Real complete video remains simple; signed URL bytes, zero statistics and seconds survive")
    add("page/encoded", "Inert percent encoded JSON is decoded without executing script", html=page(encoded=True))
    rich = item()
    rich["origin_video_download"] = media("original", 3840, 2160, download_list=[{"url": "https://mirror.example/original.mp4"}, {"url": "https://media.example/original.mp4?keep=a%2Bb+z&token=fixed"}])
    rich["video"].update(video_high=media("high", 1280, 720), video_mid={"url_list": ["javascript:alert(1)", "https://media.example/unknown.mp4"]}, video_fallback=media("main", 4096, 4096))
    rich["note"] = {"multi_image": [{"url_list": ["https://images.example/one.jpg"]}, {"url_list": ["https://images.example/two.jpg"]}]}
    add("video/all-representations-and-images", "Every declared video source and image stays; duplicate URL retains first evidence; origin is highest actual quality", value=rich)
    tied = item(video={"video_download": media("z-first", 1280, 720), "video_high": media("a-second", 1280, 720)})
    add("video/tied-quality-keeps-order", "Equal area preserves PHP insertion order", value=tied)
    backup = item(video={"video_download": {"download_list": [{"url": "//media.example/backup.mp4"}, "https://mirror.example/backup.mp4"], "width": "1024", "height": "576"}})
    add("video/download-list-only", "Download-only candidates and protocol-relative URLs remain independently represented", value=backup)
    image_only = item(video={}, note={"multi_image": [{"url_list": ["javascript:x", {"url": "https://images.example/a.jpg"}]}, {"url_list": ["https://images.example/b.jpg"]}, {"url_list": ["https://images.example/a.jpg"]}]})
    add("album/without-video", "Image-only work with no comments is valid; first usable image and de-duplication follow PHP", value=image_only)
    add("album/ignores-comment-video", "An unrelated comment video never changes image-only work", html=page(image_only, extra={"ppxCellComment": {"cell_comments": [{"item": item()}]}}))
    numeric = item(content="000123", author={"name": "+0012"})
    add("metadata/text-not-numeric-check", "Entry encoder keeps numeric-looking title and author text", value=numeric)
    exact = item(item_id=6879718018532776200); exact.pop("item_id_str")
    add("identity/int64-token", "A large native JSON integer below PHP_INT_MAX remains exact", value=exact)
    bigint_id = "98765432109876543210"
    bigint = item(item_id=int(bigint_id)); bigint.pop("item_id_str")
    add("identity/bigint-token", "JSON_BIGINT_AS_STRING preserves a 20-digit item ID", url="https://h5.pipix.com/item/" + bigint_id, target=bigint_id, value=bigint)
    precedence = item(item_id=6879718018532776000)
    add("identity/string-precedes-rounded-number", "Exact item_id_str wins over the rounded numeric observation", value=precedence)
    for name, value in [("mismatch", item(item_id_str="1")), ("array", item(item_id_str=[])), ("float", {**item(), "item_id_str": None, "item_id": 1.5}), ("missing", {k:v for k,v in item().items() if k != "item_id_str"})]:
        add("identity/"+name, "Unavailable or mismatched identity cannot borrow another work", value=value)
    add("item/unavailable", "Explicit status is distinct from missing media", value=item(status=1))
    add("item/string-zero-status", "PHP empty string-zero status is accessible", value=item(status="0"))
    add("item/no-media", "An existing item with no usable media is not empty success", value=item(video={}))
    fallback = item(video={"duration": "1.5005", "video_download": media("fallback", "1920", "1080"), "cover_image": {"url_list": ["https://images.example/fallback.jpg"]}}, stats={"like_count": -1, "comment_count": "2.5", "play_count": 0, "view_count": "12"})
    add("metadata/fallback-and-casts", "Cover, duration rounding, numeric counts and invalid negative facts follow PHP", value=fallback)
    for name, link in [("modern", "https://h5.pipix.com/ppx/item/"+ID+"/?from=share"), ("http-upgrade", "http://h5.pipix.com/item/"+ID), ("query", "https://h5.pipix.com/share?item_id="+ID), ("query-last-scalar", "https://h5.pipix.com/share?item_id=1&item_id="+ID), ("query-array-then-scalar", "https://h5.pipix.com/share?item_id[]=1&item_id="+ID)]:
        add("entry/"+name, "Original path/query entry resolves exactly the requested canonical item", url=link)
    short = "https://h5.pipix.com/s/synthetic/"
    for name, location in [("absolute",URL),("relative","/item/"+ID),("protocol-relative","//h5.pipix.com/item/"+ID),("http-upgrade","http://h5.pipix.com/item/"+ID)]:
        add("short/"+name, "Explicit share redirect stays platform-scoped before one canonical page", url=short, prefix=[response(short,status=302,headers={"Location":location})])
    add("short/loop-limit", "Exactly five unresolved redirects, no sixth request", url=short, prefix=[response(short,status=302,headers={"Location":short}) for _ in range(5)], fetch_page=False)
    add("short/foreign-host", "Foreign redirect never becomes an upstream request", url=short, prefix=[response(short,status=302,headers={"Location":"https://example.com/item/"+ID})], fetch_page=False)
    add("short/nonredirect", "A landing page without target ID is not parsed as another work", url=short,prefix=[response(short,body=page())],fetch_page=False)
    add("entry/query-array-rejected", "Array item_id cannot substitute a scalar",url="https://h5.pipix.com/share?item_id[]="+ID,prefix=[response("https://h5.pipix.com/share?item_id[]="+ID)],fetch_page=False)
    for name, link in [("foreign-host","https://pipix.com.example/item/"+ID),("userinfo","https://user@h5.pipix.com/item/"+ID),("nonstandard-port","https://h5.pipix.com:8080/item/"+ID)]:
        add("entry/reject-"+name,"Invalid initial authority creates no request",url=link,fetch_page=False)
    add("budget/shared-ceil", "Remaining total budget is ceiled per request; late canonical request receives one second", url=short,prefix=[response(short,status=302,headers={"Location":URL})],ticks=[100,101,119.2,119.8])
    add("budget/expired-after-redirect", "Expired shared budget stops before canonical request",url=short,prefix=[response(short,status=302,headers={"Location":URL})],fetch_page=False,ticks=[100,100,120])
    add("budget/subsecond-direct", "Subsecond remaining request budget is ceiled to one second",ticks=[100,119.5])
    for status in (401,403,429,404,500): add("page/http-"+str(status),"Upstream HTTP status preserves original public error classification",final=response(status=status))
    for name,errno in [("transport",6),("timeout",28)]: add("page/"+name,"Transport failure and timeout remain distinct",final=response(status=0,error="synthetic transport failure",errno=errno))
    for name,html in [("missing-script","<html>empty</html>"),("invalid-json",'<script>{"ppxItemDetail":broken}</script>'),("comment-only",'<script>'+compact({"ppxCellComment":{"cell_comments":[{"item":item()}]}})+'</script>'),("executable-script",'<script>window.state='+compact({"ppxItemDetail":{"item":item()}})+'</script>')]:
        add("page/"+name,"Only inert root item JSON can supply the work",html=html)
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
    with tempfile.TemporaryDirectory(prefix="dokitool-pipixia-php-") as folder:
        baseline = pathlib.Path(folder); archive(args.repo.resolve(), baseline)
        command = [str(args.php.resolve()), "-d", "disable_functions=" + DISABLED, "-d", "allow_url_fopen=0",
                   str(ROOT / PRODUCERS[1]), str(baseline)]
        process = subprocess.run(command, input=dump(document), text=True, encoding="utf-8", capture_output=True)
        require(process.returncode == 0 and not process.stderr.strip(), "PHP Pipixia oracle failed:\n" + process.stderr + process.stdout[:3000])
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
                    "evidence_scope": "Synthetic metadata responses through immutable PHP pipixia/pipixiaRequest/parser/entry-wire/adapter/public functions. No Go-generated expectations or live media evidence.",
                    "files": {"php-baseline.json": sha(output / "php-baseline.json")},
                    "input_files": [{"path": str(frozen_inputs.relative_to(ROOT)), "sha256": sha(frozen_inputs)}],
                    "source_files": [{"path": path, "sha256": sha(baseline / path)} for path in SOURCES],
                    "producer_files": [{"path": path, "sha256": sha(ROOT / path)} for path in PRODUCERS],
                    "transport_substitution": {"disabled_native_functions": DISABLED.split(","),
                        "mechanism": "Only native cURL IO and microtime are substituted; the original header callback and bounded response writer execute. Unknown/reordered/unused requests fail; the shared 20-second budget uses reviewed ticks.",
                        "captured_request_fields": ["method", "url", "headers", "body", "follow_redirects", "timeout_seconds"]},
                    "comparison_rules": ["Entry has no JSON_NUMERIC_CHECK; exact numbers, missing/null, array/source order, URL bytes and stable identities remain exact.",
                        "Successful data re-finalized at the explicit public fixture clock; synthetic URLs expire far in 2100, not near wall clock.",
                        "Only JSON object key order and equivalent number spelling are non-semantic; no metadata/identity/URL exclusions.",
                        "Request header casing is normalized; timeout is the original ceiled shared 20-second budget, body bytes and all intentional headers remain exact.",
                        "The PHP recorder captures explicit cURL headers and User-Agent. All recorded headers are compared exactly; no application header is ignored."]}
        (output / "manifest.json").write_bytes(dump(manifest).encode())
    print(compact({"baseline": BASE, "php_version": runtime, "cases": len(cases), "public_envelopes": len(cases) * 2}))


if __name__ == "__main__":
    main()
