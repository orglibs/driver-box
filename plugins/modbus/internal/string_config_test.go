package internal

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/orglibs/driver-box/v2/pkg/config"
)

func stringPoint() config.Point {
	return config.Point{"name": "text", "description": "text", "rawType": "String", "valueType": "string", "readWrite": "R", "primaryTable": "HOLDING_REGISTER", "startAddress": "27", "duration": "1s"}
}

func TestStringPointQuantity(t *testing.T) {
	for _, tt := range []struct {
		raw  interface{}
		want uint16
	}{
		{nil, 1}, {1, 1}, {3, 3}, {16, 16}, {24, 24}, {125, 125}, {json.Number("16"), 16},
	} {
		p := stringPoint()
		if tt.raw != nil {
			p["quantity"] = tt.raw
		}
		ext, err := convToPointExtend(p)
		if err != nil || ext.Quantity != tt.want {
			t.Fatalf("quantity %v: %+v %v; want %d", tt.raw, ext, err, tt.want)
		}
	}
	for _, raw := range []interface{}{0, 126, -1, 1.5, "16", nil, true, []int{16}, 65537, 1e20, json.Number("1.0"), json.Number("1e1")} {
		p := stringPoint()
		p["quantity"] = raw
		if _, err := convToPointExtend(p); err == nil {
			t.Fatalf("accepted quantity %#v", raw)
		}
	}
	for _, tt := range []struct {
		address  interface{}
		quantity int
		valid    bool
	}{
		{"65535", 1, true}, {"65535", 2, false}, {65411, 125, true}, {65412, 125, false},
		{"65536", 1, false}, {"0x10000", 1, false}, {"65536d", 1, false}, {-1, 1, false},
		{"0xffff", 1, true}, {"40001", 1, true},
	} {
		p := stringPoint()
		p["startAddress"] = tt.address
		p["quantity"] = tt.quantity
		_, err := convToPointExtend(p)
		if (err == nil) != tt.valid {
			t.Fatalf("address %v q=%d: %v", tt.address, tt.quantity, err)
		}
	}
}

func TestStringPointValidation(t *testing.T) {
	for _, tt := range []struct {
		field string
		value interface{}
		valid bool
	}{
		{"primaryTable", "INPUT_REGISTER", true}, {"primaryTable", "COIL", false}, {"primaryTable", "DISCRETE_INPUT", false},
		{"valueType", "int", false}, {"valueType", "float", false}, {"valueType", nil, false}, {"valueType", []int{1}, false},
		{"readWrite", nil, false}, {"readWrite", "", false}, {"readWrite", true, false}, {"readWrite", "RW", true}, {"readWrite", "W", true},
		{"rawType", "sTrInG", true},
	} {
		p := stringPoint()
		p[tt.field] = tt.value
		_, err := convToPointExtend(p)
		if (err == nil) != tt.valid {
			t.Fatalf("%s=%v: %v", tt.field, tt.value, err)
		}
	}
	for _, field := range []string{"scale", "decimals", "bit", "bitLen"} {
		for _, value := range []interface{}{0, float64(0), 1, -1, "0", nil, true} {
			p := stringPoint()
			p[field] = value
			_, err := convToPointExtend(p)
			valid := reflect.DeepEqual(value, 0) || reflect.DeepEqual(value, float64(0))
			if (err == nil) != valid {
				t.Fatalf("%s=%v: %v", field, value, err)
			}
		}
	}
	for _, access := range []string{"W", "RW"} {
		for _, quantity := range []int{123, 124, 125} {
			p := stringPoint()
			p["readWrite"] = access
			p["quantity"] = quantity
			_, err := convToPointExtend(p)
			if (err == nil) != (quantity == 123) {
				t.Fatalf("%s q=%d: %v", access, quantity, err)
			}
		}
		p := stringPoint()
		p["readWrite"] = access
		p["primaryTable"] = "INPUT_REGISTER"
		if _, err := convToPointExtend(p); err == nil {
			t.Fatal("accepted writable input registers")
		}
	}
	for rawType, want := range map[string]uint16{"Uint16": 1, "Uint32": 2, "Float64": 4} {
		for _, irrelevant := range []interface{}{nil, "invalid", 0, 65537, []int{1}} {
			p := stringPoint()
			p["rawType"] = rawType
			p["quantity"] = irrelevant
			ext, err := convToPointExtend(p)
			if err != nil || ext.Quantity != want {
				t.Fatalf("numeric compatibility: %+v, %v", ext, err)
			}
		}
	}
}

