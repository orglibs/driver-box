package internal

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/orglibs/driver-box/v2/driverbox/plugin"
	"github.com/orglibs/driver-box/v2/internal/logger"
	"github.com/orglibs/driver-box/v2/pkg/config"
	"github.com/orglibs/driver-box/v2/pkg/convutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type reverseTLSFixture struct {
	serverPEM, clientCA                    string
	roots                                  *x509.CertPool
	client, untrusted, expired, wrongUsage tls.Certificate
}

func newReverseTLSFixture(t *testing.T) reverseTLSFixture {
	t.Helper()
	issue := func(parent *x509.Certificate, signer *ecdsa.PrivateKey, isCA bool, usage x509.ExtKeyUsage, expired bool) (*x509.Certificate, *ecdsa.PrivateKey, string, tls.Certificate) {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "reverse TLS test"},
			NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
			IsCA: isCA, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		if isCA {
			template.KeyUsage |= x509.KeyUsageCertSign
			template.ExtKeyUsage = nil
			parent, signer = template, key
		} else if usage == x509.ExtKeyUsageServerAuth {
			template.DNSNames = []string{"localhost"}
			template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
		if expired {
			template.NotBefore, template.NotAfter = now.Add(-2*time.Hour), now.Add(-time.Hour)
		}
		der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
		pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
		if err != nil {
			t.Fatal(err)
		}
		return cert, key, certPEM + keyPEM, pair
	}
	serverCA, serverKey, _, _ := issue(nil, nil, true, 0, false)
	_, _, serverPEM, _ := issue(serverCA, serverKey, false, x509.ExtKeyUsageServerAuth, false)
	clientCA, clientKey, _, _ := issue(nil, nil, true, 0, false)
	_, _, _, client := issue(clientCA, clientKey, false, x509.ExtKeyUsageClientAuth, false)
	_, _, _, expired := issue(clientCA, clientKey, false, x509.ExtKeyUsageClientAuth, true)
	_, _, _, wrongUsage := issue(clientCA, clientKey, false, x509.ExtKeyUsageServerAuth, false)
	_, _, _, untrusted := issue(serverCA, serverKey, false, x509.ExtKeyUsageClientAuth, false)
	roots := x509.NewCertPool()
	roots.AddCert(serverCA)
	return reverseTLSFixture{serverPEM: serverPEM, clientCA: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCA.Raw})),
		roots: roots, client: client, untrusted: untrusted, expired: expired, wrongUsage: wrongUsage}
}

