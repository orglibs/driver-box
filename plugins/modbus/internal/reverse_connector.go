package internal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
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

// reverseSession 代表一个连入的 Modbus TCP 客户端会话
type reverseSession struct {
	deviceId  string
	conn      net.Conn
	timeout   time.Duration
	lastTxnId uint16
	mu        sync.Mutex
	createdAt time.Time
	closed    bool
}

func (s *reverseSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

// execute 发送 Modbus TCP 报文并同步等待应答
func (s *reverseSession) execute(slaveId uint8, functionCode uint8, payload []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.conn == nil {
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
type reverseConnector struct {
	config   *ConnectionConfig
	plugin   *Plugin
	listener net.Listener
	closed   bool
	mutex    sync.RWMutex

	// deviceSessions 活跃会话映射：deviceId -> *reverseSession
	deviceSessions map[string]*reverseSession
	// connDevices 反向索引：net.Conn -> deviceId
	connDevices map[net.Conn]string

	// devicePointGroups 每个设备拥有的点位组列表
	devicePointGroups map[string][]*pointGroup
	// devicesMeta 维护连接下的所有物模型设备元信息
	devicesMeta map[string]config.Device

	collectTask *crontab.Future

	writeSemaphore  atomic.Int32
	latestWriteTime time.Time
	writeEncodeMu   sync.Mutex
	latestIoTimes   sync.Map // deviceId -> time.Time
}

func newReverseConnector(p *Plugin, cf *ConnectionConfig) (*reverseConnector, error) {
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
		connDevices:       make(map[net.Conn]string),
		devicePointGroups: make(map[string][]*pointGroup),
		devicesMeta:       make(map[string]config.Device),
	}

	// 启动后台 TCP 接收循环
	go rc.acceptLoop()

	return rc, nil
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
			if rc.closed {
				return
			}
			modbusLog().Warn("modbus reverse accept error", zap.Error(err))
			continue
		}
		modbusLog().Info("modbus reverse client connected", zap.String("remoteAddr", conn.RemoteAddr().String()))
		go rc.handleIncomingConn(conn)
	}
}

func (rc *reverseConnector) handleIncomingConn(conn net.Conn) {
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
		_ = conn.Close()
		return
	}

	// 设置正常轮询超时
	session.timeout = time.Duration(rc.config.Timeout) * time.Millisecond

	// 注册会话并踢除旧连接
	rc.registerSession(deviceId, session)

	modbusLog().Info("modbus reverse client successfully bound to device",
		zap.String("deviceId", deviceId),
		zap.String("remoteAddr", conn.RemoteAddr().String()))
}

// identifyDevice 通过探针读取或 IP 匹配确定连入设备的 deviceId
func (rc *reverseConnector) identifyDevice(session *reverseSession) (string, error) {
	rc.mutex.RLock()
	defer rc.mutex.RUnlock()

	// 1. 如果配置了 identify 探针，向客户端主动发送 Modbus 03 读取 ESN
	if rc.config.Identify != nil && rc.config.Identify.Quantity > 0 {
		regs, err := session.readRegisters(1, 0x03, rc.config.Identify.StartAddress, rc.config.Identify.Quantity)
		if err != nil {
			return "", fmt.Errorf("probe read failed: %w", err)
		}
		var ident string
		if strings.ToLower(rc.config.Identify.Format) == "hex" {
			var sb strings.Builder
			for _, val := range regs {
				sb.WriteString(fmt.Sprintf("%04x", val))
			}
			ident = sb.String()
		} else {
			// 默认 ascii 格式
			bytes := make([]byte, len(regs)*2)
			for i, val := range regs {
				bytes[i*2] = byte(val >> 8)
				bytes[i*2+1] = byte(val & 0xFF)
			}
			ident = strings.TrimRight(string(bytes), "\x00 \t\r\n")
		}

		modbusLog().Info("modbus reverse probed device identity",
			zap.String("remoteAddr", session.conn.RemoteAddr().String()),
			zap.String("identity", ident))

		// 匹配设备
		for devId, dev := range rc.devicesMeta {
			if dev.Properties != nil {
				if dev.Properties["esn"] == ident || dev.Properties["device_key"] == ident || dev.Properties["identify"] == ident {
					return devId, nil
				}
			}
			if devId == ident {
				return devId, nil
			}
		}
		return "", fmt.Errorf("no configured device matches probed identity %q", ident)
	}

	// 2. 未配置 identify，尝试按客户端 IP 匹配
	remoteHost, _, err := net.SplitHostPort(session.conn.RemoteAddr().String())
	if err == nil {
		for devId, dev := range rc.devicesMeta {
			if dev.Properties != nil && dev.Properties["ip"] == remoteHost {
				return devId, nil
			}
		}
	}

	// 3. 若只有一个设备且未配置任何区分，直接绑定
	if len(rc.devicesMeta) == 1 {
		for devId := range rc.devicesMeta {
			return devId, nil
		}
	}

	return "", fmt.Errorf("unable to identify client %s: no matching esn or ip", session.conn.RemoteAddr().String())
}

