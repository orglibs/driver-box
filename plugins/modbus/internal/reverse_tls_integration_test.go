package internal

import (
	"crypto/tls"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/pkg/config"
)

// Exporter, cache and Shadow state must stay isolated from other test cases.
func TestReverseTLSIntegration(t *testing.T) {
	if os.Getenv("DRIVERBOX_TLS_TEST_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestReverseTLSIntegration$", "-test.timeout=45s")
		cmd.Env = append(os.Environ(), "DRIVERBOX_TLS_TEST_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("reverse TLS integration: %v\n%s", err, output)
		}
		return
	}
	recorder, points := initStringFramework(t)
	f := newReverseTLSFixture(t)
	for _, mutual := range []bool{false, true} {
		name := "one_way"
		if mutual {
			name = "mutual"
		}
		t.Run(name, func(t *testing.T) {
			conf := &ConnectionConfig{Mode: "reverse_tcp", Address: "127.0.0.1:0", ServerTLS: f.serverPEM,
				Timeout: 1000, MinInterval: 1, BatchReadLen: 4,
				Identify: &IdentifyConfig{Mode: identifyModeModbusRegister, UnitID: 1, StartAddress: 27, Quantity: 16, Format: "ascii", Timeout: 1000}}
			if mutual {
				conf.ClientTLS = f.clientCA
			}
			rc, err := newReverseConnector(&Plugin{}, conf)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			dev := config.Device{ID: "string-A", Properties: map[string]string{"unitID": "1", "identifyValue": "PILE-A"}}
			rc.addDevice(config.DeviceModel{Model: config.Model{DevicePoints: points[:4]}}, dev)
			if err := rc.validateDeviceBinding(); err != nil {
				t.Fatal(err)
			}
			rc.Start()
			clientCfg := &tls.Config{RootCAs: f.roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}
			if mutual {
				clientCfg.Certificates = []tls.Certificate{f.client}
			}
			connect := func(text string, previous *reverseSession) *reverseSession {
				t.Helper()
				client, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", rc.listener.Addr().String(), clientCfg)
				if err != nil {
					t.Fatal(err)
				}
				slave := &stringSlave{}
				slave.set(27, []uint16{0x5049, 0x4c45, 0x2d41})
				regs, err := encodeModbusString(text, 3, false, false)
				if err != nil {
					t.Fatal(err)
				}
				slave.set(100, regs)
				done := make(chan struct{})
				go func() { defer close(done); serveStringSlave(t, client, false, slave) }()
				t.Cleanup(func() { _ = client.Close(); <-done })
				deadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					if session, ok := rc.getSession(dev.ID); ok && session != previous {
						return session
					}
					time.Sleep(time.Millisecond)
				}
				t.Fatal("TLS device did not bind")
				return nil
			}
			session := connect("ABCDEF", nil)
			_ = driverbox.Shadow().DeleteDevice(dev.ID)
			driverbox.Shadow().AddDevice(dev.ID, "strings")
			recorder.take()
			for _, group := range rc.devicePointGroups[dev.ID] {
				if err := rc.sendReadCommand(session, dev.ID, command{Mode: plugin.ReadMode, Value: group}); err != nil {
					t.Fatal(err)
				}
			}
			for point, want := range map[string]interface{}{"text": "ABCDEF", "alias": "ABCD", "number": int64(0x43444142), "bit": int64(2)} {
				got, err := driverbox.Shadow().GetDevicePoint(dev.ID, point)
				if err != nil || got != want {
					t.Fatalf("Shadow %s=%#v,%v want %#v", point, got, err, want)
				}
			}
			calls, batches := recorder.take()
			if calls != 1 || len(batches) != 1 || len(batches[0].Values) != 4 {
				t.Fatalf("TLS ExportTo: %d %+v", calls, batches)
			}
			// Reconnect through the real TLS listener and replace only after identification.
			replacement := connect("NEW", session)
			if !session.closed.Load() {
				t.Fatal("TLS reconnect did not close old session")
			}
			for _, group := range rc.devicePointGroups[dev.ID] {
				if err := rc.sendReadCommand(replacement, dev.ID, command{Mode: plugin.ReadMode, Value: group}); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := driverbox.Shadow().GetDevicePoint(dev.ID, "text"); err != nil || got != "NEW" {
				t.Fatalf("reconnected TLS Shadow=%v,%v", got, err)
			}
		})
	}
}
