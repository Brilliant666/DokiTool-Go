#!/usr/bin/env python3
"""Verify the fixed PHP Zuiyou product/HTTP oracle without platform traffic."""
from __future__ import annotations

import argparse
import copy
import hashlib
import importlib.util
import json
import pathlib
import re
import subprocess
from urllib.parse import parse_qs, urlsplit

ROOT = pathlib.Path(__file__).resolve().parents[2]
DIRECTORY = ROOT / "backend/internal/platform/zuiyou/testdata"
STATUS = {"OK": 200, "INVALID_INPUT": 400, "CONTENT_UNAVAILABLE": 404,
          "UPSTREAM_INVALID_RESPONSE": 502}
USER_AGENT = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
              "(KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def all_strings(value):
    if isinstance(value, str):
        yield value
    elif isinstance(value, dict):
        for child in value.values():
            yield from all_strings(child)
    elif isinstance(value, list):
        for child in value:
            yield from all_strings(child)


def check_route(raw, endpoint, source, debug, item_id=None):
    parsed = urlsplit(raw)
    require(not parsed.scheme and not parsed.netloc and not parsed.fragment and parsed.path == endpoint,
            "Refresh/resolve endpoint changed")
    wanted = {"schema_version": ["1.0"], "url": [source]}
    if debug:
        wanted["debug"] = ["1"]
    if item_id is not None:
        wanted["item_id"] = [item_id]
    require(parse_qs(parsed.query, keep_blank_values=True, strict_parsing=True) == wanted,
            "Refresh/resolve source, debug or selected item context changed")


def video_id(key):
    return "video-" + hashlib.sha256(str(key).encode()).hexdigest()[:24]


def check_success(case, view, producer):
    given, expected = case["input"], case["expected"]
    data = expected[view]["data"]
    require(data["platform"] == "zuiyou" and data["source_url"] == given["url"], "Public source identity changed")
    require(given["now"] == producer.NOW and data["generated_at"] == producer.CLOCK, "Fixed public clock changed")
    check_route(data["refresh_url"], "/api/parse", given["url"], view == "debug")
    upstream_strings = set()
    for step in given["http_responses"]:
        if step["method"] == "POST" and "error" not in step:
            upstream_strings.update(all_strings(json.loads(step["body"])))
    legacy_strings = set(all_strings(expected["legacy_response"]))
    items = data["items"]
    require(items and len({item["id"] for item in items}) == len(items), "Missing/duplicate item IDs")
    item_ids = {item["id"] for item in items}
    require("default_item_id" not in data or data["default_item_id"] in item_ids, "Default item reference is missing")
    if given["route"] == "resolve":
        require(data.get("default_item_id") == given["item_id"], "Resolve did not select the caller's requested surviving item")
    legacy_video_ids = set()
    for index, candidate in enumerate(expected["legacy_response"]["data"]["private_videos"]):
        if isinstance(candidate, dict):
            key = candidate.get("private_video_id")
            legacy_video_ids.add(video_id(key) if type(key) in (int, str) else "video-" + str(index + 1))
    sources_count = 0
    for item in items:
        check_route(item["resolve_url"], "/api/resolve", given["url"], view == "debug", item["id"])
        if item["type"] == "video":
            require(item["id"] in legacy_video_ids, "Video item identity no longer derives from the original mapping key")
        variants = item["variants"]
        require(variants and len({variant["id"] for variant in variants}) == len(variants), "Missing/duplicate variant IDs")
        available = {}
        for variant in variants:
            delivery = variant["delivery"]
            require(delivery["kind"] in {"file", "hls", "dash"}, "Unexpected Zuiyou delivery capability")
            sources = delivery["sources"]
            require(sources and len({source.get("id", index) for index, source in enumerate(sources)}) == len(sources),
                    "Missing/duplicate source identity")
            for index, source in enumerate(sources):
                raw = source["url"]
                require(raw in legacy_strings and raw in upstream_strings, "Source URL was invented outside the PHP/upstream candidates")
                parsed = urlsplit(raw)
                require(parsed.scheme == "https" and parsed.hostname and not parsed.username and not parsed.fragment,
                        "Unexpected source URL shape")
                if delivery["kind"] == "file" and source.get("availability", "available") == "available":
                    available[(variant["id"], source.get("id"))] = source
                sources_count += 1
        selected = item["default"]
        if selected is None:
            require(not available and item.get("default_unavailable_reason") == "processing_required",
                    "Unsupported delivery lost its explicit unavailable default")
        else:
            source = available.get((selected["variant_id"], selected.get("source_id")))
            require(source is not None and selected["url"] == source["url"], "Default does not reference a usable complete file")
            require("default_unavailable_reason" not in item, "Usable default retained an unavailable reason")
    for group in data.get("groups", []):
        require(group["kind"] == "album" and set(group["item_ids"]) <= item_ids, "Album contains a missing item reference")
    return sources_count


