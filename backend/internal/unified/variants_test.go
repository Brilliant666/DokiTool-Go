package unified

import "testing"

func TestManifestTrailingSlashDoesNotBecomeCompleteFile(t *testing.T) {
	// PHP pathinfo ignores trailing directory separators when identifying the
	// extension. Preserve that behavior before deriving delivery eligibility.
	for _, tc := range []struct{ url, kind string }{
		{"https://media.example/playlist.m3u8/", "hls"},
		{"https://media.example/manifest.MPD///?token=a%2Bb", "dash"},
		{"https://media.example/playlist.m3u8/.", "file"},
	} {
		t.Run(tc.url, func(t *testing.T) {
			variants := FileVariants(Array{Object{"url": tc.url}}, 1800000000)
			if len(variants) != 1 || Str(Map(Map(variants[0])["delivery"])["kind"]) != tc.kind {
				t.Fatalf("delivery mismatch: %#v; want %s", variants, tc.kind)
			}
			selected := SelectDefault(Object{"id": "item", "type": "video", "variants": variants}, 1800000000)
			if tc.kind != "file" && selected["default"] != nil {
				t.Fatal("manifest became directly downloadable default")
			}
		})
	}
}

func TestVariantAggregationOwnsItsMutableState(t *testing.T) {
	// A Go server may reuse immutable fixture/upstream data across requests. A
	// mutation of one returned pairing must not alter its sibling or input data.
	input := Array{Object{"url": "https://media.example/video.mp4", "private_media_identity": "shared-video",
		"private_delivery_kind": "video_track", "width": 3840, "height": 2160, "fps": 60,
		"private_audio_candidates": Array{
			Object{"url": "https://media.example/audio-a.m4a", "private_media_identity": "audio-a", "audio_channels": 2},
			Object{"url": "https://media.example/audio-b.m4a", "private_media_identity": "audio-b", "audio_channels": 6},
		}}}
	before := CloneValue(input)
	first := FileVariants(input, 1800000000)
	if len(first) != 2 {
		t.Fatalf("got %d pairings, want 2", len(first))
	}
	if !Equal(input, before) {
		t.Fatal("construction mutated caller's observations")
	}
	firstResources := Slice(Map(Map(first[0])["delivery"])["resources"])
	Map(Map(firstResources[0])["metadata"])["fps"] = 1
	Map(Map(first[0])["metadata"])["fps"] = 2
	if !Equal(input, before) {
		t.Fatal("returned value aliases caller's observations")
	}
	secondResources := Slice(Map(Map(first[1])["delivery"])["resources"])
	if !Equal(Map(Map(secondResources[0])["metadata"])["fps"], 60) {
		t.Fatal("one pairing aliases another's video metadata")
	}
	if !Equal(FileVariants(input, 1800000000), FileVariants(Slice(before), 1800000000)) {
		t.Fatal("one response changed later construction")
	}
}
