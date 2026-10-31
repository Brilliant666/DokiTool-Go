#!/usr/bin/env python3
"""Freeze actual approved PHP behavior. Development-only; never used by Go runtime."""
from __future__ import annotations

import argparse
import base64
import copy
import datetime as dt
import hashlib
import hmac
import io
import json
import pathlib
import subprocess
import tarfile
import tempfile

BASE = "665bd55134982b9fe107aa1cd00373ba8da8ff3c"
TREE = "93744d4d718c3299f82640d2888e93252106db70"
CLOCK = "2026-10-30T12:00:00Z"
NOW = int(dt.datetime.fromisoformat(CLOCK.replace("Z", "+00:00")).timestamp())
FUTURE = "2100-01-01T00:00:00Z"
PAST = "2000-01-01T00:00:00Z"
ROOT = pathlib.Path(__file__).resolve().parents[2]


def dump(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n"


def digest(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def build_inputs(baseline: pathlib.Path) -> list[dict]:
    cases: list[dict] = []

    def add(identifier: str, operation: str, scenario: str, **inputs: object) -> None:
        cases.append({"id": identifier, "operation": operation, "scenario": scenario, "input": inputs})

    examples = baseline / "docs/schema/unified-v1/examples"
    for path in sorted(examples.glob("*.json")):
        payload = json.loads(path.read_text())
        add("design/" + path.stem, "validate_envelope", "Approved synthetic design fixture", payload=payload, must_accept=True)
        cases[-1]["source_fixture"] = str(path.relative_to(baseline))
        if payload["code"] == "OK":
            add("finalize/" + path.stem, "finalize_data", "PHP default selection at the original fixture clock", data=payload["data"], now=NOW)

    basic = json.loads((examples / "02-kuaishou-single-file.json").read_text())
    invalid = []
    def bad(name: str, mutate) -> None:
        payload = copy.deepcopy(basic)
        mutate(payload)
        invalid.append((name, payload))
    bad("missing-default", lambda p: p["data"]["items"][0].pop("default"))
    bad("null-eligible-default", lambda p: p["data"]["items"][0].update(default=None))
    bad("unknown-default-variant", lambda p: p["data"]["items"][0]["default"].update(variant_id="absent"))
    bad("wrong-default-url", lambda p: p["data"]["items"][0]["default"].update(url="https://media.example.test/different.mp4"))
    bad("duplicate-item", lambda p: p["data"]["items"].append(copy.deepcopy(p["data"]["items"][0])))
    bad("duplicate-variant", lambda p: p["data"]["items"][0]["variants"].append(copy.deepcopy(p["data"]["items"][0]["variants"][0])))
    bad("unknown-wire-field", lambda p: p.update(unrecognized=True))
    bad("wrong-schema-version", lambda p: p.update(schema_version="2.0"))
    bad("invalid-generation-time", lambda p: p["data"].update(generated_at="not-a-time"))
    bad("unknown-group-member", lambda p: p["data"].update(groups=[{"id":"g","kind":"album","item_ids":["absent"],"complete":True}]))
    bad("default-item-missing", lambda p: p["data"].update(default_item_id="absent"))
    bad("audio-contradiction", lambda p: p["data"]["items"][0]["variants"][0].update(metadata={"has_audio":False,"audio_channels":2,"evidence":"upstream"}))
    bad("fractional-width", lambda p: p["data"]["items"][0]["variants"][0].update(metadata={"width":1920.5,"height":1080,"evidence":"upstream"}))
    bad("null-has-audio", lambda p: p["data"]["items"][0]["variants"][0].update(metadata={"has_audio":None,"evidence":"upstream"}))
    bad("unknown-metadata", lambda p: p["data"]["items"][0]["variants"][0].update(metadata={"mystery":1,"evidence":"upstream"}))
    bad("non-http-source", lambda p: p["data"]["items"][0]["variants"][0]["delivery"]["sources"][0].update(url="javascript:alert(1)"))
    bad("invalid-source-expiry", lambda p: p["data"]["items"][0]["variants"][0]["delivery"]["sources"][0].update(expires_at="yesterday"))
    bad("forbidden-source-header", lambda p: p["data"]["items"][0]["variants"][0]["delivery"]["sources"][0].update(headers={"Cookie":"synthetic-not-a-secret"}))
    for name, payload in invalid:
        add("invalid/" + name, "validate_envelope", "Contract must reject this mutation of the compact valid response", payload=payload, must_accept=False)

    def file(ident="full", metadata=None, source=None):
        return {"id":ident,"metadata":{"width":3840,"height":2160,"evidence":"upstream",**(metadata or {})},
                "delivery":{"kind":"file","sources":[{"id":"s-"+ident,"url":"https://media.example.test/"+ident+".mp4",**(source or {})}]}}
    def mux(ident="mux", video=None, audio=None, metadata=None):
        result = {"id":ident,"delivery":{"kind":"mux","resources":[
            {"id":"video","role":"video","metadata":{"width":3840,"height":2160,"evidence":"upstream",**(video or {})},"delivery":{"kind":"file","sources":[{"url":"https://media.example.test/video.m4s"}]}},
            {"id":"audio","role":"audio","metadata":{"audio_channels":2,"evidence":"upstream",**(audio or {})},"delivery":{"kind":"file","sources":[{"url":"https://media.example.test/audio.m4a"}]}}
        ],"output":{"container":"mp4","extension":"mp4"}}}
        if metadata: result["metadata"]={"evidence":"upstream",**metadata}
        return result
    policies = [
        ("covered-same-resolution", [file(),mux()]),
        ("higher-resolution-only-mux", [file(metadata={"width":1920,"height":1080}),mux()]),
        ("high-fps", [file(metadata={"fps":30}),mux(video={"fps":60})]),
        ("fps-equivalent-5994-60", [file(metadata={"fps":59.94}),mux(video={"fps":60})]),
        ("fps-threshold-tolerance", [file(),mux(video={"fps":30.2})]),
        ("hdr-profile-not-covered", [file(metadata={"hdr_type":"sdr"}),mux(video={"hdr_type":"hdr10"})]),
        ("hdr-same-profile-covered", [file(metadata={"hdr_type":"hdr10"}),mux(video={"hdr_type":"hdr10"})]),
        ("hdr-unknown-kept", [file(metadata={"hdr_type":"hdr10"}),mux(video={"hdr_type":"hdr_unknown"})]),
        ("alternate-language", [file(metadata={"audio_language":"en"}),mux(audio={"audio_language":"ja","audio_language_role":"alternate"})]),
        ("language-without-main-evidence", [file(),mux(audio={"audio_language":"ja"})]),
        ("multichannel", [file(metadata={"audio_channels":2}),mux(audio={"audio_channels":6})]),
        ("multichannel-covered", [file(metadata={"audio_channels":8}),mux(audio={"audio_channels":6})]),
        ("expiry-not-coverage", [file(source={"expires_at":PAST}),mux()]),
        ("silent-file-not-coverage", [file(metadata={"has_audio":False}),mux()]),
        ("mux-only", [mux()]),
        ("unknown-geometry", [file(),mux(video={"width":2000,"height":2000})]),
        ("no-combined-advantages", [file("resolution",{"fps":30}),file("fps",{"width":1920,"height":1080,"fps":60}),mux(video={"fps":60})]),
        ("conflicting-fps", [file(metadata={"fps":60}),mux(video={"field_conflicts":{"fps":[{"value":30,"evidence":"upstream"},{"value":60,"evidence":"probe"}]}})]),
    ]
    for location in ("variant", "resource"):
        main=mux("main-en",video={"width":1920,"height":1080},audio={"audio_language":"en","audio_language_role":"main"} if location=="resource" else {},metadata={"audio_language":"en","audio_language_role":"main"} if location=="variant" else {})
        for state in ("valid","expired"):
            observed=copy.deepcopy(main)
            if state=="expired":
                for r in observed["delivery"]["resources"]: r["delivery"]["sources"][0]["expires_at"]=PAST
            policies.append(("main-language-"+location+"-"+state,[file(),observed,mux("ja",audio={"audio_language":"ja"})]))
    ambiguous=mux(metadata={"audio_language":"en","audio_language_role":"main"})
    ambiguous["delivery"]["resources"].append({"id":"audio2","role":"audio","delivery":{"kind":"file","sources":[{"url":"https://media.example.test/second.m4a"}]}})
    policies.append(("multi-audio-ambiguous-parent",[file(),ambiguous]))
    policies.append(("deferred-stays-visible",[file(),{"id":"later","state":"deferred","reason":{"code":"ORIGINAL_UNVERIFIED","message":"Synthetic deferred candidate"}}]))
    for name, variants in policies:
        data={"platform":"fixture","refresh_url":"/api/parse?schema_version=1.0&url=https%3A%2F%2Fsource.example.test%2Fwork",
              "items":[{"id":"video","type":"video","default":None,"variants":variants,"resolve_url":"/api/resolve?schema_version=1.0&item_id=video&url=https%3A%2F%2Fsource.example.test%2Fwork"}]}
        for debug in (False,True):
            add("policy/"+name+("/debug" if debug else "/normal"),"finalize_and_public_view","Same normalized candidates through PHP default and candidate policy",data=data,debug=debug,now=NOW)

    for name,a,b in [("dominates",(3840,2160),(1920,1080)),("rotated-equal",(1080,1920),(1920,1080)),("aspect-incomparable",(2000,2000),(1920,1080)),("close-aspect",(1920,1080),(1918,1080)),("wide-versus-tall",(2200,1000),(1920,1080)),("smaller",(1280,720),(1920,1080))]:
        add("geometry/"+name,"quality_compare","Spatial dominance preserves rotation and aspect-ratio tolerance",left={"metadata":{"width":a[0],"height":a[1]}},right={"metadata":{"width":b[0],"height":b[1]}})
    add("geometry/unknown","quality_compare","Labels do not manufacture geometry",left={"label":"4K original"},right={"metadata":{"width":1920,"height":1080}})
    add("geometry/mux-resource","quality_compare","Mux video-resource geometry is authoritative when parent omits it",left=mux(),right=file(metadata={"width":1920,"height":1080}))
    for a,b in [(59.94,60),(29.97,30),(30.2,30),(30.5,30),(60,120)]:
        add(f"fps/{a}-{b}","same_frame_rate","One-percent frame-rate tolerance",a=a,b=b)

    urls=[
        "https://www.douyin.com/aweme/v1/play/?file_id=video&sign=before&line=0",
        "https://www.douyin.com/aweme/v1/play/?file_id=video&sign=after&line=0",
        "https://www.douyin.com/aweme/v1/play/?file_id=video&line=0&line=1&sign=after&x=a%26b+z",
        "https://www.douyin.com/aweme/v1/play/?file_id=video&line=1&line=0&%73ign=after&x=a%26b+z",
        "https://www.douyin.com/aweme/v1/play/?sign=before#part",
        "https://www.douyin.com/aweme/v1/play/?#",
        "https://www.douyin.com/aweme/v1/play/",
        "https://www.douyin.com/other/?sign=must-stay",
        "https://f.video.weibocdn.com/v.mp4?media_id=big&template=1080&Expires=4102444800&ssig=synthetic&KID=1",
        "https://f.video.weibocdn.com/v.mp4?media_id=big&template=720&Expires=4102444800&ssig=synthetic&KID=1",
        "https://a.bilivideo.com/v.m4s?video_id=1&deadline=4102444800&upsig=synthetic&token=not-auth-here",
        "https://media.example.test/v.mp4?sign=meaningful&x=1&x=2#fragment",
        "https://media.example.test/v%2Fclip.mp4?x=%ZZ",
        "https://media.example.test/v.mp4?",
        "https://media.example.test/v.mp4#",
        "HTTPS://www.douyin.com/aweme/v1/play/?file_id=video&sign=before",
    ]
    for host,signature in [("v3-web.douyinvod.com","a"*32),("v3-web.douyinvod.com","b"*32),("v26-web.douyinvod.com","a"*32)]:
        urls.append(f"https://{host}/{signature}/ffffffff/video/tos/cn/tos-cn-ve-15/synthetic_media/media-video-avc1?line=0&x=%2B#piece")
    for i,url in enumerate(urls): add(f"source-identity/{i:02d}","source_identity","Exact URL identity rules without rewriting transport URLs",url=url)
    source_sets=[
        ("plain-dedup",["https://media.example.test/a.mp4","https://media.example.test/a.mp4"]),
        ("signed-renewal",[{"url":urls[0],"expires_at":PAST},{"url":urls[1],"expires_at":FUTURE}]),
        ("known-availability-wins",[{"url":urls[0],"availability":"available","expires_at":FUTURE},{"url":urls[1],"expires_at":"2101-01-01T00:00:00Z"}]),
        ("whole-observation",[{"url":"https://media.example.test/old.mp4","private_source_identity":"same","size_bytes":10,"expires_at":PAST},{"url":"https://other.example.test/new.mp4","private_source_identity":"same","expires_at":FUTURE}]),
        ("unicode-private-id",[{"url":"https://media.example.test/a.mp4","private_source_identity":"同一来源😀/\"\\\n\u0001"}]),
        ("large-private-id-string",[{"url":"https://media.example.test/a.mp4","private_source_identity":"9223372036854775800"}]),
        ("large-private-id-number",[{"url":"https://media.example.test/a.mp4","private_source_identity":9223372036854775800}]),
        ("source-metadata",[{"url":" //media.example.test/a.mp4 ","size":10,"size_bytes":11,"browser_fetch":"unknown","availability":"unknown"}]),
        ("bad-sources",[None,False,7,"javascript:alert(1)","https://media.example.test/汉字.mp4",{"url":"https://user@media.example.test/a.mp4"},"https://media.example.test/v%ZZ.mp4"]),
    ]
    for name,sources in source_sets: add("sources/"+name,"sources","Actual PHP source construction and credential selection",sources=sources,now=NOW)
    add("merge-sources/credential-pair","merge_sources","Expiry and availability select one complete observation",existing=[{"id":"s","url":urls[0],"expires_at":PAST,"size_bytes":100}],additional=[{"id":"s","url":urls[1],"expires_at":FUTURE}],now=NOW)

    raw_metadata=[
        {},{"width":1920.5,"height":1080,"duration":1.2345,"bit_rate":1234.5},
        {"frameRate":"60000/1001","dynamic_range":"HDR10+","channels":"6","language":"ZH_cn","is_alternate_language":True},
        {"fps":"broken","frame_rate":"24/1","is_hdr":True,"audio_language":"und","has_audio":False},
        {"bit_rate":9000,"metadata_source":"probe","codec":"avc1","container":"mp4","has_audio":True},
        {"bit_rate":9000,"private_delivery_kind":"video_track","fps":60,"audio_language":"ja","audio_language_role":"alternate"},
        {"audio_channels":0,"channels":8,"audio_language":"not a tag","sample_rate_hz":48000,"audio_bitrate_bps":192000},
    ]
    for i,raw in enumerate(raw_metadata): add(f"candidate-metadata/{i:02d}","candidate_metadata","PHP platform-independent metadata extraction",raw=raw)
    merge_pairs=[
        ({},{"fps":60,"hdr_type":"hdr10","evidence":"upstream"}),
        ({"fps":60,"evidence":"upstream"},{"fps":60,"evidence":"probe"}),
        ({"fps":30,"evidence":"upstream"},{"fps":60,"evidence":"probe"}),
        ({"hdr_type":"sdr","evidence":"upstream"},{"hdr_type":"hdr10","evidence":"upstream"}),
        ({"has_audio":False,"evidence":"upstream"},{"audio_channels":2,"evidence":"upstream"}),
        ({"audio_language":"en","evidence":"upstream"},{"audio_language":"ja","evidence":"probe"}),
        ({"fps":60,"evidence":"probe"},{"width":3840,"height":2160,"evidence":"upstream"}),
        ({"field_conflicts":{"fps":[{"value":30,"evidence":"upstream"},{"value":60,"evidence":"probe"}]}},{"fps":30,"evidence":"probe"}),
    ]
    for i,(first,second) in enumerate(merge_pairs):
        for reverse in (False,True):
            add(f"merge-metadata/{i:02d}/"+("reverse" if reverse else "forward"),"merge_metadata","Retain missing, agreeing and conflicting evidence without an input-order winner",existing=second if reverse else first,additional=first if reverse else second)

    audio_a={"url":"https://media.example.test/a.m4a","private_media_identity":"audio-a","audio_channels":2,"audio_language":"en","has_audio":True}
    audio_b={"url":"https://media.example.test/b.m4a","private_media_identity":"audio-b","audio_channels":6,"audio_language":"ja","has_audio":True}
    video={"url":"https://www.douyin.com/aweme/v1/play/dash/?file_id=video&sign=synthetic","private_media_identity":"same-video","private_delivery_kind":"video_track","width":3840,"height":2160,"has_audio":False}
    paired_a={**video,"private_audio_candidates":[audio_a]}
    paired_b={**video,"private_audio_candidates":[audio_b],"fps":60,"hdr_type":"hdr10"}
    complete={"url":"https://media.example.test/full.mp4","private_media_identity":"full","width":3840,"height":2160,"has_audio":True}
    file_cases=[
        ("compact-single",["https://media.example.test/one.mp4"]),
        ("unicode-identity",[{**complete,"private_media_identity":"视频😀/\"\\\n\u0001"}]),
        ("same-identity-cdn-facts",[{**complete,"fps":30},{**complete,"url":"https://mirror.example.test/full.mp4","hdr_type":"hdr10"}]),
        ("same-pair-later-facts",[complete,paired_a,{**paired_a,"fps":60,"hdr_type":"hdr10"}]),
        ("distinct-paired-audio",[complete,paired_a,paired_b]),
        ("repeated-pair",[complete,paired_a,paired_a,paired_b,paired_b]),
        ("missing-first-audio",[{**video,"private_audio_candidates":[]},paired_b]),
        ("conflicting-complete-isolation",[{**complete,"has_audio":False},{"url":complete["url"],"private_media_identity":"full","audio_channels":2},{**complete,"private_media_identity":"other","url":"https://media.example.test/other.mp4"}]),
        ("conflicting-audio-isolation",[{**video,"private_audio_candidates":[{**audio_a,"has_audio":False},{"url":audio_a["url"],"private_media_identity":"audio-a","audio_channels":2},audio_b]}]),
        ("hls-and-dash",[{"url":"https://media.example.test/a.m3u8","scope":"adaptive"},{"url":"https://media.example.test/a.mpd","scope":"adaptive"}]),
    ]
    for name,candidates in file_cases:
        for reverse in (False,True):
            add("variants/"+name+("/reverse" if reverse else "/forward"),"file_variants","Actual PHP candidate identity, aggregation and explicit audio composition",candidates=list(reversed(candidates)) if reverse else candidates,now=NOW)
    return cases


def build_ticket_sign_inputs() -> list[dict]:
    key="t"*32  # Deliberately public synthetic fixture key, never a deployment key.
    url="https://f.video.weibocdn.com/synthetic.mp4?media_id=fixture&template=1080&ssig=synthetic"
    candidates=[
        ("canonical",url,key,None),
        ("query-and-unicode",url+"&label=%E4%B8%AD%E6%96%87%F0%9F%98%80&x=a+b&x=a%2Bb&x=last",key,None),
        ("explicit-default-port",url.replace(".com/",".com:443/"),key,None),
        ("upstream-expiry",url,key,NOW+60),
        ("expired-upstream",url,key,NOW-1),
        ("weak-key",url,"short",None),
        ("foreign-host",url.replace("f.video.weibocdn.com","media.example.test"),key,None),
        ("raw-unicode-url",url+"&label=中文",key,None),
    ]
    return [{"id":"ticket/sign/"+name,"operation":"ticket_sign","scenario":"Approved PHP v1 ticket signing with a public synthetic key", "input":{"url":source,"key":secret,"now":NOW,"expires_at":expires}} for name,source,secret,expires in candidates]


def build_ticket_verify_inputs(signed: list[dict]) -> list[dict]:
    key="t"*32
    result=[]
    for case in signed:
        if case["expected"] is not None:
            result.append({"id":case["id"].replace("/sign/","/verify/"),"operation":"ticket_verify","scenario":"Verify a ticket actually minted by the approved PHP function", "input":{"ticket":case["expected"].split("?ticket=",1)[1],"key":key,"now":NOW}})
    token=result[0]["input"]["ticket"]
    for name,ticket,secret,now in [
        ("wrong-key",token,"x"*32,NOW),
        ("expired",token,key,NOW+1800),
        ("future-issued",token,key,NOW-1),
        ("signature-tampered",token[:-1]+("A" if token[-1]!="A" else "B"),key,NOW),
        ("malformed","not-a-ticket",key,NOW),
    ]:
        result.append({"id":"ticket/verify/"+name,"operation":"ticket_verify","scenario":"Approved PHP rejection of invalid ticket credentials or lifetime", "input":{"ticket":ticket,"key":secret,"now":now}})
    # Construct inputs only; expected acceptance/rejection still comes from PHP.
    def custom(payload):
        b64=lambda b:base64.urlsafe_b64encode(b).rstrip(b"=").decode("ascii")
        encoded=b64(json.dumps(payload,ensure_ascii=True,separators=(",",":")).encode())
        return encoded+"."+b64(hmac.new(key.encode(),encoded.encode(),hashlib.sha256).digest())
    url="https://f.video.weibocdn.com/synthetic.mp4?x=1&x=2"
    for name,payload in [
        ("ttl-too-long",{"v":1,"u":url,"iat":NOW,"exp":NOW+1801}),
        ("expiry-not-after-issue",{"v":1,"u":url,"iat":NOW,"exp":NOW}),
        ("invalid-version",{"v":2,"u":url,"iat":NOW,"exp":NOW+60}),
        ("foreign-host",{"v":1,"u":"https://media.example.test/file.mp4","iat":NOW,"exp":NOW+60}),
    ]:
        result.append({"id":"ticket/verify/"+name,"operation":"ticket_verify","scenario":"Input payload is synthetically signed; only PHP supplies the expected verification result", "input":{"ticket":custom(payload),"key":key,"now":NOW},"synthetic_payload":payload})
    return result


def main() -> None:
    ap=argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--php",required=True,type=pathlib.Path)
    ap.add_argument("--repo",default=ROOT,type=pathlib.Path)
    ap.add_argument("--output",default=ROOT/"backend/testdata/php-baseline",type=pathlib.Path)
    args=ap.parse_args()
    repo=args.repo.resolve()
    actual=subprocess.check_output(["git","rev-parse",BASE+"^{tree}"],cwd=repo,text=True).strip()
    if actual!=TREE: raise SystemExit("Approved PHP commit/tree mismatch")
    archive=subprocess.check_output(["git","archive",BASE],cwd=repo)
    with tempfile.TemporaryDirectory(prefix="dokitool-approved-php-") as temp:
        baseline=pathlib.Path(temp)
        with tarfile.open(fileobj=io.BytesIO(archive)) as tar: tar.extractall(baseline,filter="data")
        def observe(inputs):
            proc=subprocess.run([str(args.php.resolve()),str(ROOT/"scripts/go-migration/produce.php"),str(baseline)],input=dump({"cases":inputs}),text=True,capture_output=True,check=True)
            observed=json.loads(proc.stdout)["cases"]
            if proc.stderr.strip(): raise SystemExit("PHP baseline producer emitted diagnostics: "+proc.stderr)
            if [c["id"] for c in observed]!=[c["id"] for c in inputs]: raise SystemExit("Producer case identity/order mismatch")
            output=[]
            for original,result in zip(inputs,observed):
                case=copy.deepcopy(original);case['expected']=result['expected']
                if 'php_error' in result: case['php_error']=result['php_error']
                output.append(case)
            return output
        cases=observe(build_inputs(baseline))
        for case in cases:
            if case["operation"]=="validate_envelope" and case["expected"]["valid"]!=case["input"]["must_accept"]:
                raise SystemExit("Unexpected PHP contract outcome: "+case["id"])
            if case.get("php_error") and case["operation"]!="validate_envelope":
                raise SystemExit("Unexpected PHP operation error: "+case["id"]+": "+case["php_error"])
        out=args.output.resolve();out.mkdir(parents=True,exist_ok=True)
        document={"format_version":1,"cases":cases}
        (out/"cases.json").write_text(dump(document))
        signed=observe(build_ticket_sign_inputs())
        tickets=signed+observe(build_ticket_verify_inputs(signed))
        if any('php_error' in case for case in tickets): raise SystemExit("Unexpected PHP ticket exception")
        (out/"tickets.json").write_text(dump({"format_version":1,"synthetic_key_notice":"Every key is public synthetic fixture material, never a deployment key.","cases":tickets}))
        catalog_proc=subprocess.run([str(args.php.resolve()),str(baseline/"backend/bin/catalog.php")],capture_output=True,text=True,check=True)
        if catalog_proc.stderr.strip(): raise SystemExit("Unexpected PHP catalog diagnostics")
        (out/"catalog.json").write_text(dump({"operation":"platform_catalog","input":{},"producer":"backend/bin/catalog.php","expected":json.loads(catalog_proc.stdout)}))
        sources=["backend/lib/media-contract.php","backend/lib/media-adapters.php","backend/lib/media-metadata.php","backend/lib/media-policy.php","backend/lib/platforms.php","backend/lib/runtime.php","backend/lib/weibo-media.php","backend/bin/catalog.php","backend/config/platforms.json","docs/schema/unified-v1/unified-v1.schema.json","docs/schema/unified-v1/fixtures-manifest.json"]
        sources += [str(p.relative_to(baseline)) for p in sorted((baseline/"docs/schema/unified-v1/examples").glob("*.json"))]
        producers=["scripts/go-migration/produce.py","scripts/go-migration/produce.php"]
        manifest={"format_version":1,"baseline_commit":BASE,"baseline_tree":TREE,"fixed_clock":{"rfc3339":CLOCK,"unix_seconds":NOW},
          "evidence_scope":"Synthetic deterministic calls to the approved PHP implementation; no platform network, browser, real credentials or Go-derived expected output.",
          "php_version":subprocess.check_output([str(args.php.resolve()),"-r","echo PHP_VERSION;"],text=True),
          "case_count":len(cases),"operation_counts":{op:sum(c["operation"]==op for c in cases) for op in sorted({c["operation"] for c in cases})},
          "ticket_case_count":len(tickets),"catalog_case_count":1,
          "files":{name:digest(out/name) for name in ["cases.json","tickets.json","catalog.json"]},
          "source_files":[{"path":path,"sha256":digest(baseline/path)} for path in sources],
          "producer_files":[{"path":path,"sha256":digest(ROOT/path)} for path in producers],
          "dynamic_difference_rules":[
            "Explicit now is fixed per case; generated_at is compared exactly, not omitted.",
            "PHP snapMergeSources reads wall time internally. Its inputs use expiry in 2000 or 2100/2101; rank is stable while wall time is between those bounds. Go receives the fixed now. No production PHP source was changed.",
            "No URL, ID, order, signature parameter, expires_at, browser_fetch, source credential or policy field is globally stripped. Every output value is compared.",
            "Only internal candidate_metadata/merge_metadata empty PHP arrays are encoded as objects because these functions represent metadata objects; public envelope shapes are never normalized this way.",
            "JSON object key order and equivalent numeric spellings (30 and 30.0) are non-semantic. Array order, missing/null, false/zero, exact integers and string bytes remain significant.",
            "Validation compares accepted/rejected; php_error records the original diagnostic for evidence without forcing Go to copy PHP wording."
            ,"Ticket cases carry explicit signing/verification clocks and public synthetic keys. PHP-minted ticket bytes and PHP verification results are preserved exactly; negative token construction only generates inputs."
          ]}
        (out/"manifest.json").write_text(dump(manifest))
        print(json.dumps({"case_count":len(cases),"operation_counts":manifest["operation_counts"],"baseline_commit":BASE,"baseline_tree":TREE},sort_keys=True))


if __name__=="__main__": main()
