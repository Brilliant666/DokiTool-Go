#!/usr/bin/env python3
"""Freeze original PHP Zuiyou parsing, entry-480, adapter and public HTTP paths."""
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

ROOT = Path(__file__).resolve().parents[2]
BASE = "25beaee107f8c1a208ced9deb3c637068445e316"
TREE = "e4b9d40f5e6c12a86fff629c105553886b434dcd"
DIRECTORY = ROOT / "backend/internal/platform/zuiyou/testdata"
NOW, CLOCK = 1793361600, "2026-10-30T12:00:00Z"
PID = "123456789"
URL = "https://www.izuiyou.com/detail/" + PID
SHORT = "https://share.izuiyou.com/synthetic-short"
API = "https://share.xiaochuankeji.cn/planck/share/post/detail_h5"
ENTRY_FLAGS = 480
DISABLED = "time,curl_init,curl_setopt_array,curl_setopt,curl_exec,curl_getinfo,curl_close,curl_error,curl_errno,get_headers"
PRODUCERS = ["scripts/go-migration/produce-zuiyou.py", "scripts/go-migration/produce-zuiyou.php"]
SOURCES = ["backend/api/zuiyou.php", "backend/lib/runtime.php", "backend/lib/gateway.php",
           "backend/lib/platforms.php", "backend/lib/media-adapters.php", "backend/lib/media-contract.php",
           "backend/lib/media-metadata.php", "backend/lib/media-policy.php", "backend/lib/weibo-media.php",
           "backend/config/platforms.json", "backend/tests/parser-contract.php",
           "backend/tests/unified-review-platforms.php", "docs/schema/unified-v1/unified-v1.schema.json"]


def dump(value):
    return json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n"


