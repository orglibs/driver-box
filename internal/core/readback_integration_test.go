package core_test

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/orglibs/driver-box/v2/internal/testutil/exporttest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type readbackTestPlugin struct {
	plugin.Plugin
	conn *readbackTestConnector
}

func (p *readbackTestPlugin) Connector(string) (plugin.Connector, error) { return p.conn, nil }

type readbackTestConnector struct {
	mu            sync.Mutex
	reads, writes int
}

func (*readbackTestConnector) Encode(_ string, mode plugin.EncodeMode, _ ...plugin.PointData) (interface{}, error) {
	return mode, nil
}
func (c *readbackTestConnector) Send(raw interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if raw.(plugin.EncodeMode) == plugin.WriteMode {
		c.writes++
		return nil
	}
	c.reads++
	return driverbox.Shadow().SetDevicePoint("A", "U", math.Copysign(0, -1))
}
func (*readbackTestConnector) Release() error { return nil }

// Run the real public write -> tryReadNewValues -> SendBatchRead -> Shadow
// confirmation path. Only external I/O is simulated, with singleton isolation.
func TestWriteReadbackNegativeZeroStopsRetrying(t *testing.T) {
	c := &readbackTestConnector{}
	p := &readbackTestPlugin{conn: c}
	exporttest.Run(t, "readback-zero", p, func(_ *exporttest.Recorder) {
		core, logs := observer.New(zap.DebugLevel)
		logger.Logger = zap.New(core)
		point, _ := driverbox.CoreCache().GetPointByDevice("A", "U")
		point["readWrite"] = "RW"
		if err := driverbox.Shadow().SetDevicePoint("A", "U", 1.0); err != nil {
			t.Fatal(err)
		}
		if err := driverbox.WritePoint("A", plugin.PointData{PointName: "U", Value: 0}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for logs.FilterMessage("point write success, read new value").Len() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("numeric zero was never confirmed")
			}
			time.Sleep(time.Millisecond)
		}
		// A confirmed read terminates the worker, rather than issuing another
		// request at the next retry delay. This fails with the old string check.
		time.Sleep(350 * time.Millisecond)
		c.mu.Lock()
		reads, writes := c.reads, c.writes
		c.mu.Unlock()
		if reads != 1 || writes != 1 {
			t.Fatalf("reads=%d writes=%d; want one write and one read", reads, writes)
		}
		if logs.FilterMessage("point write success, read new value").Len() != 1 {
			t.Fatal("unconfirmed/repeated success log")
		}
		if logs.FilterMessage("point write readback not confirmed").Len() != 1 {
			t.Fatal("initial mismatch did not take the retry path")
		}
	})
}
