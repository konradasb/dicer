// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// serverTLSConfig builds the TLS configuration for the network listener.
// With a client CA, callers without a valid certificate fail the handshake.
func serverTLSConfig(cfg TLSConfig, logger *slog.Logger) (*tls.Config, error) {
	cert, err := newCertificate(cfg.CertFile, cfg.KeyFile, logger)
	if err != nil {
		return nil, err
	}

	out := &tls.Config{
		MinVersion: tls.VersionTLS13,

		// GetCertificate picks up a renewed certificate without a restart.
		GetCertificate: cert.get,
	}

	if !cfg.RequiresClientCert() {
		return out, nil
	}

	pem, err := os.ReadFile(cfg.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read api.tcp.tls.client_ca_file: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("api.tcp.tls.client_ca_file %s holds no certificates; want PEM", cfg.ClientCAFile)
	}

	out.ClientCAs = pool
	out.ClientAuth = tls.RequireAndVerifyClientCert

	return out, nil
}

// certificate is the daemon's certificate, reloaded when its files change.
type certificate struct {
	certFile, keyFile string
	logger            *slog.Logger

	mu       sync.Mutex
	cert     *tls.Certificate
	modCert  time.Time
	modKey   time.Time
	complain time.Time
}

// newCertificate loads the certificate.
func newCertificate(certFile, keyFile string, logger *slog.Logger) (*certificate, error) {
	c := &certificate{certFile: certFile, keyFile: keyFile, logger: logger}
	if err := c.load(); err != nil {
		return nil, err
	}

	return c, nil
}

// get returns the certificate to present, reloading it first if the files
// have changed. It is tls.Config's GetCertificate.
func (c *certificate) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.changed() {
		return c.cert, nil
	}

	if err := c.load(); err != nil {
		// Keep serving the previous certificate; the renewal may be half
		// written. Logging is rate-limited since every handshake gets here.
		if time.Since(c.complain) > certComplainInterval {
			c.complain = time.Now()
			c.logger.Error("the API certificate changed but could not be read; still serving the previous one",
				"cert_file", c.certFile, "error", err)
		}
	}

	return c.cert, nil
}

// certComplainInterval bounds how often a failing reload is logged.
const certComplainInterval = time.Minute

// load reads the pair and records its modification times. The caller holds
// the lock, except during construction.
func (c *certificate) load() error {
	certInfo, err := os.Stat(c.certFile)
	if err != nil {
		return fmt.Errorf("read api.tcp.tls.cert_file: %w", err)
	}
	keyInfo, err := os.Stat(c.keyFile)
	if err != nil {
		return fmt.Errorf("read api.tcp.tls.key_file: %w", err)
	}

	pair, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return fmt.Errorf("load the API certificate: %w", err)
	}

	c.cert, c.modCert, c.modKey = &pair, certInfo.ModTime(), keyInfo.ModTime()

	return nil
}

// changed reports whether either file has been modified since it was read. A
// file that cannot be stat'ed counts as unchanged.
func (c *certificate) changed() bool {
	certInfo, err := os.Stat(c.certFile)
	if err != nil {
		return false
	}
	keyInfo, err := os.Stat(c.keyFile)
	if err != nil {
		return false
	}

	return !certInfo.ModTime().Equal(c.modCert) || !keyInfo.ModTime().Equal(c.modKey)
}
