package unified

import "testing"

func TestCredentialFallbackSourceValidation(t *testing.T) {
	base := "https://media.example.test/video.mp4?signature=old"
	good := Object{"url": base, "fallback_urls": Array{"https://media.example.test/video.mp4?signature=new"}}
	if err := ValidateSource(good, nil); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{Array{}, Array{base}, Array{"https://other.test/video.mp4"}, Array{"http://media.example.test/video.mp4"}, Array{"https://user:pass@media.example.test/video.mp4"}, Array{"https://media.example.test/video.mp4#fragment"}, Array{"https://media.example.test/a", "https://media.example.test/b", "https://media.example.test/c"}, "not-an-array"} {
		if err := ValidateSource(Object{"url": base, "fallback_urls": value}, nil); err == nil {
			t.Errorf("accepted invalid fallback %v", value)
		}
	}
	if err := ValidateSource(Object{"url": "https://MEDIA.EXAMPLE.TEST:443/video.mp4", "fallback_urls": Array{"https://media.example.test/video.mp4?new=1"}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSourceIdentityRenewalAcrossPlatforms(t *testing.T) {
	const now int64 = 1762300800
	yt1 := "https://rr2---sn-ogueln67.googlevideo.com/videoplayback?expire=1700000000&ei=AAA&ip=1.2.3.4&id=o-123&itag=18&source=youtube&clen=28523658&sig=SIG_OLD"
	yt2 := "https://rr2---sn-ogueln67.googlevideo.com/videoplayback?expire=1700003600&ei=BBB&ip=5.6.7.8&id=o-123&itag=18&source=youtube&clen=28523658&sig=SIG_NEW"
	ytDifferentItag := "https://rr2---sn-ogueln67.googlevideo.com/videoplayback?expire=1700003600&ei=BBB&ip=5.6.7.8&id=o-123&itag=299&source=youtube&clen=28523658&sig=SIG_NEW"
	ytDifferentContent := "https://rr2---sn-ogueln67.googlevideo.com/videoplayback?expire=1700003600&ei=BBB&ip=5.6.7.8&id=o-999&itag=18&source=youtube&clen=28523658&sig=SIG_NEW"

	s1 := Map(Sources(Array{yt1}, now)[0])
	s2 := Map(Sources(Array{yt2}, now)[0])
	sDiffItag := Map(Sources(Array{ytDifferentItag}, now)[0])
	sDiffContent := Map(Sources(Array{ytDifferentContent}, now)[0])

	if Str(s1["id"]) != Str(s2["id"]) {
		t.Fatalf("expected identical source.id across expire/sig rotation, got %s vs %s", s1["id"], s2["id"])
	}
	if Str(s1["url"]) != yt1 || Str(s2["url"]) != yt2 {
		t.Fatalf("SourceIdentity must not rewrite raw source URL bytes")
	}
	if Str(s1["id"]) == Str(sDiffItag["id"]) {
		t.Fatalf("different itag must not share source.id: %s", s1["id"])
	}
	if Str(s1["id"]) == Str(sDiffContent["id"]) {
		t.Fatalf("different content id must not share source.id: %s", s1["id"])
	}

	ig1 := "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/progressive720.mp4?efg=eyJ&oe=672A0001&oh=00_AAA&_nc_sid=111"
	ig2 := "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/progressive720.mp4?efg=eyJ2&oe=672B0002&oh=00_BBB&_nc_sid=222"
	igOther := "https://scontent-nrt1-2.cdninstagram.com/o1/v/t16/f2/m69/other720.mp4?efg=eyJ2&oe=672B0002&oh=00_BBB&_nc_sid=222"
	if Str(Map(Sources(Array{ig1}, now)[0])["id"]) != Str(Map(Sources(Array{ig2}, now)[0])["id"]) {
		t.Fatalf("expected identical Instagram source.id across oe/oh rotation")
	}
	if Str(Map(Sources(Array{ig1}, now)[0])["id"]) == Str(Map(Sources(Array{igOther}, now)[0])["id"]) {
		t.Fatalf("different Instagram media paths must not share source.id")
	}

	x1 := "https://video.twimg.com/ext_tw_video/101/pu/vid/avc1/1280x720/a.mp4?tag=12&expires=1700000000&signature=AAA"
	x2 := "https://video.twimg.com/ext_tw_video/101/pu/vid/avc1/1280x720/a.mp4?tag=12&expires=1700003600&signature=BBB"
	xOther := "https://video.twimg.com/ext_tw_video/101/pu/vid/avc1/640x360/b.mp4?tag=12&expires=1700003600&signature=BBB"
	if Str(Map(Sources(Array{x1}, now)[0])["id"]) != Str(Map(Sources(Array{x2}, now)[0])["id"]) {
		t.Fatalf("expected identical X source.id across expires/signature rotation")
	}
	if Str(Map(Sources(Array{x1}, now)[0])["id"]) == Str(Map(Sources(Array{xOther}, now)[0])["id"]) {
		t.Fatalf("different X video renditions must not share source.id")
	}
}
