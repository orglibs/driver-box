package internal

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/testutil/exporttest"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"github.com/orglibs/driver-box/v2/pkg/luautil"
)

func TestModbusOffsetConfiguration(t *testing.T) {
	for _, tt := range []struct {
		field string
		value interface{}
		valid bool
	}{
		{"offset", 0, true}, {"offset", -40, true}, {"offset", .25, true},
		{"offset", nil, false}, {"offset", "10", false}, {"offset", true, false}, {"offset", []int{1}, false},
		{"scale", "0.1", false}, {"valueType", "string", false}, {"primaryTable", "COIL", false},
		{"primaryTable", "DISCRETE_INPUT", false}, {"bitLen", 1, false}, {"rawType", "String", false},
	} {
		p := config.Point{"name": "temperature", "description": "temperature", "valueType": "float", "rawType": "Uint16", "readWrite": "W", "startAddress": "100d", "primaryTable": "HOLDING_REGISTER", "offset": -40}
		p[tt.field] = tt.value
		_, err := convToPointExtend(p)
		if (err == nil) != tt.valid {
			t.Fatalf("%s=%v: %v", tt.field, tt.value, err)
		}
		models := []config.DeviceModel{{Model: config.Model{Name: "offset", DevicePoints: []config.Point{p}}, Devices: []config.Device{{ID: "A", ConnectionKey: "offset"}}}}
		conf := &ConnectionConfig{}
		conf.ConnectionKey = "offset"
		err = validateConnectionPoints(conf, models)
		if (err == nil) != tt.valid {
			t.Fatalf("prevalidation %s=%v: %v", tt.field, tt.value, err)
		}
		if err != nil && !strings.Contains(err.Error(), "connectionKey=offset model=offset deviceId=A pointName=temperature") {
			t.Fatal(err)
		}
	}
	text := stringPoint()
	text["offset"] = 0
	if _, err := convToPointExtend(text); err != nil {
		t.Fatal(err)
	}
	text["offset"] = 1
	if _, err := convToPointExtend(text); err == nil {
		t.Fatal("String accepted nonzero offset")
	}
	// Bad offset must be rejected before clients or reverse listeners are created.
	for _, mode := range []string{"tcp", "reverse_tcp"} {
		p := &Plugin{}
		p.Initialize(config.DeviceConfig{Connections: map[string]interface{}{"bad": map[string]interface{}{"mode": mode, "address": "invalid listen address", "enable": true}},
			DeviceModels: []config.DeviceModel{{Model: config.Model{Name: "bad", DevicePoints: []config.Point{{"name": "bad", "offset": nil}}}, Devices: []config.Device{{ID: "A", ConnectionKey: "bad"}}}}})
		if len(p.connPool) != 0 {
			t.Fatal("invalid offset connection initialized")
		}
	}
}

