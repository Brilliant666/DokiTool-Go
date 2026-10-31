package unified

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const publicProjectionNow int64 = 1793448000

func TestPublicSourceProjectionPreservesURLBytes(t *testing.T) {
	for _, tc := range []struct {
		name, access, raw, want string
	}{
		{"plain", "", "https://media.example.test/file.mp4?b=2&a=1", "https://media.example.test/file.mp4?b=2&a=1"},
		{"fragment", "", "https://media.example.test/file.mp4#chapter", "https://media.example.test/file.mp4"},
		{"explicit direct", "direct", "https://media.example.test/file.mp4#one#two", "https://media.example.test/file.mp4"},
		{"empty fragment", "", "https://media.example.test/file.mp4#", "https://media.example.test/file.mp4"},
		{"escaped hashes", "", "https://media.example.test/a%23b.mp4?key=c%23d&nested=%2523", "https://media.example.test/a%23b.mp4?key=c%23d&nested=%2523"},
		{"signed query", "direct", "https://MEDIA.example.test:443/a%2Fb.mp4?line=2&line=1&sign=A%2Bb+c&key=%23%2523#chapter", "https://MEDIA.example.test:443/a%2Fb.mp4?line=2&line=1&sign=A%2Bb+c&key=%23%2523"},
		{"relay", "relay", "/api/media/weibo?ticket=A%2Bb+c&key=%23#keep", "/api/media/weibo?ticket=A%2Bb+c&key=%23#keep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := Object{"id": "stable-source", "url": tc.raw, "size_bytes": int64(42), "availability": "available", "browser_fetch": "allowed"}
			if tc.access != "" {
				source["access"] = tc.access
			}
			want := Clone(source)
			want["url"] = tc.want
			projectPublicSource(source)
			if !reflect.DeepEqual(source, want) {
				t.Fatalf("unexpected source projection: got %#v want %#v", source, want)
			}
		})
	}
}

func TestPublicSourceProjectionUsesExactIntegerBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		omit  bool
	}{
		{"one", int64(1), false},
		{"below maximum", json.Number("9007199254740990"), false},
		{"maximum", json.Number("9007199254740991"), false},
		{"maximum int64", int64(9007199254740991), false},
		{"maximum float64", float64(9007199254740991), false},
		{"maximum exponent", json.Number("9.007199254740991e15"), false},
		{"first unsafe integer", json.Number("9007199254740992"), true},
		{"unsafe integer lost by float64", json.Number("9007199254740993"), true},
		{"large int64", int64(9223372036854775800), true},
		{"large json integer", json.Number("9223372036854775800"), true},
		{"large uint64", uint64(18446744073709551615), true},
		{"unsafe float64", float64(9007199254740992), true},
		{"unsafe exponent", json.Number("9.007199254740992e15"), true},
		{"unsafe integral decimal", json.Number("9007199254740993.0"), true},
		{"noninteger above maximum", json.Number("9007199254740992.5"), false},
		{"invalid numeric string", "9007199254740993", false},
		{"invalid boolean", true, false},
		{"invalid null", nil, false},
		{"invalid zero", int64(0), false},
		{"invalid negative", int64(-9007199254740993), false},
		{"invalid fraction", json.Number("1.5"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, access := range []string{"direct", "relay"} {
				source := Object{"id": "stable-source", "url": "https://media.example.test/file.mp4", "access": access, "size_bytes": tc.value}
				if access == "relay" {
					source["url"] = "/api/media/weibo?ticket=unchanged"
				}
				want := Clone(source)
				if tc.omit {
					delete(want, "size_bytes")
				}
				projectPublicSource(source)
				if !reflect.DeepEqual(source, want) {
					t.Fatalf("%s changed size or another source field: got %#v want %#v", access, source, want)
				}
			}
		})
	}
}

