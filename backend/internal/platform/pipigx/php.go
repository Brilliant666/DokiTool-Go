package pipigx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// PHP decodes JSON objects as ordered associative arrays. Keep insertion order
// and the last value for a repeated key; never derive source order from a Go map.
type phpObject struct {
	keys   []string
	fields unified.Object
}

func decodePHP(raw []byte, bigintStrings bool, maxDepth int) (any, error) {
	if !utf8.Valid(raw) || !validJSONSurrogates(raw) {
		return nil, fmt.Errorf("invalid upstream Unicode")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if delimiter, ok := token.(json.Delim); ok {
			if depth+1 >= maxDepth {
				return nil, fmt.Errorf("PHP JSON depth exceeded")
			}
			if delimiter == '{' {
				out := &phpObject{fields: unified.Object{}}
				for decoder.More() {
					keyToken, err := decoder.Token()
					if err != nil {
						return nil, err
					}
					key, ok := keyToken.(string)
					if !ok {
						return nil, fmt.Errorf("invalid object key")
					}
					value, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					if _, seen := out.fields[key]; !seen {
						out.keys = append(out.keys, key)
					}
					out.fields[key] = value
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim('}') {
					return nil, fmt.Errorf("unterminated object")
				}
				return out, nil
			}
			if delimiter == '[' {
				out := unified.Array{}
				for decoder.More() {
					value, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					out = append(out, value)
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim(']') {
					return nil, fmt.Errorf("unterminated list")
				}
				return out, nil
			}
			return nil, fmt.Errorf("unexpected delimiter")
		}
		if number, ok := token.(json.Number); ok {
			if !strings.ContainsAny(number.String(), ".eE") {
				if integer, err := number.Int64(); err == nil {
					return integer, nil
				}
				if bigintStrings {
					return number.String(), nil
				}
			}
			return number.Float64()
		}
		return token, nil
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON")
	}
	return value, nil
}
func field(value any, key string) any {
	if object, ok := value.(*phpObject); ok {
		return object.fields[key]
	}
	if object := unified.Map(value); object != nil {
		return object[key]
	}
	if list := unified.Slice(value); list != nil {
		i, err := strconv.Atoi(key)
		if err == nil && i >= 0 && i < len(list) && strconv.Itoa(i) == key {
			return list[i]
		}
	}
	return nil
}
func isArray(value any) bool {
	_, ok := value.(*phpObject)
	return ok || unified.Map(value) != nil || unified.Slice(value) != nil
}
func values(value any) unified.Array {
	if object, ok := value.(*phpObject); ok {
		out := make(unified.Array, 0, len(object.keys))
		for _, key := range object.keys {
			out = append(out, object.fields[key])
		}
		return out
	}
	if list := unified.Slice(value); list != nil {
		return list
	}
	return unified.Array{}
}
func plain(value any) any {
	if object, ok := value.(*phpObject); ok {
		list := true
		for index, key := range object.keys {
			if strconv.Itoa(index) != key {
				list = false
				break
			}
		}
		if list {
			out := unified.Array{}
			for _, key := range object.keys {
				out = append(out, plain(object.fields[key]))
			}
			return out
		}
		out := unified.Object{}
		for _, key := range object.keys {
			out[key] = plain(object.fields[key])
		}
		return out
	}
	if list := unified.Slice(value); list != nil {
		out := make(unified.Array, 0, len(list))
		for _, child := range list {
			out = append(out, plain(child))
		}
		return out
	}
	return value
}
func first(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
func truthy(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != "" && value != "0"
	case int64:
		return value != 0
	case float64:
		return value != 0
	case *phpObject:
		return len(value.keys) != 0
	}
	if list := unified.Slice(value); list != nil {
		return len(list) != 0
	}
	return true
}
func scalarString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case int64:
		return strconv.FormatInt(value, 10)
	case int:
		return strconv.Itoa(value)
	case float64:
		return strconv.FormatFloat(value, 'G', 14, 64)
	case bool:
		if value {
			return "1"
		}
		return ""
	case nil:
		return ""
	}
	return "Array"
}
func percentDecode(raw string) string {
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '%' && i+2 < len(raw) {
			if value, err := strconv.ParseUint(raw[i+1:i+3], 16, 8); err == nil {
				out.WriteByte(byte(value))
				i += 2
				continue
			}
		}
		out.WriteByte(raw[i])
	}
	return out.String()
}

// Query scalar/array assignment follows PHP parse_str, without decoding twice.
func queryValue(query, wanted string) (value string, present, scalar bool) {
	query, _, _ = strings.Cut(query, "\x00")
	for _, pair := range strings.Split(query, "&") {
		key, raw, _ := strings.Cut(pair, "=")
		key = percentDecode(strings.ReplaceAll(key, "+", " "))
		key, _, _ = strings.Cut(key, "\x00")
		key = strings.TrimLeft(key, " ")
		array := false
		if bracket := strings.IndexByte(key, '['); bracket >= 0 {
			if strings.Contains(key[bracket+1:], "]") {
				key, array = key[:bracket], true
			} else {
				key = strings.ReplaceAll(key, "[", "_")
			}
		}
		key = strings.NewReplacer(" ", "_", ".", "_").Replace(key)
		if key == wanted {
			value, present, scalar = percentDecode(strings.ReplaceAll(raw, "+", " ")), true, !array
		}
	}
	return
}

func validJSONSurrogates(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		unit, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if unit >= 0xdc00 && unit <= 0xdfff {
			return false
		}
		if unit >= 0xd800 && unit <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
