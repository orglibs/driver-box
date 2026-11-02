package core

import (
	"errors"
	"fmt"

	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/cache"
	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"github.com/orglibs/driver-box/v2/pkg/convutil"
	"github.com/orglibs/driver-box/v2/pkg/library"
	"go.uber.org/zap"
)

// serialNo 网关序列号
var Metadata = config.Metadata{
	SerialNo: "driver-box",
	Model:    "driver-box",
	Vendor:   "iBUILDING",
}

// 校验model有效性
func checkMode(mode plugin.EncodeMode) bool {
	switch mode {
	case plugin.ReadMode, plugin.WriteMode:
		return true
	default:
		return false
	}
}

// 点位值加工：设备驱动
func deviceDriverProcess(deviceId string, mode plugin.EncodeMode, pointData ...plugin.PointData) ([]plugin.PointData, error) {
	device, ok := cache.Get().GetDevice(deviceId)
	if !ok {
		logger.Logger.Error("unknown device", zap.Any("deviceId", device))
		return nil, errors.New("unknown device")
	}
	scaleEnable := len(device.DriverKey) == 0

	if mode == plugin.WriteMode {
		// Preserve engineering values for callers and write/readback comparison.
		pointData = append([]plugin.PointData(nil), pointData...)
		for i, p := range pointData {
			point, ok := cache.Get().GetPointByDevice(deviceId, p.PointName)
			if !ok {
				return nil, fmt.Errorf("not found point, point name is %s", p.PointName)
			}
			offset, err := point.Offset()
			if err != nil {
				return nil, fmt.Errorf("device %s point %s: %w", deviceId, p.PointName, err)
			}
			var value interface{}
			if scaleEnable && offset != 0 {
				value, err = convutil.WritePointOffset(p.Value, point)
			} else {
				value, err = convutil.PointValue(p.Value, point.ValueType())
			}
			if err != nil {
				return nil, fmt.Errorf("device %s point %s: %w", deviceId, p.PointName, err)
			}
			if scaleEnable && offset == 0 && point.Scale() != 0 {
				value, err = divideStrings(value, point.Scale())
				if err != nil {
					return nil, err
				}
			}
			pointData[i].Value = value
		}
	}

	if scaleEnable {
		return pointData, nil
	}
	result := library.Driver().DeviceEncode(device.DriverKey, library.DeviceEncodeRequest{
		DeviceId: deviceId,
		Mode:     mode,
		Points:   pointData,
	})
	return result.Points, result.Error
}

func divideStrings(value interface{}, scale float64) (float64, error) {
	switch v := value.(type) {
	case float64:
		return v / scale, nil
	case int:
		return float64(v) / scale, nil
	case int64:
		return float64(v) / scale, nil
	default:
		return 0, fmt.Errorf("cannot divide %T with float64", value)
	}
}
