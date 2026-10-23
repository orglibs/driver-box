package internal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/pkg/convutil"
	"go.uber.org/zap"
)

// Decode 解码数据
func (c *connector) Decode(raw interface{}) (res []plugin.DeviceData, err error) {
	readValue, ok := raw.(plugin.PointReadValue)
	if !ok {
		return nil, fmt.Errorf("unexpected raw: %v", raw)
	}

	res = append(res, plugin.DeviceData{
		ID: readValue.ID,
		Values: []plugin.PointData{{
			PointName: readValue.PointName,
			Value:     readValue.Value,
		}},
	})
	return
}

// Encode 编码数据
func (c *connector) Encode(deviceId string, mode plugin.EncodeMode, values ...plugin.PointData) (res interface{}, err error) {
	if mode == plugin.WriteMode {
		writeValues, err := c.batchWriteEncode(deviceId, values)
		if err != nil {
			c.writeEncodeMu.Unlock()
		}
		return command{
			Mode:  plugin.WriteMode,
			Value: writeValues,
		}, err
	}

	device, ok := driverbox.CoreCache().GetDevice(deviceId)
	if !ok {
		return nil, fmt.Errorf("device [%s] not found", deviceId)
	}
	unitId, e := getUnitId(device.Properties)
	if e != nil {
		return nil, e
	}
	slave := c.devices[unitId]
	if slave == nil {
		return nil, fmt.Errorf("device [%s] not found", deviceId)
	}

	indexes := make(map[int]*pointGroup)
	var pointGroups []*pointGroup
	//寻找待读点位关联的pointGroup
	for _, readPoint := range values {
		ok = false
		for _, group := range slave.pointGroup {
			for _, point := range group.Points {
				if point.Name() == readPoint.PointName {
					if _, ok := indexes[group.index]; !ok {
						indexes[group.index] = group
						pointGroups = append(pointGroups, group)
					}
					ok = true
					break
				}
			}
			//匹配成功
			if ok {
				break
			}
		}
	}

	//找到待读点所属的group
	return command{
		Mode:  BatchReadMode,
		Value: pointGroups,
	}, nil
}

func (c *connector) batchWriteEncode(deviceId string, points []plugin.PointData) ([]*writeValue, error) {
	c.writeEncodeMu.Lock()

	values := make([]*writeValue, 0)
	for _, p := range points {
		wv, err := c.getWriteValue(deviceId, p, values)
		if err != nil {
			return values, err
		}
		//发生点位合并，跳过
		if wv.unitID == 0 {
			continue
		}
		values = append(values, &wv)
	}
	//按照address排序
	sort.Slice(values, func(i, j int) bool {
		return values[i].Address < values[j].Address
	})

	mergedValues := make([]*writeValue, 0)
	var preValue *writeValue
	for _, v := range values {
		//仅保持寄存器支持批量
		if v.RegisterType != HoldingRegister {
			mergedValues = append(mergedValues, v)
			continue
		}
		if preValue == nil {
			preValue = v
			mergedValues = append(mergedValues, v)
			continue
		}

		//批量下发必须为连续地址
		if int(preValue.Address)+len(preValue.Value) != int(v.Address) {
			preValue = v
			mergedValues = append(mergedValues, v)
			continue
		}
		//超过批量写支持的字节长度范围
		batchLen := len(preValue.Value) + len(v.Value)
		if uint16(batchLen) > c.config.BatchWriteLen {
			preValue = v
			mergedValues = append(mergedValues, v)
			continue
		}
		//合并数据
		bytes := make([]uint16, batchLen)
		copy(bytes, preValue.Value)
		copy(bytes[len(preValue.Value):], v.Value)
		preValue.Value = bytes
	}
	return mergedValues, nil
}

type registerReader func(unitID uint8, registerType string, address, quantity uint16) ([]uint16, error)

func (c *connector) getWriteValue(deviceId string, pointData plugin.PointData, writeValues []*writeValue) (writeValue, error) {
	return c.getWriteValueWithReader(deviceId, pointData, writeValues, func(unitID uint8, registerType string, address, quantity uint16) ([]uint16, error) {
		if c.virtual {
			return c.mockRead(unitID, registerType, address, quantity)
		}
		return c.read(unitID, registerType, address, quantity)
	})
}

