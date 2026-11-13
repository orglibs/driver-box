package internal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/orglibs/driver-box/v2/pkg/config"
)

func TestModbusReadWriteEnums(t *testing.T) {
	for _, access := range []config.ReadWrite{config.ReadWrite_R, config.ReadWrite_W, config.ReadWrite_RW} {
		for _, raw := range []interface{}{string(access), access} {
			t.Run(fmt.Sprintf("%T/%s", raw, access), func(t *testing.T) {
				point := stringPoint()
				point["readWrite"] = raw
				if _, err := convToPointExtend(point); err != nil {
					t.Fatal(err)
				}
				// A write-only String is not subject to the read batching limit.
				point["quantity"] = 24
				conf := &ConnectionConfig{BatchReadLen: 16}
				conf.ConnectionKey = "conn"
				model := config.DeviceModel{Model: config.Model{Name: "model", DevicePoints: []config.Point{point}},
					Devices: []config.Device{{ID: "dev", ConnectionKey: "conn"}}}
				err := validateConnectionPoints(conf, []config.DeviceModel{model})
				if (err == nil) != (access == config.ReadWrite_W) {
					t.Fatalf("read-limit validation for %T(%v): %v", raw, raw, err)
				}
				point["quantity"], point["primaryTable"] = 1, string(InputRegister)
				_, err = convToPointExtend(point)
				if (err == nil) != (access == config.ReadWrite_R) {
					t.Fatalf("input register validation for %T(%v): %v", raw, raw, err)
				}
			})
		}
	}
	for _, raw := range []interface{}{"W", config.ReadWrite_W} {
		point := writeOnlyTestPoint("command", 101, "W")
		point["readWrite"] = raw
		if got, err := writeOnlyReadBarriers([]config.Point{point}); err != nil || len(got) != 1 {
			t.Fatalf("write-only barrier missing for %T(%v): %v", raw, raw, err)
		}
		point["bitLen"] = 1
		if _, err := convToPointExtend(point); err == nil || !strings.Contains(err.Error(), "write-only") {
			t.Fatalf("write-only bit validation for %T(%v): %v", raw, raw, err)
		}
	}
}

func TestModbusInvalidReadWrite(t *testing.T) {
	for _, rawType := range []string{"Uint16", "String"} {
		for _, raw := range []interface{}{nil, true, 1, 1.5, []string{"W"}, map[string]string{"value": "W"}, "", "w", "invalid", config.ReadWrite("invalid")} {
			t.Run(fmt.Sprintf("%s/%T/%v", rawType, raw, raw), func(t *testing.T) {
				point := writeOnlyTestPoint("invalid", 100, "W")
				point["rawType"], point["readWrite"] = rawType, raw
				if rawType == "String" {
					point["valueType"] = "string"
				}
				conf := &ConnectionConfig{}
				conf.ConnectionKey = "conn"
				model := config.DeviceModel{Model: config.Model{Name: "model", DevicePoints: []config.Point{point}},
					Devices: []config.Device{{ID: "dev", ConnectionKey: "conn"}}}
				err := validateConnectionPoints(conf, []config.DeviceModel{model})
				if err == nil || !strings.Contains(err.Error(), "readWrite") || !strings.Contains(err.Error(), "connectionKey=conn model=model deviceId=dev pointName=invalid") {
					t.Fatalf("invalid readWrite did not produce a contextual error: %v", err)
				}
			})
		}
	}
}

func TestModbusEnumReadGrouping(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, skip := range []bool{false, true} {
			first, last, command := writeOnlyTestPoint("first", 100, "R"), writeOnlyTestPoint("last", 102, "RW"), writeOnlyTestPoint("command", 101, "W")
			first["readWrite"], last["readWrite"], command["readWrite"] = config.ReadWrite_R, config.ReadWrite_RW, config.ReadWrite_W
			groups := modbusTestReadGroups(&ConnectionConfig{SkipWriteOnlyInBatchRead: skip},
				config.DeviceModel{Model: config.Model{DevicePoints: []config.Point{first, last, command}}},
				config.Device{ID: "dev", Properties: map[string]string{"unitID": "1"}}, reverse)
			wantGroups := 1
			if skip {
				wantGroups = 2
			}
			if len(groups) != wantGroups {
				t.Fatalf("reverse=%v skip=%v: groups=%d, want %d", reverse, skip, len(groups), wantGroups)
			}
			for _, group := range groups {
				for _, point := range group.Points {
					if point.ReadWrite() == config.ReadWrite_W {
						t.Fatal("enum write-only point entered a read group")
					}
				}
			}
		}
	}
}
