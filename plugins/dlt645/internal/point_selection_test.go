package internal

import (
	"testing"

	"github.com/orglibs/driver-box/v2/pkg/config"
)

func TestPointSelectionDLTReadGroups(t *testing.T) {
	for _, names := range [][]string{{"first"}, {}} {
		c := &connector{devices: map[string]*slaveDevice{}}
		model := config.DeviceModel{Model: config.Model{DevicePoints: []config.Point{
			{"name": "first", "readWrite": "R", "duration": "1s", "dataMaker": "00000000"},
			{"name": "second", "readWrite": "R", "duration": "1s", "dataMaker": "00000001"},
		}}}
		dev := config.Device{ID: "meter", Properties: map[string]string{"slaveId": "000000000001"}, PointSelection: &config.PointSelection{Mode: "selected", Names: names}}
		c.createPointGroup(&ConnectionConfig{}, model, dev)
		count := 0
		for _, device := range c.devices {
			for _, group := range device.pointGroup {
				count += len(group.Points)
			}
		}
		if count != len(names) {
			t.Fatalf("read points=%d want=%d", count, len(names))
		}
	}
}
