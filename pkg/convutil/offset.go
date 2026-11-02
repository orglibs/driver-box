package convutil

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/orglibs/driver-box/v2/pkg/config"
)

// ReadPointOffset applies raw*scale+offset before converting the engineering
// type. Decimal arithmetic keeps integer registers exact, including >2^53.
// Callers keep their original path when Offset is zero or Lua owns conversion.
func ReadPointOffset(value interface{}, point config.Point) (interface{}, error) {
	result, err := transformOffset(value, point, false)
	if err != nil {
		return nil, err
	}
	if point.ValueType() == config.ValueType_Int {
		return offsetInteger(result, true, 64)
	}
	return offsetFloat(result)
}

// WritePointOffset inverses the read transform. Integer wire types must be
// integral and within range; this new path never rounds register values.
func WritePointOffset(value interface{}, point config.Point) (interface{}, error) {
	result, err := transformOffset(value, point, true)
	if err != nil {
		return nil, err
	}
	return offsetRawValue(result, point)
}

// OffsetRawWriteValue also protects protocol Encode callers that supply raw
// values directly. It validates without applying the inverse a second time.
func OffsetRawWriteValue(value interface{}, point config.Point) (interface{}, error) {
	result, err := offsetNumber(value)
	if err != nil {
		return nil, err
	}
	return offsetRawValue(result, point)
}

func transformOffset(value interface{}, point config.Point, inverse bool) (*big.Rat, error) {
	offset, err := point.Offset()
	if err != nil {
		return nil, err
	}
	scale, err := point.ConversionScale()
	if err != nil {
		return nil, err
	}
	result, err := offsetNumber(value)
	if err != nil {
		return nil, err
	}
	bias, _ := offsetNumber(offset)
	factor, _ := offsetNumber(scale)
	if inverse {
		// Validate the requested engineering value before inversing it.
		if point.ValueType() == config.ValueType_Int {
			if _, err := offsetInteger(result, true, 64); err != nil {
				return nil, err
			}
		}
		result.Sub(result, bias)
		result.Quo(result, factor)
		return result, nil
	}
	result.Mul(result, factor)
	result.Add(result, bias)
	return result, nil
}

func offsetNumber(value interface{}) (*big.Rat, error) {
	if v, ok := value.(float32); ok {
		// Preserve the decoded binary value, as in the framework's Float64
		// conversion. Formatting float32 directly would round it to fewer
		// decimal digits and can change large register values substantially.
		return offsetNumber(float64(v))
	}
	// Integral floats may be large powers of two whose shortest decimal
	// formatting differs from their exact integer magnitude.
	if v, ok := value.(float64); ok && !math.IsInf(v, 0) && v == math.Trunc(v) {
		return new(big.Rat).SetFloat64(v), nil
	}
	var text string
	switch v := value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float64, json.Number, string:
		text = fmt.Sprint(v)
	default:
		return nil, fmt.Errorf("offset conversion requires a number, got %T", value)
	}
	// Reject rational/hex syntax and non-finite floating point values.
	floating, err := strconv.ParseFloat(text, 64)
	if err == nil && !math.IsNaN(floating) && !math.IsInf(floating, 0) && !strings.Contains(strings.ToLower(text), "0x") {
		if number, ok := new(big.Rat).SetString(text); ok {
			return number, nil
		}
	}
	return nil, fmt.Errorf("invalid number for offset conversion")
}

func offsetRawValue(value *big.Rat, point config.Point) (interface{}, error) {
	raw, _ := point["rawType"].(string)
	switch strings.ToUpper(raw) {
	case "INT8", "INT16", "INT32", "INT64", "UINT8", "UINT16", "UINT32", "UINT64":
		signed := !strings.HasPrefix(strings.ToUpper(raw), "U")
		digits := strings.TrimPrefix(strings.TrimPrefix(strings.ToUpper(raw), "U"), "INT")
		bits, _ := strconv.Atoi(digits)
		return offsetInteger(value, signed, bits)
	case "FLOAT32":
		v, err := offsetFloat(value)
		if err == nil && math.Abs(v) > math.MaxFloat32 {
			return nil, fmt.Errorf("offset result exceeds Float32 range")
		}
		return v, err
	case "FLOAT64":
		return offsetFloat(value)
	default:
		if point.ValueType() == config.ValueType_Int {
			return offsetInteger(value, true, 64)
		}
		return offsetFloat(value)
	}
}

func offsetInteger(value *big.Rat, signed bool, bits int) (interface{}, error) {
	if !value.IsInt() {
		return nil, fmt.Errorf("offset result must be an integer for an integer point/register")
	}
	n := value.Num()
	if signed {
		if !n.IsInt64() {
			return nil, fmt.Errorf("offset result exceeds Int%d range", bits)
		}
		v := n.Int64()
		if bits < 64 && (v < -(int64(1)<<(bits-1)) || v > (int64(1)<<(bits-1))-1) {
			return nil, fmt.Errorf("offset result exceeds Int%d range", bits)
		}
		return v, nil
	}
	if !n.IsUint64() || n.BitLen() > bits {
		return nil, fmt.Errorf("offset result exceeds Uint%d range", bits)
	}
	return n.Uint64(), nil
}

func offsetFloat(value *big.Rat) (float64, error) {
	v, _ := value.Float64()
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("offset result must be finite")
	}
	return v, nil
}
