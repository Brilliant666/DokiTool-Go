package probe

import (
	"encoding/binary"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type box struct {
	kind   string
	size   int64
	header int64
}

func u32(data []byte, offset int64) int64 {
	return int64(binary.BigEndian.Uint32(data[offset : offset+4]))
}

func u64(data []byte, offset int64) (int64, bool) {
	high, low := u32(data, offset), u32(data, offset+4)
	// Match the PHP reader's exact integer boundary, not just Go's int64 limit.
	if high > 0x1fffff {
		return 0, false
	}
	return high*(1<<32) + low, true
}

func atom(data []byte, offset, available int64, complete bool) (box, bool) {
	if offset < 0 || int64(len(data))-offset < 8 || available < 8 {
		return box{}, false
	}
	size, header := u32(data, offset), int64(8)
	if size == 1 {
		if int64(len(data))-offset < 16 {
			return box{}, false
		}
		var ok bool
		size, ok = u64(data, offset+8)
		if !ok {
			return box{}, false
		}
		header = 16
	} else if size == 0 {
		size = available
	}
	if size < header || size > available || (complete && size > int64(len(data))-offset) {
		return box{}, false
	}
	return box{kind: string(data[offset+4 : offset+8]), size: size, header: header}, true
}

type childBox struct {
	kind string
	data []byte
}

func children(data []byte) ([]childBox, bool) {
	out := []childBox{}
	var offset int64
	length := int64(len(data))
	for offset < length && len(out) < 4096 {
		b, ok := atom(data, offset, length-offset, true)
		if !ok {
			return nil, false
		}
		out = append(out, childBox{kind: b.kind, data: data[offset+b.header : offset+b.size]})
		offset += b.size
	}
	return out, offset == length
}

func child(data []byte, kind string) ([]byte, bool) {
	items, ok := children(data)
	if !ok {
		return nil, false
	}
	var found []byte
	present := false
	for _, item := range items {
		if item.kind != kind {
			continue
		}
		if present {
			return nil, false
		}
		found, present = item.data, true
	}
	return found, present
}

// Metadata measures one unambiguous video track. Dimensions/codec come from
// stsd, duration from mdhd, and video-only bitrate from stsz sample bytes.
// It deliberately does not infer FPS, HDR or audio presence from incomplete
// container evidence. Unknown/unsupported/ambiguous layouts return nil.
func Metadata(moov []byte, fileSize int64) unified.Object {
	root, ok := atom(moov, 0, int64(len(moov)), true)
	if !ok || root.kind != "moov" || root.size != int64(len(moov)) {
		return nil
	}
	items, ok := children(moov[root.header:])
	if !ok {
		return nil
	}
	var video unified.Object
	for _, track := range items {
		if track.kind != "trak" {
			continue
		}
		mdia, ok := child(track.data, "mdia")
		if !ok {
			continue
		}
		handler, ok := child(mdia, "hdlr")
		if !ok || len(handler) < 12 || string(handler[8:12]) != "vide" {
			continue
		}
		if video != nil {
			return nil
		}
		mdhd, haveMDHD := child(mdia, "mdhd")
		minf, haveMINF := child(mdia, "minf")
		if !haveMDHD || !haveMINF || len(mdhd) < 20 {
			return nil
		}
		var scale, ticks int64
		switch mdhd[0] {
		case 0:
			scale, ticks = u32(mdhd, 12), u32(mdhd, 16)
		case 1:
			if len(mdhd) < 32 {
				return nil
			}
			scale = u32(mdhd, 20)
			ticks, ok = u64(mdhd, 24)
			if !ok {
				return nil
			}
		default:
			return nil
		}
		if scale < 1 || ticks < 1 {
			return nil
		}
		duration := float64(ticks) / float64(scale)
		if duration < 0.01 || duration > 86400 {
			return nil
		}
		stbl, ok := child(minf, "stbl")
		if !ok {
			return nil
		}
		stsd, haveSTSD := child(stbl, "stsd")
		stsz, haveSTSZ := child(stbl, "stsz")
		if !haveSTSD || len(stsd) < 44 || u32(stsd, 4) != 1 || !haveSTSZ || len(stsz) < 12 {
			return nil
		}
		entry, ok := atom(stsd, 8, int64(len(stsd))-8, true)
		if !ok || entry.header != 8 || entry.size < 86 {
			return nil
		}
		codec := map[string]string{"avc1": "h264", "avc3": "h264", "hvc1": "hevc", "hev1": "hevc", "av01": "av1"}[entry.kind]
		width := int64(binary.BigEndian.Uint16(stsd[40:42]))
		height := int64(binary.BigEndian.Uint16(stsd[42:44]))
		if codec == "" || width < 1 || height < 1 || width > 16384 || height > 16384 {
			return nil
		}
		fixed, count := u32(stsz, 4), u32(stsz, 8)
		if count < 1 || count > 1000000 {
			return nil
		}
		expected := int64(12)
		if fixed == 0 {
			expected += count * 4
		}
		if int64(len(stsz)) != expected {
			return nil
		}
		bytes := fixed * count
		if fixed == 0 {
			for i := int64(0); i < count; i++ {
				bytes += u32(stsz, 12+i*4)
			}
		}
		if bytes < 1 || bytes > fileSize {
			return nil
		}
		video = unified.Object{
			"width": width, "height": height, "codec": codec,
			"duration": unified.PHPRound(duration, 3),
			"bit_rate": int64(unified.PHPRound(float64(bytes)*8/duration, 0)),
			"size":     fileSize, "metadata_source": "mp4",
		}
	}
	return video
}
