package core

import (
	"fmt"

	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/cache"
	"github.com/orglibs/driver-box/v2/pkg/config"
)

// Check the complete request before expected-value Shadow updates, Lua encoding,
// connector acquisition or network I/O. Preserve legacy behavior without selected.
func validateSelectedAccess(deviceID string, mode plugin.EncodeMode, values []plugin.PointData) error {
	device, ok := cache.Get().GetDevice(deviceID)
	if !ok || device.PointSelection == nil || device.PointSelection.Mode != "selected" {
		return nil
	}
	for _, value := range values {
		point, ok := cache.Get().GetPointByDevice(deviceID, value.PointName)
		if !ok {
			return fmt.Errorf("device %q point %q is not enabled", deviceID, value.PointName)
		}
		access := point.ReadWrite()
		if mode == plugin.ReadMode && access != config.ReadWrite_R && access != config.ReadWrite_RW {
			return fmt.Errorf("point %q is not readable", value.PointName)
		}
		if mode == plugin.WriteMode && access != config.ReadWrite_W && access != config.ReadWrite_RW {
			return fmt.Errorf("point %q is not writable", value.PointName)
		}
	}
	return nil
}
