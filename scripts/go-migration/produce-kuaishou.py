#!/usr/bin/env python3
"""Freeze real Kuaishou PHP parser/480 entry/public output with controlled IO."""
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

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASE = "25beaee107f8c1a208ced9deb3c637068445e316"
TREE = "e4b9d40f5e6c12a86fff629c105553886b434dcd"
CLOCK, NOW = "2026-10-30T12:00:00Z", 1793361600
ID = "3xsynthetic7"
URL = f"https://www.kuaishou.com/short-video/{ID}"
SHORT = "https://v.kuaishou.com/synthetic/"
DIRECTORY = ROOT / "backend/internal/platform/kuaishou/testdata"
DISABLED = "curl_init,curl_setopt_array,curl_setopt,curl_exec,curl_getinfo,curl_close,curl_error,curl_errno,get_headers"
ENTRY_FLAGS = 480
PRODUCERS = ["scripts/go-migration/produce-kuaishou.py", "scripts/go-migration/produce-kuaishou.php"]
SOURCES = ["backend/api/kuaishou/KuaishouSpider.php", "backend/api/kuaishou/ksjx.php",
           "backend/lib/runtime.php", "backend/lib/gateway.php", "backend/lib/media-adapters.php",
           "backend/lib/media-contract.php", "backend/lib/media-metadata.php", "backend/lib/media-policy.php",
           "backend/lib/platforms.php", "backend/lib/weibo-media.php", "backend/config/platforms.json",
           "backend/tests/unified-contract.php", "docs/schema/unified-v1/unified-v1.schema.json"]


def dump(value):
    return json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n"


