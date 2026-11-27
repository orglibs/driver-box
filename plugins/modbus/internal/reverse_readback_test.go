package internal

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/orglibs/driver-box/v2/internal/testutil/exporttest"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestReverseReadbackEncoding(t *testing.T) {
	point := func(name string, address uint16) *Point {
		return &Point{Point: config.Point{"name": name}, DeviceId: "A", Address: address,
			Quantity: 1, RegisterType: HoldingRegister, RawType: ValueTypeUint16}
	}
	first := &pointGroup{UnitID: 7, Address: 100, Quantity: 2, RegisterType: HoldingRegister,
		LatestTime: time.Now(), TimeOutCount: 3, Points: []*Point{point("U", 100), point("I", 101)}}
	second := &pointGroup{UnitID: 7, Address: 200, Quantity: 1, RegisterType: InputRegister, Points: []*Point{point("P", 200)}}
	rc := &reverseConnector{devicePointGroups: map[string][]*pointGroup{"A": {first, second}}}
	encoded, err := rc.Encode("A", plugin.ReadMode, plugin.PointData{PointName: "U"}, plugin.PointData{PointName: "U"}, plugin.PointData{PointName: "P"})
	if err != nil {
		t.Fatal(err)
	}
	request := encoded.(command).Value.(reverseReadRequest)
	if request.deviceID != "A" || len(request.groups) != 2 {
		t.Fatalf("unexpected request: %+v", request)
	}
	got := request.groups[0]
	if got == first || got.UnitID != 7 || got.Address != 100 || got.Quantity != 2 || len(got.Points) != 1 || got.Points[0].Name() != "U" {
		t.Fatalf("read selection/window changed: %+v", got)
	}
	if !got.LatestTime.IsZero() || got.TimeOutCount != 0 || request.groups[1].RegisterType != InputRegister {
		t.Fatal("readback copied scheduling state or lost register type")
	}
	got.Points[0] = nil
	if len(first.Points) != 2 || first.Points[0] == nil || first.TimeOutCount != 3 {
		t.Fatal("readback mutated periodic groups")
	}
	for _, tt := range []struct{ device, point string }{{"missing", "U"}, {"A", "missing"}} {
		if _, err := rc.Encode(tt.device, plugin.ReadMode, plugin.PointData{PointName: "U"}, plugin.PointData{PointName: tt.point}); err == nil {
			t.Fatalf("accepted unreadable point: %+v", tt)
		}
	}
	if _, err := rc.Encode("A", plugin.EncodeMode("invalid")); err == nil {
		t.Fatal("accepted invalid mode")
	}
	if err := rc.Send(command{Mode: plugin.ReadMode, Value: "invalid"}); err == nil {
		t.Fatal("accepted invalid read payload")
	}
	empty, err := rc.Encode("A", plugin.ReadMode)
	if err != nil || rc.Send(empty) != nil {
		t.Fatalf("empty read is not a no-op: %v", err)
	}
	if err := rc.Send(encoded); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("missing session: %v", err)
	}
}

func TestReverseReadbackIOError(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	_ = client.Close()
	rc := &reverseConnector{config: &ConnectionConfig{}, deviceSessions: map[string]*reverseSession{
		"A": {conn: server, timeout: time.Second},
	}}
	group := &pointGroup{UnitID: 1, RegisterType: HoldingRegister, Quantity: 1}
	err := rc.Send(command{Mode: plugin.ReadMode, Value: reverseReadRequest{deviceID: "A", groups: []*pointGroup{group}}})
	if err == nil || !strings.Contains(err.Error(), "modbus write error") {
		t.Fatalf("transport failure was hidden: %v", err)
	}
}

