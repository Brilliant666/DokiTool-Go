package unified

import (
	"testing"
	"time"
)

func TestPolicyOneFileMustCoverAllProtectedCapabilities(t *testing.T) {
	payload := contractExample(t, "28")
	item := contractFirstItem(payload)
	first := contractVariant(item, 0)
	second := Clone(first)
	second["id"] = "other-file"
	first["metadata"] = Object{"width": 3840, "height": 2160, "fps": 60, "hdr_type": "sdr", "has_audio": true, "audio_channels": 2, "audio_language": "en", "evidence": "upstream"}
	second["metadata"] = Object{"width": 3840, "height": 2160, "fps": 30, "hdr_type": "hdr10", "has_audio": true, "audio_channels": 6, "audio_language": "ja", "evidence": "upstream"}
	item["variants"] = Array{first, second, contractVariant(item, 1)}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC).Unix()
	if len(Slice(FilterItemVariants(item, now)["variants"])) != 3 {
		t.Fatal("combined separate files' advantages to remove the protected mux")
	}
	Map(first["metadata"])["hdr_type"] = "hdr10"
	Map(first["metadata"])["audio_channels"] = 6
	Map(first["metadata"])["audio_language"] = "ja"
	if len(Slice(FilterItemVariants(item, now)["variants"])) != 2 {
		t.Fatal("one fully covering file failed to hide the redundant mux")
	}
}

func TestPolicyExpiredMainLanguageAndParentFactsRemainEffective(t *testing.T) {
	base := contractExample(t, "16")
	item := contractFirstItem(base)
	file := contractVariant(item, 0)
	Map(file["metadata"])["width"], Map(file["metadata"])["height"] = 3840, 2160
	ja := contractVariant(item, 1)
	jaAudio := Map(Slice(Map(ja["delivery"])["resources"])[1])
	Map(jaAudio["metadata"])["audio_language"] = "ja"
	main := Clone(ja)
	main["id"] = "main-language-evidence"
	main["metadata"] = Object{"audio_language": "en", "audio_language_role": "main", "evidence": "upstream"}
	mainResources := Slice(Map(main["delivery"])["resources"])
	Map(Map(mainResources[0])["metadata"])["width"] = 1920
	Map(Map(mainResources[0])["metadata"])["height"] = 1080
	mainAudio := Map(mainResources[1])
	delete(Map(mainAudio["metadata"]), "audio_language")
	Map(Slice(Map(mainAudio["delivery"])["sources"])[0])["expires_at"] = "2000-01-01T00:00:00Z"
	item["variants"] = Array{file, main, ja}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC).Unix()
	filtered := FilterItemVariants(item, now)
	found := false
	for _, value := range Slice(filtered["variants"]) {
		found = found || Str(Map(value)["id"]) == "4k-mux"
	}
	if !found {
		t.Fatal("expired parent-level main language was lost, removing the Japanese mux")
	}
	delete(main, "metadata")
	Map(mainAudio["metadata"])["audio_language"] = "en"
	Map(mainAudio["metadata"])["audio_language_role"] = "main"
	if !Equal(filtered, FilterItemVariants(item, now)) {
		// Metadata placement differs; inventory must still match.
		ids := func(i Object) Array {
			out := Array{}
			for _, value := range Slice(i["variants"]) {
				out = append(out, Map(value)["id"])
			}
			return out
		}
		if !Equal(ids(filtered), ids(FilterItemVariants(item, now))) {
			t.Fatal("same associated language facts changed policy when moved between layers")
		}
	}
}

func TestPublicViewPreservesSignedURLAndRefreshMode(t *testing.T) {
	payload := contractExample(t, "28")
	data := Map(payload["data"])
	data["refresh_url"] = "/api/parse?url=https%3A%2F%2Fsource.test%2Fa%3Fx%3D1&debug=0"
	item := contractFirstItem(payload)
	item["resolve_url"] = "/api/resolve?item_id=file-and-mux&debug=0"
	signed := "https://media.example.test/video?line=2&line=1&sign=A%2Bb+c"
	contractSource(contractVariant(item, 0))["url"] = signed
	Map(item["default"])["url"] = signed
	before := Clone(payload)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC).Unix()
	normal, debug := PublicView(payload, false, now), PublicView(payload, true, now)
	if !Equal(payload, before) {
		t.Fatal("view generation mutated the full inventory")
	}
	for _, view := range []Object{normal, debug} {
		if contractSource(contractVariant(contractFirstItem(view), 0))["url"] != signed || Map(contractFirstItem(view)["default"])["url"] != signed {
			t.Fatal("view rewrote signed media URL bytes")
		}
		if err := ValidateEnvelope(view); err != nil {
			t.Fatal(err)
		}
	}
	if Str(Map(debug["data"])["refresh_url"]) != "/api/parse?url=https%3A%2F%2Fsource.test%2Fa%3Fx%3D1&debug=1" || Str(contractFirstItem(normal)["resolve_url"]) != "/api/resolve?item_id=file-and-mux" {
		t.Fatal("API navigation lost its requested view")
	}
}