def compact(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def media(name="main", **extra):
    return {"url": f"https://media.example/{name}.mp4?signature=synthetic%2Bvalue+kept&deadline=4102444800", **extra}


def photo(**extra):
    return {"photoType": "VIDEO", "caption": "快手 A+B %25 😀", "userName": "合成作者",
            "headUrl": "https://images.example/avatar.jpg", "likeCount": 0, "timestamp": 1746700800000,
            "coverUrls": [{"url": "https://images.example/cover.jpg"}],
            "mainMvUrls": [media()], **extra}


def init_page(work=None, *, state=None):
    if state is None:
        state = {"ignored": {"photo": photo(caption="must not win")},
                 "tusjoh-first": {"fid": ID, "photo": photo() if work is None else work}}
    return "<script>window.INIT_STATE = " + compact(state) + ";</script>"


def apollo_page(*, content_id=ID, video=None, authors=None, trailing=False, function_tail=False):
    if video is None:
        video = {"photoUrl": media()["url"], "caption": "APOLLO 视频", "coverUrl": "https://images.example/apollo.jpg"}
    if authors is None:
        authors = {"VisionVideoDetailAuthor:z-first": {"name": "首位作者", "headerUrl": "https://images.example/author-z.jpg"},
                   "VisionVideoDetailAuthor:a-second": {"name": "次位作者", "headerUrl": "https://images.example/author-a.jpg"}}
    state = {"defaultClient": {f"VisionVideoDetailPhoto:{content_id}": video, **authors}}
    text = compact(state)
    if trailing:
        text = text[:-1] + ",}"
    if function_tail:
        text += ";(function(){}());"
    return "<script>window.__APOLLO_STATE__ = " + text + "</script>"


def response(url, *, method="GET", status=200, body="", final_url=None, **extra):
    return {"method": method, "url": url, "status": status, "headers": {}, "body": body,
            "follow_redirects": True, "final_url": url if final_url is None else final_url, **extra}


def build_cases():
    cases = []

    def add(name, purpose, *, url=URL, html=None, final_url=None, head=None, page=None, fetch_page=True):
        final = url if final_url is None else final_url
        steps = [response(url, method="HEAD", final_url=final) if head is None else head]
        if fetch_page:
            steps.append(response(final, body=init_page() if html is None else html) if page is None else page)
        cases.append({"id": "kuaishou/" + name, "purpose": purpose,
                      "input": {"url": url, "now": NOW, "http_responses": steps}})

    add("init/minimal", "Actual INIT_STATE video extraction with empty configured Cookie and known zero likes")
    rich = photo(mainMvUrls=[media("main-a"), media("main-b")], music={"name": "背景音乐", "artist": "歌手",
                 "imageUrls": [{"url": "https://images.example/music.jpg"}], "audioUrls": [{"url": "https://media.example/music.mp3"}]})
    rich["manifest"] = {"adaptationSet": [{"representation": [
        {"url": media("4k")["url"], "backupUrl": [media("4k-backup")["url"], media("4k-extra", size=50000000)],
         "width": 3840, "height": 2160, "codec": "hevc", "avgBitrate": 8134, "maxBitrate": 16384},
        {"url": media("720")["url"], "backupUrl": ["javascript:alert(1)", media("720-backup")],
         "width": "1280", "height": "720", "codec": "h264"},
        {"url": "", "backupUrl": ["https://media.example/backup-only.mp4"], "codec": ""},
        {"url": "javascript:alert(1)", "backupUrl": []},
    ]}]}
    add("manifest/all-representations", "Keep main source group, representations, backup order and actual dimensions/codecs; never infer bitrate units", html=init_page(rich))
    as_string = copy.deepcopy(rich); as_string["manifest"] = compact(as_string["manifest"])
    add("manifest/json-string", "Manifest may be JSON string and follows the same representation semantics", html=init_page(as_string))
    add("manifest/no-main-sources", "A valid manifest supplies first legacy URL when mainMvUrls is absent",
        html=init_page({k: v for k, v in rich.items() if k != "mainMvUrls"}))
    add("manifest/unknown-bitrate-units", "avgBitrate/maxBitrate without dimensions or codec cannot manufacture technical facts",
        html=init_page(photo(mainMvUrls=[], manifest={"adaptationSet": [{"representation": [
            {"url": media()["url"], "avgBitrate": 3469, "maxBitrate": 9568}]}]})))
    add("manifest/duplicate-source", "Duplicate and overlapping source groups preserve original extraction before unified identity merging",
        html=init_page(photo(mainMvUrls=[media(), media()], manifest={"adaptationSet": [{"representation": [
            {"url": media()["url"], "backupUrl": [media("backup")["url"]], "width": 1920, "height": 1080}]}]})))
    add("manifest/malformed-string", "Malformed optional manifest does not discard usable mainMvUrls", html=init_page(photo(manifest="{bad")))
    add("manifest/dash", "Recognize existing DASH delivery without claiming a directly downloadable complete file",
        html=init_page(photo(mainMvUrls=[{"url": "https://media.example/MANIFEST.MPD?signature=kept%2Bbytes"}])))
    add("manifest/hls", "Existing HLS recognition remains a structured unsupported delivery",
        html=init_page(photo(mainMvUrls=[{"url": "https://media.example/playlist.m3u8?signature=kept%2Bbytes"}])))
    add("album/ordered-images", "Atlas precedes video and preserves image order; duplicate images follow unchanged adapter behavior",
        html=init_page(photo(ext_params={"atlas": {"list": ["z-first.jpg", "a-second.jpg", "z-first.jpg"], "music": "/background.mp3"}})))
    add("album/without-music-path", "Original atlas emits its literal base music URL even when path is absent",
        html=init_page(photo(ext_params={"atlas": {"list": ["one.jpg"]}})))
    add("image/photo-type", "SINGLE_PICTURE precedes video and returns exact first cover",
        html=init_page(photo(photoType="SINGLE_PICTURE")))
    add("image/boolean-marker", "Strict true singlePicture flag works without changing catalog Live capabilities",
        html=init_page(photo(singlePicture=True)))
    add("image/empty-cover-falls-to-video", "Empty cover cannot create a single image; available video remains",
        html=init_page(photo(photoType="SINGLE_PICTURE", coverUrls=[])))
    add("image/numeric-marker-is-not-true", "Numeric singlePicture is not PHP strict boolean true",
        html=init_page(photo(singlePicture=1)))
    add("music/soundtrack-fallback", "Absent music uses soundTrack names, avatar fallback and audio URL",
        html=init_page(photo(soundTrack={"name": "音轨", "artist": "创作者", "avatarUrls": [{"url": "https://images.example/audio.jpg"}],
                                        "audioUrls": [{"url": "https://media.example/audio.mp3"}]})))
    add("music/empty-primary", "Explicit empty music blocks soundTrack fallback through PHP null-coalescing semantics",
        html=init_page(photo(music=[], soundTrack={"audioUrls": [{"url": "https://media.example/ignored.mp3"}]})))
    add("init/first-matching-object", "Original JSON insertion order selects z-first before a-second",
        html=init_page(state={"tusjoh-z": {"photo": photo(caption="z-first")}, "tusjoh-a": {"photo": photo(caption="a-second")}}))
    add("init/first-empty-is-not-next-work", "A later valid tusjoh work is not used when the first selected entry has no media",
        html=init_page(state={"tusjoh-z": {"fid": "no-media"}, "tusjoh-a": {"photo": photo()}}))
    numeric = photo(caption="900719925474099300123", userName="+0012", likeCount="000123", timestamp="1746700800000")
    add("entry/numeric-check", "Entry JSON_OPTIONS=480 converts numeric strings before the real unified adapter, including large numeric tokens",
        html=init_page(numeric))
    escaped = init_page().replace("\\", "\\\\").replace('"', '\\"')
    add("init/escaped-json-repair", "Original limited stripslashes recovery runs only after initial JSON failure", html=escaped)
    add("init/broken-json", "Malformed INIT_STATE emits original explicit JSON syntax error", html="<script>window.INIT_STATE = {broken};</script>")
    add("init/error-prevents-apollo", "A returned INIT_STATE error is not silently replaced by APOLLO success",
        html="<script>window.INIT_STATE = {broken};</script>" + apollo_page())
    add("init/empty-falls-to-apollo", "An empty valid INIT_STATE permits APOLLO fallback", html=init_page(state={}) + apollo_page())
    add("init/no-live-capability", "A Live marker without existing video/image fields stays unavailable",
        html=init_page(photo(photoType="LIVE", mainMvUrls=[])))
    add("apollo/minimal", "Actual APOLLO lookup uses precise requested ID and first author in JSON order", html=apollo_page())
    add("apollo/h265-manifest", "APOLLO manifestH265.json retains all representations and main photoUrl",
        html=apollo_page(video={"photoUrl": media("apollo-main")["url"], "caption": "H265", "manifestH265": {"json": compact(rich["manifest"])}}))
    photo_url = f"https://www.kuaishou.com/photo/{ID}"
    add("apollo/photo-path", "APOLLO photo path is an image even though legacy candidates are present", url=photo_url,
        html=apollo_page(video={"photoUrl": "https://images.example/apollo-photo.jpg", "caption": "照片"}))
    add("apollo/long-video", "Existing long-video entry dispatch remains supported",
        url=f"https://www.kuaishou.com/long-video/{ID}", html=apollo_page())
    add("apollo/trailing-comma", "Original APOLLO cleanup removes trailing object/array commas", html=apollo_page(trailing=True))
    add("apollo/function-tail", "Limited function placeholder cleanup is data parsing and never JavaScript execution", html=apollo_page(function_tail=True))
    add("apollo/trailing-slash-id", "PHP ID regex includes a trailing slash; do not silently normalize it",
        url=URL + "/", html=apollo_page(content_id=ID + "/"))
    add("apollo/wrong-work", "A different work's APOLLO key never supplies media", html=apollo_page(content_id="other"))
    add("apollo/broken-json", "Invalid APOLLO alone yields original no-media error", html="<script>window.__APOLLO_STATE__ = {broken}</script>")
    add("render/missing-state", "Missing recognized state yields explicit unavailable content", html="<html>ordinary page</html>")
    add("transport/short-head", "HEAD obtains final platform URL, followed by a separate GET of that exact URL",
        url=SHORT, final_url=URL, html=init_page())
    add("transport/head-failure", "HEAD transport failure stops before the page request",
        head=response(URL, method="HEAD", error="Synthetic connection failure"), fetch_page=False)
    add("transport/page-failure", "GET transport failure remains distinct from HEAD failure",
        page=response(URL, error="Synthetic connection failure"))
    add("transport/status-is-not-parsed-error", "PHP does not infer a parser failure merely from HTTP status when valid state is returned",
        head=response(URL, method="HEAD", status=403), page=response(URL, status=403, body=init_page()))
    add("entry/unrecognized-path", "Legacy obtains page before reporting an unsupported path",
        url="https://www.kuaishou.com/profile/synthetic", html=init_page())
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
    require(args.php is not None, "--php is required to produce independent expectations")
    frozen_inputs = DIRECTORY / "inputs.json"
    require(frozen_inputs.read_bytes() == dump(document).encode(), "Frozen inputs differ from reviewed producer")
    runtime = subprocess.check_output([str(args.php.resolve()), "-r", "echo PHP_VERSION;"], text=True).strip()
    require(runtime == "8.3.6", "PHP 8.3.6 required")
    with tempfile.TemporaryDirectory(prefix="dokitool-kuaishou-php-") as folder:
        baseline = pathlib.Path(folder); archive(args.repo.resolve(), baseline)
        command = [str(args.php.resolve()), "-d", "disable_functions=" + DISABLED, "-d", "allow_url_fopen=0",
                   str(ROOT / PRODUCERS[1]), str(baseline)]
        process = subprocess.run(command, input=dump(document), text=True, encoding="utf-8", capture_output=True)
        require(process.returncode == 0 and not process.stderr.strip(), "PHP Kuaishou oracle failed:\n" + process.stderr + process.stdout[:3000])
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
                    "evidence_scope": "Synthetic HTML through immutable PHP KuaishouSpider, native request configuration, bounded writer, 480 entry JSON, adapter and public functions. No Go-generated expectations or live platform/media evidence.",
                    "files": {"php-baseline.json": sha(output / "php-baseline.json")},
                    "input_files": [{"path": str(frozen_inputs.relative_to(ROOT)), "sha256": sha(frozen_inputs)}],
                    "source_files": [{"path": path, "sha256": sha(baseline / path)} for path in SOURCES],
                    "producer_files": [{"path": path, "sha256": sha(ROOT / path)} for path in PRODUCERS],
                    "transport_substitution": {"disabled_native_functions": DISABLED.split(","),
                        "mechanism": "Only cURL IO substituted; actual HEAD/GET configuration and bounded writer run. Each response includes the effective URL of native following; unknown/reordered/unused requests fail.",
                        "captured_request_fields": ["method", "url", "headers", "body", "follow_redirects", "auto_referer", "timeout_seconds", "max_redirects"]},
                    "comparison_rules": ["Entry JSON_NUMERIC_CHECK is preserved before adaptation; numeric strings may change type exactly as PHP 480 does.",
                        "Successful data re-finalized at the explicit public fixture clock; only JSON object key order and equivalent numeric spelling are non-semantic.",
                        "Array/representation/source order, URL bytes, default and stable identities remain exact; no metadata exclusions.",
                        "PHP records explicit cURL options; Go declares libcurl's implicit HEAD Accept: */* explicitly. That sole HEAD header is added only to comparison views.",
                        "The configured empty GET Cookie is retained. Both requests follow at most five redirects within a 20-second request deadline; only GET enables automatic Referer.",
                        "Go additionally applies fixed platform URL, DNS/public-IP and cancellation policy before every outbound hop; unauthorized redirects are tested separately and cannot be authorized by PHP fixture parity."]}
        (output / "manifest.json").write_bytes(dump(manifest).encode())
    print(compact({"baseline": BASE, "php_version": runtime, "cases": len(cases), "public_envelopes": len(cases) * 2}))


if __name__ == "__main__":
    main()
