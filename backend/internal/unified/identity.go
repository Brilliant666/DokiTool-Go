package unified

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const phpTrimCharacters = " \n\r\t\v\x00"

// Text preserves the PHP private-boundary string/int distinction. In particular
// a floating-point JSON value is not an integer ID even when its value is 30.
func Text(value any) (string, bool) {
	if text, ok := value.(string); ok {
		return text, strings.Trim(text, phpTrimCharacters) != ""
	}
	var text string
	switch value := value.(type) {
	case int:
		text = strconv.Itoa(value)
	case int64:
		text = strconv.FormatInt(value, 10)
	case int32:
		text = strconv.FormatInt(int64(value), 10)
	case uint:
		if uint64(value) > math.MaxInt64 {
			return "", false
		}
		text = strconv.FormatUint(uint64(value), 10)
	case uint64:
		if value > math.MaxInt64 {
			return "", false
		}
		text = strconv.FormatUint(value, 10)
	case json.Number:
		n, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return "", false
		}
		text = strconv.FormatInt(n, 10)
	default:
		return "", false
	}
	return text, true
}

type rawURLParts struct {
	scheme, host, path string
	port               *int
	query, fragment    *string
	userInfo           bool
}

var rawURLPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*)://([^/?#]*)([^?#]*)(?:\?([^#]*))?(?:#(.*))?$`)
var domainLabelPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
var douyinSignedPath = regexp.MustCompile(`^/[a-f0-9]{32}/[a-f0-9]{8}(/video/tos/cn/tos-cn-[a-z0-9-]+/[A-Za-z0-9_-]+(?:/media-video-(?:hvc1|avc1))?/?)$`)

// Parse only URL components, retaining raw query/path bytes. net/url.Path is
// decoded and would wrongly equate e.g. /aweme%2fv1/play with /aweme/v1/play.
func splitRawURL(raw string) (rawURLParts, bool) {
	match := rawURLPattern.FindStringSubmatchIndex(raw)
	if match == nil {
		return rawURLParts{}, false
	}
	part := func(i int) string {
		if match[2*i] < 0 {
			return ""
		}
		return raw[match[2*i]:match[2*i+1]]
	}
	p := rawURLParts{scheme: part(1), path: part(3)}
	if match[8] >= 0 {
		query := part(4)
		p.query = &query
	}
	if match[10] >= 0 {
		fragment := part(5)
		p.fragment = &fragment
	}
	authority := part(2)
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		p.userInfo = true
		authority = authority[at+1:]
	}
	host, port := authority, ""
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return rawURLParts{}, false
		}
		host = authority[:end+1]
		if rest := authority[end+1:]; rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return rawURLParts{}, false
			}
			port = rest[1:]
		}
	} else if colon := strings.LastIndexByte(authority, ':'); colon >= 0 {
		host, port = authority[:colon], authority[colon+1:]
		if strings.ContainsRune(host, ':') {
			return rawURLParts{}, false
		}
	}
	if host == "" {
		return rawURLParts{}, false
	}
	if port != "" {
		for _, c := range port {
			if c < '0' || c > '9' {
				return rawURLParts{}, false
			}
		}
		n, err := strconv.Atoi(port)
		if err != nil || n > 65535 {
			return rawURLParts{}, false
		}
		p.port = &n
	}
	p.host = host
	return p, true
}

// MediaURL mirrors the existing private media URL boundary. This is format
// validation, not permission for a server-side request; fetch policies own that.
func MediaURL(value any) string {
	raw, ok := value.(string)
	if !ok {
		return ""
	}
	raw = strings.Trim(raw, phpTrimCharacters)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] <= 0x20 || raw[i] >= 0x7f {
			return ""
		}
	}
	p, ok := splitRawURL(raw)
	if !ok || p.userInfo || (!strings.EqualFold(p.scheme, "http") && !strings.EqualFold(p.scheme, "https")) {
		return ""
	}
	if strings.HasPrefix(p.host, "[") {
		if net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(p.host, "["), "]")) == nil {
			return ""
		}
	} else {
		host := strings.TrimSuffix(p.host, ".")
		if len(host) > 253 {
			return ""
		}
		for _, label := range strings.Split(host, ".") {
			if !domainLabelPattern.MatchString(label) {
				return ""
			}
		}
	}
	return raw
}

