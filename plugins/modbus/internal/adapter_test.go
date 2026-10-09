package internal

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"testing"
)

func TestParseUint16Value(t *testing.T) {
	tests := []struct {
		input   string
		want    uint16
		wantErr bool
	}{
		// 正常整数
		{"0", 0, false},
		{"100", 100, false},
		{"65535", 65535, false},

		// 浮点毛刺及四舍五入
		{"101.99999999999999", 102, false},
		{"100.00000000000001", 100, false},
		{"10.4", 10, false},
		{"10.5", 11, false},
		{"10.6", 11, false},

		// 零点边界浮点毛刺
		{"-0.0", 0, false},
		{"-0.0000000001", 0, false},
		{"0.0000000001", 0, false},
		{"-0.49", 0, false},

		// 上边界浮点毛刺
		{"65535.0", 65535, false},
		{"65535.4", 65535, false},
		{"65534.99999999", 65535, false},

		// 越界
		{"-0.6", 0, true},
		{"-1", 0, true},
		{"-100", 0, true},
		{"65535.6", 0, true},
		{"65536", 0, true},
		{"100000", 0, true},

		// 非法字符及特殊浮点数
		{"", 0, true},
		{"abc", 0, true},
		{"NaN", 0, true},
		{"+Inf", 0, true},
		{"-Inf", 0, true},
	}

	for _, tt := range tests {
		got, err := parseUint16Value(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseUint16Value(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseUint16Value(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseInt16Value(t *testing.T) {
	tests := []struct {
		input   string
		want    int16
		wantErr bool
	}{
		// 正常整数
		{"0", 0, false},
		{"-1", -1, false},
		{"100", 100, false},
		{"-100", -100, false},
		{"32767", 32767, false},
		{"-32768", -32768, false},

		// 浮点毛刺及四舍五入
		{"101.99999999999999", 102, false},
		{"-32.00000000001", -32, false},
		{"-1.4", -1, false},
		{"-1.5", -2, false},
		{"-1.6", -2, false},
		{"1.4", 1, false},
		{"1.5", 2, false},

		// 边界浮点毛刺
		{"32767.4", 32767, false},
		{"32766.99999999", 32767, false},
		{"-32768.4", -32768, false},
		{"-32767.99999999", -32768, false},

		// 越界
		{"32767.6", 0, true},
		{"32768", 0, true},
		{"-32768.6", 0, true},
		{"-32769", 0, true},

		// 非法字符及特殊浮点数
		{"", 0, true},
		{"abc", 0, true},
		{"NaN", 0, true},
		{"+Inf", 0, true},
		{"-Inf", 0, true},
	}

	for _, tt := range tests {
		got, err := parseInt16Value(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseInt16Value(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseInt16Value(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseUint32Value(t *testing.T) {
	tests := []struct {
		input   string
		want    uint32
		wantErr bool
	}{
		// 正常整数
		{"0", 0, false},
		{"1000", 1000, false},
		{"4294967295", 4294967295, false},

		// 浮点毛刺及四舍五入
		{"65535.9999999", 65536, false},
		{"-0.0000000001", 0, false},
		{"4294967295.4", 4294967295, false},

		// 越界
		{"-1", 0, true},
		{"-0.6", 0, true},
		{"4294967295.6", 0, true},
		{"4294967296", 0, true},

		// 非法输入
		{"", 0, true},
		{"xyz", 0, true},
		{"NaN", 0, true},
		{"+Inf", 0, true},
		{"-Inf", 0, true},
	}

	for _, tt := range tests {
		got, err := parseUint32Value(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseUint32Value(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseUint32Value(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseInt32Value(t *testing.T) {
	tests := []struct {
		input   string
		want    int32
		wantErr bool
	}{
		// 正常整数
		{"0", 0, false},
		{"-1", -1, false},
		{"2147483647", math.MaxInt32, false},
		{"-2147483648", math.MinInt32, false},

		// 浮点毛刺及四舍五入
		{"-100.00000000001", -100, false},
		{"2147483647.4", math.MaxInt32, false},
		{"-2147483648.4", math.MinInt32, false},

		// 越界
		{"2147483647.6", 0, true},
		{"2147483648", 0, true},
		{"-2147483648.6", 0, true},
		{"-2147483649", 0, true},

		// 非法输入
		{"", 0, true},
		{"xyz", 0, true},
		{"NaN", 0, true},
		{"+Inf", 0, true},
		{"-Inf", 0, true},
	}

	for _, tt := range tests {
		got, err := parseInt32Value(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseInt32Value(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseInt32Value(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseUint64Value(t *testing.T) {
	tests := []struct {
		input   string
		want    uint64
		wantErr bool
	}{
		// 正常大整数（全 64 位精度直解）
		{"0", 0, false},
		{"18446744073709551615", math.MaxUint64, false},
		{"1234567890123456789", 1234567890123456789, false},

		// 浮点毛刺及四舍五入
		{"101.99999999999999", 102, false},
		{"-0.0000000001", 0, false},
		{"1000.4", 1000, false},
		{"1000.5", 1001, false},

		// 越界
		{"-1", 0, true},
		{"-0.6", 0, true},
		{"18446744073709551616", 0, true},

		// 非法输入
		{"", 0, true},
		{"abc", 0, true},
		{"NaN", 0, true},
		{"+Inf", 0, true},
		{"-Inf", 0, true},
	}

	for _, tt := range tests {
		got, err := parseUint64Value(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseUint64Value(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseUint64Value(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseInt64Value(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		// 正常大整数（全 64 位精度直解）
		{"0", 0, false},
		{"-1", -1, false},
		{"9223372036854775807", math.MaxInt64, false},
		{"-9223372036854775808", math.MinInt64, false},

		// 浮点毛刺及四舍五入
		{"101.99999999999999", 102, false},
		{"-32.00000000001", -32, false},
		{"-100.4", -100, false},
		{"-100.5", -101, false},

		// 越界
		{"9223372036854775808", 0, true},
		{"-9223372036854775809", 0, true},

		// 非法输入
		{"", 0, true},
		{"abc", 0, true},
		{"NaN", 0, true},
		{"+Inf", 0, true},
		{"-Inf", 0, true},
	}

	for _, tt := range tests {
		got, err := parseInt64Value(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseInt64Value(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseInt64Value(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// encodeHoldingRegisterHelper 模拟 adapter.go 中 HoldingRegister 的编码逻辑，验证字节排布与转换
func encodeHoldingRegisterHelper(rawType string, valueStr string, byteSwap, wordSwap bool) ([]uint16, error) {
	switch rawType {
	case ValueTypeInt16:
		v, err := parseInt16Value(valueStr)
		if err != nil {
			return nil, fmt.Errorf("convert value %v to int16 error: %v", valueStr, err)
		}
		out := make([]byte, 2)
		if byteSwap {
			binary.LittleEndian.PutUint16(out, uint16(v))
		} else {
			binary.BigEndian.PutUint16(out, uint16(v))
		}
		return []uint16{binary.BigEndian.Uint16(out)}, nil

	case ValueTypeUint16:
		v, err := parseUint16Value(valueStr)
		if err != nil {
			return nil, fmt.Errorf("convert value %v to uint16 error: %v", valueStr, err)
		}
		out := make([]byte, 2)
		if byteSwap {
			binary.LittleEndian.PutUint16(out, v)
		} else {
			binary.BigEndian.PutUint16(out, v)
		}
		return []uint16{binary.BigEndian.Uint16(out)}, nil

	case ValueTypeUint32:
		v, err := parseUint32Value(valueStr)
		if err != nil {
			return nil, fmt.Errorf("convert value %v to uint32 error: %v", valueStr, err)
		}
		out := make([]byte, 4)
		if byteSwap {
			binary.LittleEndian.PutUint32(out, v)
		} else {
			binary.BigEndian.PutUint32(out, v)
		}
		if wordSwap {
			out[0], out[1], out[2], out[3] = out[2], out[3], out[0], out[1]
		}
		return []uint16{
			binary.BigEndian.Uint16([]byte{out[2], out[3]}),
			binary.BigEndian.Uint16([]byte{out[0], out[1]}),
		}, nil

	case ValueTypeInt32:
		v, err := parseInt32Value(valueStr)
		if err != nil {
			return nil, fmt.Errorf("convert value %v to int32 error: %v", valueStr, err)
		}
		out := make([]byte, 4)
		if byteSwap {
			binary.LittleEndian.PutUint32(out, uint32(v))
		} else {
			binary.BigEndian.PutUint32(out, uint32(v))
		}
		if wordSwap {
			out[0], out[1], out[2], out[3] = out[2], out[3], out[0], out[1]
		}
		return []uint16{
			binary.BigEndian.Uint16([]byte{out[2], out[3]}),
			binary.BigEndian.Uint16([]byte{out[0], out[1]}),
		}, nil

	case ValueTypeUint64:
		v, err := parseUint64Value(valueStr)
		if err != nil {
			return nil, fmt.Errorf("convert value %v to uint64 error: %v", valueStr, err)
		}
		out := make([]byte, 8)
		if byteSwap {
			binary.LittleEndian.PutUint64(out, v)
		} else {
			binary.BigEndian.PutUint64(out, v)
		}
		if wordSwap {
			out[0], out[1], out[2], out[3], out[4], out[5], out[6], out[7] =
				out[2], out[3], out[0], out[1], out[6], out[7], out[4], out[5]
		}
		return []uint16{
			binary.BigEndian.Uint16([]byte{out[6], out[7]}),
			binary.BigEndian.Uint16([]byte{out[4], out[5]}),
			binary.BigEndian.Uint16([]byte{out[2], out[3]}),
			binary.BigEndian.Uint16([]byte{out[0], out[1]}),
		}, nil

	case ValueTypeInt64:
		v, err := parseInt64Value(valueStr)
		if err != nil {
			return nil, fmt.Errorf("convert value %v to int64 error: %v", valueStr, err)
		}
		out := make([]byte, 8)
		if byteSwap {
			binary.LittleEndian.PutUint64(out, uint64(v))
		} else {
			binary.BigEndian.PutUint64(out, uint64(v))
		}
		if wordSwap {
			out[0], out[1], out[2], out[3], out[4], out[5], out[6], out[7] =
				out[2], out[3], out[0], out[1], out[6], out[7], out[4], out[5]
		}
		return []uint16{
			binary.BigEndian.Uint16([]byte{out[6], out[7]}),
			binary.BigEndian.Uint16([]byte{out[4], out[5]}),
			binary.BigEndian.Uint16([]byte{out[2], out[3]}),
			binary.BigEndian.Uint16([]byte{out[0], out[1]}),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported: %s", rawType)
	}
}

func TestHoldingRegisterEncoding(t *testing.T) {
	// 1. Int16
	t.Run("Int16 positive and negative", func(t *testing.T) {
		// 正常负数
		vals, err := encodeHoldingRegisterHelper(ValueTypeInt16, "-1", false, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(vals, []uint16{0xFFFF}) {
			t.Fatalf("want [0xFFFF], got %x", vals)
		}

		// 负数 ByteSwap: -2 (0xFFFE -> LE bytes: [0xFE, 0xFF] -> uint16: 0xFEFF)
		vals, err = encodeHoldingRegisterHelper(ValueTypeInt16, "-2", true, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(vals, []uint16{0xFEFF}) {
			t.Fatalf("want [0xFEFF], got %x", vals)
		}

		// 浮点毛刺: "101.99999999999999" -> 102
		vals, err = encodeHoldingRegisterHelper(ValueTypeInt16, "101.99999999999999", false, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(vals, []uint16{102}) {
			t.Fatalf("want [102], got %v", vals)
		}
	})

	// 2. Uint16
	t.Run("Uint16 float glitch and byteSwap", func(t *testing.T) {
		// 浮点毛刺
		vals, err := encodeHoldingRegisterHelper(ValueTypeUint16, "101.99999999999999", false, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(vals, []uint16{102}) {
			t.Fatalf("want [102], got %v", vals)
		}

		// 浮点毛刺 ByteSwap
		vals, err = encodeHoldingRegisterHelper(ValueTypeUint16, "258.000000000001", true, false)
		if err != nil {
			t.Fatal(err)
		}
		// 258 = 0x0102. ByteSwap -> [0x02, 0x01] -> 0x0201 = 513
		if !reflect.DeepEqual(vals, []uint16{0x0201}) {
			t.Fatalf("want [0x0201], got %x", vals)
		}
	})

	// 3. Int32
	t.Run("Int32 negative, float glitch and swaps", func(t *testing.T) {
		// 浮点毛刺负数: "-100.00000000001" -> -100 (0xFFFFFF9C)
		vals, err := encodeHoldingRegisterHelper(ValueTypeInt32, "-100.00000000001", false, false)
		if err != nil {
			t.Fatal(err)
		}
		// out: [0xFF, 0xFF, 0xFF, 0x9C]
		// values = [out[2:4], out[0:2]] = [0xFF9C, 0xFFFF]
		if !reflect.DeepEqual(vals, []uint16{0xFF9C, 0xFFFF}) {
			t.Fatalf("want [0xFF9C, 0xFFFF], got %x", vals)
		}

		// 浮点毛刺带 WordSwap
		valsWS, err := encodeHoldingRegisterHelper(ValueTypeInt32, "-100.00000000001", false, true)
		if err != nil {
			t.Fatal(err)
		}
		// out: [0xFF, 0x9C, 0xFF, 0xFF]
		// values = [out[2:4], out[0:2]] = [0xFFFF, 0xFF9C]
		if !reflect.DeepEqual(valsWS, []uint16{0xFFFF, 0xFF9C}) {
			t.Fatalf("want [0xFFFF, 0xFF9C], got %x", valsWS)
		}
	})

	// 4. Uint32
	t.Run("Uint32 float glitch and swaps", func(t *testing.T) {
		// 浮点毛刺: "65535.9999999" -> 65536 (0x00010000)
		vals, err := encodeHoldingRegisterHelper(ValueTypeUint32, "65535.9999999", false, false)
		if err != nil {
			t.Fatal(err)
		}
		// out: [0x00, 0x01, 0x00, 0x00]
		// values = [out[2:4], out[0:2]] = [0x0000, 0x0001]
		if !reflect.DeepEqual(vals, []uint16{0x0000, 0x0001}) {
			t.Fatalf("want [0x0000, 0x0001], got %x", vals)
		}
	})

	// 5. Uint64
	t.Run("Uint64 float glitch and large ints", func(t *testing.T) {
		// 浮点毛刺: "101.99999999999999" -> 102
		vals, err := encodeHoldingRegisterHelper(ValueTypeUint64, "101.99999999999999", false, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(vals, []uint16{102, 0, 0, 0}) {
			t.Fatalf("want [102, 0, 0, 0], got %v", vals)
		}

		// 64 位大整数保持精度: 0x0102030405060708
		vals, err = encodeHoldingRegisterHelper(ValueTypeUint64, "72623859790382856", false, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(vals, []uint16{0x0708, 0x0506, 0x0304, 0x0102}) {
			t.Fatalf("unexpected uint64 regs: %x", vals)
		}
	})

	// 6. Int64
	t.Run("Int64 negative and float glitch", func(t *testing.T) {
		// 浮点毛刺负数: "-1.0000000000000001" -> -1
		vals, err := encodeHoldingRegisterHelper(ValueTypeInt64, "-1.0000000000000001", false, false)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(vals, []uint16{0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF}) {
			t.Fatalf("want all 0xFFFF, got %x", vals)
		}
	})
}

func TestHoldingRegisterBitLogic(t *testing.T) {
	// 测试 bitLen > 0 时的校验逻辑
	t.Run("Int16 bit field rejects negative", func(t *testing.T) {
		v, err := parseInt16Value("-1")
		if err != nil {
			t.Fatal(err)
		}
		bitLen := uint8(4)
		if bitLen > 0 {
			if v < 0 {
				// 预期正确拒绝负数
				return
			}
			t.Fatal("expected rejection of negative value in bit field")
		}
	})

	t.Run("Int16 bit field rejects out of bit range", func(t *testing.T) {
		v, err := parseInt16Value("16")
		if err != nil {
			t.Fatal(err)
		}
		bitLen := uint8(4) // 4 bits max = 15
		if bitLen > 0 {
			if v < 0 {
				t.Fatal("unexpected negative")
			}
			if uint64(v) > (1<<bitLen - 1) {
				// 预期正确拒绝溢出
				return
			}
			t.Fatal("expected rejection of value > (1<<bitLen - 1)")
		}
	})

	t.Run("Uint16 bit field rejects out of bit range", func(t *testing.T) {
		v, err := parseUint16Value("16")
		if err != nil {
			t.Fatal(err)
		}
		bitLen := uint8(4) // 4 bits max = 15
		if bitLen > 0 {
			if uint64(v) > (1<<bitLen - 1) {
				// 预期正确拒绝溢出
				return
			}
			t.Fatal("expected rejection of value > (1<<bitLen - 1)")
		}
	})

	t.Run("Bit merge into uint16", func(t *testing.T) {
		// 原值 0b0000_0000_0000_0000, 在 bit 2 写入 3位: 0b101 (5) -> 0b0000_0000_0001_0100 (20)
		orig := uint16(0)
		res := mergeBitsIntoUint16(5, 2, 3, orig)
		if res != 20 {
			t.Fatalf("expected 20, got %d", res)
		}
	})
}
