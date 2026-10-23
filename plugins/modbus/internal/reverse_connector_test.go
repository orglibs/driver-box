package internal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"go.uber.org/zap"
)

func init() {
	logger.Logger = zap.NewExample()
}

// mockPileClient 模拟反向连入的充电桩客户端
type mockPileClient struct {
	conn net.Conn
	esn  string
}

type remoteAddrConn struct {
	net.Conn
	remoteAddr net.Addr
}

func (c remoteAddrConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (m *mockPileClient) run(t *testing.T, quit chan struct{}) {
	for {
		_ = m.conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
		header := make([]byte, 7)
		if _, err := io.ReadFull(m.conn, header); err != nil {
			select {
			case <-quit:
				return
			default:
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				return
			}
		}

		txnId := binary.BigEndian.Uint16(header[0:2])
		length := binary.BigEndian.Uint16(header[4:6])
		unitId := header[6]

		pdu := make([]byte, length-1)
		if _, err := io.ReadFull(m.conn, pdu); err != nil {
			return
		}

		funcCode := pdu[0]
		switch funcCode {
		case 0x03: // 读保持寄存器
			startAddr := binary.BigEndian.Uint16(pdu[1:3])
			qty := binary.BigEndian.Uint16(pdu[3:5])

			var data []byte
			if startAddr == 27 && qty == 16 {
				// 响应 ESN 探针
				data = make([]byte, 32)
				copy(data, []byte(m.esn))
			} else if startAddr == 0x1000 {
				// 响应 0x1000 功率点位 (比如 2794 = 27.94 kW)
				data = make([]byte, int(qty)*2)
				binary.BigEndian.PutUint16(data[0:2], 2794)
			} else {
				data = make([]byte, int(qty)*2)
			}

			respPdu := make([]byte, 2+len(data))
			respPdu[0] = 0x03
			respPdu[1] = byte(len(data))
			copy(respPdu[2:], data)

			respFrame := make([]byte, 7+len(respPdu))
			binary.BigEndian.PutUint16(respFrame[0:2], txnId)
			binary.BigEndian.PutUint16(respFrame[2:4], 0)
			binary.BigEndian.PutUint16(respFrame[4:6], uint16(1+len(respPdu)))
			respFrame[6] = unitId
			copy(respFrame[7:], respPdu)
			_, _ = m.conn.Write(respFrame)

		case 0x06: // 写单个保持寄存器
			// 回显请求
			respPdu := make([]byte, len(pdu))
			copy(respPdu, pdu)
			respFrame := make([]byte, 7+len(respPdu))
			binary.BigEndian.PutUint16(respFrame[0:2], txnId)
			binary.BigEndian.PutUint16(respFrame[2:4], 0)
			binary.BigEndian.PutUint16(respFrame[4:6], uint16(1+len(respPdu)))
			respFrame[6] = unitId
			copy(respFrame[7:], respPdu)
			_, _ = m.conn.Write(respFrame)
		}
	}
}

func TestReverseConnector_HandshakeAndCollect(t *testing.T) {
	connConfig := &ConnectionConfig{
		Address: "127.0.0.1:0", // 由系统动态分配空闲端口
		Mode:    "reverse_tcp",
		Timeout: 1000,
		Identify: &IdentifyConfig{
			Mode:         identifyModeModbusRegister,
			UnitID:       1,
			StartAddress: 27,
			Quantity:     16,
			Format:       "ascii",
			Timeout:      2000,
		},
	}

	p := &Plugin{}
	rc, err := newReverseConnector(p, connConfig)
	if err != nil {
		t.Fatalf("failed to create reverse connector: %v", err)
	}
	defer rc.Close()

	addr := rc.listener.Addr().String()

	// 添加一个设备配置，定义 ESN 为 "HW_PILE_001"
	model := config.DeviceModel{
		Model: config.Model{
			Name: "hw_pile_model",
			DevicePoints: []config.Point{
				{
					"name":         "active_power",
					"readWrite":    "RW",
					"startAddress": "0x1000",
					"primaryTable": "HOLDING_REGISTER",
					"rawType":      "Uint16",
					"duration":     "100ms",
				},
			},
		},
	}
	dev := config.Device{
		ID: "charger_01",
		Properties: map[string]string{
			"identifyValue": "HW_PILE_001",
		},
	}
	rc.addDevice(model, dev)
	rc.Start()

	// 模拟充电桩客户端主动连入
	rawConn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("client failed to dial: %v", err)
	}
	defer rawConn.Close()

	pile := &mockPileClient{
		conn: rawConn,
		esn:  "HW_PILE_001",
	}
	quit := make(chan struct{})
	defer close(quit)
	go pile.run(t, quit)

	// 等待握手完成并绑定
	time.Sleep(150 * time.Millisecond)

	session, ok := rc.getSession("charger_01")
	if !ok {
		t.Fatalf("expected device charger_01 to be bound in reverse connector")
	}
	if session == nil || session.conn == nil {
		t.Fatalf("invalid session for charger_01")
	}

	// 测试主动读取点位
	groups := rc.devicePointGroups["charger_01"]
	if len(groups) == 0 {
		t.Fatalf("no point groups configured for charger_01")
	}
	cmd := command{
		Mode:  plugin.ReadMode,
		Value: groups[0],
	}
	if err := rc.sendReadCommand(session, "charger_01", cmd); err != nil {
		t.Fatalf("sendReadCommand failed: %v", err)
	}

	// 测试控制下发（写操作）
	writeCmd := command{
		Mode: plugin.WriteMode,
		Value: []*writeValue{
			{
				DeviceId:     "charger_01",
				unitID:       1,
				Address:      0x2000,
				Value:        []uint16{1},
				RegisterType: HoldingRegister,
			},
		},
	}
	if err := rc.Send(writeCmd); err != nil {
		t.Fatalf("Send write command failed: %v", err)
	}
}

