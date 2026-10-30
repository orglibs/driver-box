package internal

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"go.uber.org/zap/zapcore"
)

const reverseTLSHandshakeTimeout = 5 * time.Second

// UnmarshalJSON distinguishes an omitted TLS field from an explicitly empty or
// null value. Invalid TLS configuration must never silently fall back to TCP.
func (c *ConnectionConfig) UnmarshalJSON(data []byte) error {
	type plain ConnectionConfig
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name, raw := range fields {
		if !strings.EqualFold(name, "serverTls") && !strings.EqualFold(name, "clientTls") {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
			return errors.New("serverTls and clientTls, when provided, must be non-empty PEM strings")
		}
	}
	var parsed plain
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*c = ConnectionConfig(parsed)
	return nil
}

// MarshalLogObject deliberately excludes PEM data, particularly the server key.
func (c *ConnectionConfig) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddString("connectionKey", c.ConnectionKey)
	enc.AddString("mode", c.Mode)
	enc.AddString("address", c.Address)
	enc.AddBool("serverTls", c.ServerTLS != "")
	enc.AddBool("clientTls", c.ClientTLS != "")
	return nil
}

func reverseTLSConfig(c *ConnectionConfig) (*tls.Config, error) {
	if c.ServerTLS == "" && c.ClientTLS == "" {
		return nil, nil
	}
	if c.Mode != "reverse_tcp" {
		return nil, errors.New("serverTls and clientTls are only supported in reverse_tcp mode")
	}
	if c.ServerTLS == "" {
		return nil, errors.New("clientTls requires serverTls")
	}
	// The same PEM contains both the certificate chain and its matching key.
	pem := []byte(c.ServerTLS)
	pair, err := tls.X509KeyPair(pem, pem)
	if err != nil {
		// Do not include input or parser details that could expose private data.
		return nil, errors.New("invalid serverTls: expected a PEM certificate chain and matching unencrypted private key")
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{pair},
	}
	if c.ClientTLS != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(c.ClientTLS)) {
			return nil, errors.New("invalid clientTls: expected trusted client CA certificates in PEM format")
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

func handshakeReverseTLS(conn net.Conn, cfg *tls.Config, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	secured := tls.Server(conn, cfg)
	if err := secured.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("TLS handshake failed: %w", err)
	}
	return secured, nil
}
