package internal

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/cache"
	exports "github.com/orglibs/driver-box/v2/internal/export"
	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"go.uber.org/zap"
)

type stringRequest struct {
	unit, function    byte
	address, quantity uint16
	values            []uint16
}

// A wire-level slave for TCP, reverse TCP and RTU framing tests.
type stringSlave struct {
	mu        sync.Mutex
	registers map[uint16]uint16
	requests  []stringRequest
}

func (s *stringSlave) set(address uint16, values []uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registers == nil {
		s.registers = map[uint16]uint16{}
	}
	for i, value := range values {
		s.registers[address+uint16(i)] = value
	}
}

func (s *stringSlave) take() []stringRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := s.requests
	s.requests = nil
	return requests
}

func (s *stringSlave) respond(unit byte, pdu []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(pdu) < 5 {
		return nil, fmt.Errorf("short request %x", pdu)
	}
	r := stringRequest{unit: unit, function: pdu[0], address: binary.BigEndian.Uint16(pdu[1:3]), quantity: binary.BigEndian.Uint16(pdu[3:5])}
	var response []byte
	switch r.function {
	case 3, 4:
		if r.quantity < 1 || r.quantity > 125 || int(r.address)+int(r.quantity) > 65536 {
			return nil, fmt.Errorf("invalid read %+v", r)
		}
		response = make([]byte, 2+2*int(r.quantity))
		response[0] = r.function
		response[1] = byte(2 * r.quantity)
		for i := 0; i < int(r.quantity); i++ {
			binary.BigEndian.PutUint16(response[2+2*i:], s.registers[r.address+uint16(i)])
		}
	case 6:
		r.values = []uint16{r.quantity}
		r.quantity = 1
		response = append([]byte(nil), pdu...)
	case 16:
		if r.quantity < 1 || r.quantity > 123 || len(pdu) != 6+2*int(r.quantity) || int(pdu[5]) != 2*int(r.quantity) {
			return nil, fmt.Errorf("invalid FC16 %x", pdu)
		}
		for i := 0; i < int(r.quantity); i++ {
			r.values = append(r.values, binary.BigEndian.Uint16(pdu[6+2*i:]))
		}
		response = append([]byte(nil), pdu[:5]...)
	default:
		return nil, fmt.Errorf("unsupported function %x", r.function)
	}
	for i, value := range r.values {
		s.registers[r.address+uint16(i)] = value
	}
	s.requests = append(s.requests, r)
	return response, nil
}

func stringCRC(data []byte) uint16 {
	crc := uint16(0xffff)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xa001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

func serveStringSlave(t *testing.T, conn io.ReadWriteCloser, rtu bool, slave *stringSlave) {
	t.Helper()
	defer conn.Close()
	for {
		var unit byte
		var pdu, header []byte
		if rtu {
			header = make([]byte, 2)
			if _, err := io.ReadFull(conn, header); err != nil {
				return
			}
			unit = header[0]
			length := 4
			if header[1] == 16 {
				length = 5
			}
			body := make([]byte, length)
			if _, err := io.ReadFull(conn, body); err != nil {
				return
			}
			if header[1] == 16 {
				data := make([]byte, int(body[4]))
				if _, err := io.ReadFull(conn, data); err != nil {
					return
				}
				body = append(body, data...)
			}
			crc := make([]byte, 2)
			if _, err := io.ReadFull(conn, crc); err != nil {
				return
			}
			if stringCRC(append(append([]byte(nil), header...), body...)) != binary.LittleEndian.Uint16(crc) {
				t.Error("invalid request CRC")
				return
			}
			pdu = append([]byte{header[1]}, body...)
		} else {
			header = make([]byte, 7)
			if _, err := io.ReadFull(conn, header); err != nil {
				return
			}
			unit = header[6]
			length := int(binary.BigEndian.Uint16(header[4:6])) - 1
			if length < 1 || length > 253 {
				t.Errorf("invalid MBAP length %d", length)
				return
			}
			pdu = make([]byte, length)
			if _, err := io.ReadFull(conn, pdu); err != nil {
				return
			}
		}
		response, err := slave.respond(unit, pdu)
		if err != nil {
			t.Error(err)
			return
		}
		var frame []byte
		if rtu {
			frame = append([]byte{unit}, response...)
			crc := stringCRC(frame)
			frame = append(frame, byte(crc), byte(crc>>8))
		} else {
			binary.BigEndian.PutUint16(header[4:6], uint16(1+len(response)))
			frame = append(header, response...)
		}
		if _, err := conn.Write(frame); err != nil {
			return
		}
	}
}

func startStringSlave(t *testing.T, rtu bool, slave *stringSlave) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var active sync.Map
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			active.Store(conn, true)
			workers.Add(1)
			go func() { defer workers.Done(); defer active.Delete(conn); serveStringSlave(t, conn, rtu, slave) }()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		active.Range(func(key, value interface{}) bool { _ = key.(net.Conn).Close(); return true })
		workers.Wait()
	})
	return listener.Addr().String()
}

