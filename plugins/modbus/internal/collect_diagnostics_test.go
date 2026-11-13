package internal

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"github.com/orglibs/driver-box/v2/pkg/luautil"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestCollectDiagnosticsFailuresAndRecovery(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	log := zap.New(core)
	var d collectDiagnostics
	g := &pointGroup{UnitID: 2, Address: 12288, Quantity: 32, Duration: 10 * time.Second,
		RegisterType: HoldingRegister, Points: []*Point{{Point: config.Point{"description": "must not appear in warning"}}}}
	now := time.Unix(1000, 0)
	for i := 0; i < 21; i++ {
		d.readFailed(log, "bus", g, errors.New("timeout"), now.Add(time.Duration(i)*time.Millisecond))
	}
	if logs.Len() != 1 {
		t.Fatalf("21 identical failures produced %d logs", logs.Len())
	}
	d.flush(log, "bus", now.Add(29*time.Second))
	if logs.Len() != 1 {
		t.Fatal("summary emitted too early")
	}
	d.flush(log, "bus", now.Add(30*time.Second))
	if logs.Len() != 2 {
		t.Fatal("pending failures were not summarized")
	}
	fields := logs.All()[1].ContextMap()
	if fields["totalFailures"] != uint64(21) || fields["newFailures"] != uint64(20) || fields["address"] != uint16(12288) {
		t.Fatalf("incorrect summary: %#v", fields)
	}
	for _, entry := range logs.All() {
		for _, forbidden := range []string{"group", "connection", "Points"} {
			if _, exists := entry.ContextMap()[forbidden]; exists {
				t.Fatalf("verbose field %s", forbidden)
			}
		}
	}
	// A changed error and a different group must not be hidden by an earlier fault.
	d.readFailed(log, "bus", g, errors.New("illegal address"), now.Add(31*time.Second))
	other := &pointGroup{UnitID: 3, Quantity: 1}
	d.readFailed(log, "bus", other, errors.New("timeout"), now.Add(31*time.Second))
	if logs.Len() != 4 {
		t.Fatal("new fault was suppressed")
	}
	d.readFailed(log, "bus", g, errors.New("illegal address"), now.Add(32*time.Second))
	d.readSucceeded(log, "bus", g, now.Add(33*time.Second))
	recovered := logs.FilterMessage("modbus collect read recovered")
	if recovered.Len() != 1 || recovered.All()[0].ContextMap()["unreportedFailures"] != uint64(1) {
		t.Fatal("recovery omitted suppressed failures")
	}
	d.readSucceeded(log, "bus", g, now.Add(34*time.Second))
	if recovered.Len() != 1 || logs.FilterMessage("modbus collect read recovered").Len() != 1 {
		t.Fatal("duplicate recovery")
	}
	d.readSucceeded(log, "bus", other, now.Add(34*time.Second))
	if len(d.failures) != 0 {
		t.Fatal("recovered state retained")
	}
	d.readFailed(log, "bus", g, errors.New("timeout"), now.Add(35*time.Second))
	if logs.FilterMessage("modbus collect read failed").Len() != 5 {
		t.Fatal("new episode suppressed")
	}
	// An ongoing failure remains visible during retry backoff, without claiming new I/O.
	d.flush(log, "bus", now.Add(65*time.Second))
	last := logs.All()[logs.Len()-1].ContextMap()
	if last["newFailures"] != uint64(0) || last["totalFailures"] != uint64(1) {
		t.Fatalf("backoff summary: %#v", last)
	}
}

func TestCollectDiagnosticsWriteDeferral(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	log := zap.New(core)
	var d collectDiagnostics
	now := time.Unix(1000, 0)
	groups := make([]*pointGroup, 100)
	for i := range groups {
		groups[i] = &pointGroup{UnitID: 2, Address: uint16(i), Quantity: 1}
	}
	for scan := 0; scan < 100; scan++ {
		at := now.Add(time.Duration(scan) * 300 * time.Millisecond)
		for _, g := range groups {
			d.deferRead(g, at)
		}
		d.flush(log, "bus", at)
	}
	if logs.Len() != 0 {
		t.Fatal("transient write deferral should be silent")
	}
	d.flush(log, "bus", now.Add(30*time.Second))
	if logs.Len() != 1 {
		t.Fatal("persistent deferral should warn once per connection")
	}
	f := logs.All()[0].ContextMap()
	if f["deferredGroups"] != int64(100) || f["skippedGroupChecks"] != uint64(10000) {
		t.Fatalf("deferral counts: %#v", f)
	}
	// One successful group must not hide the starvation of the remaining groups.
	d.readStarted(log, "bus", groups[0], now.Add(31*time.Second))
	d.flush(log, "bus", now.Add(59*time.Second))
	if logs.Len() != 1 {
		t.Fatal("duplicate deferral summary")
	}
	d.flush(log, "bus", now.Add(60*time.Second))
	if logs.Len() != 2 {
		t.Fatal("missing persistent deferral summary")
	}
	for _, g := range groups[1:] {
		d.readStarted(log, "bus", g, now.Add(61*time.Second))
	}
	if logs.FilterMessage("modbus write deferral cleared").Len() != 1 {
		t.Fatal("missing deferral recovery")
	}
	if d.skipped != 0 || len(d.deferred) != 0 || d.busyWarned {
		t.Fatal("deferral state retained")
	}
	d.deferRead(groups[0], now.Add(62*time.Second))
	d.readStarted(log, "bus", groups[0], now.Add(63*time.Second))
	if logs.Len() != 3 {
		t.Fatal("short episode should not add a recovery log")
	}
}

