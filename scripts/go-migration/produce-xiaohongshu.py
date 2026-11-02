#!/usr/bin/env python3
"""Freeze original XHS page parsing and actual MP4/MOV probing without network."""
from __future__ import annotations

import argparse
import base64
import copy
import hashlib
import io
import json
import pathlib
import struct
import subprocess
import tarfile
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASE = "25beaee107f8c1a208ced9deb3c637068445e316"
TREE = "e4b9d40f5e6c12a86fff629c105553886b434dcd"
NOW, CLOCK = 1793361600, "2026-10-30T12:00:00Z"
DIRECTORY = ROOT / "backend/internal/platform/xiaohongshu/testdata"
DISABLED = "time,microtime,dns_get_record,curl_init,curl_setopt_array,curl_setopt,curl_exec,curl_getinfo,curl_close,curl_error,curl_errno"
ENTRY_FLAGS = 2097472
PRODUCERS = ["scripts/go-migration/produce-xiaohongshu.py", "scripts/go-migration/produce-xiaohongshu.php"]
SOURCES = ["backend/api/xiaohongshu/XiaohongshuParser.php", "backend/api/xiaohongshu/XiaohongshuMediaProbe.php",
           "backend/api/xiaohongshu/xhsjx.php", "backend/lib/runtime.php", "backend/lib/media-probe.php",
           "backend/lib/media-adapters.php", "backend/lib/media-contract.php", "backend/lib/media-policy.php",
           "backend/lib/media-metadata.php", "backend/lib/platforms.php", "backend/lib/weibo-media.php",
           "backend/lib/gateway.php", "backend/config/platforms.json", "docs/schema/unified-v1/unified-v1.schema.json"]
ID = "fixture01"
QUERY = "?xsec_token=synthetic%2Btoken%2Fvalue%3D&xsec_source=pc_feed&keep=a+b%2525"
URL = "https://www.xiaohongshu.com/explore/" + ID + QUERY
RED = URL.replace("www.xiaohongshu.com", "www.rednote.com")


def dump(value): return json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n"
def compact(value): return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()
def require(condition, message):
    if not condition: raise ValueError(message)


def stream(name="720", width=1280, height=720, **extra):
    return {"masterUrl": "https://media.example/" + name + ".mp4?sig=synthetic%2Btoken", "width": width,
            "height": height, "avgBitrate": 2500000, "duration": 10000, "size": 3000000, **extra}


def note(**extra):
    return {"noteId": ID, "type": "video", "title": "undefined new Set([]) 字符串 A+B", "desc": "视频说明",
            "user": {"nickname": "合成作者", "userId": "90071992547409930012", "avatar": "https://images.example/avatar.jpg"},
            "time": 1746700800000, "interactInfo": {"likedCount": 0, "commentCount": "12", "shareCount": 0, "collectedCount": 1},
            "imageList": [{"url": "https://images.example/cover.jpg"}],
            "video": {"media": {"stream": {"h264": [stream()]}}}, **extra}


def page(value=None, *, fallback=False):
    value = note() if value is None else value
    state = {"noteData": {"data": {"noteData": value}}} if fallback else {"note": {"noteDetailMap": {ID: {"note": value}}}}
    return "<script>window.__INITIAL_STATE__=" + compact(state) + ";</script>"


def reply(url, body="", *, status=200, headers=None, media=False, error=0):
    if isinstance(body, str): body = body.encode()
    return {"url": url, "method": "GET", "range": "0-65535" if media else "", "status": status,
            "headers": headers or {}, "body_b64": base64.b64encode(body).decode(), "error": error}


def movie(container="mov", width=3840, height=2160):
    def words(*values): return struct.pack(">" + "I" * len(values), *values)
    def box(name, payload): return words(len(payload) + 8) + name.encode() + payload
    entry = bytearray(78); struct.pack_into(">HH", entry, 24, width, height)
    stsd = box("stsd", words(0, 1) + box("hvc1", entry))
    stsz = box("stsz", words(0, 5, 10))
    mdia = box("mdia", box("mdhd", words(0, 0, 0, 1000, 10000)) + box("hdlr", b"\x00" * 8 + b"vide") + box("minf", box("stbl", stsd + stsz)))
    brand = b"qt  " if container == "mov" else b"isom"
    return box("ftyp", brand + words(0) + brand) + box("moov", box("trak", mdia)) + box("mdat", b"\x00" * 50)