func (c *connector) getWriteValueWithReader(deviceId string, pointData plugin.PointData, writeValues []*writeValue, read registerReader) (writeValue, error) {
	value := pointData.Value
	d, ok := driverbox.CoreCache().GetDevice(deviceId)
	if !ok {
		return writeValue{}, errors.New("device not found")
	}
	unitId, err := getUnitId(d.Properties)
	if err != nil {
		return writeValue{}, err
	}
	p, ok := driverbox.CoreCache().GetPointByDevice(deviceId, pointData.PointName)
	if !ok {
		return writeValue{}, errors.New("point not found")
	}

	ext, err := convToPointExtend(p)
	if err != nil {
		driverbox.Log().Error("error modbus point config", zap.String("deviceId", deviceId), zap.Any("point", pointData.PointName), zap.Error(err))
		return writeValue{}, err
	}
	var values []uint16
	switch ext.RegisterType {
	case Coil: // 线圈固定长度1
		i, err := convutil.Int64(value)
		if err != nil {
			return writeValue{}, err
		}
		values = []uint16{uint16(i & 1)}
	case HoldingRegister:
		valueStr := fmt.Sprintf("%v", value)
		switch strings.ToUpper(ext.RawType) {
		case strings.ToUpper(ValueTypeInt16):
			v, err := parseInt16Value(valueStr)
			if err != nil {
				return writeValue{}, fmt.Errorf("convert value %v to int16 error: %v", value, err)
			}
			if ext.BitLen > 0 {
				if v < 0 {
					return writeValue{}, fmt.Errorf("negative value %v not allowed to set in bits", v)
				}
				if uint64(v) > (1<<ext.BitLen - 1) {
					return writeValue{}, fmt.Errorf("too large value %v to set in %d bits", v, ext.BitLen)
				}
				uint16s, err := read(unitId, string(ext.RegisterType), ext.Address, ext.Quantity)

				if err != nil {
					return writeValue{}, fmt.Errorf("read original register error: %v", err)
				}
				uint16Val := uint16s[0]
				if ext.ByteSwap {
					uint16Val = (uint16Val << 8) | (uint16Val >> 8)
				}
				intoUint16 := mergeBitsIntoUint16(int(v), ext.Bit, ext.BitLen, uint16Val)
				if ext.ByteSwap {
					intoUint16 = (intoUint16 << 8) | (intoUint16 >> 8)
				}
				// 如果之前已经写入过，则合并
				for _, writeVal := range writeValues {
					if writeVal.Address == ext.Address && writeVal.RegisterType == ext.RegisterType && len(writeVal.Value) == 1 {
						driverbox.Log().Info("merge bits", zap.Uint16("preValue", writeVal.Value[0]), zap.Uint16("bitValue", intoUint16))
						writeVal.Value[0] = (writeVal.Value[0] & ^(((1 << ext.BitLen) - 1) << ext.Bit)) | (intoUint16 & (((1 << ext.BitLen) - 1) << ext.Bit))
						driverbox.Log().Info("merge bits result", zap.Uint16("finalVal", writeVal.Value[0]))
						return writeValue{}, nil
					}
				}
				values = []uint16{intoUint16}
				break
			}
			out := make([]byte, 2)
			if ext.ByteSwap {
				binary.LittleEndian.PutUint16(out, uint16(v))
			} else {
				binary.BigEndian.PutUint16(out, uint16(v))
			}
			values = []uint16{binary.BigEndian.Uint16(out)}

		case strings.ToUpper(ValueTypeUint16):
			v, err := parseUint16Value(valueStr)
			if err != nil {
				return writeValue{}, fmt.Errorf("convert value %v to uint16 error: %v", value, err)
			}
			if ext.BitLen > 0 {
				if uint64(v) > (1<<ext.BitLen - 1) {
					return writeValue{}, fmt.Errorf("too large value %v to set in %d bits", v, ext.BitLen)
				}
				uint16s, err := read(unitId, string(ext.RegisterType), ext.Address, ext.Quantity)

				if err != nil {
					return writeValue{}, fmt.Errorf("read original register error: %v", err)
				}
				uint16Val := uint16s[0]
				if ext.ByteSwap {
					uint16Val = (uint16Val << 8) | (uint16Val >> 8)
				}
				intoUint16 := mergeBitsIntoUint16(int(v), ext.Bit, ext.BitLen, uint16Val)
				if ext.ByteSwap {
					intoUint16 = (intoUint16 << 8) | (intoUint16 >> 8)
				}
				// 如果之前已经写入过，则合并
				for _, writeVal := range writeValues {
					if writeVal.Address == ext.Address && writeVal.RegisterType == ext.RegisterType && len(writeVal.Value) == 1 {
						driverbox.Log().Info("merge bits", zap.Uint16("preValue", writeVal.Value[0]), zap.Uint16("bitValue", intoUint16))
						writeVal.Value[0] = (writeVal.Value[0] & ^(((1 << ext.BitLen) - 1) << ext.Bit)) | (intoUint16 & (((1 << ext.BitLen) - 1) << ext.Bit))
						driverbox.Log().Info("merge bits result", zap.Uint16("finalVal", writeVal.Value[0]))
						return writeValue{}, nil
					}
				}
				values = []uint16{intoUint16}
				break
			}
			out := make([]byte, 2)
			if ext.ByteSwap {
				binary.LittleEndian.PutUint16(out, v)
			} else {
				binary.BigEndian.PutUint16(out, v)
			}
			values = []uint16{binary.BigEndian.Uint16(out)}
		case strings.ToUpper(ValueTypeUint32):
			v, err := parseUint32Value(valueStr)
			if err != nil {
				return writeValue{}, fmt.Errorf("convert value %v to uint32 error: %v", value, err)
			}
			out := make([]byte, 4)
			if ext.ByteSwap {
				binary.LittleEndian.PutUint32(out, v)
			} else {
				binary.BigEndian.PutUint32(out, v)
			}
			if ext.WordSwap {
				out[0], out[1], out[2], out[3] =
					out[2], out[3], out[0], out[1]
			}
			values = []uint16{binary.BigEndian.Uint16([]byte{out[2], out[3]}),
				binary.BigEndian.Uint16([]byte{out[0], out[1]})}
		case strings.ToUpper(ValueTypeInt32):
			v, err := parseInt32Value(valueStr)
			if err != nil {
				return writeValue{}, fmt.Errorf("convert value %v to int32 error: %v", value, err)
			}
			out := make([]byte, 4)
			if ext.ByteSwap {
				binary.LittleEndian.PutUint32(out, uint32(v))
			} else {
				binary.BigEndian.PutUint32(out, uint32(v))
			}
			if ext.WordSwap {
				out[0], out[1], out[2], out[3] =
					out[2], out[3], out[0], out[1]
			}
			values = []uint16{binary.BigEndian.Uint16([]byte{out[2], out[3]}),
				binary.BigEndian.Uint16([]byte{out[0], out[1]})}
		case strings.ToUpper(ValueTypeFloat32):
			v, err := strconv.ParseFloat(valueStr, 32)
			if err != nil {
				return writeValue{}, fmt.Errorf("convert value %v to float32 error: %v", value, err)
			}
			v32 := float32(v)
			bits := math.Float32bits(v32)
			out := make([]byte, 4)
			if ext.ByteSwap {
				binary.LittleEndian.PutUint32(out, bits)
			} else {
				binary.BigEndian.PutUint32(out, bits)
			}
			if ext.WordSwap {
				out[0], out[1], out[2], out[3] =
					out[2], out[3], out[0], out[1]
			}
			values = []uint16{binary.BigEndian.Uint16([]byte{out[2], out[3]}),
				binary.BigEndian.Uint16([]byte{out[0], out[1]})}
		case strings.ToUpper(ValueTypeUint64):
			v, err := parseUint64Value(valueStr)
			if err != nil {
				return writeValue{}, fmt.Errorf("convert value %v to uint64 error: %v", value, err)
			}
			out := make([]byte, 8)
			if ext.ByteSwap {
				binary.LittleEndian.PutUint64(out, v)
			} else {
				binary.BigEndian.PutUint64(out, v)
			}
			if ext.WordSwap {
				out[0], out[1], out[2], out[3], out[4], out[5], out[6], out[7] =
					out[2], out[3], out[0], out[1], out[6], out[7], out[4], out[5]
			}
			values = []uint16{
				binary.BigEndian.Uint16([]byte{out[6], out[7]}),
				binary.BigEndian.Uint16([]byte{out[4], out[5]}),
				binary.BigEndian.Uint16([]byte{out[2], out[3]}),
				binary.BigEndian.Uint16([]byte{out[0], out[1]}),
			}
		case strings.ToUpper(ValueTypeInt64):
			v, err := parseInt64Value(valueStr)
			if err != nil {
				return writeValue{}, fmt.Errorf("convert value %v to int64 error: %v", value, err)
			}
			out := make([]byte, 8)
			if ext.ByteSwap {
				binary.LittleEndian.PutUint64(out, uint64(v))
			} else {
				binary.BigEndian.PutUint64(out, uint64(v))
			}
			if ext.WordSwap {
				out[0], out[1], out[2], out[3], out[4], out[5], out[6], out[7] =
					out[2], out[3], out[0], out[1], out[6], out[7], out[4], out[5]
			}
			values = []uint16{
				binary.BigEndian.Uint16([]byte{out[6], out[7]}),
				binary.BigEndian.Uint16([]byte{out[4], out[5]}),
				binary.BigEndian.Uint16([]byte{out[2], out[3]}),
				binary.BigEndian.Uint16([]byte{out[0], out[1]}),
			}
		case strings.ToUpper(ValueTypeFloat64):
			v, err := strconv.ParseFloat(valueStr, 64)
			if err != nil {
				return writeValue{}, fmt.Errorf("convert value %v to float64 error: %v", value, err)
			}
			out := make([]byte, 8)
			if ext.ByteSwap {
				binary.LittleEndian.PutUint64(out, math.Float64bits(v))
			} else {
				binary.BigEndian.PutUint64(out, math.Float64bits(v))
			}
			if ext.WordSwap {
				out[0], out[1], out[2], out[3], out[4], out[5], out[6], out[7] =
					out[2], out[3], out[0], out[1], out[6], out[7], out[4], out[5]
			}
			values = []uint16{
				binary.BigEndian.Uint16([]byte{out[6], out[7]}),
				binary.BigEndian.Uint16([]byte{out[4], out[5]}),
				binary.BigEndian.Uint16([]byte{out[2], out[3]}),
				binary.BigEndian.Uint16([]byte{out[0], out[1]}),
			}
		case strings.ToUpper(ValueTypeString):
			valueBytes := []byte(valueStr)
			if len(valueBytes) > int(ext.Quantity*2) {
				return writeValue{}, fmt.Errorf("too long string [%v] to set in %d registers", valueStr, ext.Quantity)
			}
			if ext.ByteSwap {
				for i := 0; i < len(valueBytes); i += 2 {
					if i+1 < len(valueBytes) {
						valueBytes[i], valueBytes[i+1] = valueBytes[i+1], valueBytes[i]
					}
				}
			}
			if ext.WordSwap {
				for i := 0; i < len(valueBytes); i += 4 {
					if i+3 < len(valueBytes) {
						valueBytes[i], valueBytes[i+1], valueBytes[i+2], valueBytes[i+3] =
							valueBytes[i+2], valueBytes[i+3], valueBytes[i], valueBytes[i+1]
					}
				}
			}
			values = make([]uint16, ext.Quantity)
			for i := 0; i < len(valueBytes); i += 2 {
				if i+1 < len(valueBytes) {
					values[i/2] = binary.BigEndian.Uint16(valueBytes[i : i+2])
				} else {
					values[i/2] = binary.BigEndian.Uint16([]byte{valueBytes[i], 0})
				}
			}
			for i := 0; i < len(values)/2; i++ {
				values[i], values[len(values)-1-i] = values[len(values)-1-i], values[i]
			}
		default:
			return writeValue{}, fmt.Errorf("unsupported raw type: %v", ext)
		}
	default:
		return writeValue{}, fmt.Errorf("unsupported write register type: %v", ext)
	}
	return writeValue{
		DeviceId:     deviceId,
		unitID:       unitId,
		RegisterType: ext.RegisterType,
		Address:      ext.Address,
		Value:        values,
		MultiWrite:   ext.MultiWrite,
	}, nil
}

