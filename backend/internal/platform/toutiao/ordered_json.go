package toutiao

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// decodePHPJSON reproduces JSON_BIGINT_AS_STRING rather than rounding a work
// or author ID. PHP decodes fractional/exponent tokens as doubles.
func decodePHPJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) || !validJSONSurrogates(raw) {
		return nil, fmt.Errorf("invalid upstream JSON Unicode")
	}
	value, err := unified.DecodeValue(raw)
	if err != nil {
		return nil, err
	}
	var convert func(any, int) (any, error)
	convert = func(value any, depth int) (any, error) {
		if depth >= 512 {
			return nil, fmt.Errorf("upstream JSON depth exceeds PHP boundary")
		}
		switch v := value.(type) {
		case json.Number:
			if !strings.ContainsAny(v.String(), ".eE") {
				if integer, err := v.Int64(); err == nil {
					return integer, nil
				}
				return v.String(), nil
			}
			return v.Float64()
		case map[string]any:
			for key, child := range v {
				converted, err := convert(child, depth+1)
				if err != nil {
					return nil, err
				}
				v[key] = converted
			}
		case []any:
			for index, child := range v {
				converted, err := convert(child, depth+1)
				if err != nil {
					return nil, err
				}
				v[index] = converted
			}
		}
		return value, nil
	}
	return convert(value, 0)
}

// Go's decoder replaces lone UTF-16 surrogates, whereas PHP rejects them.
// Track string escapes so literal backslash-u text is not misclassified.
func validJSONSurrogates(raw []byte) bool {
	inString := false
	for index := 0; index < len(raw); index++ {
		if raw[index] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[index] != '\\' {
			continue
		}
		index++
		if index >= len(raw) {
			return false
		}
		if raw[index] != 'u' {
			continue
		}
		if index+4 >= len(raw) {
			return false
		}
		unit, err := strconv.ParseUint(string(raw[index+1:index+5]), 16, 16)
		if err != nil {
			return false
		}
		index += 4
		if unit >= 0xdc00 && unit <= 0xdfff {
			return false
		}
		if unit >= 0xd800 && unit <= 0xdbff {
			if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[index+3:index+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			index += 6
		}
	}
	return true
}

// orderedValues uses original JSON object insertion order with PHP's
// last-value/first-position handling of repeated keys. Lists retain order.
func orderedValues(raw []byte, value any, path ...string) (unified.Array, error) {
	if values := unified.Slice(value); values != nil {
		return values, nil
	}
	fields := unified.Map(value)
	if fields == nil {
		return unified.Array{}, nil
	}
	current := json.RawMessage(raw)
	for _, key := range path {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(current, &object); err != nil {
			return nil, err
		}
		current = object[key]
	}
	decoder := json.NewDecoder(bytes.NewReader(current))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("ordered representations must be an object")
	}
	values, seen := unified.Array{}, map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("representation key must be a string")
		}
		var ignored json.RawMessage
		if err := decoder.Decode(&ignored); err != nil {
			return nil, err
		}
		if !seen[key] {
			values = append(values, fields[key])
			seen[key] = true
		}
	}
	return values, nil
}

// rawurldecode preserves '+' and leaves malformed percent escapes intact.
func percentDecode(raw string) string {
	var out strings.Builder
	for index := 0; index < len(raw); index++ {
		if raw[index] == '%' && index+2 < len(raw) {
			if value, err := strconv.ParseUint(raw[index+1:index+3], 16, 8); err == nil {
				out.WriteByte(byte(value))
				index += 2
				continue
			}
		}
		out.WriteByte(raw[index])
	}
	return out.String()
}
