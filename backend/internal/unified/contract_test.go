package unified

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func contractExample(t *testing.T, prefix string) Object {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "docs", "schema", "unified-v1", "examples", prefix+"-*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("example %s: %v %v", prefix, paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	response, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func contractFirstItem(payload Object) Object       { return Map(Slice(Map(payload["data"])["items"])[0]) }
func contractVariant(item Object, index int) Object { return Map(Slice(item["variants"])[index]) }
func contractSource(variant Object) Object          { return Map(Slice(Map(variant["delivery"])["sources"])[0]) }

func TestEmbeddedSchemaMatchesCanonical(t *testing.T) {
	canonical, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "schema", "unified-v1", "unified-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, SchemaBytes()) {
		t.Fatal("embedded schema drift; run go generate ./internal/unified")
	}
	returned := SchemaBytes()
	returned[0] = 'x'
	if !bytes.Equal(canonical, SchemaBytes()) {
		t.Fatal("caller mutated embedded schema")
	}
}

func TestSchemaVocabularyAndUnsupportedCombinationsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema Object
		value  any
	}{
		{"new assertion", Object{"type": "integer", "multipleOf": 2}, 5},
		{"unknown format", Object{"type": "string", "format": "ipv4"}, "not-an-ip"},
		{"assertion beside anyOf", Object{"anyOf": Array{Object{"type": "string"}}, "minLength": 2}, "x"},
		{"assertion beside ref", Object{"$ref": "#/$defs/Source", "minLength": 2}, "x"},
		{"implicit assertion type", Object{"minLength": 2}, "x"},
		{"two branch operators", Object{"oneOf": Array{Object{"type": "string"}}, "anyOf": Array{Object{"type": "string"}}}, "x"},
		{"unknown dialect", Object{"$schema": "https://example.test/new-schema", "type": "string"}, "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSchemaDefinition(tc.schema); err == nil {
				t.Fatal("schema generation/startup would silently ignore new semantics")
			}
			if err := validateSchemaNode(tc.value, tc.schema, "$"); err == nil {
				t.Fatal("runtime silently ignored unsupported schema semantics")
			}
		})
	}
	mutated := Clone(schemaDocument)
	Map(Map(Map(mutated["$defs"])["Source"])["properties"])["future"] = Object{"type": "string", "not": Object{"const": "bad"}}
	if err := ValidateSchemaDefinition(mutated); err == nil {
		t.Fatal("nested unsupported assertion escaped schema audit")
	}
	if err := ValidateSchemaDefinition(schemaDocument); err != nil {
		t.Fatal(err)
	}
}

func TestURIBoundaryMatchesPHPWithoutRewritingSourceBytes(t *testing.T) {
	for _, tc := range []struct {
		url   string
		valid bool
	}{
		{"https://bad_host.test/a.mp4", false},
		{"https://example.test/中文.mp4", false},
		{"https://example.test/a%ZZ.mp4", true},
		{"https://example.test/a%2Fb.mp4?sign=A%2Bb+c&line=2&line=1", true},
	} {
		t.Run(tc.url, func(t *testing.T) {
			payload := contractExample(t, "02")
			item := contractFirstItem(payload)
			contractSource(contractVariant(item, 0))["url"] = tc.url
			Map(item["default"])["url"] = tc.url
			if err := ValidateEnvelope(payload); (err == nil) != tc.valid {
				t.Fatalf("PHP URI acceptance differs: %v", err)
			}
			if contractSource(contractVariant(item, 0))["url"] != tc.url || Map(item["default"])["url"] != tc.url {
				t.Fatal("validation rewrote source bytes")
			}
		})
	}
}

