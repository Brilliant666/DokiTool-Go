#!/usr/bin/env python3
"""Regenerate the integer oracle from the approved, unmodified PHP archive.

This is a development tool; Go tests consume only the frozen JSON. No platform
requests occur: the Parse harness replaces native cURL/get_headers with fixtures.
"""
from __future__ import annotations

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[5]
BASE = "665bd55134982b9fe107aa1cd00373ba8da8ff3c"
TREE = "93744d4d718c3299f82640d2888e93252106db70"
DISABLED = "curl_init,curl_setopt_array,curl_setopt,curl_exec,curl_getinfo,curl_close,curl_error,curl_errno,get_headers"
URL = "https://www.douyin.com/video/7628438784602389247"
DETAIL_URL = "https://api.amemv.com/aweme/v1/aweme/detail/?aweme_id=7628438784602389247&aid=6383&device_platform=web&version_code=190500&version_name=19.5.0"
NOW = 1793361600
SOURCES = (
    "backend/api/douyin/DouyinParser.php",
    "backend/lib/runtime.php",
    "backend/lib/douyin-media-probe.php",
    "backend/lib/media-probe.php",
    "backend/lib/weibo-media.php",
    "backend/lib/media-adapters.php",
    "backend/lib/media-contract.php",
    "backend/lib/media-metadata.php",
    "backend/lib/media-policy.php",
    "backend/lib/platforms.php",
    "backend/config/platforms.json",
    "docs/schema/unified-v1/unified-v1.schema.json",
)
PRODUCERS = (
    "backend/internal/platform/douyin/testdata/produce_integer.py",
    "scripts/go-migration/produce-douyin.php",
)


