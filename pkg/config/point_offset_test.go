package config

import (
	"encoding/json"
	"math"
	"testing"
)

func TestPointOffset(t *testing.T) {
	for _, raw := range []interface{}{0, float64(0), 10, -40, .25, json.Number("-4e1")} {
		p := Point{"valueType": "float", "offset": raw}
		if _, err := p.Offset(); err != nil {
			t.Fatalf("offset=%v: %v", raw, err)
		}
	}
	for _, raw := range []interface{}{nil, "10", true, []int{1}, map[string]int{"x": 1}, math.NaN(), math.Inf(1), math.Inf(-1), json.Number("1e309")} {
		if _, err := (Point{"valueType": "float", "offset": raw}).Offset(); err == nil {
			t.Fatalf("accepted offset=%#v", raw)
		}
	}
	if value, err := (Point{}).Offset(); err != nil || value != 0 {
		t.Fatalf("default=%v,%v", value, err)
	}
	for _, fields := range []Point{
		{"valueType": "string"}, {"valueType": "bool"}, {},
		{"valueType": "int", "primaryTable": "COIL"}, {"valueType": "int", "primaryTable": "DISCRETE_INPUT"},
		{"valueType": "int", "primaryTable": "Coil"}, {"valueType": "int", "primaryTable": "coil"},
		{"valueType": "int", "primaryTable": "Discrete_Input"}, {"valueType": "int", "primaryTable": "discrete_input"},
		{"valueType": "int", "bitLen": 1}, {"valueType": "int", "bit": 2},
		{"valueType": "int", "rawType": "sTrInG"}, {"valueType": "int", "rawType": "Bool"},
	} {
		fields["offset"] = 1
		if _, err := fields.Offset(); err == nil {
			t.Fatalf("accepted nonnumeric point: %v", fields)
		}
		fields["offset"] = 0
		if _, err := fields.Offset(); err != nil {
			t.Fatalf("zero must remain compatible: %v", err)
		}
	}
	if _, err := (Point{"valueType": "int", "offset": 1, "bit": 0, "bitLen": 0}).Offset(); err != nil {
		t.Fatal(err)
	}
}

func TestFinitePointNumberCompatibility(t *testing.T) {
	type namedNumber float64
	for _, raw := range []interface{}{
		float64(.1), float32(.1), namedNumber(.1),
		int(-40), int8(-40), int16(-40), int32(-40), int64(math.MinInt64),
		uint(40), uint8(40), uint16(40), uint32(40), uint64(math.MaxUint64),
		json.Number("-4e1"), json.Number("1e309"), nil, "10", true,
		[]int{1}, map[string]int{"value": 1}, math.NaN(), math.Inf(1), math.Inf(-1),
	} {
		// Compare the optimized parser with the previous JSON-only semantics,
		// particularly float32 formatting, large integers and invalid inputs.
		data, marshalErr := json.Marshal(raw)
		var want float64
		valid := marshalErr == nil && string(data) != "null" && json.Unmarshal(data, &want) == nil && !math.IsNaN(want) && !math.IsInf(want, 0)
		got, err := finitePointNumber(raw, "offset")
		if (err == nil) != valid || (valid && got != want) {
			t.Fatalf("%T(%v): got %v,%v; previous parser valid=%v value=%v", raw, raw, got, err, valid, want)
		}
	}
}

func TestOffsetScale(t *testing.T) {
	for _, raw := range []interface{}{nil, 0, 1, .1, -2} {
		p := Point{}
		if raw != nil {
			p["scale"] = raw
		}
		factor, err := p.ConversionScale()
		if err != nil || factor == 0 {
			t.Fatalf("scale=%v: %v,%v", raw, factor, err)
		}
	}
	for _, raw := range []interface{}{nil, "1", true, []int{1}, math.NaN(), math.Inf(1)} {
		if _, err := (Point{"scale": raw}).ConversionScale(); err == nil {
			t.Fatalf("accepted scale=%v", raw)
		}
	}
}

func BenchmarkPointOffset(b *testing.B) {
	for _, tt := range []struct {
		name  string
		point Point
	}{
		{"absent", Point{"valueType": "float"}},
		{"zero", Point{"valueType": "float", "offset": float64(0)}},
		{"float64", Point{"valueType": "float", "offset": -40., "scale": .1}},
		{"int", Point{"valueType": "float", "offset": -40, "scale": 1}},
	} {
		b.Run(tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := tt.point.Offset(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