func TestCollectDiagnosticsConcurrentAndIndependent(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	log := zap.New(core)
	var first, second collectDiagnostics
	now := time.Unix(1000, 0)
	g := &pointGroup{UnitID: 2, Quantity: 1}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				first.deferRead(g, now)
				first.readFailed(log, "first", g, errors.New("timeout"), now)
				first.flush(log, "first", now)
			}
		}()
	}
	wg.Wait()
	second.readFailed(log, "second", g, errors.New("timeout"), now)
	if logs.Len() != 2 {
		t.Fatal("independent connection fault suppressed")
	}
	first.flush(log, "first", now.Add(30*time.Second))
	if first.failures[g].total != 400 || first.skipped != 400 {
		t.Fatal("concurrent counts lost")
	}
	first.readStarted(log, "first", g, now.Add(31*time.Second))
	first.readSucceeded(log, "first", g, now.Add(31*time.Second))
}

// Exercise the real scheduler and sendReadCommand. Isolate global logger/Lua
// state from the framework's singleton lifecycle goroutines in other tests.
func TestCollectDiagnosticsIntegration(t *testing.T) {
	if os.Getenv("DRIVERBOX_COLLECT_LOG_TEST_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCollectDiagnosticsIntegration$", "-test.timeout=20s")
		cmd.Env = append(os.Environ(), "DRIVERBOX_COLLECT_LOG_TEST_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("integration: %v\n%s", err, out)
		}
		return
	}
	core, logs := observer.New(zap.InfoLevel)
	logger.Logger = zap.New(core)
	conf := &ConnectionConfig{MinInterval: 100}
	conf.ConnectionKey = "bus"
	c := &connector{config: conf, virtual: true, devices: map[uint8]*slaveDevice{}}
	device := &slaveDevice{unitID: 2}
	c.devices[2] = device
	for i := 0; i < 100; i++ {
		device.pointGroup = append(device.pointGroup, &pointGroup{UnitID: 2, Address: uint16(i), Quantity: 1, Duration: time.Second})
	}
	c.writeSemaphore.Store(1)
	for i := 0; i < 10; i++ {
		c.collectOnce(conf)
	}
	if logs.Len() != 0 || c.collectLog.skipped != 1000 {
		t.Fatalf("scheduler write burst: logs=%d skipped=%d", logs.Len(), c.collectLog.skipped)
	}
	// This lower-level branch also keeps its historical nil return on write yield.
	g := device.pointGroup[0]
	if err := c.sendReadCommand(g); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatal("lower-level write yield is noisy")
	}
	c.writeSemaphore.Store(0)
	g.Duration, g.TimeOutCount, g.LatestTime = 10*time.Second, 1, time.Now()
	device.pointGroup = []*pointGroup{g}
	for i := 0; i < 10; i++ {
		c.collectOnce(conf)
	}
	if logs.Len() != 0 || g.TimeOutCount != 1 {
		t.Fatal("backoff scan changed state or emitted a warning")
	}
	// A genuine failed read still propagates the original error to its caller.
	ls = nil
	if err := c.sendReadCommand(g); err == nil || err.Error() != "lua vm is nil" {
		t.Fatalf("read error propagation: %v", err)
	}
	for i := 0; i < 5; i++ {
		g.LatestTime = time.Time{}
		c.collectOnce(conf)
	}
	if logs.FilterMessage("modbus collect read failed").Len() != 1 {
		t.Fatal("repeated real failures were not condensed")
	}
	// An avoided read must never be reported as a successful read/recovery.
	c.writeSemaphore.Store(1)
	if err := c.sendReadCommand(g); err != nil {
		t.Fatal(err)
	}
	if logs.FilterMessage("modbus collect read recovered").Len() != 0 {
		t.Fatal("write yield reported false recovery")
	}
	c.writeSemaphore.Store(0)
	converter := filepath.Join(t.TempDir(), "converter.lua")
	if err := os.WriteFile(converter, []byte(`function mockRead(...) return "[0]" end`), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	ls, err = luautil.InitLuaVM(converter)
	if err != nil {
		t.Fatal(err)
	}
	defer luautil.Close(ls)
	g.LatestTime = time.Time{}
	c.collectOnce(conf)
	if logs.FilterMessage("modbus collect read recovered").Len() != 1 || g.TimeOutCount != 0 || g.LatestTime.IsZero() {
		t.Fatal("successful native read did not restore collection")
	}
}
