package bilibili

import (
	"strings"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// Redacted from two single-file parse/resolve responses: the raw parameter order
// and uparams membership match the observations. All content IDs, credentials
// and values below are synthetic.
const fixtureCDN = "https://upos-sz-mirrorcosov.bilivideo.com/upgcxcode/12/34/12345678901/12345678901-1-064.mp4"
const parseQuery = "e=enc&mid=0&oi=1&og=cdn&nbs=1&uipk=1&platform=pc&trid=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaah&gen=play&os=cdn&deadline=1700000100&upsig=11111111111111111111111111111111&uparams=e,mid,oi,og,nbs,uipk,platform,trid,gen,os,deadline&bvc=1&nettype=0&bw=1&lrs=1&V=v1.2.3&agrr=0&buvid=&build=0&dl=0&f=pc&orderid=0,1"
const resolveQuery = "e=enc&trid=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbh&oi=1&gen=play&og=cdn&mid=0&deadline=1700000200&os=cdn&nbs=1&uipk=1&platform=pc&upsig=22222222222222222222222222222222&uparams=e,trid,oi,gen,og,mid,deadline,os,nbs,uipk,platform&bvc=1&nettype=0&bw=1&lrs=1&V=v1.2.4&dl=0&f=pc&agrr=0&buvid=&build=0&orderid=0,1"

type durlChoice struct{ item, variant, source string }

func adaptedDURL(t *testing.T, bvid, cid string, page, quality int, urls ...string) []durlChoice {
	t.Helper()
	return durlWithFacts(t, bvid, cid, page, quality, "mp4_720p", 13131661, urls...)
}

func durlWithFacts(t *testing.T, bvid, cid string, page, quality int, format string, size int, urls ...string) []durlChoice {
	t.Helper()
	const now = 1700000000
	selected := object{"page": page, "cid": cid, "duration": 109}
	part := object{"url": urls[0], "backup_url": urls[1:]}
	play := object{"quality": quality, "durl": array{part}}
	if size > 0 {
		part["size"] = size
	}
	if format != "" {
		play["format"] = format
	}
	raw, err := legacyWire(buildResponse(object{"pages": array{selected}}, bvid, int64(page), selected,
		object{"code": 0, "data": play}))
	if err != nil {
		t.Fatal(err)
	}
	sourceURL := "https://www.bilibili.com/video/" + bvid + "/"
	data := adapt(unified.Map(raw["data"]), sourceURL, now)
	public := unified.PublicView(unified.FinalizeData(data, now), false, now)
	if err := unified.ValidateEnvelopeAt(public, now); err != nil {
		t.Fatal(err)
	}
	item := unified.Map(unified.Slice(unified.Map(public["data"])["items"])[0])
	vs := unified.Slice(item["variants"])
	if len(vs) != 1 {
		t.Fatalf("got %d variants", len(vs))
	}
	v := unified.Map(vs[0])
	ss := unified.Slice(unified.Map(v["delivery"])["sources"])
	if len(ss) != len(urls) {
		t.Fatalf("got %d sources, want %d", len(ss), len(urls))
	}
	got := make([]durlChoice, len(ss))
	for i, entry := range ss {
		s := unified.Map(entry)
		if s["url"] != urls[i] {
			t.Fatal("source was reordered or the signed address was rewritten")
		}
		got[i] = durlChoice{unified.Str(item["id"]), unified.Str(v["id"]), unified.Str(s["id"])}
	}
	if d := unified.Map(item["default"]); d["source_id"] != got[0].source || d["variant_id"] != got[0].variant || d["url"] != urls[0] {
		t.Fatal("default does not reference the first usable source")
	}
	return got
}

func TestSingleDURLRefreshIdentity(t *testing.T) {
	const bvid, cid = "BV1234567890", "12345678901"
	a, b := fixtureCDN+"?"+parseQuery, fixtureCDN+"?"+resolveQuery
	pick := func(urls ...string) []durlChoice { return adaptedDURL(t, bvid, cid, 1, 64, urls...) }
	before, after := pick(a)[0], pick(b)[0]
	if before != after {
		t.Fatalf("the same part, representation and CDN object lost its explicit choice: %v -> %v", before, after)
	}
	for _, changed := range []string{
		strings.Replace(b, "mirrorcosov", "mirrorother", 1), strings.Replace(b, "-064.mp4", "-065.mp4", 1),
		strings.Replace(b, "platform=pc", "platform=other", 1), strings.Replace(b, "v1.2.4", "v1.3.4", 1),
		strings.Replace(b, "orderid=0,1", "orderid=0,2", 1), b + "&unknown=x",
	} {
		if got := pick(changed)[0]; got.source == before.source || got.variant == before.variant {
			t.Fatal("a different route or media identity inherited the explicit choice")
		}
	}
	for _, other := range [][]durlChoice{
		adaptedDURL(t, "BV1234567891", cid, 1, 64, b), adaptedDURL(t, bvid, "12345678902", 1, 64, b),
		adaptedDURL(t, bvid, cid, 2, 64, b), adaptedDURL(t, bvid, cid, 1, 80, b),
	} {
		if other[0].source == before.source || other[0].variant == before.variant {
			t.Fatal("a different BVID, CID, P or quality inherited the explicit choice")
		}
	}
	for _, changed := range []struct {
		format string
		size   int
	}{{"mp4_1080p", 13131661}, {"mp4_720p", 13131333}, {"", 13131661}, {"mp4_720p", 0}} {
		got := durlWithFacts(t, bvid, cid, 1, 64, changed.format, changed.size, b)[0]
		if got.variant == before.variant || got.source == before.source {
			t.Fatal("changed or missing format/size evidence inherited the explicit choice")
		}
	}
	backup := strings.Replace(b, "mirrorcosov", "mirrorsecond", 1)
	extra := strings.Replace(b, "mirrorcosov", "mirrorthird", 1)
	withBackup := pick(b, backup)
	withTwo := pick(a, extra, backup)
	if before != withBackup[0] || withBackup[1] != withTwo[2] || withBackup[0] != withTwo[0] {
		t.Fatal("adding or removing another backup orphaned a still-present explicit source")
	}
	if withBackup[1].source == before.source || pick(backup)[0].variant == before.variant {
		t.Fatal("a vanished route or replaced primary was silently substituted")
	}
	for _, remaining := range pick(b, extra) {
		if remaining.variant != withBackup[1].variant || remaining.source == withBackup[1].source {
			t.Fatal("a removed explicit source was rebound to another route")
		}
	}
	for _, pair := range [][2]string{
		{a + "&extra=%2F", b + "&extra=%2f"}, {a + "&extra", b + "&extra="},
		{a + "&x=1&y=2", b + "&y=2&x=1"}, {a + "&extra=1&extra=2", b + "&extra=2&extra=1"},
		{strings.Replace(a, "uparams=e,", "uparams=other,", 1), strings.Replace(b, "uparams=e,", "uparams=other,", 1)},
	} {
		if pick(pair[0])[0] == pick(pair[1])[0] {
			t.Fatal("an unknown or ambiguous query was collapsed")
		}
	}
	duo, reverse := pick(a, b), pick(b, a)
	if duo[0].source == duo[1].source || duo[0].source == before.source || duo[1].source == before.source ||
		duo[0] != reverse[1] || duo[1] != reverse[0] {
		t.Fatal("coexisting ambiguous routes lost identity or inherited the unambiguous choice")
	}
}
