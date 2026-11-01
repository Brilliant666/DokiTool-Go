#!/usr/bin/env python3
"""Freeze original Toutiao PHP parser, request wire and public views without network."""
from __future__ import annotations

import argparse
import base64
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
ID = "7501991960077500979"
URL = f"https://www.toutiao.com/video/{ID}/"
SESSION = "https://ttwid.bytedance.com/ttwid/union/register/"
DIRECTORY = ROOT / "backend/internal/platform/toutiao/testdata"
DISABLED = "curl_init,curl_setopt_array,curl_setopt,curl_exec,curl_getinfo,curl_close,curl_error,curl_errno,get_headers"
ENTRY_FLAGS = 2097472  # UNESCAPED_UNICODE | UNESCAPED_SLASHES | INVALID_UTF8_SUBSTITUTE
PRODUCERS = ["scripts/go-migration/produce-toutiao.py", "scripts/go-migration/produce-toutiao.php"]
SOURCES = ["backend/api/toutiao.php", "backend/lib/runtime.php", "backend/lib/gateway.php",
           "backend/lib/media-adapters.php", "backend/lib/media-contract.php", "backend/lib/media-metadata.php",
           "backend/lib/media-policy.php", "backend/lib/platforms.php", "backend/lib/weibo-media.php",
           "backend/config/platforms.json", "backend/tests/toutiao-contract.php",
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


def b64(value):
    return base64.b64encode(value.encode()).decode()


def representation(name="main", width=1920, height=1080, **extra):
    return {"main_url": b64(f"https://v3-sign.toutiaovod.com/{name}.mp4?keep=a%2Bb+z&deadline=4102444800&signature=fixture"),
            "video_meta": {"vwidth": width, "vheight": height, "real_bitrate": 2500000, "size": 12000000, "codec_type": "h264"}, **extra}


def video(**extra):
    return {"title": "头条迁移 A+B %25 😀", "itemId": ID, "coverUrl": "//images.example/toutiao-cover.jpg",
            "userInfo": {"name": "合成作者", "userId": "90071992547409930012", "avatarUrl": "https://images.example/avatar.jpg", "description": "作者简介保留"},
            "publishTime": 1746700800, "diggCount": 0, "commentCount": "16", "repinCount": 2, "playCount": 0,
            "videoPlayInfo": {"video_duration": 149.718, "video_list": {"video_1": representation()}}, **extra}


def page(work=None, *, root_id=ID, encoded=True, attributes='type="application/json" id="RENDER_DATA"'):
    root = {"initialVideo": video() if work is None else work}
    if root_id is not None:
        root["itemId"] = root_id
    text = compact({"data": root})
    if encoded:
        text = urllib.parse.quote(text, safe="")
    return f"<html><script {attributes}>{text}</script></html>"


def response(url, *, method="GET", status=200, body="", headers=None, **extra):
    return {"method": method, "url": url, "status": status, "headers": headers or {}, "body": body,
            "follow_redirects": False, **extra}


def session(cookie="synthetic-ttwid", **extra):
    return response(SESSION, method="POST", headers={"Set-Cookie": ["unrelated=ignored; Path=/", f"ttwid={cookie}; Domain=.bytedance.com; Path=/; Secure"]}, **extra)


def build_cases():
    cases = []

    def add(identifier, scenario, *, url=URL, html=None, work=None, prefix=None, init=None, fetch_session=True, fetch_page=True,
            target_id=ID, page_response=None, clock_ticks=None):
        tape = copy.deepcopy(prefix or [])
        if fetch_session:
            tape.append(session() if init is None else init)
        if fetch_page:
            tape.append(page_response if page_response is not None else response(f"https://www.toutiao.com/video/{target_id}/", body=html if html is not None else page(work)))
        item = {"id": "toutiao/" + identifier, "scenario": scenario,
                "input": {"url": url, "now": NOW, "clock_ticks": clock_ticks or [100.0], "http_responses": tape}}
        cases.append(item)

    add("file/minimal", "Single actual complete video remains a simple item/default and preserves zero counts and author description")
    rich = video()
    rich["itemCell"] = {"userInfo": {"name": "优先作者", "userID": "90071992547409930013", "avatarURL": "//images.example/priority.jpg", "description": "优先路径简介"},
                        "videoAbility": {"music": {"title": "合成音乐", "author": "合成歌手", "url": "https://media.example/music.mp3"}}}
    rich["videoPlayInfo"]["video_list"] = {
        "lower_first": representation("720", 1280, 720),
        "highest_second": representation("4k", 3840, 2160, backup_url="https://mirror.example/4k.mp4?keep=a%2Bb+z", backup_url_1=b64("//mirror2.example/4k.mp4")),
        "unknown": {"main_url": "https://media.example/unknown.mp4"},
        "unsafe": {"main_url": b64("file:///etc/passwd")}}
    add("file/all-versions-backups", "Highest actual dimensions win regardless of input order; all legal backup sources, music and priority author survive", work=rich)
    numeric = video(title="000123", itemId=900719925474099300123)
    numeric["userInfo"].update(name="+0012", userId=900719925474099300123)
    numeric["videoPlayInfo"]["video_duration"] = "1.5005"
    numeric["videoPlayInfo"]["video_list"]["video_1"]["video_meta"].update(vwidth="1920", vheight="1080", size="12000000", real_bitrate="2.5e6")
    add("entry/bigint-and-numeric-strings", "JSON_BIGINT_AS_STRING preserves oversized numeric IDs; entry flags do not coerce textual title/author", target_id="900719925474099300123", url="https://www.toutiao.com/video/900719925474099300123/", html=page(numeric, root_id=900719925474099300123))
    add("render/raw-json", "Raw object JSON uses the same parser path without percent decoding", html=page(encoded=False))
    add("render/raw-plus", "rawurldecode keeps plus in percent-encoded script text rather than converting it to a space", html=page().replace("%2B", "+"))
    add("render/single-quote-uppercase-script", "Supported script tag case, attribute order, single quotes and whitespace", html=page(attributes="data-test='yes' id = 'RENDER_DATA' type='application/json'").replace("<script", "<SCRIPT").replace("</script>", "</SCRIPT >"))
    without_id = video(); without_id.pop("itemId")
    add("render/optional-item-identities", "Absent root/video IDs do not invent a mismatch; requested ID remains work identity", html=page(without_id, root_id=None))
    fallback = video(userInfo="not-an-object", title=42, poster="https://images.example/fallback.jpg", duration="12.25")
    fallback.pop("coverUrl"); fallback["videoPlayInfo"].pop("video_duration")
    fallback.update(diggCount=-1, commentCount="invalid", repinCount=None)
    add("metadata/fallback-and-unknown", "Fallback poster/duration and invalid author/statistics use exact PHP absence semantics", work=fallback)
    tie = video()
    same_meta = {"vwidth": 1280, "vheight": 720, "real_bitrate": 1000000, "size": 1000}
    tie["videoPlayInfo"]["video_list"] = {"z-first": {"main_url": "https://media.example/z.mp4", "video_meta": same_meta},
                                            "a-second": {"main_url": "https://media.example/a.mp4", "video_meta": same_meta}}
    add("candidates/tie-keeps-json-order", "An object video_list retains insertion order on equal dimensions/bitrate/size, not sorted Go map keys", work=tie)
    duplicate = video()
    duplicate["videoPlayInfo"]["video_list"] = [representation("duplicate", 1280, 720), representation("duplicate", 3840, 2160),
                                                 {"backup_url_2": b64("https://media.example/backup-only.mp4"), "definition": "576p", "codec_type": "h265"},
                                                 {"main_url": "%%%invalid-base64%%%"}, "not-a-representation"]
    add("candidates/first-observation-and-backup-only", "URL de-duplication retains first observed metadata; legal backup-only representation and definition fallback remain", work=duplicate)
    for name, link in [
        ("path-group", f"https://m.toutiao.com/group/{ID}/"),
        ("path-mobile-i", f"https://m.toutiao.com/i{ID}/?wid=1765247684252"),
        ("query-group-priority", f"https://m.toutiao.com/share/?item_id=123456&group_id={ID}"),
        ("query-final-scalar", f"https://m.toutiao.com/share/?group_id=123456&group_id={ID}"),
        ("query-array-falls-back", f"https://m.toutiao.com/share/?group_id[]={ID}&item_id={ID}"),
    ]:
        add("entry/" + name, "PHP path/query ID extraction preserves exact canonical target and source URL", url=link)

    short = "https://m.toutiao.com/is/synthetic/"
    add("short/absolute", "Share redirects finish before anonymous registration; share cookies are not forwarded", url=short,
        prefix=[response(short, status=302, headers={"Location": URL + "?wid=1765247684252", "Set-Cookie": "ttwid=share-cookie; Path=/"})])
    add("short/relative", "Relative redirect stays on current host and resolves the requested video ID", url=short,
        prefix=[response(short, status=307, headers={"Location": "/video/" + ID + "/"})])
    add("short/protocol-relative", "Protocol-relative Location uses HTTPS and remains platform scoped", url=short,
        prefix=[response(short, status=301, headers={"Location": "//www.toutiao.com/video/" + ID + "/"})])
    next_link = "https://m.toutiao.com/is/next/"
    third_link = "https://m.toutiao.com/is/third/"
    chain = [response(short, status=302, headers={"Location": next_link}), response(next_link, status=302, headers={"Location": third_link}), response(third_link, status=302, headers={"Location": URL})]
    add("budget/five-requests-shared", "Three redirects, registration and page consume one 35-second budget: 20/20/19/11/7 seconds", url=short, prefix=chain, clock_ticks=[100, 100, 108, 116, 124, 128])
    add("budget/page-not-started", "Exhausted registration budget never requests page", fetch_page=False, clock_ticks=[100, 100, 135])
    add("budget/subsecond-share-remainder", "Less than one second remaining refuses the next redirect", url=short, fetch_session=False, fetch_page=False,
        prefix=[response(short, status=302, headers={"Location": next_link})], clock_ticks=[100, 100, 134.5])
    add("budget/session-not-started", "A fully consumed total budget refuses initial registration", fetch_session=False, fetch_page=False, clock_ticks=[100, 135])
    add("short/foreign-target", "An external redirect is rejected without following it", url=short, fetch_session=False, fetch_page=False,
        prefix=[response(short, status=302, headers={"Location": "https://example.com/video/" + ID + "/"})])
    add("short/loop-limit", "Exactly three unresolved redirects; no registration or fourth request", url=short, fetch_session=False, fetch_page=False,
        prefix=[response(short, status=302, headers={"Location": short}) for _ in range(3)])
    add("short/nonredirect-without-id", "A normal page at an unsupported ID location cannot be interpreted as another work", url=short, fetch_session=False, fetch_page=False,
        prefix=[response(short, body="<html>share landing</html>")])
    add("short/transport-failure", "Share transport error is distinct from budget exhaustion", url=short, fetch_session=False, fetch_page=False,
        prefix=[response(short, status=0, error="Synthetic connection failure")])
    for name, link in [("foreign-host", "https://toutiao.com.example/video/" + ID), ("userinfo", "https://user@www.toutiao.com/video/" + ID), ("nonstandard-port", "https://www.toutiao.com:8080/video/" + ID)]:
        add("entry/reject-" + name, "Invalid initial URL fails without any request", url=link, fetch_session=False, fetch_page=False)
    for name, init in [("transport", session(status=0, error="Synthetic connection failure")), ("http-error", session(status=500)),
                       ("missing-cookie", response(SESSION, method="POST", body="{}")), ("zero-cookie", session(cookie="0"))]:
        add("session/" + name, "Anonymous session failure never starts page request and never leaks cookie", init=init, fetch_page=False)
    repeated = response(SESSION, method="POST", headers={"Set-Cookie": ["ttwid=first; Path=/", "other=ignored; Path=/", "ttwid=last%2Bvalue; Path=/"]})
    add("session/last-ttwid-header", "Exact last ttwid value is used once for canonical page; other cookies are discarded", init=repeated)
    for status in (403, 429, 404, 500):
        add("page/http-" + str(status), "Page HTTP classification preserves original retry/error contract", page_response=response(URL, status=status))
    add("page/transport-failure", "Page transport error remains explicit", page_response=response(URL, status=0, error="Synthetic connection failure"))
    for name, html in [
        ("missing-script", "<html>No video metadata</html>"),
        ("challenge", "<html>验证码 __ac_signature</html>"),
        ("data-id-not-id", page().replace(' id="', ' data-id="')),
        ("invalid-json", '<script id="RENDER_DATA">%7Bbad</script>'),
        ("root-id-mismatch", page(root_id="123456")),
        ("video-id-mismatch", page(video(itemId="123456"))),
        ("root-id-nonscalar", page(root_id=[])),
        ("missing-video", '<script id="RENDER_DATA">{"data":{"itemId":"' + ID + '"}}</script>'),
        ("no-valid-media", page(video(videoPlayInfo={"video_list": {"bad": {"main_url": b64("javascript:alert(1)")}}}))),
    ]:
        add("render/" + name, "Invalid/mismatched work or media is an explicit failure, never empty success", html=html)
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
    with tempfile.TemporaryDirectory(prefix="dokitool-toutiao-php-") as folder:
        baseline = pathlib.Path(folder); archive(args.repo.resolve(), baseline)
        command = [str(args.php.resolve()), "-d", "disable_functions=" + DISABLED, "-d", "allow_url_fopen=0",
                   str(ROOT / PRODUCERS[1]), str(baseline)]
        process = subprocess.run(command, input=dump(document), text=True, encoding="utf-8", capture_output=True)
        require(process.returncode == 0 and not process.stderr.strip(), "PHP Toutiao oracle failed:\n" + process.stderr + process.stdout[:3000])
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
                    "evidence_scope": "Synthetic metadata responses through immutable PHP toutiao/toutiaoRequest/parser/entry-wire/adapter/public functions. No Go-generated expectations or live media evidence.",
                    "files": {"php-baseline.json": sha(output / "php-baseline.json")},
                    "input_files": [{"path": str(frozen_inputs.relative_to(ROOT)), "sha256": sha(frozen_inputs)}],
                    "source_files": [{"path": path, "sha256": sha(baseline / path)} for path in SOURCES],
                    "producer_files": [{"path": path, "sha256": sha(ROOT / path)} for path in PRODUCERS],
                    "transport_substitution": {"disabled_native_functions": DISABLED.split(","),
                        "mechanism": "Only native cURL IO replaced; original header callback, Cookie option, JSON request body and bounded response writer execute. Unknown/reordered/unused requests fail; monotonic clock uses reviewed ticks.",
                        "captured_request_fields": ["method", "url", "headers", "body", "follow_redirects", "timeout_seconds"]},
                    "comparison_rules": ["Entry has no JSON_NUMERIC_CHECK; exact numbers, missing/null, array/source order, URL bytes and stable identities remain exact.",
                        "Successful data re-finalized at the explicit public fixture clock; synthetic URLs expire far in 2100, not near wall clock.",
                        "Only JSON object key order and equivalent number spelling are non-semantic; no metadata/identity/URL exclusions.",
                        "Request header casing is normalized; timeout is the original floored shared 35-second budget, body bytes and all intentional headers remain exact.",
                        "The PHP recorder captures explicit cURL header options, not all implicit library wire headers. Go explicitly supplies Accept: */*; that exact single transport header is added to the comparison view without changing the PHP oracle. No other header is ignored."]}
        (output / "manifest.json").write_bytes(dump(manifest).encode())
    print(compact({"baseline": BASE, "php_version": runtime, "cases": len(cases), "public_envelopes": len(cases) * 2}))


if __name__ == "__main__":
    main()
