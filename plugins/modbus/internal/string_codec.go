package internal

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"
)

func normalizeModbusText(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("Modbus text contains invalid UTF-8")
	}
	return strings.Trim(value, "\x00 "), nil
}

// decodeModbusString reads an address-ordered window without modifying it.
func decodeModbusString(registers []uint16, byteSwap, wordSwap bool) (string, error) {
	if len(registers) < 1 || len(registers) > 125 {
		return "", fmt.Errorf("String read quantity must be between 1 and 125, got %d", len(registers))
	}
	data := make([]byte, 2*len(registers))
	for i := range registers {
		j := i
		if wordSwap && i^1 < len(registers) {
			j = i ^ 1
		}
		v := registers[j]
		if byteSwap {
			v = v<<8 | v>>8
		}
		binary.BigEndian.PutUint16(data[2*i:], v)
	}
	return normalizeModbusText(string(data))
}

func encodeModbusString(value string, quantity uint16, byteSwap, wordSwap bool) ([]uint16, error) {
	if quantity < 1 || quantity > 123 {
		return nil, fmt.Errorf("String write quantity must be between 1 and 123, got %d", quantity)
	}
	value, err := normalizeModbusText(value)
	if err != nil {
		return nil, err
	}
	if len(value) > 2*int(quantity) {
		return nil, fmt.Errorf("String requires %d bytes, capacity is %d bytes", len(value), 2*int(quantity))
	}
	data := make([]byte, 2*int(quantity))
	copy(data, value)
	registers := make([]uint16, quantity)
	for i := range registers {
		v := binary.BigEndian.Uint16(data[2*i:])
		if byteSwap {
			v = v<<8 | v>>8
		}
		j := i
		if wordSwap && i^1 < len(registers) {
			j = i ^ 1
		}
		registers[j] = v
	}
	return registers, nil
}