func TestCanonicalDesignExamples(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "docs", "schema", "unified-v1", "examples", "*.json"))
	if err != nil || len(paths) != 29 {
		t.Fatalf("expected all 29 canonical examples: count=%d error=%v", len(paths), err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateEnvelopeAt(payload, 1793448000); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestContractRejectsBrokenStructureAndReferences(t *testing.T) {
	cases := []struct {
		name string
		edit func(Object)
	}{
		{"unknown envelope key", func(p Object) { p["url"] = "https://example.test/old" }},
		{"missing default", func(p Object) { delete(contractFirstItem(p), "default") }},
		{"null variants", func(p Object) { contractFirstItem(p)["variants"] = nil }},
		{"empty variants", func(p Object) { contractFirstItem(p)["variants"] = Array{} }},
		{"default points elsewhere", func(p Object) { Map(contractFirstItem(p)["default"])["variant_id"] = "missing" }},
		{"default URL mismatch", func(p Object) { Map(contractFirstItem(p)["default"])["url"] = "https://example.test/different" }},
		{"duplicate variant", func(p Object) {
			i := contractFirstItem(p)
			i["variants"] = append(Slice(i["variants"]), Clone(contractVariant(i, 0)))
		}},
		{"expired projection without generation", func(p Object) {
			contractSource(contractVariant(contractFirstItem(p), 0))["expires_at"] = "2100-01-01T00:00:00Z"
		}},
		{"invalid date", func(p Object) { Map(p["data"])["generated_at"] = "2026-02-30T00:00:00Z" }},
		{"invalid timezone hour", func(p Object) { Map(p["data"])["generated_at"] = "2026-10-30T00:00:00+24:00" }},
		{"invalid timezone minute", func(p Object) { Map(p["data"])["generated_at"] = "2026-10-30T00:00:00+02:60" }},
		{"blank language", func(p Object) {
			contractVariant(contractFirstItem(p), 0)["metadata"] = Object{"audio_language": "  ", "evidence": "upstream"}
		}},
		{"technical metadata without evidence", func(p Object) { contractVariant(contractFirstItem(p), 0)["metadata"] = Object{"width": 1920} }},
		{"false audio and channels", func(p Object) {
			contractVariant(contractFirstItem(p), 0)["metadata"] = Object{"has_audio": false, "audio_channels": 2, "evidence": "upstream"}
		}},
		{"language role without language", func(p Object) {
			contractVariant(contractFirstItem(p), 0)["metadata"] = Object{"audio_language_role": "alternate", "evidence": "upstream"}
		}},
		{"mixed evidence missing field", func(p Object) {
			contractVariant(contractFirstItem(p), 0)["metadata"] = Object{"width": 1920, "evidence": "mixed", "field_evidence": Object{"height": "probe"}}
		}},
		{"deferred with delivery", func(p Object) {
			v := contractVariant(contractFirstItem(p), 0)
			v["state"] = "deferred"
			v["reason"] = Object{"code": "LATER", "message": "Later"}
		}},
		{"eligible file with null default", func(p Object) {
			i := contractFirstItem(p)
			i["default"] = nil
			i["default_unavailable_reason"] = "unavailable"
		}},
		{"unknown warning item", func(p Object) { p["warnings"] = Array{Object{"code": "TEST", "message": "Test", "item_id": "missing"}} }},
		{"missing group member", func(p Object) {
			Map(p["data"])["groups"] = Array{Object{"id": "g", "kind": "album", "item_ids": Array{"missing"}}}
		}},
		{"error code mismatch", func(p Object) {
			delete(p, "data")
			p["code"] = "NO_MEDIA"
			p["error"] = Object{"code": "INVALID_INPUT", "message": "Invalid"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := contractExample(t, "02")
			tc.edit(payload)
			if err := ValidateEnvelope(payload); err == nil {
				t.Fatal("accepted malformed contract")
			}
		})
	}
}

func TestContractKeepsZeroFalseAndExactIntegers(t *testing.T) {
	payload := contractExample(t, "02")
	data := Map(payload["data"])
	data["content_id"] = "18446744073709551615"
	data["statistics"] = Object{"likes": Object{"value": json.Number("0")}}
	variant := contractVariant(contractFirstItem(payload), 0)
	variant["metadata"] = Object{"has_audio": false, "evidence": "upstream"}
	contractSource(variant)["size_bytes"] = json.Number("9007199254740993")
	if err := ValidateEnvelope(payload); err != nil {
		t.Fatal(err)
	}
	wire, err := Encode(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"has_audio":false`, `"value":0`, `"size_bytes":9007199254740993`, `"content_id":"18446744073709551615"`} {
		if !strings.Contains(string(wire), fragment) {
			t.Fatalf("wire lost %s", fragment)
		}
	}
}

func TestContractMetadataConflictsAndRoles(t *testing.T) {
	base := contractExample(t, "16")
	item := contractFirstItem(base)
	mux := contractVariant(item, 1)
	video := Map(Slice(Map(mux["delivery"])["resources"])[0])
	metadata := Map(video["metadata"])
	metadata["field_conflicts"] = Object{"fps": Array{Object{"value": 30, "evidence": "upstream"}, Object{"value": 60, "evidence": "probe"}}}
	if err := ValidateEnvelope(base); err != nil {
		t.Fatal(err)
	}
	metadata["fps"] = 60
	if err := ValidateEnvelope(base); err == nil {
		t.Fatal("scalar conflict winner accepted")
	}
	delete(metadata, "fps")
	Map(Slice(Map(metadata["field_conflicts"])["fps"])[1])["value"] = 30.0
	if err := ValidateEnvelope(base); err == nil {
		t.Fatal("30 and 30.0 manufactured a conflict")
	}
	delete(metadata, "field_conflicts")
	metadata["audio_channels"] = 2
	if err := ValidateEnvelope(base); err == nil {
		t.Fatal("video resource accepted audio metadata")
	}
}

func TestContractSourceSafety(t *testing.T) {
	for _, raw := range []string{"/api/../secret", "/api/%2e%2e/secret", "/api/%252e%252e/secret", "/api/a%5cb", "/api/a%00b", "//attacker.test/api/x"} {
		t.Run(raw, func(t *testing.T) {
			if err := ValidateSource(Object{"url": raw, "access": "relay"}, nil); err == nil {
				t.Fatal("accepted unsafe relay")
			}
		})
	}
	for _, raw := range []string{"https://u:p@example.test/file", "file:///etc/passwd", "https://example.test/a b", "https://example.test/a\\b"} {
		if err := ValidateSource(Object{"url": raw}, nil); err == nil {
			t.Fatalf("accepted unsafe direct URL %q", raw)
		}
	}
	raw := "https://example.test/file?token=a%2Bb+z&line=1&line=2"
	if err := ValidateSource(Object{"url": raw}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSelectionPreservesInputOrderAndRequiresEveryResource(t *testing.T) {
	payload := contractExample(t, "16")
	item := contractFirstItem(payload)
	before := Clone(item)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC).Unix()
	selected := SelectDefault(item, now)
	if !Equal(item, before) {
		t.Fatal("selector mutated an existing parse/download snapshot")
	}
	if Str(Map(selected["default"])["variant_id"]) != "1080-file" || len(Slice(selected["higher_quality_variants"])) != 1 {
		t.Fatal("default or higher mux incorrect")
	}
	audio := Map(Slice(Map(contractVariant(item, 1)["delivery"])["resources"])[1])
	Map(Slice(Map(audio["delivery"])["sources"])[0])["expires_at"] = "2000-01-01T00:00:00Z"
	selected = SelectDefault(item, now)
	if _, exists := selected["higher_quality_variants"]; exists {
		t.Fatal("unusable audio dependency retained as a deliverable higher variant")
	}
	item["variants"] = Array{contractVariant(item, 1)}
	selected = SelectDefault(item, now)
	if selected["default"] != nil || Str(selected["default_unavailable_reason"]) != "needs_refresh" {
		t.Fatal("expired mux did not request refresh")
	}
}

func TestSelectionNumericIDTieMatchesPHP(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		cmp  int
	}{
		{"2", "10", -1}, {"0002", "2", 0}, {"100000000000000000001", "100000000000000000002", -1},
		{"-100000000000000000001", "-100000000000000000002", -1}, {"1e3", "999", 1},
		{"1e400", "9e399", -1}, {"9007199254740993.0", "9007199254740992.0", 0}, {" 2 ", "10", -1}, {"x2", "x10", 1},
	} {
		if got := comparePHPID(tc.a, tc.b); got != tc.cmp {
			t.Errorf("PHP string tie %q/%q = %d, want %d", tc.a, tc.b, got, tc.cmp)
		}
	}
	item := contractFirstItem(contractExample(t, "02"))
	a := contractVariant(item, 0)
	b := Clone(a)
	a["id"], b["id"] = "10", "2"
	item["variants"] = Array{a, b}
	if Str(Map(SelectDefault(item, 1793448000)["default"])["variant_id"]) != "2" {
		t.Fatal("numeric ID tie selected lexically instead of PHP's numeric order")
	}
}

func TestPlatformScopedDefaultEligibilityAndUnknownVideoFields(t *testing.T) {
	const now int64 = 1793448000

	makeVariant := func(id, label string, meta Object, url string) Object {
		return Object{
			"id":       id,
			"label":    label,
			"metadata": meta,
			"delivery": Object{
				"kind": "file",
				"sources": Array{
					Object{"id": "src-" + id, "url": url},
				},
			},
		}
	}

	// 1. Legacy platform (douyin): high-res silent file + lower-res audible file still selects high-res silent file (main behavior).
	legacyPayload := contractExample(t, "02")
	legacyData := Map(legacyPayload["data"])
	legacyData["platform"] = "douyin"
	legacyItem := contractFirstItem(legacyPayload)
	delete(legacyItem, "higher_quality_variants")
	v1080SilentMinimal := makeVariant("v-1080-silent", "1080p", Object{
		"width": 1920, "height": 1080, "has_audio": false, "evidence": "upstream",
	}, "https://example.test/1080-silent.mp4")
	v720Audible := makeVariant("v-720-audible", "720p", Object{
		"width": 1280, "height": 720, "has_audio": true, "video_codec": "avc1.64001f", "audio_codec": "mp4a.40.2",
		"container": "mp4", "extension": "mp4", "mime_type": "video/mp4", "evidence": "upstream",
	}, "https://example.test/720-audible.mp4")
	legacyItem["variants"] = Array{v1080SilentMinimal, v720Audible}
	finalLegacy := FinalizeData(legacyData, now)
	if got := Str(Map(contractFirstItem(finalLegacy)["default"])["variant_id"]); got != "v-1080-silent" {
		t.Fatalf("legacy platform should keep main spatial selection (v-1080-silent), got %q", got)
	}
	if err := ValidateEnvelopeAt(finalLegacy, now); err != nil {
		t.Fatalf("legacy envelope failed validation: %v", err)
	}
	wrongLegacy := Clone(finalLegacy)
	Map(contractFirstItem(wrongLegacy)["default"])["variant_id"] = "v-720-audible"
	Map(contractFirstItem(wrongLegacy)["default"])["source_id"] = "src-v-720-audible"
	Map(contractFirstItem(wrongLegacy)["default"])["url"] = "https://example.test/720-audible.mp4"
	if err := ValidateEnvelopeAt(wrongLegacy, now); err == nil {
		t.Fatal("expected legacy platform to reject lower-res default when higher-res silent file exists")
	}

	// 2. New yt-dlp platform (youtube): 4K silent (even without label/codec/container/mime_type) + 360p audible + M4A audio-only
	// selects 360p audible across FinalizeData and PublicView (normal & debug).
	ytData := Clone(legacyData)
	ytData["platform"] = "youtube"
	ytData["source_url"] = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	ytItem := contractFirstItem(Object{"data": ytData})
	v4kSilentNoLabel := makeVariant("v-4k-silent", "4K", Object{
		"width": 3840, "height": 2160, "has_audio": false, "evidence": "upstream",
	}, "https://rr1---sn-a.googlevideo.com/videoplayback?itag=313")
	v360Audible := makeVariant("v-360-audible", "360p", Object{
		"width": 640, "height": 360, "has_audio": true, "video_codec": "avc1.42001E", "audio_codec": "mp4a.40.2",
		"container": "mp4", "extension": "mp4", "mime_type": "video/mp4", "evidence": "upstream",
	}, "https://rr1---sn-a.googlevideo.com/videoplayback?itag=18")
	vM4AAudio := makeVariant("v-m4a", "M4A", Object{
		"has_audio": true, "audio_codec": "mp4a.40.2", "container": "m4a", "extension": "m4a", "mime_type": "audio/mp4", "evidence": "upstream",
	}, "https://rr1---sn-a.googlevideo.com/videoplayback?itag=140")
	ytItem["variants"] = Array{v4kSilentNoLabel, v360Audible, vM4AAudio}
	finalYT := FinalizeData(ytData, now)
	for _, debug := range []bool{false, true} {
		view := PublicView(finalYT, debug, now)
		if got := Str(Map(contractFirstItem(view)["default"])["variant_id"]); got != "v-360-audible" {
			t.Fatalf("youtube (debug=%v) expected v-360-audible default, got %q", debug, got)
		}
		if err := ValidateEnvelopeAt(view, now); err != nil {
			t.Fatalf("youtube (debug=%v) failed validation: %v", debug, err)
		}
	}

	// 3. Candidate with only audio_codec, missing video_codec/dimensions, non-audio/* MIME is never misclassified as audio-only
	// on either legacy or new platforms.
	vAudioCodecOnlyVideo := makeVariant("v-acodec-video", "标清", Object{
		"has_audio": true, "audio_codec": "mp4a.40.2", "container": "mp4", "extension": "mp4", "mime_type": "video/mp4", "evidence": "upstream",
	}, "https://rr1---sn-a.googlevideo.com/videoplayback?itag=18")
	if isAudioOnlyFile(vAudioCodecOnlyVideo) {
		t.Fatal("candidate with video/mp4 MIME and missing video_codec/dimensions was misclassified as audio-only")
	}
	for _, platform := range []string{"douyin", "youtube"} {
		d := Clone(legacyData)
		d["platform"] = platform
		it := contractFirstItem(Object{"data": d})
		it["variants"] = Array{vAudioCodecOnlyVideo, vM4AAudio}
		out := FinalizeData(d, now)
		if got := Str(Map(contractFirstItem(out)["default"])["variant_id"]); got != "v-acodec-video" {
			t.Fatalf("platform %s failed to select v-acodec-video, got %q", platform, got)
		}
		if err := ValidateEnvelopeAt(out, now); err != nil {
			t.Fatalf("platform %s envelope failed validation: %v", platform, err)
		}
	}
}
