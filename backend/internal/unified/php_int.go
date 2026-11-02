package unified

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// PHPInt is the direct integer conversion used after is_numeric checks in the
// frozen PHP implementation. Integer tokens/strings take an exact path; round()
// callers must continue to use PHPRound instead of this truncation helper.
func PHPInt(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case uint:
		if uint64(v) <= math.MaxInt64 {
			return int64(v), true
		}
		return phpFloatInt(float64(v))
	case uint64:
		if v <= math.MaxInt64 {
			return int64(v), true
		}
		return phpFloatInt(float64(v))
	case json.Number:
		if integerSpelling.MatchString(v.String()) {
			if n, err := v.Int64(); err == nil {
				return n, true
			}
		}
		n, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return phpFloatInt(n)
	case string:
		v = strings.Trim(v, " \t\r\n\v\f")
		if !numericString.MatchString(v) {
			return 0, false
		}
		if !strings.ContainsAny(v, ".eE") {
			n, err := strconv.ParseInt(v, 10, 64)
			if err == nil {
				return n, true
			}
			if strings.HasPrefix(v, "-") {
				return math.MinInt64, true
			}
			return math.MaxInt64, true
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		// PHP caps numeric-string conversions, unlike a direct float cast.
		if n >= float64(math.MaxInt64) {
			return math.MaxInt64, true
		}
		if n <= float64(math.MinInt64) {
			return math.MinInt64, true
		}
		return int64(n), true
	case float64:
		return phpFloatInt(v)
	case float32:
		return phpFloatInt(float64(v))
	default:
		return 0, false
	}
}

func phpFloatInt(value float64) (int64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	value = math.Trunc(value)
	if value >= 9223372036854775808.0 || value < -9223372036854775808.0 {
		value = math.Mod(value, 18446744073709551616.0)
		if value >= 9223372036854775808.0 {
			value -= 18446744073709551616.0
		}
		if value < -9223372036854775808.0 {
			value += 18446744073709551616.0
		}
	}
	return int64(value), true
}
