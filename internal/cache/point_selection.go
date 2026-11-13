package cache

import (
	"github.com/orglibs/driver-box/v2/pkg/config"
)

func copyDeviceSelection(device config.Device) config.Device {
	if device.PointSelection != nil {
		selection := *device.PointSelection
		if selection.Names != nil {
			selection.Names = append([]string{}, selection.Names...)
		}
		device.PointSelection = &selection
	}
	return device
}

func compileDevice(device config.Device, model config.Model) (cacheDevice, error) {
	device = copyDeviceSelection(device)
	points, err := model.PointsFor(device)
	if err != nil {
		return cacheDevice{}, err
	}
	result := cacheDevice{Device: device, enabledPoints: points}
	if device.PointSelection != nil && device.PointSelection.Mode == "selected" {
		result.enabledNames = make(map[string]bool, len(points))
		for _, point := range points {
			result.enabledNames[point.Name()] = true
		}
	}
	return result, nil
}

// GetDevicePoints returns effective points; (empty, true) is a valid zero-point
// device. GetPoints continues to return the complete shared model definition.
func (c *cache) GetDevicePoints(id string) ([]config.Point, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	device, ok := c.devices[id]
	if !ok {
		return nil, false
	}
	return append([]config.Point{}, device.enabledPoints...), true
}
