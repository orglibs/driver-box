package internal

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"github.com/spf13/cast"
	"go.uber.org/zap"
)

func isStringPoint(point config.Point) bool {
	rawType, _ := point["rawType"].(string)
	return strings.EqualFold(rawType, ValueTypeString)
}

// Keep the existing PLC address notation, but check before narrowing to uint16
// and accept the final physical registers (50000–65535) as decimal addresses.
func stringPointAddress(raw interface{}) (uint16, error) {
	s := cast.ToString(raw)
	digits, base := s, 10
	if strings.HasPrefix(s, "0x") {
		digits, base = s[2:], 16
	} else if strings.HasSuffix(s, "d") {
		digits = strings.TrimSuffix(s, "d")
	}
	address, err := strconv.ParseUint(digits, base, 16)
	if err != nil {
		return 0, fmt.Errorf("invalid String startAddress: %w", err)
	}
	if digits == s && len(s) == 5 && address < 50000 {
		return castModbusAddress(s)
	}
	return uint16(address), nil
}

func stringRegisterQuantity(point config.Point) (uint16, error) {
	raw, exists := point["quantity"]
	if !exists {
		return 1, nil
	}
	data, err := json.Marshal(raw)
	if err != nil || string(data) == "null" {
		return 0, fmt.Errorf("String quantity must be a JSON integer between 1 and 125")
	}
	var quantity uint16
	if err := json.Unmarshal(data, &quantity); err != nil || quantity == 0 || quantity > 125 {
		return 0, fmt.Errorf("String quantity must be a JSON integer between 1 and 125")
	}
	return quantity, nil
}

func validateStringPoint(point config.Point) (uint16, error) {
	quantity, err := stringRegisterQuantity(point)
	if err != nil {
		return 0, err
	}
	if point["valueType"] != "string" {
		return 0, fmt.Errorf("String valueType must be string")
	}
	access, _ := point["readWrite"].(string)
	if access != "R" && access != "W" && access != "RW" {
		return 0, fmt.Errorf("String readWrite must be R, W or RW")
	}
	table, _ := point["primaryTable"].(string)
	if table != string(HoldingRegister) && table != string(InputRegister) {
		return 0, fmt.Errorf("String requires HOLDING_REGISTER or INPUT_REGISTER")
	}
	if access != "R" {
		if table == string(InputRegister) {
			return 0, fmt.Errorf("INPUT_REGISTER String must be read-only")
		}
		if quantity > 123 {
			return 0, fmt.Errorf("writable String quantity must be between 1 and 123")
		}
	}
	for _, field := range []string{"scale", "decimals", "bit", "bitLen"} {
		if raw, exists := point[field]; exists {
			data, err := json.Marshal(raw)
			var number float64
			if err != nil || string(data) == "null" || json.Unmarshal(data, &number) != nil || number != 0 {
				return 0, fmt.Errorf("String %s must be absent or numeric zero", field)
			}
		}
	}
	return quantity, nil
}

func readGroupLimit(conf *ConnectionConfig, table primaryTable) uint32 {
	limit := uint32(conf.BatchReadLen)
	if limit == 0 {
		limit = 32
	}
	if (table == HoldingRegister || table == InputRegister) && limit > 125 {
		limit = 125
	}
	return limit
}

// Validate all bound String points before constructing clients, listeners or tasks.
// Initialize has no error result: reject only this connection and log each context.
func validateConnectionStrings(conf *ConnectionConfig, models []config.DeviceModel) error {
	var firstErr error
	for _, model := range models {
		for _, device := range model.Devices {
			if device.ConnectionKey != conf.ConnectionKey {
				continue
			}
			for _, point := range model.DevicePoints {
				if !isStringPoint(point) {
					continue
				}
				ext, err := convToPointExtend(point)
				if err == nil && point["readWrite"] != "W" && uint32(ext.Quantity) > readGroupLimit(conf, ext.RegisterType) {
					err = fmt.Errorf("String quantity %d exceeds effective batchReadLen %d; increase batchReadLen", ext.Quantity, readGroupLimit(conf, ext.RegisterType))
				}
				if err != nil {
					context := fmt.Errorf("connectionKey=%s model=%s deviceId=%s pointName=%v: %w", conf.ConnectionKey, model.Name, device.ID, point["name"], err)
					driverbox.Log().Error("invalid Modbus String point", zap.String("connectionKey", conf.ConnectionKey),
						zap.String("model", model.Name), zap.String("deviceId", device.ID), zap.Any("pointName", point["name"]), zap.Error(err))
					if firstErr == nil {
						firstErr = context
					}
				}
			}
		}
	}
	return firstErr
}