// Exercises the public write APIs, both protocol paths and final ExportTo/Shadow
// values without requiring TCP listener permission. Shared state is isolated by
// exporttest.Run's child process.
func TestModbusOffsetIntegration(t *testing.T) {
	p := &Plugin{}
	exporttest.Run(t, ProtocolName, p, func(r *exporttest.Recorder) {
		for i, name := range []string{"U", "I", "P"} {
			point, _ := driverbox.CoreCache().GetPointByDevice("A", name)
			point["rawType"], point["primaryTable"], point["startAddress"], point["readWrite"] = "Uint16", "HOLDING_REGISTER", []string{"100d", "101d", "102d"}[i], "W"
			if name == "U" {
				point["scale"], point["offset"] = .1, -40
			} else {
				point["valueType"] = "int"
			}
			if name == "I" {
				point["offset"] = -10000
			}
		}
		for _, id := range []string{"A", "B"} {
			dev, _ := driverbox.CoreCache().GetDevice(id)
			dev.ConnectionKey, dev.Properties = id, map[string]string{"unitID": "1"}
			if err := driverbox.CoreCache().AddOrUpdateDevice(dev); err != nil {
				t.Fatal(err)
			}
		}
		script := `local json = require("json")
local registers = {[100]=650, [101]=10025, [102]=200}
local writes = 0
function mockRead(unit, kind, address, quantity)
  local out = {}
  for i=0,quantity-1 do out[#out+1] = registers[address+i] or 0 end
  return json.encode(out)
end
function mockWrite(unit, kind, address, values)
  writes = writes + 1
  for i,value in ipairs(values) do registers[address+i-1] = value end
  return "ok"
end
function mockCount() return tostring(writes) end`
		path := filepath.Join(config.ResourcePath, "mock.lua")
		if err := os.WriteFile(path, []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		var err error
		ls, err = luautil.InitLuaVM(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { ls.Close(); ls = nil }()
		forward := &connector{virtual: true, config: &ConnectionConfig{Retry: 1, BatchWriteLen: 2}}
		server, client := net.Pipe()
		session := &reverseSession{conn: server, timeout: time.Second}
		reverse := &reverseConnector{config: &ConnectionConfig{BatchWriteLen: 2}, deviceSessions: map[string]*reverseSession{"B": session}}
		defer reverse.Close()
		slave := &stringSlave{}
		slave.set(100, []uint16{650, 10025, 200})
		done := make(chan struct{})
		go func() { defer close(done); serveStringSlave(t, client, false, slave) }()
		defer func() { _ = client.Close(); <-done }()
		p.connPool = map[string]closeableConnector{"A": forward, "B": reverse}
		read := func(id string) {
			t.Helper()
			group := &pointGroup{UnitID: 1, Address: 100, Quantity: 3, RegisterType: HoldingRegister}
			for _, name := range []string{"U", "I", "P"} {
				point, _ := driverbox.CoreCache().GetPointByDevice(id, name)
				ext, err := convToPointExtend(point)
				if err != nil {
					t.Fatal(err)
				}
				ext.DeviceId = id
				group.Points = append(group.Points, ext)
			}
			var err error
			if id == "A" {
				err = forward.sendReadCommand(group)
			} else {
				err = reverse.sendReadCommand(session, id, command{Mode: plugin.ReadMode, Value: group})
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{"A", "B"} {
			read(id)
		}
		values := []plugin.PointData{{PointName: "U", Value: float64(25)}, {PointName: "I", Value: int64(25)}, {PointName: "P", Value: int64(200)}}
		r.Assert(t, 2, exporttest.Data("A", values...), exporttest.Data("B", values...))
		slave.take()
		for _, id := range []string{"A", "B"} {
			if err := driverbox.WritePoint(id, plugin.PointData{PointName: "U", Value: 25}); err != nil {
				t.Fatal(err)
			}
			if err := driverbox.WritePoints(id, []plugin.PointData{{PointName: "U", Value: 0}, {PointName: "I", Value: 30}}); err != nil {
				t.Fatal(err)
			}
			read(id)
			if got, err := driverbox.Shadow().GetDevicePoint(id, "U"); err != nil || got != float64(0) {
				t.Fatalf("%s U=%#v,%v", id, got, err)
			}
			if got, err := driverbox.Shadow().GetDevicePoint(id, "I"); err != nil || got != int64(30) {
				t.Fatalf("%s I=%#v,%v", id, got, err)
			}
		}
		requests := slave.take()
		if len(requests) != 3 || requests[0].function != 6 || requests[0].values[0] != 650 || requests[1].function != 16 || len(requests[1].values) != 2 || requests[1].values[0] != 400 || requests[1].values[1] != 10030 {
			t.Fatalf("reverse raw write frames: %+v", requests)
		}
		count, err := luautil.CallLuaMethod(ls, "mockCount")
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"A", "B"} {
			for _, value := range []float64{25.05, -41, 6513.6} {
				if err := driverbox.WritePoint(id, plugin.PointData{PointName: "U", Value: value}); err == nil {
					t.Fatalf("accepted invalid %s write %v", id, value)
				}
			}
			if err := driverbox.WritePoints(id, []plugin.PointData{{PointName: "U", Value: 25}, {PointName: "I", Value: 60000}}); err == nil {
				t.Fatal("accepted partially valid batch")
			}
		}
		if got, _ := luautil.CallLuaMethod(ls, "mockCount"); got != count {
			t.Fatal("invalid forward write reached the device")
		}
		if requests := slave.take(); len(requests) != 0 {
			t.Fatalf("invalid write sent frames: %+v", requests)
		}
		// Direct protocol callers still receive range/integrality validation,
		// without applying offset a second time.
		for _, conn := range []closeableConnector{forward, reverse} {
			if _, err := conn.Encode("A", plugin.WriteMode, plugin.PointData{PointName: "U", Value: 650.5}); err == nil {
				t.Fatal("protocol rounded a fractional offset write")
			}
		}
	})
}
