package internal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/orglibs/driver-box/v2/driverbox"
	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"github.com/orglibs/driver-box/v2/pkg/crontab"
	"go.uber.org/zap"
)

const (
	identifyModeModbusRegister = "modbus_register"
	identifyModeSourceIP       = "source_ip"
	identifyModeSingleDevice   = "single_device"
)

// reverseSession 代表一个连入的 Modbus TCP 客户端会话
type reverseSession struct {
	deviceId  string
	conn      net.Conn
	timeout   time.Duration
	lastTxnId uint16
	mu        sync.Mutex
	createdAt time.Time
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func (s *reverseSession) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		if s.conn != nil {
			s.closeErr = s.conn.Close()
		}
	})
	return s.closeErr
}

// execute 发送 Modbus TCP 报文并同步等待应答
func (s *reverseSession) execute(slaveId uint8, functionCode uint8, payload []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed.Load() || s.conn == nil {
		return nil, errors.New("modbus connection closed")
	}

	_ = s.conn.SetDeadline(time.Now().Add(s.timeout))

	s.lastTxnId++
	txnId := s.lastTxnId

	// MBAP Header (7 bytes): TransactionID(2) + ProtocolID(2, 0) + Length(2) + UnitID(1)
	mbapLen := uint16(1 + 1 + len(payload))
	reqFrame := make([]byte, 7+1+len(payload))
	binary.BigEndian.PutUint16(reqFrame[0:2], txnId)
	binary.BigEndian.PutUint16(reqFrame[2:4], 0)
	binary.BigEndian.PutUint16(reqFrame[4:6], mbapLen)
	reqFrame[6] = slaveId
	reqFrame[7] = functionCode
	copy(reqFrame[8:], payload)

	if _, err := s.conn.Write(reqFrame); err != nil {
		return nil, fmt.Errorf("modbus write error: %w", err)
	}

	// 读取 7 字节 MBAP 响应头
	header := make([]byte, 7)
	if _, err := io.ReadFull(s.conn, header); err != nil {
		return nil, fmt.Errorf("modbus read header error: %w", err)
	}

	respTxnId := binary.BigEndian.Uint16(header[0:2])
	protoId := binary.BigEndian.Uint16(header[2:4])
	respLen := binary.BigEndian.Uint16(header[4:6])

	if protoId != 0 {
		return nil, fmt.Errorf("invalid modbus protocol id: %d", protoId)
	}
	if respTxnId != txnId {
		return nil, fmt.Errorf("modbus txnId mismatch: want %d, got %d", txnId, respTxnId)
	}
	if respLen < 2 {
		return nil, fmt.Errorf("modbus invalid response length: %d", respLen)
	}

	// 读取剩余 PDU 字节 (UnitId 已包含在 7 字节后，剩余长度为 respLen - 1)
	pdu := make([]byte, respLen-1)
	if _, err := io.ReadFull(s.conn, pdu); err != nil {
		return nil, fmt.Errorf("modbus read pdu error: %w", err)
	}

	respFunc := pdu[0]
	if respFunc == functionCode|0x80 {
		exceptionCode := uint8(0)
		if len(pdu) > 1 {
			exceptionCode = pdu[1]
		}
		return nil, fmt.Errorf("modbus exception: 0x%02X", exceptionCode)
	}
	if respFunc != functionCode {
		return nil, fmt.Errorf("modbus function code mismatch: want 0x%02X, got 0x%02X", functionCode, respFunc)
	}

	return pdu[1:], nil
}

func (s *reverseSession) readRegisters(slaveId uint8, functionCode uint8, address, quantity uint16) ([]uint16, error) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[0:2], address)
	binary.BigEndian.PutUint16(payload[2:4], quantity)

	data, err := s.execute(slaveId, functionCode, payload)
	if err != nil {
		return nil, err
	}
	if len(data) < 1 {
		return nil, errors.New("modbus empty data")
	}
	byteCount := int(data[0])
	if len(data) < 1+byteCount || byteCount != int(quantity*2) {
		return nil, fmt.Errorf("modbus byteCount mismatch: want %d, got %d", quantity*2, byteCount)
	}

	res := make([]uint16, quantity)
	for i := 0; i < int(quantity); i++ {
		res[i] = binary.BigEndian.Uint16(data[1+i*2 : 3+i*2])
	}
	return res, nil
}

