package pipixia

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const representationNow int64 = 1793556000
const representationItemID = "7385813877985909043"
const representationQuery = "a=1319&mime_type=video_mp4&rc=encoded%2Bvalue&l=opaque&x=1&x=2"

func representationURL(host, signature, object, query string) string {
	return "https://" + host + "/" + signature + "/6ae78ced/video/tos/cn/tos-cn-ve-0076/" + object + "/?" + query
}

func representationSources() []string {
	return []string{
		representationURL("v3-cdn-tos.ppxvod.com", strings.Repeat("a", 32), "FileObjectA", representationQuery),
		representationURL("v26-cdn-tos.ppxvod.com", strings.Repeat("b", 32), "FileObjectA", representationQuery),
	}
}

type representationExecutor struct{ body []byte }

func (e representationExecutor) Do(ctx context.Context, r fetch.Request) (fetch.Response, error) {
	if r.URL != "https://h5.pipix.com/ppx/item/"+representationItemID {
		return fetch.Response{}, fmt.Errorf("unexpected metadata request")
	}
	return fetch.Response{StatusCode: 200, URL: r.URL, Body: e.body}, nil
}

// Run the real page decoder, parser, wire adapter, default selection and public
// projection. Only upstream HTTP is substituted; no frozen PHP oracle changes.
func representationPayload(t *testing.T, groups ...[]string) unified.Object {
	t.Helper()
	video := unified.Object{}
	for i, urls := range groups {
		name := []string{"video_download", "video_high", "video_mid"}[i]
		list := unified.Array{}
		for _, url := range urls {
			list = append(list, unified.Object{"url": url})
		}
		video[name] = unified.Object{"uri": "same-work-video-id", "width": 960, "height": 720, "codec_type": 1, "url_list": list}
	}
	item := unified.Object{"item_id_str": representationItemID, "content": "representation identity fixture", "video": video}
	raw, err := json.Marshal(unified.Object{"ppxItemDetail": unified.Object{"item": item}})
	if err != nil {
		t.Fatal(err)
	}
	p := New(representationExecutor{[]byte("<script>" + string(raw) + "</script>")})
	payload, err := p.Parse(context.Background(), "https://h5.pipix.com/ppx/item/"+representationItemID, representationNow)
	if err != nil || payload["code"] != "OK" {
		t.Fatalf("parse failed: %v %v", payload, err)
	}
	for _, debug := range []bool{false, true} {
		view := unified.PublicView(payload, debug, representationNow)
		if err := unified.ValidateEnvelopeAt(view, representationNow); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(view)
		if strings.Contains(string(encoded), "private_media_identity") || strings.Contains(string(encoded), "private_representation_id") {
			t.Fatal("private grouping identity leaked")
		}
	}
	return unified.PublicView(payload, false, representationNow)
}

func representationItem(payload unified.Object) unified.Object {
	return unified.Map(unified.Slice(unified.Map(payload["data"])["items"])[0])
}
func representationVariant(t *testing.T, payload unified.Object) unified.Object {
	t.Helper()
	variants := unified.Slice(representationItem(payload)["variants"])
	if len(variants) != 1 {
		t.Fatalf("want one representation, got %d", len(variants))
	}
	return unified.Map(variants[0])
}
func representationSourceURLs(variant unified.Object) []string {
	out := []string{}
	for _, value := range unified.Slice(unified.Map(variant["delivery"])["sources"]) {
		out = append(out, unified.Str(unified.Map(value)["url"]))
	}
	return out
}

func TestPipixiaRepresentationBackupMembership(t *testing.T) {
	original := representationSources()
	base := representationVariant(t, representationPayload(t, original))
	third := representationURL("v6-cdn-tos.ppxvod.com", strings.Repeat("c", 32), "FileObjectA", representationQuery)
	for name, urls := range map[string][]string{
		"add-backup":               append(append([]string{}, original...), third),
		"remove-unselected-backup": original[:1],
		"reorder-backups":          {original[1], original[0]},
	} {
		t.Run(name, func(t *testing.T) {
			payload := representationPayload(t, urls)
			variant := representationVariant(t, payload)
			if base["id"] != variant["id"] {
				t.Fatalf("same object changed parent ID when %s", name)
			}
			if !reflect.DeepEqual(representationSourceURLs(variant), urls) {
				t.Fatal("signed URL bytes/order changed")
			}
			if unified.Map(representationItem(payload)["default"])["variant_id"] != variant["id"] {
				t.Fatal("server default lost")
			}
		})
	}
}

func TestPipixiaRepresentationSignedPathNotSourceIdentity(t *testing.T) {
	base := representationVariant(t, representationPayload(t, representationSources()))
	urls := []string{representationURL("v26-cdn-tos.ppxvod.com", strings.Repeat("d", 32), "FileObjectA", representationQuery)}
	fresh := representationVariant(t, representationPayload(t, urls))
	if base["id"] != fresh["id"] {
		t.Fatal("signature-only path changed parent representation")
	}
	oldSource := unified.Map(unified.Slice(unified.Map(base["delivery"])["sources"])[0])["id"]
	newSource := unified.Map(unified.Slice(unified.Map(fresh["delivery"])["sources"])[0])["id"]
	if oldSource == newSource {
		t.Fatal("missing explicit source was silently invented")
	}
}

