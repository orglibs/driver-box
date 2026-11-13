package internal

import (
	"testing"

	"github.com/orglibs/driver-box/v2/pkg/config"
)

func TestPointSelectionIEC104Routes(t *testing.T) {
	for _, names := range [][]string{{"switch"}, {}} {
		s, cfg := fixture()
		cfg.DeviceModels[0].Devices = cfg.DeviceModels[0].Devices[:1]
		cfg.DeviceModels[0].DevicePoints = append(cfg.DeviceModels[0].DevicePoints, config.Point{"name": "excluded", "readWrite": "R", "ext": map[string]any{"ioa": 2, "category": "signal"}})
		cfg.DeviceModels[0].Devices[0].PointSelection = &config.PointSelection{Mode: "selected", Names: names}
		c, err := newConnector(s, cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer c.cancel()
		if len(c.nodes["a"]) != len(names) || len(c.routes) != len(names) || (len(names) == 0 && len(c.nodes) != 0) {
			t.Fatal("selection not applied to IEC104 device routes")
		}
	}
}
