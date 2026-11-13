package core

import (
	"fmt"
	"math"
	"math/big"
	"reflect"
)

// Compare numeric values without formatting or narrowing all integers to
// float64: +0 and -0 are equal, but distinct integers above 2^53 stay distinct.
// Preserve the existing textual comparison for nonnumeric values.
func readbackValuesEqual(expected, actual interface{}) bool {
	a, an := readbackNumber(expected)
	b, bn := readbackNumber(actual)
	if (an && a == nil) || (bn && b == nil) {
		return false // NaN/Inf cannot confirm a finite device setpoint.
	}
	if an && bn {
		return a.Cmp(b) == 0
	}
	return fmt.Sprint(expected) == fmt.Sprint(actual)
}

func readbackNumber(value interface{}) (*big.Rat, bool) {
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return new(big.Rat).SetInt64(v.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(v.Uint())), true
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, true
		}
		return new(big.Rat).SetFloat64(f), true
	default:
		return nil, false
	}
}