func (s *reverseSession) readBits(slaveId uint8, functionCode uint8, address, quantity uint16) ([]uint16, error) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[0:2], address)
	binary.BigEndian.PutUint16(payload[2:4], quantity)

	data, err := s.execute(slaveId, functionCode, payload)
	if err != nil {
		return nil, err
	}
	if len(data) < 1 {
		return nil, errors.New("modbus empty data")
	}
	byteCount := int(data[0])
	if len(data) < 1+byteCount {
		return nil, fmt.Errorf("modbus bit data length error")
	}

	res := make([]uint16, quantity)
	for i := uint16(0); i < quantity; i++ {
		byteIndex := i / 8
		bitIndex := i % 8
		if int(byteIndex) < byteCount {
			if (data[1+byteIndex] & (1 << bitIndex)) != 0 {
				res[i] = 1
			} else {
				res[i] = 0
			}
		}
	}
	return res, nil
}

func (s *reverseSession) writeRegister(slaveId uint8, address, value uint16) error {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[0:2], address)
	binary.BigEndian.PutUint16(payload[2:4], value)
	_, err := s.execute(slaveId, 0x06, payload)
	return err
}

func (s *reverseSession) writeRegisters(slaveId uint8, address uint16, values []uint16) error {
	if len(values) < 1 || len(values) > 123 || int(address)+len(values) > 65536 {
		return errors.New("FC16 requires 1–123 registers within the address range")
	}
	qty := uint16(len(values))
	byteCount := uint8(qty * 2)
	payload := make([]byte, 5+len(values)*2)
	binary.BigEndian.PutUint16(payload[0:2], address)
	binary.BigEndian.PutUint16(payload[2:4], qty)
	payload[4] = byteCount
	for i, v := range values {
		binary.BigEndian.PutUint16(payload[5+i*2:7+i*2], v)
	}
	_, err := s.execute(slaveId, 0x10, payload)
	return err
}

func (s *reverseSession) writeCoil(slaveId uint8, address uint16, value bool) error {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[0:2], address)
	if value {
		binary.BigEndian.PutUint16(payload[2:4], 0xFF00)
	} else {
		binary.BigEndian.PutUint16(payload[2:4], 0x0000)
	}
	_, err := s.execute(slaveId, 0x05, payload)
	return err
}

func (s *reverseSession) writeCoils(slaveId uint8, address uint16, values []bool) error {
	qty := uint16(len(values))
	byteCount := uint8((qty + 7) / 8)
	payload := make([]byte, 5+int(byteCount))
	binary.BigEndian.PutUint16(payload[0:2], address)
	binary.BigEndian.PutUint16(payload[2:4], qty)
	payload[4] = byteCount
	for i, v := range values {
		if v {
			payload[5+i/8] |= (1 << (i % 8))
		}
	}
	_, err := s.execute(slaveId, 0x0F, payload)
	return err
}

// reverseConnector 反向 Modbus TCP 连接器
type reverseWriteState struct {
	inFlight        atomic.Int32
	latestWriteTime atomic.Int64
}

type reverseConnector struct {
	config    *ConnectionConfig
	plugin    *Plugin
	listener  net.Listener
	closed    atomic.Bool
	mutex     sync.RWMutex
	startOnce sync.Once
	closeOnce sync.Once

	// deviceSessions 活跃会话映射：deviceId -> *reverseSession
	deviceSessions map[string]*reverseSession
	pendingConns   map[net.Conn]struct{}
	// connDevices 反向索引：net.Conn -> deviceId
	connDevices map[net.Conn]string

	// devicePointGroups 每个设备拥有的点位组列表
	devicePointGroups map[string][]*pointGroup
	// devicesMeta 维护连接下的所有物模型设备元信息
	devicesMeta map[string]config.Device

	collectTask *crontab.Future

	latestIoTimes sync.Map // deviceId -> time.Time
	intervalLocks sync.Map // deviceId -> *sync.Mutex
	writeStates   sync.Map // deviceId -> *reverseWriteState
}

func newReverseConnector(p *Plugin, cf *ConnectionConfig) (*reverseConnector, error) {
	if cf == nil {
		return nil, errors.New("modbus reverse connection config is required")
	}
	if err := normalizeIdentifyConfig(cf.Identify); err != nil {
		return nil, err
	}
	if cf.MinInterval == 0 {
		cf.MinInterval = 100
	}
	if cf.Retry == 0 {
		cf.Retry = 3
	}
	if cf.Timeout <= 0 {
		cf.Timeout = 1000
	}
	if cf.BatchReadLen == 0 {
		cf.BatchReadLen = 32
	}

	listener, err := net.Listen("tcp", cf.Address)
	if err != nil {
		return nil, fmt.Errorf("modbus reverse listener error on %s: %w", cf.Address, err)
	}

	rc := &reverseConnector{
		config:            cf,
		plugin:            p,
		listener:          listener,
		deviceSessions:    make(map[string]*reverseSession),
		pendingConns:      make(map[net.Conn]struct{}),
		connDevices:       make(map[net.Conn]string),
		devicePointGroups: make(map[string][]*pointGroup),
		devicesMeta:       make(map[string]config.Device),
	}

	return rc, nil
}