func TestReverseConnector_KickOffStaleConnection(t *testing.T) {
	connConfig := &ConnectionConfig{
		Address: "127.0.0.1:0",
		Mode:    "reverse_tcp",
		Timeout: 1000,
		Identify: &IdentifyConfig{
			Mode:         identifyModeModbusRegister,
			UnitID:       1,
			StartAddress: 27,
			Quantity:     16,
			Format:       "ascii",
		},
	}

	rc, err := newReverseConnector(&Plugin{}, connConfig)
	if err != nil {
		t.Fatalf("failed to create reverse connector: %v", err)
	}
	defer rc.Close()

	addr := rc.listener.Addr().String()
	rc.addDevice(config.DeviceModel{}, config.Device{
		ID: "charger_02",
		Properties: map[string]string{
			"identifyValue": "ESN_RECONNECT_TEST",
		},
	})
	rc.Start()

	// 第一次连接
	c1, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()

	pile1 := &mockPileClient{conn: c1, esn: "ESN_RECONNECT_TEST"}
	quit1 := make(chan struct{})
	defer close(quit1)
	go pile1.run(t, quit1)

	// 等待第一次连接绑定完成
	var s1 *reverseSession
	var ok bool
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		s1, ok = rc.getSession("charger_02")
		if ok && s1 != nil && s1.conn.RemoteAddr().String() == c1.LocalAddr().String() {
			break
		}
	}
	if !ok || s1.conn.RemoteAddr().String() != c1.LocalAddr().String() {
		t.Fatalf("session 1 not bound")
	}

	// 第二次连接（模拟断线重连）
	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	pile2 := &mockPileClient{conn: c2, esn: "ESN_RECONNECT_TEST"}
	quit2 := make(chan struct{})
	defer close(quit2)
	go pile2.run(t, quit2)

	// 等待第二次连接踢除旧连接并绑定完成
	var s2 *reverseSession
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		s2, ok = rc.getSession("charger_02")
		if ok && s2 != nil && s2.conn.RemoteAddr().String() == c2.LocalAddr().String() {
			break
		}
	}
	if !ok || s2.conn.RemoteAddr().String() != c2.LocalAddr().String() {
		t.Fatalf("session 2 should have replaced session 1")
	}
	if !s1.closed.Load() {
		t.Fatalf("stale session 1 should be closed")
	}
}

