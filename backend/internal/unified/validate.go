package unified

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var leftoverEscape = regexp.MustCompile(`(?i)%[0-9a-f]{2}`)

func unsafeURLByte(text string) bool {
	for _, c := range text {
		if c <= 0x20 || c == 0x7f || c == '\\' {
			return true
		}
	}
	return false
}

func ValidateSource(source Object, generatedAt *int64) error {
	raw := Str(source["url"])
	if unsafeURLByte(raw) {
		return fmt.Errorf("unsafe source URL")
	}
	if Str(source["access"]) == "relay" {
		path := raw
		if end := strings.IndexAny(path, "?#"); end >= 0 {
			path = path[:end]
		}
		path = rawPercentDecode(path)
		if !strings.HasPrefix(raw, "/api/") || unsafeURLByte(path) || leftoverEscape.MatchString(path) {
			return fmt.Errorf("unsafe relay URL")
		}
		for _, component := range strings.Split(path, "/") {
			if component == "." || component == ".." {
				return fmt.Errorf("unsafe relay URL path")
			}
		}
	} else {
		parsed, ok := splitRawURL(raw)
		if !ok || parsed.userInfo || MediaURL(raw) != raw {
			return fmt.Errorf("absolute direct URL required")
		}
	}
	if alternatives, exists := source["fallback_urls"]; exists {
		values := Slice(alternatives)
		base, err := url.Parse(raw)
		if len(values) < 1 || len(values) > 2 || err != nil || base.Scheme != "https" || Str(source["access"]) == "relay" {
			return fmt.Errorf("invalid credential fallback list")
		}
		seen := map[string]bool{raw: true}
		for _, value := range values {
			alternate, ok := value.(string)
			parsed, parseErr := url.Parse(alternate)
			if !ok || parseErr != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || strings.TrimSuffix(strings.ToLower(parsed.Host), ":443") != strings.TrimSuffix(strings.ToLower(base.Host), ":443") || seen[alternate] {
				return fmt.Errorf("invalid credential fallback origin or duplicate")
			}
			if err := ValidateSource(Object{"url": alternate}, nil); err != nil {
				return err
			}
			seen[alternate] = true
		}
	}
	if nonNullField(source, "expires_at") {
		if _, ok := parseContractTime(Str(source["expires_at"])); generatedAt == nil || !ok {
			return fmt.Errorf("expiry needs valid generation time")
		}
	}
	return nil
}

func evidenceValue(value any) bool {
	return Str(value) == "upstream" || Str(value) == "probe"
}