func normalizeIdentifyConfig(identify *IdentifyConfig) error {
	if identify == nil {
		return errors.New("reverse mode requires identify configuration")
	}
	identify.Mode = strings.TrimSpace(identify.Mode)

	switch identify.Mode {
	case identifyModeModbusRegister:
		if identify.Quantity == 0 || identify.Quantity > 125 {
			return errors.New("identify.quantity must be between 1 and 125 for modbus_register")
		}
		if uint32(identify.StartAddress)+uint32(identify.Quantity) > 1<<16 {
			return errors.New("identify.startAddress and quantity exceed the Modbus register address range")
		}
		if identify.UnitID == 0 {
			identify.UnitID = 1
		}
		identify.Format = strings.ToLower(strings.TrimSpace(identify.Format))
		if identify.Format == "" {
			identify.Format = "ascii"
		}
		if identify.Format != "ascii" && identify.Format != "hex" {
			return errors.New("identify.format must be ascii or hex")
		}
		if identify.Timeout == 0 {
			identify.Timeout = 3000
		}
	case identifyModeSourceIP, identifyModeSingleDevice:
	default:
		return fmt.Errorf("unsupported identify.mode %q", identify.Mode)
	}
	return nil
}

func (rc *reverseConnector) validateDeviceBinding() error {
	identify := rc.config.Identify
	if identify.Mode == identifyModeSingleDevice {
		if len(rc.devicesMeta) != 1 {
			return fmt.Errorf("identify.mode single_device requires exactly one configured device, got %d", len(rc.devicesMeta))
		}
		return nil
	}

	switch identify.Mode {
	case identifyModeModbusRegister, identifyModeSourceIP:
	default:
		return fmt.Errorf("unsupported identify.mode %q", identify.Mode)
	}
	deviceIDs := make([]string, 0, len(rc.devicesMeta))
	for deviceID := range rc.devicesMeta {
		deviceIDs = append(deviceIDs, deviceID)
	}
	sort.Strings(deviceIDs)

	seenValues := make(map[string]string, len(deviceIDs))
	type ipBinding struct {
		deviceID string
		network  *net.IPNet
	}
	var ipBindings []ipBinding
	for _, deviceID := range deviceIDs {
		device := rc.devicesMeta[deviceID]
		value := device.Properties["identifyValue"]
		if identify.Mode == identifyModeModbusRegister && identify.Format != "hex" {
			var err error
			value, err = normalizeModbusText(value)
			if err != nil {
				return fmt.Errorf("device %q properties.identifyValue: %w", deviceID, err)
			}
		}
		if value == "" || ((identify.Mode == identifyModeSourceIP || identify.Format == "hex") && strings.TrimSpace(value) == "") {
			return fmt.Errorf("device %q has no properties.identifyValue for reverse identification", deviceID)
		}
		if identify.Mode == identifyModeModbusRegister {
			if previous, exists := seenValues[value]; exists {
				return fmt.Errorf("devices %q and %q share properties.identifyValue %q", previous, deviceID, value)
			}
			seenValues[value] = deviceID
		}
		if identify.Mode == identifyModeSourceIP {
			network, err := parseIdentifyIPRange(value)
			if err != nil {
				return fmt.Errorf("device %q has invalid properties.identifyValue IP range %q: %w", deviceID, value, err)
			}
			for _, previous := range ipBindings {
				if ipRangesOverlap(previous.network, network) {
					return fmt.Errorf("devices %q and %q have overlapping source IP match ranges", previous.deviceID, deviceID)
				}
			}
			ipBindings = append(ipBindings, ipBinding{deviceID: deviceID, network: network})
		}
	}
	return nil
}

func parseIdentifyIPRange(value string) (*net.IPNet, error) {
	value = strings.TrimSpace(value)
	if ip := net.ParseIP(value); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			return &net.IPNet{IP: ipv4, Mask: net.CIDRMask(32, 32)}, nil
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}, nil
	}
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		return nil, err
	}
	if ipv4 := network.IP.To4(); ipv4 != nil {
		network.IP = ipv4
	}
	return network, nil
}

func ipRangesOverlap(a, b *net.IPNet) bool {
	if (a.IP.To4() == nil) != (b.IP.To4() == nil) {
		return false
	}
	return a.Contains(b.IP) || b.Contains(a.IP)
}

func (rc *reverseConnector) Start() {
	rc.startOnce.Do(func() {
		if !rc.closed.Load() {
			go rc.acceptLoop()
		}
	})
}

func modbusLog() *zap.Logger {
	l := driverbox.Log()
	if l == nil {
		return zap.NewNop()
	}
	return l
}

