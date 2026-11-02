package config

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Offset is expressed in engineering units, after scaling. Only this new
// option and its scale are validated here; omitted/zero offsets retain legacy conversions.
func (p Point) Offset() (float64, error) {
	raw, exists := p["offset"]
	if !exists {
		return 0, nil
	}
	offset, err := finitePointNumber(raw, "offset")
	if err != nil || offset == 0 {
		return offset, err
	}
	valueType, _ := p["valueType"].(string)
	if valueType != string(ValueType_Int) && valueType != string(ValueType_Float) {
		return 0, fmt.Errorf("nonzero offset requires valueType int or float")
	}
	rawType, _ := p["rawType"].(string)
	table, _ := p["primaryTable"].(string)
	if strings.EqualFold(rawType, "String") || strings.EqualFold(rawType, "Bool") ||
		strings.EqualFold(table, "COIL") || strings.EqualFold(table, "DISCRETE_INPUT") {
		return 0, fmt.Errorf("nonzero offset is not allowed on String, boolean or coil points")
	}
	for _, field := range []string{"bit", "bitLen"} {
		if raw, exists := p[field]; exists {
			number, err := finitePointNumber(raw, field)
			if err != nil || number != 0 {
				return 0, fmt.Errorf("nonzero offset is not allowed on bit-extraction points")
			}
		}
	}
	if _, err := p.ConversionScale(); err != nil {
		return 0, err
	}
	return offset, nil
}

// ConversionScale validates the scale of a point using nonzero offset. Zero
// retains this project's established meaning of no scaling (factor one).
func (p Point) ConversionScale() (float64, error) {
	raw, exists := p["scale"]
	if !exists {
		return 1, nil
	}
	scale, err := finitePointNumber(raw, "scale")
	if err != nil {
		return 0, err
	}
	if scale == 0 {
		return 1, nil
	}
	return scale, nil
}

func finitePointNumber(raw interface{}, field string) (float64, error) {
	var number float64
	// JSON-decoded point maps contain float64; programmatic maps commonly
	// contain integers. Avoid JSON allocation on these sampling hot paths.
	switch value := raw.(type) {
	case float64:
		number = value
	case int:
		number = float64(value)
	case int8:
		number = float64(value)
	case int16:
		number = float64(value)
	case int32:
		number = float64(value)
	case int64:
		number = float64(value)
	case uint:
		number = float64(value)
	case uint8:
		number = float64(value)
	case uint16:
		number = float64(value)
	case uint32:
		number = float64(value)
	case uint64:
		number = float64(value)
	default:
		// Preserve JSON semantics for float32, json.Number, named numeric
		// types and custom marshalers; strings, null and booleans stay invalid.
		data, err := json.Marshal(raw)
		var decoded float64
		if err != nil || string(data) == "null" || json.Unmarshal(data, &decoded) != nil {
			return 0, fmt.Errorf("%s must be a finite JSON number", field)
		}
		number = decoded
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, fmt.Errorf("%s must be a finite JSON number", field)
	}
	return number, nil
}