func (rc *reverseConnector) registerSession(deviceId string, session *reverseSession) {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()

	// 踢除旧连接（如果桩掉线重连）
	if old, exists := rc.deviceSessions[deviceId]; exists {
		modbusLog().Warn("client reconnected, kicking off stale connection",
			zap.String("deviceId", deviceId),
			zap.String("oldAddr", old.conn.RemoteAddr().String()),
			zap.String("newAddr", session.conn.RemoteAddr().String()))
		_ = old.Close()
		delete(rc.connDevices, old.conn)
	}

	session.deviceId = deviceId
	rc.deviceSessions[deviceId] = session
	rc.connDevices[session.conn] = deviceId

	// 触发设备上线
	if shadow := driverbox.Shadow(); shadow != nil {
		_ = shadow.SetOnline(deviceId)
	}
}

func (rc *reverseConnector) removeSessionByConn(conn net.Conn) {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()

	deviceId, ok := rc.connDevices[conn]
	if !ok {
		return
	}
	delete(rc.connDevices, conn)

	if current, exists := rc.deviceSessions[deviceId]; exists && current.conn == conn {
		delete(rc.deviceSessions, deviceId)
		modbusLog().Warn("modbus reverse client disconnected, marking device offline",
			zap.String("deviceId", deviceId),
			zap.String("remoteAddr", conn.RemoteAddr().String()))
		if shadow := driverbox.Shadow(); shadow != nil {
			_ = shadow.SetOffline(deviceId)
		}
	}
}