def compact(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def video(name, **facts):
    return {"url": "https://media.example/zuiyou-" + name + ".mp4?Expires=4102444800&token=synthetic&keep=a%2Bb",
            "width": 1920, "height": 1080, "size": 5000000, "fps": 30, "codec": "h264", **facts}


def picture(key, **sizes):
    return {"id": key, "urls": sizes or {
        "origin": {"urls": ["https://images.example/" + str(key) + "-original.jpg"]},
        "540_webp": {"urls": ["https://images.example/" + str(key) + "-preview.webp"]}}}


def post():
    return {"content": "最右产品迁移 A+B 中文😀", "member": {"name": "合成作者", "avatar_urls": {
        "origin": {"urls": ["https://images.example/zuiyou-avatar.jpg"]}}},
        "imgs": [picture("42"), picture("11")],
        "videos": {"11": video("second"), "42": video("first")}}


def response(url=API, *, data=None, body=None, status=200, method="POST", follow=True, error=None):
    result = {"url": url, "method": method, "follow_redirects": follow, "status": status,
              "body": compact(data) if body is None else body}
    if error is not None:
        result["error"] = error
    return result


def head(location=None, *, status=302, url=SHORT, error=None):
    headers = "HTTP/1.1 " + str(status) + " Synthetic\r\n"
    if location is not None:
        headers += "Location: " + location + "\r\n"
    return response(url, method="HEAD", follow=False, status=status, body=headers + "\r\n", error=error)


def item_id(key):
    # This is the caller's previously stored public ID, not a Go-produced expectation.
    return "video-" + hashlib.sha256(key.encode()).hexdigest()[:24]


def input_document():
    cases = []

    def add(name, *, work=None, url=URL, replies=None, route="parse", item=None, note=""):
        if replies is None:
            replies = [response(data={"data": {"post": post() if work is None else work}})]
        cases.append({"id": "zuiyou/" + name, "purpose": note,
                      "input": {"url": url, "now": NOW, "route": route, "item_id": item,
                                "http_responses": copy.deepcopy(replies)}})

    add("video/img-order-map-identity", note="imgs order overrides reversed videos map; video map keys remain stable item identity")
    one = post(); one["imgs"] = one["imgs"][:1]; one["videos"].pop("11")
    add("video/single", work=one)
    reverse = post(); reverse["imgs"].reverse()
    add("video/reversed-imgs", work=reverse)
    missing = post(); missing["videos"].pop("42")
    add("identity/first-missing", work=missing, note="Removing the first media cannot rename the surviving second item")
    invalid = post(); invalid["videos"]["42"]["url"] = ""
    add("identity/first-invalid", work=invalid)
    absent = post(); absent["videos"].pop("11")
    add("resolve/second-survives", work=missing, route="resolve", item=item_id("11"))
    add("resolve/selected-missing", work=absent, route="resolve", item=item_id("11"))
    add("resolve/second-original", route="resolve", item=item_id("11"))
    add("resolve/unknown", route="resolve", item="video-missing")
    unlisted = post(); unlisted["videos"] = {"78": video("third"), **unlisted["videos"]}
    add("video/unlisted-map-entry", work=unlisted)
    duplicate = post(); duplicate["imgs"] += duplicate["imgs"][:1]
    add("video/duplicate-img-id", work=duplicate)
    numeric = post(); numeric["imgs"][0]["id"] = 42; numeric["imgs"][1]["id"] = 11
    add("identity/numeric-img-id", work=numeric)
    listed = post(); listed["videos"] = [video("zero"), video("one")]; listed["imgs"] = [picture(1), picture(0)]
    add("identity/list-video-keys", work=listed)
    big = post(); big["imgs"] = [picture("9007199254740993")]; big["videos"] = {"9007199254740993": video("big-key")}
    add("entry/exact-large-key", work=big)
    numeric_text = copy.deepcopy(one); numeric_text["content"] = "00042"; numeric_text["member"]["name"] = "00123"
    numeric_text["videos"]["42"].update(width="1920", height="1080", size="5000000", fps="29.97")
    add("entry/numeric-check-480", work=numeric_text)
    rich = copy.deepcopy(one); rich["videos"]["42"]["sources"] = [rich["videos"]["42"]["url"], "https://media.example/zuiyou-alternate.mp4"]
    add("video/multiple-sources", work=rich)
    for kind in ("m3u8", "mpd"):
        adaptive = copy.deepcopy(one); adaptive["videos"]["42"]["url"] = "https://media.example/adaptive." + kind
        add("video/unsupported-" + kind, work=adaptive)
    album = {"content": "图集", "member": {"name": "图片作者"}, "imgs": [picture("a"), picture("b")]}
    add("image/original-album", work=album)
    add("image/single", work={"imgs": [picture("only")]})
    add("resolve/image", work=album, route="resolve", item="image-2")
    add("image/fallback-order", work={"imgs": [picture("large", large={"urls": ["https://images.example/large.jpg"]}),
        picture("fallback", thumb={"urls": ["https://images.example/first.jpg"]}, other={"urls": ["https://images.example/second.jpg"]})]})
    add("image/duplicate-and-invalid", work={"imgs": [picture("a"), picture("a"), {"urls": {"origin": {"urls": ["javascript:bad"]}}}]})
    add("image/empty-origin-no-fallback", work={"imgs": [picture("bad", origin={}, large={"urls": ["https://images.example/not-selected.jpg"]}), picture("good")]})
    add("media/missing-video-not-image-success", work={"imgs": [picture("42")], "videos": {"42": {"url": ""}}})
    add("media/empty-post", work={})
    add("media/no-media", work={"content": "no media"})
    add("media/scalar-post", replies=[response(data={"data": {"post": "invalid"}})])
    add("api/invalid-json", replies=[response(body="{broken")])
    add("api/json-scalar", replies=[response(data="invalid")])
    add("api/transport", replies=[response(status=0, body="", error="Synthetic transport failure")])
    add("api/http-status-not-parser-code", replies=[response(status=500, data={"data": {"post": one}})])
    for name, url in [
        ("query-pid", "https://share.izuiyou.com/post?pid=" + PID),
        ("query-final-scalar", "https://share.izuiyou.com/post?pid=1&pid=" + PID),
        ("query-array-path-fallback", URL + "?pid[]=987654321"),
        ("unanchored-detail-path", "https://www.izuiyou.com/a/detail/" + PID + "suffix"),
        ("nineteen-digit-overflow", "https://www.izuiyou.com/post?pid=9223372036854775808"),
    ]:
        add("entry/" + name, url=url)
    for name, link in [("invalid-pid", "?pid=-1"), ("leading-zero", "?pid=0123456"), ("nondecimal", "?pid=1e6")]:
        add("entry/" + name, url="https://share.izuiyou.com/post" + link, replies=[])
    for name, location in [("absolute", "https://share.izuiyou.com/post?pid=" + PID), ("relative", "/post?pid=" + PID),
                           ("foreign-location-parameter-only", "https://example.invalid/post?pid=" + PID)]:
        add("short/" + name, url=SHORT, replies=[head(location), response(data={"data": {"post": one}})],
            note="Location is used only for pid extraction; the only following request is the fixed detail API")
    add("short/does-not-follow-second-hop", url=SHORT, replies=[head("https://share.izuiyou.com/another-short")])
    add("short/nonredirect", url=SHORT, replies=[head(status=200)])
    add("short/transport", url=SHORT, replies=[head(status=0, error="Synthetic transport failure")])
    add("entry/zero-pid-needs-share-lookup", url=SHORT + "?pid=0", replies=[head(status=200, url=SHORT + "?pid=0")])
    add("entry/invalid-url", url="not a URL", replies=[])
    return {"format_version": 1, "baseline_commit": BASE, "baseline_tree": TREE,
            "fixed_clock": {"rfc3339": CLOCK, "unix_seconds": NOW}, "cases": cases}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--php", type=Path)
    parser.add_argument("--repo", type=Path, default=ROOT)
    parser.add_argument("--output", type=Path, default=DIRECTORY)
    parser.add_argument("--write-inputs", action="store_true")
    args = parser.parse_args()
    document = input_document(); output = args.output.resolve(); output.mkdir(parents=True, exist_ok=True)
    if args.write_inputs:
        (output / "inputs.json").write_text(dump(document), encoding="utf-8")
        print(compact({"inputs_only": True, "cases": len(document["cases"])})); return
    require(args.php is not None, "A fixed PHP 8.3.6 executable is required")
    frozen_inputs = DIRECTORY / "inputs.json"
    require(frozen_inputs.read_bytes() == dump(document).encode(), "Reviewed Zuiyou inputs changed")
    runtime = subprocess.check_output([str(args.php.resolve()), "-r", "echo PHP_VERSION;"], text=True).strip()
    require(runtime == "8.3.6", "Expected PHP 8.3.6")
    repo = args.repo.resolve()
    require(subprocess.check_output(["git", "rev-parse", BASE + "^{tree}"], cwd=repo, text=True).strip() == TREE, "Frozen PHP tree changed")
    with tempfile.TemporaryDirectory(prefix="zuiyou-php-oracle-") as temporary:
        archive = Path(temporary)
        with tarfile.open(fileobj=io.BytesIO(subprocess.check_output(["git", "archive", BASE], cwd=repo))) as source:
            for entry in source.getmembers():
                require((entry.isfile() or entry.isdir()) and (archive / entry.name).resolve().is_relative_to(archive), "Unsafe source archive")
            source.extractall(archive, filter="data")
        command = [str(args.php.resolve()), "-d", "disable_functions=" + DISABLED, "-d", "allow_url_fopen=0", str(ROOT / PRODUCERS[1]), str(archive)]
        result = subprocess.run(command, input=dump(document), text=True, encoding="utf-8", capture_output=True)
        require(result.returncode == 0 and not result.stderr.strip(), "PHP oracle failed:\n" + result.stderr + result.stdout[:3000])
        observed = json.loads(result.stdout)
        require([c["id"] for c in observed["cases"]] == [c["id"] for c in document["cases"]], "PHP inventory changed")
        produced = copy.deepcopy(document)
        for case, actual in zip(produced["cases"], observed["cases"], strict=True):
            case["expected"] = actual["expected"]
        (output / "php-baseline.json").write_text(dump(produced), encoding="utf-8")
        manifest = {"format_version": 1, "baseline_commit": BASE, "baseline_tree": TREE, "php_version": runtime,
                    "fixed_clock": document["fixed_clock"], "parse_case_count": len(produced["cases"]),
                    "public_envelope_count": len(produced["cases"]) * 2, "entry_json_flags": ENTRY_FLAGS,
                    "files": {"php-baseline.json": sha(output / "php-baseline.json")},
                    "source_files": [{"path": p, "sha256": sha(archive / p)} for p in SOURCES],
                    "producer_files": [{"path": p, "sha256": sha(ROOT / p)} for p in PRODUCERS],
                    "input_files": [{"path": str(frozen_inputs.relative_to(ROOT)), "sha256": sha(frozen_inputs)}],
                    "scope": "Unmodified parseZuiyou/cURL IO choreography, entry JSON 480, snapAdaptParser and actual snapHandle normal/debug/resolve; synthetic metadata only; fixed time(); no PHP source changes or real media.",
                    "request_comparison": "Exact URL/method/body/explicit headers and redirect/timeout settings. PHP cURL's implicit Accept: */* is added only for comparison of HEAD requests; no other request field is excluded."}
        (output / "manifest.json").write_text(dump(manifest), encoding="utf-8")
    print(compact({"cases": len(produced["cases"]), "public_envelopes": len(produced["cases"]) * 2, "php": runtime}))


if __name__ == "__main__":
    main()
