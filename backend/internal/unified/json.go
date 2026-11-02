// Package unified implements the language-independent unified-v1 contract.
// The wire tree deliberately preserves absent fields, explicit null, false,
// zero and exact JSON integers. ValidateEnvelope supplies the schema and
// semantic boundary; platform parsers may not publish unchecked trees.
package unified

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

type Object = map[string]any
type Array = []any

const SchemaVersion = "1.0"

// DecodeValue never routes upstream IDs through a float64.
func DecodeValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var tail any
	if err := dec.Decode(&tail); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON data")
	}
	return value, nil
}

func Decode(data []byte) (Object, error) {
	value, err := DecodeValue(data)
	if err != nil {
		return nil, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON object required")
	}
	return obj, nil
}

func Encode(value any) ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func Map(value any) Object { obj, _ := value.(map[string]any); return obj }

func Slice(value any) Array {
	switch values := value.(type) {
	case []any:
		return values
	case []Object:
		out := make(Array, len(values))
		for i, value := range values {
			out[i] = value
		}
		return out
	case []string:
		out := make(Array, len(values))
		for i, value := range values {
			out[i] = value
		}
		return out
	default:
		return nil
	}
}

func Str(value any) string { text, _ := value.(string); return text }

var numericString = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// Numeric mirrors numeric input acceptance at the private parser boundary.
// Schema validation must additionally check the actual wire value's type.
func Numeric(value any) (float64, bool) {
	var n float64
	var err error
	switch v := value.(type) {
	case json.Number:
		n, err = v.Float64()
	case string:
		v = strings.Trim(v, " \t\r\n\v\f")
		if !numericString.MatchString(v) {
			return 0, false
		}
		n, err = strconv.ParseFloat(v, 64)
	case float64:
		n = v
	case float32:
		n = float64(v)
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case int32:
		n = float64(v)
	case uint:
		n = float64(v)
	case uint64:
		n = float64(v)
	default:
		return 0, false
	}
	return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
}

func CloneValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(Object, len(v))
		for key, child := range v {
			out[key] = CloneValue(child)
		}
		return out
	case []any:
		out := make(Array, len(v))
		for i, child := range v {
			out[i] = CloneValue(child)
		}
		return out
	case []Object:
		out := make(Array, len(v))
		for i, child := range v {
			out[i] = CloneValue(child)
		}
		return out
	case []string:
		out := make(Array, len(v))
		for i, child := range v {
			out[i] = child
		}
		return out
	default:
		return value
	}
}

func Clone(value Object) Object {
	if value == nil {
		return nil
	}
	return CloneValue(value).(map[string]any)
}

// Equal compares JSON values without conflating absent/null or false/zero.
// Numeric spellings such as 30 and 30.0 denote the same JSON numeric value.
func Equal(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if av, ok := a.(map[string]any); ok {
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for key, child := range av {
			other, exists := bv[key]
			if !exists || !Equal(child, other) {
				return false
			}
		}
		return true
	}
	if av := Slice(a); av != nil {
		bv := Slice(b)
		if bv == nil || len(av) != len(bv) {
			return false
		}
		for i, child := range av {
			if !Equal(child, bv[i]) {
				return false
			}
		}
		return true
	}
	if av, ok := numberText(a); ok {
		bv, ok := numberText(b)
		if !ok {
			return false
		}
		ar, aok := new(big.Rat).SetString(av)
		br, bok := new(big.Rat).SetString(bv)
		return aok && bok && ar.Cmp(br) == 0
	}
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	default:
		return false
	}
}

func numberText(value any) (string, bool) {
	switch v := value.(type) {
	case json.Number:
		return v.String(), true
	case int:
		return strconv.Itoa(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case uint:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), !math.IsNaN(v) && !math.IsInf(v, 0)
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32), !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0)
	default:
		return "", false
	}
}
