package douyin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type object = unified.Object
type array = unified.Array

const phpWhitespace = " \t\n\r\x00\x0b"

func pick(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func child(value any, fields ...string) any {
	for _, field := range fields {
		value = unified.Map(value)[field]
	}
	return value
}

func first(value any) any {
	if values := unified.Slice(value); len(values) > 0 {
		return values[0]
	}
	return nil
}

func stringValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "1"
		}
		return ""
	case json.Number:
		if _, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return string(v)
		}
		if n, err := v.Float64(); err == nil {
			return phpFloatString(n)
		}
		return string(v)
	case float64:
		return phpFloatString(v)
	case float32:
		return phpFloatString(float64(v))
	}
	if unified.Map(value) != nil || unified.Slice(value) != nil {
		return "Array"
	}
	return fmt.Sprint(value)
}

func phpFloatString(value float64) string {
	// PHP's scalar string conversion uses precision=14. This is deliberately
	// separate from PHPJSON, whose serialization precision drives stable IDs.
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

func scalar(value any) bool {
	switch value.(type) {
	case string, bool, json.Number, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	}
	return false
}

func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return v != "" && v != "0"
	case bool:
		return v
	}
	if values := unified.Slice(value); values != nil {
		return len(values) > 0
	}
	if fields := unified.Map(value); fields != nil {
		return len(fields) > 0
	}
	if n, ok := unified.Numeric(value); ok {
		return n != 0
	}
	return true
}

// ordered preserves PHP array insertion order while values are replaced. The
// private representation hash includes this object before its own hash field.
type ordered struct {
	values object
	keys   []string
}

func newOrdered() *ordered { return &ordered{values: object{}} }

func (o *ordered) set(key string, value any) {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *ordered) unionKnown(values object, order []string) {
	for _, key := range order {
		if value, present := values[key]; present {
			if _, exists := o.values[key]; !exists {
				o.set(key, value)
			}
		}
	}
}

func (o *ordered) phpValue() any {
	if len(o.keys) == 0 {
		return array{}
	}
	fields := make(unified.PHPObject, 0, len(o.keys))
	for _, key := range o.keys {
		fields = append(fields, unified.PHPField{Key: key, Value: phpValue(o.values[key])})
	}
	return fields
}

// Observation metadata is explicitly ksort'ed by the PHP formatter. The known
// facts are scalars; recursive conversion also keeps injected probe fixtures
// representable without letting map iteration order affect a local dedupe key.
func phpValue(value any) any {
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
			out = append(out, unified.PHPField{Key: key, Value: phpValue(fields[key])})
		}
		return out
	}
	if values := unified.Slice(value); values != nil {
		out := make(array, 0, len(values))
		for _, entry := range values {
			out = append(out, phpValue(entry))
		}
		return out
	}
	return value
}

// The old parser emitted json_encode(..., 480), including JSON_NUMERIC_CHECK,
// before the gateway decoded its private response. Reproduce that wire boundary
// here: typed integer/string distinctions entering the adapter must match PHP.
// Hashes above are built before this conversion, exactly as in the PHP parser.
func legacyWireData(value object) (object, error) {
	encoded, err := unified.PHPJSON(legacyPHPValue(value), true)
	if err != nil {
		return nil, err
	}
	decoded, err := unified.Decode(encoded)
	if err != nil {
		return nil, err
	}
	return unified.Map(canonicalPHPZero(decoded)), nil
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

func canonicalPHPZero(value any) any {
	if value == json.Number("-0") {
		return json.Number("0")
	}
	if fields := unified.Map(value); fields != nil {
		for key, child := range fields {
			fields[key] = canonicalPHPZero(child)
		}
	}
	if values := unified.Slice(value); values != nil {
		for index, child := range values {
			values[index] = canonicalPHPZero(child)
		}
	}
	return value
}

func hashPHP(value any) (string, error) {
	encoded, err := unified.PHPJSON(value, false)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func jsonObject(value any) object {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	parsed, err := unified.Decode([]byte(text))
	if err != nil {
		return nil
	}
	return parsed
}

func toHTTPS(raw string) string {
	if strings.HasPrefix(raw, "http://") {
		return "https://" + raw[7:]
	}
	return raw
}

var playHost = regexp.MustCompile(`://([^/]+)`)

func replacePlayHost(raw string) string {
	return playHost.ReplaceAllString(raw, "://v26-luna.douyinvod.com")
}

type queryEntry struct {
	value  string
	scalar bool
}

func formDecode(raw string) string {
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		switch {
		case raw[i] == '+':
			out.WriteByte(' ')
		case raw[i] == '%' && i+2 < len(raw):
			if value, err := strconv.ParseUint(raw[i+1:i+3], 16, 8); err == nil {
				out.WriteByte(byte(value))
				i += 2
			} else {
				out.WriteByte(raw[i])
			}
		default:
			out.WriteByte(raw[i])
		}
	}
	return out.String()
}

func queryEntries(query string) map[string]queryEntry {
	out := map[string]queryEntry{}
	for _, pair := range strings.Split(query, "&") {
		name, rawValue, _ := strings.Cut(pair, "=")
		name = strings.TrimLeft(formDecode(name), " ")
		name, _, _ = strings.Cut(name, "\x00")
		name = strings.NewReplacer(" ", "_", ".", "_").Replace(name)
		value := formDecode(rawValue)
		if bracket := strings.IndexByte(name, '['); bracket >= 0 {
			out[name[:bracket]] = queryEntry{}
		} else {
			out[name] = queryEntry{value: value, scalar: true}
		}
	}
	return out
}

func ensureVideoMIME(raw string) string {
	if raw == "" || !strings.HasPrefix(strings.ToLower(raw), "http") {
		return raw
	}
	base, fragment, hasFragment := strings.Cut(raw, "#")
	if _, query, hasQuery := strings.Cut(base, "?"); hasQuery && query != "" {
		if _, present := queryEntries(query)["mime_type"]; present {
			return raw
		}
	}
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	result := base + separator + "mime_type=video_mp4"
	if hasFragment {
		result += "#" + fragment
	}
	return result
}

func buildOriginalPlayURL(vid string) string {
	if vid == "" || vid == "0" {
		return ""
	}
	return originalEndpoint + "?video_id=" + unified.RFC3986Query(vid) + "&ratio=default&line=0"
}

func normalizeCreateTime(value any) int64 {
	n, ok := unified.Numeric(value)
	if !ok || n <= 0 {
		return 0
	}
	if n >= 1000000000000 {
		n /= 1000
	}
	n = math.Floor(n)
	if n < 1000000000 || n > 4102444800 {
		return 0
	}
	return int64(n)
}

func formatPublishTime(timestamp int64) string {
	if timestamp == 0 {
		return ""
	}
	return time.Unix(timestamp, 0).In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04:05")
}

func formatSizeLabel(value any) string {
	n, ok := unified.Numeric(value)
	if !ok || n <= 0 {
		return ""
	}
	units, unit := []string{"B", "KB", "MB", "GB", "TB"}, 0
	for n >= 1024 && unit < len(units)-1 {
		n /= 1024
		unit++
	}
	if unit == 0 {
		return strconv.FormatFloat(unified.PHPRound(n, 0), 'f', 0, 64) + "B"
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(unified.PHPRound(n, 2), 'f', 2, 64), "0"), ".") + units[unit]
}

func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
