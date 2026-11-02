#!/usr/bin/env python3
"""Freeze the unmodified PHP Bilibili entry/adapter/public paths without network IO."""
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
CLOCK = "2026-10-30T12:00:00Z"
NOW = 1793361600
BV = "BV1234567890"
OTHER_BV = "BVabcdefghij"
URL = "https://www.bilibili.com/video/" + BV + "/"
DIRECTORY = ROOT / "backend/internal/platform/bilibili/testdata"
DISABLED = "curl_init,curl_setopt_array,curl_setopt,curl_exec,curl_getinfo,curl_close,curl_error,curl_errno,get_headers"
PRODUCERS = ["scripts/go-migration/produce-bilibili.py", "scripts/go-migration/produce-bilibili.php"]
SOURCES = [
    "backend/api/bilibili/bilibili.php", "backend/lib/runtime.php", "backend/lib/gateway.php",
    "backend/lib/media-adapters.php", "backend/lib/media-contract.php", "backend/lib/media-metadata.php",
    "backend/lib/media-policy.php", "backend/lib/platforms.php", "backend/lib/weibo-media.php",
    "backend/config/platforms.json", "backend/tests/parser-contract.php",
    "docs/schema/unified-v1/unified-v1.schema.json",
]


def dump(value):
    return json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n"


