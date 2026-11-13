package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/testutil/exporttest"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"github.com/orglibs/driver-box/v2/pkg/convutil"
	"github.com/orglibs/driver-box/v2/pkg/luautil"
)

func writeOnlyTestPoint(name string, address uint16, access string) config.Point {
	return config.Point{"name": name, "description": name, "valueType": "int", "rawType": "Uint16",
		"primaryTable": string(HoldingRegister), "startAddress": fmt.Sprintf("0x%x", address), "readWrite": access}
}

func modbusTestReadGroups(conf *ConnectionConfig, model config.DeviceModel, dev config.Device, reverse bool) []*pointGroup {
	if reverse {
		rc := &reverseConnector{config: conf, devicesMeta: map[string]config.Device{}, devicePointGroups: map[string][]*pointGroup{}}
		rc.addDevice(model, dev)
		return rc.devicePointGroups[dev.ID]
	}
	c := &connector{devices: map[uint8]*slaveDevice{}}
	c.createPointGroup(conf, model, dev)
	return c.devices[1].pointGroup
}

func TestModbusSkipWriteOnlyInBatchRead(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, table := range []primaryTable{HoldingRegister, Coil} {
			for _, skip := range []interface{}{nil, false, true} {
				t.Run(fmt.Sprintf("reverse=%v/%s/skip=%v", reverse, table, skip), func(t *testing.T) {
					input := map[string]interface{}{}
					if skip != nil {
						input["skipWriteOnlyInBatchRead"] = skip
					}
					conf := &ConnectionConfig{}
					if err := convutil.Struct(input, conf); err != nil {
						t.Fatal(err)
					}
					points := []config.Point{writeOnlyTestPoint("first", 100, "R"), writeOnlyTestPoint("last", 102, "R"), writeOnlyTestPoint("command", 101, "W")}
					for _, point := range points {
						point["primaryTable"] = string(table)
					}
					groups := modbusTestReadGroups(conf, config.DeviceModel{Model: config.Model{DevicePoints: points}},
						config.Device{ID: "dev", Properties: map[string]string{"unitID": "1"}}, reverse)
					want := [][2]uint16{{100, 3}}
					if skip == true {
						want = [][2]uint16{{100, 1}, {102, 1}}
					}
					var got [][2]uint16
					readPoints := 0
					for _, group := range groups {
						got = append(got, [2]uint16{group.Address, group.Quantity})
						for _, point := range group.Points {
							if point.ReadWrite() == config.ReadWrite_W {
								t.Fatal("write-only point must not become a collected point")
							}
							readPoints++
						}
					}
					if !reflect.DeepEqual(got, want) || readPoints != 2 {
						t.Fatalf("read windows = %v points=%d, want %v and two readable points", got, readPoints, want)
					}
				})
			}
		}
		for _, skip := range []bool{false, true} {
			t.Run(fmt.Sprintf("reverse=%v/overlapping-point/skip=%v", reverse, skip), func(t *testing.T) {
				first := writeOnlyTestPoint("first", 100, "R")
				first["rawType"] = "Uint32"
				points := []config.Point{first, writeOnlyTestPoint("last", 104, "R"), writeOnlyTestPoint("command", 101, "W")}
				groups := modbusTestReadGroups(&ConnectionConfig{SkipWriteOnlyInBatchRead: skip},
					config.DeviceModel{Model: config.Model{DevicePoints: points}}, config.Device{ID: "dev", Properties: map[string]string{"unitID": "1"}}, reverse)
				address, quantity := uint16(100), uint16(5)
				if skip {
					address, quantity = 104, 1
				}
				if len(groups) != 1 || groups[0].Address != address || groups[0].Quantity != quantity {
					t.Fatalf("unexpected read groups: %+v", groups)
				}
			})
		}
	}
}

