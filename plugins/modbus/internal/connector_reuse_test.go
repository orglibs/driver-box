package internal

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/orglibs/driver-box/v2/pkg/crontab"
	"github.com/simonvetter/modbus"
)

// Only external client I/O is substituted; reads/writes, classification,
// locking, connection reuse and shutdown run through the real connector.
type reuseTestClient struct {
	*modbus.ModbusClient
	opens, closes, requests int
	nextError               error
	openError               error
	requestStarted          chan struct{}
	requestRelease          chan struct{}
}

func (m *reuseTestClient) Open() error           { m.opens++; return m.openError }
func (m *reuseTestClient) Close() error          { m.closes++; return nil }
func (m *reuseTestClient) SetUnitId(uint8) error { return nil }
func (m *reuseTestClient) request() error {
	m.requests++
	if m.requestStarted != nil {
		close(m.requestStarted)
		<-m.requestRelease
		m.requestStarted = nil
	}
	err := m.nextError
	m.nextError = nil
	return err
}
func (m *reuseTestClient) ReadRegisters(uint16, uint16, modbus.RegType) ([]uint16, error) {
	return []uint16{42}, m.request()
}
func (m *reuseTestClient) ReadCoils(uint16, uint16) ([]bool, error) {
	return []bool{true}, m.request()
}
func (m *reuseTestClient) ReadDiscreteInputs(uint16, uint16) ([]bool, error) {
	return []bool{true}, m.request()
}
func (m *reuseTestClient) WriteRegister(uint16, uint16) error    { return m.request() }
func (m *reuseTestClient) WriteRegisters(uint16, []uint16) error { return m.request() }
func (m *reuseTestClient) WriteCoil(uint16, bool) error          { return m.request() }
func (m *reuseTestClient) WriteCoils(uint16, []bool) error       { return m.request() }

func TestConnectorReuseAfterException(t *testing.T) {
	for _, mode := range []string{"tcp", "rtu", "rtuovertcp"} {
		for _, requestError := range []error{
			nil, modbus.ErrIllegalFunction, modbus.ErrIllegalDataAddress, modbus.ErrIllegalDataValue,
			modbus.ErrServerDeviceFailure, modbus.ErrAcknowledge, modbus.ErrServerDeviceBusy,
			modbus.ErrMemoryParityError, modbus.ErrGWPathUnavailable, modbus.ErrGWTargetFailedToRespond,
			modbus.ErrUnexpectedParameters, fmt.Errorf("wrapped: %w", modbus.ErrIllegalDataAddress),
		} {
			t.Run(fmt.Sprintf("%s/%v", mode, requestError), func(t *testing.T) {
				c, logs := logTestConnector(t, mode)
				client := &reuseTestClient{nextError: requestError}
				c.client, c.keepAlive = client, false
				if _, err := c.read(7, "HOLDING_REGISTER", 100, 1); err != requestError {
					t.Fatalf("error identity changed: %v", err)
				}
				values, err := c.read(7, "HOLDING_REGISTER", 200, 1)
				if err != nil || len(values) != 1 || values[0] != 42 {
					t.Fatalf("next segment failed: %v / %v", values, err)
				}
				if client.opens != 1 || client.closes != 0 || client.requests != 2 || !c.keepAlive {
					t.Fatalf("connection was not reused: %+v", client)
				}
				if requestError != nil {
					entries := logs.FilterMessage("modbus request failed, connection kept").All()
					if len(entries) != 1 || entries[0].ContextMap()["connectionAction"] != "keep" {
						t.Fatalf("missing retained-connection log: %v", logs.All())
					}
				}
				c.Close()
				if client.closes != 1 || c.keepAlive {
					t.Fatal("shutdown did not close persistent connection")
				}
			})
		}
	}
}

func TestConnectorReconnectAfterTransportFailure(t *testing.T) {
	for _, requestError := range []error{
		io.EOF, net.ErrClosed, modbus.ErrRequestTimedOut, modbus.ErrBadCRC,
		modbus.ErrShortFrame, modbus.ErrProtocolError, modbus.ErrBadUnitId,
		modbus.ErrBadTransactionId, errors.New("unknown failure"),
		fmt.Errorf("wrapped: %w", io.EOF),
	} {
		t.Run(requestError.Error(), func(t *testing.T) {
			c, logs := logTestConnector(t, "tcp")
			client := &reuseTestClient{nextError: requestError}
			c.client, c.keepAlive = client, false
			if _, err := c.read(7, "HOLDING_REGISTER", 100, 1); err != requestError {
				t.Fatalf("error identity changed: %v", err)
			}
			if c.keepAlive || client.closes != 1 {
				t.Fatal("failed connection was retained")
			}
			if _, err := c.read(7, "HOLDING_REGISTER", 200, 1); err != nil || client.opens != 2 {
				t.Fatalf("next request did not reopen: opens=%d err=%v", client.opens, err)
			}
			entries := logs.FilterMessage("modbus client error, will close it").All()
			if len(entries) != 1 || entries[0].ContextMap()["connectionAction"] != "close" {
				t.Fatalf("missing close log: %v", logs.All())
			}
			c.Close()
		})
	}
}

