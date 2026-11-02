package pipixia

import (
	"encoding/json"
	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestPipixiaCredentialFallback(t *testing.T) {
	old := representationURL("v3-cdn-tos.ppxvod.com", strings.Repeat("a", 32), "CredentialObject", "a=1319&ft=old&dy_q=100&l=old")
	fresh := representationURL("v3-cdn-tos.ppxvod.com", strings.Repeat("b", 32), "CredentialObject", "a=1319&ft=new&dy_q=101&l=new")
	before := representationPayload(t, []string{old})
	cases := []map[string]any{}
	for _, pair := range []struct {
		name string
		urls []string
	}{
		{"stale-first", []string{old, fresh}}, {"fresh-first", []string{fresh, old}},
	} {
		t.Run(pair.name, func(t *testing.T) {
			payload := representationPayload(t, pair.urls)
			variant := representationVariant(t, payload)
			sources := unified.Slice(unified.Map(variant["delivery"])["sources"])
			if len(sources) != 1 {
				t.Fatalf("logical source split: %v", sources)
			}
			source := unified.Map(sources[0])
			if source["url"] != pair.urls[0] {
				t.Fatal("primary/default was changed")
			}
			if !reflect.DeepEqual(unified.Slice(source["fallback_urls"]), unified.Array{pair.urls[1]}) {
				t.Errorf("lost same-host credential: %v", source)
			}
			cases = append(cases, map[string]any{"name": pair.name, "before": before, "payload": payload, "old_url": old, "working_url": fresh})
		})
	}
	if path := os.Getenv("DOKITOOL_PIPIXIA_CREDENTIAL_OUTPUT"); path != "" {
		body, _ := json.MarshalIndent(map[string]any{"cases": cases}, "", "  ")
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPipixiaCredentialFallbackBounds(t *testing.T) {
	makeURL := func(host, sig, object, query string) string {
		return representationURL(host, strings.Repeat(sig, 32), object, query)
	}
	q := "a=1319&ft=old&dy_q=100&l=old"
	old := makeURL("v3-cdn-tos.ppxvod.com", "a", "CredentialObject", q)
	many := []string{old}
	for _, sig := range []string{"b", "c", "d", "e"} {
		many = append(many, makeURL("v3-cdn-tos.ppxvod.com", sig, "CredentialObject", q))
	}
	v := representationVariant(t, representationPayload(t, many))
	source := unified.Map(unified.Slice(unified.Map(v["delivery"])["sources"])[0])
	if !reflect.DeepEqual(unified.Slice(source["fallback_urls"]), unified.Array{many[1], many[2]}) {
		t.Fatalf("not bounded/input ordered: %v", source)
	}
	for name, other := range map[string]string{
		"different-host":    makeURL("v26-cdn-tos.ppxvod.com", "b", "CredentialObject", q),
		"different-object":  makeURL("v3-cdn-tos.ppxvod.com", "b", "OtherObject", q),
		"different-content": makeURL("v3-cdn-tos.ppxvod.com", "b", "CredentialObject", q+"&watermark=1"),
		"malformed-renewal": makeURL("v3-cdn-tos.ppxvod.com", "b", "CredentialObject", q+"&ft=duplicate"),
	} {
		t.Run(name, func(t *testing.T) {
			payload := representationPayload(t, []string{old, other})
			for _, variant := range unified.Slice(representationItem(payload)["variants"]) {
				for _, raw := range unified.Slice(unified.Map(unified.Map(variant)["delivery"])["sources"]) {
					if _, exists := unified.Map(raw)["fallback_urls"]; exists {
						t.Fatalf("unrelated credential admitted: %v", raw)
					}
				}
			}
		})
	}
}