func ValidateMetadata(metadata Object, role string) error {
	technical := make([]string, 0)
	for field := range metadata {
		if !hasString([]string{"container", "extension", "mime_type", "evidence", "field_evidence", "field_conflicts"}, field) {
			technical = append(technical, field)
		}
	}
	if len(technical) > 0 && !hasString([]string{"probe", "upstream", "mixed"}, Str(metadata["evidence"])) {
		return fmt.Errorf("technical metadata needs evidence")
	}
	if Str(metadata["evidence"]) == "mixed" {
		fields := Map(metadata["field_evidence"])
		for _, field := range technical {
			if !evidenceValue(fields[field]) {
				return fmt.Errorf("mixed metadata evidence incomplete")
			}
		}
		for field := range fields {
			if _, exists := metadata[field]; !exists || hasString([]string{"evidence", "field_evidence", "field_conflicts"}, field) {
				return fmt.Errorf("unknown field evidence")
			}
		}
	}
	conflicts := Map(metadata["field_conflicts"])
	definitions := Map(Map(Map(schemaDocument["$defs"])["MediaMetadata"])["properties"])
	for _, field := range sortedObjectKeys(conflicts) {
		definition := Map(definitions[field])
		if definition == nil || hasString([]string{"evidence", "field_evidence", "field_conflicts"}, field) {
			return fmt.Errorf("unknown metadata conflict field")
		}
		if _, exists := metadata[field]; exists {
			return fmt.Errorf("conflicting metadata cannot claim a scalar fact")
		}
		facts := Slice(conflicts[field])
		if len(facts) < 2 {
			return fmt.Errorf("metadata conflicts need distinct values")
		}
		for i, value := range facts {
			fact := Map(value)
			if err := validateSchemaNode(fact["value"], definition, "$.metadata.field_conflicts."+field); err != nil {
				return err
			}
			if field == "audio_language" && strings.TrimSpace(Str(fact["value"])) == "" {
				return fmt.Errorf("language observation must not be blank")
			}
			if field == "has_audio" && hasString([]string{"video", "image", "subtitle"}, role) && fact["value"] != false {
				return fmt.Errorf("track cannot claim embedded audio")
			}
			if field == "mime_type" && hasString([]string{"image", "audio"}, role) && !strings.HasPrefix(Str(fact["value"]), role+"/") {
				return fmt.Errorf("MIME conflicts with media role")
			}
			for _, prior := range facts[:i] {
				if Equal(Map(prior)["value"], fact["value"]) {
					return fmt.Errorf("metadata conflicts need distinct values")
				}
			}
		}
	}
	present := map[string]bool{}
	for field := range metadata {
		present[field] = true
	}
	for field := range conflicts {
		present[field] = true
	}
	if nonNullField(metadata, "audio_language") && strings.TrimSpace(Str(metadata["audio_language"])) == "" {
		return fmt.Errorf("language must not be blank")
	}
	if present["audio_language_role"] && !present["audio_language"] {
		return fmt.Errorf("language role requires an explicit language")
	}
	if metadata["has_audio"] == false || hasString([]string{"video", "image", "subtitle"}, role) {
		for _, field := range []string{"audio_codec", "audio_language", "audio_language_role", "audio_channels", "sample_rate_hz", "audio_bitrate_bps"} {
			if present[field] {
				return fmt.Errorf("contradictory audio metadata")
			}
		}
		if role != "" && nonNullField(metadata, "has_audio") && metadata["has_audio"] != false {
			return fmt.Errorf("track cannot claim embedded audio")
		}
	}
	if role == "audio" {
		for _, field := range []string{"width", "height", "fps", "hdr_type", "video_codec", "video_bitrate_bps"} {
			if present[field] {
				return fmt.Errorf("audio cannot carry video metadata")
			}
		}
	}
	return nil
}

func ValidateDelivery(delivery Object, generatedAt *int64, leaf bool) error {
	kind := Str(delivery["kind"])
	if leaf && (kind == "mux" || kind == "pair") {
		return fmt.Errorf("nested composition is unsupported")
	}
	for _, sources := range DeliverySources(delivery) {
		seen := map[string]bool{}
		for _, value := range sources {
			source := Map(value)
			if err := ValidateSource(source, generatedAt); err != nil {
				return err
			}
			if len(sources) != 1 && !nonNullField(source, "id") {
				return fmt.Errorf("multiple sources require IDs")
			}
			if nonNullField(source, "id") {
				id := Str(source["id"])
				if seen[id] {
					return fmt.Errorf("duplicate source ID")
				}
				seen[id] = true
			}
		}
	}
	if kind == "segments" {
		chunks := make(Array, 0)
		if init := Map(delivery["init"]); init != nil {
			chunks = append(chunks, init)
		}
		for i, value := range Slice(delivery["pieces"]) {
			piece := Map(value)
			if !Equal(piece["index"], i+1) {
				return fmt.Errorf("segments must preserve contiguous time order")
			}
			chunks = append(chunks, piece)
		}
		if Str(delivery["join"]) == "byte_concat" && Str(delivery["join_evidence"]) != "validated_fragment_layout" {
			return fmt.Errorf("byte concat requires layout evidence")
		}
		for _, value := range chunks {
			if limits := Map(Map(value)["byte_range"]); limits != nil {
				start, startOK := schemaNumber(limits["start"])
				end, endOK := schemaNumber(limits["end_inclusive"])
				if !startOK || !endOK || end.Cmp(start) < 0 {
					return fmt.Errorf("invalid byte range")
				}
			}
		}
	}
	if (kind == "hls" || kind == "dash") && nonNullField(delivery, "selection") && len(Map(delivery["selection"])) == 0 {
		return fmt.Errorf("empty manifest selection")
	}
	if kind == "mux" || kind == "pair" {
		ids, roles := map[string]bool{}, map[string]int{}
		resources := Slice(delivery["resources"])
		for _, value := range resources {
			resource := Map(value)
			id, role := Str(resource["id"]), Str(resource["role"])
			if ids[id] {
				return fmt.Errorf("duplicate resource ID")
			}
			ids[id], roles[role] = true, roles[role]+1
			if err := ValidateMetadata(Map(resource["metadata"]), role); err != nil {
				return err
			}
			if err := ValidateDelivery(Map(resource["delivery"]), generatedAt, true); err != nil {
				return err
			}
		}
		if kind == "pair" {
			if len(resources) != 2 || roles["image"] != 1 || roles["motion"] != 1 {
				return fmt.Errorf("invalid live pair")
			}
		} else if len(roles) != 2 || roles["video"] != 1 || roles["audio"] < 1 {
			return fmt.Errorf("invalid mux roles")
		}
	}
	return nil
}

