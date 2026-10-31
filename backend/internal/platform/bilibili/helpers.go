package bilibili

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type object = unified.Object
type array = unified.Array

func phpArray(value any) bool { return unified.Map(value) != nil || unified.Slice(value) != nil }

func pick(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
func first(value any) any {
	if values := unified.Slice(value); len(values) > 0 {
		return values[0]
	}
	return nil
}
func child(value any, fields ...string) any {
	for _, field := range fields {
		value = unified.Map(value)[field]
	}
	return value
}

func intValue(value any) int64 {
	if n, ok := unified.PHPInt(value); ok {
		return n
	}
	if value == true {
		return 1
	}
	return 0
}

func strictInteger(value any, wanted int64) bool {
	switch value := value.(type) {
	case int:
		return int64(value) == wanted
	case int64:
		return value == wanted
	case json.Number:
		n, err := value.Int64()
		return err == nil && n == wanted
	}
	return false
}
func anyStrictInteger(value any, wanted ...int64) bool {
	for _, n := range wanted {
		if strictInteger(value, n) {
			return true
		}
	}
	return false
}

func truthy(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != "" && value != "0"
	}
	if fields := unified.Map(value); fields != nil {
		return len(fields) > 0
	}
	if values := unified.Slice(value); values != nil {
		return len(values) > 0
	}
	if n, ok := unified.Numeric(value); ok {
		return n != 0
	}
	return true
}

func stringValue(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		if value {
			return "1"
		}
		return ""
	case json.Number:
		if _, err := value.Int64(); err == nil {
			return value.String()
		}
		if n, err := value.Float64(); err == nil {
			return floatString(n)
		}
		return value.String()
	case float64:
		return floatString(value)
	}
	if unified.Map(value) != nil || unified.Slice(value) != nil {
		return "Array"
	}
	return fmt.Sprint(value)
}

func floatString(value float64) string {
	text := strconv.FormatFloat(value, 'G', 14, 64)
	if i := strings.IndexByte(text, 'E'); i >= 0 {
		mantissa, exponent := text[:i], text[i+1:]
		if !strings.Contains(mantissa, ".") {
			mantissa += ".0"
		}
		sign := ""
		if strings.HasPrefix(exponent, "+") || strings.HasPrefix(exponent, "-") {
			sign, exponent = exponent[:1], exponent[1:]
		}
		exponent = strings.TrimLeft(exponent, "0")
		if exponent == "" {
			exponent = "0"
		}
		text = mantissa + "E" + sign + exponent
	}
	return text
}

// The legacy entrypoint JSON_NUMERIC_CHECK encoding is part of the existing
// parser/gateway boundary. Preserve it before adapting private fields, without
// routing integer tokens through float64 or changing upstream response bytes.
func legacyWire(value object) (object, error) {
	encoded, err := unified.PHPJSON(legacyPHPValue(value), true)
	if err != nil {
		return nil, err
	}
	decoded, err := unified.Decode(encoded)
	if err != nil {
		return nil, err
	}
	return unified.Map(canonicalZero(decoded)), nil
}

func legacyPHPValue(value any) any {
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
	if fields := unified.Map(value); fields != nil {
		if len(fields) == 0 {
			return array{}
		}
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(unified.PHPObject, 0, len(keys))
		for _, key := range keys {
			out = append(out, unified.PHPField{Key: key, Value: legacyPHPValue(fields[key])})
		}
		return out
	}
	if values := unified.Slice(value); values != nil {
		out := make(array, 0, len(values))
		for _, child := range values {
			out = append(out, legacyPHPValue(child))
		}
		return out
	}
	return value
}

func canonicalZero(value any) any {
	if value == json.Number("-0") {
		return json.Number("0")
	}
	if fields := unified.Map(value); fields != nil {
		for key, child := range fields {
			fields[key] = canonicalZero(child)
		}
	}
	if values := unified.Slice(value); values != nil {
		for index, child := range values {
			values[index] = canonicalZero(child)
		}
	}
	return value
}