func (rc *reverseConnector) acceptLoop() {
	modbusLog().Info("modbus reverse server started listening", zap.String("address", rc.config.Address))
	for {
		conn, err := rc.listener.Accept()
		if err != nil {
			if rc.closed.Load() {
				return
			}
			modbusLog().Warn("modbus reverse accept error", zap.Error(err))
			continue
		}
		modbusLog().Info("modbus reverse client connected", zap.String("remoteAddr", conn.RemoteAddr().String()))
		if !rc.trackPendingConn(conn) {
			_ = conn.Close()
			continue
		}
		go rc.handleIncomingConn(conn)
	}
}

func (rc *reverseConnector) trackPendingConn(conn net.Conn) bool {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()
	if rc.closed.Load() {
		return false
	}
	rc.pendingConns[conn] = struct{}{}
	return true
}

func (rc *reverseConnector) untrackPendingConn(conn net.Conn) {
	rc.mutex.Lock()
	delete(rc.pendingConns, conn)
	rc.mutex.Unlock()
}

func (rc *reverseConnector) handleIncomingConn(conn net.Conn) {
	defer rc.untrackPendingConn(conn)

	timeout := time.Duration(rc.config.Timeout) * time.Millisecond
	if rc.config.Identify != nil && rc.config.Identify.Timeout > 0 {
		timeout = time.Duration(rc.config.Identify.Timeout) * time.Millisecond
	}

	session := &reverseSession{
		conn:      conn,
		timeout:   timeout,
		createdAt: time.Now(),
	}

	// 探测与匹配设备 ID
	deviceId, err := rc.identifyDevice(session)
	if err != nil {
		modbusLog().Warn("modbus reverse identify client failed, disconnecting",
			zap.String("remoteAddr", conn.RemoteAddr().String()),
			zap.Error(err))
		_ = session.Close()
		return
	}
	if rc.closed.Load() {
		_ = session.Close()
		return
	}

	// 设置正常轮询超时
	session.timeout = time.Duration(rc.config.Timeout) * time.Millisecond

	// 注册会话并踢除旧连接
	if !rc.registerSession(deviceId, session) {
		_ = session.Close()
		return
	}

	modbusLog().Info("modbus reverse client successfully bound to device",
		zap.String("deviceId", deviceId),
		zap.String("remoteAddr", conn.RemoteAddr().String()))
}

// identifyDevice uses the configured strategy to bind an incoming session to one device.
func (rc *reverseConnector) identifyDevice(session *reverseSession) (string, error) {
	if rc.config == nil || rc.config.Identify == nil {
		return "", errors.New("reverse mode requires identify configuration")
	}
	identify := rc.config.Identify

	rc.mutex.RLock()
	devicesMeta := make(map[string]config.Device, len(rc.devicesMeta))
	for id, device := range rc.devicesMeta {
		devicesMeta[id] = device
	}
	rc.mutex.RUnlock()

	switch identify.Mode {
	case identifyModeModbusRegister:
		regs, err := session.readRegisters(identify.UnitID, 0x03, identify.StartAddress, identify.Quantity)
		if err != nil {
			return "", fmt.Errorf("modbus_register probe read failed: %w", err)
		}
		var ident string
		if identify.Format == "hex" {
			var sb strings.Builder
			for _, val := range regs {
				sb.WriteString(fmt.Sprintf("%04x", val))
			}
			ident = sb.String()
		} else {
			ident, err = decodeModbusString(regs, false, false)
			if err != nil {
				return "", fmt.Errorf("modbus_register probe text: %w", err)
			}
		}
		if ident == "" {
			return "", errors.New("modbus_register probe returned an empty identity")
		}

		modbusLog().Info("modbus reverse probed device identity",
			zap.String("remoteAddr", session.conn.RemoteAddr().String()),
			zap.String("identity", ident))

		var matches []string
		for deviceID, device := range devicesMeta {
			configured := device.Properties["identifyValue"]
			if identify.Format != "hex" {
				configured, err = normalizeModbusText(configured)
				if err != nil || configured == "" {
					return "", fmt.Errorf("device %q has invalid or empty properties.identifyValue", deviceID)
				}
			}
			if configured == ident {
				matches = append(matches, deviceID)
			}
		}
		return resolveUniqueDevice(matches, fmt.Sprintf("properties.identifyValue value %q", ident))

	case identifyModeSourceIP:
		remoteIP, err := identifyRemoteIP(session.conn.RemoteAddr())
		if err != nil {
			return "", err
		}
		var matches []string
		for deviceID, device := range devicesMeta {
			network, err := parseIdentifyIPRange(device.Properties["identifyValue"])
			if err != nil {
				return "", fmt.Errorf("device %q has invalid properties.identifyValue IP range: %w", deviceID, err)
			}
			if network.Contains(remoteIP) {
				matches = append(matches, deviceID)
			}
		}
		return resolveUniqueDevice(matches, fmt.Sprintf("source IP %s", remoteIP.String()))

	case identifyModeSingleDevice:
		if len(devicesMeta) != 1 {
			return "", fmt.Errorf("single_device requires exactly one configured device, got %d", len(devicesMeta))
		}
		for deviceID := range devicesMeta {
			return deviceID, nil
		}
	}

	return "", fmt.Errorf("unsupported identify.mode %q", identify.Mode)
}

