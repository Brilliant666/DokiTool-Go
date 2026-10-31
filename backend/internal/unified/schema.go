package unified

import (
	_ "embed"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The generated resource is an exact copy of the canonical repository schema.
// A test detects drift; neither Go code nor a second hand-maintained schema is
// allowed to redefine the public contract.
//
//go:generate go run generate_schema.go
//go:embed unified-v1.generated.schema.json
var embeddedSchema []byte

var schemaDocument = func() Object {
	schema, err := Decode(embeddedSchema)
	if err != nil {
		panic("invalid embedded unified-v1 schema: " + err.Error())
	}
	if err := ValidateSchemaDefinition(schema); err != nil {
		panic("unsupported embedded unified-v1 schema: " + err.Error())
	}
	return schema
}()

func SchemaBytes() []byte { return append([]byte(nil), embeddedSchema...) }

// ValidateStructure implements the keywords used by the canonical schema.
// Independent Draft 2020-12 validation remains an external acceptance check.
func ValidateStructure(value any) error {
	return validateSchemaNode(value, schemaDocument, "$")
}

func sortedObjectKeys(object Object) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func schemaNumber(value any) (*big.Rat, bool) {
	text, ok := numberText(value)
	if !ok {
		return nil, false
	}
	if _, finite := Numeric(value); !finite {
		return nil, false
	}
	number, ok := new(big.Rat).SetString(text)
	return number, ok
}

func schemaInteger(value any) bool {
	switch value.(type) {
	case float32, float64:
		return false
	}
	text, ok := numberText(value)
	if !ok {
		return false
	}
	// Preserve the PHP runtime's integer wire values. Numeric strings and
	// floating point observations are not integer metadata, even when integral.
	return integerSpelling.MatchString(text)
}

var integerSpelling = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)$`)
var timestampSpelling = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})$`)