func TestModbusMaxReadGap(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, tt := range []struct {
			name       string
			batch      uint16
			gap        interface{}
			last       uint16
			blocked    bool
			wantGroups int
		}{
			{"default", 0, nil, 102, false, 1},
			{"default-batch-boundary", 0, nil, 131, false, 1},
			{"default-follows-larger-batch", 64, nil, 141, false, 1},
			{"strict", 32, 0, 102, false, 2},
			{"strict-adjacent", 32, 0, 101, false, 1},
			{"explicit-gap", 32, 1, 102, false, 1},
			{"gap-exceeded", 32, 1, 103, false, 2},
			{"gap-boundary", 32, 2, 103, false, 1},
			{"batch-limit", 4, 65535, 105, false, 2},
			{"protocol-limit", 500, nil, 225, false, 2},
			{"write-only-default", 32, nil, 102, true, 2},
			{"write-only-explicit-gap", 32, 100, 102, true, 2},
		} {
			t.Run(fmt.Sprintf("reverse=%v/%s", reverse, tt.name), func(t *testing.T) {
				input := map[string]interface{}{"batchReadLen": tt.batch, "skipWriteOnlyInBatchRead": tt.blocked}
				if tt.gap != nil {
					input["maxReadGap"] = tt.gap
				}
				conf := &ConnectionConfig{}
				if err := convutil.Struct(input, conf); err != nil {
					t.Fatal(err)
				}
				points := []config.Point{writeOnlyTestPoint("first", 100, "R"), writeOnlyTestPoint("last", tt.last, "R")}
				if tt.blocked {
					points = append(points, writeOnlyTestPoint("command", 101, "W"))
				}
				model := config.DeviceModel{Model: config.Model{DevicePoints: points}}
				dev := config.Device{ID: "dev", Properties: map[string]string{"unitID": "1"}}
				groups := modbusTestReadGroups(conf, model, dev, reverse)
				if len(groups) != tt.wantGroups {
					t.Fatalf("read groups = %d, want %d", len(groups), tt.wantGroups)
				}
				if len(groups) == 1 && (groups[0].Address != 100 || groups[0].Quantity != tt.last-99) {
					t.Fatalf("merged read window = %d/%d", groups[0].Address, groups[0].Quantity)
				}
			})
		}
	}
	for _, invalid := range []interface{}{-1, 65536, 1.5, "0", true} {
		if err := convutil.Struct(map[string]interface{}{"maxReadGap": invalid}, &ConnectionConfig{}); err == nil {
			t.Fatalf("accepted invalid maxReadGap %#v", invalid)
		}
	}
}

func TestModbusWriteOnlyBarriersAcrossModels(t *testing.T) {
	for _, scope := range []string{"same-slave", "other-slave", "other-connection", "other-table", "multi-register"} {
		for _, writeFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/write-first=%v", scope, writeFirst), func(t *testing.T) {
				conf := &ConnectionConfig{SkipWriteOnlyInBatchRead: true}
				conf.ConnectionKey = "conn"
				dev := config.Device{ID: "read", ConnectionKey: "conn", Properties: map[string]string{"unitID": "1"}}
				writeDev := config.Device{ID: "write", ConnectionKey: "conn", Properties: map[string]string{"unitID": "1"}}
				blocked := writeOnlyTestPoint("command", 101, "W")
				want := 2
				switch scope {
				case "other-slave":
					writeDev.Properties["unitID"], want = "2", 1
				case "other-connection":
					writeDev.ConnectionKey, want = "other", 1
				case "other-table":
					blocked["primaryTable"], want = string(Coil), 1
				case "multi-register":
					blocked["rawType"] = "Uint32"
				}
				readModel := config.DeviceModel{Model: config.Model{DevicePoints: []config.Point{writeOnlyTestPoint("first", 100, "R"), writeOnlyTestPoint("last", 104, "R")}}, Devices: []config.Device{dev}}
				writeModel := config.DeviceModel{Model: config.Model{DevicePoints: []config.Point{blocked}}, Devices: []config.Device{writeDev}}
				models := []config.DeviceModel{readModel, writeModel}
				if writeFirst {
					models[0], models[1] = models[1], models[0]
				}
				c := &connector{devices: map[uint8]*slaveDevice{}, plugin: &Plugin{config: config.DeviceConfig{DeviceModels: models}}}
				for _, model := range models {
					if model.Devices[0].ConnectionKey == conf.ConnectionKey {
						c.createPointGroup(conf, model, model.Devices[0])
					}
				}
				if got := len(c.devices[1].pointGroup); got != want {
					t.Fatalf("read groups = %d, want %d", got, want)
				}
			})
		}
	}
}