func TestReverseTLSConfiguration(t *testing.T) {
	f := newReverseTLSFixture(t)
	for _, tt := range []struct {
		name, server, client, mode string
		valid                      bool
	}{
		{"plain", "", "", "reverse_tcp", true},
		{"one way", f.serverPEM, "", "reverse_tcp", true},
		{"mutual", f.serverPEM, f.clientCA, "reverse_tcp", true},
		{"missing server", "", f.clientCA, "reverse_tcp", false},
		{"bad server", "PRIVATE-SECRET", "", "reverse_tcp", false},
		{"missing key", f.clientCA, "", "reverse_tcp", false},
		{"mismatched key", f.clientCA + f.serverPEM, "", "reverse_tcp", false},
		{"bad CA", f.serverPEM, "PRIVATE-SECRET", "reverse_tcp", false},
		{"TCP unsupported", f.serverPEM, "", "tcp", false},
		{"RTU unsupported", f.serverPEM, "", "rtu", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &ConnectionConfig{Mode: tt.mode, ServerTLS: tt.server, ClientTLS: tt.client}
			cfg, err := reverseTLSConfig(c)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v error=%v", tt.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE-SECRET") {
				t.Fatal("error contains PEM input")
			}
			if cfg != nil {
				if cfg.MinVersion != tls.VersionTLS12 || len(cfg.Certificates) != 1 {
					t.Fatal("invalid server TLS configuration")
				}
				if tt.client != "" && (cfg.ClientAuth != tls.RequireAndVerifyClientCert || cfg.ClientCAs == nil) {
					t.Fatal("mutual authentication not enforced")
				}
				if tt.client == "" && cfg.ClientAuth != tls.NoClientCert {
					t.Fatal("one-way TLS requires a client certificate")
				}
			}
			if tt.server == "" && tt.client == "" && cfg != nil {
				t.Fatal("plain TCP enabled TLS")
			}
			if !tt.valid && tt.mode == "reverse_tcp" {
				c.Address = "not a listen address"
				c.Identify = &IdentifyConfig{Mode: identifyModeSingleDevice}
				if _, initErr := newReverseConnector(&Plugin{}, c); initErr == nil || initErr.Error() != err.Error() {
					t.Fatalf("TLS must be rejected before listening: %v", initErr)
				}
			}
			if !tt.valid && tt.mode != "reverse_tcp" {
				if _, err := newConnector(&Plugin{}, c); err == nil {
					t.Fatal("forward connector ignored TLS")
				}
			}
		})
	}
	for _, field := range []string{"serverTls", "clientTls"} {
		for _, value := range []interface{}{nil, "", " \r\n", true, 123, []string{"PEM"}, map[string]string{"cert": "PEM"}} {
			var c ConnectionConfig
			if err := convutil.Struct(map[string]interface{}{field: value}, &c); err == nil {
				t.Fatalf("accepted %s=%#v", field, value)
			}
		}
	}
	var parsed ConnectionConfig
	if err := convutil.Struct(map[string]interface{}{"mode": "reverse_tcp", "serverTls": f.serverPEM, "clientTls": f.clientCA, "batchReadLen": 32}, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ServerTLS != f.serverPEM || parsed.ClientTLS != f.clientCA || parsed.BatchReadLen != 32 {
		t.Fatal("configuration was not preserved")
	}
	if err := json.Unmarshal([]byte(`{"mode":"reverse_tcp","timeout":2000}`), &parsed); err != nil || parsed.ServerTLS != "" || parsed.Timeout != 2000 {
		t.Fatalf("legacy configuration: %+v %v", parsed, err)
	}
}

func TestReverseTLSLogRedaction(t *testing.T) {
	var output bytes.Buffer
	log := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.DebugLevel))
	c := &ConnectionConfig{Mode: "reverse_tcp", ServerTLS: "PRIVATE-SECRET", ClientTLS: "CA-SECRET"}
	log.Error("connection error", zap.Any("connection", c))
	previousLogger := logger.Logger
	logger.Logger = log
	defer func() { logger.Logger = previousLogger }()
	p := &Plugin{}
	p.initNetworks(config.DeviceConfig{Connections: map[string]interface{}{
		"bad-pem":     map[string]interface{}{"mode": "reverse_tcp", "serverTls": "PRIVATE-SECRET", "identify": map[string]string{"mode": "single_device"}},
		"bad-type":    map[string]interface{}{"mode": "reverse_tcp", "serverTls": map[string]string{"key": "PRIVATE-SECRET"}},
		"client-only": map[string]interface{}{"mode": "reverse_tcp", "clientTls": "CA-SECRET", "identify": map[string]string{"mode": "single_device"}},
	}})
	if len(p.connPool) != 0 {
		t.Fatal("invalid TLS connection initialized")
	}
	if strings.Contains(output.String(), "SECRET") || !strings.Contains(output.String(), `"serverTls":true`) {
		t.Fatalf("TLS log redaction failed: %s", output.String())
	}
}

func TestReverseTLSRejectedConnectionKeepsSession(t *testing.T) {
	f := newReverseTLSFixture(t)
	for _, tt := range []struct {
		name      string
		cert      *tls.Certificate
		plaintext bool
	}{
		{"missing certificate", nil, false},
		{"untrusted certificate", &f.untrusted, false},
		{"expired certificate", &f.expired, false},
		{"plain TCP", nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rc, _, raw, done := testReverseTLSPipe(t, f, identifyModeSingleDevice, true)
			oldServer, oldClient := net.Pipe()
			defer oldClient.Close()
			old := &reverseSession{conn: oldServer}
			rc.mutex.Lock()
			rc.deviceSessions["A"] = old
			rc.connDevices[oldServer] = "A"
			rc.mutex.Unlock()
			_ = raw.SetDeadline(time.Now().Add(2 * time.Second))
			if tt.plaintext {
				_, _ = raw.Write([]byte{0, 1, 0, 0, 0, 6, 1})
			} else {
				cfg := &tls.Config{RootCAs: f.roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}
				if tt.cert != nil {
					cfg.Certificates = []tls.Certificate{*tt.cert}
				}
				client := tls.Client(raw, cfg)
				err := client.Handshake()
				if err == nil {
					var b [1]byte
					_, err = client.Read(b[:])
				}
				if err == nil {
					t.Fatal("invalid TLS client accepted")
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("rejected TLS connection retained")
			}
			current, ok := rc.getSession("A")
			if !ok || current != old || old.closed.Load() || len(rc.pendingConns) != 0 {
				t.Fatal("invalid TLS client replaced the existing session")
			}
		})
	}
}