func parseContractTime(raw string) (time.Time, bool) {
	if !timestampSpelling.MatchString(raw) || strings.HasPrefix(raw, "0000-") {
		return time.Time{}, false
	}
	if !strings.HasSuffix(raw, "Z") {
		offset := raw[len(raw)-6:]
		hours, _ := strconv.Atoi(offset[1:3])
		minutes, _ := strconv.Atoi(offset[4:6])
		if hours > 23 || minutes > 59 {
			return time.Time{}, false
		}
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	return t, err == nil
}

func validateSchemaNode(value any, node Object, path string) error {
	fail := func(message string) error { return fmt.Errorf("%s: %s", path, message) }
	if err := auditSchemaKeywords(node, path); err != nil {
		return err
	}
	if reference := Str(node["$ref"]); reference != "" {
		const prefix = "#/$defs/"
		if !strings.HasPrefix(reference, prefix) {
			return fail("unsupported schema reference")
		}
		definition := Map(Map(schemaDocument["$defs"])[strings.TrimPrefix(reference, prefix)])
		if definition == nil {
			return fail("unknown schema reference")
		}
		return validateSchemaNode(value, definition, path)
	}
	if alternatives, one := Slice(node["oneOf"]), true; alternatives != nil || Slice(node["anyOf"]) != nil {
		if alternatives == nil {
			alternatives, one = Slice(node["anyOf"]), false
		}
		matches := 0
		for _, alternative := range alternatives {
			if validateSchemaNode(value, Map(alternative), path) == nil {
				matches++
			}
		}
		if (one && matches != 1) || (!one && matches == 0) {
			return fail("invalid schema branch")
		}
		return nil
	}
	if wanted, exists := node["const"]; exists && !Equal(value, wanted) {
		return fail("invalid constant")
	}
	if enumeration := Slice(node["enum"]); enumeration != nil {
		found := false
		for _, allowed := range enumeration {
			found = found || Equal(value, allowed)
		}
		if !found {
			return fail("invalid enum")
		}
	}
	typeName := Str(node["type"])
	valid := true
	switch typeName {
	case "object":
		valid = Map(value) != nil
	case "array":
		valid = Slice(value) != nil
	case "string":
		_, valid = value.(string)
	case "integer":
		valid = schemaInteger(value)
	case "number":
		_, valid = schemaNumber(value)
	case "boolean":
		_, valid = value.(bool)
	case "null":
		valid = value == nil
	case "":
	default:
		valid = false
	}
	if !valid {
		return fail("invalid type")
	}
	if typeName == "object" {
		object := Map(value)
		if minimum, ok := Numeric(node["minProperties"]); ok && float64(len(object)) < minimum {
			return fail("too few properties")
		}
		for _, required := range Slice(node["required"]) {
			if _, exists := object[Str(required)]; !exists {
				return fail("missing " + Str(required))
			}
		}
		properties := Map(node["properties"])
		for _, key := range sortedObjectKeys(object) {
			if names := Map(node["propertyNames"]); names != nil {
				if err := validateSchemaNode(key, names, path+".[property]"); err != nil {
					return err
				}
			}
			if child := Map(properties[key]); child != nil {
				if err := validateSchemaNode(object[key], child, path+"."+key); err != nil {
					return err
				}
			} else if node["additionalProperties"] == false {
				return fail("unknown " + key)
			} else if child := Map(node["additionalProperties"]); child != nil {
				if err := validateSchemaNode(object[key], child, path+"."+key); err != nil {
					return err
				}
			}
		}
	}
	if typeName == "array" {
		values := Slice(value)
		if minimum, ok := Numeric(node["minItems"]); ok && float64(len(values)) < minimum {
			return fail("too few array items")
		}
		if maximum, ok := Numeric(node["maxItems"]); ok && float64(len(values)) > maximum {
			return fail("too many array items")
		}
		for index, child := range values {
			if node["uniqueItems"] == true {
				for _, prior := range values[:index] {
					if Equal(prior, child) {
						return fail("duplicate array item")
					}
				}
			}
			if err := validateSchemaNode(child, Map(node["items"]), fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	if typeName == "string" {
		text := Str(value)
		if !utf8.ValidString(text) {
			return fail("invalid UTF-8")
		}
		length := float64(utf8.RuneCountInString(text))
		if minimum, ok := Numeric(node["minLength"]); ok && length < minimum {
			return fail("text too short")
		}
		if maximum, ok := Numeric(node["maxLength"]); ok && length > maximum {
			return fail("text too long")
		}
		if pattern := Str(node["pattern"]); pattern != "" {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return fail("unsupported schema pattern")
			}
			if !re.MatchString(text) {
				return fail("invalid pattern")
			}
		}
		switch Str(node["format"]) {
		case "date-time":
			if _, ok := parseContractTime(text); !ok {
				return fail("invalid RFC3339 timestamp")
			}
		case "uri":
			// Use the same raw HTTP(S) boundary as PHP's FILTER_VALIDATE_URL.
			// net/url would both admit invalid DNS labels/raw Unicode and reject
			// some percent bytes that PHP preserves in transport URLs.
			if MediaURL(text) != text {
				return fail("invalid absolute URI")
			}
		}
	}
	if number, ok := schemaNumber(value); ok {
		for _, bound := range []struct {
			name  string
			valid func(int) bool
		}{{"minimum", func(c int) bool { return c >= 0 }}, {"exclusiveMinimum", func(c int) bool { return c > 0 }}, {"maximum", func(c int) bool { return c <= 0 }}} {
			if limit, present := schemaNumber(node[bound.name]); present && !bound.valid(number.Cmp(limit)) {
				return fail("outside " + bound.name)
			}
		}
	}
	return nil
}

var schemaAnnotations = map[string]bool{
	"$schema": true, "$id": true, "$defs": true, "title": true,
	"description": true, "default": true, "discriminator": true,
}

var schemaAssertions = map[string]bool{
	"$ref": true, "type": true, "const": true, "enum": true, "anyOf": true, "oneOf": true,
	"properties": true, "additionalProperties": true, "propertyNames": true, "required": true, "minProperties": true,
	"items": true, "minItems": true, "maxItems": true, "uniqueItems": true,
	"minLength": true, "maxLength": true, "pattern": true, "format": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true,
}

func auditSchemaKeywords(node Object, path string) error {
	fail := func(message string) error { return fmt.Errorf("%s: %s", path, message) }
	for key := range node {
		if !schemaAnnotations[key] && !schemaAssertions[key] {
			return fail("unsupported schema keyword " + key)
		}
	}
	if format, exists := node["format"]; exists && !hasString([]string{"uri", "date-time"}, Str(format)) {
		return fail("unsupported schema format")
	}
	if dialect, exists := node["$schema"]; exists && Str(dialect) != "https://json-schema.org/draft/2020-12/schema" {
		return fail("unsupported schema dialect")
	}
	if kind, exists := node["type"]; exists && !hasString([]string{"object", "array", "string", "integer", "number", "boolean", "null"}, Str(kind)) {
		return fail("unsupported schema type")
	}
	branch := ""
	for _, key := range []string{"$ref", "anyOf", "oneOf"} {
		if _, exists := node[key]; exists {
			if branch != "" {
				return fail("unsupported combined schema branches")
			}
			branch = key
		}
	}
	if branch != "" {
		for key := range node {
			if schemaAssertions[key] && key != branch {
				return fail("unsupported assertion beside " + branch)
			}
		}
	}
	for _, rule := range []struct {
		keys  []string
		types []string
	}{
		{[]string{"properties", "additionalProperties", "propertyNames", "required", "minProperties"}, []string{"object"}},
		{[]string{"items", "minItems", "maxItems", "uniqueItems"}, []string{"array"}},
		{[]string{"minLength", "maxLength", "pattern", "format"}, []string{"string"}},
		{[]string{"minimum", "maximum", "exclusiveMinimum"}, []string{"integer", "number"}},
	} {
		for _, key := range rule.keys {
			if _, exists := node[key]; exists && !hasString(rule.types, Str(node["type"])) {
				return fail("unsupported implicit type for " + key)
			}
		}
	}
	return nil
}

// ValidateSchemaDefinition is a fail-closed vocabulary and shape gate. A future
// canonical assertion cannot silently become an ignored annotation in Go.
// Unsupported combinations require implementing their semantics first.
func ValidateSchemaDefinition(document Object) error {
	var audit func(Object, string) error
	audit = func(node Object, path string) error {
		if node == nil {
			return fmt.Errorf("%s: schema object required", path)
		}
		if err := auditSchemaKeywords(node, path); err != nil {
			return err
		}
		if reference, exists := node["$ref"]; exists {
			text := Str(reference)
			if !strings.HasPrefix(text, "#/$defs/") || Map(Map(document["$defs"])[strings.TrimPrefix(text, "#/$defs/")]) == nil {
				return fmt.Errorf("%s: unknown local schema reference", path)
			}
		}
		for _, key := range []string{"$defs", "properties"} {
			if value, exists := node[key]; exists {
				children := Map(value)
				if children == nil {
					return fmt.Errorf("%s.%s: object required", path, key)
				}
				for name, child := range children {
					if err := audit(Map(child), path+"."+key+"."+name); err != nil {
						return err
					}
				}
			}
		}
		for _, key := range []string{"items", "propertyNames"} {
			if value, exists := node[key]; exists {
				if err := audit(Map(value), path+"."+key); err != nil {
					return err
				}
			}
		}
		if value, exists := node["additionalProperties"]; exists {
			if _, boolean := value.(bool); !boolean {
				if err := audit(Map(value), path+".additionalProperties"); err != nil {
					return err
				}
			}
		}
		for _, key := range []string{"anyOf", "oneOf"} {
			if value, exists := node[key]; exists {
				children := Slice(value)
				if len(children) == 0 {
					return fmt.Errorf("%s.%s: nonempty schema array required", path, key)
				}
				for index, child := range children {
					if err := audit(Map(child), fmt.Sprintf("%s.%s[%d]", path, key, index)); err != nil {
						return err
					}
				}
			}
		}
		if value, exists := node["required"]; exists {
			values := Slice(value)
			if values == nil {
				return fmt.Errorf("%s.required: array required", path)
			}
			for _, name := range values {
				if _, ok := name.(string); !ok {
					return fmt.Errorf("%s.required: property name required", path)
				}
			}
		}
		if value, exists := node["enum"]; exists && len(Slice(value)) == 0 {
			return fmt.Errorf("%s.enum: nonempty array required", path)
		}
		if value, exists := node["uniqueItems"]; exists {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s.uniqueItems: boolean required", path)
			}
		}
		for _, key := range []string{"minProperties", "minItems", "maxItems", "minLength", "maxLength"} {
			if value, exists := node[key]; exists {
				n, ok := Numeric(value)
				if !schemaInteger(value) || !ok || n < 0 {
					return fmt.Errorf("%s.%s: nonnegative integer required", path, key)
				}
			}
		}
		for _, key := range []string{"minimum", "maximum", "exclusiveMinimum"} {
			if value, exists := node[key]; exists {
				if _, ok := schemaNumber(value); !ok {
					return fmt.Errorf("%s.%s: numeric bound required", path, key)
				}
			}
		}
		if value, exists := node["pattern"]; exists {
			pattern, ok := value.(string)
			if !ok {
				return fmt.Errorf("%s.pattern: string required", path)
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("%s.pattern: unsupported expression: %w", path, err)
			}
		}
		return nil
	}
	return audit(document, "$")
}
