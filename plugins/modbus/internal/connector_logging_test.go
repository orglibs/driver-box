package internal

import (
	"fmt"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/simonvetter/modbus"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func logTestConnector(t *testing.T, mode string) (*connector, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.InfoLevel)
	previous := logger.Logger
	logger.Logger = zap.New(core)
	t.Cleanup(func() { logger.Logger = previous })
	client, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL: "tcp://[invalid", Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &connector{
		config: &ConnectionConfig{
			BaseConnection: plugin.BaseConnection{ConnectionKey: "pcs-link"},
			Mode:           mode, Address: "192.0.2.10:502",
			ServerTLS: "PRIVATE-CERTIFICATE", ClientTLS: "PRIVATE-CA",
		},
		client: client, keepAlive: true,
	}, logs
}

func assertConnectionLog(t *testing.T, logs *observer.ObservedLogs, message, mode string, extra map[string]string) {
	t.Helper()
	entries := logs.FilterMessage(message).All()
	if len(entries) != 1 {
		t.Fatalf("expected one %q log, got %v", message, logs.All())
	}
	fields := entries[0].ContextMap()
	want := map[string]string{
		"connectionKey": "pcs-link", "mode": mode, "connectionAddress": "192.0.2.10:502",
	}
	for key, value := range extra {
		want[key] = value
	}
	for key, value := range want {
		if fmt.Sprint(fields[key]) != value {
			t.Errorf("field %s = %v, want %s", key, fields[key], value)
		}
	}
	if len(fields) != len(want) {
		t.Errorf("unexpected fields: %v", fields)
	}
	for _, secret := range []string{"PRIVATE-CERTIFICATE", "PRIVATE-CA"} {
		if strings.Contains(fmt.Sprint(fields), secret) {
			t.Errorf("log exposed connection credentials: %s", secret)
		}
	}
}

func TestConnectorErrorLogConnectionContext(t *testing.T) {
	for _, mode := range []string{"tcp", "rtu", "rtuovertcp"} {
		t.Run(mode, func(t *testing.T) {
			c, logs := logTestConnector(t, mode)
			c.mutex.Lock()
			c.closeModbusClient(modbus.ErrProtocolError)
			assertConnectionLog(t, logs, "modbus client error, will close it", mode,
				map[string]string{"error": "protocol error", "connectionAction": "close"})
			if c.keepAlive || !c.mutex.TryLock() {
				t.Fatal("error must still close connection and unlock connector")
			}
			c.mutex.Unlock()
		})
	}
}

func TestConnectorReadErrorLogRequestContext(t *testing.T) {
	c, logs := logTestConnector(t, "tcp")
	// A zero quantity fails inside the real client before network I/O.
	_, err := c.read(7, "holding_register", 123, 0)
	if err != modbus.ErrUnexpectedParameters {
		t.Fatalf("read error identity changed: %v", err)
	}
	assertConnectionLog(t, logs, "modbus request failed, connection kept", "tcp", map[string]string{
		"error": err.Error(), "connectionAction": "keep", "operation": "read", "unitID": "7",
		"registerType": "HOLDING_REGISTER", "address": "123", "quantity": "0",
	})
}

func TestConnectorOpenErrorLogConnectionContext(t *testing.T) {
	c, logs := logTestConnector(t, "tcp")
	c.keepAlive = false
	// Malformed client address fails URL parsing without opening a socket.
	err := c.openModbusClient()
	if err == nil {
		t.Fatal("expected address parsing error")
	}
	assertConnectionLog(t, logs, "open modbus client error", "tcp", map[string]string{"error": err.Error()})
	if !c.mutex.TryLock() {
		t.Fatal("failed open must still unlock connector")
	}
	c.mutex.Unlock()
}

func TestConnectorSuccessfulCloseRemainsQuiet(t *testing.T) {
	for _, mode := range []string{"tcp", "rtu"} {
		t.Run(mode, func(t *testing.T) {
			c, logs := logTestConnector(t, mode)
			c.mutex.Lock()
			c.closeModbusClient(nil)
			if logs.Len() != 0 || !c.keepAlive {
				t.Fatalf("successful close behavior changed: logs=%v keepAlive=%v", logs.All(), c.keepAlive)
			}
			if !c.mutex.TryLock() {
				t.Fatal("successful close must still unlock connector")
			}
			c.mutex.Unlock()
		})
	}
}
