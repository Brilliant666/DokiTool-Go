#!/usr/bin/env python3
"""Check the separate frozen PHP XHS wire oracle without PHP, Go or network."""
from __future__ import annotations

import argparse
from decimal import Decimal
import hashlib
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
DIRECTORY = ROOT / "backend/internal/platform/xiaohongshu/testdata"
BASE = "25beaee107f8c1a208ced9deb3c637068445e316"
TREE = "e4b9d40f5e6c12a86fff629c105553886b434dcd"
NOW, CLOCK = 1793361600, "2026-10-30T12:00:00Z"
PRODUCER = "scripts/go-migration/produce-xiaohongshu-wire.php"
URL = "https://www.xiaohongshu.com/explore/wirefixture01?xsec_token=synthetic%2Bwire&keep=a+b%2525"
PAGE_URL = URL.replace("www.xiaohongshu.com", "www.rednote.com")
COVER = '[{"url":"https://images.example/wire-cover.jpg"}]'
STREAM = '{"masterUrl":"https://media.example/wire-video.mp4?sig=synthetic%2Bwire","width":1920,"height":1080,"duration":12345,"avgBitrate":5000000,"size":7715625}'
LIVE = '{"h264":[' + STREAM + ']}'
SPECS = [
    ("integral-numbers", "1e3", "12.0", COVER, "video"),
    ("negative-zero", "-0.0", "-0", COVER, "video"),
    ("fractional-numbers", "1.25", "12.5", COVER, "video"),
    ("exponent-numbers", "1e-5", "1e21", COVER, "video"),
    ("numeric-strings", '"1e3"', '"12.0"', COVER, "video"),
    ("large-integers", "9007199254740993", "9223372036854775808", COVER, "video"),
    ("empty-array-cover", '"empty array"', '"author"', '[{"urlPre":[],"urlDefault":"https://images.example/fallback.jpg"}]', "video"),
    ("empty-object-cover", '"empty object"', '"author"', '[{"urlPre":{},"urlDefault":"https://images.example/fallback.jpg"}]', "video"),
    ("empty-image-live", '"live association"', '"author"', '[{"url":[],"stream":' + LIVE + '},{"url":"https://images.example/static.jpg"},{"url":"https://images.example/live.jpg","stream":' + LIVE + '}]', "normal"),
    ("associative-image-key-zero", '"numeric image keys"', '"author"', '{"1":{"urlPre":"https://images.example/one-cover.jpg","url":"https://images.example/one.jpg"},"0":{"urlPre":"https://images.example/zero-cover.jpg","url":"https://images.example/zero.jpg"}}', "normal"),
]


def require(condition, message):
    if not condition:
        raise ValueError(message)


def read_json(path):
    return json.loads(path.read_text(encoding="utf-8"), parse_float=Decimal)


def reviewed_input(spec):
    _, title, nickname, images, kind = spec
    note = '{"noteId":"wirefixture01","type":"' + kind + '","title":' + title + ',"desc":"PHP wire boundary","user":{"nickname":' + nickname + ',"userId":"007"},"imageList":' + images + ',"video":{"media":{"stream":' + LIVE + '}}}'
    html = '<script>window.__INITIAL_STATE__={"note":{"noteDetailMap":{"wirefixture01":{"note":' + note + '}}}};</script>'
    return {"url": URL, "now": NOW, "page_url": PAGE_URL, "html": html}