func TestReverseConnector_IdentifyUsesConfiguredUnitID(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	const unitID = 7
	rc := &reverseConnector{
		config: &ConnectionConfig{Identify: &IdentifyConfig{
			Mode:         identifyModeModbusRegister,
			UnitID:       unitID,
			StartAddress: 27,
			Quantity:     4,
			Format:       "ascii",
		}},
		devicesMeta: map[string]config.Device{
			"charger_07": {ID: "charger_07", Properties: map[string]string{"identifyValue": "PILE-7"}},
		},
	}
	session := &reverseSession{conn: serverConn, timeout: time.Second}

	clientDone := make(chan error, 1)
	go func() {
		header := make([]byte, 7)
		if _, err := io.ReadFull(clientConn, header); err != nil {
			clientDone <- err
			return
		}
		if header[6] != unitID {
			clientDone <- fmt.Errorf("probe unit ID = %d, want %d", header[6], unitID)
			return
		}
		request := make([]byte, 5)
		if _, err := io.ReadFull(clientConn, request); err != nil {
			clientDone <- err
			return
		}
		data := make([]byte, 8)
		copy(data, []byte("PILE-7"))
		response := make([]byte, 7+2+len(data))
		copy(response[0:2], header[0:2])
		binary.BigEndian.PutUint16(response[4:6], uint16(1+2+len(data)))
		response[6] = unitID
		response[7] = 0x03
		response[8] = byte(len(data))
		copy(response[9:], data)
		_, err := clientConn.Write(response)
		clientDone <- err
	}()

	deviceID, err := rc.identifyDevice(session)
	if err != nil {
		t.Fatalf("identifyDevice failed: %v", err)
	}
	if deviceID != "charger_07" {
		t.Fatalf("identified device = %q, want charger_07", deviceID)
	}
	if err := <-clientDone; err != nil {
		t.Fatalf("mock client failed: %v", err)
	}
}

func TestReverseConnector_IdentifyBySourceIP(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	rc := &reverseConnector{
		config: &ConnectionConfig{Identify: &IdentifyConfig{
			Mode: identifyModeSourceIP,
		}},
		devicesMeta: map[string]config.Device{
			"charger_10": {ID: "charger_10", Properties: map[string]string{"identifyValue": "10.20.0.0/16"}},
			"charger_11": {ID: "charger_11", Properties: map[string]string{"identifyValue": "10.21.0.8"}},
		},
	}
	session := &reverseSession{conn: remoteAddrConn{
		Conn:       serverConn,
		remoteAddr: &net.TCPAddr{IP: net.ParseIP("10.20.3.4"), Port: 40000},
	}}

	deviceID, err := rc.identifyDevice(session)
	if err != nil {
		t.Fatalf("identifyDevice failed: %v", err)
	}
	if deviceID != "charger_10" {
		t.Fatalf("identified device = %q, want charger_10", deviceID)
	}
}

func TestReverseConnector_IdentifyRejectsMultipleMatches(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	rc := &reverseConnector{
		config: &ConnectionConfig{Identify: &IdentifyConfig{Mode: identifyModeSourceIP}},
		devicesMeta: map[string]config.Device{
			"charger_10": {ID: "charger_10", Properties: map[string]string{"identifyValue": "10.20.0.0/16"}},
			"charger_11": {ID: "charger_11", Properties: map[string]string{"identifyValue": "10.20.3.4"}},
		},
	}
	session := &reverseSession{conn: remoteAddrConn{
		Conn:       serverConn,
		remoteAddr: &net.TCPAddr{IP: net.ParseIP("10.20.3.4"), Port: 40000},
	}}

	if _, err := rc.identifyDevice(session); err == nil || !strings.Contains(err.Error(), "multiple configured devices") {
		t.Fatalf("identifyDevice error = %v, want multiple-match error", err)
	}
}