// parseUint16Value 解析 uint16 值，支持浮点字符串四舍五入（容错毛刺如 "101.99999999999999"）。
func parseUint16Value(valueStr string) (uint16, error) {
	// 先尝试直接作为无符号整数解析
	if v, err := strconv.ParseUint(valueStr, 10, 16); err == nil {
		return uint16(v), nil
	}
	// 针对带小数毛刺的浮点字符串，转为 float 后四舍五入
	if f, err := strconv.ParseFloat(valueStr, 64); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("invalid uint16 syntax: %s", valueStr)
		}
		rounded := math.Round(f)
		if rounded < 0 || rounded > math.MaxUint16 {
			return 0, fmt.Errorf("value %v out of range for uint16", f)
		}
		return uint16(rounded), nil
	}
	return 0, fmt.Errorf("invalid uint16 syntax: %s", valueStr)
}

// parseInt16Value 解析 int16 值，支持浮点字符串四舍五入（容错毛刺如 "-32.00000000001"）。
func parseInt16Value(valueStr string) (int16, error) {
	// 先尝试直接作为有符号整数解析
	if v, err := strconv.ParseInt(valueStr, 10, 16); err == nil {
		return int16(v), nil
	}
	// 针对带小数毛刺的浮点字符串，转为 float 后四舍五入
	if f, err := strconv.ParseFloat(valueStr, 64); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("invalid int16 syntax: %s", valueStr)
		}
		rounded := math.Round(f)
		if rounded < math.MinInt16 || rounded > math.MaxInt16 {
			return 0, fmt.Errorf("value %v out of range for int16", f)
		}
		return int16(rounded), nil
	}
	return 0, fmt.Errorf("invalid int16 syntax: %s", valueStr)
}