func TestReverseReadbackDoesNotReadCollectorState(t *testing.T) {
	// The collector updates these fields without rc.mutex. Encoding must only
	// copy immutable protocol metadata; -race catches accidental struct copies.
	group := &pointGroup{UnitID: 1, RegisterType: HoldingRegister, Quantity: 1,
		Points: []*Point{{Point: config.Point{"name": "U"}}}}
	rc := &reverseConnector{devicePointGroups: map[string][]*pointGroup{"A": {group}}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			group.LatestTime, group.TimeOutCount = time.Now(), i
		}
	}()
	for i := 0; i < 100; i++ {
		if _, err := rc.Encode("A", plugin.ReadMode, plugin.PointData{PointName: "U"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

// Exercise public single/batch writes -> automatic readback -> real Modbus
// frames -> Export -> Shadow. net.Pipe replaces only the device transport.
func TestReverseWriteReadbackIntegration(t *testing.T) {
	p := &Plugin{}
	exporttest.Run(t, ProtocolName, p, func(_ *exporttest.Recorder) {
		core, logs := observer.New(zap.DebugLevel)
		logger.Logger = zap.New(core)
		var points []config.Point
		for i, name := range []string{"U", "I", "P"} {
			point, _ := driverbox.CoreCache().GetPointByDevice("A", name)
			point["rawType"], point["primaryTable"], point["startAddress"], point["readWrite"] = "Uint16", "HOLDING_REGISTER", []string{"100d", "101d", "102d"}[i], "RW"
			if name == "U" {
				point["scale"], point["offset"] = .1, -40
			}
			if name == "P" {
				point["readWrite"] = "W"
			}
			points = append(points, point)
		}
		dev, _ := driverbox.CoreCache().GetDevice("A")
		dev.ConnectionKey, dev.Properties = "reverse", map[string]string{"unitID": "7"}
		if err := driverbox.CoreCache().AddOrUpdateDevice(dev); err != nil {
			t.Fatal(err)
		}
		server, client := net.Pipe()
		rc := &reverseConnector{config: &ConnectionConfig{MinInterval: 10, BatchWriteLen: 2},
			deviceSessions: map[string]*reverseSession{"A": {conn: server, timeout: time.Second}},
			devicesMeta:    map[string]config.Device{}, devicePointGroups: map[string][]*pointGroup{}}
		rc.addDevice(config.DeviceModel{Model: config.Model{DevicePoints: points}}, dev)
		p.connPool = map[string]closeableConnector{"reverse": rc}
		defer rc.Close()
		slave := &stringSlave{}
		slave.set(100, []uint16{0, 0, 0})
		done := make(chan struct{})
		go func() { defer close(done); serveStringSlave(t, client, false, slave) }()
		defer func() { _ = client.Close(); <-done }()
		for _, name := range []string{"U", "I"} {
			if err := driverbox.Shadow().SetDevicePoint("A", name, -99.0); err != nil {
				t.Fatal(err)
			}
		}
		waitConfirmed := func(count int) {
			t.Helper()
			deadline := time.Now().Add(3 * time.Second)
			for logs.FilterMessage("point write success, read new value").Len() < count {
				if logs.FilterMessage("point write success, read new value error").Len() != 0 || time.Now().After(deadline) {
					t.Fatalf("readback did not confirm: %+v", logs.All())
				}
				time.Sleep(time.Millisecond)
			}
		}
		if _, err := rc.Encode("A", plugin.ReadMode, plugin.PointData{PointName: "P"}); err == nil {
			t.Fatal("accepted write-only point for readback")
		}
		if err := driverbox.WritePoint("A", plugin.PointData{PointName: "U", Value: 25.0}); err != nil {
			t.Fatal(err)
		}
		waitConfirmed(1)
		if got, err := driverbox.Shadow().GetDevicePoint("A", "I"); err != nil || got != -99.0 {
			t.Fatalf("single readback exported an unrequested point: %v, %v", got, err)
		}
		values := []plugin.PointData{{PointName: "U", Value: 30.0}, {PointName: "I", Value: 45.0}}
		if err := driverbox.WritePoints("A", values); err != nil {
			t.Fatal(err)
		}
		waitConfirmed(3)
		for _, value := range values {
			if got, err := driverbox.Shadow().GetDevicePoint("A", value.PointName); err != nil || got != value.Value {
				t.Fatalf("Shadow %s=%v, %v; want %v", value.PointName, got, err, value.Value)
			}
		}
		requests := slave.take()
		if len(requests) != 4 {
			t.Fatalf("want one write/read per operation: %+v", requests)
		}
		for i, function := range []byte{6, 3, 16, 3} {
			if requests[i].function != function || requests[i].unit != 7 {
				t.Fatalf("unexpected Modbus request: %+v", requests[i])
			}
		}
		if requests[0].values[0] != 650 || requests[2].values[0] != 700 {
			t.Fatalf("engineering scale/offset lost: %+v", requests)
		}
		if logs.FilterMessage("point write success, read new value error").Len() != 0 {
			t.Fatal("readback logged an error")
		}
	})
}
