package internal

import (
	"net"
	"testing"
	"time"

	"github.com/orglibs/driver-box/v2/pkg/config"
)

func TestStringIdentityNormalization(t *testing.T) {
	for _, tt := range []struct {
		first, second string
		valid         bool
	}{
		{"PILE-1", " PILE-1\x00", false},
		{"PILE-1", "PILE-2", true},
		{"PILE-1", "pile-1", true},
		{" \x00 ", "PILE-2", false},
		{"\xff", "PILE-2", false},
		{"\tPILE-1\n", "PILE-1", true},
	} {
		devices := map[string]config.Device{"A": {ID: "A", Properties: map[string]string{"identifyValue": tt.first}}, "B": {ID: "B", Properties: map[string]string{"identifyValue": tt.second}}}
		rc := &reverseConnector{config: &ConnectionConfig{Identify: &IdentifyConfig{Mode: identifyModeModbusRegister, Format: "ascii"}}, devicesMeta: devices}
		err := rc.validateDeviceBinding()
		if (err == nil) != tt.valid {
			t.Fatalf("identities %q/%q: %v", tt.first, tt.second, err)
		}
		if devices["A"].Properties["identifyValue"] != tt.first || devices["B"].Properties["identifyValue"] != tt.second {
			t.Fatal("identity properties mutated")
		}
	}
}

func TestStringIdentityProbe(t *testing.T) {
	for _, tt := range []struct {
		name, format, configured string
		registers                []uint16
		valid                    bool
	}{
		{"padded", "ascii", " PILE-1\x00", []uint16{0x2050, 0x494c, 0x452d, 0x3100}, true},
		{"UTF8", "ascii", "中文", []uint16{0xe4b8, 0xade6, 0x9687}, true},
		{"empty", "ascii", "PILE-1", []uint16{0, 0x2020}, false},
		{"invalid UTF8", "ascii", "PILE-1", []uint16{0xffff}, false},
		{"case sensitive", "ascii", "pile-1", []uint16{0x5049, 0x4c45, 0x2d31}, false},
		{"hex unchanged", "hex", "ffff0000", []uint16{0xffff, 0}, true},
		{"hex case unchanged", "hex", "FFFF0000", []uint16{0xffff, 0}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer server.Close()
			slave := &stringSlave{}
			slave.set(27, tt.registers)
			done := make(chan struct{})
			go func() { defer close(done); serveStringSlave(t, client, false, slave) }()
			defer func() { _ = client.Close(); <-done }()
			rc := &reverseConnector{config: &ConnectionConfig{BatchReadLen: 1, Identify: &IdentifyConfig{Mode: identifyModeModbusRegister, Format: tt.format, UnitID: 7, StartAddress: 27, Quantity: uint16(len(tt.registers))}}, devicesMeta: map[string]config.Device{"A": {ID: "A", Properties: map[string]string{"identifyValue": tt.configured}}}}
			id, err := rc.identifyDevice(&reverseSession{conn: server, timeout: time.Second})
			if (err == nil) != tt.valid || (tt.valid && id != "A") {
				t.Fatalf("identify=%q,%v", id, err)
			}
			requests := slave.take()
			if len(requests) != 1 || requests[0].unit != 7 || requests[0].quantity != uint16(len(tt.registers)) {
				t.Fatalf("probe %+v", requests)
			}
		})
	}
}

func TestStringIdentityFailureClosesPendingConnection(t *testing.T) {
	for _, registers := range [][]uint16{{0xffff}, {0}} {
		server, client := net.Pipe()
		rc := &reverseConnector{config: &ConnectionConfig{Timeout: 500, Identify: &IdentifyConfig{Mode: identifyModeModbusRegister, Format: "ascii", UnitID: 1, StartAddress: 27, Quantity: 1}}, pendingConns: map[net.Conn]struct{}{server: {}}, devicesMeta: map[string]config.Device{"A": {ID: "A", Properties: map[string]string{"identifyValue": "PILE-1"}}}}
		slave := &stringSlave{}
		slave.set(27, registers)
		done := make(chan struct{})
		go func() { defer close(done); rc.handleIncomingConn(server) }()
		serveStringSlave(t, client, false, slave)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("invalid probe connection was not closed")
		}
		if len(rc.pendingConns) != 0 || len(rc.deviceSessions) != 0 {
			t.Fatal("invalid identity bound or retained")
		}
	}
}
