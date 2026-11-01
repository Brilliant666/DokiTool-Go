package douyin

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// PHP iterates audio URL lists in wire order, including named CDN alternatives.
// Decode these objects before their ordering is lost to Go map iteration. The
// ordinary array form needs no second inspection of the upstream bytes.
func normalizeAudioURLLists(raw []byte, detail object) error {
	entries := unified.Slice(child(detail, "video", "bit_rate_audio"))
	var wire []json.RawMessage
	for i, value := range entries {
		audio := unified.Map(child(value, "audio_meta"))
		fields := unified.Map(audio["url_list"])
		if len(fields) == 0 {
			continue
		}
		if wire == nil {
			list, err := rawJSONField(raw, "aweme_detail", "video", "bit_rate_audio")
			if err != nil {
				return err
			}
			if err := json.Unmarshal(list, &wire); err != nil {
				return err
			}
		}
		wireURLs, err := rawJSONField(wire[i], "audio_meta", "url_list")
		if err != nil {
			return err
		}
		dec := json.NewDecoder(bytes.NewReader(wireURLs))
		if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
			return fmt.Errorf("expected named audio URLs")
		}
		ordered := make(array, 0, len(fields))
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			var ignored json.RawMessage
			if err := dec.Decode(&ignored); err != nil {
				return err
			}
			name := key.(string) // JSON object keys are strings.
			if url, exists := fields[name]; exists {
				// Duplicate JSON names keep the last value at the first position.
				ordered = append(ordered, url)
				delete(fields, name)
			}
		}
		audio["url_list"] = ordered
	}
	return nil
}

// Read only the requested object members; JSON field matching is case-sensitive.
func rawJSONField(raw json.RawMessage, path ...string) (json.RawMessage, error) {
	for _, key := range path {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		raw = fields[key]
	}
	return raw, nil
}
