package base

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"go.uber.org/zap"
)

var discoverTestLogger sync.Once

func initDiscoverTestLogger() { discoverTestLogger.Do(func() { logger.Logger = zap.NewNop() }) }

type memoryPacketConn struct {
	closed chan struct{}
	reads  chan []byte
	writes chan []byte
	once   sync.Once
}

func newMemoryPacketConn() *memoryPacketConn {
	return &memoryPacketConn{closed: make(chan struct{}), reads: make(chan []byte, 1), writes: make(chan []byte, 1)}
}

func (c *memoryPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case data := <-c.reads:
		return copy(b, data), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *memoryPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	select {
	case c.writes <- append([]byte(nil), b...):
		return len(b), nil
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *memoryPacketConn) Close() error                     { c.once.Do(func() { close(c.closed) }); return nil }
func (c *memoryPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *memoryPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *memoryPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *memoryPacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestDestroyBeforeInitialization(t *testing.T) {
	initDiscoverTestLogger()
	e := &Export{}
	for i := 0; i < 2; i++ {
		if err := e.Destroy(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDestroyAfterDiscoveryBindFailure(t *testing.T) {
	initDiscoverTestLogger()
	t.Setenv(config.ENV_UDP_DISCOVER_LISTEN, "-1")
	e := &Export{}
	e.udpDiscover() // Invalid port fails before opening a socket.
	if err := e.Destroy(); err != nil {
		t.Fatal(err)
	}
	if e.discoverConn != nil {
		t.Fatal("failed bind left a connection")
	}
}

func TestDiscoveryStopDuringBindAndRestart(t *testing.T) {
	initDiscoverTestLogger()
	e := &Export{}
	conn := newMemoryPacketConn()
	binding, release := make(chan struct{}), make(chan struct{})
	started, stopped := make(chan error, 1), make(chan struct{})
	go func() {
		started <- e.startDiscover(func() (net.PacketConn, error) {
			close(binding)
			<-release
			return conn, nil
		})
	}()
	<-binding
	go func() { e.stopDiscover(); close(stopped) }()
	close(release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop blocked during bind")
	}
	select {
	case <-conn.closed:
	default:
		t.Fatal("listener survived stop")
	}
	// An old reader exiting must not clear a newer discovery connection.
	next := newMemoryPacketConn()
	if err := e.startDiscover(func() (net.PacketConn, error) { return next, nil }); err != nil {
		t.Fatal(err)
	}
	e.stopDiscover()
	e.stopDiscover()
	select {
	case <-next.closed:
	default:
		t.Fatal("restarted listener survived stop")
	}
}

func TestDiscoveryReplyUsesCurrentListener(t *testing.T) {
	initDiscoverTestLogger()
	e := &Export{httpListen: "8081"}
	conn := newMemoryPacketConn()
	if err := e.startDiscover(func() (net.PacketConn, error) { return conn, nil }); err != nil {
		t.Fatal(err)
	}
	defer e.stopDiscover()
	conn.reads <- []byte("driver-box discovery")
	select {
	case raw := <-conn.writes:
		var response struct{ Port string }
		if err := json.Unmarshal(raw, &response); err != nil || response.Port != "8081" {
			t.Fatalf("discovery response = %s, %v", raw, err)
		}
	case <-time.After(time.Second):
		t.Fatal("discovery did not reply")
	}
	if err := e.startDiscover(func() (net.PacketConn, error) {
		return nil, errors.New("duplicate listener must not be opened")
	}); err != nil {
		t.Fatal(err)
	}
}

type failedReadPacketConn struct{ *memoryPacketConn }

func (c *failedReadPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	return 0, nil, errors.New("injected read failure")
}

func TestDiscoveryReadFailureClosesListener(t *testing.T) {
	initDiscoverTestLogger()
	e := &Export{}
	conn := &failedReadPacketConn{newMemoryPacketConn()}
	if err := e.startDiscover(func() (net.PacketConn, error) { return conn, nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.closed:
	case <-time.After(time.Second):
		t.Fatal("failed reader did not close its listener")
	}
	if err := e.Destroy(); err != nil {
		t.Fatal(err)
	}
}