def check_identity_cases(cases):
    by_id = {case["id"].removeprefix("zuiyou/"): case for case in cases}

    def data(name):
        return by_id[name]["expected"]["normal"]["data"]

    def ids(name):
        return [item["id"] for item in data(name)["items"]]

    first, second = video_id("42"), video_id("11")
    require(ids("video/img-order-map-identity") == [first, second] and ids("video/reversed-imgs") == [second, first],
            "imgs no longer controls video display order")
    for name in ("identity/first-missing", "identity/first-invalid", "resolve/second-survives"):
        require(ids(name) == [second], "Removing the first entry renamed/lost the second item: " + name)
    require(ids("resolve/second-original") == [first, second] and data("resolve/second-original")["default_item_id"] == second,
            "Resolve reordered the inventory instead of selecting the existing item")
    for name in ("resolve/selected-missing", "resolve/unknown"):
        require(by_id[name]["expected"]["normal"]["code"] == "CONTENT_UNAVAILABLE", "A missing selected item silently fell back")
    require(ids("video/unlisted-map-entry") == [first, second, video_id("78")], "Unlisted mapping entries lost source order")
    require(ids("video/duplicate-img-id") == [first, second], "Duplicate image IDs duplicated video items")
    require(ids("identity/numeric-img-id") == [first, second], "Numeric upstream IDs changed video identity")
    require(ids("identity/list-video-keys") == [video_id("1"), video_id("0")], "List-backed videos lost original numeric keys")
    require(ids("entry/exact-large-key") == [video_id("9007199254740993")], "Entry 480 lost exact identity above 2^53")
    numeric = data("entry/numeric-check-480")
    require(numeric["title"] == "42" and numeric["author"]["name"] == "123", "Original JSON_NUMERIC_CHECK boundary changed")