func rawPercentDecode(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '%' && i+2 < len(value) {
			if n, err := strconv.ParseUint(value[i+1:i+3], 16, 8); err == nil {
				out.WriteByte(byte(n))
				i += 2
				continue
			}
		}
		out.WriteByte(value[i])
	}
	return out.String()
}

// SourceIdentity removes only the credential fields with existing provider
// evidence. The actual source URL remains untouched by this identity operation.
func SourceIdentity(raw string) string {
	p, ok := splitRawURL(raw)
	if !ok {
		return raw
	}
	host := strings.ToLower(p.host)
	if p.scheme == "https" && !p.userInfo && hasString([]string{"v3-web.douyinvod.com", "v26-web.douyinvod.com", "v26-luna.douyinvod.com", "v11-weba.douyinvod.com"}, host) {
		if path := douyinSignedPath.FindStringSubmatch(p.path); path != nil {
			var port, query, fragment any
			if p.port != nil {
				port = *p.port
			}
			if p.query != nil {
				query = *p.query
			}
			if p.fragment != nil {
				fragment = *p.fragment
			}
			return phpJSON(Array{"douyin-cdn", host, port, path[1], query, fragment}, false, true)
		}
	}
	credentials := []string(nil)
	if hasString([]string{"www.douyin.com", "aweme.snssdk.com", "api.amemv.com"}, host) && hasString([]string{"/aweme/v1/play", "/aweme/v1/play/dash"}, strings.TrimRight(p.path, "/")) {
		credentials = []string{"sign", "biz_sign"}
	} else if host == "f.video.weibocdn.com" {
		credentials = []string{"Expires", "ssig", "KID"}
	} else if host == "bilivideo.com" || strings.HasSuffix(host, ".bilivideo.com") {
		credentials = []string{"deadline", "upsig"}
	}
	if len(credentials) == 0 || p.query == nil {
		return raw
	}
	query := make([]string, 0)
	for _, entry := range strings.Split(*p.query, "&") {
		key := strings.SplitN(entry, "=", 2)[0]
		if !hasString(credentials, rawPercentDecode(key)) {
			query = append(query, entry)
		}
	}
	base := strings.SplitN(raw, "?", 2)[0]
	if len(query) > 0 {
		base += "?" + strings.Join(query, "&")
	}
	if p.fragment != nil {
		base += "#" + *p.fragment
	}
	return base
}

func identityDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:24]
}

// PHPObject preserves the insertion order of a PHP associative array. It is
// used only for existing signed/hashed bytes, not for ordinary wire encoding.
type PHPField struct {
	Key   string
	Value any
}
type PHPObject []PHPField

// PHPJSON emits PHP json_encode default Unicode/number spelling, with the one
// requested slash flag. Unordered Go maps are rejected at this byte boundary.
func PHPJSON(value any, unescapedSlashes bool) ([]byte, error) {
	var ordered func(any) bool
	ordered = func(value any) bool {
		switch v := value.(type) {
		case Object:
			return false
		case PHPObject:
			for _, field := range v {
				if !ordered(field.Value) {
					return false
				}
			}
		default:
			if array := Slice(value); array != nil {
				for _, child := range array {
					if !ordered(child) {
						return false
					}
				}
			}
		}
		return true
	}
	if !ordered(value) {
		return nil, fmt.Errorf("PHP identity object requires explicit field order")
	}
	text := phpJSON(value, false, unescapedSlashes)
	if text == "" {
		return nil, fmt.Errorf("value cannot be encoded as PHP identity JSON")
	}
	return []byte(text), nil
}

// PHPIdentityJSON is for the ordered array identities used by unified-v1.
// PHP's default JSON escapes Unicode and slashes, unlike ordinary Go Marshal.
// Object-valued IDs must provide their insertion order explicitly before use;
// the identities in this package contain arrays and scalar values only.
func PHPIdentityJSON(value any, unescapedSlashes bool) string {
	data, _ := PHPJSON(value, unescapedSlashes)
	return string(data)
}