func TestModbusReadGroupsAvoidUnreadableGaps(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, table := range []primaryTable{HoldingRegister, InputRegister, Coil, DiscreteInput} {
			for _, gap := range []string{"write-only", "unconfigured", "adjacent", "descending", "overlap", "multi-register", "address-boundary", "contiguous-coils"} {
				if gap == "contiguous-coils" && table != Coil && table != DiscreteInput || gap == "multi-register" && (table == Coil || table == DiscreteInput) {
					continue
				}
				t.Run(fmt.Sprintf("reverse=%v/%s/%s", reverse, table, gap), func(t *testing.T) {
					points := []config.Point{writeOnlyTestPoint("first", 100, "R"), writeOnlyTestPoint("last", 102, "RW")}
					want := [][2]uint16{{100, 1}, {102, 1}}
					switch gap {
					case "write-only":
						points = append(points, writeOnlyTestPoint("command", 101, "W"))
					case "adjacent":
						points[1]["startAddress"] = "0x65"
						want = [][2]uint16{{100, 2}}
					case "descending":
						points[0], points[1] = points[1], points[0]
					case "overlap":
						points[1]["startAddress"] = "0x64"
						want = [][2]uint16{{100, 1}}
					case "multi-register":
						points[0]["rawType"] = "Uint32"
						want = [][2]uint16{{100, 3}}
					case "address-boundary":
						points[0]["startAddress"], points[1]["startAddress"] = "0xfffe", "0xffff"
						want = [][2]uint16{{65534, 2}}
					case "contiguous-coils":
						points = nil
						for address := uint16(100); address < 232; address++ {
							points = append(points, writeOnlyTestPoint(fmt.Sprint(address), address, "R"))
						}
						want = [][2]uint16{{100, 132}}
					}
					for _, point := range points {
						point["primaryTable"] = string(table)
					}
					strictGap := uint16(0)
					conf := &ConnectionConfig{BatchReadLen: 200, MaxReadGap: &strictGap, SkipWriteOnlyInBatchRead: true}
					model := config.DeviceModel{Model: config.Model{DevicePoints: points}}
					dev := config.Device{ID: "device", Properties: map[string]string{"unitID": "1"}}
					groups := modbusTestReadGroups(conf, model, dev, reverse)
					var got [][2]uint16
					for _, group := range groups {
						got = append(got, [2]uint16{group.Address, group.Quantity})
						for _, point := range group.Points {
							if point.ReadWrite() == config.ReadWrite_W {
								t.Fatal("write-only point entered a read group")
							}
						}
					}
					sort.Slice(got, func(i, j int) bool { return got[i][0] < got[j][0] })
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("read windows = %v, want %v", got, want)
					}
				})
			}
		}
	}
}

func TestModbusWriteOnlyReadTasks(t *testing.T) {
	conf := &ConnectionConfig{}
	model := config.DeviceModel{Model: config.Model{DevicePoints: []config.Point{writeOnlyTestPoint("command", 100, "W")}}}
	dev := config.Device{ID: "dev", Properties: map[string]string{"unitID": "1"}}
	c := &connector{devices: map[uint8]*slaveDevice{}}
	c.createPointGroup(conf, model, dev)
	if c.devices[1] == nil || len(c.devices[1].pointGroup) != 0 {
		t.Fatal("write-only device must remain registered without read groups")
	}
	if task, err := c.initCollectTask(conf); task != nil || err != nil {
		if task != nil {
			task.Disable()
		}
		t.Fatalf("write-only forward read task = %v, %v", task, err)
	}
	rc := &reverseConnector{config: conf, devicesMeta: map[string]config.Device{}, devicePointGroups: map[string][]*pointGroup{}}
	rc.addDevice(model, dev)
	if _, ok := rc.devicesMeta[dev.ID]; !ok || len(rc.devicePointGroups[dev.ID]) != 0 {
		t.Fatal("write-only reverse device must remain registered without read groups")
	}
	if task, err := rc.initCollectTask(); task != nil || err != nil {
		if task != nil {
			task.Disable()
		}
		t.Fatalf("write-only reverse read task = %v, %v", task, err)
	}
}

