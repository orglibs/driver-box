package internal

import (
	"reflect"
	"strings"
	"testing"
)

func TestModbusStringOrder(t *testing.T) {
	for _, tt := range []struct {
		registers          []uint16
		byteSwap, wordSwap bool
		want               string
	}{
		{[]uint16{0x4142, 0x4344, 0x4546, 0x4748}, false, false, "ABCDEFGH"},
		{[]uint16{0x4142, 0x4344, 0x4546, 0x4748}, true, false, "BADCFEHG"},
		{[]uint16{0x4142, 0x4344, 0x4546, 0x4748}, false, true, "CDABGHEF"},
		{[]uint16{0x4142, 0x4344, 0x4546, 0x4748}, true, true, "DCBAHGFE"},
		{[]uint16{0x4142, 0x4344, 0x4546}, false, false, "ABCDEF"},
		{[]uint16{0x4142, 0x4344, 0x4546}, false, true, "CDABEF"},
		{[]uint16{0x4142}, false, true, "AB"},
		{[]uint16{0xe4b8, 0xade6, 0x9687}, false, false, "中文"},
	} {
		before := append([]uint16(nil), tt.registers...)
		got, err := decodeModbusString(tt.registers, tt.byteSwap, tt.wordSwap)
		if err != nil || got != tt.want || !reflect.DeepEqual(tt.registers, before) {
			t.Fatalf("decode(%x,%v,%v)=%q,%v; want %q; unchanged=%v", before, tt.byteSwap, tt.wordSwap, got, err, tt.want, reflect.DeepEqual(tt.registers, before))
		}
	}
}

func TestNormalizeModbusText(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"  HW PILE 001  \x00\x00", "HW PILE 001"},
		{"\x00 \x00 A \x00 \x00", "A"},
		{" A  B ", "A  B"},
		{" A\x00B ", "A\x00B"},
		{" \tA\n\u3000 ", "\tA\n\u3000"},
		{"  ", ""}, {"\x00\x00", ""}, {"", ""}, {"中文", "中文"},
	} {
		got, err := normalizeModbusText(tt.in)
		if err != nil || got != tt.want {
			t.Fatalf("normalize(%q)=%q,%v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"\xff\xff", "\xe4\xb8", " \xff\x00"} {
		if _, err := normalizeModbusText(bad); err == nil {
			t.Fatalf("accepted invalid UTF-8 %x", bad)
		}
	}
}

func TestModbusStringRoundTrip(t *testing.T) {
	for _, quantity := range []uint16{1, 3, 16, 24, 123} {
		for _, byteSwap := range []bool{false, true} {
			for _, wordSwap := range []bool{false, true} {
				for _, text := range []string{"", " A ", "AB", strings.Repeat("X", 2*int(quantity)), "A\x00B", "中文", "\tA\n"} {
					want, _ := normalizeModbusText(text)
					if len(want) > 2*int(quantity) {
						continue
					}
					regs, err := encodeModbusString(text, quantity, byteSwap, wordSwap)
					if err != nil || len(regs) != int(quantity) {
						t.Fatalf("encode: %x %v", regs, err)
					}
					got, err := decodeModbusString(regs, byteSwap, wordSwap)
					if err != nil || got != want {
						t.Fatalf("roundtrip q=%d swaps=%v/%v %q -> %x -> %q, %v", quantity, byteSwap, wordSwap, text, regs, got, err)
					}
				}
			}
		}
	}
	// Swaps apply to the entire padded capacity, including odd byte strings.
	for _, tt := range []struct {
		byteSwap, wordSwap bool
		want               []uint16
	}{
		{false, false, []uint16{0x4142, 0x4300, 0}},
		{true, false, []uint16{0x4241, 0x0043, 0}},
		{false, true, []uint16{0x4300, 0x4142, 0}},
		{true, true, []uint16{0x0043, 0x4241, 0}},
	} {
		got, err := encodeModbusString("ABC", 3, tt.byteSwap, tt.wordSwap)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("padded encode=%x,%v want %x", got, err, tt.want)
		}
	}
}

func TestModbusStringInvalidParameters(t *testing.T) {
	for _, regs := range [][]uint16{nil, {}, make([]uint16, 126), {0xffff}, {0xe4b8}} {
		if _, err := decodeModbusString(regs, false, false); err == nil {
			t.Fatalf("accepted %x", regs)
		}
	}
	if got, err := decodeModbusString(make([]uint16, 125), false, true); err != nil || got != "" {
		t.Fatalf("valid empty text: %q %v", got, err)
	}
	for _, quantity := range []uint16{0, 124, 125, 65535} {
		if _, err := encodeModbusString("", quantity, false, false); err == nil {
			t.Fatalf("accepted write quantity %d", quantity)
		}
	}
	for _, text := range []string{"ABC", "中", "\xff"} {
		if _, err := encodeModbusString(text, 1, false, false); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}