// parseUint32Value 解析 uint32 值，支持浮点字符串四舍五入（容错毛刺如 "65535.9999999"）。
func parseUint32Value(valueStr string) (uint32, error) {
	// 先尝试直接作为无符号整数解析
	if v, err := strconv.ParseUint(valueStr, 10, 32); err == nil {
		return uint32(v), nil
	}
	// 针对带小数毛刺的浮点字符串，转为 float 后四舍五入
	if f, err := strconv.ParseFloat(valueStr, 64); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("invalid uint32 syntax: %s", valueStr)
		}
		rounded := math.Round(f)
		if rounded < 0 || rounded > math.MaxUint32 {
			return 0, fmt.Errorf("value %v out of range for uint32", f)
		}
		return uint32(rounded), nil
	}
	return 0, fmt.Errorf("invalid uint32 syntax: %s", valueStr)
}

// parseInt32Value 解析 int32 值，支持浮点字符串四舍五入（容错毛刺如 "-100.00000000001"）。
func parseInt32Value(valueStr string) (int32, error) {
	// 先尝试直接作为有符号整数解析
	if v, err := strconv.ParseInt(valueStr, 10, 32); err == nil {
		return int32(v), nil
	}
	// 针对带小数毛刺的浮点字符串，转为 float 后四舍五入
	if f, err := strconv.ParseFloat(valueStr, 64); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("invalid int32 syntax: %s", valueStr)
		}
		rounded := math.Round(f)
		if rounded < math.MinInt32 || rounded > math.MaxInt32 {
			return 0, fmt.Errorf("value %v out of range for int32", f)
		}
		return int32(rounded), nil
	}
	return 0, fmt.Errorf("invalid int32 syntax: %s", valueStr)
}

