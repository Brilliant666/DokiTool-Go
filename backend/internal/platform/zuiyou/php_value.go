package zuiyou

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func defined(value, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

func arrayObject(value any) unified.Object {
	if fields := unified.Map(value); fields != nil {
		return unified.Clone(fields)
	}
	fields := unified.Object{}
	for index, value := range unified.Slice(value) {
		fields[strconv.Itoa(index)] = unified.CloneValue(value)
	}
	return fields
}

// The original entrypoint encodes with 480, including JSON_NUMERIC_CHECK.
// Preserve that conversion before computing the adapter's item identities.
func legacyWire(value unified.Object) (unified.Object, error) {
	encoded, err := unified.PHPJSON(entryValue(value), true)
	if err != nil {
		return nil, err
	}
	decoded, err := unified.Decode(encoded)
	if err != nil {
		return nil, err
	}
	return unified.Map(canonicalZero(decoded)), nil
}

func entryValue(value any) any {
	if text, ok := value.(string); ok {
		if number, numeric := unified.Numeric(text); numeric {
			trimmed := strings.Trim(text, " \t\r\n\v\f")
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
			return unified.Array{}
		}
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := unified.PHPObject{}
		for _, key := range keys {
			out = append(out, unified.PHPField{Key: key, Value: entryValue(fields[key])})
		}
		return out
	}
	if values := unified.Slice(value); values != nil {
		out := unified.Array{}
		for _, child := range values {
			out = append(out, entryValue(child))
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
