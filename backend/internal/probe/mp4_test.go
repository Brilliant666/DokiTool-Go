package probe

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func TestMeasuredMetadataAndUnknownCapabilities(t *testing.T) {
	for codec, expected := range map[string]string{"avc1": "h264", "avc3": "h264", "hvc1": "hevc", "hev1": "hevc", "av01": "av1"} {
		t.Run(codec, func(t *testing.T) {
			want := unified.Object{"width": 2160, "height": 3840, "codec": expected, "duration": 10, "bit_rate": 4000000, "size": 6000000, "metadata_source": "mp4"}
			got := Metadata(makeMovie(2160, 3840, codec, 500000, 0, false), 6000000)
			if !unified.Equal(got, want) {
				t.Fatalf("wrong sample bitrate/dimensions: %#v", got)
			}
			for _, field := range []string{"fps", "hdr", "has_audio", "audio_channels", "audio_language"} {
				if _, exists := got[field]; exists {
					t.Fatalf("invented unmeasured capability %s", field)
				}
			}
		})
	}
	variable := Metadata(makeMovie(1920, 1080, "avc1", 100000, 1, true), 6000000)
	if variable["bit_rate"] != int64(800000) || variable["width"] != int64(1920) {
		t.Fatalf("variable sample sizes/version-one duration lost: %#v", variable)
	}
}

func TestMalformedOrAmbiguousMetadataStaysUnknown(t *testing.T) {
	base := makeMovie(2160, 3840, "hvc1", 500000, 0, false)
	mutate := func(kind string, relative int, value uint32) []byte {
		out := bytes.Clone(base)
		i := bytes.Index(out, []byte(kind)) + 4 + relative
		binary.BigEndian.PutUint32(out[i:], value)
		return out
	}
	tests := map[string][]byte{
		"empty":                         nil,
		"truncated root":                base[:len(base)-1],
		"root with trailing garbage":    append(bytes.Clone(base), 1),
		"multiple video tracks":         makeBox("moov", append(bytes.Clone(base[8:]), base[8:]...)),
		"malformed child size":          makeBox("moov", append(words(4), []byte("trak")...)),
		"unsafe extended size":          append(append(words(1), []byte("moov")...), words(0xffffffff, 0xffffffff)...),
		"unsupported codec":             makeMovie(2160, 3840, "zzzz", 500000, 0, false),
		"zero width":                    makeMovie(0, 3840, "hvc1", 500000, 0, false),
		"oversized height":              makeMovie(2160, 16385, "hvc1", 500000, 0, false),
		"zero timescale":                mutate("mdhd", 12, 0),
		"zero duration":                 mutate("mdhd", 16, 0),
		"too short duration":            mutate("mdhd", 16, 9),
		"too long duration":             mutate("mdhd", 16, 86400001),
		"unknown mdhd version":          mutate("mdhd", 0, 0x02000000),
		"ambiguous sample descriptions": mutate("stsd", 4, 2),
		"truncated visual entry":        mutate("stsd", 8, 85),
		"zero sample count":             mutate("stsz", 8, 0),
		"sample count exceeds bound":    mutate("stsz", 8, 1000001),
		"missing variable samples":      mutate("stsz", 4, 0),
		"impossible fixed sample bytes": mutate("stsz", 4, 0xffffffff),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if value := Metadata(data, 6000000); value != nil {
				t.Fatalf("invented metadata for malformed layout: %#v", value)
			}
		})
	}
	if value := Metadata(base, 1000); value != nil {
		t.Fatal("sample bytes exceeded file size")
	}
}

func TestExtendedZeroSizedAndNonVideoAtoms(t *testing.T) {
	base := makeMovie(2160, 3840, "hvc1", 500000, 0, false)
	want := Metadata(base, 6000000)
	extended := append(append(words(1), []byte("moov")...), words(0, uint32(len(base)+8))...)
	extended = append(extended, base[8:]...)
	zeroSized := bytes.Clone(base)
	binary.BigEndian.PutUint32(zeroSized, 0)
	audio := makeBox("trak", makeBox("mdia", makeBox("hdlr", append(make([]byte, 8), []byte("soun")...))))
	withAudio := makeBox("moov", append(audio, base[8:]...))
	for name, value := range map[string][]byte{"extended": extended, "zero-to-end": zeroSized, "non-video ignored": withAudio} {
		t.Run(name, func(t *testing.T) {
			if got := Metadata(value, 6000000); !unified.Equal(got, want) {
				t.Fatalf("supported container variant rejected: %#v", got)
			}
		})
	}
	tooMany := makeBox("moov", append(bytes.Repeat(makeBox("free", nil), 4096), base[8:]...))
	if Metadata(tooMany, 6000000) != nil {
		t.Fatal("nested child budget exceeded")
	}
}

func FuzzMetadataNeverPanics(f *testing.F) {
	f.Add([]byte{}, int64(1))
	f.Add(makeMovie(2160, 3840, "hvc1", 500000, 0, false), int64(6000000))
	f.Add(append(append(words(1), []byte("moov")...), words(0xffffffff, 0xffffffff)...), int64(6000000))
	f.Fuzz(func(t *testing.T, data []byte, fileSize int64) {
		if len(data) > int(MaxBytes) {
			t.Skip()
		}
		value := Metadata(data, fileSize)
		if value != nil && (len(value) != 7 || value["metadata_source"] != "mp4") {
			t.Fatalf("incomplete measured metadata: %#v", value)
		}
	})
}
