package convutil

import (
	"math"
	"testing"

	"github.com/orglibs/driver-box/v2/pkg/config"
)

func TestOffsetReadAndWrite(t *testing.T) {
	for _, tt := range []struct {
		name, kind, rawType string
		scale, offset       float64
		raw, engineering    interface{}
	}{
		{"temperature", "float", "Uint16", .1, -40, uint16(650), float64(25)},
		{"negative engineering", "float", "Uint16", .1, -40, uint16(350), float64(-5)},
		{"zero engineering", "float", "Uint16", .1, -40, uint16(400), float64(0)},
		{"base value", "int", "Uint16", 0, -10000, uint16(10025), int64(25)},
		{"negative scale", "float", "Int16", -2, .5, int16(-10), float64(20.5)},
		{"fractional offset integer output", "int", "Int16", .5, .5, int16(3), int64(2)},
		{"decimal inverse", "float", "Uint16", .1, .2, uint16(1), float64(.3)},
		{"above float exact integer", "int", "Int64", 1, -1, int64(9007199254740993), int64(9007199254740992)},
		{"signed limit", "int", "Int64", 1, -1, int64(math.MaxInt64), int64(math.MaxInt64 - 1)},
		{"unsigned limit", "int", "Uint64", 1, -9223372036854775808., uint64(math.MaxUint64), int64(math.MaxInt64)},
		{"float register", "float", "Float32", .5, .25, float32(1.5), float64(1)},
		{"large float32", "float", "Float32", 1, 1, float32(1e20), float64(float32(1e20))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := config.Point{"valueType": tt.kind, "rawType": tt.rawType, "scale": tt.scale, "offset": tt.offset}
			got, err := ReadPointOffset(tt.raw, p)
			if err != nil || got != tt.engineering {
				t.Fatalf("read=%#v,%v want %#v", got, err, tt.engineering)
			}
			encoded, err := WritePointOffset(got, p)
			if err != nil {
				t.Fatal(err)
			}
			// Compare exact numeric magnitudes without converting integers to float64.
			want, _ := offsetNumber(tt.raw)
			actual, _ := offsetNumber(encoded)
			if actual.Cmp(want) != 0 {
				t.Fatalf("write=%v want %v", actual, want)
			}
		})
	}
}

func TestOffsetNumericStrings(t *testing.T) {
	p := config.Point{"valueType": "float", "offset": 1}
	for _, value := range []string{"+25", "25.", "025", ".25", "2.5e1"} {
		if _, err := ReadPointOffset(value, p); err != nil {
			t.Fatalf("numeric string %q: %v", value, err)
		}
	}
}

func TestOffsetPreservesDecodedFloat32(t *testing.T) {
	p := config.Point{"valueType": "float", "rawType": "Float32", "offset": 1}
	for _, raw := range []float32{.1, -.1, 1e20} {
		got, err := ReadPointOffset(raw, p)
		want := float64(raw) + 1
		if err != nil || got != want {
			t.Fatalf("float32 %v: got %v,%v; want %v", raw, got, err, want)
		}
	}
}

func TestOffsetRejectsInvalidResults(t *testing.T) {
	for _, tt := range []struct {
		name  string
		point config.Point
		value interface{}
		write bool
	}{
		{"fractional raw", config.Point{"valueType": "float", "rawType": "Uint16", "scale": .1, "offset": -40}, 25.05, true},
		{"negative unsigned", config.Point{"valueType": "float", "rawType": "Uint16", "offset": 10}, 0, true},
		{"unsigned overflow", config.Point{"valueType": "float", "rawType": "Uint16", "offset": -1}, 65535, true},
		{"signed overflow", config.Point{"valueType": "float", "rawType": "Int16", "offset": -1}, 32767, true},
		{"integer engineering fraction", config.Point{"valueType": "int", "rawType": "Uint16", "offset": 1}, 1.5, true},
		{"fractional engineering read", config.Point{"valueType": "int", "offset": .5}, 1, false},
		{"signed read overflow", config.Point{"valueType": "int", "offset": 1}, int64(math.MaxInt64), false},
		{"float read overflow", config.Point{"valueType": "float", "scale": 2, "offset": 1}, math.MaxFloat64, false},
		{"float32 write overflow", config.Point{"valueType": "float", "rawType": "Float32", "offset": 1}, math.MaxFloat64, true},
		{"bad scale", config.Point{"valueType": "float", "scale": ".1", "offset": 1}, 10, true},
		{"nonfinite input", config.Point{"valueType": "float", "offset": 1}, math.NaN(), false},
		{"boolean input", config.Point{"valueType": "float", "offset": 1}, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.write {
				_, err = WritePointOffset(tt.value, tt.point)
			} else {
				_, err = ReadPointOffset(tt.value, tt.point)
			}
			if err == nil {
				t.Fatal("accepted invalid conversion")
			}
		})
	}
}