func TestReverseConnector_IdentifySingleDevice(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	rc := &reverseConnector{
		config: &ConnectionConfig{Identify: &IdentifyConfig{Mode: identifyModeSingleDevice}},
		devicesMeta: map[string]config.Device{
			"charger_only": {ID: "charger_only"},
		},
	}
	deviceID, err := rc.identifyDevice(&reverseSession{conn: serverConn})
	if err != nil {
		t.Fatalf("identifyDevice failed: %v", err)
	}
	if deviceID != "charger_only" {
		t.Fatalf("identified device = %q, want charger_only", deviceID)
	}

	rc.devicesMeta["charger_second"] = config.Device{ID: "charger_second"}
	if _, err := rc.identifyDevice(&reverseSession{conn: serverConn}); err == nil {
		t.Fatal("identifyDevice should reject multiple devices in single_device mode")
	}
}

func TestNormalizeIdentifyConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *IdentifyConfig
		wantErr bool
	}{
		{name: "missing config", wantErr: true},
		{name: "missing mode", config: &IdentifyConfig{}, wantErr: true},
		{name: "unknown mode", config: &IdentifyConfig{Mode: "auto"}, wantErr: true},
		{name: "register invalid quantity", config: &IdentifyConfig{Mode: identifyModeModbusRegister, Quantity: 126}, wantErr: true},
		{name: "register address overflow", config: &IdentifyConfig{Mode: identifyModeModbusRegister, StartAddress: 65535, Quantity: 2}, wantErr: true},
		{name: "register defaults", config: &IdentifyConfig{Mode: identifyModeModbusRegister, Quantity: 16}},
		{name: "source ip", config: &IdentifyConfig{Mode: identifyModeSourceIP}},
		{name: "single device", config: &IdentifyConfig{Mode: identifyModeSingleDevice}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeIdentifyConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeIdentifyConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.name == "register defaults" && (tt.config.UnitID != 1 || tt.config.Format != "ascii" || tt.config.Timeout != 3000) {
				t.Fatalf("register defaults = unitID:%d format:%q timeout:%d", tt.config.UnitID, tt.config.Format, tt.config.Timeout)
			}
		})
	}
}

