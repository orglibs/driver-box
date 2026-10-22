package internal

import (
	"encoding/binary"
	"io"
	"net"
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
			"esn": "HW_PILE_001",
		},
	}
	rc.addDevice(model, dev)

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
			"esn": "ESN_RECONNECT_TEST",
		},
	})

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
	if !s1.closed {
		t.Fatalf("stale session 1 should be closed")
	}
}