func TestPipixiaRepresentationSameDimensionsDifferentFiles(t *testing.T) {
	urls := representationSources()
	other := []string{strings.Replace(urls[0], "FileObjectA", "FileObjectB", 1)}
	payload := representationPayload(t, urls, other)
	variants := unified.Slice(representationItem(payload)["variants"])
	if len(variants) != 2 || unified.Map(variants[0])["id"] == unified.Map(variants[1])["id"] {
		t.Fatal("same-size different objects merged")
	}
}

func TestPipixiaRepresentationKeepsEveryQueryByte(t *testing.T) {
	base := representationVariant(t, representationPayload(t, representationSources()))
	for _, query := range []string{representationQuery + "&watermark=1", strings.Replace(representationQuery, "%2B", "%2b", 1), strings.Replace(representationQuery, "x=1&x=2", "x=2&x=1", 1), strings.Replace(representationQuery, "l=opaque", "l=renewed", 1)} {
		urls := []string{representationURL("v3-cdn-tos.ppxvod.com", strings.Repeat("a", 32), "FileObjectA", query)}
		other := representationVariant(t, representationPayload(t, urls))
		if base["id"] == other["id"] {
			t.Fatalf("unknown query change was discarded: %s", query)
		}
	}
}

func TestPipixiaRepresentationUnknownOrMixedSourcesKeepFallback(t *testing.T) {
	urls := representationSources()
	for name, sources := range map[string][]string{
		"unknown-host":  {strings.Replace(urls[0], "v3-cdn-tos.ppxvod.com", "cdn.example", 1)},
		"unknown-path":  {"https://v3-cdn-tos.ppxvod.com/unrecognized.mp4?x=1"},
		"mixed-objects": {urls[0], strings.Replace(urls[1], "FileObjectA", "FileObjectB", 1)},
		"mixed-queries": {urls[0], urls[1] + "&watermark=1"},
	} {
		t.Run(name, func(t *testing.T) {
			got := representationVariant(t, representationPayload(t, sources))
			candidates := unified.Array{}
			for _, url := range sources {
				candidates = append(candidates, unified.Object{"url": url, "private_representation_id": "original-source-set-group"})
			}
			want := unified.Map(unified.FileVariants(candidates, representationNow)[0])
			if got["id"] != want["id"] {
				t.Fatal("unsupported identity guessed instead of preserving fallback")
			}
		})
	}
}