func testStringPoint(name, address string, quantity int, access string) config.Point {
	p := stringPoint()
	p["name"] = name
	p["startAddress"] = address
	p["quantity"] = quantity
	p["readWrite"] = access
	p["reportMode"] = "change"
	return p
}

func initStringFramework(t *testing.T) (*batchRecorder, []config.Point) {
	t.Helper()
	logger.Logger = zap.NewNop()
	recorder := &batchRecorder{}
	exports.Exports = nil
	driverbox.EnableExport(recorder)
	points := []config.Point{
		testStringPoint("text", "100d", 3, "R"), testStringPoint("alias", "100d", 2, "R"),
		testStringPoint("number", "100d", 1, "R"), testStringPoint("bit", "100d", 1, "R"), testStringPoint("bad", "103d", 1, "R"),
		testStringPoint("version", "3", 24, "R"), testStringPoint("esn", "27", 16, "R"),
		testStringPoint("write-one", "200d", 1, "RW"), testStringPoint("write-multi", "201d", 1, "RW"),
		testStringPoint("write-big", "1000d", 123, "W"), testStringPoint("write-adjacent", "1123d", 1, "W"),
	}
	points[2]["rawType"] = "Uint32"
	points[2]["valueType"] = "int"
	points[3]["rawType"] = "Uint16"
	points[3]["valueType"] = "int"
	points[3]["bit"] = 0
	points[3]["bitLen"] = 4
	points[8]["multiWrite"] = true
	for _, bs := range []bool{false, true} {
		for _, ws := range []bool{false, true} {
			p := testStringPoint(fmt.Sprintf("write-%v-%v", bs, ws), "210d", 3, "RW")
			p["byteSwap"] = bs
			p["wordSwap"] = ws
			points = append(points, p)
		}
	}
	config.ResourcePath = t.TempDir()
	cfg := config.DeviceConfig{PluginName: ProtocolName, DeviceModels: []config.DeviceModel{{Model: config.Model{Name: "strings", ModelID: "strings", DevicePoints: points}, Devices: []config.Device{{ID: "string-A", Properties: map[string]string{"unitID": "1"}}, {ID: "string-B", Properties: map[string]string{"unitID": "1"}}}}}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(config.ResourcePath, "driver", ProtocolName)
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.InitCoreCache(map[string]plugin.Plugin{ProtocolName: &Plugin{}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"string-A", "string-B"} {
		driverbox.Shadow().AddDevice(id, "strings")
	}
	return recorder, points
}

// All cache/Shadow/export integration cases run in one isolated process.
func TestModbusStringIntegration(t *testing.T) {
	if os.Getenv("DRIVERBOX_STRING_TEST_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestModbusStringIntegration$", "-test.timeout=60s")
		cmd.Env = append(os.Environ(), "DRIVERBOX_STRING_TEST_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("String integration: %v\n%s", err, output)
		}
		return
	}
	recorder, points := initStringFramework(t)
	for _, mode := range []string{"tcp", "rtuovertcp", "reverse_tcp"} {
		t.Run(mode, func(t *testing.T) { testStringTransport(t, mode, recorder, points) })
	}
	t.Run("initialization", testStringInitialization)
	t.Run("reverse session isolation", func(t *testing.T) { testStringReverseSessions(t, points[0]) })
}

func testStringReverseSessions(t *testing.T, point config.Point) {
	conf := &ConnectionConfig{Mode: "reverse_tcp", Address: "127.0.0.1:0", Timeout: 1000, MinInterval: 1, BatchReadLen: 3,
		Identify: &IdentifyConfig{Mode: identifyModeModbusRegister, StartAddress: 27, Quantity: 16, Format: "ascii"}}
	rc, err := newReverseConnector(&Plugin{}, conf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rc.Close)
	for _, id := range []string{"string-A", "string-B"} {
		rc.addDevice(config.DeviceModel{Model: config.Model{DevicePoints: []config.Point{point}}}, config.Device{ID: id, Properties: map[string]string{"unitID": "1", "identifyValue": id}})
	}
	if err := rc.validateDeviceBinding(); err != nil {
		t.Fatal(err)
	}
	rc.Start()
	connect := func(id, text string, old *reverseSession) *reverseSession {
		t.Helper()
		slave := &stringSlave{}
		identity, err := encodeModbusString(id, 16, false, false)
		if err != nil {
			t.Fatal(err)
		}
		value, err := encodeModbusString(text, 3, false, false)
		if err != nil {
			t.Fatal(err)
		}
		slave.set(27, identity)
		slave.set(100, value)
		conn, err := net.Dial("tcp", rc.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { defer close(done); serveStringSlave(t, conn, false, slave) }()
		t.Cleanup(func() { _ = conn.Close(); <-done })
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if session, ok := rc.getSession(id); ok && session != old {
				return session
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("session for %s did not bind", id)
		return nil
	}
	first := connect("string-A", "ABCD", nil)
	second := connect("string-B", "EFGH", nil)
	read := func(id, want string, session *reverseSession) {
		t.Helper()
		groups := rc.devicePointGroups[id]
		if err := rc.sendReadCommand(session, id, command{Mode: plugin.ReadMode, Value: groups[0]}); err != nil {
			t.Fatal(err)
		}
		got, err := driverbox.Shadow().GetDevicePoint(id, "text")
		if err != nil || got != want {
			t.Fatalf("%s text=%q,%v want %q", id, got, err, want)
		}
	}
	read("string-A", "ABCD", first)
	read("string-B", "EFGH", second)
	replacement := connect("string-A", "NEW", first)
	read("string-A", "NEW", replacement)
	read("string-B", "EFGH", second)
	deadline := time.Now().Add(time.Second)
	for !first.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !first.closed.Load() || second.closed.Load() {
		t.Fatal("reconnect closed the wrong session")
	}
}

func testStringTransport(t *testing.T, mode string, recorder *batchRecorder, points []config.Point) {
	slave := &stringSlave{}
	slave.set(100, []uint16{0x4142, 0x4344, 0x4546, 0xffff})
	var forward *connector
	var reverse *reverseConnector
	var session *reverseSession
	conf := &ConnectionConfig{Mode: mode, Timeout: 1000, MinInterval: 1, BatchReadLen: 32, BatchWriteLen: 200}
	if mode == "reverse_tcp" {
		conf.Address = "127.0.0.1:0"
		conf.Identify = &IdentifyConfig{Mode: identifyModeModbusRegister, StartAddress: 27, Quantity: 16, Format: "ascii"}
		var err error
		reverse, err = newReverseConnector(&Plugin{}, conf)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reverse.Close)
		dev := config.Device{ID: "string-A", Properties: map[string]string{"unitID": "1", "identifyValue": " \x00PILE-A "}}
		reverse.addDevice(config.DeviceModel{Model: config.Model{DevicePoints: points}}, dev)
		if err := reverse.validateDeviceBinding(); err != nil {
			t.Fatal(err)
		}
		slave.set(27, []uint16{0x2050, 0x494c, 0x452d, 0x4100})
		reverse.Start()
		link, err := net.Dial("tcp", reverse.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { defer close(done); serveStringSlave(t, link, false, slave) }()
		t.Cleanup(func() { _ = link.Close(); <-done })
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if found, ok := reverse.getSession("string-A"); ok {
				session = found
				break
			}
			time.Sleep(time.Millisecond)
		}
		if session == nil {
			t.Fatal("reverse identity did not bind")
		}
		// Probe quantity is independent of batchReadLen.
		requests := slave.take()
		if len(requests) != 1 || requests[0].address != 27 || requests[0].quantity != 16 {
			t.Fatalf("probe requests: %+v", requests)
		}
	} else {
		conf.Address = startStringSlave(t, mode == "rtuovertcp", slave)
		var err error
		forward, err = newConnector(&Plugin{}, conf)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(forward.Close)
	}
	read := func(group *pointGroup) {
		t.Helper()
		var err error
		if reverse != nil {
			err = reverse.sendReadCommand(session, "string-A", command{Mode: plugin.ReadMode, Value: group})
		} else {
			err = forward.sendReadCommand(group)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	group := &pointGroup{UnitID: 1, Address: 100, Quantity: 4, RegisterType: HoldingRegister}
	for _, p := range points[:5] {
		ext, err := convToPointExtend(p)
		if err != nil {
			t.Fatal(err)
		}
		ext.DeviceId = "string-A"
		group.Points = append(group.Points, ext)
	}
	_ = driverbox.Shadow().DeleteDevice("string-A")
	driverbox.Shadow().AddDevice("string-A", "strings")
	_ = driverbox.Shadow().SetDevicePoint("string-A", "bad", "kept")
	recorder.take()
	assertShadow := func(name string, want interface{}) {
		t.Helper()
		got, err := driverbox.Shadow().GetDevicePoint("string-A", name)
		if err != nil || got != want {
			t.Fatalf("Shadow %s=%#v,%v want %#v", name, got, err, want)
		}
	}
	read(group)
	assertShadow("text", "ABCDEF")
	assertShadow("alias", "ABCD")
	assertShadow("number", int64(0x43444142))
	assertShadow("bit", int64(2))
	assertShadow("bad", "kept")
	calls, batches := recorder.take()
	if calls != 1 || len(batches) != 1 || len(batches[0].Values) != 4 {
		t.Fatalf("batch=%d %+v", calls, batches)
	}
	read(group)
	_, batches = recorder.take()
	if len(batches) != 0 {
		t.Fatalf("unchanged values exported: %+v", batches)
	}
	// Reversing model order must not alter overlapping numeric or String values.
	for i, j := 0, len(group.Points)-1; i < j; i, j = i+1, j-1 {
		group.Points[i], group.Points[j] = group.Points[j], group.Points[i]
	}
	read(group)
	assertShadow("text", "ABCDEF")
	assertShadow("number", int64(0x43444142))
	_, batches = recorder.take()
	if len(batches) != 0 {
		t.Fatal("model order changed results")
	}
	// Invalid windows are isolated in the same way as invalid UTF-8.
	broken := &Point{Point: config.Point{"name": "text"}, DeviceId: "string-A", Address: 99, Quantity: 3, RawType: "String"}
	group.Points = append(group.Points, broken)
	read(group)
	group.Points = group.Points[:len(group.Points)-1]
	// Empty is a valid change; a later valid sample recovers without reconnecting.
	slave.set(100, []uint16{0, 0, 0, 0x4f4b})
	read(group)
	assertShadow("text", "")
	assertShadow("bad", "OK")
	_, batches = recorder.take()
	foundEmpty := false
	for _, b := range batches {
		for _, p := range b.Values {
			if p.PointName == "text" && p.Value == "" {
				foundEmpty = true
			}
		}
	}
	if !foundEmpty {
		t.Fatal("empty String change was not exported")
	}
	slave.set(100, []uint16{0xe4b8, 0xade6, 0x9687, 0x4f4b})
	read(group)
	assertShadow("text", "中文")
	if session != nil && session.closed.Load() {
		t.Fatal("text decode error closed session")
	}
	// Version and ESN use the configured full address and register count.
	for _, index := range []int{5, 6} {
		ext, err := convToPointExtend(points[index])
		if err != nil {
			t.Fatal(err)
		}
		ext.DeviceId = "string-A"
		for _, sample := range []string{"HW VERSION 1.2.3", " \x00 HW PILE 001 \x00 ", " A  B ", " A\x00B ", " \tA\n\u3000 ", "", "中文"} {
			for _, bs := range []bool{false, true} {
				for _, ws := range []bool{false, true} {
					regs, err := encodeModbusString(sample, ext.Quantity, bs, ws)
					if err != nil {
						t.Fatal(err)
					}
					slave.set(ext.Address, regs)
					ext.ByteSwap = bs
					ext.WordSwap = ws
					if index == 5 {
						ext.RegisterType = InputRegister
					} else {
						ext.RegisterType = HoldingRegister
					}
					slave.take()
					read(&pointGroup{UnitID: 1, Address: ext.Address, Quantity: ext.Quantity, RegisterType: ext.RegisterType, Points: []*Point{ext}})
					want, _ := normalizeModbusText(sample)
					assertShadow(ext.Name(), want)
					requests := slave.take()
					if len(requests) != 1 || requests[0].address != ext.Address || requests[0].quantity != ext.Quantity {
						t.Fatalf("configured String request: %+v", requests)
					}
				}
			}
		}
	}
	// Read through the final physical address without uint16 endpoint wrap.
	for _, boundary := range []struct {
		address  string
		quantity int
	}{{"65411", 125}, {"65535", 1}} {
		point := testStringPoint("esn", boundary.address, boundary.quantity, "R")
		ext, err := convToPointExtend(point)
		if err != nil {
			t.Fatal(err)
		}
		ext.DeviceId = "string-A"
		registers := make([]uint16, ext.Quantity)
		for i := range registers {
			registers[i] = 0x4142
		}
		slave.set(ext.Address, registers)
		slave.take()
		read(&pointGroup{UnitID: 1, Address: ext.Address, Quantity: ext.Quantity, RegisterType: HoldingRegister, Points: []*Point{ext}})
		assertShadow("esn", strings.Repeat("AB", boundary.quantity))
		requests := slave.take()
		if len(requests) != 1 || requests[0].address != ext.Address || requests[0].quantity != ext.Quantity {
			t.Fatalf("address boundary request: %+v", requests)
		}
	}
	testStringWrites(t, forward, reverse, slave)
}

func testStringWrites(t *testing.T, forward *connector, reverse *reverseConnector, slave *stringSlave) {
	t.Helper()
	write := func(values ...plugin.PointData) error {
		if reverse != nil {
			encoded, err := reverse.Encode("string-A", plugin.WriteMode, values...)
			if err != nil {
				return err
			}
			return reverse.Send(encoded)
		}
		encoded, err := forward.Encode("string-A", plugin.WriteMode, values...)
		if err != nil {
			return err
		}
		return forward.Send(encoded)
	}
	for _, name := range []string{"write-one", "write-multi", "write-false-false", "write-true-false", "write-false-true", "write-true-true"} {
		for _, text := range []string{"AB", "A", ""} {
			slave.take()
			if err := write(plugin.PointData{PointName: name, Value: text}); err != nil {
				t.Fatal(err)
			}
			requests := slave.take()
			if len(requests) != 1 {
				t.Fatalf("write requests %+v", requests)
			}
			request := requests[0]
			wantFC := byte(16)
			if name == "write-one" {
				wantFC = 6
			}
			if request.function != wantFC || request.unit != 1 {
				t.Fatalf("write frame %+v", request)
			}
			point, _ := driverbox.CoreCache().GetPointByDevice("string-A", name)
			ext, err := convToPointExtend(point)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeModbusString(request.values, ext.ByteSwap, ext.WordSwap)
			if err != nil || got != text || len(request.values) != int(ext.Quantity) {
				t.Fatalf("write %s=%q frame %+v decoded %q %v", name, text, request, got, err)
			}
		}
	}
	for _, text := range []string{"TOO LONG", "\xff"} {
		slave.take()
		if err := write(plugin.PointData{PointName: "write-one", Value: text}); err == nil {
			t.Fatal("accepted invalid write")
		}
		if requests := slave.take(); len(requests) != 0 {
			t.Fatalf("invalid text sent requests %+v", requests)
		}
	}
	// A 123-register point stays whole with a small merge threshold; adjacent
	// writes cannot make an FC16 exceed 123 even with a larger threshold.
	for _, limit := range []uint16{1, 200} {
		if reverse != nil {
			reverse.config.BatchWriteLen = limit
		} else {
			forward.config.BatchWriteLen = limit
		}
		slave.take()
		if err := write(plugin.PointData{PointName: "write-big", Value: strings.Repeat("X", 246)}, plugin.PointData{PointName: "write-adjacent", Value: "OK"}); err != nil {
			t.Fatal(err)
		}
		requests := slave.take()
		if len(requests) != 2 || requests[0].quantity != 123 || requests[1].quantity != 1 {
			t.Fatalf("FC16 merge boundary %+v", requests)
		}
	}
}

func testStringInitialization(t *testing.T) {
	bad := testStringPoint("invalid", "27", 0, "W")
	good := testStringPoint("valid", "27", 3, "W")
	cfg := config.DeviceConfig{Connections: map[string]interface{}{}, DeviceModels: []config.DeviceModel{}}
	for _, key := range []string{"bad-forward", "bad-reverse", "good-reverse"} {
		mode := "reverse_tcp"
		if key == "bad-forward" {
			mode = "tcp"
		}
		cfg.Connections[key] = map[string]interface{}{"mode": mode, "address": "127.0.0.1:0", "enable": true, "identify": map[string]interface{}{"mode": "single_device"}}
		point := bad
		if key == "good-reverse" {
			point = good
		}
		cfg.DeviceModels = append(cfg.DeviceModels, config.DeviceModel{Model: config.Model{Name: key, DevicePoints: []config.Point{point}}, Devices: []config.Device{{ID: key, ConnectionKey: key, Properties: map[string]string{"unitID": "1"}}}})
	}
	p := &Plugin{}
	p.Initialize(cfg)
	t.Cleanup(func() {
		for _, conn := range p.connPool {
			conn.Close()
		}
	})
	if len(p.connPool) != 1 || p.connPool["good-reverse"] == nil {
		t.Fatalf("invalid String connections were initialized: %v", reflect.ValueOf(p.connPool).MapKeys())
	}
}
