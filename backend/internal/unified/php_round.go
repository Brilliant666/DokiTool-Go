package unified

import (
	"math"
	"strconv"
)

// PHPRound preserves PHP 8.3's default HALF_UP behavior, including the decimal
// pre-rounding that math.Round does not perform. For example PHP rounds 0.5005
// to three places as 0.501 even though 0.5005*1000 is slightly below 500.5.
//
// Algorithm reference: _php_math_round and php_round_helper in
// https://github.com/php/php-src/blob/PHP-8.3/ext/standard/math.c
// (reviewed source blob 0c5dd964b4591d8c984fe133cc0a851367a7f710).
// This is the numeric compatibility boundary used by media normalization;
// callers still validate finite/positive values before publishing metadata.
func PHPRound(value float64, precision int) float64 {
	if value == 0 || math.IsInf(value, 0) || math.IsNaN(value) {
		return value
	}
	// Outside float64's decimal exponent range the PHP result is unchanged or
	// zero. Bound precision before negating it or constructing a power/string.
	if precision > 308 {
		return value
	}
	if precision < -308 {
		return math.Copysign(0, value)
	}

	magnitude := math.Abs(value)
	exponent := int(math.Floor(math.Log10(magnitude)))
	// Correct logarithm rounding at exact powers of ten, using the same exact
	// powers that PHP's small-exponent table compares against.
	if magnitude < math.Pow10(exponent) {
		exponent--
	} else if magnitude >= math.Pow10(exponent+1) {
		exponent++
	}
	pre := 14 - exponent
	factor := math.Pow10(absRoundPrecision(precision))
	var scaled float64
	if pre > precision && pre-15 < precision {
		preFactor := math.Pow10(absRoundPrecision(pre))
		if pre >= 0 {
			scaled = value * preFactor
		} else {
			scaled = value / preFactor
		}
		scaled = halfUp(scaled) / math.Pow10(pre-precision)
	} else {
		if precision >= 0 {
			scaled = value * factor
		} else {
			scaled = value / factor
		}
		if math.Abs(scaled) >= 1e15 {
			return value
		}
	}
	scaled = halfUp(scaled)
	if absRoundPrecision(precision) < 23 {
		if precision > 0 {
			return scaled / factor
		}
		return scaled * factor
	}
	// PHP uses decimal conversion for larger places so the final value is the
	// nearest representable float, rather than another rounded multiplication.
	text := strconv.FormatFloat(scaled, 'f', 0, 64) + "e" + strconv.Itoa(-precision)
	result, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(result, 0) || math.IsNaN(result) {
		return value
	}
	return result
}

func halfUp(value float64) float64 {
	if value >= 0 {
		return math.Floor(value + 0.5)
	}
	return math.Ceil(value - 0.5)
}

func absRoundPrecision(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
