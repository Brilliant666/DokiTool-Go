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