def media_reply(url, *, container="mov", body=None, status=206, headers=None):
    body = movie(container) if body is None else body
    fields = {"Content-Type": "video/quicktime" if container == "mov" else "video/mp4", "Content-Length": str(len(body))}
    if status == 206: fields["Content-Range"] = f"bytes 0-{len(body)-1}/{len(body)}"
    fields.update(headers or {})
    return reply(url, body, status=status, headers=fields, media=True)


def input_document():
    cases = []
    def add(name, responses, *, source=URL):
        cases.append({"id": "xiaohongshu/" + name, "input": {"url": source, "now": NOW, "http_responses": responses}})
    add("stream/minimal", [reply(RED, page())])
    rich = note(); rich["video"]["media"]["stream"] = {"h264": [stream(backupUrls=["https://backup.example/720.mp4", "https://backup.example/720.mp4"])], "h265": [stream("1080", 1920, 1080)], "av1": [stream("av1", 1920, 1080)]}
    rich["video"]["mediaV2"] = compact({"source_dimension": {"width": 7680, "height": 4320}, "opaque1": compact({"hd_screencast_stream": compact(stream("screencast", 2560, 1440, videoCodec="hevc")), "default_screencast_stream": {"master_url": stream()["masterUrl"], "backup_urls": ["https://third.example/720.mp4"]}})})
    add("stream/all-codecs-media-v2", [reply(RED, page(rich))])
    add("entry/rednote-direct", [reply(RED, page())], source=RED)
    discovery = URL.replace("/explore/", "/discovery/item/")
    add("entry/discovery", [reply(discovery.replace("www.xiaohongshu.com", "www.rednote.com"), page())], source=discovery)
    short = "https://xhslink.com/a/synthetic"
    add("entry/short", [reply(short, status=302, headers={"Location": URL}), reply(URL, page()), reply(RED, page())], source=short)
    add("entry/xhs-com-alias", [reply(short, status=302, headers={"Location": URL}), reply(URL, page()), reply(RED, page())], source=short.replace("xhslink.com", "xhs.com"))
    add("page/fallback-after-403", [reply(RED, status=403), reply(URL, page())])
    add("page/fallback-after-empty", [reply(RED, page({})), reply(URL, page(fallback=True))])
    add("page/relative-redirect", [reply(RED, status=307, headers={"Location": "/explore/" + ID + QUERY + "&step=2"}), reply(RED + "&step=2", page())])
    add("page/foreign-redirect-fallback", [reply(RED, status=302, headers={"Location": "https://example.com/unsafe"}), reply(URL, page())])
    js = page().replace("};</script>", ',"none":undefined,"set":new Set([]),"map":new Map([])};</script>')
    add("state/inert-js-values", [reply(RED, js)])
    album = note(type="normal", video={}, imageList=[
        {"url": "https://images.example/notes_pre_post/abc!small"},
        {"url": "https://images.example/oss-sg/spectrum/def!small"},
        {"url": "https://images.example/123456/ghi!small"}])
    add("image/album-rewrites", [reply(RED, page(album))])
    live = copy.deepcopy(album); live["imageList"][0]["stream"] = {"h264": [stream("live", 1080, 1920)], "h265": [stream("live-hevc", 1080, 1920)]}
    add("image/mixed-live", [reply(RED, page(live))])
    cover = note(imageList=[], cover={"fileId": "cover01"}); add("image/cover-fileid", [reply(RED, page(cover))])
    unknown = note(video={"media": {"stream": {"unknown": [{"masterUrl": "https://media.example/unknown.mp4", "source_dimension": {"width": 7680, "height": 4320}}]}}})
    add("stream/unknown-does-not-borrow-dimensions", [reply(RED, page(unknown))])
    origin = note(); origin["video"]["consumer"] = {"originVideoKey": "spectrum/synthetic-origin"}
    origins = ["https://sns-video-" + cdn + ".xhscdn.com/spectrum/synthetic-origin" for cdn in ["qn", "al", "qc", "bd"]]
    for container in ["mov", "mp4"]:
        add("origin/measured-4k-" + container, [reply(RED, page(origin)), media_reply(origins[0], container=container)])
    add("origin/second-cdn", [reply(RED, page(origin)), reply(origins[0], status=403, media=True), media_reply(origins[1])])
    add("origin/all-unavailable", [reply(RED, page(origin))] + [reply(url, status=403, media=True) for url in origins])
    add("origin/unmeasured-retained", [reply(RED, page(origin))] + [media_reply(url, body=b"not a media container") for url in origins])
    edge = "https://edge.example.net/original.mov?sig=synthetic%2Bvalue"
    add("origin/public-cdn-redirect", [reply(RED, page(origin)), reply(origins[0], status=302, headers={"Location": edge}, media=True), media_reply(edge)])
    invalid = copy.deepcopy(origin); invalid["video"]["consumer"]["originVideoKey"] = "../unsafe"
    add("origin/invalid-key", [reply(RED, page(invalid))])
    add("origin/small-complete-200", [reply(RED, page(origin)), media_reply(origins[0], status=200)])
    for code in [0, 403, 404, 429, 500]:
        add("failure/http-" + str(code), [reply(RED, status=code, error=7 if code == 0 else 0), reply(URL, status=code, error=7 if code == 0 else 0)])
    for name, body in [("invalid-json", '<script>window.__INITIAL_STATE__={bad};</script>'), ("mismatch", page(note(noteId="other"))), ("empty-note", page({})), ("no-media", page(note(video={}, imageList=[]))), ("no-state", "<html>verification</html>")]:
        add("failure/" + name, [reply(RED, body), reply(URL, body)] if name != "no-media" else [reply(RED, body)])
    add("failure/short-without-id", [reply(short, "<html>unknown</html>")], source=short)
    for name, url in [("foreign", "https://example.com/explore/fixture01"), ("userinfo", "https://user@www.rednote.com/explore/fixture01"), ("port", "https://www.rednote.com:8443/explore/fixture01")]:
        add("entry/reject-" + name, [], source=url)
    return {"format_version": 1, "baseline_commit": BASE, "baseline_tree": TREE,
            "fixed_clock": {"rfc3339": CLOCK, "unix_seconds": NOW}, "cases": cases}

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
    with tempfile.TemporaryDirectory(prefix="dokitool-xiaohongshu-php-") as folder:
        baseline = pathlib.Path(folder); archive(args.repo.resolve(), baseline)
        command = [str(args.php.resolve()), "-d", "disable_functions=" + DISABLED, "-d", "allow_url_fopen=0",
                   str(ROOT / PRODUCERS[1]), str(baseline)]
        process = subprocess.run(command, input=dump(document), text=True, encoding="utf-8", capture_output=True)
        require(process.returncode == 0 and not process.stderr.strip(), "PHP Xiaohongshu oracle failed:\n" + process.stderr + process.stdout[:3000])
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
                    "evidence_scope": "Controlled page and original-CDN byte responses through immutable PHP XiaohongshuParser, real container probe, entry JSON, adapter and public views; no Go expected or live platform evidence.",
                    "files": {"php-baseline.json": sha(output / "php-baseline.json")},
                    "input_files": [{"path": str(frozen_inputs.relative_to(ROOT)), "sha256": sha(frozen_inputs)}],
                    "source_files": [{"path": path, "sha256": sha(baseline / path)} for path in SOURCES],
                    "producer_files": [{"path": path, "sha256": sha(ROOT / path)} for path in PRODUCERS],
                    "transport_substitution": {"disabled_native_functions": DISABLED.split(","),
                        "mechanism": "Native cURL/DNS and time only replaced; original page and media header/writer callbacks execute, with fixed public DNS and actual synthetic MP4/MOV bytes. Unknown/reordered/unused requests fail.",
                        "captured_request_fields": ["method", "url", "headers", "body", "follow_redirects", "timeout_ms", "range"]},
                    "comparison_rules": ["Entry has no JSON_NUMERIC_CHECK; exact numbers, missing/null, array/source order, URL bytes and stable identities remain exact.",
                        "Successful data re-finalized at the explicit public fixture clock; synthetic URLs expire far in 2100, not near wall clock.",
                        "Only JSON object key order and equivalent number spelling are non-semantic; no metadata/identity/URL exclusions.",
                        "Page headers/method/URL/redirect order and range bytes are exact. Probe timeouts are positive and bounded by 6 seconds; elapsed wall-clock milliseconds are not public contract fields.",
                        "PHP CURLOPT_ENCODING advertises compiled-in decoders; Go uses its bounded gzip/deflate transport. All explicit headers and signed query bytes stay exact. No cookies are supplied or harvested."]}
        (output / "manifest.json").write_bytes(dump(manifest).encode())
    print(compact({"baseline": BASE, "php_version": runtime, "cases": len(cases), "public_envelopes": len(cases) * 2}))


if __name__ == "__main__":
    main()
