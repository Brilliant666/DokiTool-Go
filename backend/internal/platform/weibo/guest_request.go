package weibo

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/url"
	"strconv"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// guestExchange is a narrow, injected interface for the frozen PHP request
// control flow. The eventual HTTP adapter owns the 35-second budget, bounded
// fetch, cookie jar, and fixed-host policy; this layer never accepts user URLs.
type guestExchange func(context.Context, string, *string, string) (unified.Object, error)

type guestSession struct {
	exchange  guestExchange
	attempted bool
	nonce     func() string
}

func newGuestSession(exchange guestExchange) *guestSession {
	return &guestSession{exchange: exchange, nonce: secureGuestNonce}
}

// Only the visitor cache buster is random. It cannot enter media identities,
// signatures, or an expected oracle. Tests normalize this non-identity field.
func secureGuestNonce() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "0.5"
	}
	return strconv.FormatFloat(float64(n.Int64()+1)/1000000, 'f', -1, 64)
}

func (s *guestSession) request(ctx context.Context, address string, post *string, referer string) unified.Object {
	if ctx.Err() != nil || s == nil || s.exchange == nil {
		return unified.Object{"error": true, "status": 0, "body": "", "url": address}
	}
	result, err := s.exchange(ctx, address, post, referer)
	if err != nil || result == nil {
		return unified.Object{"error": true, "status": 0, "body": "", "url": address}
	}
	return result
}

func guestJSONObject(body string) any {
	decoded, err := unified.DecodeValue([]byte(body))
	if err != nil || (unified.Map(decoded) == nil && unified.Slice(decoded) == nil) {
		return nil
	}
	return phpGuestAssociative(decoded)
}

// startGuest mirrors weiboStartGuest. No CAPTCHA or account login is attempted.
// Both URLs are fixed official endpoints, not derived from the user's link.
func (s *guestSession) startGuest(ctx context.Context, referer string) bool {
	const fingerprint = `{"os":"1","browser":"Chrome131,0,0,0","fonts":"undefined","screenInfo":"1920*1080*24","plugins":""}`
	post := "cb=gen_callback&fp=" + url.QueryEscape(fingerprint)
	first := s.request(ctx, "https://passport.weibo.com/visitor/genvisitor", &post, referer)
	if legacyTransportFailure(first) != nil {
		return false
	}
	guest := unified.Map(legacyJSONP(unified.Str(first["body"]), "gen_callback"))
	data := unified.Map(guest["data"])
	tid, valid := data["tid"].(string)
	if phpGuestNumber(guest["retcode"]) != 20000000 || !valid || len(tid) == 0 || len(tid) > 512 {
		return false
	}
	w := "2"
	if phpGuestTruthy(data["new_tid"]) {
		w = "3"
	}
	confidence := int64(100)
	if value, exists := data["confidence"]; exists && value != nil {
		confidence = phpGuestNumber(value)
	}
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 100 {
		confidence = 100
	}
	nonce := secureGuestNonce()
	if s.nonce != nil {
		nonce = s.nonce()
	}
	visit := "https://passport.weibo.com/visitor/visitor?a=incarnate&t=" + url.QueryEscape(tid) +
		"&w=" + w + "&c=" + fmt.Sprintf("%03d", confidence) +
		"&gc=&cb=cross_domain&from=weibo&_rand=" + nonce
	second := s.request(ctx, visit, nil, referer)
	if legacyTransportFailure(second) != nil {
		return false
	}
	callback := unified.Map(legacyJSONP(unified.Str(second["body"]), "cross_domain"))
	return phpGuestNumber(callback["retcode"]) == 20000000
}

// jsonRequest mirrors weiboJsonRequest: a single guest initialization at most
// across both the component and the optional status request. It never silently
// retries transport failures, second challenges or unrecognizable JSON.
func (s *guestSession) jsonRequest(ctx context.Context, address string, post *string, referer string) unified.Object {
	first := s.request(ctx, address, post, referer)
	if failure := legacyTransportFailure(first); failure != nil {
		return unified.Object{"failure": failure}
	}
	if legacyIsVisitor(first) {
		if s.attempted {
			return unified.Object{"failure": unified.Object{"code": 503, "msg": "微博匿名访客会话仍受限制，请稍后重试"}}
		}
		s.attempted = true
		guestReferer := "https://weibo.com/"
		if value, present := first["url"]; present && value != nil {
			guestReferer = unified.Str(value)
		}
		if !s.startGuest(ctx, guestReferer) {
			return unified.Object{"failure": unified.Object{"code": 503, "msg": "微博匿名访客会话暂不可用，请稍后重试"}}
		}
		first = s.request(ctx, address, post, referer)
		if failure := legacyTransportFailure(first); failure != nil {
			return unified.Object{"failure": failure}
		}
		if legacyIsVisitor(first) {
			return unified.Object{"failure": unified.Object{"code": 503, "msg": "微博匿名访客会话仍受限制，请稍后重试"}}
		}
	}
	if decoded := guestJSONObject(unified.Str(first["body"])); decoded != nil {
		return unified.Object{"json": decoded}
	}
	return unified.Object{"failure": unified.Object{"code": 502, "msg": "微博返回了无法识别的接口响应，请稍后重试"}}
}
