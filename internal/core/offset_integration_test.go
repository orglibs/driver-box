package core_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/testutil/exporttest"
	"github.com/orglibs/driver-box/v2/pkg/config"
)

type offsetPlugin struct {
	plugin.Plugin
	conn *offsetConnector
}

func (p *offsetPlugin) Connector(string) (plugin.Connector, error) { return p.conn, nil }

type offsetConnector struct{ sent [][]plugin.PointData }

func (c *offsetConnector) Encode(_ string, _ plugin.EncodeMode, values ...plugin.PointData) (interface{}, error) {
	return append([]plugin.PointData(nil), values...), nil
}
func (c *offsetConnector) Send(raw interface{}) error {
	c.sent = append(c.sent, raw.([]plugin.PointData))
	return nil
}
func (*offsetConnector) Release() error { return nil }

func TestOffsetWriteAPI(t *testing.T) {
	p := &offsetPlugin{conn: &offsetConnector{}}
	exporttest.Run(t, "offset", p, func(_ *exporttest.Recorder) {
		u, _ := driverbox.CoreCache().GetPointByDevice("A", "U")
		u["scale"], u["offset"], u["rawType"], u["readWrite"] = .1, -40, "Uint16", "W"
		i, _ := driverbox.CoreCache().GetPointByDevice("A", "I")
		i["valueType"], i["offset"], i["rawType"], i["readWrite"] = "int", 2, "Int16", "W"
		if err := driverbox.WritePoint("A", plugin.PointData{PointName: "U", Value: 25}); err != nil {
			t.Fatal(err)
		}
		if got := p.conn.sent[0][0].Value; got != uint64(650) {
			t.Fatalf("single write=%#v", got)
		}
		input := []plugin.PointData{{PointName: "U", Value: float64(0)}, {PointName: "I", Value: int64(-5)}}
		original := append([]plugin.PointData(nil), input...)
		if err := driverbox.WritePoints("A", input); err != nil {
			t.Fatal(err)
		}
		want := []plugin.PointData{{PointName: "U", Value: uint64(400)}, {PointName: "I", Value: int64(-7)}}
		if !reflect.DeepEqual(p.conn.sent[1], want) || !reflect.DeepEqual(input, original) {
			t.Fatalf("batch=%#v input=%#v", p.conn.sent[1], input)
		}
		for _, value := range []interface{}{25.05, -41, 6513.6, "NaN"} {
			if err := driverbox.WritePoint("A", plugin.PointData{PointName: "U", Value: value}); err == nil {
				t.Fatalf("accepted invalid write %v", value)
			}
		}
		i["offset"] = nil
		if err := driverbox.WritePoints("A", input); err == nil {
			t.Fatal("accepted invalid offset")
		}
		if len(p.conn.sent) != 2 {
			t.Fatal("failed conversion sent a partial write")
		}
		// A zero/omitted offset keeps the original scale-only conversion path.
		u["offset"] = 0
		if err := driverbox.WritePoint("A", plugin.PointData{PointName: "U", Value: 25.05}); err != nil {
			t.Fatal(err)
		}
		if got := p.conn.sent[2][0].Value; got != 25.05/.1 {
			t.Fatalf("legacy scale path changed: %#v", got)
		}
		large, _ := driverbox.CoreCache().GetPointByDevice("A", "P")
		large["valueType"], large["readWrite"], large["offset"] = "int", "W", 0
		if err := driverbox.WritePoint("A", plugin.PointData{PointName: "P", Value: int64(9007199254740993)}); err != nil {
			t.Fatal(err)
		}
		if got := p.conn.sent[3][0].Value; got != int64(9007199254740993) {
			t.Fatalf("zero offset lost integer precision: %#v", got)
		}
	})
}

func TestOffsetLuaOwnsConversion(t *testing.T) {
	p := &offsetPlugin{conn: &offsetConnector{}}
	exporttest.Run(t, "offset-lua", p, func(r *exporttest.Recorder) {
		u, _ := driverbox.CoreCache().GetPointByDevice("A", "U")
		u["scale"], u["offset"], u["rawType"], u["readWrite"] = .1, -40, "Uint16", "W"
		dir := filepath.Join(config.ResourcePath, "library", "driver")
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
		script := `function encode(device, mode, points)
  for _, p in ipairs(points) do p.value = p.value - 100 end
  return points
end
function decode(device, points)
  for _, p in ipairs(points) do p.value = p.value + 100 end
  return points
end`
		if err := os.WriteFile(filepath.Join(dir, "offset.lua"), []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		dev, _ := driverbox.CoreCache().GetDevice("A")
		dev.DriverKey = "offset"
		if err := driverbox.CoreCache().AddOrUpdateDevice(dev); err != nil {
			t.Fatal(err)
		}
		if err := driverbox.WritePoint("A", plugin.PointData{PointName: "U", Value: 105}); err != nil {
			t.Fatal(err)
		}
		if got := p.conn.sent[0][0].Value; fmt.Sprint(got) != "5" {
			t.Fatalf("Lua write converted twice: %#v", got)
		}
		driverbox.Export([]plugin.DeviceData{exporttest.Data("A", plugin.PointData{PointName: "U", Value: 10})})
		r.Assert(t, 1, exporttest.Data("A", plugin.PointData{PointName: "U", Value: float64(110)}))
	})
}
