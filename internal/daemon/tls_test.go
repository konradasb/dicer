// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testCA is a certificate authority for one test, issuing the server and
// client certificates it needs. Certificates are made rather than kept as
// fixtures so that nothing in the tree expires.
type testCA struct {
	t    *testing.T
	dir  string
	cert *x509.Certificate
	key  *ecdsa.PrivateKey

	// File is the authority's certificate, for client_ca_file and for a
	// client's ca_file.
	File string
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "dicer test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	ca := &testCA{t: t, dir: t.TempDir(), cert: cert, key: key}
	ca.File = filepath.Join(ca.dir, "ca.pem")
	writeFile(t, ca.File, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	return ca
}

// issue writes a certificate and its key, named for what it is for, and
// returns the two paths.
func (c *testCA) issue(name string, usage x509.ExtKeyUsage) (certFile, keyFile string) {
	c.t.Helper()

	certFile = filepath.Join(c.dir, name+".pem")
	keyFile = filepath.Join(c.dir, name+"-key.pem")
	c.issueInto(name, usage, certFile, keyFile)

	return certFile, keyFile
}

// issueInto writes a fresh certificate over the given paths, which is what a
// renewal does.
func (c *testCA) issueInto(name string, usage x509.ExtKeyUsage, certFile, keyFile string) {
	c.t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		c.t.Fatal(err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		c.t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	if usage == x509.ExtKeyUsageServerAuth {
		tmpl.DNSNames = []string{"localhost"}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		c.t.Fatal(err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		c.t.Fatal(err)
	}

	writeFile(c.t, certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	writeFile(c.t, keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// serve starts a TLS listener that echoes what it is sent, which is enough
// to drive a handshake and to carry its verdict back, and returns its
// address.
func serve(t *testing.T, cfg *tls.Config) string {
	t.Helper()

	l, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				// Echoed rather than discarded: the client waits for a
				// byte back, and would otherwise wait forever.
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	return l.Addr().String()
}

// dial completes a handshake against addr with the given client material.
func dial(t *testing.T, addr string, ca string, certFile, keyFile string) error {
	t.Helper()

	cfg := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "127.0.0.1"}

	pem, err := os.ReadFile(ca)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	cfg.RootCAs = pool

	if certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: cfg}
	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	// The server verifies the client certificate while completing the
	// handshake, and TLS 1.3 lets the client finish before it has heard
	// the verdict. A round trip is what carries it back: a refusal arrives
	// as an alert on the write or the read.
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if _, err := conn.Write([]byte("x")); err != nil {
		return err
	}
	if _, err := conn.Read(make([]byte, 1)); err != nil {
		return err
	}

	return nil
}

func TestClientCAFileRequiresACertificateItIssued(t *testing.T) {
	ca := newTestCA(t)
	serverCert, serverKey := ca.issue("server", x509.ExtKeyUsageServerAuth)
	clientCert, clientKey := ca.issue("client", x509.ExtKeyUsageClientAuth)

	cfg, err := serverTLSConfig(TLSConfig{
		CertFile:     serverCert,
		KeyFile:      serverKey,
		ClientCAFile: ca.File,
	}, testLogger())
	if err != nil {
		t.Fatalf("serverTLSConfig: %v", err)
	}
	addr := serve(t, cfg)

	if err := dial(t, addr, ca.File, clientCert, clientKey); err != nil {
		t.Errorf("a client the CA signed was refused: %v", err)
	}

	if err := dial(t, addr, ca.File, "", ""); err == nil {
		t.Error("a client with no certificate was let in")
	}

	// A certificate from somewhere else is exactly as good as none.
	other := newTestCA(t)
	strangerCert, strangerKey := other.issue("stranger", x509.ExtKeyUsageClientAuth)
	if err := dial(t, addr, ca.File, strangerCert, strangerKey); err == nil {
		t.Error("a client holding another CA's certificate was let in")
	}
}

func TestWithoutAClientCAFileAnyClientIsServed(t *testing.T) {
	ca := newTestCA(t)
	serverCert, serverKey := ca.issue("server", x509.ExtKeyUsageServerAuth)

	cfg, err := serverTLSConfig(TLSConfig{CertFile: serverCert, KeyFile: serverKey}, testLogger())
	if err != nil {
		t.Fatalf("serverTLSConfig: %v", err)
	}
	if cfg.ClientAuth != tls.NoClientCert {
		t.Errorf("ClientAuth = %v with no client CA, want NoClientCert", cfg.ClientAuth)
	}

	if err := dial(t, serve(t, cfg), ca.File, "", ""); err != nil {
		t.Errorf("a client with no certificate was refused: %v", err)
	}
}

func TestTheServerCertificateStillHasToBeTrusted(t *testing.T) {
	ca := newTestCA(t)
	serverCert, serverKey := ca.issue("server", x509.ExtKeyUsageServerAuth)

	cfg, err := serverTLSConfig(TLSConfig{CertFile: serverCert, KeyFile: serverKey}, testLogger())
	if err != nil {
		t.Fatalf("serverTLSConfig: %v", err)
	}

	// A client that does not know this CA must not connect, or nothing
	// stops something else answering at the address.
	other := newTestCA(t)
	if err := dial(t, serve(t, cfg), other.File, "", ""); err == nil {
		t.Error("a daemon signed by an unknown CA was accepted")
	}
}

func TestARenewedCertificateIsPickedUp(t *testing.T) {
	ca := newTestCA(t)
	certFile, keyFile := ca.issue("server", x509.ExtKeyUsageServerAuth)

	cert, err := newCertificate(certFile, keyFile, testLogger())
	if err != nil {
		t.Fatalf("newCertificate: %v", err)
	}

	first, err := cert.get(nil)
	if err != nil {
		t.Fatal(err)
	}

	// Renewal: the same paths, a new pair. The modification time has to
	// move for the change to be seen, and a test can outrun the clock.
	time.Sleep(10 * time.Millisecond)
	ca.issueInto("server", x509.ExtKeyUsageServerAuth, certFile, keyFile)

	second, err := cert.get(nil)
	if err != nil {
		t.Fatal(err)
	}

	if first.Leaf == nil || second.Leaf == nil {
		t.Fatal("LoadX509KeyPair left no parsed leaf to compare")
	}
	if first.Leaf.SerialNumber.Cmp(second.Leaf.SerialNumber) == 0 {
		t.Error("the renewed certificate was not picked up")
	}
}

func TestAnUnreadableRenewalKeepsTheCertificateInUse(t *testing.T) {
	ca := newTestCA(t)
	certFile, keyFile := ca.issue("server", x509.ExtKeyUsageServerAuth)

	cert, err := newCertificate(certFile, keyFile, testLogger())
	if err != nil {
		t.Fatalf("newCertificate: %v", err)
	}
	before, err := cert.get(nil)
	if err != nil {
		t.Fatal(err)
	}

	// Half-written, as a renewal in progress looks for an instant.
	time.Sleep(10 * time.Millisecond)
	writeFile(t, certFile, []byte("-----BEGIN CERTIFICATE-----\ntruncated"))

	after, err := cert.get(nil)
	if err != nil {
		t.Fatalf("get after a bad write: %v", err)
	}
	if after != before {
		t.Error("a certificate that could not be read replaced the one in use")
	}
}

func TestServerTLSConfigRejectsMaterialItCannotUse(t *testing.T) {
	ca := newTestCA(t)
	serverCert, serverKey := ca.issue("server", x509.ExtKeyUsageServerAuth)

	empty := filepath.Join(t.TempDir(), "empty.pem")
	writeFile(t, empty, []byte("not a certificate"))

	for _, tc := range []struct {
		name string
		cfg  TLSConfig
		want string
	}{
		{
			name: "no certificate file",
			cfg:  TLSConfig{CertFile: filepath.Join(ca.dir, "absent.pem"), KeyFile: serverKey},
			want: "cert_file",
		},
		{
			name: "no key file",
			cfg:  TLSConfig{CertFile: serverCert, KeyFile: filepath.Join(ca.dir, "absent-key.pem")},
			want: "key_file",
		},
		{
			name: "client CA holding no certificates",
			cfg:  TLSConfig{CertFile: serverCert, KeyFile: serverKey, ClientCAFile: empty},
			want: "want PEM",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := serverTLSConfig(tc.cfg, testLogger())
			if err == nil {
				t.Fatal("serverTLSConfig accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestTLSConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     TLSConfig
		wantErr bool
	}{
		{"nothing", TLSConfig{}, false},
		{"a pair", TLSConfig{CertFile: "c", KeyFile: "k"}, false},
		{"a pair and a client CA", TLSConfig{CertFile: "c", KeyFile: "k", ClientCAFile: "ca"}, false},
		{"a certificate alone", TLSConfig{CertFile: "c"}, true},
		{"a key alone", TLSConfig{KeyFile: "k"}, true},
		{"a client CA alone", TLSConfig{ClientCAFile: "ca"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validate()
			if tc.wantErr && err == nil {
				t.Error("validate accepted it")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validate = %v, want no error", err)
			}
		})
	}
}