func TestModbusWriteOnlyBitConfiguration(t *testing.T) {
	for _, rawType := range []string{"Int16", "Uint16"} {
		for _, access := range []string{"W", "RW", "R"} {
			point := writeOnlyTestPoint("command", 100, access)
			point["rawType"], point["bitLen"] = rawType, 1
			_, err := convToPointExtend(point)
			if access == "W" {
				if err == nil || !strings.Contains(err.Error(), "write-only") {
					t.Fatalf("%s %s bit configuration: %v", rawType, access, err)
				}
			} else if err != nil {
				t.Fatalf("readable bit point rejected: %v", err)
			}
			conf := &ConnectionConfig{}
			conf.ConnectionKey = "conn"
			models := []config.DeviceModel{{Model: config.Model{Name: "model", DevicePoints: []config.Point{point}}, Devices: []config.Device{{ID: "dev", ConnectionKey: "conn"}}}}
			err = validateConnectionPoints(conf, models)
			if access == "W" && (err == nil || !strings.Contains(err.Error(), "connectionKey=conn model=model deviceId=dev pointName=command")) {
				t.Fatalf("write-only bit prevalidation missing context: %v", err)
			}
			if access != "W" && err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Isolate the framework cache, Lua VM and singleton exporters in a child process.
// The virtual transport fails every read so successful writes cannot hide a read.
func TestModbusWriteOnlyConnectionIntegration(t *testing.T) {
	p := &Plugin{}
	exporttest.Run(t, ProtocolName, p, func(_ *exporttest.Recorder) {
		var points []config.Point
		for i, name := range []string{"U", "I", "P"} {
			point, _ := driverbox.CoreCache().GetPointByDevice("A", name)
			for key, value := range writeOnlyTestPoint(name, uint16(100+i), "W") {
				point[key] = value
			}
			// Exercise mixed enum/string access modes through the public write APIs.
			if name != "P" {
				point["readWrite"] = config.ReadWrite_W
			}
			points = append(points, point)
		}
		dev, _ := driverbox.CoreCache().GetDevice("A")
		dev.ConnectionKey, dev.Properties = "write-only", map[string]string{"unitID": "1"}
		if err := driverbox.CoreCache().AddOrUpdateDevice(dev); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(config.ResourcePath, "driver", ProtocolName, "converter.lua")
		if err := os.WriteFile(script, []byte(`local writes = 0
function mockRead(...) error("write-only device must never be read") end
function mockWrite(unit, kind, address, values)
  assert(unit == 1 and kind == "HOLDING_REGISTER")
  assert(values[1] == address - 99)
  writes = writes + 1
  return "ok"
end
function mockCount() return tostring(writes) end`), 0600); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if ls != nil {
				luautil.Close(ls)
				ls = nil
			}
		}()
		for i, mode := range []string{"tcp", "rtuovertcp", "rtu"} {
			address := "127.0.0.1:502"
			if mode == "rtu" {
				address = "/dev/null"
			}
			p.Initialize(config.DeviceConfig{Connections: map[string]interface{}{"write-only": map[string]interface{}{
				"mode": mode, "address": address, "virtual": true, "enable": true,
			}}, DeviceModels: []config.DeviceModel{{Model: config.Model{Name: "batch", DevicePoints: points}, Devices: []config.Device{dev}}}})
			conn, err := p.Connector("A")
			if err != nil {
				t.Fatalf("%s write-only connection missing: %v", mode, err)
			}
			forward := conn.(*connector)
			defer forward.Close()
			if forward.collectTask != nil || len(forward.devices[1].pointGroup) != 0 {
				t.Fatalf("%s write-only connection has a read task", mode)
			}
			if err := driverbox.WritePoint("A", plugin.PointData{PointName: "U", Value: 1}); err != nil {
				t.Fatal(err)
			}
			if err := driverbox.WritePoints("A", []plugin.PointData{{PointName: "I", Value: 2}, {PointName: "P", Value: 3}}); err != nil {
				t.Fatal(err)
			}
			count, err := luautil.CallLuaMethod(ls, "mockCount")
			if err != nil || count != fmt.Sprint((i+1)*3) {
				t.Fatalf("%s writes = %s, %v", mode, count, err)
			}
			if err := driverbox.ReadPoint("A", "U"); err == nil || !strings.Contains(err.Error(), "writeOnly") {
				t.Fatalf("explicit read was not rejected locally: %v", err)
			}
		}
		// Runtime mutations must also fail before the read-before-write path.
		points[0]["bitLen"] = 1
		reads := 0
		_, err := (&connector{}).getWriteValueWithReader("A", plugin.PointData{PointName: "U", Value: 1}, nil,
			func(uint8, string, uint16, uint16) ([]uint16, error) { reads++; return []uint16{0}, nil })
		if err == nil || !strings.Contains(err.Error(), "write-only") || reads != 0 {
			t.Fatalf("write-only bit write: reads=%d error=%v", reads, err)
		}
		_, err = (&reverseConnector{}).Encode("A", plugin.WriteMode, plugin.PointData{PointName: "U", Value: 1})
		if err == nil || !strings.Contains(err.Error(), "write-only") {
			t.Fatalf("reverse write-only bit write: %v", err)
		}
		// Read/write bit points retain their existing read-modify-write behavior.
		points[0]["readWrite"] = "RW"
		value, err := (&connector{}).getWriteValueWithReader("A", plugin.PointData{PointName: "U", Value: 1}, nil,
			func(uint8, string, uint16, uint16) ([]uint16, error) { reads++; return []uint16{0xaaa0}, nil })
		if err != nil || reads != 1 || !reflect.DeepEqual(value.Value, []uint16{0xaaa1}) {
			t.Fatalf("read/write bit write: value=%v reads=%d error=%v", value.Value, reads, err)
		}
	})
}