func normalizeHigher(entries Array) Array {
	result := make(Array, 0, len(entries))
	for _, value := range entries {
		entry := objectCopy(Map(value))
		required := make([]string, 0)
		for _, value := range Slice(entry["requires"]) {
			required = append(required, Str(value))
		}
		sort.Strings(required)
		entry["requires"] = Slice(required)
		result = append(result, entry)
	}
	sort.SliceStable(result, func(i, j int) bool { return Str(Map(result[i])["variant_id"]) < Str(Map(result[j])["variant_id"]) })
	return result
}

func ValidateEnvelope(payload Object) error { return ValidateEnvelopeAt(payload, time.Now().Unix()) }

// ValidateEnvelopeAt makes missing-generated_at legacy fixtures deterministic;
// when generated_at is present it remains authoritative, as in the PHP oracle.
func ValidateEnvelopeAt(payload Object, fallbackNow int64) error {
	if err := ValidateStructure(payload); err != nil {
		return err
	}
	if Str(payload["code"]) != "OK" {
		if !Equal(Map(payload["error"])["code"], payload["code"]) {
			return fmt.Errorf("error code mismatch")
		}
		return nil
	}
	data := Map(payload["data"])
	var generatedAt *int64
	now := fallbackNow
	if nonNullField(data, "generated_at") {
		generated, ok := parseContractTime(Str(data["generated_at"]))
		if !ok {
			return fmt.Errorf("invalid generation time")
		}
		now = generated.Unix()
		generatedAt = &now
	}
	ids := map[string]bool{}
	for _, value := range Slice(data["items"]) {
		item := Map(value)
		id := Str(item["id"])
		if ids[id] {
			return fmt.Errorf("duplicate item ID")
		}
		ids[id] = true
		if cover := Map(item["cover"]); cover != nil {
			if err := ValidateSource(cover, generatedAt); err != nil {
				return err
			}
		}
		variants := Slice(item["variants"])
		variantIDs := map[string]bool{}
		for _, value := range variants {
			variant := Map(value)
			variantID := Str(variant["id"])
			if variantIDs[variantID] {
				return fmt.Errorf("duplicate variant ID")
			}
			variantIDs[variantID] = true
			if Str(variant["state"]) == "deferred" {
				continue
			}
			metadata, delivery := Map(variant["metadata"]), Map(variant["delivery"])
			if err := ValidateMetadata(metadata, ""); err != nil {
				return err
			}
			if err := ValidateDelivery(delivery, generatedAt, false); err != nil {
				return err
			}
			kind := Str(delivery["kind"])
			if (kind == "hls" || kind == "dash") && Str(delivery["scope"]) == "adaptive" && !nonNullField(delivery, "selection") &&
				(nonNullField(metadata, "width") || nonNullField(metadata, "height")) {
				return fmt.Errorf("adaptive manifest has no fixed resolution")
			}
		}
		expected := SelectDefault(item, now)
		if item["default"] != nil {
			if expected["default"] == nil {
				return fmt.Errorf("no eligible complete file")
			}
			projection := Map(item["default"])
			var selected Object
			for _, value := range variants {
				if variant := Map(value); Equal(variant["id"], projection["variant_id"]) {
					selected = variant
				}
			}
			if selected == nil || !readyState(selected) || Str(Map(selected["delivery"])["kind"]) != "file" {
				return fmt.Errorf("default must reference complete file")
			}
			sources := Slice(Map(selected["delivery"])["sources"])
			if len(sources) != 1 && !nonNullField(projection, "source_id") {
				return fmt.Errorf("default requires source ID")
			}
			var source Object
			for _, value := range sources {
				entry := Map(value)
				if (nonNullField(projection, "source_id") && Equal(entry["id"], projection["source_id"])) || (!nonNullField(projection, "source_id") && len(sources) == 1) {
					source = entry
				}
			}
			if source == nil || !SourceUsable(source, now) {
				return fmt.Errorf("default source is unavailable")
			}
			sourceAccess, defaultAccess := Str(source["access"]), Str(projection["access"])
			if sourceAccess == "" {
				sourceAccess = "direct"
			}
			if defaultAccess == "" {
				defaultAccess = "direct"
			}
			if !Equal(source["url"], projection["url"]) || sourceAccess != defaultAccess || !Equal(source["expires_at"], projection["expires_at"]) {
				return fmt.Errorf("default source projection mismatch")
			}
			higher := Array{}
			for _, value := range variants {
				variant := Map(value)
				if cmp, comparable := QualityCompare(variant, selected); !comparable || cmp != 1 || !DeliveryUsable(Map(variant["delivery"]), now) {
					continue
				}
				if Str(Map(variant["delivery"])["kind"]) == "file" {
					return fmt.Errorf("higher complete file omitted from default")
				}
				higher = append(higher, Object{"variant_id": variant["id"], "requires": Slice(DeliveryRequires(Map(variant["delivery"])))})
			}
			expected["higher_quality_variants"] = higher
		} else if expected["default"] != nil {
			return fmt.Errorf("eligible complete file has null default")
		}
		if !Equal(expected["default_unavailable_reason"], item["default_unavailable_reason"]) {
			return fmt.Errorf("invalid null-default reason")
		}
		if !Equal(normalizeHigher(Slice(expected["higher_quality_variants"])), normalizeHigher(Slice(item["higher_quality_variants"]))) {
			return fmt.Errorf("higher-quality projection mismatch")
		}
	}
	if len(ids) != 1 && !nonNullField(data, "default_item_id") {
		return fmt.Errorf("multiple items need explicit entry")
	}
	if nonNullField(data, "default_item_id") && !ids[Str(data["default_item_id"])] {
		return fmt.Errorf("unknown default item")
	}
	groupIDs := map[string]bool{}
	for _, value := range Slice(data["groups"]) {
		group := Map(value)
		id := Str(group["id"])
		if groupIDs[id] {
			return fmt.Errorf("duplicate group")
		}
		groupIDs[id] = true
		members := map[string]bool{}
		for _, value := range Slice(group["item_ids"]) {
			member := Str(value)
			if members[member] {
				return fmt.Errorf("duplicate group member")
			}
			if !ids[member] {
				return fmt.Errorf("unknown group item")
			}
			members[member] = true
		}
	}
	for _, value := range Slice(payload["warnings"]) {
		warning := Map(value)
		if nonNullField(warning, "item_id") && !ids[Str(warning["item_id"])] {
			return fmt.Errorf("unknown warning item")
		}
	}
	for _, source := range []Object{Map(data["cover"]), Map(Map(data["author"])["avatar"])} {
		if source != nil {
			if err := ValidateSource(source, generatedAt); err != nil {
				return err
			}
		}
	}
	attachmentIDs := map[string]bool{}
	for _, value := range Slice(data["attachments"]) {
		attachment := Map(value)
		id := Str(attachment["id"])
		if attachmentIDs[id] {
			return fmt.Errorf("duplicate attachment")
		}
		attachmentIDs[id] = true
		resource := Map(attachment["resource"])
		if err := ValidateMetadata(Map(resource["metadata"]), Str(resource["role"])); err != nil {
			return err
		}
		if err := ValidateDelivery(Map(resource["delivery"]), generatedAt, true); err != nil {
			return err
		}
		if cover := Map(attachment["cover"]); cover != nil {
			if err := ValidateSource(cover, generatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}
