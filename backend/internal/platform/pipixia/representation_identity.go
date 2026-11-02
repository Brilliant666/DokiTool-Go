package pipixia

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

var ppxObjectHost = regexp.MustCompile(`^v[0-9]+-cdn-tos\.ppxvod\.com$`)
var ppxObjectPath = regexp.MustCompile(`^/[a-f0-9]{32}/[a-f0-9]{8}(/video/tos/cn/tos-cn-ve-[a-z0-9-]+/[A-Za-z0-9_-]+/?)$`)

// Fresh Pipixia pages renew ft/dy_q/l; a sampled btag change also yielded
// byte-identical complete MP4s at the same TOS object. Preserve all other raw
// query bytes, including ordering, encoding and duplicates. Missing/ambiguous
// renewal fields keep the original exact-query identity.
func ppxTransportQuery(raw string) (string, bool) {
	seen := map[string]bool{}
	btag := false
	kept := make([]string, 0)
	for _, part := range strings.Split(raw, "&") {
		key, value, hasValue := strings.Cut(part, "=")
		if key == "ft" || key == "dy_q" || key == "l" {
			if !hasValue || value == "" || seen[key] {
				return raw, false
			}
			seen[key] = true
			continue
		}
		if key == "btag" {
			if btag || !hasValue || len(value) != 14 || strings.Trim(value, "0123456789abcdefABCDEF") != "" {
				return raw, false
			}
			btag = true
			continue
		}
		kept = append(kept, part)
	}
	if len(seen) != 3 {
		return raw, false
	}
	mode := "\x00pipixia-transport-v1:"
	if btag {
		mode = "\x00pipixia-transport-btag-v1:"
	}
	// Distinct marker prevents an exact query (including missing btag) from
	// aliasing a normalized one. Literal NUL is invalid in a URL.
	return mode + strings.Join(kept, "&"), true
}

func ppxObjectIdentity(raw string) (identity, host string, rotating bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" ||
		(u.Port() != "" && u.Port() != "443") {
		return "", "", false
	}
	host = strings.ToLower(u.Hostname())
	if !ppxObjectHost.MatchString(host) {
		return "", "", false
	}
	path := ppxObjectPath.FindStringSubmatch(u.EscapedPath())
	if path == nil {
		return "", "", false
	}
	query, rotating := ppxTransportQuery(u.RawQuery)
	identity = "pipixia:tos:" + unified.PHPIdentityJSON(unified.Array{path[1], query, u.ForceQuery}, false)
	return identity, host, rotating
}

// representationIdentity identifies one file across backup CDN membership and
// the three observed transport-only query renewals, not across file changes.
// An unrecognized or mixed object retains the previous source-set behavior.
func representationIdentity(urls []string) string {
	identity := ""
	for _, raw := range urls {
		key, _, _ := ppxObjectIdentity(raw)
		if key == "" || (identity != "" && identity != key) {
			return ""
		}
		identity = key
	}
	return identity
}

// Keep a user's explicit CDN mirror choice when that same host and object
// returns with fresh signing/query credentials. A missing mirror, changed file
// or changed content-affecting query still has a different source ID.
func ppxRenewedSourceIdentity(raw, group string) string {
	key, host, rotating := ppxObjectIdentity(raw)
	if !rotating || group == "" || group != key {
		return ""
	}
	return "pipixia:cdn-source:" + unified.PHPIdentityJSON(unified.Array{host, key}, false)
}

// Preserve at most two extra credential observations inside an already proven
// logical CDN source. Do not reorder primary URLs, infer freshness, or combine
// unknown representations. Every candidate gets the same ordered pool, so the
// existing whole-observation merge/ranking remains authoritative.
func preserveCredentialFallbacks(candidates unified.Array) {
	pools := map[string][]string{}
	for _, value := range candidates {
		candidate := unified.Map(value)
		key := unified.Str(candidate["private_source_identity"])
		if key == "" {
			continue
		}
		raw := unified.Str(candidate["url"])
		pool := pools[key]
		found := false
		for _, prior := range pool {
			if prior == raw {
				found = true
			}
		}
		if !found && len(pool) < 3 {
			pools[key] = append(pool, raw)
		}
	}
	for _, value := range candidates {
		candidate := unified.Map(value)
		key := unified.Str(candidate["private_source_identity"])
		alternatives := unified.Array{}
		for _, raw := range pools[key] {
			if raw != candidate["url"] && len(alternatives) < 2 {
				alternatives = append(alternatives, raw)
			}
		}
		if len(alternatives) > 0 {
			candidate["fallback_urls"] = alternatives
		}
	}
}
