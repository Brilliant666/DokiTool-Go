#!/usr/bin/env python3
"""Freeze the approved PHP Douyin Parse/formatter/public views without network IO."""
from __future__ import annotations

import argparse
import copy
import datetime as dt
import hashlib
import io
import json
import pathlib
import subprocess
import tarfile
import tempfile
import urllib.parse

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASE = "665bd55134982b9fe107aa1cd00373ba8da8ff3c"
TREE = "93744d4d718c3299f82640d2888e93252106db70"
CLOCK = "2026-10-30T12:00:00Z"
NOW = int(dt.datetime.fromisoformat(CLOCK.replace("Z", "+00:00")).timestamp())
BEFORE = 4102444800  # 2100-01-01, usable at the fixed fixture clock.
AFTER = 4133980800  # 2101-01-01, later credential for the same media/source.
AWEME_ID = "7628438784602389247"
URL = "https://www.douyin.com/video/" + AWEME_ID
DISABLED = "curl_init,curl_setopt_array,curl_setopt,curl_exec,curl_getinfo,curl_close,curl_error,curl_errno,get_headers"


def dump(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n"


def compact(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


def digest(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def detail_url(identifier: str = AWEME_ID) -> str:
    return "https://api.amemv.com/aweme/v1/aweme/detail/?" + urllib.parse.urlencode(
        {"aweme_id": identifier, "aid": "6383", "device_platform": "web", "version_code": "190500", "version_name": "19.5.0"})


def original_url(identifier: str) -> str:
    return "https://aweme.snssdk.com/aweme/v1/play/?video_id=" + urllib.parse.quote(identifier, safe="") + "&ratio=default&line=0"


def complete(identifier="complete-file", width=3840, height=2160, urls=None, facts=None, bitrate=4000000):
    return {**(facts if facts is not None else {"fps": 30, "hdr_type": "sdr", "has_audio": True}),
            "format": "mp4", "bit_rate": bitrate,
            "video_extra": compact({"file_id": identifier}),
            "play_addr": {"uri": identifier + "-uri", "file_hash": hashlib.md5(identifier.encode()).hexdigest(),
                          "width": width, "height": height, "data_size": 2500000,
                          "url_list": urls or ["https://www.douyin.com/aweme/v1/play/?file_id=" + identifier + "&sign=synthetic"]}}


def track(audio_id="audio-a", facts=None, identity=True, urls=None, width=3840, height=2160):
    extra = {"audio_file_id": audio_id}
    address = {"width": width, "height": height,
               "url_list": urls or ["https://www.douyin.com/aweme/v1/play/dash/?file_id=shared-video&sign=synthetic"]}
    if identity:
        extra["file_id"] = "shared-video"
        address["uri"] = "shared-video-uri"
        address["file_hash"] = hashlib.md5(b"shared-video").hexdigest()
    return {**(facts or {}), "format": "dash", "bit_rate": 3000000,
            "video_extra": compact(extra), "play_addr": address}


def audio(identifier="audio-a", facts=None, url=None):
    return {"audio_meta": {"file_id": identifier, "media_type": "audio",
                           "url_list": [url or "https://media.example/" + identifier + ".m4a"],
                           "sub_info": compact(facts if facts is not None else {"audio_channels": 2, "audio_language": "en", "audio_profile": "aac"})}}


def work(rates=None, audios=None, **video):
    result = {"aweme_id": AWEME_ID, "desc": "Migration fixture 中文😀", "create_time": 1700000000000,
              "author": {"nickname": "Fixture author", "uid": "9223372036854775800", "avatar_thumb": {"url_list": ["//images.example/avatar.jpg"]}},
              "statistics": {"digg_count": 0, "comment_count": "7", "share_count": 2, "collect_count": 0},
              "text_extra": [{"hashtag_name": "migration"}, {"hashtag_name": "migration"}],
              "cha_list": [{"cha_name": "media"}], "video_tag": [{"tag_name": "fixture"}],
              "video": {"duration": 2000, "cover": {"url_list": ["http://images.example/cover.jpg"]}, **video}}
    if rates is not None:
        result["video"]["bit_rate"] = rates
        result["video"]["cdn_url_expired"] = BEFORE
    if audios is not None:
        result["video"]["bit_rate_audio"] = audios
    return result


def build_cases():
    cases = []

    def add(identifier, scenario, detail, *, url=URL, prefix=None, suffix=None, probes=None, body=None, group=None):
        responses = list(prefix or [])
        if detail is not None or body is not None:
            responses.append({"method": "GET", "url": detail_url(), "status": 200,
                              "headers": {"Content-Type": "application/json"}, "follow_redirects": True,
                              "body": body if body is not None else compact({"status_code": 0, "aweme_detail": detail})})
        responses.extend(suffix or [])
        case = {"id": identifier, "scenario": scenario,
                "input": {"url": url, "now": NOW, "detail": detail, "http_responses": responses, "probe_results": probes or []}}
        if group:
            case["group"] = group
        cases.append(case)

    rates = [complete("720-h264", 1280, 720, bitrate=8000000),
             complete("4k-hevc", urls=["https://v3-web.douyinvod.com/high.mp4", "https://v11-weba.douyinvod.com/high.mp4"], facts={"codec": "hevc", "has_audio": True}, bitrate=3000000),
             complete("1080-h264", 1920, 1080, bitrate=6000000),
             complete("576-h264", 1024, 576, bitrate=2000000)]
    add("video/multi-quality", "Spatial quality wins over bitrate; complete 4K, 1080p, 720p and 576p files retain all CDNs and zero statistics", work(rates))
    add("video/unknown-fallback", "Unmeasured fallback does not manufacture geometry, encoding or file size", work(play_addr={"url_list": ["https://v3-web.douyinvod.com/unknown.mp4"]}))
    add("video/neutral-filename-dash", "Explicit DASH signal and matching audio produce a mux despite a neutral filename; the default stays the complete 1080p file", work([complete(width=1920, height=1080), track(urls=["https://media.example/neutral.bin"])], [audio(), audio("unrelated")]))
    add("video/dash-audio-missing", "A DASH video cannot borrow an unrelated audio track", work([complete(width=1920, height=1080), track()], [audio("unrelated")]))
    add("video/bare-track-only", "A bare track without any explicitly matched audio remains deferred and cannot become a file default", work([track()], [audio("unrelated")]))
    add("pairing/mux-only", "Two explicitly paired mux versions without any complete file keep a processing-required default reason", work([track(), track("audio-b")], [audio(), audio("audio-b"), audio("unrelated")]))
    add("video/silent-file", "An explicitly silent MP4 is not selected as the audio-bearing default", work([complete("silent", facts={"has_audio": False}), complete("audible", 1920, 1080)]))
    for name, facts in [("alternate-language", {"audio_language": "ja", "audio_language_role": "alternate", "audio_channels": 2}),
                        ("multichannel", {"audio_language": "en", "audio_channels": 6})]:
        add("audio-capability/" + name, "The four-capability public policy sees actual audio sub_info extracted by the Douyin formatter", work(
            [complete(facts={"has_audio": True, "audio_language": "en", "audio_language_role": "main", "audio_channels": 2}), track()], [audio(facts=facts)]))
    aliases = work()
    aliases["video"]["bitRateList"] = [{"gearName": "UHD", "is_h265": 1, "bit_rate": 7000000,
                                         "playAddr": [{"src": "http://v26-web.douyinvod.com/alias.mp4", "width": 3840, "height": 2160, "dataSize": 4200000}]}]
    add("video/bit-rate-list-alias", "Camel-case upstream representation fields and v26 host rewrite remain supported", aliases)

    for identity in (True, False):
        for order, missing, duplicate in [("forward", False, False), ("reverse", False, False), ("missing-first", True, False), ("duplicate", False, True)]:
            tracks = [track("audio-a", identity=identity), track("audio-b", identity=identity)]
            if order == "reverse": tracks.reverse()
            if duplicate: tracks += copy.deepcopy(tracks)
            audios = ([audio("audio-b")] if missing else [audio(), audio("audio-b")]) + [audio("unrelated")]
            add(f"pairing/{'identity' if identity else 'source'}/{order}", "Same URL retains both explicit A/B pairings; missing and unrelated audio do not cross-pair; exact duplicates collapse", work([complete(), *tracks], audios))

    for name, first, second in [("fps-later", {}, {"fps": 60}), ("hdr-later", {}, {"hdr_type": "hdr10"}),
                                ("fps-conflict", {"fps": 30}, {"fps": 60}), ("hdr-conflict", {"hdr_type": "sdr"}, {"hdr_type": "hdr10"})]:
        for main in (False, True):
            for reverse in (False, True):
                tracks = [track("audio-a", first), track("audio-a", second)]
                if reverse: tracks.reverse()
                file = complete(bitrate=1000000 if main else 6000000)
                rates = [*tracks, file] if main else [file, *tracks]
                add(f"observation/{name}/{'main' if main else 'backup'}/{'reverse' if reverse else 'forward'}", "Same video/audio pairing retains later FPS/HDR observations and conflicts at both main/backup and backup/backup boundaries", work(rates, [audio(), audio("unrelated")]))

    for identity in (True, False):
        for name, first, second in [("fps-later", {}, {"fps": 60}), ("hdr-conflict", {"hdr_type": "sdr"}, {"hdr_type": "hdr10"})]:
            for reverse in (False, True):
                tracks = [track("audio-a", first, identity), track("audio-b", second, identity)]
                if reverse: tracks.reverse()
                add(f"merge/{'identity' if identity else 'source'}/{name}/{'reverse' if reverse else 'forward'}", "Shared video facts merge across explicit audio pairings without discarding either audio or assigning an unrelated one", work([complete(bitrate=6000000), *tracks], [audio(), audio("audio-b"), audio("unrelated")]))

    for resource in (False, True):
        for reverse in (False, True):
            if resource:
                observations = [audio("audio-a", {"has_audio": False}, "https://media.example/audio-cdn-a.m4a"), audio("audio-a", {"audio_channels": 2}, "https://media.example/audio-cdn-b.m4a")]
                if reverse: observations.reverse()
                rates = [complete("independent-file", 1920, 1080), track("audio-a"), track("audio-b")]
                audios = observations + [audio("audio-b")]
            else:
                observations = [complete("shared-file", urls=["https://media.example/cdn-a.mp4"], facts={"has_audio": False}), complete("shared-file", urls=["https://media.example/cdn-b.mp4"], facts={"audio_channels": 2})]
                if reverse: observations.reverse()
                rates = observations + [complete("independent-file", 1920, 1080), track()]
                audios = [audio()]
            add(f"conflict/{'audio-resource' if resource else 'complete-file'}/{'reverse' if reverse else 'forward'}", "Individually valid audio observations become contradictory only after identity merge; isolate the deferred candidate and preserve independent files/pairings", work(rates, audios))

    for mode in ("cdn", "play"):
        for state, expiry, signature in [("before", BEFORE, "a" * 32), ("after", AFTER, "b" * 32)]:
            if mode == "cdn":
                urls = [f"https://{host}/{signature}/{expiry:08x}/video/tos/cn/tos-cn-ve-15/fixture-stable-video-resource/?mime_type=video_mp4&custom=retained" for host in ["v26-web.douyinvod.com", "v11-weba.douyinvod.com"]]
            else:
                urls = [f"https://www.douyin.com/aweme/v1/play/?video_id=renewal-video&file_id=renewal-file&ratio=1080p&line={line}&sign={signature}&biz_sign=synthetic-{state}" for line in [0, 1]]
            if state == "after": urls.reverse()
            detail = work([complete("renewal-file", 1920, 1080, urls=urls)])
            detail["video"]["cdn_url_expired"] = expiry
            add(f"renewal/{mode}/{state}", "Full Parse credential renewal changes signatures, expiry and source input order while preserving media/source identity and a usable server default", detail,
                group={"id": "renewal/" + mode, "role": state, "fixed_now": NOW, "source_expiry": expiry, "frontend_renewal_pair": True})

    probe_input = original_url("original-fixture")
    for name, metadata, failure in [
        ("higher-resolution", {"width": 3840, "height": 2160, "codec": "hevc", "duration": 10, "bit_rate": 4000000, "size": 6000000, "metadata_source": "mp4"}, None),
        ("same-size-higher-bitrate", {"width": 1920, "height": 1080, "codec": "h264", "duration": 10, "bit_rate": 9000000, "size": 12000000, "metadata_source": "mp4"}, None),
        ("smaller-higher-bitrate", {"width": 1280, "height": 720, "codec": "h264", "duration": 10, "bit_rate": 9000000, "size": 12000000, "metadata_source": "mp4"}, None),
        ("metadata-unknown", {}, None), ("unavailable", None, None), ("throws", None, "Synthetic metadata-probe failure"),
    ]:
        p = {"url": probe_input, "result": None if metadata is None else {"url": "https://v5-test.douyinvod.com/original.mp4", "metadata": metadata}}
        if failure: p["error"] = failure
        add("original/" + name, "Bounded probe outcome either improves the main representation or preserves the known file and unverified original route", work([complete(width=1920, height=1080)], play_addr={"uri": "original-fixture"}), probes=[p])
    add("original/only-route-unavailable", "No measured alternatives retains the original route without invented geometry", work(play_addr={"uri": "original-fixture"}), probes=[{"url": probe_input, "result": None}])

    music = {"title": "Synthetic background audio", "author": "Fixture musician", "play_url": {"url_list": ["http://media.example/background.mp3"]}, "cover_thumb": {"url_list": ["//images.example/music.jpg"]}}
    album = work(); album["images"] = [{"url_list": ["http://images.example/one.jpg", "https://images.example/one-backup.jpg"]}, {"urlList": ["https://images.example/two.jpg"]}]; album["music"] = music
    add("album/images-background-audio", "Image order and background audio become distinct typed media with one album group", album)
    alias = copy.deepcopy(album); alias["image_list"] = alias.pop("images"); alias["create_time"] = "invalid"
    add("album/image-list-alias", "image_list alias and unknown publication time remain valid", alias)
    direct = work(); direct["images"] = [
        {"url_list": ["https://images.example/live-1.jpg"], "video": {"play_addr": {"url_list": ["http://v26-web.douyinvod.com/live-1.mp4"]}}},
        {"url_list": ["https://images.example/still-2.jpg"]},
        {"url_list": ["https://images.example/live-3.jpg"], "video": {"playApi": "https://v3-web.douyinvod.com/live-3.mp4?x=a%2Bb#fragment"}},
    ]; direct["music"] = music
    add("live/direct-and-still", "Live and still entries preserve order, item/resource refresh context, mime placement and background audio", direct)
    redirected = work(); redirected["images"] = [
        {"url_list": ["https://images.example/live-a.jpg"], "video": {"play_addr": {"uri": "live-vid", "url_list": ["https://v3-web.douyinvod.com/fallback.mp4"]}}},
        {"url_list": ["https://images.example/live-b.jpg"], "video": {"play_addr": {"uri": "live-vid", "url_list": ["https://v3-web.douyinvod.com/fallback.mp4"]}}},
    ]
    live_original = original_url("live-vid")
    live_cdn = "https://v5-test.douyinvod.com/live-original.mp4"
    add("live/head-redirect-deduplicated", "Repeated live vid makes one manual redirect chain and shares the resolved original source", redirected, suffix=[
        {"method": "HEAD", "url": live_original, "status": 302, "headers": {"Location": live_cdn}, "follow_redirects": False},
        {"method": "HEAD", "url": live_cdn, "status": 200, "headers": {"Content-Type": "video/mp4"}, "follow_redirects": False},
    ])
    add("live/head-fails-known-fallback", "Failed live original HEAD retains the existing URL without discarding the still resource", redirected, suffix=[
        {"method": "HEAD", "url": live_original, "status": 0, "headers": {}, "error": "Synthetic connection failure", "follow_redirects": False},
    ])

    short = "https://v.douyin.com/synthetic/"
    add("entry/short-link-get", "Short-link GET redirect history resolves the detail ID", work([complete()]), url=short,
        prefix=[{"method": "GET", "url": short, "status": 200, "headers": {}, "history": [URL], "final_url": URL, "follow_redirects": True}])
    add("entry/short-link-head-fallback", "Short-link GET without Location uses the existing followed HEAD fallback", work([complete()]), url=short,
        prefix=[{"method": "GET", "url": short, "status": 200, "headers": {}, "body": "", "follow_redirects": True},
                {"method": "HEAD", "url": short, "status": 200, "headers": {}, "final_url": URL, "follow_redirects": True}])
    for name, body in [("invalid-json", "not JSON"), ("missing-status", compact({"aweme_detail": {"aweme_id": AWEME_ID}})),
                       ("blocked-status", compact({"status_code": 8})), ("empty-detail", compact({"status_code": 0, "aweme_detail": {}})),
                       ("mismatched-id", compact({"status_code": 0, "aweme_detail": {"aweme_id": "7628438784602389248"}}))]:
        add("entry/" + name, "Actual PHP Parse error mapping for malformed or mismatched detail responses", None, body=body)
    add("entry/transport-failure", "Actual PHP Parse transport failure remains retryable upstream unavailability", None,
        prefix=[{"method": "GET", "url": detail_url(), "status": 0, "headers": {}, "error": "Synthetic connection failure", "follow_redirects": True}])
    return cases


def check_observations(cases):
    """Check intended coverage without supplying/replacing any PHP expectations."""
    by_id = {case["id"]: case for case in cases}
    for group in ("cdn", "play"):
        before = by_id[f"renewal/{group}/before"]["expected"]["normal"]["data"]["items"][0]
        after = by_id[f"renewal/{group}/after"]["expected"]["normal"]["data"]["items"][0]
        if before["id"] != after["id"] or before["default"]["variant_id"] != after["default"]["variant_id"]:
            raise SystemExit("PHP renewal fixture lost item/default identity")
        first = before["variants"][0]
        second = after["variants"][0]
        a = {source["id"]: source for source in first["delivery"]["sources"]}
        b = {source["id"]: source for source in second["delivery"]["sources"]}
        if first["id"] != second["id"] or a.keys() != b.keys() or len(a) != 2:
            raise SystemExit("PHP renewal fixture lost variant/explicit-source identity")
        if not all(a[key]["url"] != b[key]["url"] and a[key]["expires_at"] < b[key]["expires_at"] for key in a):
            raise SystemExit("PHP renewal fixture failed to renew complete URL/expiry observations")
    for identity in ("identity", "source"):
        expected = by_id[f"pairing/{identity}/duplicate"]["expected"]
        if len(expected["debug"]["data"]["items"][0]["variants"]) != 3 or len(expected["normal"]["data"]["items"][0]["variants"]) != 1:
            raise SystemExit("PHP duplicate pairing/filter fixture does not exercise the intended path")


def build_numeric_cases():
    cases = []
    values = [0.5005, 0.5015, 1.0005, 500.49999999999994, 500.49999999999,
              0.005, 1.005, 0.00000005, 5e-324, 99999999999999.5, 1000000000000000.5, 1e300]
    for precision in (0, 2, 3):
        for index, value in enumerate(values):
            for sign in (1, -1):
                cases.append({"id": f"round/p{precision}/{index:02d}/{'positive' if sign == 1 else 'negative'}",
                              "operation": "round", "scenario": "Actual PHP default half-up rounding near decimal ties and finite magnitude boundaries",
                              "input": {"value": sign * value, "precision": precision}})
    return cases


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--php", required=True, type=pathlib.Path)
    ap.add_argument("--repo", default=ROOT, type=pathlib.Path)
    ap.add_argument("--output", default=ROOT / "backend/testdata/php-douyin", type=pathlib.Path)
    args = ap.parse_args()
    repo = args.repo.resolve()
    if subprocess.check_output(["git", "rev-parse", BASE + "^{tree}"], cwd=repo, text=True).strip() != TREE:
        raise SystemExit("Approved PHP commit/tree mismatch")
    archive = subprocess.check_output(["git", "archive", BASE], cwd=repo)
    with tempfile.TemporaryDirectory(prefix="dokitool-douyin-php-") as temp:
        baseline = pathlib.Path(temp)
        with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
            tar.extractall(baseline, filter="data")
        inputs = build_cases()
        command = [str(args.php.resolve()), "-d", "disable_functions=" + DISABLED, str(ROOT / "scripts/go-migration/produce-douyin.php"), str(baseline)]
        numeric_inputs = build_numeric_cases()
        proc = subprocess.run(command, input=dump({"cases": inputs, "numeric_cases": numeric_inputs}), text=True, capture_output=True)
        if proc.returncode or proc.stderr.strip():
            raise SystemExit("PHP Douyin fixture producer failed:\n" + proc.stderr + proc.stdout[:3000])
        observed = json.loads(proc.stdout)
        if [c["id"] for c in inputs] != [c["id"] for c in observed["cases"]]:
            raise SystemExit("PHP fixture case identity/order mismatch")
        cases = []
        for original, actual in zip(inputs, observed["cases"]):
            case = copy.deepcopy(original)
            case["expected"] = actual["expected"]
            cases.append(case)
        check_observations(cases)
        out = args.output.resolve()
        out.mkdir(parents=True, exist_ok=True)
        (out / "cases.json").write_text(dump({"format_version": 1, "cases": cases}))
        (out / "probe.json").write_text(dump({"format_version": 1, "cases": observed["probe_cases"]}))
        if [c["id"] for c in numeric_inputs] != [c["id"] for c in observed["numeric_cases"]]:
            raise SystemExit("PHP numeric fixture identity/order mismatch")
        numeric = []
        for original, actual in zip(numeric_inputs, observed["numeric_cases"]):
            case = copy.deepcopy(original); case["expected"] = actual["expected"]; numeric.append(case)
        (out / "numeric.json").write_text(dump({"format_version": 1, "cases": numeric}))
        sources = [
            "backend/api/douyin/DouyinParser.php", "backend/lib/runtime.php", "backend/lib/douyin-media-probe.php", "backend/lib/media-probe.php", "backend/lib/weibo-media.php",
            "backend/lib/media-adapters.php", "backend/lib/media-contract.php", "backend/lib/media-metadata.php", "backend/lib/media-policy.php", "backend/lib/platforms.php", "backend/config/platforms.json",
            "backend/tests/parser-contract.php", "backend/tests/douyin-media-contract.php", "backend/tests/douyin-observation-contract.php", "backend/tests/unified-review-identity.php", "backend/tests/media-merge-contract.php",
            "docs/schema/unified-v1/unified-v1.schema.json",
        ]
        manifest = {
            "format_version": 1, "baseline_commit": BASE, "baseline_tree": TREE,
            "fixed_clock": {"rfc3339": CLOCK, "unix_seconds": NOW},
            "evidence_scope": "Synthetic upstream detail and ISO BMFF records derived from approved PHP regressions. Unmodified archived PHP Parse, formatter, adapter, default selector, ordinary/debug policy and bounded probe generate every expectation. No platform network or online quality claim; no Go-derived expected output.",
            "transport_substitution": {
                "mechanism": "Development PHP process disables native cURL/get_headers and installs exact in-memory request fixtures before loading the immutable archive. Media probe uses its existing injectable callback. Unknown, reordered or unused requests fail the producer even if application code catches the immediate error.",
                "disabled_native_functions": DISABLED.split(","),
                "command_template": "PHPRC=<php.ini> <php> -d disable_functions=" + DISABLED + " scripts/go-migration/produce-douyin.php <approved-git-archive>",
                "captured_request_fields": ["method", "url", "headers", "follow_redirects", "transport"],
                "probe_transport": "Actual bounded-reader callback records requested URL, offset, length and exact status/headers/body bytes. No full media payload is downloaded or stored; base64 contains only synthetic requested slices.",
            },
            "php_version": subprocess.check_output([str(args.php.resolve()), "-r", "echo PHP_VERSION;"], text=True),
            "parse_case_count": len(cases), "public_envelope_count": 2 * len(cases),
            "probe_metadata_case_count": sum(c["operation"] == "metadata" for c in observed["probe_cases"]),
            "probe_transport_case_count": sum(c["operation"] == "probe" for c in observed["probe_cases"]),
            "numeric_round_case_count": len(numeric),
            "renewal_groups": [
                {"id": "renewal/cdn", "before": "renewal/cdn/before", "after": "renewal/cdn/after", "use": "Frontend default/explicit-source/task-snapshot renewal with actual Go outputs"},
                {"id": "renewal/play", "before": "renewal/play/before", "after": "renewal/play/after", "use": "Known play-endpoint sign/biz_sign credential renewal"},
            ],
            "files": {name: digest(out / name) for name in ["cases.json", "probe.json", "numeric.json"]},
            "source_files": [{"path": path, "sha256": digest(baseline / path)} for path in sources],
            "producer_files": [{"path": path, "sha256": digest(ROOT / path)} for path in ["scripts/go-migration/produce-douyin.py", "scripts/go-migration/produce-douyin.php"]],
            "dynamic_difference_rules": [
                "The actual adapter is called first; its successful data is re-finalized through the unchanged PHP snapFinalizeData with the explicit fixed now. Both public views then use the same clock. generated_at is compared exactly.",
                "Source selection internally reads wall time in approved PHP. Synthetic sources expire in 2100 or 2101; rank remains stable before 2100. Renewal before/after deliberately preserve exact distinct expires_at values.",
                "The private PHP probe unknown metadata array [] is allowed to map to an empty Go Object {}; this exception applies only to probe results, never to public envelopes or arbitrary empty arrays.",
                "JSON object key order and equivalent JSON number spellings are non-semantic. Exact integers, string bytes, all arrays and their order, missing/null, false/zero, source URLs, signatures, IDs, metadata, explicit audio pairing, defaults and policy decisions remain significant.",
                "No global stripping of URLs, signatures, IDs, expiry, source order, navigation URLs or metadata is allowed. Old/new renewal outputs are separately frozen rather than normalized into one response.",
                "Transport labels curl/get_headers describe the PHP test harness, not a required Go implementation detail. Compare method, URL, redirect policy and relevant request headers; Go may implement both through fetch.Executor.",
                "Parser probe failures are deliberate inputs; harness fixture violations are separate and fatal. All ordinary/debug envelopes, including failures, passed the actual archived PHP validator during production.",
                "numeric.json freezes actual PHP round(value, precision) with its default half-up mode, at precisions 0, 2 and 3. Inputs preserve JSON numeric values; no Go result or hand-calculated expectation is used. Three mdhd duration fixtures exercise this behavior through the actual MP4 reader.",
            ],
        }
        (out / "manifest.json").write_text(dump(manifest))
        print(compact({"parse_cases": len(cases), "public_envelopes": 2 * len(cases), "probe_cases": len(observed["probe_cases"]), "numeric_cases": len(numeric), "baseline": BASE}))


if __name__ == "__main__":
    main()