func TestReverseTLSHandshake(t *testing.T) {
	f := newReverseTLSFixture(t)
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		for _, tt := range []struct {
			name   string
			mutual bool
			cert   *tls.Certificate
			roots  *x509.CertPool
			host   string
			valid  bool
		}{
			{"one way", false, nil, f.roots, "localhost", true},
			{"mutual", true, &f.client, f.roots, "localhost", true},
			{"no client certificate", true, nil, f.roots, "localhost", false},
			{"untrusted client", true, &f.untrusted, f.roots, "localhost", false},
			{"expired client", true, &f.expired, f.roots, "localhost", false},
			{"wrong client usage", true, &f.wrongUsage, f.roots, "localhost", false},
			{"untrusted server", false, nil, x509.NewCertPool(), "localhost", false},
			{"wrong server name", false, nil, f.roots, "other.example", false},
		} {
			t.Run(tls.VersionName(version)+"/"+tt.name, func(t *testing.T) {
				c := &ConnectionConfig{Mode: "reverse_tcp", ServerTLS: f.serverPEM}
				if tt.mutual {
					c.ClientTLS = f.clientCA
				}
				cfg, err := reverseTLSConfig(c)
				if err != nil {
					t.Fatal(err)
				}
				server, rawClient := net.Pipe()
				defer server.Close()
				defer rawClient.Close()
				_ = rawClient.SetDeadline(time.Now().Add(3 * time.Second))
				done := make(chan error, 1)
				go func() {
					conn, err := handshakeReverseTLS(server, cfg, 2*time.Second)
					if err == nil {
						_, err = conn.Write([]byte{1})
					}
					done <- err
				}()
				clientCfg := &tls.Config{RootCAs: tt.roots, ServerName: tt.host, MinVersion: version, MaxVersion: version}
				if tt.cert != nil {
					clientCfg.Certificates = []tls.Certificate{*tt.cert}
				}
				client := tls.Client(rawClient, clientCfg)
				clientErr := client.Handshake()
				if clientErr == nil {
					var b [1]byte
					_, clientErr = io.ReadFull(client, b[:])
				}
				serverErr := <-done
				if tt.valid && (serverErr != nil || clientErr != nil) {
					t.Fatalf("server=%v client=%v", serverErr, clientErr)
				}
				if !tt.valid && serverErr == nil {
					t.Fatal("server accepted invalid handshake")
				}
			})
		}
	}
}

func TestReverseTLSHandshakeTimeout(t *testing.T) {
	f := newReverseTLSFixture(t)
	cfg, err := reverseTLSConfig(&ConnectionConfig{Mode: "reverse_tcp", ServerTLS: f.serverPEM})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	start := time.Now()
	if _, err := handshakeReverseTLS(server, cfg, 30*time.Millisecond); err == nil {
		t.Fatal("silent client did not time out")
	}
	if time.Since(start) > time.Second {
		t.Fatal("TLS handshake timeout was not enforced")
	}
}

