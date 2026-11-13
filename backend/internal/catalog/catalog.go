// Package catalog owns registration facts, not parser or media-selection rules.
package catalog

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Brilliant666/DokiTool-Go/backend/config"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

type Catalog struct {
	rows []unified.Object
	byID map[string]unified.Object
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
var hostName = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

func Load() (*Catalog, error) {
	c, err := Decode(config.Platforms())
	if err != nil {
		return nil, err
	}
	if err := c.AddExactDomains("zuiyou", "share.xiaochuankeji.cn"); err != nil {
		return nil, err
	}
	return c, nil
}

// XPlatform returns the optional registry entry for X / Twitter.
func XPlatform() unified.Object {
	return unified.Object{
		"id":           "x",
		"name":         "X",
		"site_url":     "https://x.com/",
		"icon":         "x",
		"capabilities": unified.Array{"video", "image"},
		"domains":      unified.Array{"x.com", "twitter.com"},
	}
}

// YouTubePlatform returns the optional registry entry for YouTube.
func YouTubePlatform() unified.Object {
	return unified.Object{
		"id":           "youtube",
		"name":         "YouTube",
		"site_url":     "https://www.youtube.com/",
		"icon":         "youtube",
		"capabilities": unified.Array{"video"},
		"domains":      unified.Array{"youtube.com", "youtu.be"},
	}
}

// InstagramPlatform returns the optional registry entry for Instagram.
func InstagramPlatform() unified.Object {
	return unified.Object{
		"id":           "instagram",
		"name":         "Instagram",
		"site_url":     "https://www.instagram.com/",
		"icon":         "instagram",
		"capabilities": unified.Array{"video", "image"},
		"domains":      unified.Array{"instagram.com"},
	}
}

func Decode(data []byte) (*Catalog, error) {
	value, err := unified.DecodeValue(data)
	if err != nil {
		return nil, err
	}
	rows, ok := value.([]any)
	if !ok || len(rows) == 0 {
		return nil, fmt.Errorf("platform registry must be a nonempty array")
	}
	c := &Catalog{byID: make(map[string]unified.Object)}
	for _, value := range rows {
		if err := c.Register(unified.Map(value)); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Register validates and appends an optional platform entry to the runtime catalog.
func (c *Catalog) Register(row unified.Object) error {
	if c == nil {
		return fmt.Errorf("missing catalog")
	}
	if c.byID == nil {
		c.byID = make(map[string]unified.Object)
	}
	cloned := unified.Clone(row)
	id := unified.Str(cloned["id"])
	if !identifier.MatchString(id) || c.byID[id] != nil {
		return fmt.Errorf("invalid or duplicate platform id %q", id)
	}
	for _, key := range []string{"name", "site_url", "icon"} {
		if unified.Str(cloned[key]) == "" {
			return fmt.Errorf("platform %s lacks %s", id, key)
		}
	}
	if (len(unified.Slice(cloned["domains"])) == 0 && len(unified.Slice(cloned["exact_domains"])) == 0) || len(unified.Slice(cloned["capabilities"])) == 0 {
		return fmt.Errorf("platform %s lacks domains or capabilities", id)
	}
	for _, domain := range unified.Slice(cloned["domains"]) {
		name := unified.Str(domain)
		if !hostName.MatchString(name) || name != strings.ToLower(name) {
			return fmt.Errorf("invalid platform domain")
		}
	}
	for _, domain := range unified.Slice(cloned["exact_domains"]) {
		name := unified.Str(domain)
		if !hostName.MatchString(name) || name != strings.ToLower(name) {
			return fmt.Errorf("invalid platform exact domain")
		}
	}
	c.rows = append(c.rows, cloned)
	c.byID[id] = cloned
	return nil
}

// AddExactDomains appends exact-match hostnames to an already registered platform
// without publishing private domain metadata in Envelope() or allowing subdomains.
func (c *Catalog) AddExactDomains(id string, domains ...string) error {
	if c == nil || c.byID[id] == nil {
		return fmt.Errorf("unknown platform id %q", id)
	}
	row := c.byID[id]
	existing := unified.Slice(row["exact_domains"])
	for _, d := range domains {
		name := strings.ToLower(strings.TrimSpace(d))
		if !hostName.MatchString(name) {
			return fmt.Errorf("invalid platform exact domain %q", d)
		}
		existing = append(existing, name)
	}
	row["exact_domains"] = existing
	return nil
}

func (c *Catalog) Has(id string) bool { return c.byID[id] != nil }

// Envelope returns only existing public fields, in the registry's original order.
func (c *Catalog) Envelope() unified.Object {
	rows := make(unified.Array, 0, len(c.rows))
	for _, row := range c.rows {
		public := unified.Object{}
		for _, key := range []string{"id", "name", "site_url", "icon", "capabilities"} {
			public[key] = unified.CloneValue(row[key])
		}
		rows = append(rows, public)
	}
	return unified.Object{"schema_version": unified.SchemaVersion, "code": "OK", "data": unified.Object{"platforms": rows}}
}

func parseInputHost(raw string) (string, bool) {
	if unified.MediaURL(raw) == "" {
		return "", false
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return "", false
		}
	}
	// PHP accepts opaque percent bytes in paths. Parse only the already
	// validated authority for dispatch; retain the complete input for adapters.
	separator := strings.Index(raw, "://")
	if separator < 0 {
		return "", false
	}
	end := len(raw)
	if suffix := strings.IndexAny(raw[separator+3:], "/?#"); suffix >= 0 {
		end = separator + 3 + suffix
	}
	u, err := url.Parse(raw[:end])
	if err != nil || u.User != nil || u.Opaque != "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if !hostName.MatchString(host) || net.ParseIP(host) != nil {
		return "", false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || (scheme == "https" && n != 443) || (scheme == "http" && n != 80) {
			return "", false
		}
	}
	return host, true
}

// ValidInputURL reports whether raw is a syntactically valid HTTP/HTTPS domain URL
// without userinfo or non-default port, independent of platform registration.
func (c *Catalog) ValidInputURL(raw string) bool {
	_, ok := parseInputHost(raw)
	return ok
}

// PlatformForURL matches the host, never a path/query substring or a userinfo field.
func (c *Catalog) PlatformForURL(raw string) string {
	host, ok := parseInputHost(raw)
	if !ok {
		return ""
	}
	for _, row := range c.rows {
		for _, value := range unified.Slice(row["exact_domains"]) {
			if host == unified.Str(value) {
				return unified.Str(row["id"])
			}
		}
		for _, value := range unified.Slice(row["domains"]) {
			domain := unified.Str(value)
			if host == domain || strings.HasSuffix(host, "."+domain) {
				return unified.Str(row["id"])
			}
		}
	}
	return ""
}
