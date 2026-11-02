#!/usr/bin/env python3
"""Freeze real PHP Weibo parsing, signing and public HTTP results without network."""
from __future__ import annotations

import argparse
import copy
import hashlib
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
from urllib.parse import quote

ROOT = Path(__file__).resolve().parents[2]
BASE = "25beaee107f8c1a208ced9deb3c637068445e316"
TREE = "e4b9d40f5e6c12a86fff629c105553886b434dcd"
DIRECTORY = ROOT / "backend/internal/platform/weibo/testdata"
NOW = 1793361600
KEY = "public-synthetic-weibo-migration-key-DO-NOT-DEPLOY"
URL = "https://weibo.com/1234567890/AbC123"
FID = "1034:1234567890123456"
TV = "https://weibo.com/tv/show/" + FID
DISABLED = "time,curl_init,curl_setopt_array,curl_setopt,curl_exec,curl_getinfo,curl_close,curl_error,curl_errno"
PRODUCERS = ["scripts/go-migration/produce-weibo-signed.py", "scripts/go-migration/produce-weibo-signed.php"]
SOURCES = ["backend/api/weibo.php", "backend/lib/gateway.php", "backend/lib/runtime.php",
           "backend/lib/platforms.php", "backend/lib/media-adapters.php", "backend/lib/media-contract.php",
           "backend/lib/media-metadata.php", "backend/lib/media-policy.php", "backend/lib/weibo-media.php",
           "backend/config/platforms.json", "docs/schema/unified-v1/unified-v1.schema.json"]


def dump(value):
    return json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n"