func testReverseTLSPipe(t *testing.T, f reverseTLSFixture, mode string, mutual bool) (*reverseConnector, *tls.Conn, net.Conn, <-chan struct{}) {
	t.Helper()
	c := &ConnectionConfig{Mode: "reverse_tcp", ServerTLS: f.serverPEM, Timeout: 1000,
		Identify: &IdentifyConfig{Mode: mode, UnitID: 1, StartAddress: 27, Quantity: 16, Format: "ascii", Timeout: 1000}}
	if mutual {
		c.ClientTLS = f.clientCA
	}
	cfg, err := reverseTLSConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	rc := &reverseConnector{config: c, tlsConfig: cfg, pendingConns: map[net.Conn]struct{}{},
		deviceSessions: map[string]*reverseSession{}, connDevices: map[net.Conn]string{},
		devicesMeta: map[string]config.Device{"A": {ID: "A", Properties: map[string]string{"identifyValue": "PILE-A"}}}}
	if mode == identifyModeSourceIP {
		rc.devicesMeta["A"] = config.Device{ID: "A", Properties: map[string]string{"identifyValue": "192.0.2.1"}}
	}
	server, rawClient := net.Pipe()
	wrapped := remoteAddrConn{Conn: server, remoteAddr: &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1234}}
	rc.trackPendingConn(wrapped)
	done := make(chan struct{})
	go func() { defer close(done); rc.handleIncomingConn(wrapped) }()
	clientCfg := &tls.Config{RootCAs: f.roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}
	if mutual {
		clientCfg.Certificates = []tls.Certificate{f.client}
	}
	client := tls.Client(rawClient, clientCfg)
	t.Cleanup(func() { _ = rawClient.Close(); rc.Close(); <-done })
	return rc, client, rawClient, done
}

func TestReverseTLSIdentificationAndWrites(t *testing.T) {
	f := newReverseTLSFixture(t)
	for _, mode := range []string{identifyModeModbusRegister, identifyModeSourceIP, identifyModeSingleDevice} {
		for _, mutual := range []bool{false, true} {
			name := mode + "/one_way"
			if mutual {
				name = mode + "/mutual"
			}
			t.Run(name, func(t *testing.T) {
				rc, client, raw, done := testReverseTLSPipe(t, f, mode, mutual)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err := client.HandshakeContext(ctx); err != nil {
					t.Fatal(err)
				}
				slave := &stringSlave{}
				slave.set(27, []uint16{0x5049, 0x4c45, 0x2d41})
				slave.set(100, []uint16{0x4142, 0x4344})
				worker := make(chan struct{})
				go func() { defer close(worker); serveStringSlave(t, client, false, slave) }()
				defer func() { _ = raw.Close(); <-worker }()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("TLS identity did not finish")
				}
				session, ok := rc.getSession("A")
				if !ok {
					t.Fatal("TLS identity did not bind")
				}
				secured, ok := session.conn.(*tls.Conn)
				if !ok || !secured.ConnectionState().HandshakeComplete {
					t.Fatal("session is not secured")
				}
				regs, err := session.readRegisters(1, 3, 100, 2)
				if err != nil {
					t.Fatal(err)
				}
				text, err := decodeModbusString(regs, false, false)
				if err != nil || text != "ABCD" {
					t.Fatalf("text=%q err=%v", text, err)
				}
				for _, q := range []uint16{1, 3} {
					values, err := encodeModbusString("XY", q, false, false)
					if err != nil {
						t.Fatal(err)
					}
					if err := rc.Send(command{Mode: plugin.WriteMode, Value: []*writeValue{{DeviceId: "A", unitID: 1, Address: 100, Value: values, RegisterType: HoldingRegister}}}); err != nil {
						t.Fatal(err)
					}
				}
				requests := slave.take()
				if len(requests) < 3 || requests[len(requests)-2].function != 6 || requests[len(requests)-1].function != 16 {
					t.Fatalf("write frames: %+v", requests)
				}
			})
		}
	}
}

func TestReverseTLSClosePendingHandshake(t *testing.T) {
	f := newReverseTLSFixture(t)
	for _, mode := range []string{identifyModeSourceIP, identifyModeSingleDevice} {
		t.Run(mode, func(t *testing.T) {
			rc, _, _, done := testReverseTLSPipe(t, f, mode, false)
			// These modes do no probe I/O, but must still wait for TLS authentication.
			select {
			case <-done:
				t.Fatal("bound a device before TLS handshake")
			case <-time.After(20 * time.Millisecond):
			}
			rc.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("Close did not interrupt pending TLS handshake")
			}
			if len(rc.deviceSessions) != 0 || len(rc.pendingConns) != 0 {
				t.Fatal("pending TLS connection retained or bound")
			}
		})
	}
}