// parseUint64Value 解析 uint64 值，支持浮点字符串四舍五入。
func parseUint64Value(valueStr string) (uint64, error) {
	// 先尝试直接作为无符号整数解析（保持完整 64 位整型精度）
	v, err := strconv.ParseUint(valueStr, 10, 64)
	if err == nil {
		return v, nil
	}
	if errors.Is(err, strconv.ErrRange) {
		return 0, fmt.Errorf("value %s out of range for uint64", valueStr)
	}
	// 针对带小数毛刺的浮点字符串，转为 float 后四舍五入
	if f, err := strconv.ParseFloat(valueStr, 64); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("invalid uint64 syntax: %s", valueStr)
		}
		rounded := math.Round(f)
		if rounded < 0 || rounded >= math.Exp2(64) {
			return 0, fmt.Errorf("value %v out of range for uint64", f)
		}
		return uint64(rounded), nil
	}
	return 0, fmt.Errorf("invalid uint64 syntax: %s", valueStr)
}

// parseInt64Value 解析 int64 值，支持浮点字符串四舍五入。
func parseInt64Value(valueStr string) (int64, error) {
	// 先尝试直接作为有符号整数解析（保持完整 64 位整型精度）
	v, err := strconv.ParseInt(valueStr, 10, 64)
	if err == nil {
		return v, nil
	}
	if errors.Is(err, strconv.ErrRange) {
		return 0, fmt.Errorf("value %s out of range for int64", valueStr)
	}
	// 针对带小数毛刺的浮点字符串，转为 float 后四舍五入
	if f, err := strconv.ParseFloat(valueStr, 64); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("invalid int64 syntax: %s", valueStr)
		}
		rounded := math.Round(f)
		if rounded < math.MinInt64 || rounded >= math.Exp2(63) {
			return 0, fmt.Errorf("value %v out of range for int64", f)
		}
		return int64(rounded), nil
	}
	return 0, fmt.Errorf("invalid int64 syntax: %s", valueStr)
}
