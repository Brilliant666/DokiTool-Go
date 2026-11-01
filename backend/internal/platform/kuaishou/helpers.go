package kuaishou

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// PHP associative arrays preserve insertion order, including first position
// and last value for duplicate JSON keys. Extraction must not use Go map order.
type phpObject struct {
	keys   []string
	fields unified.Object
}

type phpJSONError struct{ message string }

func (e phpJSONError) Error() string { return e.message }

func decodePHPJSON(raw string) (any, error) {
	if !utf8.ValidString(raw) {
		return nil, phpJSONError{"Malformed UTF-8 characters, possibly incorrectly encoded"}
	}
	if unpairedSurrogate([]byte(raw)) {
		return nil, phpJSONError{"Single unpaired UTF-16 surrogate in unicode escape"}
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth >= 512 {
			return nil, phpJSONError{"Maximum stack depth exceeded"}
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, jsonError(err, raw)
		}
		switch token {
		case json.Delim('{'):
			out := &phpObject{fields: unified.Object{}}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, jsonError(err, raw)
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, phpJSONError{"Syntax error"}
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
			if _, err := decoder.Token(); err != nil {
				return nil, jsonError(err, raw)
			}
			return out, nil
		case json.Delim('['):
			out := unified.Array{}
			for decoder.More() {
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				out = append(out, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, jsonError(err, raw)
			}
			return out, nil
		}
		if number, ok := token.(json.Number); ok {
			if !strings.ContainsAny(number.String(), ".eE") {
				if integer, err := number.Int64(); err == nil {
					return integer, nil
				}
			}
			value, err := number.Float64()
			if err != nil {
				return nil, phpJSONError{"Syntax error"}
			}
			return value, nil
		}
		return token, nil
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, phpJSONError{"Syntax error"}
	}
	return value, nil
}

func jsonError(err error, raw string) error {
	if syntax, ok := err.(*json.SyntaxError); ok && syntax.Offset > 0 && syntax.Offset <= int64(len(raw)) {
		if raw[syntax.Offset-1] < 0x20 && strings.Contains(syntax.Error(), "string literal") {
			return phpJSONError{"Control character error, possibly incorrectly encoded"}
		}
	}
	return phpJSONError{"Syntax error"}
}

func unpairedSurrogate(raw []byte) bool {
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
		if index+4 >= len(raw) || raw[index] != 'u' {
			continue
		}
		unit, err := strconv.ParseUint(string(raw[index+1:index+5]), 16, 16)
		if err != nil {
			continue
		}
		index += 4
		if unit >= 0xdc00 && unit <= 0xdfff {
			return true
		}
		if unit >= 0xd800 && unit <= 0xdbff {
			if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
				return true
			}
			low, err := strconv.ParseUint(string(raw[index+3:index+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return true
			}
			index += 6
		}
	}
	return false
}

func field(value any, keys ...string) any {
	for _, key := range keys {
		switch object := value.(type) {
		case *phpObject:
			value = object.fields[key]
		case map[string]any:
			value = object[key]
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(object) || strconv.Itoa(index) != key {
				return nil
			}
			value = object[index]
		default:
			return nil
		}
	}
	return value
}

func fallback(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func arrayValues(value any) (unified.Array, bool) {
	switch array := value.(type) {
	case *phpObject:
		values := make(unified.Array, 0, len(array.keys))
		for _, key := range array.keys {
			values = append(values, array.fields[key])
		}
		return values, true
	case []any:
		return array, true
	}
	return nil, false
}

func truthy(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case string:
		return value != "" && value != "0"
	case bool:
		return value
	}
	if values, ok := arrayValues(value); ok {
		return len(values) > 0
	}
	if number, ok := unified.Numeric(value); ok {
		return number != 0
	}
	return true
}

// Convert PHP arrays to their JSON wire shape, including empty/numerically
// indexed objects. Upstream order is retained until all selection is finished.
func plain(value any) any {
	switch value := value.(type) {
	case *phpObject:
		list := true
		for index, key := range value.keys {
			if key != strconv.Itoa(index) {
				list = false
				break
			}
		}
		if list {
			out := unified.Array{}
			for _, key := range value.keys {
				out = append(out, plain(value.fields[key]))
			}
			return out
		}
		out := unified.Object{}
		for _, key := range value.keys {
			out[key] = plain(value.fields[key])
		}
		return out
	case []any:
		out := unified.Array{}
		for _, child := range value {
			out = append(out, plain(child))
		}
		return out
	}
	return value
}

// JSON_OPTIONS=480 includes NUMERIC_CHECK. This mirrors the existing Bilibili
// entry boundary: exact int64 numeric strings stay exact; larger ones become
// doubles before PHP-compatible JSON serialization.
func legacyWire(value unified.Object) (unified.Object, error) {
	encoded, err := unified.PHPJSON(entryValue(value), true)
	if err != nil {
		return nil, err
	}
	wire, err := unified.Decode(encoded)
	if err != nil {
		return nil, err
	}
	return unified.Map(canonicalZero(wire)), nil
}

func entryValue(value any) any {
	if text, ok := value.(string); ok {
		if number, numeric := unified.Numeric(text); numeric {
			trimmed := strings.Trim(text, " \t\n\r\v\f")
			if !strings.ContainsAny(trimmed, ".eE") {
				if integer, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
					return integer
				}
			}
			return number
		}
		return text
	}
	if object := unified.Map(value); object != nil {
		if len(object) == 0 {
			return unified.Array{}
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(unified.PHPObject, 0, len(keys))
		for _, key := range keys {
			out = append(out, unified.PHPField{Key: key, Value: entryValue(object[key])})
		}
		return out
	}
	if values := unified.Slice(value); values != nil {
		out := make(unified.Array, 0, len(values))
		for _, child := range values {
			out = append(out, entryValue(child))
		}
		return out
	}
	return value
}

func canonicalZero(value any) any {
	if number, ok := value.(json.Number); ok && number == "-0" {
		return json.Number("0")
	}
	if object := unified.Map(value); object != nil {
		for key, child := range object {
			object[key] = canonicalZero(child)
		}
	}
	if values := unified.Slice(value); values != nil {
		for index, child := range values {
			values[index] = canonicalZero(child)
		}
	}
	return value
}

func stripslashes(raw string) string {
	var out bytes.Buffer
	for index := 0; index < len(raw); index++ {
		if raw[index] == '\\' {
			index++
			if index >= len(raw) {
				break
			}
			if raw[index] == '0' {
				out.WriteByte(0)
				continue
			}
		}
		out.WriteByte(raw[index])
	}
	return out.String()
}

func scalarString(value any) (string, error) {
	switch value := value.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	case bool:
		if value {
			return "1", nil
		}
		return "", nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case float64:
		return strconv.FormatFloat(value, 'G', 14, 64), nil
	default:
		return "", fmt.Errorf("kuaishou expected a scalar image or music path")
	}
}