// Optional export for the existing frontend consumers and local real-data
// checks. It contains only synthetic URLs and actual production Go envelopes.
func TestPipixiaRepresentationExport(t *testing.T) {
	urls := representationSources()
	third := representationURL("v6-cdn-tos.ppxvod.com", strings.Repeat("c", 32), "FileObjectA", representationQuery)
	freshTransport := transportSources(ppxNewTransportQuery, "c", "d")
	freshTransport[0], freshTransport[1] = freshTransport[1], freshTransport[0]
	cases := map[string]unified.Object{
		"before":                   representationPayload(t, urls),
		"added":                    representationPayload(t, append(append([]string{}, urls...), third)),
		"removed":                  representationPayload(t, urls[:1]),
		"source-missing":           representationPayload(t, urls[1:]),
		"different-file":           representationPayload(t, []string{strings.Replace(urls[0], "FileObjectA", "FileObjectB", 1)}),
		"transport-before":         representationPayload(t, transportSources(ppxOldTransportQuery, "a", "b")),
		"transport-renewed":        representationPayload(t, freshTransport),
		"transport-source-missing": representationPayload(t, freshTransport[:1]),
		"transport-other-content":  representationPayload(t, []string{strings.Replace(freshTransport[1], "rc=opaque%2Bencode", "rc=other%2Bencode", 1)}),
	}
	if path := os.Getenv("DOKITOOL_PIPIXIA_REPRESENTATION_OUTPUT"); path != "" {
		body, err := json.MarshalIndent(cases, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

const ppxOldTransportQuery = "a=1319&br=704&mime_type=video_mp4&rc=opaque%2Bencode&btag=80000e00008000&ft=old_cdn_feature&dy_q=1793556183&l=2026110202030372865787CF2ED5DA6860"
const ppxNewTransportQuery = "a=1319&br=704&mime_type=video_mp4&rc=opaque%2Bencode&btag=c0000e00008000&ft=new_cdn_feature&dy_q=1793556184&l=2026110202040389265787CF2ED5DA6861"

func transportSources(query, a, b string) []string {
	return []string{
		representationURL("v3-cdn-tos.ppxvod.com", strings.Repeat(a, 32), "FileObjectA", query),
		representationURL("v26-cdn-tos.ppxvod.com", strings.Repeat(b, 32), "FileObjectA", query),
	}
}

func sourceIDsByHost(v unified.Object) map[string]string {
	ids := map[string]string{}
	for _, raw := range unified.Slice(unified.Map(v["delivery"])["sources"]) {
		source := unified.Map(raw)
		url := unified.Str(source["url"])
		for _, host := range []string{"v3-cdn-tos.ppxvod.com", "v26-cdn-tos.ppxvod.com"} {
			if strings.Contains(url, "https://"+host+"/") {
				ids[host] = unified.Str(source["id"])
			}
		}
	}
	return ids
}

func TestPipixiaTransportRenewalRetainsExplicitHostSource(t *testing.T) {
	beforeURLs := transportSources(ppxOldTransportQuery, "a", "b")
	newURLs := transportSources(ppxNewTransportQuery, "c", "d")
	newURLs[0], newURLs[1] = newURLs[1], newURLs[0] // real page changed source order
	before := representationVariant(t, representationPayload(t, beforeURLs))
	after := representationVariant(t, representationPayload(t, newURLs))
	if before["id"] != after["id"] {
		t.Fatal("same TOS object/encoding changed parent across observed transport renewal")
	}
	oldIDs, newIDs := sourceIDsByHost(before), sourceIDsByHost(after)
	if len(oldIDs) != 2 || len(newIDs) != 2 ||
		oldIDs["v3-cdn-tos.ppxvod.com"] != newIDs["v3-cdn-tos.ppxvod.com"] ||
		oldIDs["v26-cdn-tos.ppxvod.com"] != newIDs["v26-cdn-tos.ppxvod.com"] {
		t.Fatalf("explicit CDN mirror source identity was not renewed: before=%v after=%v", oldIDs, newIDs)
	}
	if oldIDs["v3-cdn-tos.ppxvod.com"] == oldIDs["v26-cdn-tos.ppxvod.com"] {
		t.Fatal("distinct CDN source choices merged")
	}
	if !reflect.DeepEqual(representationSourceURLs(after), newURLs) {
		t.Fatal("refreshed signed URL bytes or order were rewritten")
	}
	if unified.Map(representationItem(representationPayload(t, newURLs))["default"])["variant_id"] != after["id"] {
		t.Fatal("server default lost")
	}
}

func TestPipixiaTransportRenewalDoesNotGuessMissingSourceOrFile(t *testing.T) {
	before := representationVariant(t, representationPayload(t, transportSources(ppxOldTransportQuery, "a", "b")))
	oldIDs := sourceIDsByHost(before)
	fresh := transportSources(ppxNewTransportQuery, "c", "d")
	for name, urls := range map[string][]string{
		"missing-host":              {fresh[1]},
		"different-file":            {strings.Replace(fresh[0], "FileObjectA", "FileObjectB", 1)},
		"changed-content-parameter": {strings.Replace(fresh[0], "rc=opaque%2Bencode", "rc=other%2Bencode", 1)},
		"unknown-new-parameter":     {fresh[0] + "&watermark=1"},
	} {
		t.Run(name, func(t *testing.T) {
			after := representationVariant(t, representationPayload(t, urls))
			if name == "missing-host" {
				if before["id"] != after["id"] {
					t.Fatal("same file with remaining mirror changed parent")
				}
				if sourceIDsByHost(after)["v3-cdn-tos.ppxvod.com"] == oldIDs["v3-cdn-tos.ppxvod.com"] {
					t.Fatal("missing explicit source was invented")
				}
			} else if before["id"] == after["id"] {
				t.Fatal("distinct file/content parameter was incorrectly matched")
			}
		})
	}
}

func TestPipixiaTransportPartialKeysStayDistinct(t *testing.T) {
	oldURL := transportSources(ppxOldTransportQuery, "a", "b")[0]
	for _, changed := range []string{
		strings.Replace(ppxNewTransportQuery, "&ft=new_cdn_feature", "", 1),
		strings.Replace(ppxNewTransportQuery, "&dy_q=1793556184", "", 1),
		strings.Replace(ppxNewTransportQuery, "&l=2026110202040389265787CF2ED5DA6861", "", 1),
		ppxNewTransportQuery + "&ft=duplicate",
		strings.Replace(ppxNewTransportQuery, "&btag=c0000e00008000", "", 1),
		strings.Replace(ppxNewTransportQuery, "btag=c0000e00008000", "btag=invalid-value", 1),
		ppxNewTransportQuery + "&btag=duplicate",
	} {
		next := representationURL("v3-cdn-tos.ppxvod.com", strings.Repeat("c", 32), "FileObjectA", changed)
		if representationVariant(t, representationPayload(t, []string{oldURL}))["id"] ==
			representationVariant(t, representationPayload(t, []string{next}))["id"] {
			t.Fatalf("partial/duplicate transport keys were loosely normalized")
		}
	}
}