def compact(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def response(url, data=None, *, body=None, status=200, post=None, error=False):
    return {"url": url, "method": "GET" if post is None else "POST", "post": post,
            "status": status, "body": compact(data) if body is None else body, "error": error}


def video_url(name, credential="before"):
    return f"https://f.video.weibocdn.com/{name}.mp4?Expires=4102444800&ssig={credential}&KID=1&keep=a%2Bb"


def status_document(credential="before"):
    videos = [{"url": video_url(name, credential), "mime": "video/mp4", "label": label,
               "width": width, "height": height, "size": size, "bitrate": bitrate,
               "fps": 30, "video_codecs": "h264"}
              for name, label, width, height, size, bitrate in [
                  ("full-4k", "4K", 3840, 2160, 16000000, 8000000),
                  ("full-1080", "1080p", 1920, 1080, 8000000, 4000000)]]
    return {"idstr": "1234567890123456", "text_raw": "微博签发全路径 A+B 中文😀",
            "user": {"idstr": "90071992547409930012", "screen_name": "合成作者"},
            "page_info": {"page_pic": "https://images.example/weibo.jpg", "media_info": {
                "duration": 12.345, "online_users_number": 0,
                "playback_list": [{"play_info": entry} for entry in videos]}},
            "attitudes_count": 0, "comments_count": "12", "reposts_count": 0}


def input_document():
    cases = []
    endpoint = "https://weibo.com/ajax/statuses/show?id=AbC123"
    component = "https://weibo.com/tv/api/component?page=" + quote("/tv/show/" + FID, safe="")
    component_post = "data=" + quote(compact({"Component_Play_Playinfo": {"oid": FID}}), safe="")

    def add(name, replies, *, source=URL, key=KEY, now=NOW, routes=("parse", "resolve"), item="video"):
        for route in routes:
            cases.append({"id": "weibo/" + name + "/" + route,
                          "input": {"url": source, "now": now, "key": key, "route": route,
                                    "item_id": item, "http_responses": copy.deepcopy(replies)}})

    primary = status_document()
    add("status-complete", [response(endpoint, primary)])
    for name, value in [("integer", 9007199254740993), ("string", "9007199254740993")]:
        large = copy.deepcopy(primary); large["attitudes_count"] = value
        add("large-count-" + name, [response(endpoint, large)], routes=("parse",))
    mixed = copy.deepcopy(primary)
    mixed["page_info"]["media_info"]["playback_list"][0]["play_info"]["url"] = "https://unsupported.example/high.mp4"
    add("unqualified-high-falls-back", [response(endpoint, mixed)])
    unsupported = copy.deepcopy(primary)
    for p in unsupported["page_info"]["media_info"]["playback_list"]:
        p["play_info"]["url"] = p["play_info"]["url"].replace("f.video.weibocdn.com", "unsupported.example")
    add("no-qualified-source", [response(endpoint, unsupported)])
    add("missing-key", [response(endpoint, primary)], key="")
    add("short-key", [response(endpoint, primary)], key="short", routes=("parse",))
    add("no-video", [response(endpoint, {"idstr": "123", "page_info": {"media_info": {}}})])
    add("blocked", [response(endpoint, status=403)])
    add("transport-failure", [response(endpoint, status=0, body="", error=True)])
    add("invalid-json", [response(endpoint, body="{broken")])
    add("missing-target", [response(endpoint, primary)], routes=("resolve",), item="missing-video")
    info = {"title": "TV 主视频", "author": "TV 作者", "duration_time": 12.345,
            "urls": {"1080p": video_url("tv-1080"), "720p": video_url("tv-720")}}
    tv = {"code": 100000, "data": {"Component_Play_Playinfo": info}}
    add("tv-component", [response(component, tv, post=component_post)], source=TV)
    tv_with_mid = copy.deepcopy(tv); tv_with_mid["data"]["Component_Play_Playinfo"]["mid"] = "1234567890123456"
    supplement = "https://weibo.com/ajax/statuses/show?id=1234567890123456"
    add("tv-supplement", [response(component, tv_with_mid, post=component_post), response(supplement, primary)], source=TV)
    add("tv-optional-failure", [response(component, tv_with_mid, post=component_post), response(supplement, status=403)], source=TV)
    add("tv-business-error-scalar-data", [response(component, {"code": 100001, "data": "blocked"}, post=component_post)], source=TV)
    tv_array = copy.deepcopy(tv); tv_array["data"]["Component_Play_Playinfo"]["urls"] = [video_url("array-0"), video_url("array-1")]
    add("tv-array-urls", [response(component, tv_array, post=component_post)], source=TV)
    fingerprint = compact({"os": "1", "browser": "Chrome131,0,0,0", "fonts": "undefined", "screenInfo": "1920*1080*24", "plugins": ""})
    guest_post = "cb=gen_callback&fp=" + quote(fingerprint, safe="")
    guest_url = "https://passport.weibo.com/visitor/visitor?a=incarnate&t=synthetic_tid&w=3&c=080&gc=&cb=cross_domain&from=weibo&_rand=<nonce>"
    add("anonymous-session", [
        response(endpoint, body="<html><title>Sina Visitor System</title></html>"),
        response("https://passport.weibo.com/visitor/genvisitor", body='gen_callback({"retcode":20000000,"data":{"tid":"synthetic_tid","new_tid":true,"confidence":80}})', post=guest_post),
        response(guest_url, body='cross_domain({"retcode":20000000})'),
        response(endpoint, primary)])
    add("renewal/before", [response(endpoint, status_document("before"))], routes=("parse",))
    after = status_document("after")
    after["page_info"]["media_info"]["playback_list"].reverse()
    add("renewal/after", [response(endpoint, after)], now=NOW + 3600, routes=("parse", "resolve"))
    return {"format_version": 1, "baseline_commit": BASE, "fixed_clock": "2026-10-30T12:00:00Z", "cases": cases}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--php", type=Path)
    parser.add_argument("--output", type=Path, default=DIRECTORY)
    parser.add_argument("--write-inputs", action="store_true")
    args = parser.parse_args()
    document = input_document(); output = args.output.resolve(); output.mkdir(parents=True, exist_ok=True)
    frozen_inputs = DIRECTORY / "signed-inputs.json"
    if args.write_inputs:
        (output / "signed-inputs.json").write_text(dump(document), encoding="utf-8")
        print(compact({"inputs_only": True, "cases": len(document["cases"])})); return
    if args.php is None or frozen_inputs.read_bytes() != dump(document).encode():
        raise ValueError("Fixed PHP and unchanged reviewed inputs are required")
    runtime = subprocess.check_output([str(args.php.resolve()), "-r", "echo PHP_VERSION;"], text=True).strip()
    if runtime != "8.3.6": raise ValueError("PHP 8.3.6 required, got " + runtime)
    if subprocess.check_output(["git", "rev-parse", BASE + "^{tree}"], cwd=ROOT, text=True).strip() != TREE:
        raise ValueError("Frozen PHP tree changed")
    with tempfile.TemporaryDirectory(prefix="weibo-signed-php-") as directory:
        archive = Path(directory)
        with tarfile.open(fileobj=io.BytesIO(subprocess.check_output(["git", "archive", BASE], cwd=ROOT))) as source:
            for entry in source.getmembers():
                if not (entry.isfile() or entry.isdir()) or not (archive / entry.name).resolve().is_relative_to(archive):
                    raise ValueError("Unsafe archive entry")
            source.extractall(archive, filter="data")
        command = [str(args.php.resolve()), "-d", "disable_functions=" + DISABLED, "-d", "allow_url_fopen=0",
                   str(ROOT / PRODUCERS[1]), str(archive)]
        result = subprocess.run(command, input=dump(document), text=True, encoding="utf-8", capture_output=True)
        if result.returncode or result.stderr.strip(): raise RuntimeError(result.stderr + result.stdout[:3000])
        actual = json.loads(result.stdout)
        if [c["id"] for c in actual["cases"]] != [c["id"] for c in document["cases"]]: raise ValueError("PHP inventory differs")
        produced = copy.deepcopy(document)
        for case, observed in zip(produced["cases"], actual["cases"], strict=True): case["expected"] = observed["expected"]
        (output / "php-signed-baseline.json").write_text(dump(produced), encoding="utf-8")
        manifest = {"format_version": 1, "baseline_commit": BASE, "baseline_tree": TREE, "php_version": runtime,
                    "cases": len(document["cases"]), "public_envelopes": len(document["cases"]) * 2,
                    "files": {"php-signed-baseline.json": sha(output / "php-signed-baseline.json")},
                    "inputs": {str(frozen_inputs.relative_to(ROOT)): sha(frozen_inputs)},
                    "sources": {p: sha(archive / p) for p in SOURCES},
                    "producers": {p: sha(ROOT / p) for p in PRODUCERS},
                    "scope": "Original weiboFetchTarget, outputSuccess, snapAdaptParser and actual snapHandle/signing/resolve; controlled cURL metadata and internal-forward IO; fixed time() only; no source edits or live network.",
                    "request_comparison": "Exact URL/method/post/explicit PHP headers; only _rand normalized. Go performs its own approved redirects and decoder negotiation; PHP's cURL-native encoding/follow settings are not invented as Go wire headers."}
        (output / "signed-manifest.json").write_text(dump(manifest), encoding="utf-8")
    print(compact({"cases": len(document["cases"]), "public_envelopes": len(document["cases"]) * 2, "php": runtime}))


if __name__ == "__main__":
    main()