func identifyRemoteIP(address net.Addr) (net.IP, error) {
	if tcpAddress, ok := address.(*net.TCPAddr); ok && tcpAddress.IP != nil {
		return tcpAddress.IP, nil
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return nil, fmt.Errorf("cannot parse remote IP from %q: %w", address.String(), err)
	}
	if zoneIndex := strings.LastIndex(host, "%"); zoneIndex >= 0 {
		host = host[:zoneIndex]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("remote address %q is not an IP address", address.String())
	}
	return ip, nil
}

func resolveUniqueDevice(matches []string, description string) (string, error) {
	sort.Strings(matches)
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no configured device matches %s", description)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("multiple configured devices match %s: %s", description, strings.Join(matches, ", "))
	}
}

func (rc *reverseConnector) registerSession(deviceId string, session *reverseSession) bool {
	rc.mutex.Lock()
	if rc.closed.Load() {
		rc.mutex.Unlock()
		return false
	}

	// 踢除旧连接（如果桩掉线重连）
	var old *reverseSession
	if existing, exists := rc.deviceSessions[deviceId]; exists {
		old = existing
		modbusLog().Warn("client reconnected, kicking off stale connection",
			zap.String("deviceId", deviceId),
			zap.String("oldAddr", existing.conn.RemoteAddr().String()),
			zap.String("newAddr", session.conn.RemoteAddr().String()))
		delete(rc.connDevices, existing.conn)
	}

	session.deviceId = deviceId
	rc.deviceSessions[deviceId] = session
	rc.connDevices[session.conn] = deviceId

	// 触发设备上线
	if shadow := driverbox.Shadow(); shadow != nil {
		_ = shadow.SetOnline(deviceId)
	}
	rc.mutex.Unlock()

	if old != nil {
		_ = old.Close()
	}
	return true
}

func (rc *reverseConnector) removeSessionByConn(conn net.Conn) {
	rc.mutex.Lock()
	deviceId, ok := rc.connDevices[conn]
	if !ok {
		rc.mutex.Unlock()
		return
	}
	delete(rc.connDevices, conn)

	var removed *reverseSession
	if current, exists := rc.deviceSessions[deviceId]; exists && current.conn == conn {
		delete(rc.deviceSessions, deviceId)
		removed = current
		modbusLog().Warn("modbus reverse client disconnected, marking device offline",
			zap.String("deviceId", deviceId),
			zap.String("remoteAddr", conn.RemoteAddr().String()))
		if shadow := driverbox.Shadow(); shadow != nil {
			_ = shadow.SetOffline(deviceId)
		}
	}
	rc.mutex.Unlock()

	if removed != nil {
		_ = removed.Close()
	}
}

func (rc *reverseConnector) getSession(deviceId string) (*reverseSession, bool) {
	rc.mutex.RLock()
	defer rc.mutex.RUnlock()
	session, ok := rc.deviceSessions[deviceId]
	if !ok || session.closed.Load() {
		return nil, false
	}
	return session, true
}

func (rc *reverseConnector) addDevice(model config.DeviceModel, dev config.Device) {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()

	rc.devicesMeta[dev.ID] = dev

	groupIndex := 0
	groups := make([]*pointGroup, 0)

	unitId, err := getUnitId(dev.Properties)
	if err != nil {
		unitId = 1
	}

	for _, point := range model.DevicePoints {
		if point.ReadWrite() != config.ReadWrite_R && point.ReadWrite() != config.ReadWrite_RW {
			continue
		}
		ext, err := convToPointExtend(point)
		if err != nil {
			modbusLog().Error("error modbus reverse point config", zap.String("deviceId", dev.ID), zap.Any("point", point), zap.Error(err))
			continue
		}
		ext.DeviceId = dev.ID
		duration, err := time.ParseDuration(ext.Duration)
		if err != nil {
			duration = time.Second
		}

		ok := false
		for _, group := range groups {
			if group.Duration != duration || group.RegisterType != ext.RegisterType {
				continue
			}
			start := uint32(group.Address)
			if start > uint32(ext.Address) {
				start = uint32(ext.Address)
			}
			end := uint32(group.Address) + uint32(group.Quantity)
			if end < uint32(ext.Address)+uint32(ext.Quantity) {
				end = uint32(ext.Address) + uint32(ext.Quantity)
			}
			if end <= 65536 && end-start <= readGroupLimit(rc.config, ext.RegisterType) {
				group.Points = append(group.Points, ext)
				ok = true
				group.Address = uint16(start)
				group.Quantity = uint16(end - start)
				break
			}
		}
		if !ok {
			groups = append(groups, &pointGroup{
				index:        groupIndex,
				UnitID:       unitId,
				Duration:     duration,
				RegisterType: ext.RegisterType,
				Address:      ext.Address,
				Quantity:     ext.Quantity,
				Points:       []*Point{ext},
			})
			groupIndex++
		}
	}
	rc.devicePointGroups[dev.ID] = groups
}