def compact(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def require(condition, message):
    if not condition:
        raise ValueError(message)


def detail(pages=1):
    return {"bvid": BV, "title": "B站迁移 fixture 中文😀", "desc": "Synthetic selected-part fixture",
            "pic": "//images.example/bili-cover.jpg", "pubdate": 1700000000,
            "owner": {"name": "Fixture author", "face": "https://images.example/bili-avatar.jpg"},
            "stat": {"view": 0, "reply": "7", "favorite": 2, "share": 0, "like": 12},
            "pages": [{"page": i, "cid": 100 + i, "part": "章节 " + str(i), "duration": 120 + i,
                       "dimension": {"width": 3840, "height": 2160}}
                      for i in range(1, pages + 1)]}


def durl(name="complete", **facts):
    return {"url": "https://upos-fixture.bilivideo.com/" + name + ".mp4?deadline=4102444800&upsig=synthetic&keep=1",
            "backup_url": ["https://upos-backup.bilivideo.com/" + name + ".mp4?deadline=4102444800&upsig=backup&keep=1"],
            "size": 2500000, "length": 121000, **facts}


def play(parts=None, **facts):
    return {"quality": 64, "durl": parts if parts is not None else [durl()], **facts}


def video(name="video-4k", **facts):
    return {"id": 120, "baseUrl": "https://upos-fixture.bilivideo.com/" + name + ".m4s",
            "backupUrl": ["https://upos-backup.bilivideo.com/" + name + ".m4s"],
            "width": 3840, "height": 2160, "codecs": "hev1.1.6.L150.90", "bandwidth": 6000000,
            "frameRate": "30000/1001", **facts}


def audio(name="audio-main", **facts):
    return {"id": 30280, "base_url": "https://upos-fixture.bilivideo.com/" + name + ".m4s",
            "backup_url": ["https://upos-backup.bilivideo.com/" + name + ".m4s"],
            "codecs": "mp4a.40.2", "bandwidth": 128000, "audio_channels": 2,
            "audio_language": "zh-CN", "audio_language_role": "main", **facts}


def view_url():
    return "https://api.bilibili.com/x/web-interface/view?bvid=" + BV


def play_url(cid):
    return "https://api.bilibili.com/x/player/playurl?" + urllib.parse.urlencode(
        {"otype": "json", "fnver": 0, "fnval": 3, "player": 3, "qn": 112,
         "bvid": BV, "cid": cid, "platform": "html5", "high_quality": 1})


def response(url, value=None, *, method="GET", status=200, body=None, follow=False, **extra):
    return {"method": method, "url": url, "status": status, "headers": {"Content-Type": "application/json"},
            "body": compact(value) if body is None and value is not None else (body or ""),
            "follow_redirects": follow, **extra}


def build_cases():
    cases = []

    def add(identifier, scenario, *, url=URL, work=None, media=None, cid=101,
            prefix=None, view=None, fetch_view=True, fetch_play=True, play_response=None):
        replies = copy.deepcopy(prefix or [])
        if fetch_view:
            replies.append(view if view is not None else response(view_url(), {"code": 0, "data": detail() if work is None else work}))
        if fetch_play:
            replies.append(play_response if play_response is not None else response(play_url(cid), {"code": 0, "data": play() if media is None else media}))
        cases.append({"id": "bilibili/" + identifier, "scenario": scenario,
                      "input": {"url": url, "now": NOW, "http_responses": replies}})

    add("file/playback-quality-not-upload-dimensions", "Requested qn=112 and view 4K dimensions cannot invent current stream dimensions; actual play quality is 720p, zero statistics survive")
    add("parts/p2-only", "Only P2 cid is fetched; other parts stay deferred with stable item IDs and exact resolve URLs", url=URL + "?p=2&x=a%26b", work=detail(3), cid=102)
    add("parts/missing-target", "A missing requested P fails before any play request", url=URL + "?p=4", work=detail(3), fetch_play=False)
    missing = detail(); missing["pages"][0]["cid"] = 0
    add("parts/missing-cid", "A selected page without a usable cid fails without substituting another part", work=missing, fetch_play=False)
    for name, value in [("zero", "0"), ("leading-zero", "02"), ("signed", "%2B2"), ("too-long", "1000000"),
                        ("array", "2&p[]=3"), ("nested", "2&p[x]=3"), ("empty", ""), ("fraction", "1.5")]:
        add("entry/invalid-p/" + name, "PHP parse_str and the exact positive one-to-six-digit p grammar reject malformed selection", url=URL + "?p=" + value, fetch_view=False, fetch_play=False)
    add("entry/duplicate-p-last-wins", "PHP parse_str takes the final scalar p; the parser still requests only that cid", url=URL + "?p=1&p=2", work=detail(3), cid=102)
    add("entry/query-unclosed-bracket", "An unclosed bracket is normalized as a different PHP query key and must not overwrite scalar p", url=URL + "?p=2&p[=3", work=detail(3), cid=102)
    add("entry/query-nul-key", "PHP truncates a decoded NUL in the query key; the resulting p still selects P2", url=URL + "?p%00=2", work=detail(3), cid=102)
    add("entry/query-array-then-scalar", "A later scalar p replaces an earlier array p under PHP parse_str semantics", url=URL + "?p=2&p[x]=3&p=1", work=detail(3), cid=101)
    add("entry/mobile-host", "The allowed mobile host retains the same BVID and selected-P identity", url="https://m.bilibili.com/video/" + BV + "?p=2", work=detail(2), cid=102)

    short = "https://b23.tv/synthetic"
    add("entry/short-target-p", "A short-link target p remains selected when the original has no p", url=short, work=detail(3), cid=103,
        prefix=[response(short, method="HEAD", follow=True, final_url=URL + "?p=3")])
    add("entry/short-source-p-wins", "An explicit p on the original short URL overrides the redirected target p", url=short + "?p=2", work=detail(3), cid=102,
        prefix=[response(short + "?p=2", method="HEAD", follow=True, final_url=URL + "?p=3&track=a%26b#discard")])
    add("entry/short-invalid-source-p", "An array p on the original short URL is preserved and rejected after redirect, never converted to P1", url=short + "?p[]=2", fetch_view=False, fetch_play=False,
        prefix=[response(short + "?p[]=2", method="HEAD", follow=True, final_url=URL + "?p=1")])
    add("entry/short-failure", "A failed short-link HEAD maps to the actual parser error", url=short, fetch_view=False, fetch_play=False,
        prefix=[response(short, method="HEAD", status=0, follow=True, error="Synthetic connection failure")])
    html_url = "https://www.bilibili.com/video/av123456/?p=2"
    short_html_target = "https://www.bilibili.com/video/av123456/?p=2&track=a%26b"
    add("entry/short-html-source-p-wins", "An explicit original short-link p survives both target query rebuilding and the av-page HTML BV fallback", url=short + "?p=2", work=detail(3), cid=102,
        prefix=[response(short + "?p=2", method="HEAD", follow=True, final_url="https://www.bilibili.com/video/av123456/?p=3&track=a%26b#discard"),
                response(short_html_target, body='<html><meta itemprop="url" content="' + URL + '"></html>', follow=True)])
    add("entry/html-bvid", "The actual legacy meta itemprop=url fallback resolves a BV ID while retaining P2", url=html_url, work=detail(2), cid=102,
        prefix=[response(html_url, body='<html><meta itemprop="url" content="' + URL + '"></html>', follow=True)])
    add("entry/html-no-bvid", "A page without the supported BV meta field fails explicitly", url=html_url, fetch_view=False, fetch_play=False,
        prefix=[response(html_url, body="<html>No usable BV meta</html>", follow=True)])
    add("entry/html-transport-failure", "A failed HTML fallback produces no output noise or guessed BV", url=html_url, fetch_view=False, fetch_play=False,
        prefix=[response(html_url, status=0, follow=True, error="Synthetic connection failure")])

    for name, status, body, error in [
        ("http-401", 401, {"code": 0, "data": detail()}, None),
        ("http-403", 403, {"code": -403}, None),
        ("http-412", 412, {"code": -412}, None),
        ("http-429-html", 429, "<html>restricted</html>", None),
        ("http-404", 404, {"code": -404}, None),
        ("negative-restricted", 200, {"code": -101}, None),
        ("deleted", 200, {"code": 62002}, None),
        ("invalid-json", 200, "not JSON", None),
        ("missing-code", 200, {"data": detail()}, None),
        ("string-success-code", 200, {"code": "0", "data": detail()}, None),
        ("float-success-code", 200, {"code": 0.0, "data": detail()}, None),
        ("transport-failure", 0, None, "Synthetic connection failure"),
    ]:
        entry = response(view_url(), status=status, body=body if isinstance(body, str) else compact(body), **({"error": error} if error else {}))
        add("view/" + name, "Preserve the PHP view-stage business/transport classification and retryable flag", view=entry, fetch_play=False)
    for name, entry in [
        ("http-412", response(play_url(102), {"code": -412}, status=412)),
        ("invalid-json", response(play_url(102), body="not JSON")),
        ("empty-sources", response(play_url(102), {"code": 0, "data": {"quality": 80, "durl": []}})),
    ]:
        add("play/" + name, "A failing selected play response leaves top-level success and only the selected item unavailable; other parts remain deferred",
            url=URL + "?p=2", work=detail(3), cid=102, play_response=entry)

    add("file/measured-facts", "Explicit playback dimensions, fractional FPS, SDR and audio facts are retained without borrowing view metadata",
        media=play([durl(width=1280, height=720, fps="30000/1001", hdr_type="sdr", has_audio=True, audio_channels=2, audio_language="zh-CN")], quality=64))
    add("segments/two-pieces", "Multiple durl entries remain ordered segments requiring remux; no concatenated file is invented",
        media=play([durl("segment-1", length=1000), durl("segment-2", length=2000)], quality=80))
    add("dash/two-video-two-audio", "Every legal video/audio pairing remains an independent mux candidate with its own identity",
        media={"dash": {"video": [video(), video("video-1080", id=80, width=1920, height=1080, codecs="avc1.640028")],
                         "audio": [audio(), audio("audio-aac-lower", id=30216, bandwidth=64000)]}})
    add("dash/missing-audio", "Missing audio is unknown completeness, not proof of silence; each video remains deferred",
        media={"dash": {"video": [video()], "audio": []}})
    add("dash/invalid-audio", "An audio entry with no valid source cannot form a supposedly complete mux pair",
        media={"dash": {"video": [video()], "audio": [{"baseUrl": "invalid", "backupUrl": []}]}})
    add("policy/complete-file-covers-mux", "Ordinary view removes covered mux while debug preserves it and the complete default remains unchanged",
        media=play([durl(width=3840, height=2160, fps="30000/1001", hdr_type="sdr", audio_channels=2, audio_language="zh-CN", audio_language_role="main")],
                   dash={"video": [video(hdr_type="sdr")], "audio": [audio()]}))
    add("policy/higher-mux-retained", "A higher-resolution mux remains discoverable and is referenced without becoming the direct-download default",
        media=play([durl(width=1280, height=720)], dash={"video": [video()], "audio": [audio()]}))
    add("policy/hdr-catalogue-profiles", "Explicit catalogue IDs 125 and 126 retain HDR capabilities even when a same-size complete file exists",
        media=play([durl(width=3840, height=2160, hdr_type="sdr")], dash={"video": [video("video-hdr", id=125), video("video-dolby", id=126)], "audio": [audio()]}))
    add("policy/high-fps", "Explicit high FPS protects a mux despite a complete file of the same dimensions",
        media=play([durl(width=3840, height=2160, fps=30)], dash={"video": [video(frameRate="60000/1001")], "audio": [audio()]}))
    add("policy/alternate-language-and-channels", "Known alternate language and multichannel audio protect the actual paired mux resources",
        media=play([durl(width=3840, height=2160, audio_language="zh-CN", audio_language_role="main", audio_channels=2)],
                   dash={"video": [video()], "audio": [audio("audio-ja", audio_language="ja", audio_language_role="alternate"), audio("audio-surround", audio_channels=6)]}))
    expired = audio("audio-expired", base_url="", backup_url=[{"url": "https://upos-fixture.bilivideo.com/audio-expired.m4s", "expires_at": "2000-01-01T00:00:00Z"}])
    add("dash/expired-alternative", "An explicit expired source object accepted by the legacy normalizer does not invalidate the other complete audio pairing (synthetic accepted shape, not observed platform output)",
        media={"dash": {"video": [video()], "audio": [expired, audio()]}})

    collection = detail(2)
    collection["ugc_season"] = {"sections": [{"title": "合辑 A", "episodes": [
        {"bvid": BV, "title": "Existing work"}, {"bvid": OTHER_BV, "title": "Related work", "arc": {"pic": "//images.example/related.jpg"}},
        {"bvid": OTHER_BV, "title": "Repeated episode"}, {"bvid": "invalid"}]},
        {"title": "合辑 B", "episodes": [{"arc": {"bvid": OTHER_BV, "title": "Fallback title"}}]}]}
    add("collection/deduplicated-members", "Collection membership deduplicates per section, existing P1 is shared, other works stay deferred, completeness remains false", work=collection)
    numeric = detail(2); numeric.update(title="00123", desc="00042", pubdate="1700000000")
    numeric["owner"]["name"] = "+00123"; numeric["pages"][1].update(cid="00102", part="0002", duration="1.5005")
    numeric["stat"].update(view="0", like="1e3")
    add("entry/numeric-wire-boundary", "The real parser entry uses json_encode flags 480 including JSON_NUMERIC_CHECK before gateway decoding; numeric strings keep the resulting PHP types",
        url=URL + "?p=2", work=numeric, cid="00102", media=play([durl(width="1280", height="720", fps="29.97", size="2500000")]))
    for role, deadline, signature in [("before", 4102444800, "signed-before"), ("after", 4133980800, "signed-after")]:
        urls = [f"https://{host}/stable-bilibili.mp4?deadline={deadline}&upsig={signature}&keep=a%2Bb&duplicate=one&duplicate=two"
                for host in ("upos-fixture.bilivideo.com", "upos-backup.bilivideo.com")]
        if role == "after":
            urls.reverse()
        add("renewal/" + role, "Bilibili credential renewal and reversed primary/backup order preserve media/source identity while the new server default follows the current source order",
            media=play([durl(url=urls[0], backup_url=[urls[1]], width=1280, height=720)]))
        cases[-1]["group"] = {"id": "renewal/bilibili", "role": role, "fixed_now": NOW, "frontend_renewal_pair": True}
    return cases


def input_document():
    return {"format_version": 1, "baseline_commit": BASE, "baseline_tree": TREE,
            "fixed_clock": {"rfc3339": CLOCK, "unix_seconds": NOW}, "cases": build_cases()}


def archive(repo, target):
    actual = subprocess.check_output(["git", "rev-parse", BASE + "^{tree}"], cwd=repo, text=True).strip()
    require(actual == TREE, "Pinned PHP commit/tree mismatch")
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
    parser.add_argument("--write-inputs", action="store_true", help="Write only synthetic upstream inputs; never generate expectations")
    args = parser.parse_args()
    document = input_document()
    output = args.output.resolve(); output.mkdir(parents=True, exist_ok=True)
    if args.write_inputs:
        (output / "inputs.json").write_bytes(dump(document).encode("utf-8"))
        print(compact({"inputs_only": True, "cases": len(document["cases"])})); return
    require(args.php is not None, "--php is required to produce any expectation")
    frozen_inputs = DIRECTORY / "inputs.json"
    require(frozen_inputs.read_bytes() == dump(document).encode("utf-8"), "Frozen inputs differ from the reviewed producer")
    runtime = subprocess.check_output([str(args.php.resolve()), "-r", "echo PHP_VERSION;"], text=True).strip()
    require(runtime == "8.3.6", "PHP 8.3.6 is required; runtime changes need a separate oracle")
    with tempfile.TemporaryDirectory(prefix="dokitool-bilibili-php-") as folder:
        baseline = pathlib.Path(folder); archive(args.repo.resolve(), baseline)
        command = [str(args.php.resolve()), "-d", "disable_functions=" + DISABLED, "-d", "allow_url_fopen=0",
                   str(ROOT / PRODUCERS[1]), str(baseline)]
        process = subprocess.run(command, input=dump(document), text=True, encoding="utf-8", capture_output=True)
        require(process.returncode == 0 and not process.stderr.strip(), "PHP Bilibili oracle failed:\n" + process.stderr + process.stdout[:3000])
        observed = json.loads(process.stdout)
        require([c["id"] for c in observed["cases"]] == [c["id"] for c in document["cases"]], "PHP case inventory/order mismatch")
        cases = []
        for original, actual in zip(document["cases"], observed["cases"], strict=True):
            case = copy.deepcopy(original); case["expected"] = actual["expected"]; cases.append(case)
        produced = {k: v for k, v in document.items() if k != "cases"}; produced["cases"] = cases
        (output / "php-baseline.json").write_bytes(dump(produced).encode("utf-8"))
        manifest = {"format_version": 1, "baseline_commit": BASE, "baseline_tree": TREE,
                    "php_version": runtime, "fixed_clock": document["fixed_clock"],
                    "parse_case_count": len(cases), "public_envelope_count": len(cases) * 2,
                    "evidence_scope": "Synthetic metadata responses only. Immutable PHP parser, actual numeric-check entry wire boundary, adapter and ordinary/debug policy generated every expectation. No Go-generated output, real platform request, media download or online quality claim.",
                    "entry_json_flags": 480,
                    "files": {"php-baseline.json": sha(output / "php-baseline.json")},
                    "input_files": [{"path": str(frozen_inputs.relative_to(ROOT)), "sha256": sha(frozen_inputs)}],
                    "source_files": [{"path": path, "sha256": sha(baseline / path)} for path in SOURCES],
                    "producer_files": [{"path": path, "sha256": sha(ROOT / path)} for path in PRODUCERS],
                    "transport_substitution": {"disabled_native_functions": DISABLED.split(","),
                        "mechanism": "Disabled native cURL/get_headers and allow_url_fopen=0; exact in-memory cURL responses. Unknown, reordered and unused requests fail. Parser source bytes are unmodified.",
                        "captured_request_fields": ["method", "url", "headers", "follow_redirects", "transport"]},
                    "comparison_rules": [
                        "Expected parser_response is before entry encoding; legacy_response is json_decode(json_encode(parser_response, 480), true), matching the gateway wire boundary.",
                        "Successful data is re-finalized at the explicit fixture clock before normal/debug policy. Internal PHP source merging still reads wall time; synthetic explicit expiry is 2000 or 2100, not near either clock.",
                        "JSON object order and equivalent number spelling are non-semantic; arrays/order, exact integer values, missing/null, booleans, URL bytes, stable IDs and default references remain exact.",
                        "HTTP transport labels and PHP accept-encoding empty string describe cURL implementation; compare URL/method/follow policy and forbid introduced Cookie/Authorization/browser headers on view/play calls.",
                        "The one known HTML transport E_USER_WARNING is captured without converting it into an exception, matching the real gateway's suppressed warning display; every unexpected warning remains fatal to the producer.",
                        "The expired source case exercises a source-object shape accepted by the legacy normalizer; it is deliberately synthetic and not a claimed live Bilibili response.",
                    ]}
        (output / "manifest.json").write_bytes(dump(manifest).encode("utf-8"))
    print(compact({"baseline": BASE, "php_version": runtime, "cases": len(cases), "public_envelopes": len(cases) * 2}))


if __name__ == "__main__":
    main()
