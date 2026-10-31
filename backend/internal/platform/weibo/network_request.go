package weibo

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/fetch"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

// Network requests are one parsing operation's ephemeral guest session. The
// caller supplies the 35-second total parse context; metadata requests retain
// the 20-second per-hop cap and the existing bound DNS/socket/TLS fetch layer.
// No request-scoped cookies or user-supplied URLs escape this session.
type guestHTTP struct {
	executor fetch.Executor
	jar      *cookiejar.Jar
}

func newNetworkGuestSession(executor fetch.Executor) *guestSession {
	jar, _ := cookiejar.New(nil)
	return newGuestSession((&guestHTTP{executor: executor, jar: jar}).exchange)
}

const weiboUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// This ordered list comes from the frozen PHP getRequestHeaders() product helper.
func officialWeiboHeaders(referer string) []string {
	return []string{"Referer: " + referer, "Accept: application/json, text/plain, */*", "User-Agent: " + weiboUserAgent}
}

func weiboMetadataURL(raw string) bool {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\\\r\n\t ") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawFragment != "" || u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	switch u.Hostname() {
	case "weibo.com", "passport.weibo.com":
		return true
	}
	return false
}

func weiboFirstRequestURL(raw string, post *string) bool {
	if !weiboMetadataURL(raw) {
		return false
	}
	u, _ := url.Parse(raw)
	switch u.Hostname() + u.EscapedPath() {
	case "weibo.com/ajax/statuses/show", "passport.weibo.com/visitor/visitor":
		return post == nil
	case "weibo.com/tv/api/component", "passport.weibo.com/visitor/genvisitor":
		return post != nil
	}
	return false
}

// The jar is per parse invocation, and cannot accept a public-suffix cookie or
// a cookie for an unrelated host. The ordinary fetch layer does not manage
// cookies; they are deliberately forwarded on each individually approved hop.
func safeWeiboCookies(headers http.Header) []*http.Cookie {
	response := http.Response{Header: headers}
	var accepted []*http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Domain != "" {
			domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
			if domain != "weibo.com" && domain != "passport.weibo.com" {
				continue
			}
		}
		accepted = append(accepted, cookie)
	}
	return accepted
}

func (g *guestHTTP) exchange(ctx context.Context, address string, post *string, referer string) (unified.Object, error) {
	if g == nil || g.executor == nil || g.jar == nil || !weiboFirstRequestURL(address, post) || !weiboMetadataURL(referer) {
		return nil, fetch.ErrPolicy
	}
	method := http.MethodGet
	if post != nil {
		method = http.MethodPost
	}
	body := []byte(nil)
	if post != nil {
		body = []byte(*post)
	}
	current := address
	for hop := 0; hop <= 3; hop++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !weiboMetadataURL(current) {
			return nil, fetch.ErrPolicy
		}
		budget := 20 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return nil, context.DeadlineExceeded
			}
			if remaining < budget {
				budget = remaining
			}
		}
		target, _ := url.Parse(current)
		headers := make(http.Header)
		for _, entry := range officialWeiboHeaders(referer) {
			key, value, _ := strings.Cut(entry, ": ")
			headers.Set(key, value)
		}
		if method == http.MethodPost {
			headers.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookies := g.jar.Cookies(target); len(cookies) != 0 {
			request := &http.Request{Header: make(http.Header)}
			for _, cookie := range cookies {
				request.AddCookie(cookie)
			}
			headers.Set("Cookie", request.Header.Get("Cookie"))
		}
		reply, err := g.executor.Do(ctx, fetch.Request{
			URL: current, Method: method, Header: headers, Body: body,
			Policy: fetch.Policy{
				Purpose: "weibo-metadata", AllowedURL: weiboMetadataURL,
				Timeout: budget, MaxBytes: 2 << 20, FollowRedirects: false,
			},
		})
		if err != nil {
			return nil, err
		}
		g.jar.SetCookies(target, safeWeiboCookies(reply.Header))
		if reply.StatusCode == http.StatusMovedPermanently || reply.StatusCode == http.StatusFound || reply.StatusCode == http.StatusSeeOther || reply.StatusCode == http.StatusTemporaryRedirect || reply.StatusCode == http.StatusPermanentRedirect {
			if hop == 3 {
				return nil, fetch.ErrRedirect
			}
			locations := reply.Header.Values("Location")
			if len(locations) != 1 || locations[0] == "" {
				return nil, fetch.ErrRedirect
			}
			redirect, err := url.Parse(locations[0])
			if err != nil {
				return nil, fetch.ErrRedirect
			}
			next := target.ResolveReference(redirect).String()
			if !weiboMetadataURL(next) {
				return nil, fetch.ErrPolicy
			}
			current = next
			if reply.StatusCode <= http.StatusSeeOther && method == http.MethodPost {
				method, body = http.MethodGet, nil
			}
			continue
		}
		return unified.Object{"body": string(reply.Body), "error": false, "status": reply.StatusCode, "url": current}, nil
	}
	return nil, errors.New("unreachable weibo redirect state")
}