// These explicit fixture paths are independent of the production delivery
// walker, so forgetting a Source position cannot silently remove its test.
func TestPublicViewProjectsEverySourceSlot(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		paths  []string
	}{
		{"03", []string{
			"/data/items/0/variants/0/delivery/sources/0",
		}},
		{"10", []string{
			"/data/items/0/variants/0/delivery/sources/0",
			"/data/items/1/variants/0/delivery/resources/0/delivery/sources/0",
			"/data/items/1/variants/0/delivery/resources/1/delivery/sources/0",
			"/data/attachments/0/cover",
			"/data/attachments/0/resource/delivery/sources/0",
		}},
		{"13", []string{
			"/data/items/0/variants/0/delivery/resources/0/delivery/init/sources/0",
			"/data/items/0/variants/0/delivery/resources/0/delivery/pieces/0/sources/0",
			"/data/items/0/variants/0/delivery/resources/0/delivery/pieces/1/sources/0",
			"/data/items/0/variants/0/delivery/resources/1/delivery/sources/0",
		}},
		{"14", []string{
			"/data/items/0/variants/0/delivery/sources/0",
		}},
		{"24", []string{
			"/data/items/0/variants/0/delivery/sources/0",
		}},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			payload := contractExample(t, tc.prefix)
			data := Map(payload["data"])
			data["cover"] = Object{"url": "https://media.example.test/work-cover.jpg"}
			data["author"] = Object{"url": "https://source.example.test/author#profile", "avatar": Object{"url": "https://media.example.test/avatar.jpg"}}
			data["source_url"] = "https://source.example.test/work#section"
			data["extra"] = Object{"url": "https://source.example.test/extra#keep", "size_bytes": json.Number("9007199254740993")}
			paths := append([]string(nil), tc.paths...)
			paths = append(paths, "/data/cover", "/data/author/avatar")
			for i, value := range Slice(data["items"]) {
				Map(value)["cover"] = Object{"url": fmt.Sprintf("https://media.example.test/item-%d.jpg", i)}
				paths = append(paths, fmt.Sprintf("/data/items/%d/cover", i))
			}
			for i, value := range Slice(data["attachments"]) {
				attachment := Map(value)
				author := objectCopy(Map(attachment["author"]))
				author["url"] = "https://source.example.test/music-author#profile"
				author["avatar"] = Object{"url": "https://media.example.test/music-avatar.jpg"}
				attachment["author"] = author
				paths = append(paths, fmt.Sprintf("/data/attachments/%d/author/avatar", i))
			}
			wanted := make(map[string]Object, len(paths))
			for _, path := range paths {
				source := publicProjectionObjectAt(t, payload, path)
				wanted[path] = Clone(source)
				delete(wanted[path], "size_bytes")
				source["url"] = Str(source["url"]) + "#fragment"
				source["size_bytes"] = json.Number("9223372036854775800")
			}
			// Rebuild the private default to match its original fragment-bearing
			// Source, just as the adapter does before the public boundary.
			payload = FinalizeData(data, publicProjectionNow)
			if err := ValidateEnvelopeAt(payload, publicProjectionNow); err != nil {
				t.Fatalf("invalid internal fixture: %v", err)
			}
			before := Clone(payload)
			for _, debug := range []bool{false, true} {
				view := PublicView(payload, debug, publicProjectionNow)
				for _, path := range paths {
					if got := publicProjectionObjectAt(t, view, path); !Equal(got, wanted[path]) {
						t.Fatalf("debug=%v Source %s: got %#v want %#v", debug, path, got, wanted[path])
					}
				}
				for _, path := range []string{"/data/author", "/data/extra"} {
					if publicProjectionObjectAt(t, view, path)["url"] != publicProjectionObjectAt(t, before, path)["url"] {
						t.Fatalf("rewrote non-Source URL at %s", path)
					}
				}
				if Map(view["data"])["source_url"] != Map(before["data"])["source_url"] || !Equal(Map(view["data"])["extra"], Map(before["data"])["extra"]) {
					t.Fatal("projection reached work navigation or extra metadata")
				}
				for i := range Slice(Map(view["data"])["attachments"]) {
					path := fmt.Sprintf("/data/attachments/%d/author", i)
					if publicProjectionObjectAt(t, view, path)["url"] != publicProjectionObjectAt(t, before, path)["url"] {
						t.Fatal("projection reached attachment author profile URL")
					}
				}
				if err := ValidateEnvelopeAt(view, publicProjectionNow); err != nil {
					t.Fatalf("invalid public response: %v", err)
				}
				if !Equal(view, PublicView(view, debug, publicProjectionNow)) {
					t.Fatal("public projection is not idempotent")
				}
			}
			if !reflect.DeepEqual(payload, before) {
				t.Fatal("public projection mutated private observations")
			}
		})
	}
}