func (rc *reverseConnector) getSession(deviceId string) (*reverseSession, bool) {
	rc.mutex.RLock()
	defer rc.mutex.RUnlock()
	session, ok := rc.deviceSessions[deviceId]
	if !ok || session.closed {
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
			start := group.Address
			if start > ext.Address {
				start = ext.Address
			}
			end := group.Address + group.Quantity
			if end < ext.Address+ext.Quantity {
				end = ext.Address + ext.Quantity
			}
			if end-start <= rc.config.BatchReadLen {
				group.Points = append(group.Points, ext)
				ok = true
				group.Address = start
				group.Quantity = end - start
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

func (rc *reverseConnector) initCollectTask() (*crontab.Future, error) {
	scanInterval, err := crontab.ParseDurationOrDefault(rc.config.ScanInterval, time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid scanInterval: %w", err)
	}

	return driverbox.AddFunc(scanInterval.String(), func() {
		if rc.closed {
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
				if rc.closed {
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
				if rc.writeSemaphore.Load() > 0 || rc.latestWriteTime.Add(time.Duration(rc.config.MinInterval)*time.Millisecond).After(time.Now()) {
					continue
				}

				bac := command{
					Mode:  plugin.ReadMode,
					Value: group,
				}
				if err := rc.sendReadCommand(session, devId, bac); err != nil {
					modbusLog().Error("modbus reverse read error",
						zap.String("deviceId", devId),
						zap.Any("group", group),
						zap.Error(err))
					group.TimeOutCount++
					if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "closed") || strings.Contains(err.Error(), "broken pipe") {
						rc.removeSessionByConn(session.conn)
					}
				} else {
					group.TimeOutCount = 0
				}
				group.LatestTime = time.Now()
			}
		}
	})
}

func (rc *reverseConnector) ensureInterval(deviceId string) {
	if val, ok := rc.latestIoTimes.Load(deviceId); ok {
		lastTime := val.(time.Time)
		np := lastTime.Add(time.Duration(rc.config.MinInterval) * time.Millisecond)
		if time.Now().Before(np) {
			time.Sleep(time.Until(np))
		}
	}
	rc.latestIoTimes.Store(deviceId, time.Now())
}

func (rc *reverseConnector) sendReadCommand(session *reverseSession, deviceId string, data command) error {
	group := data.Value.(*pointGroup)
	if group.Quantity == 0 {
		return errors.New("read quantity is zero")
	}

	rc.ensureInterval(deviceId)

	var values []uint16
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

	if err != nil {
		return err
	}

	var batches []plugin.DeviceData
	for _, point := range group.Points {
		var value interface{}
		start := point.Address - group.Address
		if int(start+point.Quantity) > len(values) {
			continue
		}
		rawValues := values[start : start+point.Quantity]
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
			case strings.ToUpper(ValueTypeString):
				out := getBytesFromUint16s(rawValues, point.ByteSwap)
				out = swapWords(out, point.WordSwap)
				value = strings.TrimRight(string(out), "\x00 ")
			default:
				continue
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
		rc.writeEncodeMu.Lock()
		defer rc.writeEncodeMu.Unlock()
		values := cmd.Value.([]*writeValue)
		for _, wv := range values {
			session, ok := rc.getSession(wv.DeviceId)
			if !ok {
				return fmt.Errorf("device [%s] is not connected", wv.DeviceId)
			}
			rc.ensureInterval(wv.DeviceId)
			rc.writeSemaphore.Add(1)
			var err error
			switch wv.RegisterType {
			case Coil:
				bools := uint16SliceToBool(wv.Value)
				if len(bools) == 1 {
					err = session.writeCoil(wv.unitID, wv.Address, bools[0])
				} else if len(bools) > 1 {
					err = session.writeCoils(wv.unitID, wv.Address, bools)
				}
			case HoldingRegister:
				if len(wv.Value) == 1 && !wv.MultiWrite {
					err = session.writeRegister(wv.unitID, wv.Address, wv.Value[0])
				} else if len(wv.Value) > 0 {
					err = session.writeRegisters(wv.unitID, wv.Address, wv.Value)
				}
			default:
				err = fmt.Errorf("unsupported write register type: %v", wv.RegisterType)
			}
			rc.latestWriteTime = time.Now()
			rc.writeSemaphore.Add(-1)
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
		dummyConn := &connector{}
		for _, p := range values {
			wv, err := dummyConn.getWriteValue(deviceId, p, writeValues)
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
			Value: writeValues,
		}, nil
	}
	return nil, errors.New("reverse connector encode only supports write mode")
}

func (rc *reverseConnector) Release() error {
	return nil
}

func (rc *reverseConnector) Close() {
	rc.closed = true
	if rc.collectTask != nil {
		rc.collectTask.Disable()
	}
	if rc.listener != nil {
		_ = rc.listener.Close()
	}
	rc.mutex.Lock()
	defer rc.mutex.Unlock()
	for _, session := range rc.deviceSessions {
		_ = session.Close()
	}
	rc.deviceSessions = make(map[string]*reverseSession)
	rc.connDevices = make(map[net.Conn]string)
}
