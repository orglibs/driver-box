package convutil

import (
	"reflect"
	"testing"

	"github.com/orglibs/driver-box/v2/pkg/config"
)

func TestPointValue_ZeroFloat(t *testing.T) {
	tests := []struct {
		name      string
		input     interface{}
		valueType config.ValueType
		wantType  reflect.Kind
		wantVal   interface{}
	}{
		{
			name:      "int 0 to float type",
			input:     0,
			valueType: config.ValueType_Float,
			wantType:  reflect.Float64,
			wantVal:   float64(0),
		},
		{
			name:      "float64 0.0 to float type",
			input:     float64(0),
			valueType: config.ValueType_Float,
			wantType:  reflect.Float64,
			wantVal:   float64(0),
		},
		{
			name:      "string 0 to float type",
			input:     "0",
			valueType: config.ValueType_Float,
			wantType:  reflect.Float64,
			wantVal:   float64(0),
		},
		{
			name:      "int 0 to int type",
			input:     0,
			valueType: config.ValueType_Int,
			wantType:  reflect.Int64,
			wantVal:   int64(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PointValue(tt.input, tt.valueType)
			if err != nil {
				t.Fatalf("PointValue(%v, %v) returned unexpected error: %v", tt.input, tt.valueType, err)
			}
			if reflect.TypeOf(got).Kind() != tt.wantType {
				t.Errorf("PointValue(%v, %v) returned type %T, want %v", tt.input, tt.valueType, got, tt.wantType)
			}
			if got != tt.wantVal {
				t.Errorf("PointValue(%v, %v) = %v, want %v", tt.input, tt.valueType, got, tt.wantVal)
			}
		})
	}
}