func TestPublicViewProjectionPreservesIdentityDefaultsAndOrder(t *testing.T) {
	payload := contractExample(t, "16")
	data := Map(payload["data"])
	item := contractFirstItem(payload)
	file, mux := contractVariant(item, 0), contractVariant(item, 1)
	item["variants"] = Array{mux, file}
	const cleanURL = "https://media.example.test/file.mp4?line=2&line=1&sign=A%2Bb+c&key=%23%2523"
	sources := Array{
		Object{"id": "expired-first", "url": cleanURL + "#expired", "expires_at": "2000-01-01T00:00:00Z", "size_bytes": json.Number("9007199254740991")},
		Object{"id": "selected-second", "url": cleanURL + "#selected", "access": "direct", "availability": "available", "expires_at": "2100-01-01T00:00:00Z", "size_bytes": json.Number("9007199254740993")},
		Object{"id": "distinct-third", "url": cleanURL + "#alternative", "availability": "available", "size_bytes": json.Number("9007199254740990")},
	}
	Map(file["delivery"])["sources"] = sources
	item["default"] = Object{"variant_id": "1080-file", "source_id": "selected-second", "url": cleanURL + "#selected", "access": "direct", "expires_at": "2100-01-01T00:00:00Z"}
	data["generated_at"] = time.Unix(publicProjectionNow, 0).UTC().Format(time.RFC3339)
	const refresh = "/api/parse?url=https%3A%2F%2Fsource.example.test%2Fa%3Fkey%3D%2523%23chapter"
	const resolve = "/api/resolve?item_id=file-and-mux&url=https%3A%2F%2Fsource.example.test%2Fa%23chapter"
	data["refresh_url"], item["resolve_url"] = refresh+"&debug=0", resolve+"&debug=0"
	if err := ValidateEnvelopeAt(payload, publicProjectionNow); err != nil {
		t.Fatalf("invalid internal fixture: %v", err)
	}
	before := Clone(payload)
	for _, debug := range []bool{false, true} {
		want := Clone(before)
		wantData, wantItem := Map(want["data"]), contractFirstItem(want)
		wantSources := Slice(Map(contractVariant(wantItem, 1)["delivery"])["sources"])
		for _, value := range wantSources {
			Map(value)["url"] = cleanURL
		}
		delete(Map(wantSources[1]), "size_bytes")
		Map(wantItem["default"])["url"] = cleanURL
		wantData["refresh_url"], wantItem["resolve_url"] = refresh, resolve
		if debug {
			wantData["refresh_url"], wantItem["resolve_url"] = refresh+"&debug=1", resolve+"&debug=1"
		}
		got := PublicView(payload, debug, publicProjectionNow)
		if !Equal(got, want) {
			actual, _ := Encode(got)
			expected, _ := Encode(want)
			t.Fatalf("projection changed more than the two allowed fields or navigation mode:\ngot %s\nwant %s", actual, expected)
		}
		if err := ValidateEnvelopeAt(got, publicProjectionNow); err != nil {
			t.Fatal(err)
		}
		if !Equal(got, PublicView(got, debug, publicProjectionNow)) {
			t.Fatal("repeated projection changed identities, source order or default")
		}
	}
	if !reflect.DeepEqual(payload, before) {
		t.Fatal("projection changed original signed URLs, source sizes or identities")
	}
}

func TestPublicViewProjectionDoesNotHideInvalidSourceSizes(t *testing.T) {
	for _, value := range []any{"9007199254740993", true, nil, int64(0), int64(-1), json.Number("1.5")} {
		payload := contractExample(t, "02")
		contractSource(contractVariant(contractFirstItem(payload), 0))["size_bytes"] = value
		if err := ValidateEnvelopeAt(payload, publicProjectionNow); err == nil {
			t.Fatalf("test size should fail internal validation: %#v", value)
		}
		for _, debug := range []bool{false, true} {
			view := PublicView(payload, debug, publicProjectionNow)
			if err := ValidateEnvelopeAt(view, publicProjectionNow); err == nil {
				t.Fatalf("projection hid an unrelated invalid size: %#v", value)
			}
			got, exists := contractSource(contractVariant(contractFirstItem(view), 0))["size_bytes"]
			if !exists || !reflect.DeepEqual(got, value) {
				t.Fatalf("invalid size was silently removed or coerced: %#v", value)
			}
		}
	}
}

func publicProjectionObjectAt(t *testing.T, payload Object, path string) Object {
	t.Helper()
	var value any = payload
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		switch current := value.(type) {
		case map[string]any:
			var exists bool
			value, exists = current[component]
			if !exists {
				t.Fatalf("missing fixture path %s at %s", path, component)
			}
		case []any:
			index, err := strconv.Atoi(component)
			if err != nil || index < 0 || index >= len(current) {
				t.Fatalf("invalid fixture index %s in %s", component, path)
			}
			value = current[index]
		default:
			t.Fatalf("invalid fixture path %s at %s", path, component)
		}
	}
	object := Map(value)
	if object == nil {
		t.Fatalf("fixture path %s is not an object", path)
	}
	return object
}