def verify(directory, check_git_source):
    path = directory / "php-wire-baseline.json"
    document = read_json(path)
    require((document["format_version"], document["baseline_commit"], document["baseline_tree"], document["php_version"], document["entry_json_flags"]) ==
            (1, BASE, TREE, "8.3.6", 2097472), "Wrong fixed PHP runtime/source/output flags")
    require(document["fixed_clock"] == {"unix_seconds": NOW, "rfc3339": CLOCK}, "Fixed clock changed")
    # Reuse the already immutable original source-hash list, without changing
    # or re-generating any of its inputs/expected output/producer files.
    original = read_json(DIRECTORY / "manifest.json")
    require((original["baseline_commit"], original["baseline_tree"], original["php_version"]) == (BASE, TREE, "8.3.6"), "Original PHP source provenance differs")
    sources = document["source_files"]
    require(sources == original["source_files"] and len(sources) == 14, "Fixed PHP source inventory/hash changed")
    require(len({entry["path"] for entry in sources}) == 14, "Duplicate source path")
    for source in sources:
        require(re.fullmatch(r"[0-9a-f]{64}", source["sha256"]) is not None, "Invalid source digest")
    producer_records = document["producer_files"]
    require(producer_records == [{"path": PRODUCER, "sha256": hashlib.sha256((ROOT / PRODUCER).read_bytes()).hexdigest()}], "Producer path/hash changed")
    if check_git_source:
        require(subprocess.check_output(["git", "rev-parse", BASE + "^{tree}"], cwd=ROOT, text=True).strip() == TREE, "Fixed PHP source tree changed")
        for source in sources:
            raw = subprocess.check_output(["git", "show", BASE + ":" + source["path"]], cwd=ROOT)
            require(hashlib.sha256(raw).hexdigest() == source["sha256"], "Fixed PHP source bytes changed: " + source["path"])
    transport = document["transport_substitution"]
    require(transport["disabled_native_functions"] == ["time", "curl_exec", "curl_multi_exec", "dns_get_record", "fsockopen", "pfsockopen", "stream_socket_client"], "Offline IO/time boundary changed")
    require(transport["recorded_fields"] == ["page URL sequence"], "Callback evidence scope changed")
    cases = document["cases"]
    wanted_ids = ["xiaohongshu-wire/" + spec[0] for spec in SPECS]
    require([case["id"] for case in cases] == wanted_ids and len(set(wanted_ids)) == 10, "Missing/reordered/duplicate wire case")
    require(document["parse_case_count"] == 10 and document["public_envelope_count"] == 20, "Wire inventory count changed")
    success = 0
    for case, spec in zip(cases, SPECS, strict=True):
        require(set(case) == {"id", "input", "expected"}, "Case fields changed")
        require(case["input"] == reviewed_input(spec), "Reviewed literal HTML/source/clock changed: " + case["id"])
        expected = case["expected"]
        require(set(expected) == {"legacy_wire", "legacy_response", "normal", "debug", "page_requests"}, "Observation fields changed")
        require(expected["page_requests"] == [PAGE_URL], "Parser did not consume exactly the allowed page callback")
        require(json.loads(expected["legacy_wire"], parse_float=Decimal) == expected["legacy_response"], "Saved legacy wire differs from its decode")
        require(expected["legacy_response"]["code"] == 200, "Wire fixture did not exercise a successful original parser")
        for view in ("normal", "debug"):
            response = expected[view]
            require(response["schema_version"] == "1.0" and response["code"] == "OK", "Wire fixture lost a successful unified response")
            data = response["data"]
            require(data["platform"] == "xiaohongshu" and data["source_url"] == URL and data["generated_at"] == CLOCK, "Public platform/source/clock changed")
            require(bool(data["items"]) and len({item["id"] for item in data["items"]}) == len(data["items"]), "Missing/duplicate public media identity")
        success += expected["normal"]["code"] == "OK"
    return {"passed": True, "baseline": BASE, "php_version": "8.3.6", "cases": len(cases), "parse_cases": len(cases),
            "public_envelopes": len(cases) * 2, "success_cases": success, "page_callbacks": len(cases), "media_requests": 0,
            "output_sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "git_source_checked": check_git_source}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, default=DIRECTORY)
    parser.add_argument("--check-git-source", action="store_true")
    args = parser.parse_args()
    print(json.dumps(verify(args.directory.resolve(), args.check_git_source)))


if __name__ == "__main__":
    main()