func TestConnectionStringPrevalidation(t *testing.T) {
	for _, tt := range []struct {
		quantity, batch int
		access          string
		valid           bool
	}{
		{24, 16, "R", false}, {24, 32, "R", true}, {125, 0, "R", false}, {125, 125, "R", true},
		{125, 500, "R", true}, {123, 1, "W", true}, {24, 16, "RW", false},
	} {
		p := stringPoint()
		p["quantity"] = tt.quantity
		p["readWrite"] = tt.access
		models := []config.DeviceModel{{Model: config.Model{Name: "model", DevicePoints: []config.Point{p}}, Devices: []config.Device{{ID: "dev", ConnectionKey: "conn"}}}}
		conf := &ConnectionConfig{BatchReadLen: uint16(tt.batch)}
		conf.ConnectionKey = "conn"
		err := validateConnectionStrings(conf, models)
		if (err == nil) != tt.valid {
			t.Fatalf("%+v: %v", tt, err)
		}
		if err != nil && (!strings.Contains(err.Error(), "model=model deviceId=dev pointName=text") || !strings.Contains(err.Error(), "batchReadLen")) {
			t.Fatal(err)
		}
	}
}

func TestStringGroupingBoundaries(t *testing.T) {
	for _, table := range []primaryTable{HoldingRegister, InputRegister, Coil, DiscreteInput} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%v", table, reverse), func(t *testing.T) {
				conf := &ConnectionConfig{BatchReadLen: 200}
				p := stringPoint()
				p["primaryTable"] = string(table)
				p["quantity"] = 125
				p["startAddress"] = "65411"
				other := stringPoint()
				other["name"] = "other"
				other["primaryTable"] = string(table)
				other["startAddress"] = "65410"
				other["rawType"] = "Uint16"
				if table == Coil || table == DiscreteInput {
					p["rawType"] = "Uint16"
				}
				model := config.DeviceModel{Model: config.Model{DevicePoints: []config.Point{p, other}}}
				dev := config.Device{ID: "dev", Properties: map[string]string{"unitID": "1"}}
				// Numeric addresses retain PLC notation; use explicit hex here.
				p["startAddress"] = "0xff83"
				other["startAddress"] = "0xff82"
				if table == Coil || table == DiscreteInput {
					other["startAddress"] = "0xff00"
				}
				var groups []*pointGroup
				if reverse {
					rc := &reverseConnector{config: conf, devicesMeta: map[string]config.Device{}, devicePointGroups: map[string][]*pointGroup{}}
					rc.addDevice(model, dev)
					groups = rc.devicePointGroups[dev.ID]
				} else {
					c := &connector{devices: map[uint8]*slaveDevice{}}
					c.createPointGroup(conf, model, dev)
					groups = c.devices[1].pointGroup
				}
				want := 2
				if table == Coil || table == DiscreteInput {
					want = 1
				}
				if len(groups) != want {
					t.Fatalf("groups=%+v want %d", groups, want)
				}
				for _, g := range groups {
					if uint32(g.Address)+uint32(g.Quantity) > 65536 {
						t.Fatalf("overflow: %+v", g)
					}
				}
			})
		}
	}
}

func TestStringConfiguredGroupLimit(t *testing.T) {
	for _, batch := range []uint16{24, 32} {
		for _, reverse := range []bool{false, true} {
			conf := &ConnectionConfig{BatchReadLen: batch}
			model := config.DeviceModel{Model: config.Model{DevicePoints: []config.Point{
				testStringPoint("version", "3", 24, "R"), testStringPoint("esn", "27", 16, "R"),
			}}}
			dev := config.Device{ID: "dev", Properties: map[string]string{"unitID": "1"}}
			var groups []*pointGroup
			if reverse {
				rc := &reverseConnector{config: conf, devicesMeta: map[string]config.Device{}, devicePointGroups: map[string][]*pointGroup{}}
				rc.addDevice(model, dev)
				groups = rc.devicePointGroups[dev.ID]
			} else {
				c := &connector{devices: map[uint8]*slaveDevice{}}
				c.createPointGroup(conf, model, dev)
				groups = c.devices[1].pointGroup
			}
			if len(groups) != 2 || groups[0].Address != 3 || groups[0].Quantity != 24 || groups[1].Address != 27 || groups[1].Quantity != 16 {
				t.Fatalf("batch=%d reverse=%v split String: %+v", batch, reverse, groups)
			}
		}
	}
}

func TestPointReadWindowBounds(t *testing.T) {
	for _, tt := range []struct {
		address, group, quantity uint16
		count                    int
		valid                    bool
	}{
		{65535, 65535, 1, 1, true}, {65535, 65534, 1, 2, true}, {65535, 65535, 2, 2, false},
		{0, 65535, 1, 2, false}, {10, 11, 1, 2, false}, {11, 10, 2, 2, false}, {10, 10, 0, 1, false},
	} {
		_, err := pointReadWindow(make([]uint16, tt.count), tt.group, &Point{Address: tt.address, Quantity: tt.quantity})
		if (err == nil) != tt.valid {
			t.Fatalf("%+v: %v", tt, err)
		}
	}
}
