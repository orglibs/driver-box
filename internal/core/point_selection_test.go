package core_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/testutil/exporttest"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"github.com/orglibs/driver-box/v2/pkg/event"
)

func TestPointSelectionLuaCannotExpandAccess(t *testing.T) {
	p := &offsetPlugin{conn: &offsetConnector{}}
	exporttest.Run(t, "selection-lua", p, func(r *exporttest.Recorder) {
		u, _ := driverbox.CoreCache().GetPointByDevice("A", "U")
		i, _ := driverbox.CoreCache().GetPointByDevice("A", "I")
		u["readWrite"], i["readWrite"] = "RW", "RW"
		dir := filepath.Join(config.ResourcePath, "library", "driver")
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		script := `function encode(device, mode, points)
  return {{name="I", value=11}}
end
function decode(device, points)
  return {{name="I", value=22}}
end`
		if err := os.WriteFile(filepath.Join(dir, "selection.lua"), []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		dev, _ := driverbox.CoreCache().GetDevice("A")
		dev.DriverKey = "selection"
		dev.PointSelection = &config.PointSelection{Mode: "selected", Names: []string{"U"}}
		if err := driverbox.CoreCache().AddOrUpdateDevice(dev); err != nil {
			t.Fatal(err)
		}
		if err := driverbox.WritePoint("A", plugin.PointData{PointName: "U", Value: 7}); err == nil {
			t.Fatal("single Lua encoding expanded enabled points")
		}
		if err := driverbox.WritePoints("A", []plugin.PointData{{PointName: "U", Value: 7}}); err == nil {
			t.Fatal("batch Lua encoding expanded enabled points")
		}
		if err := driverbox.ReadPoint("A", "U"); err == nil {
			t.Fatal("Lua read encoding expanded enabled points")
		}
		if len(p.conn.sent) != 0 {
			t.Fatal("Lua bypass reached physical I/O")
		}
		driverbox.Export([]plugin.DeviceData{exporttest.Data("A", plugin.PointData{PointName: "U", Value: 1})})
		r.Assert(t, 1)
		if value, _ := driverbox.Shadow().GetDevicePoint("A", "I"); value != nil {
			t.Fatal("Lua decoding bypassed point selection")
		}
	})
}

func TestPointSelectionDecodeFailureDoesNotLeak(t *testing.T) {
	p := &offsetPlugin{conn: &offsetConnector{}}
	exporttest.Run(t, "selection-decode-error", p, func(r *exporttest.Recorder) {
		dir := filepath.Join(config.ResourcePath, "library", "driver")
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "decode-error.lua"), []byte(`function decode(device, points) error("injected decode failure") end`), 0600); err != nil {
			t.Fatal(err)
		}
		for _, driver := range []string{"decode-error", "missing-driver"} {
			for _, selection := range []*config.PointSelection{nil, {Mode: "all"}, {Mode: "selected", Names: []string{"U"}}, {Mode: "selected", Names: []string{}}} {
				dev, _ := driverbox.CoreCache().GetDevice("A")
				dev.DriverKey, dev.PointSelection = driver, selection
				if err := driverbox.CoreCache().AddOrUpdateDevice(dev); err != nil {
					t.Fatal(err)
				}
				data := exporttest.Data("A", plugin.PointData{PointName: "I", Value: 9.0})
				driverbox.Export([]plugin.DeviceData{data})
				if selection != nil && selection.Mode == "selected" {
					r.Assert(t, 1)
				} else {
					// Preserve the legacy failure path when selection is not enabled.
					r.Assert(t, 1, data)
				}
				if value, _ := driverbox.Shadow().GetDevicePoint("A", "I"); value != nil {
					t.Fatal("decode failure wrote a Shadow value")
				}
			}
		}
		if len(r.Events(event.Exporting)) != 0 {
			t.Fatal("decode failure emitted a processed-point event")
		}
	})
}

func TestPointSelectionReadWriteAndExport(t *testing.T) {
	p := &offsetPlugin{conn: &offsetConnector{}}
	exporttest.Run(t, "selection", p, func(r *exporttest.Recorder) {
		u, _ := driverbox.CoreCache().GetPointByDevice("A", "U")
		u["readWrite"] = "W"
		device, _ := driverbox.CoreCache().GetDevice("A")
		device.PointSelection = &config.PointSelection{Mode: "selected", Names: []string{"U", "P"}}
		if err := driverbox.CoreCache().AddOrUpdateDevice(device); err != nil {
			t.Fatal(err)
		}
		blocked := []func() error{
			func() error { return driverbox.ReadPoint("A", "I") },
			func() error { return driverbox.ReadPoint("A", "U") },
			func() error { return driverbox.WritePoint("A", plugin.PointData{PointName: "I", Value: 1}) },
			func() error { return driverbox.WritePoint("A", plugin.PointData{PointName: "P", Value: 1}) },
			func() error { return driverbox.ReadPoints("A", []plugin.PointData{{PointName: "P"}, {PointName: "I"}}) },
			func() error {
				return driverbox.WritePoints("A", []plugin.PointData{{PointName: "U", Value: 1}, {PointName: "I", Value: 1}})
			},
		}
		for i, call := range blocked {
			if err := call(); err == nil {
				t.Fatalf("blocked request %d accepted", i)
			}
		}
		if len(p.conn.sent) != 0 {
			t.Fatal("rejected request reached physical connector")
		}
		if value, err := driverbox.Shadow().GetWritePointValue("A", "U"); err == nil || value != nil {
			t.Fatal("rejected batch changed expected Shadow value")
		}
		if err := driverbox.WritePoint("A", plugin.PointData{PointName: "U", Value: 7}); err != nil {
			t.Fatal(err)
		}
		if err := driverbox.ReadPoint("A", "P"); err != nil {
			t.Fatal(err)
		}
		if len(p.conn.sent) != 2 {
			t.Fatalf("enabled requests sent=%d", len(p.conn.sent))
		}
		kept := plugin.PointData{PointName: "P", Value: 3.0}
		driverbox.Export([]plugin.DeviceData{exporttest.Data("A", kept, plugin.PointData{PointName: "I", Value: 9.0})})
		r.Assert(t, 1, exporttest.Data("A", kept))
		if v, _ := driverbox.Shadow().GetDevicePoint("A", "I"); v != nil {
			t.Fatal("excluded decoder value entered Shadow")
		}
		for _, value := range r.Events(event.Exporting) {
			data := value.(plugin.DeviceData)
			for _, point := range data.Values {
				if point.PointName == "I" {
					t.Fatal("excluded value entered point export event")
				}
			}
		}
		empty, _ := driverbox.CoreCache().GetDevice("B")
		empty.PointSelection = &config.PointSelection{Mode: "selected", Names: []string{}}
		if err := driverbox.CoreCache().AddOrUpdateDevice(empty); err != nil {
			t.Fatal(err)
		}
		if points, ok := driverbox.CoreCache().GetDevicePoints("B"); !ok || points == nil || len(points) != 0 {
			t.Fatal("zero-point identity lost")
		}
		if err := driverbox.ReadPoint("B", "P"); err == nil {
			t.Fatal("empty selection fell back to model")
		}
		if err := driverbox.WritePoints("B", []plugin.PointData{{PointName: "U", Value: 1}}); err == nil {
			t.Fatal("empty selection allowed write")
		}
		driverbox.Export([]plugin.DeviceData{exporttest.Data("B", kept)})
		r.Assert(t, 1)
	})
}
