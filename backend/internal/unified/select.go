package unified

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SourceUsable is delivery eligibility, not a browser CORS capability check.
func SourceUsable(source Object, now int64) bool {
	if source == nil || Str(source["availability"]) == "unavailable" {
		return false
	}
	if expiry, exists := source["expires_at"]; exists && expiry != nil {
		t, err := time.Parse(time.RFC3339Nano, Str(expiry))
		return err == nil && t.Unix() > now
	}
	return true
}

// DeliverySources returns one ordered alternative-source group per dependency.
func DeliverySources(delivery Object) []Array {
	groups := make([]Array, 0)
	switch Str(delivery["kind"]) {
	case "file", "hls", "dash":
		groups = append(groups, Slice(delivery["sources"]))
	case "segments":
		if init := Map(delivery["init"]); init != nil {
			groups = append(groups, Slice(init["sources"]))
		}
		for _, value := range Slice(delivery["pieces"]) {
			groups = append(groups, Slice(Map(value)["sources"]))
		}
	case "mux", "pair":
		for _, value := range Slice(delivery["resources"]) {
			groups = append(groups, DeliverySources(Map(Map(value)["delivery"]))...)
		}
	}
	return groups
}

func DeliveryUsable(delivery Object, now int64) bool {
	groups := DeliverySources(delivery)
	if len(groups) == 0 {
		return false
	}
	for _, sources := range groups {
		usable := false
		for _, value := range sources {
			if SourceUsable(Map(value), now) {
				usable = true
				break
			}
		}
		if !usable {
			return false
		}
	}
	return true
}

func DeliveryRequires(delivery Object) []string {
	required := map[string]bool{}
	var visit func(Object)
	visit = func(d Object) {
		switch kind := Str(d["kind"]); kind {
		case "hls", "dash":
			required[kind] = true
		case "segments":
			if Str(d["join"]) == "byte_concat" {
				required["segment_byte_concat"] = true
			} else {
				required["segment_remux"] = true
			}
		case "mux", "pair":
			required[kind] = true
			for _, value := range Slice(d["resources"]) {
				visit(Map(Map(value)["delivery"]))
			}
		}
	}
	visit(delivery)
	out := make([]string, 0, len(required))
	for capability := range required {
		out = append(out, capability)
	}
	sort.Strings(out)
	return out
}

func nonNullField(object Object, field string) bool {
	value, exists := object[field]
	return exists && value != nil
}

func objectCopy(object Object) Object {
	if object == nil {
		return Object{}
	}
	return Clone(object)
}

func overlayMetadata(base, overlay Object) Object {
	out := objectCopy(base)
	for field, value := range overlay {
		out[field] = CloneValue(value)
	}
	return out
}

// VariantVideoMetadata declines contradictory geometry instead of choosing a
// scalar winner. A mux can obtain its geometry from its actual video resource.
func VariantVideoMetadata(variant Object) Object {
	metadata := Map(variant["metadata"])
	delivery := Map(variant["delivery"])
	if Str(delivery["kind"]) == "mux" {
		for _, value := range Slice(delivery["resources"]) {
			resource := Map(value)
			if Str(resource["role"]) != "video" {
				continue
			}
			facts := Map(resource["metadata"])
			combined := overlayMetadata(facts, metadata)
			for _, field := range []string{"width", "height"} {
				if nonNullField(Map(metadata["field_conflicts"]), field) || nonNullField(Map(facts["field_conflicts"]), field) ||
					(nonNullField(metadata, field) && nonNullField(facts, field) && !Equal(metadata[field], facts[field])) {
					delete(combined, field)
				}
			}
			return combined
		}
	}
	out := objectCopy(metadata)
	for _, field := range []string{"width", "height"} {
		if nonNullField(Map(metadata["field_conflicts"]), field) {
			delete(out, field)
		}
	}
	return out
}

// QualityCompare reports spatial dominance; comparable=false is PHP's null.
// No codec, bitrate, label or original-endpoint claim creates higher quality.
func QualityCompare(left, right Object) (comparison int, comparable bool) {
	a, b := VariantVideoMetadata(left), VariantVideoMetadata(right)
	aw, awOK := Numeric(a["width"])
	ah, ahOK := Numeric(a["height"])
	bw, bwOK := Numeric(b["width"])
	bh, bhOK := Numeric(b["height"])
	if !awOK || !ahOK || !bwOK || !bhOK || aw <= 0 || ah <= 0 || bw <= 0 || bh <= 0 {
		return 0, false
	}
	if aw > ah {
		aw, ah = ah, aw
	}
	if bw > bh {
		bw, bh = bh, bw
	}
	if math.Abs((ah/aw)/(bh/bw)-1) > 0.01 {
		return 0, false
	}
	if aw == bw && ah == bh {
		return 0, true
	}
	if aw >= bw && ah >= bh {
		return 1, true
	}
	if aw <= bw && ah <= bh {
		return -1, true
	}
	return 0, false
}

func readyState(object Object) bool {
	return !nonNullField(object, "state") || Str(object["state"]) == "ready"
}

func variantBefore(a, b Object) bool {
	am, bm := VariantVideoMetadata(a), VariantVideoMetadata(b)
	aKnown := nonNullField(am, "width") && nonNullField(am, "height")
	bKnown := nonNullField(bm, "width") && nonNullField(bm, "height")
	if aKnown != bKnown {
		return aKnown
	}
	aOriginal := Str(a["provenance"]) == "original_endpoint"
	bOriginal := Str(b["provenance"]) == "original_endpoint"
	if aOriginal != bOriginal {
		return aOriginal
	}
	return comparePHPID(Str(a["id"]), Str(b["id"])) < 0
}