func TestReverseConnector_ValidateDeviceBinding(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		devices map[string]config.Device
		wantErr bool
	}{
		{
			name: "single device requires one device",
			mode: identifyModeSingleDevice,
			devices: map[string]config.Device{
				"charger_1": {ID: "charger_1"},
				"charger_2": {ID: "charger_2"},
			},
			wantErr: true,
		},
		{
			name: "register requires identifyValue property",
			mode: identifyModeModbusRegister,
			devices: map[string]config.Device{
				"charger_1": {ID: "charger_1", Properties: map[string]string{"esn": "legacy-value"}},
			},
			wantErr: true,
		},
		{
			name: "register identifyValues must be unique",
			mode: identifyModeModbusRegister,
			devices: map[string]config.Device{
				"charger_1": {ID: "charger_1", Properties: map[string]string{"identifyValue": "same"}},
				"charger_2": {ID: "charger_2", Properties: map[string]string{"identifyValue": "same"}},
			},
			wantErr: true,
		},
		{
			name: "source ip requires identifyValue property",
			mode: identifyModeSourceIP,
			devices: map[string]config.Device{
				"charger_1": {ID: "charger_1", Properties: map[string]string{"ip": "192.168.1.21"}},
			},
			wantErr: true,
		},
		{
			name: "source ip ranges must not overlap",
			mode: identifyModeSourceIP,
			devices: map[string]config.Device{
				"charger_1": {ID: "charger_1", Properties: map[string]string{"identifyValue": "10.20.0.0/16"}},
				"charger_2": {ID: "charger_2", Properties: map[string]string{"identifyValue": "10.20.3.4"}},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &reverseConnector{
				config:      &ConnectionConfig{Identify: &IdentifyConfig{Mode: tt.mode}},
				devicesMeta: tt.devices,
			}
			if err := rc.validateDeviceBinding(); (err != nil) != tt.wantErr {
				t.Fatalf("validateDeviceBinding() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestReverseConnector_ReadOriginalRegisterUsesBoundSession(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	rc := &reverseConnector{
		config:         &ConnectionConfig{},
		deviceSessions: map[string]*reverseSession{"charger_01": &reverseSession{conn: serverConn, timeout: time.Second}},
		intervalLocks:  sync.Map{},
		latestIoTimes:  sync.Map{},
	}

	clientDone := make(chan error, 1)
	go func() {
		request := make([]byte, 12)
		if _, err := io.ReadFull(clientConn, request); err != nil {
			clientDone <- err
			return
		}
		if request[6] != 1 || request[7] != 0x03 {
			clientDone <- fmt.Errorf("unexpected request unit/function = %d/%#x", request[6], request[7])
			return
		}
		response := make([]byte, 11)
		copy(response[:2], request[:2])
		binary.BigEndian.PutUint16(response[4:6], 5)
		response[6] = request[6]
		response[7] = 0x03
		response[8] = 2
		binary.BigEndian.PutUint16(response[9:11], 0x1234)
		_, err := clientConn.Write(response)
		clientDone <- err
	}()

	values, err := rc.readOriginalRegister("charger_01", 1, string(HoldingRegister), 0x10, 1)
	if err != nil {
		t.Fatalf("readOriginalRegister failed: %v", err)
	}
	if len(values) != 1 || values[0] != 0x1234 {
		t.Fatalf("read values = %v, want [0x1234]", values)
	}
	if err := <-clientDone; err != nil {
		t.Fatalf("mock client failed: %v", err)
	}
}

func TestReverseConnector_WithIntervalWaitsAfterIOCompletes(t *testing.T) {
	rc := &reverseConnector{
		config: &ConnectionConfig{MinInterval: 50},
	}

	var firstFinished time.Time
	if err := rc.withInterval("charger_01", func() error {
		time.Sleep(75 * time.Millisecond)
		firstFinished = time.Now()
		return nil
	}); err != nil {
		t.Fatalf("first operation failed: %v", err)
	}

	var secondStarted time.Time
	if err := rc.withInterval("charger_01", func() error {
		secondStarted = time.Now()
		return nil
	}); err != nil {
		t.Fatalf("second operation failed: %v", err)
	}
	if gap := secondStarted.Sub(firstFinished); gap < 40*time.Millisecond {
		t.Fatalf("second operation started %s after first completed, want at least 40ms", gap)
	}
}

func TestReverseConnector_WriteAvoidanceIsPerDevice(t *testing.T) {
	rc := &reverseConnector{config: &ConnectionConfig{MinInterval: 100}}
	now := time.Now()

	stateA := rc.writeState("charger_a")
	stateA.inFlight.Store(1)
	if !rc.shouldAvoidRead("charger_a", now) {
		t.Fatal("read should be avoided for the device being written")
	}
	if rc.shouldAvoidRead("charger_b", now) {
		t.Fatal("write on charger_a must not avoid reads for charger_b")
	}

	stateA.inFlight.Store(0)
	stateA.latestWriteTime.Store(now.UnixNano())
	if !rc.shouldAvoidRead("charger_a", now.Add(50*time.Millisecond)) {
		t.Fatal("read should be avoided during the post-write interval")
	}
	if rc.shouldAvoidRead("charger_b", now.Add(50*time.Millisecond)) {
		t.Fatal("post-write interval for charger_a must not avoid charger_b")
	}
}

func TestIsReverseConnectionClosed(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "eof", err: io.EOF, want: true},
		{name: "reset", err: errors.New("read: connection reset by peer"), want: true},
		{name: "timeout", err: errors.New("i/o timeout"), want: false},
		{name: "modbus exception", err: errors.New("modbus exception: 0x02"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReverseConnectionClosed(tt.err); got != tt.want {
				t.Fatalf("isReverseConnectionClosed(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestReverseConnector_CloseClosesPendingConnections(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	rc := &reverseConnector{
		pendingConns: make(map[net.Conn]struct{}),
	}
	if !rc.trackPendingConn(serverConn) {
		t.Fatal("expected pending connection to be tracked")
	}
	rc.Close()
	defer clientConn.Close()

	if _, err := clientConn.Write([]byte("probe")); err == nil {
		t.Fatal("expected pending connection to be closed")
	}
	if rc.registerSession("device", &reverseSession{conn: clientConn}) {
		t.Fatal("closed connector must reject new sessions")
	}
}

func TestReverseSession_CloseUnblocksActiveRequest(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	session := &reverseSession{conn: serverConn, timeout: 5 * time.Second}

	requestReceived := make(chan struct{})
	go func() {
		request := make([]byte, 12)
		_, _ = io.ReadFull(clientConn, request)
		close(requestReceived)
	}()
	requestDone := make(chan error, 1)
	go func() {
		_, err := session.execute(1, 0x03, []byte{0, 0, 0, 1})
		requestDone <- err
	}()

	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for request")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("session close failed: %v", err)
	}
	select {
	case err := <-requestDone:
		if err == nil {
			t.Fatal("expected active request to fail after close")
		}
	case <-time.After(time.Second):
		t.Fatal("active request was not unblocked by close")
	}
	_ = clientConn.Close()
}

func TestMergeReverseWriteValues(t *testing.T) {
	values := []*writeValue{
		{DeviceId: "charger_01", unitID: 1, RegisterType: HoldingRegister, Address: 10, Value: []uint16{1}},
		{DeviceId: "charger_01", unitID: 1, RegisterType: HoldingRegister, Address: 11, Value: []uint16{2}},
		{DeviceId: "charger_01", unitID: 1, RegisterType: HoldingRegister, Address: 14, Value: []uint16{3}},
		{DeviceId: "charger_01", unitID: 1, RegisterType: Coil, Address: 15, Value: []uint16{1}},
	}

	got := mergeReverseWriteValues(values, 2)
	if len(got) != 3 {
		t.Fatalf("merged writes = %d, want 3", len(got))
	}
	if got[0].Address != 10 || !reflect.DeepEqual(got[0].Value, []uint16{1, 2}) {
		t.Fatalf("first merged write = %#v", got[0])
	}
	if got[1].Address != 14 || !reflect.DeepEqual(got[1].Value, []uint16{3}) {
		t.Fatalf("second write = %#v", got[1])
	}
	if got[2].RegisterType != Coil || got[2].Address != 15 {
		t.Fatalf("coil write = %#v", got[2])
	}

	values = []*writeValue{
		{DeviceId: "charger_01", unitID: 1, RegisterType: HoldingRegister, Address: 10, Value: []uint16{1}},
		{DeviceId: "charger_01", unitID: 1, RegisterType: Coil, Address: 15, Value: []uint16{1}},
		{DeviceId: "charger_01", unitID: 1, RegisterType: HoldingRegister, Address: 11, Value: []uint16{2}},
	}
	got = mergeReverseWriteValues(values, 2)
	if len(got) != 3 || got[0].RegisterType != HoldingRegister || got[1].RegisterType != Coil || got[2].RegisterType != HoldingRegister {
		t.Fatalf("writes across register types were reordered: %#v", got)
	}
	if !reflect.DeepEqual(got[0].Value, []uint16{1}) || !reflect.DeepEqual(got[2].Value, []uint16{2}) {
		t.Fatalf("holding writes across coil write were merged: %#v", got)
	}

	got = mergeReverseWriteValues([]*writeValue{
		{DeviceId: "charger_01", unitID: 1, RegisterType: HoldingRegister, Address: 10, Value: []uint16{1}},
		{DeviceId: "charger_01", unitID: 1, RegisterType: HoldingRegister, Address: 11, Value: []uint16{2}},
	}, 0)
	if len(got) != 2 || !reflect.DeepEqual(got[0].Value, []uint16{1}) || !reflect.DeepEqual(got[1].Value, []uint16{2}) {
		t.Fatalf("writes with batchWriteLen=0 = %#v, want two separate writes", got)
	}
}