func (rc *reverseConnector) writeState(deviceID string) *reverseWriteState {
	state, _ := rc.writeStates.LoadOrStore(deviceID, &reverseWriteState{})
	return state.(*reverseWriteState)
}

func (rc *reverseConnector) shouldAvoidRead(deviceID string, now time.Time) bool {
	state := rc.writeState(deviceID)
	if state.inFlight.Load() > 0 {
		return true
	}
	lastWrite := state.latestWriteTime.Load()
	return lastWrite != 0 && time.Unix(0, lastWrite).Add(time.Duration(rc.config.MinInterval)*time.Millisecond).After(now)
}

func isReverseConnectionClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "closed") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "forcibly closed")
}

func (rc *reverseConnector) initCollectTask() (*crontab.Future, error) {
	scanInterval, err := crontab.ParseDurationOrDefault(rc.config.ScanInterval, time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid scanInterval: %w", err)
	}

	return driverbox.AddFunc(scanInterval.String(), func() {
		if rc.closed.Load() {
			return
		}
		rc.mutex.RLock()
		devIds := make([]string, 0, len(rc.devicePointGroups))
		for id := range rc.devicePointGroups {
			devIds = append(devIds, id)
		}
		rc.mutex.RUnlock()

		for _, devId := range devIds {
			session, online := rc.getSession(devId)
			if !online {
				continue
			}

			rc.mutex.RLock()
			groups := rc.devicePointGroups[devId]
			rc.mutex.RUnlock()

			for _, group := range groups {
				if rc.closed.Load() {
					return
				}
				duration := group.Duration
				if group.TimeOutCount > 0 {
					duration = duration * time.Duration(1<<group.TimeOutCount)
					if duration > time.Minute {
						duration = time.Minute
					} else if duration < 0 {
						group.TimeOutCount = 0
						duration = group.Duration
					}
				}
				if group.LatestTime.Add(duration).After(time.Now()) {
					continue
				}

				// 写操作避让
				if rc.shouldAvoidRead(devId, time.Now()) {
					continue
				}

				bac := command{
					Mode:  plugin.ReadMode,
					Value: group,
				}
				err := rc.sendReadCommand(session, devId, bac)
				group.LatestTime = time.Now()
				if err != nil {
					modbusLog().Error("modbus reverse read error",
						zap.String("deviceId", devId),
						zap.Any("group", group),
						zap.Error(err))
					group.TimeOutCount++
					if shadow := driverbox.Shadow(); shadow != nil {
						_ = shadow.MayBeOffline(devId)
					}
					if isReverseConnectionClosed(err) {
						rc.removeSessionByConn(session.conn)
						break
					}
				} else {
					if group.TimeOutCount > 0 {
						if shadow := driverbox.Shadow(); shadow != nil {
							_ = shadow.SetOnline(devId)
						}
					}
					group.TimeOutCount = 0
				}
			}
		}
	})
}