var numericID = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
var integralID = regexp.MustCompile(`^[+-]?[0-9]+$`)

// PHP's array spaceship tie-break compares two numeric strings numerically.
// All current generated IDs are opaque prefixes, but legal numeric IDs must not
// select a different default after migrating languages. Overflowing integer
// strings retain PHP's lexical fallback when their floating values coincide.
func comparePHPID(a, b string) int {
	x, y := strings.Trim(a, " \t\r\n\v\f"), strings.Trim(b, " \t\r\n\v\f")
	if !numericID.MatchString(x) || !numericID.MatchString(y) {
		return strings.Compare(a, b)
	}
	xi, xerr := strconv.ParseInt(x, 10, 64)
	yi, yerr := strconv.ParseInt(y, 10, 64)
	if xerr == nil && yerr == nil {
		if xi < yi {
			return -1
		}
		if xi > yi {
			return 1
		}
		return 0
	}
	xf, _ := strconv.ParseFloat(x, 64)
	yf, _ := strconv.ParseFloat(y, 64)
	if xf < yf {
		return -1
	}
	if xf > yf {
		return 1
	}
	xOverflow := integralID.MatchString(x) && xerr != nil
	yOverflow := integralID.MatchString(y) && yerr != nil
	if (xOverflow && yOverflow) || math.IsInf(xf, 0) || math.IsInf(yf, 0) {
		return strings.Compare(a, b)
	}
	if xOverflow && yerr == nil {
		if xf < 0 {
			return -1
		}
		return 1
	}
	if yOverflow && xerr == nil {
		if yf < 0 {
			return 1
		}
		return -1
	}
	return 0
}

// SelectDefault never mutates its input or sorts the displayed candidate list.
func SelectDefault(input Object, now int64) Object {
	item := objectCopy(input)
	delete(item, "default_unavailable_reason")
	delete(item, "higher_quality_variants")
	item["default"] = nil
	if !readyState(item) {
		reason := "unavailable"
		if Str(item["state"]) == "deferred" {
			reason = "needs_resolve"
		}
		item["default_unavailable_reason"] = reason
		return item
	}
	variants := Slice(item["variants"])
	eligible := make([]Object, 0)
	for _, value := range variants {
		v := Map(value)
		if readyState(v) && Str(Map(v["delivery"])["kind"]) == "file" && DeliveryUsable(Map(v["delivery"]), now) {
			eligible = append(eligible, v)
		}
	}
	highest := make([]Object, 0)
	for _, v := range eligible {
		dominated := false
		for _, other := range eligible {
			if cmp, comparable := QualityCompare(other, v); comparable && cmp == 1 {
				dominated = true
				break
			}
		}
		if !dominated {
			highest = append(highest, v)
		}
	}
	sort.SliceStable(highest, func(i, j int) bool { return variantBefore(highest[i], highest[j]) })
	if len(highest) > 0 {
		selected := highest[0]
		for _, value := range Slice(Map(selected["delivery"])["sources"]) {
			source := Map(value)
			if !SourceUsable(source, now) {
				continue
			}
			projection := Object{"variant_id": selected["id"], "url": source["url"]}
			for _, fields := range [][2]string{{"id", "source_id"}, {"access", "access"}, {"expires_at", "expires_at"}} {
				if nonNullField(source, fields[0]) {
					projection[fields[1]] = source[fields[0]]
				}
			}
			item["default"] = projection
			break
		}
		higher := Array{}
		for _, value := range variants {
			v := Map(value)
			if cmp, comparable := QualityCompare(v, selected); !comparable || cmp != 1 || !DeliveryUsable(Map(v["delivery"]), now) {
				continue
			}
			if required := DeliveryRequires(Map(v["delivery"])); len(required) > 0 {
				higher = append(higher, Object{"variant_id": v["id"], "requires": Slice(required)})
			}
		}
		if len(higher) > 0 {
			item["higher_quality_variants"] = higher
		}
		return item
	}
	reason := "unavailable"
	for _, value := range variants {
		if Str(Map(value)["state"]) == "deferred" {
			reason = "needs_resolve"
			break
		}
	}
	for _, value := range variants {
		v := Map(value)
		if Str(v["state"]) == "deferred" {
			continue
		}
		delivery := Map(v["delivery"])
		if DeliveryUsable(delivery, now) {
			reason = "processing_required"
			break
		}
		for _, sources := range DeliverySources(delivery) {
			for _, value := range sources {
				source := Map(value)
				if reason != "needs_resolve" && nonNullField(source, "expires_at") {
					if expiry, err := time.Parse(time.RFC3339Nano, Str(source["expires_at"])); err == nil && expiry.Unix() <= now {
						reason = "needs_refresh"
					}
				}
			}
		}
	}
	item["default_unavailable_reason"] = reason
	return item
}

func FinalizeData(input Object, now int64) Object {
	data := objectCopy(input)
	data["generated_at"] = time.Unix(now, 0).UTC().Format(time.RFC3339)
	items := make(Array, 0, len(Slice(data["items"])))
	for _, value := range Slice(data["items"]) {
		items = append(items, SelectDefault(Map(value), now))
	}
	data["items"] = items
	return Object{"schema_version": SchemaVersion, "code": "OK", "data": data}
}

func ContractError(code, message string, retryable bool) Object {
	problem := Object{"code": code, "message": message}
	if retryable {
		problem["retryable"] = true
	}
	return Object{"schema_version": SchemaVersion, "code": code, "error": problem}
}
