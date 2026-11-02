package driverbox_test

import (
	"math"
	"testing"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/testutil/exporttest"
)

func TestExportOffset(t *testing.T) {
	exporttest.Run(t, "offset", &fixturePlugin{}, func(r *exporttest.Recorder) {
		u, _ := driverbox.CoreCache().GetPointByDevice("A", "U")
		u["scale"], u["offset"], u["decimals"] = .1, -40, 1
		i, _ := driverbox.CoreCache().GetPointByDevice("A", "I")
		i["valueType"], i["offset"] = "int", -10000
		p, _ := driverbox.CoreCache().GetPointByDevice("A", "P")
		p["valueType"], p["offset"] = "int", 0
		data := func(temp, base, large interface{}) []plugin.DeviceData {
			return []plugin.DeviceData{exporttest.Data("A", plugin.PointData{PointName: "U", Value: temp}, plugin.PointData{PointName: "I", Value: base}, plugin.PointData{PointName: "P", Value: large})}
		}
		bigValue := int64(9007199254740993)
		want := exporttest.Data("A", plugin.PointData{PointName: "U", Value: float64(25)}, plugin.PointData{PointName: "I", Value: int64(25)}, plugin.PointData{PointName: "P", Value: bigValue})
		driverbox.Export(data(650, 10025, bigValue))
		r.Assert(t, 1, want)
		driverbox.Export(data(650, 10025, bigValue))
		r.Assert(t, 1)
		driverbox.Export(data(400, 10025, bigValue))
		r.Assert(t, 1, exporttest.Data("A", plugin.PointData{PointName: "U", Value: float64(0)}))
		// Invalid samples cannot overwrite Shadow, while valid siblings export.
		i["offset"] = .5
		driverbox.Export(data(660, 1, bigValue))
		r.Assert(t, 1, exporttest.Data("A", plugin.PointData{PointName: "U", Value: float64(26)}))
		if got, err := driverbox.Shadow().GetDevicePoint("A", "I"); err != nil || got != int64(25) {
			t.Fatalf("bad sample replaced Shadow: %v,%v", got, err)
		}
		u["offset"] = "-40"
		driverbox.Export(data(9, 1, bigValue))
		r.Assert(t, 1)
		u["offset"] = -40
		driverbox.Export(data(math.Inf(1), 1, bigValue))
		r.Assert(t, 1)
		driverbox.Export(data(670, 1, bigValue))
		r.Assert(t, 1, exporttest.Data("A", plugin.PointData{PointName: "U", Value: float64(27)}))
		// Offset is added before the established decimal truncation.
		u["offset"], u["scale"] = .25, .1
		driverbox.Export(data(129, 1, bigValue))
		r.Assert(t, 1, exporttest.Data("A", plugin.PointData{PointName: "U", Value: 13.1}))
	})
}
