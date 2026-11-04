package xiaohongshu

import (
	"context"
	"reflect"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestPreparePublicLiveMotionHTTPS(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"http://sns-v11.rednotecdn.com/a%2fb?x=%2B&x=1&empty=", "https://sns-v11.rednotecdn.com/a%2fb?x=%2B&x=1&empty="},
		{"http://sns-v27.rednotecdn.com/z?foo=1", "https://sns-v27.rednotecdn.com/z?foo=1"},
		{"https://sns-v11.rednotecdn.com/a%2fb?x=%2B&x=1&empty=", "https://sns-v11.rednotecdn.com/a%2fb?x=%2B&x=1&empty="},
		{"http://sns-v11.rednotecdn.com.evil.test/a", "http://sns-v11.rednotecdn.com.evil.test/a"},
		{"http://user@sns-v11.rednotecdn.com/a", "http://user@sns-v11.rednotecdn.com/a"},
		{"http://sns-v11.rednotecdn.com:80/a", "http://sns-v11.rednotecdn.com:80/a"},
		{"http://sns-v11.rednotecdn.com\\@evil.test/a", "http://sns-v11.rednotecdn.com\\@evil.test/a"},
		{"http://SNS-V11.rednotecdn.com/a", "https://SNS-V11.rednotecdn.com/a"},
		{"http://ſns-v11.rednotecdn.com/a", "http://ſns-v11.rednotecdn.com/a"},
		{"", ""},
	}
	source := func(id, address string) unified.Object {
		return unified.Object{"id": id, "url": address, "access": "direct", "expires_at": "2030-01-01T00:00:00Z"}
	}
	sources := unified.Array{}
	for i, c := range cases {
		sources = append(sources, source(string(rune('a'+i)), c.raw))
	}
	sources = append(sources, unified.Object{"id": "relay", "url": cases[0].raw, "access": "relay", "availability": "unavailable", "expires_at": "2027-01-01T00:00:00Z"})
	motion := unified.Object{"id": "motion", "role": "motion", "delivery": unified.Object{"kind": "file", "sources": sources}}
	image := unified.Object{"id": "image", "role": "image", "delivery": unified.Object{"kind": "file", "sources": unified.Array{source("still", cases[0].raw)}}}
	pair := unified.Object{"kind": "pair", "resources": unified.Array{image, motion}}
	item := unified.Object{"id": "live", "type": "live_photo", "default": nil, "variants": unified.Array{unified.Object{"id": "v", "delivery": pair}, unified.Object{"id": "v2", "delivery": unified.Clone(pair)}}}
	payload := unified.Object{"code": "OK", "data": unified.Object{"cover": unified.Object{"url": cases[0].raw}, "items": unified.Array{item, unified.Object{"id": "ordinary", "type": "video", "variants": unified.Array{unified.Object{"delivery": pair}}}}}}
	before := unified.Clone(payload)
	got, err := PreparePublic(context.Background(), "xiaohongshu", payload, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload, before) {
		t.Fatal("input mutated")
	}
	expected := unified.Clone(before)
	variants := unified.Slice(unified.Map(unified.Slice(unified.Map(expected["data"])["items"])[0])["variants"])
	actualVariants := unified.Slice(unified.Map(unified.Slice(unified.Map(got["data"])["items"])[0])["variants"])
	for n, v := range variants {
		resource := unified.Map(unified.Slice(unified.Map(unified.Map(v)["delivery"])["resources"])[1])
		expectedSources := unified.Slice(unified.Map(resource["delivery"])["sources"])
		actualResource := unified.Map(unified.Slice(unified.Map(unified.Map(actualVariants[n])["delivery"])["resources"])[1])
		actualSources := unified.Slice(unified.Map(actualResource["delivery"])["sources"])
		for i, c := range cases {
			unified.Map(expectedSources[i])["url"] = c.want
			if address := unified.Map(actualSources[i])["url"]; address != c.want {
				t.Errorf("variant %d source %d: got %q want %q", n, i, address, c.want)
			}
		}
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatal("public payload differs from expected")
	}
	again, err := PreparePublic(context.Background(), "xiaohongshu", got, 0)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatal("not idempotent", err)
	}
	other, err := PreparePublic(context.Background(), "douyin", payload, 0)
	if err != nil || !reflect.DeepEqual(other, before) {
		t.Fatal("other platform changed", err)
	}
}