func (rc *reverseConnector) withInterval(deviceId string, operation func() error) error {
	lockValue, _ := rc.intervalLocks.LoadOrStore(deviceId, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	if val, ok := rc.latestIoTimes.Load(deviceId); ok {
		lastTime := val.(time.Time)
		np := lastTime.Add(time.Duration(rc.config.MinInterval) * time.Millisecond)
		if time.Now().Before(np) {
			time.Sleep(time.Until(np))
		}
	}
	err := operation()
	rc.latestIoTimes.Store(deviceId, time.Now())
	return err
}

func (rc *reverseConnector) readOriginalRegister(deviceId string, unitID uint8, registerType string, address, quantity uint16) ([]uint16, error) {
	session, ok := rc.getSession(deviceId)
	if !ok {
		return nil, fmt.Errorf("device [%s] is not connected", deviceId)
	}

	var functionCode uint8
	switch strings.ToUpper(registerType) {
	case string(HoldingRegister):
		functionCode = 0x03
	case string(InputRegister):
		functionCode = 0x04
	default:
		return nil, fmt.Errorf("unsupported register type for read-modify-write: %s", registerType)
	}

	var values []uint16
	err := rc.withInterval(deviceId, func() error {
		var err error
		values, err = session.readRegisters(unitID, functionCode, address, quantity)
		return err
	})
	return values, err
}

func (rc *reverseConnector) sendReadCommand(session *reverseSession, deviceId string, data command) error {
	group := data.Value.(*pointGroup)
	if group.Quantity == 0 {
		return errors.New("read quantity is zero")
	}

	var values []uint16
	err := rc.withInterval(deviceId, func() error {
		var err error
		switch strings.ToUpper(string(group.RegisterType)) {
		case string(Coil):
			values, err = session.readBits(group.UnitID, 0x01, group.Address, group.Quantity)
		case string(DiscreteInput):
			values, err = session.readBits(group.UnitID, 0x02, group.Address, group.Quantity)
		case string(InputRegister):
			values, err = session.readRegisters(group.UnitID, 0x04, group.Address, group.Quantity)
		case string(HoldingRegister):
			values, err = session.readRegisters(group.UnitID, 0x03, group.Address, group.Quantity)
		default:
			return fmt.Errorf("unsupported register type: %s", group.RegisterType)
		}
		return err
	})

	if err != nil {
		return err
	}

	var batches []plugin.DeviceData
	for _, point := range group.Points {
		var value interface{}
		window, err := pointReadWindow(values, group.Address, point)
		if err != nil {
			modbusLog().Error("Modbus point decode error", zap.String("deviceId", point.DeviceId), zap.String("pointName", point.Name()), zap.Error(err))
			continue
		}
		if strings.EqualFold(point.RawType, ValueTypeString) {
			value, err = decodeModbusString(window, point.ByteSwap, point.WordSwap)
			if err != nil {
				modbusLog().Error("Modbus String decode error", zap.String("deviceId", point.DeviceId), zap.String("pointName", point.Name()), zap.Error(err))
				continue
			}
		} else {
			// Numeric decoding keeps its historical order on a private window.
			rawValues := append([]uint16(nil), window...)
			reverseUint16s(rawValues)
			switch point.RegisterType {
			case Coil, DiscreteInput:
				value = rawValues[0]
			case InputRegister, HoldingRegister:
				switch strings.ToUpper(point.RawType) {
				case strings.ToUpper(ValueTypeUint16), strings.ToUpper(ValueTypeInt16):
					out := getBytesFromUint16s(rawValues, point.ByteSwap)
					val := binary.BigEndian.Uint16(out)
					if point.BitLen > 0 {
						value = getBitsFromPosition(val, point.Bit, point.BitLen)
					} else {
						if strings.ToUpper(point.RawType) == strings.ToUpper(ValueTypeInt16) {
							value = int16(val)
						} else {
							value = val
						}
					}
				case strings.ToUpper(ValueTypeUint32), strings.ToUpper(ValueTypeInt32), strings.ToUpper(ValueTypeFloat32):
					out := getBytesFromUint16s(rawValues, point.ByteSwap)
					out = swapWords(out, point.WordSwap)
					val := binary.BigEndian.Uint32(out)
					switch strings.ToUpper(point.RawType) {
					case strings.ToUpper(ValueTypeUint32):
						value = val
					case strings.ToUpper(ValueTypeInt32):
						value = int32(val)
					case strings.ToUpper(ValueTypeFloat32):
						value = math.Float32frombits(val)
					}
				case strings.ToUpper(ValueTypeUint64), strings.ToUpper(ValueTypeInt64), strings.ToUpper(ValueTypeFloat64):
					out := getBytesFromUint16s(rawValues, point.ByteSwap)
					out = swapWords(out, point.WordSwap)
					val := binary.BigEndian.Uint64(out)
					switch strings.ToUpper(point.RawType) {
					case strings.ToUpper(ValueTypeUint64):
						value = val
					case strings.ToUpper(ValueTypeInt64):
						value = int64(val)
					case strings.ToUpper(ValueTypeFloat64):
						value = math.Float64frombits(val)
					}

				default:
					continue
				}
			}
		}
		pointReadValue := plugin.PointReadValue{
			ID:        point.DeviceId,
			PointName: point.Name(),
			Value:     value,
		}
		res, err := rc.Decode(pointReadValue)
		if err != nil {
			continue
		}
		batches = append(batches, res...)
	}

	if len(batches) > 0 {
		driverbox.Export(batches)
	}
	return nil
}

func (rc *reverseConnector) Send(data interface{}) error {
	cmd, ok := data.(command)
	if !ok {
		return errors.New("invalid command payload")
	}

	switch cmd.Mode {
	case plugin.WriteMode:
		values := cmd.Value.([]*writeValue)
		for _, wv := range values {
			session, ok := rc.getSession(wv.DeviceId)
			if !ok {
				return fmt.Errorf("device [%s] is not connected", wv.DeviceId)
			}
			state := rc.writeState(wv.DeviceId)
			state.inFlight.Add(1)
			err := rc.withInterval(wv.DeviceId, func() error {
				switch wv.RegisterType {
				case Coil:
					bools := uint16SliceToBool(wv.Value)
					if len(bools) == 1 {
						return session.writeCoil(wv.unitID, wv.Address, bools[0])
					} else if len(bools) > 1 {
						return session.writeCoils(wv.unitID, wv.Address, bools)
					}
				case HoldingRegister:
					if len(wv.Value) == 1 && !wv.MultiWrite {
						return session.writeRegister(wv.unitID, wv.Address, wv.Value[0])
					} else if len(wv.Value) > 0 {
						return session.writeRegisters(wv.unitID, wv.Address, wv.Value)
					}
				default:
					return fmt.Errorf("unsupported write register type: %v", wv.RegisterType)
				}
				return errors.New("write data is empty")
			})
			state.inFlight.Add(-1)
			state.latestWriteTime.Store(time.Now().UnixNano())
			if err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("unsupported command mode for reverse connector")
	}
}

func (rc *reverseConnector) Decode(raw interface{}) (res []plugin.DeviceData, err error) {
	readValue, ok := raw.(plugin.PointReadValue)
	if !ok {
		return nil, fmt.Errorf("unexpected raw: %v", raw)
	}

	res = append(res, plugin.DeviceData{
		ID: readValue.ID,
		Values: []plugin.PointData{{
			PointName: readValue.PointName,
			Value:     readValue.Value,
		}},
	})
	return
}

func (rc *reverseConnector) Encode(deviceId string, mode plugin.EncodeMode, values ...plugin.PointData) (interface{}, error) {
	if mode == plugin.WriteMode {
		writeValues := make([]*writeValue, 0)
		codec := &connector{}
		read := func(unitID uint8, registerType string, address, quantity uint16) ([]uint16, error) {
			return rc.readOriginalRegister(deviceId, unitID, registerType, address, quantity)
		}
		for _, p := range values {
			wv, err := codec.getWriteValueWithReader(deviceId, p, writeValues, read)
			if err != nil {
				return nil, err
			}
			if wv.unitID == 0 {
				continue
			}
			wv.DeviceId = deviceId
			writeValues = append(writeValues, &wv)
		}
		return command{
			Mode:  plugin.WriteMode,
			Value: mergeReverseWriteValues(writeValues, rc.config.BatchWriteLen),
		}, nil
	}
	return nil, errors.New("reverse connector encode only supports write mode")
}

func mergeReverseWriteValues(values []*writeValue, batchWriteLen uint16) []*writeValue {
	if batchWriteLen > 123 {
		batchWriteLen = 123
	}

	merged := make([]*writeValue, 0, len(values))
	for i := 0; i < len(values); {
		if values[i].RegisterType != HoldingRegister || len(values[i].Value) == 0 {
			merged = append(merged, values[i])
			i++
			continue
		}

		end := i + 1
		for end < len(values) && values[end].RegisterType == HoldingRegister && len(values[end].Value) > 0 {
			end++
		}
		run := values[i:end]
		sort.SliceStable(run, func(i, j int) bool {
			return run[i].Address < run[j].Address
		})

		var previous *writeValue
		for _, value := range run {
			if previous != nil && previous.DeviceId == value.DeviceId && previous.unitID == value.unitID &&
				int(previous.Address)+len(previous.Value) == int(value.Address) &&
				len(previous.Value)+len(value.Value) <= int(batchWriteLen) {
				previous.Value = append(previous.Value, value.Value...)
				continue
			}
			previous = value
			merged = append(merged, value)
		}
		i = end
	}
	return merged
}

func (rc *reverseConnector) Release() error {
	return nil
}

func (rc *reverseConnector) Close() {
	rc.closeOnce.Do(func() {
		rc.closed.Store(true)
		if rc.collectTask != nil {
			rc.collectTask.Disable()
		}
		if rc.listener != nil {
			_ = rc.listener.Close()
		}

		rc.mutex.Lock()
		pendingConns := make([]net.Conn, 0, len(rc.pendingConns))
		for conn := range rc.pendingConns {
			pendingConns = append(pendingConns, conn)
		}
		sessions := rc.deviceSessions
		rc.pendingConns = make(map[net.Conn]struct{})
		rc.deviceSessions = make(map[string]*reverseSession)
		rc.connDevices = make(map[net.Conn]string)
		rc.mutex.Unlock()

		for _, conn := range pendingConns {
			_ = conn.Close()
		}
		for deviceID, session := range sessions {
			_ = session.Close()
			if shadow := driverbox.Shadow(); shadow != nil {
				_ = shadow.SetOffline(deviceID)
			}
		}
	})
}
