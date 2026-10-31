// Package weibo currently provides only the frozen PHP-compatible ticket
// protocol. The media relay is a later migration gate, not an enabled route.
package weibo

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

const TicketTTL int64 = 1800

func AllowedURL(raw string) bool {
	if len(raw) > 4096 || strings.Contains(raw, "#") || unified.MediaURL(raw) == "" {
		return false
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return false
		}
	}
	separator := strings.Index(raw, "://")
	if separator < 0 {
		return false
	}
	path := strings.IndexAny(raw[separator+3:], "/?#")
	if path < 0 || raw[separator+3+path] != '/' {
		return false
	}
	u, err := url.Parse(raw[:separator+3+path])
	if err != nil || u.Scheme != "https" || u.Hostname() != "f.video.weibocdn.com" || u.User != nil {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n != 443 {
			return false
		}
	}
	return true
}

func validKey(key string) bool { return len(key) >= 32 && len(key) <= 512 }

// Sign reproduces PHP's ordered JSON bytes and unpadded base64url encoding.
// expires=nil applies the original default TTL; an explicit zero stays invalid.
func Sign(raw, key string, now int64, expires *int64) (string, bool) {
	if !validKey(key) || !AllowedURL(raw) || now > 9223372036854775807-TicketTTL {
		return "", false
	}
	expiry := now + TicketTTL
	if expires != nil && *expires < expiry {
		expiry = *expires
	}
	if expiry <= now {
		return "", false
	}
	bytes, err := unified.PHPJSON(unified.PHPObject{{Key: "v", Value: 1}, {Key: "u", Value: raw}, {Key: "iat", Value: now}, {Key: "exp", Value: expiry}}, true)
	if err != nil {
		return "", false
	}
	payload := base64.RawURLEncoding.EncodeToString(bytes)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(payload))
	return "/api/media/weibo?ticket=" + payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), true
}

var ticketSyntax = regexp.MustCompile(`^([A-Za-z0-9_-]+)\.([A-Za-z0-9_-]{43})$`)
var intSyntax = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)$`)

func wireInt(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok || !intSyntax.MatchString(number.String()) {
		return 0, false
	}
	n, err := number.Int64()
	return n, err == nil
}

// Verify returns the exact decoded ticket payload, including any tolerated
// extra fields; PHP's protocol checks required facts rather than field count.
func Verify(ticket, key string, now int64) (unified.Object, bool) {
	if !validKey(key) || len(ticket) > 8192 {
		return nil, false
	}
	parts := ticketSyntax.FindStringSubmatch(ticket)
	if parts == nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(parts[1]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, false
	}
	bytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	payload, err := unified.Decode(bytes)
	if err != nil {
		return nil, false
	}
	v, vok := wireInt(payload["v"])
	issued, iok := wireInt(payload["iat"])
	expires, eok := wireInt(payload["exp"])
	if !vok || v != 1 || !iok || !eok || !AllowedURL(unified.Str(payload["u"])) || issued > now || expires <= now || expires <= issued || issued > 9223372036854775807-TicketTTL || expires > issued+TicketTTL {
		return nil, false
	}
	return payload, true
}
