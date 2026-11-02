package unified

import (
	"math/big"
	"strings"
)

const maxPublicSourceSize = 9007199254740991

// projectPublicSources changes only Source fields on PublicView's deep copy.
// Raw observations and identities keep their PHP-compatible representation.
func projectPublicSources(data Object) {
	projectPublicSource(Map(data["cover"]))
	projectPublicSource(Map(Map(data["author"])["avatar"]))
	for _, value := range Slice(data["items"]) {
		item := Map(value)
		projectPublicSource(Map(item["cover"]))
		for _, value := range Slice(item["variants"]) {
			projectPublicDelivery(Map(Map(value)["delivery"]))
		}
	}
	for _, value := range Slice(data["attachments"]) {
		attachment := Map(value)
		projectPublicSource(Map(attachment["cover"]))
		projectPublicSource(Map(Map(attachment["author"])["avatar"]))
		projectPublicDelivery(Map(Map(attachment["resource"])["delivery"]))
	}
}

func projectPublicDelivery(delivery Object) {
	for _, sources := range DeliverySources(delivery) {
		for _, value := range sources {
			projectPublicSource(Map(value))
		}
	}
}

func projectPublicSource(source Object) {
	if source == nil {
		return
	}
	if access, exists := source["access"]; !exists || access == "direct" {
		if raw, ok := source["url"].(string); ok {
			if direct, _, found := strings.Cut(raw, "#"); found {
				// Do not parse/re-encode signed queries or decode escaped %23.
				source["url"] = direct
			}
		}
	}
	if text, ok := numberText(source["size_bytes"]); ok {
		size, valid := new(big.Rat).SetString(text)
		// Keep malformed values for validation to reject. Only an exact
		// positive integer outside the browser's safe range is omitted.
		if valid && size.IsInt() && size.Sign() > 0 && size.Num().Cmp(big.NewInt(maxPublicSourceSize)) > 0 {
			delete(source, "size_bytes")
		}
	}
}