def verify(directory, check_git_source):
    spec = importlib.util.spec_from_file_location("zuiyou_producer", ROOT / "scripts/go-migration/produce-zuiyou.py")
    producer = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(producer)
    manifest = json.loads((directory / "manifest.json").read_text(encoding="utf-8"))
    require((manifest["format_version"], manifest["baseline_commit"], manifest["baseline_tree"], manifest["php_version"], manifest["entry_json_flags"]) ==
            (1, producer.BASE, producer.TREE, "8.3.6", producer.ENTRY_FLAGS), "Wrong PHP baseline/runtime/entry boundary")
    require(set(manifest["files"]) == {"php-baseline.json"}, "Fixture inventory changed")
    for path, expected in manifest["files"].items():
        require(sha(directory / path) == expected, "Frozen expected hash mismatch: " + path)
    require([entry["path"] for entry in manifest["source_files"]] == producer.SOURCES, "Frozen source inventory changed")
    require([entry["path"] for entry in manifest["producer_files"]] == producer.PRODUCERS, "Producer inventory changed")
    require([entry["path"] for entry in manifest["input_files"]] == [str((DIRECTORY / "inputs.json").relative_to(ROOT))], "Input inventory changed")
    for entry in manifest["source_files"] + manifest["producer_files"] + manifest["input_files"]:
        require(re.fullmatch(r"[0-9a-f]{64}", entry["sha256"]), "Invalid manifest digest")
    for entry in manifest["producer_files"] + manifest["input_files"]:
        require(sha(ROOT / entry["path"]) == entry["sha256"], "Input/producer integrity mismatch: " + entry["path"])
    if check_git_source:
        tree = subprocess.check_output(["git", "rev-parse", producer.BASE + "^{tree}"], cwd=ROOT, text=True).strip()
        require(tree == producer.TREE, "Pinned source tree changed")
        for entry in manifest["source_files"]:
            content = subprocess.check_output(["git", "show", producer.BASE + ":" + entry["path"]], cwd=ROOT)
            require(hashlib.sha256(content).hexdigest() == entry["sha256"], "Pinned source hash mismatch: " + entry["path"])
    reviewed = producer.input_document()
    require((DIRECTORY / "inputs.json").read_bytes() == producer.dump(reviewed).encode(), "Reviewed inputs differ from producer")
    document = json.loads((directory / "php-baseline.json").read_text(encoding="utf-8"))
    require({key: value for key, value in document.items() if key != "cases"} == {key: value for key, value in reviewed.items() if key != "cases"},
            "Oracle provenance changed")
    require(manifest["fixed_clock"] == reviewed["fixed_clock"], "Manifest clock changed")
    cases = document["cases"]
    require(len(cases) == len(reviewed["cases"]) == 48 and len({case["id"] for case in cases}) == 48, "Duplicate/missing Zuiyou cases")
    require(manifest["parse_case_count"] == len(cases) and manifest["public_envelope_count"] == len(cases) * 2, "Manifest counts changed")
    requests = success = sources = 0
    for case, original in zip(cases, reviewed["cases"], strict=True):
        require({key: value for key, value in case.items() if key != "expected"} == original, "Input changed: " + case["id"])
        given, expected = case["input"], case["expected"]
        require(set(expected) == {"parser_response", "legacy_response", "http_requests", "php_warnings", "normal", "normal_status",
                                 "normal_forwarded", "debug", "debug_status", "debug_forwarded"}, "Observation inventory changed")
        tape = given["http_responses"]
        require(len(expected["http_requests"]) == len(tape) <= 2, "Unconsumed or unbounded request tape")
        for index, (request, step) in enumerate(zip(expected["http_requests"], tape, strict=True)):
            require(set(request) == {"url", "method", "headers", "body", "follow_redirects", "timeout_seconds"}, "Request fields changed")
            require(all(request[key] == step[key] for key in ("url", "method", "follow_redirects")), "Request order changed")
            if request["method"] == "HEAD":
                require(index == 0 and request["url"] == given["url"] and not request["follow_redirects"] and request["timeout_seconds"] == 12,
                        "Share resolution is no longer one bounded HEAD to the supplied entry")
                require(request["headers"] == {"user-agent": USER_AGENT} and request["body"] == "", "Explicit PHP HEAD headers/body changed")
            else:
                require(request["method"] == "POST" and request["url"] == producer.API and request["follow_redirects"] and request["timeout_seconds"] == 20,
                        "Detail request escaped the fixed API")
                require(request["headers"] == {"content-type": "application/json", "accept": "application/json", "user-agent": USER_AGENT},
                        "Explicit PHP API request headers changed")
                body = json.loads(request["body"])
                require(list(body) == ["pid", "h_av"] and type(body["pid"]) is int and 1 <= body["pid"] <= 2**63 - 1
                        and body["h_av"] == "5.2.13.011", "Original PHP pid cast/API body changed")
                require(request["body"] == json.dumps(body, separators=(",", ":")), "API body encoding changed")
        requests += len(tape)
        warnings = ["cURL error: Synthetic transport failure"] if case["id"] == "zuiyou/api/transport" else []
        require(expected["php_warnings"] == warnings, "Unexpected PHP parser warning")
        forwarded = case["id"] != "zuiyou/entry/invalid-url"
        require(expected["normal_forwarded"] is forwarded and expected["debug_forwarded"] is forwarded, "Public handler forwarding changed")
        require(expected["normal"]["code"] == expected["debug"]["code"], "Normal/debug status differs")
        for view in ("normal", "debug"):
            envelope = expected[view]
            code = envelope["code"]
            require(envelope["schema_version"] == "1.0" and code in STATUS, "Unexpected public schema/code")
            require(type(expected[view + "_status"]) is int and expected[view + "_status"] == STATUS[code], "HTTP status disagrees with the envelope")
            if code == "OK":
                sources += check_success(case, view, producer)
            else:
                require(set(envelope) == {"schema_version", "code", "error"} and envelope["error"]["code"] == code,
                        "Public error leaked successful data or changed code")
        if expected["normal"]["code"] == "OK":
            success += 1
            normal, debug = copy.deepcopy(expected["normal"]["data"]), copy.deepcopy(expected["debug"]["data"])
            for data in (normal, debug):
                data.pop("refresh_url")
                for item in data["items"]:
                    item.pop("resolve_url")
            require(normal == debug, "Debug changed the frozen inventory, identities, quality or defaults")
    check_identity_cases(cases)
    return {"passed": True, "baseline": producer.BASE, "php_version": manifest["php_version"],
            "cases": len(cases), "parse_cases": len(cases), "public_envelopes": len(cases) * 2,
            "success_cases": success, "http_requests": requests, "public_sources": sources,
            "git_source_checked": check_git_source}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check-git-source", action="store_true")
    parser.add_argument("--directory", type=pathlib.Path, default=DIRECTORY)
    args = parser.parse_args()
    print(json.dumps(verify(args.directory.resolve(), args.check_git_source)))


if __name__ == "__main__":
    main()
