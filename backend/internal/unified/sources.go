package unified

import (
	"sort"
	"time"
)

// Source normalizes one private parser source; Sources adds its stable ID.
func Source(raw any) Object {
	if text, ok := raw.(string); ok {
		raw = Object{"url": text}
	}
	entry := Map(raw)
	if entry == nil {
		return nil
	}
	url := MediaURL(entry["url"])
	if url == "" {
		return nil
	}
	source := Object{"url": url}
	for _, field := range []string{"size", "size_bytes"} {
		if size, ok := Numeric(entry[field]); ok && size > 0 {
			integer, _ := PHPInt(entry[field])
			source["size_bytes"] = integer
		}
	}
	for _, field := range []string{"expires_at", "availability", "browser_fetch", "fallback_urls"} {
		if nonNullField(entry, field) {
			source[field] = CloneValue(entry[field])
		}
	}
	return source
}

func Sources(raw Array, now int64) Array {
	sources := make(Array, 0, len(raw))
	for _, value := range raw {
		source := Source(value)
		if source == nil {
			continue
		}
		identity, ok := Text(Map(value)["private_source_identity"])
		if !ok {
			identity = SourceIdentity(Str(source["url"]))
		}
		source["id"] = "s-" + identityDigest(identity)
		sources = append(sources, source)
	}
	return MergeSources(Array{}, sources, now)
}

func sourceRank(source Object, now int64) [3]int64 {
	rank := [3]int64{}
	if SourceUsable(source, now) {
		rank[0] = 1
	}
	if Str(source["availability"]) == "available" {
		rank[1] = 1
	}
	if expires, err := time.Parse(time.RFC3339Nano, Str(source["expires_at"])); err == nil {
		rank[2] = expires.Unix()
	}
	return rank
}

// MergeSources chooses a whole observation, never a URL from one observation
// and an expiry or availability field from another. First-seen order survives.
func MergeSources(existing, additional Array, now int64) Array {
	out := make(Array, 0, len(existing)+len(additional))
	positions := make(map[string]int)
	for _, collection := range []Array{existing, additional} {
		for _, value := range collection {
			source := Map(value)
			if source == nil {
				continue
			}
			id := Str(source["id"])
			index, exists := positions[id]
			if !exists {
				positions[id] = len(out)
				out = append(out, Clone(source))
				continue
			}
			a, b := sourceRank(source, now), sourceRank(Map(out[index]), now)
			for i := range a {
				if a[i] > b[i] {
					out[index] = Clone(source)
					break
				}
				if a[i] < b[i] {
					break
				}
			}
		}
	}
	return out
}

func ResourceIdentity(resource Object) any {
	delivery := Map(resource["delivery"])
	ids := make([]string, 0)
	for _, value := range Slice(delivery["sources"]) {
		ids = append(ids, Str(Map(value)["id"]))
	}
	sort.Strings(ids)
	return Array{resource["role"], delivery["kind"], Slice(ids)}
}
