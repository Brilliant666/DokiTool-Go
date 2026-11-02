// Package catalog owns registration facts, not parser or media-selection rules.
package catalog

import (
	"fmt"
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

func Load() (*Catalog, error) { return Decode(config.Platforms()) }

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
		row := unified.Map(value)
		id := unified.Str(row["id"])
		if !identifier.MatchString(id) || c.byID[id] != nil {
			return nil, fmt.Errorf("invalid or duplicate platform id %q", id)
		}
		for _, key := range []string{"name", "site_url", "icon"} {
			if unified.Str(row[key]) == "" {
				return nil, fmt.Errorf("platform %s lacks %s", id, key)
			}
		}
		if len(unified.Slice(row["domains"])) == 0 || len(unified.Slice(row["capabilities"])) == 0 {
			return nil, fmt.Errorf("platform %s lacks domains or capabilities", id)
		}
		for _, domain := range unified.Slice(row["domains"]) {
			name := unified.Str(domain)
			if !hostName.MatchString(name) || name != strings.ToLower(name) {
				return nil, fmt.Errorf("invalid platform domain")
			}
		}
		c.rows = append(c.rows, row)
		c.byID[id] = row
	}
	return c, nil
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

// PlatformForURL matches the host, never a path/query substring or a userinfo field.
func (c *Catalog) PlatformForURL(raw string) string {
	if unified.MediaURL(raw) == "" {
		return ""
	}
	for _, r := range raw {
		if r <= 0x20 || r >= 0x7f || r == '\\' {
			return ""
		}
	}
	// PHP accepts opaque percent bytes in paths. Parse only the already
	// validated authority for dispatch; retain the complete input for adapters.
	separator := strings.Index(raw, "://")
	if separator < 0 {
		return ""
	}
	end := len(raw)
	if suffix := strings.IndexAny(raw[separator+3:], "/?#"); suffix >= 0 {
		end = separator + 3 + suffix
	}
	u, err := url.Parse(raw[:end])
	if err != nil || u.User != nil || u.Opaque != "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if !hostName.MatchString(host) {
		return ""
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || (scheme == "https" && n != 443) || (scheme == "http" && n != 80) {
			return ""
		}
	}
	for _, row := range c.rows {
		for _, value := range unified.Slice(row["domains"]) {
			domain := unified.Str(value)
			if host == domain || strings.HasSuffix(host, "."+domain) {
				return unified.Str(row["id"])
			}
		}
	}
	return ""
}
