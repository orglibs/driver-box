package internal

import (
	"testing"

	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/testutil/exporttest"
	"github.com/orglibs/driver-box/v2/pkg/config"
)

func TestReadGroupBatch(t *testing.T) {
	exporttest.Run(t, ProtocolName, &Plugin{}, func(r *exporttest.Recorder) {
		conn := &connector{virtual: true}
		group := &pointGroup{Points: []*Point{
			{Point: config.Point{"name": "U"}, DeviceId: "A"},
			{Point: config.Point{"name": "I"}, DeviceId: "B"},
			{Point: config.Point{"name": "P"}, DeviceId: "A"},
		}}
		if err := conn.sendReadCommand(group); err != nil {
			t.Fatal(err)
		}
		// Float points retain their float64 type, including zero values.
		r.Assert(t, 1, exporttest.Data("A", plugin.PointData{PointName: "U", Value: float64(0)}, plugin.PointData{PointName: "P", Value: float64(0)}), exporttest.Data("B", plugin.PointData{PointName: "I", Value: float64(0)}))
		if err := conn.sendReadCommand(group); err != nil {
			t.Fatal(err)
		}
		r.Assert(t, 1)
		group.Points = nil
		if err := conn.sendReadCommand(group); err != nil {
			t.Fatal(err)
		}
		r.Assert(t, 0)
	})
}