func phpJSON(value any, unescapedUnicode, unescapedSlashes bool) string {
	var out strings.Builder
	var appendValue func(any) bool
	appendString := func(text string) bool {
		if !utf8.ValidString(text) {
			return false
		}
		out.WriteByte('"')
		for _, r := range text {
			switch r {
			case '"', '\\':
				out.WriteByte('\\')
				out.WriteRune(r)
			case '/':
				if !unescapedSlashes {
					out.WriteByte('\\')
				}
				out.WriteRune(r)
			case '\b':
				out.WriteString(`\b`)
			case '\f':
				out.WriteString(`\f`)
			case '\n':
				out.WriteString(`\n`)
			case '\r':
				out.WriteString(`\r`)
			case '\t':
				out.WriteString(`\t`)
			default:
				if r < 0x20 || r == 0x2028 || r == 0x2029 || (!unescapedUnicode && r > 0x7f) {
					for _, unit := range utf16.Encode([]rune{r}) {
						out.WriteString(`\u`)
						digits := strconv.FormatUint(uint64(unit), 16)
						out.WriteString(strings.Repeat("0", 4-len(digits)))
						out.WriteString(digits)
					}
				} else {
					out.WriteRune(r)
				}
			}
		}
		out.WriteByte('"')
		return true
	}
	appendValue = func(value any) bool {
		if value == nil {
			out.WriteString("null")
			return true
		}
		switch v := value.(type) {
		case string:
			return appendString(v)
		case bool:
			out.WriteString(strconv.FormatBool(v))
			return true
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return false
			}
			out.WriteString(phpFloatJSON(v))
			return true
		case float32:
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return false
			}
			out.WriteString(phpFloatJSON(float64(v)))
			return true
		case PHPObject:
			out.WriteByte('{')
			for i, field := range v {
				if i > 0 {
					out.WriteByte(',')
				}
				if !appendString(field.Key) {
					return false
				}
				out.WriteByte(':')
				if !appendValue(field.Value) {
					return false
				}
			}
			out.WriteByte('}')
			return true
		case []any:
			out.WriteByte('[')
			for i, child := range v {
				if i > 0 {
					out.WriteByte(',')
				}
				if !appendValue(child) {
					return false
				}
			}
			out.WriteByte(']')
			return true
		case Object:
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			out.WriteByte('{')
			for i, key := range keys {
				if i > 0 {
					out.WriteByte(',')
				}
				if !appendString(key) {
					return false
				}
				out.WriteByte(':')
				if !appendValue(v[key]) {
					return false
				}
			}
			out.WriteByte('}')
			return true
		}
		if array := Slice(value); array != nil {
			out.WriteByte('[')
			for i, child := range array {
				if i > 0 {
					out.WriteByte(',')
				}
				if !appendValue(child) {
					return false
				}
			}
			out.WriteByte(']')
			return true
		}
		if text, ok := numberText(value); ok {
			if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
				out.WriteString(strconv.FormatInt(integer, 10))
				return true
			}
			n, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				return false
			}
			out.WriteString(phpFloatJSON(n))
			return true
		}
		return false
	}
	if !appendValue(value) {
		return ""
	} // json_encode errors return false, which casts to an empty string in PHP.
	return out.String()
}

func phpFloatJSON(value float64) string {
	if value == 0 {
		if math.Signbit(value) {
			return "-0"
		}
		return "0"
	}
	if absolute := math.Abs(value); absolute >= 0.0001 && absolute < 1e17 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	parts := strings.Split(strconv.FormatFloat(value, 'e', -1, 64), "e")
	if !strings.ContainsRune(parts[0], '.') {
		parts[0] += ".0"
	}
	exponent, _ := strconv.Atoi(parts[1])
	sign := "+"
	if exponent < 0 {
		sign = "-"
		exponent = -exponent
	}
	return parts[0] + "e" + sign + strconv.Itoa(exponent)
}
