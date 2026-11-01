#!/usr/bin/env python3
"""Verify the frozen PHP Weibo HTTP/signing oracle without PHP, Go or network."""
from __future__ import annotations

import argparse
import base64
from datetime import datetime, timezone
import hashlib
import hmac
import importlib.util
import json
import pathlib
import re
import subprocess
from urllib.parse import parse_qs, urlsplit

ROOT = pathlib.Path(__file__).resolve().parents[2]
DIRECTORY = ROOT / "backend/internal/platform/weibo/testdata"
STATUS = {"OK": 200, "CONTENT_UNAVAILABLE": 404, "UPSTREAM_INVALID_RESPONSE": 502,
          "UPSTREAM_UNAVAILABLE": 502, "SERVICE_NOT_CONFIGURED": 503}
USER_AGENT = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
              "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def utc(timestamp):
    return datetime.fromtimestamp(timestamp, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def all_strings(value):
    if isinstance(value, str):
        yield value
    elif isinstance(value, dict):
        for child in value.values():
            yield from all_strings(child)
    elif isinstance(value, list):
        for child in value:
            yield from all_strings(child)


def decode_base64(value):
    require(isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9_-]+", value), "Invalid ticket base64url")
    decoded = base64.b64decode(value + "=" * (-len(value) % 4), altchars=b"-_", validate=True)
    require(base64.urlsafe_b64encode(decoded).decode().rstrip("=") == value, "Noncanonical ticket encoding")
    return decoded


def checked_ticket(source, raw_urls, key, now):
    url = source["url"]
    require(url.startswith("/api/media/weibo?ticket="), "Signed source left the dedicated media route")
    ticket = url.removeprefix("/api/media/weibo?ticket=")
    require(len(ticket) <= 8192 and ticket.count(".") == 1, "Invalid ticket size or shape")
    encoded, signature = ticket.split(".")
    require(len(signature) == 43, "Invalid SHA-256 signature size")
    require(32 <= len(key.encode()) <= 512, "Successful signing used an invalid key")
    expected_mac = hmac.new(key.encode(), encoded.encode(), hashlib.sha256).digest()
    require(hmac.compare_digest(decode_base64(signature), expected_mac), "Ticket HMAC does not match the fixed key")
    payload = json.loads(decode_base64(encoded))
    require(list(payload) == ["v", "u", "iat", "exp"], "Ticket payload field/order boundary changed")
    require(type(payload["v"]) is int and payload["v"] == 1, "Ticket version changed")
    require(type(payload["iat"]) is int and payload["iat"] == now, "Ticket issued-at escaped the fixed case clock")
    require(type(payload["exp"]) is int and payload["exp"] == now + 1800, "Ticket TTL escaped the frozen 30-minute boundary")
    raw = payload["u"]
    require(raw in raw_urls, "Ticket invented a source outside the frozen PHP candidates")
    parts = urlsplit(raw)
    require(parts.scheme == "https" and parts.netloc in {"f.video.weibocdn.com", "f.video.weibocdn.com:443"}
            and parts.path.startswith("/") and not parts.fragment, "Ticket authorized an unsupported media origin")
    require(len(raw) <= 4096 and raw.isascii() and not re.search(r"[\\\x00-\x20\x7f]", raw), "Ticket source bypassed the URL character budget")
    require(source.get("access") == "relay" and source.get("availability", "available") == "available", "Signed source is not a usable relay")
    require(source.get("expires_at") == utc(payload["exp"]), "Source expiry disagrees with its ticket")
    return raw


def check_refresh(url, endpoint, original, debug, item_id=None):
    parsed = urlsplit(url)
    require(not parsed.scheme and not parsed.netloc and not parsed.fragment and parsed.path == endpoint, "Refresh/resolve route changed")
    wanted = {"schema_version": ["1.0"], "url": [original]}
    if debug:
        wanted["debug"] = ["1"]
    if item_id is not None:
        wanted["item_id"] = [item_id]
    require(parse_qs(parsed.query, keep_blank_values=True, strict_parsing=True) == wanted, "Refresh/resolve context changed")


def check_success(case, view, producer):
    given, observed = case["input"], case["expected"]
    envelope = observed[view]
    data = envelope["data"]
    require(data["platform"] == "weibo" and data["source_url"] == given["url"], "Public source identity changed")
    require(type(given["now"]) is int and data["generated_at"] == utc(given["now"]), "Public response escaped its fixed clock")
    raw_urls = {entry["url"] for entry in observed["legacy_response"]["data"]["video_backup"]}
    upstream_strings = set()
    for step in given["http_responses"]:
        try:
            upstream_strings.update(all_strings(json.loads(step["body"])))
        except json.JSONDecodeError:
            pass  # The anonymous-session tape also contains HTML/JSONP.
    require(raw_urls <= upstream_strings, "PHP candidates contain a URL absent from controlled upstream input")
    require(data["items"] and len({item["id"] for item in data["items"]}) == len(data["items"]), "Missing/duplicate media items")
    check_refresh(data["refresh_url"], "/api/parse", given["url"], view == "debug")
    signed = 0
    identity = {}
    for item in data["items"]:
        check_refresh(item["resolve_url"], "/api/resolve", given["url"], view == "debug", item["id"])
        require(len({v["id"] for v in item["variants"]}) == len(item["variants"]), "Duplicate variant identity")
        available = {}
        for variant in item["variants"]:
            delivery = variant["delivery"]
            require(delivery["kind"] == "file", "Frozen Weibo oracle unexpectedly introduced a new delivery kind")
            sources = delivery["sources"]
            require(len({s["id"] for s in sources}) == len(sources), "Duplicate source identity")
            for source in sources:
                if source["url"].startswith("/api/media/weibo?"):
                    raw = checked_ticket(source, raw_urls, given["key"], given["now"])
                    available[(variant["id"], source["id"])] = source
                    identity[urlsplit(raw).path] = (item["id"], variant["id"], source["id"])
                    signed += 1
                else:
                    require(source["url"] in raw_urls and source.get("availability") == "unavailable", "Unsigned source remained usable or was invented")
                    require("access" not in source and "expires_at" not in source, "Unsupported source inherited signing facts")
        selected = item["default"]
        source = available.get((selected["variant_id"], selected["source_id"]))
        require(source is not None, "Default does not identify a usable signed source")
        for field in ("url", "access", "expires_at"):
            require(selected.get(field) == source.get(field), "Default disagrees with its signed source: " + field)
    if given["route"] == "resolve":
        require(data.get("default_item_id") == given["item_id"], "Resolve lost the requested item identity")
    require(signed > 0, "Successful response contains no signed sources")
    return signed, identity


def verify(directory, check_git_source):
    spec = importlib.util.spec_from_file_location("weibo_signed_producer", ROOT / "scripts/go-migration/produce-weibo-signed.py")
    producer = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(producer)
    manifest = json.loads((directory / "signed-manifest.json").read_text(encoding="utf-8"))
    require((manifest["format_version"], manifest["baseline_commit"], manifest["baseline_tree"], manifest["php_version"]) ==
            (1, producer.BASE, producer.TREE, "8.3.6"), "Wrong PHP source/runtime boundary")
    require(set(manifest["files"]) == {"php-signed-baseline.json"}, "Signed fixture inventory changed")
    require(list(manifest["sources"]) == producer.SOURCES, "PHP source inventory changed")
    require(list(manifest["producers"]) == producer.PRODUCERS, "Producer inventory changed")
    require(list(manifest["inputs"]) == [str((DIRECTORY / "signed-inputs.json").relative_to(ROOT))], "Reviewed input inventory changed")
    for path, expected in manifest["files"].items():
        require(sha(directory / path) == expected, "Frozen expected hash mismatch: " + path)
    for group in ("producers", "inputs"):
        for path, expected in manifest[group].items():
            require(sha(ROOT / path) == expected, "Producer/input hash mismatch: " + path)
    for expected in manifest["sources"].values():
        require(re.fullmatch(r"[0-9a-f]{64}", expected), "Invalid frozen source digest")
    if check_git_source:
        tree = subprocess.check_output(["git", "rev-parse", producer.BASE + "^{tree}"], cwd=ROOT, text=True).strip()
        require(tree == producer.TREE, "Pinned PHP source tree changed")
        for path, expected in manifest["sources"].items():
            content = subprocess.check_output(["git", "show", producer.BASE + ":" + path], cwd=ROOT)
            require(hashlib.sha256(content).hexdigest() == expected, "Pinned PHP source hash mismatch: " + path)
    expected_inputs = producer.input_document()
    require((DIRECTORY / "signed-inputs.json").read_bytes() == producer.dump(expected_inputs).encode(), "Reviewed inputs differ from producer")
    document = json.loads((directory / "php-signed-baseline.json").read_text(encoding="utf-8"))
    require({k: v for k, v in document.items() if k != "cases"} == {k: v for k, v in expected_inputs.items() if k != "cases"}, "Signed fixture provenance changed")
    require(document["fixed_clock"] == utc(producer.NOW), "Frozen root clock changed")
    cases = document["cases"]
    require(len(cases) == len(expected_inputs["cases"]) == 35 and len({c["id"] for c in cases}) == len(cases), "Duplicate/missing signed cases")
    require(manifest["cases"] == len(cases) and manifest["public_envelopes"] == len(cases) * 2, "Manifest counts changed")
    requests = success = signed = 0
    renewal = {}
    for case, reviewed in zip(cases, expected_inputs["cases"], strict=True):
        require({k: v for k, v in case.items() if k != "expected"} == reviewed, "Frozen input changed: " + case["id"])
        given, observed = case["input"], case["expected"]
        require(set(observed) == {"legacy_response", "http_requests", "normal", "normal_status", "debug", "debug_status"}, "Signed observation fields changed")
        tape = given["http_responses"]
        require(len(observed["http_requests"]) == len(tape), "Unconsumed HTTP tape: " + case["id"])
        for request, step in zip(observed["http_requests"], tape, strict=True):
            require(set(request) == {"url", "method", "post", "headers"}, "Recorded request fields changed")
            require(all(request[field] == step[field] for field in ("url", "method", "post")), "HTTP request sequence changed: " + case["id"])
            parts = urlsplit(request["url"])
            require(parts.scheme == "https" and parts.netloc in {"weibo.com", "passport.weibo.com"} and not parts.fragment, "Metadata request escaped official HTTPS hosts")
            referer = producer.TV if parts.path == "/tv/api/component" else "https://weibo.com/"
            if parts.netloc == "passport.weibo.com":
                referer = tape[0]["url"]
            require(request["headers"] == {"referer": referer, "accept": "application/json, text/plain, */*", "user-agent": USER_AGENT}, "Explicit PHP request headers changed")
        requests += len(tape)
        require(observed["normal"]["code"] == observed["debug"]["code"], "Normal/debug public result differs")
        identities = {}
        for view in ("normal", "debug"):
            envelope = observed[view]
            code = envelope["code"]
            require(envelope["schema_version"] == "1.0" and code in STATUS, "Unexpected public schema/status")
            require(type(observed[view + "_status"]) is int and observed[view + "_status"] == STATUS[code], "Public HTTP status disagrees with its envelope")
            if code == "OK":
                count, identity = check_success(case, view, producer)
                signed += count
                identities[view] = identity
            else:
                require(set(envelope) == {"schema_version", "code", "error"} and envelope["error"]["code"] == code, "Error response leaked successful data or changed its code")
        if observed["normal"]["code"] == "OK":
            success += 1
            require(identities["normal"] == identities["debug"], "Normal/debug source identity differs")
            normal_items = observed["normal"]["data"]["items"]
            debug_items = observed["debug"]["data"]["items"]
            require([i["default"] for i in normal_items] == [i["default"] for i in debug_items], "Normal/debug chose different defaults")
            if case["id"] in {"weibo/renewal/before/parse", "weibo/renewal/after/parse"}:
                renewal[case["id"]] = identities["normal"]
    require(len(renewal) == 2 and renewal["weibo/renewal/before/parse"] == renewal["weibo/renewal/after/parse"], "Renewal changed stable media identities")
    return {"passed": True, "baseline": producer.BASE, "php_version": manifest["php_version"],
            "cases": len(cases), "parse_cases": len(cases), "public_envelopes": len(cases) * 2,
            "success_cases": success, "http_requests": requests, "signed_sources": signed,
            "git_source_checked": check_git_source}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check-git-source", action="store_true")
    parser.add_argument("--directory", type=pathlib.Path, default=DIRECTORY)
    args = parser.parse_args()
    print(json.dumps(verify(args.directory.resolve(), args.check_git_source)))


if __name__ == "__main__":
    main()
