package ytgeneric

import (
	"fmt"
	"strings"
)

var builtinSites = []SiteSpec{
	{
		PlatformID:                   "facebook",
		Name:                         "Facebook",
		SiteURL:                      "https://www.facebook.com/",
		Icon:                         "facebook",
		CatalogDomains:               []string{"www.facebook.com"},
		InputHosts:                   []string{"www.facebook.com"},
		MediaHostSuffix:              []string{".fbcdn.net"},
		Extractors:                   []string{"facebook", "facebook:reel"},
		MaxEntries:                   4,
		AllowCodecLessProgressiveMP4: true,
	},
	{
		PlatformID:                   "tumblr",
		Name:                         "Tumblr",
		SiteURL:                      "https://www.tumblr.com/",
		Icon:                         "tumblr",
		CatalogDomains:               []string{"tumblr.com"},
		InputHosts:                   []string{".tumblr.com"},
		MediaHostSuffix:              []string{".media.tumblr.com"},
		Extractors:                   []string{"Tumblr"},
		MaxEntries:                   4,
		AllowCodecLessProgressiveMP4: true,
	},
	{
		PlatformID:          "dailymotion",
		Name:                "Dailymotion",
		SiteURL:             "https://www.dailymotion.com/",
		Icon:                "dailymotion",
		CatalogDomains:      []string{"www.dailymotion.com"},
		InputHosts:          []string{"www.dailymotion.com"},
		OutboundHosts:       []string{"graphql.api.dailymotion.com", "cdndirector.dailymotion.com"},
		MediaHostSuffix:     []string{".dmcdn.net"},
		Extractors:          []string{"Dailymotion"},
		MaxEntries:          4,
		SingleStreamHLSOnly: true,
	},
}

func cloneSiteSpec(s SiteSpec) SiteSpec {
	s.CatalogDomains = append([]string(nil), s.CatalogDomains...)
	s.InputHosts = append([]string(nil), s.InputHosts...)
	s.OutboundHosts = append([]string(nil), s.OutboundHosts...)
	s.MediaHostSuffix = append([]string(nil), s.MediaHostSuffix...)
	s.Extractors = append([]string(nil), s.Extractors...)
	return s
}

// KnownSiteIDs returns the canonical IDs of all hardcoded ytgeneric platforms.
func KnownSiteIDs() []string {
	ids := make([]string, 0, len(builtinSites))
	for _, s := range builtinSites {
		ids = append(ids, s.PlatformID)
	}
	return ids
}

// LookupSite returns a copy of the hardcoded SiteSpec for id when registered.
func LookupSite(id string) (SiteSpec, bool) {
	for _, s := range builtinSites {
		if s.PlatformID == id {
			return cloneSiteSpec(s), true
		}
	}
	return SiteSpec{}, false
}

// ParseSitesEnv parses DOKITOOL_YTDLP_SITES (comma-separated, default empty).
// Any empty token or unknown site ID returns an error so process startup fails closed.
func ParseSitesEnv(raw string) ([]SiteSpec, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	tokens := strings.Split(trimmed, ",")
	specs := make([]SiteSpec, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		id := strings.TrimSpace(token)
		if id == "" {
			return nil, fmt.Errorf("DOKITOOL_YTDLP_SITES must not contain empty site entries")
		}
		spec, ok := LookupSite(id)
		if !ok {
			return nil, fmt.Errorf("DOKITOOL_YTDLP_SITES contains unknown site %q (supported: %s)", id, strings.Join(KnownSiteIDs(), ", "))
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		specs = append(specs, spec)
	}
	return specs, nil
}