func TestConnectorWriteUsesFinalError(t *testing.T) {
	for _, requestError := range []error{nil, modbus.ErrIllegalDataAddress, io.EOF} {
		for _, registerType := range []primaryTable{Coil, HoldingRegister} {
			for _, count := range []int{1, 2} {
				t.Run(fmt.Sprintf("%v/%s/%d", requestError, registerType, count), func(t *testing.T) {
					c, logs := logTestConnector(t, "tcp")
					client := &reuseTestClient{nextError: requestError}
					c.client, c.keepAlive = client, false
					err := c.write(&writeValue{unitID: 7, Address: 123, RegisterType: registerType, Value: make([]uint16, count)})
					if err != requestError || client.requests != 1 {
						t.Fatalf("write result or request count changed: err=%v requests=%d", err, client.requests)
					}
					wantClose := requestError == io.EOF
					if c.keepAlive == wantClose || (client.closes == 1) != wantClose {
						t.Fatalf("write error was not used for connection policy: keepAlive=%v closes=%d", c.keepAlive, client.closes)
					}
					if requestError != nil {
						message, action := "modbus request failed, connection kept", "keep"
						if wantClose {
							message, action = "modbus client error, will close it", "close"
						}
						assertConnectionLog(t, logs, message, "tcp", map[string]string{
							"error": requestError.Error(), "connectionAction": action, "operation": "write", "unitID": "7",
							"registerType": string(registerType), "address": "123", "quantity": fmt.Sprint(count),
						})
					}
					c.Close()
				})
			}
		}
	}
}

func TestConnectorFailedOpenCanRetry(t *testing.T) {
	c, _ := logTestConnector(t, "tcp")
	client := &reuseTestClient{openError: io.EOF}
	c.client, c.keepAlive = client, false
	if _, err := c.read(7, "HOLDING_REGISTER", 1, 1); err != io.EOF || c.keepAlive || client.requests != 0 {
		t.Fatalf("unexpected failed open: %v", err)
	}
	client.openError = nil
	if _, err := c.read(7, "HOLDING_REGISTER", 1, 1); err != nil || client.opens != 2 {
		t.Fatalf("open retry failed: %v", err)
	}
	c.Close()
}

func TestConnectorClosePreventsReopen(t *testing.T) {
	c, _ := logTestConnector(t, "tcp")
	client := &reuseTestClient{}
	c.client, c.keepAlive = client, false
	if _, err := c.read(7, "HOLDING_REGISTER", 1, 1); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); c.Close() }()
	}
	wg.Wait()
	if _, err := c.read(7, "HOLDING_REGISTER", 1, 1); err == nil {
		t.Fatal("closed connector accepted another request")
	}
	if client.opens != 1 || client.closes != 1 || c.keepAlive {
		t.Fatalf("shutdown reopened or repeatedly closed connection: %+v", client)
	}
}

func TestConnectorCloseWaitsForActiveRequest(t *testing.T) {
	c, _ := logTestConnector(t, "tcp")
	started, release := make(chan struct{}), make(chan struct{})
	client := &reuseTestClient{requestStarted: started, requestRelease: release}
	c.client, c.keepAlive = client, false
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	readDone := make(chan error, 1)
	go func() { _, err := c.read(7, "HOLDING_REGISTER", 1, 1); readDone <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	closeDone := make(chan struct{})
	go func() { c.Close(); close(closeDone) }()
	deadline := time.Now().Add(time.Second)
	for !c.close.Load() {
		if time.Now().After(deadline) {
			t.Fatal("close did not start")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-closeDone:
		t.Fatal("close interrupted active request")
	default:
	}
	unblock()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not finish")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("close did not finish")
	}
	if client.closes != 1 || c.keepAlive {
		t.Fatal("active persistent connection was not closed")
	}
}

func TestConnectorConcurrentCloseWithCollectTask(t *testing.T) {
	c, _ := logTestConnector(t, "tcp")
	client := &reuseTestClient{}
	c.client = client
	var err error
	c.collectTask, err = crontab.Instance().AddFunc("1ms", func() {})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c.Close()
		}()
	}
	close(start)
	wg.Wait()
	if client.closes != 1 || c.keepAlive {
		t.Fatal("concurrent shutdown did not close the active connection exactly once")
	}
}
