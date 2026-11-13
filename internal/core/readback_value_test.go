package core

import (
	"math"
	"testing"
)

func TestReadbackValuesEqual(t *testing.T) {
	type engineering float64
	for _, tt := range []struct {
		name      string
		want, got interface{}
		equal     bool
	}{
		{"negative zero", 0, math.Copysign(0, -1), true},
		{"negative zero float32", float32(math.Copysign(0, -1)), uint64(0), true},
		{"numeric alias", engineering(1.5), float64(1.5), true},
		{"integer types", int16(12), uint64(12), true},
		{"negative versus unsigned", int64(-1), uint64(math.MaxUint64), false},
		{"large adjacent integers", uint64(9007199254740992), uint64(9007199254740993), false},
		{"large int versus rounded float", int64(9007199254740993), float64(9007199254740992), false},
		{"maximum uint", uint64(math.MaxUint64), uint64(math.MaxUint64), true},
		{"maximum uint versus float", uint64(math.MaxUint64), float64(math.MaxUint64), false},
		{"different fractions", 1.25, 1.5, false},
		{"no implicit tolerance", 1.0, math.Nextafter(1, 2), false},
		{"same text", "ready", "ready", true},
		{"different text", "01", "1", false},
		{"legacy numeric text", "12", int64(12), true},
		{"boolean", true, true, true},
		{"different boolean", true, false, false},
		{"nil", nil, nil, true},
		{"nil versus zero", nil, 0, false},
		{"nan", math.NaN(), math.NaN(), false},
		{"infinity", math.Inf(1), math.Inf(1), false},
		{"nonfinite text", "NaN", math.NaN(), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := readbackValuesEqual(tt.want, tt.got); got != tt.equal {
				t.Fatalf("equal(%#v, %#v)=%v want %v", tt.want, tt.got, got, tt.equal)
			}
		})
	}
}
