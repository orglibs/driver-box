package core

import (
	"math"
	"testing"

	"github.com/orglibs/driver-box/v2/pkg/config"
	"github.com/orglibs/driver-box/v2/pkg/convutil"
)

func TestDivideStrings(t *testing.T) {
	tests := []struct {
		name      string
		value     interface{}
		scale     float64
		want      float64
		expectErr bool
	}{
		{
			name:      "float64 zero",
			value:     float64(0),
			scale:     0.1,
			want:      0,
			expectErr: false,
		},
		{
			name:      "int zero",
			value:     int(0),
			scale:     0.1,
			want:      0,
			expectErr: false,
		},
		{
			name:      "int64 zero",
			value:     int64(0),
			scale:     0.1,
			want:      0,
			expectErr: false,
		},
		{
			name:      "float64 division",
			value:     float64(10),
			scale:     2.0,
			want:      5.0,
			expectErr: false,
		},
		{
			name:      "int division",
			value:     int(10),
			scale:     2.0,
			want:      5.0,
			expectErr: false,
		},
		{
			name:      "int64 division",
			value:     int64(10),
			scale:     2.0,
			want:      5.0,
			expectErr: false,
		},
		{
			name:      "unsupported string type",
			value:     "10",
			scale:     2.0,
			want:      0,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := divideStrings(tt.value, tt.scale)
			if (err != nil) != tt.expectErr {
				t.Fatalf("divideStrings(%v, %v) error = %v, expectErr %v", tt.value, tt.scale, err, tt.expectErr)
			}
			if !tt.expectErr && math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("divideStrings(%v, %v) = %v, want %v", tt.value, tt.scale, got, tt.want)
			}
		})
	}
}

func TestPointValueAndDivideIntegration(t *testing.T) {
	// 模拟针对带 scale 的 float 点位写入 0 的完整处理链条
	inputs := []interface{}{0, float64(0), "0", float32(0)}
	scale := 10.0

	for _, input := range inputs {
		val, err := convutil.PointValue(input, config.ValueType_Float)
		if err != nil {
			t.Fatalf("PointValue failed for input %v: %v", input, err)
		}

		res, err := divideStrings(val, scale)
		if err != nil {
			t.Fatalf("divideStrings failed for input %v (PointValue output %T): %v", input, val, err)
		}

		if res != 0 {
			t.Errorf("expected result 0, got %v", res)
		}
	}
}
