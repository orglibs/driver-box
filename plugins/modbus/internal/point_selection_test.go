package internal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/orglibs/driver-box/v2/pkg/config"
)

func TestPointSelectionModbusReadGroups(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, tc := range []struct {
			selection *config.PointSelection
			count     int
		}{
			{nil, 2}, {&config.PointSelection{Mode: "all"}, 2},
			{&config.PointSelection{Mode: "selected", Names: []string{"first"}}, 1},
			{&config.PointSelection{Mode: "selected", Names: []string{}}, 0},
			{&config.PointSelection{Mode: "selected", Names: []string{"first", "last"}}, 2},
		} {
			t.Run(fmt.Sprintf("reverse=%v/%v", reverse, tc.selection), func(t *testing.T) {
				model := config.DeviceModel{Model: config.Model{DevicePoints: []config.Point{writeOnlyTestPoint("first", 100, "R"), writeOnlyTestPoint("command", 101, "W"), writeOnlyTestPoint("last", 102, "R")}}}
				dev := config.Device{ID: "dev", Properties: map[string]string{"unitID": "1"}, PointSelection: tc.selection}
				groups := modbusTestReadGroups(&ConnectionConfig{SkipWriteOnlyInBatchRead: true}, model, dev, reverse)
				if len(groups) != tc.count {
					t.Fatalf("groups=%d want=%d", len(groups), tc.count)
				}
				for _, group := range groups {
					if group.Quantity != 1 {
						t.Fatal("unselected write-only address lost its read barrier")
					}
				}
			})
		}
	}
}

func TestPointSelectionModbusConnectionValidation(t *testing.T) {
	point := writeOnlyTestPoint("text", 100, "R")
	point["rawType"], point["valueType"], point["quantity"] = "String", "string", 64
	dev := config.Device{ID: "dev", ConnectionKey: "bus", PointSelection: &config.PointSelection{Mode: "selected", Names: []string{}}}
	model := config.DeviceModel{Model: config.Model{DevicePoints: []config.Point{point}}, Devices: []config.Device{dev}}
	conf := &ConnectionConfig{}
	conf.ConnectionKey = "bus"
	if err := validateConnectionPoints(conf, []config.DeviceModel{model}); err != nil {
		t.Fatal("unselected point applied device read limit", err)
	}
	model.Devices[0].PointSelection.Names = []string{"text"}
	if err := validateConnectionPoints(conf, []config.DeviceModel{model}); err == nil {
		t.Fatal("selected String escaped batch read limit")
	}
}

func TestPointSelectionUnselectedWriteOnlyBarriers(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, field := range []string{"bitLen", "offset", "byteSwap", "scale", "valueType", "String"} {
			t.Run(fmt.Sprintf("reverse=%v/%s", reverse, field), func(t *testing.T) {
				command := writeOnlyTestPoint("command", 101, "W")
				if field == "String" {
					command["rawType"], command["quantity"], command["offset"] = "String", 3, nil
				} else {
					command[field] = "invalid write parameter"
					if field == "bitLen" {
						command[field] = 1
					}
				}
				model := config.DeviceModel{Model: config.Model{Name: "model", DevicePoints: []config.Point{
					writeOnlyTestPoint("first", 100, "R"), command, writeOnlyTestPoint("last", 105, "R"),
				}}}
				dev := config.Device{ID: "dev", ConnectionKey: "bus", Properties: map[string]string{"unitID": "1"},
					PointSelection: &config.PointSelection{Mode: "selected", Names: []string{"first", "last"}}}
				model.Devices = []config.Device{dev}
				conf := &ConnectionConfig{SkipWriteOnlyInBatchRead: true}
				conf.ConnectionKey = "bus"
				if err := validateConnectionPoints(conf, []config.DeviceModel{model}); err != nil {
					t.Fatal(err)
				}
				groups := modbusTestReadGroups(conf, model, dev, reverse)
				if len(groups) != 2 || groups[0].Address != 100 || groups[1].Address != 105 || groups[0].Quantity != 1 || groups[1].Quantity != 1 {
					t.Fatalf("unselected write-only address lost its barrier: %+v", groups)
				}
				conf.SkipWriteOnlyInBatchRead = false
				groups = modbusTestReadGroups(conf, model, dev, reverse)
				if len(groups) != 1 || groups[0].Address != 100 || groups[0].Quantity != 6 {
					t.Fatal("default batching behavior changed")
				}
			})
		}
	}
}

func TestPointSelectionInvalidWriteOnlyBarrierRejected(t *testing.T) {
	for _, tc := range []struct {
		field string
		value interface{}
	}{
		{"startAddress", "invalid"}, {"startAddress", nil}, {"startAddress", "-1d"}, {"startAddress", "0x10000"},
		{"startAddress", "65536d"}, {"rawType", "unknown"}, {"primaryTable", "unknown"},
	} {
		command := writeOnlyTestPoint("command", 101, "W")
		command[tc.field] = tc.value
		model := config.DeviceModel{Model: config.Model{Name: "model", DevicePoints: []config.Point{
			writeOnlyTestPoint("first", 100, "R"), command, writeOnlyTestPoint("last", 105, "R"),
		}}}
		dev := config.Device{ID: "dev", ConnectionKey: "bus", Properties: map[string]string{"unitID": "1"},
			PointSelection: &config.PointSelection{Mode: "selected", Names: []string{"first", "last"}}}
		model.Devices = []config.Device{dev}
		conf := &ConnectionConfig{SkipWriteOnlyInBatchRead: true}
		conf.ConnectionKey = "bus"
		err := validateConnectionPoints(conf, []config.DeviceModel{model})
		if err == nil || !strings.Contains(err.Error(), "command") || !strings.Contains(err.Error(), "bus") || !strings.Contains(err.Error(), "dev") {
			t.Fatalf("%s=%v did not reject an unknown barrier range: %v", tc.field, tc.value, err)
		}
		for _, reverse := range []bool{false, true} {
			if groups := modbusTestReadGroups(conf, model, dev, reverse); len(groups) != 0 {
				t.Fatalf("reverse=%v created reads despite an invalid barrier", reverse)
			}
		}
		conf.SkipWriteOnlyInBatchRead = false
		if err := validateConnectionPoints(conf, []config.DeviceModel{model}); err != nil {
			t.Fatalf("disabled barrier checking changed validation: %v", err)
		}
	}
}