def dump(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def detail():
    # Keep extra nonempty: the shared harness re-packs legacy json_decode(...,
    # true), whose empty object/array ambiguity is outside this integer oracle.
    return {
        "aweme_id": "7628438784602389247",
        "desc": "PHP integer boundary fixture",
        "statistics": {"play_count": 0},
        "video": {"play_addr": {"width": 1920, "height": 1080,
                                "url_list": ["https://v5-test.douyinvod.com/integer.mp4"]}},
    }


def case(name, value, status=0):
    body = dump({"status_code": status, "aweme_detail": value})
    return {
        "id": name,
        "scenario": "Direct PHP integer cast is preserved before adapter normalization",
        "input": {
            "url": URL, "now": NOW, "detail": value,
            "http_responses": [{"method": "GET", "url": DETAIL_URL, "status": 200,
                                "headers": {"Content-Type": "application/json"},
                                "follow_redirects": True, "body": body}],
            "probe_results": [],
        },
    }


def inputs():
    cases = []
    for number in (9007199254740993, 9223372036854775800):
        for representation in (number, str(number)):
            raw = detail()
            raw["statistics"] = {"digg_count": representation, "play_count": 0,
                                 "share_count": 7.9, "comment_count": "0"}
            kind = "integer" if isinstance(representation, int) else "string"
            cases.append(case(f"statistics/{number}/{kind}", raw))
    for number in (9007199254740993, 9223372036854775800):
        for representation in (number, str(number)):
            raw = detail()
            raw["video"]["play_addr"]["data_size"] = representation
            kind = "integer" if isinstance(representation, int) else "string"
            cases.append(case(f"source-size/{number}/{kind}", raw))
    for value in (9007199254740993, "9007199254740993"):
        raw = detail()
        raw["video"]["bit_rate"] = [{"bit_rate": value, "play_addr": raw["video"]["play_addr"]}]
        kind = "integer" if isinstance(value, int) else "string"
        cases.append(case(f"representation-bitrate/{kind}", raw))
    for value in (9223372036854775800, "9223372036854775800", 18446744073709551616):
        kind = ("integer" if isinstance(value, int) else "string") + "-" + str(value)
        cases.append(case(f"status/{kind}", detail(), value))

    direct = []
    for name, value in (("int53", 9007199254740993), ("string53", "9007199254740993"),
                        ("int64", 9223372036854775800), ("string64", "9223372036854775800"),
                        ("string-fraction", "9007199254740993.75"), ("fraction", 7.9)):
        direct.append({"id": f"video-metadata/{name}", "operation": "video_metadata",
                       "input": {"width": value, "height": 1080, "bit_rate": value, "data_size": value}})
        direct.append({
            "id": f"audio-candidates/{name}", "operation": "audio_candidates",
            "input": {"entries": [{"audio_meta": {
                "media_type": "audio", "file_id": "audio-fixture",
                "url_list": ["https://v5-test.douyinvod.com/audio.m4a"],
                "bitrate": value, "size": value,
                "sub_info": dump({"audio_channels": value, "audio_sample_rate": value,
                                  "audio_profile": "aac"}),
            }}], "expires_at": "4102444800.9"},
        })
    assert len(cases) == 13 and len(direct) == 12
    assert len({entry["id"] for entry in cases + direct}) == 25
    return cases, direct


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--php", required=True, type=Path, help="PHP executable; respects caller PHPRC")
    parser.add_argument("--output", type=Path, default=Path(__file__).with_name("php-integer-boundaries.json"))
    args = parser.parse_args()
    php = str(args.php.resolve())
    env = dict(os.environ)
    tree = subprocess.check_output(["git", "rev-parse", BASE + "^{tree}"], cwd=ROOT, text=True).strip()
    if tree != TREE:
        raise RuntimeError("Approved PHP baseline tree differs")
    version = subprocess.check_output([php, "-r", "echo PHP_VERSION;"], env=env, text=True).strip()
    if version != "8.3.6":
        raise RuntimeError("This frozen oracle requires PHP 8.3.6; version changes require explicit review")
    cases, direct = inputs()
    archive = subprocess.check_output(["git", "archive", BASE], cwd=ROOT)
    with tempfile.TemporaryDirectory(prefix="dokitool-integer-php-") as folder:
        archived = Path(folder)
        with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
            tar.extractall(archived, filter="data")
        command = [php, "-d", "disable_functions=" + DISABLED, "-d", "allow_url_fopen=0"]
        produced = subprocess.run(
            command + [str(ROOT / "scripts/go-migration/produce-douyin.php"), str(archived)],
            input=dump({"cases": cases}), text=True, capture_output=True, env=env, check=True,
        )
        observed = json.loads(produced.stdout)["cases"]
        if [entry["id"] for entry in observed] != [entry["id"] for entry in cases]:
            raise RuntimeError("PHP Parse oracle inventory differs")
        for entry, actual in zip(cases, observed, strict=True):
            entry["expected"] = actual["expected"]
        reflection = archived / "integer-reflection.php"
        reflection.write_text("""<?php
require $argv[1].'/backend/api/douyin/DouyinParser.php';
require $argv[1].'/backend/lib/media-adapters.php';
$cases=json_decode(stream_get_contents(STDIN),true,512,JSON_THROW_ON_ERROR);
$parser=new DouyinParser(static fn()=>null);
foreach($cases as &$case){
    if($case['operation']==='video_metadata') $case['expected']=(new ReflectionMethod($parser,'videoMetadata'))->invoke($parser,$case['input']);
    else $case['expected']=(new ReflectionMethod($parser,'audioCandidates'))->invoke($parser,$case['input']['entries'],$case['input']['expires_at']);
}
echo json_encode($cases,JSON_UNESCAPED_SLASHES|JSON_UNESCAPED_UNICODE|JSON_THROW_ON_ERROR);
""", encoding="utf-8")
        produced = subprocess.run(command + [str(reflection), str(archived)], input=dump(direct),
                                  text=True, capture_output=True, env=env, check=True)
        observed = json.loads(produced.stdout)
        if [entry["id"] for entry in observed] != [entry["id"] for entry in direct]:
            raise RuntimeError("PHP private metadata oracle inventory differs")
        result = {
            "format_version": 1, "baseline_commit": BASE, "baseline_tree": TREE,
            "php_version": version, "fixed_clock": NOW,
            "parse_case_count": 13, "public_envelope_count": 26, "direct_case_count": 12,
            "evidence_scope": "Synthetic inputs executed through unmodified archived PHP. Public cases use real Parse, formatter, adapter and normal/debug policy with native network functions disabled. Direct cases reflect actual private videoMetadata/audioCandidates; no Go-derived expectations.",
            "source_sha256": {name: sha(archived / name) for name in SOURCES},
            "producer_sha256": {name: sha(ROOT / name) for name in PRODUCERS},
            "cases": cases, "direct_cases": observed,
        }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"parse_cases": 13, "public_responses": 26, "direct_cases": 12,
                      "baseline_commit": BASE, "sha256": sha(args.output)}))


if __name__ == "__main__":
    main()
