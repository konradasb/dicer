// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package certificate makes and identifies the self-signed TLS certificates
// the daemon and its clients authenticate each other with.
//
// There is no certificate authority. Each side generates its own key, which
// never leaves the machine it was made on, and the other side trusts it by
// fingerprint -- the SHA-256 of the certificate. That is how SSH host and
// user keys work, and for the same reason: one daemon owns one host, so there
// is no set of machines for a CA to vouch for, only pairs of parties who each
// need to recognise the other.
//
// It follows that a certificate's names and validity dates carry no weight.
// They are filled in because TLS requires them, and are there for a human
// reading one, not for verification.
package certificate

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dicer-sh/dicer/internal/atomicfile"
)

const (
	// certFile and keyFile are where LoadOrGenerate keeps a pair.
	certFile = "cert.pem"
	keyFile  = "key.pem"

	// validity is how long a generated certificate claims to be valid for.
	// Trust is by fingerprint, so this is only as long as TLS
	// implementations are happy to accept.
	validity = 10 * 365 * 24 * time.Hour

	// fingerprintPrefix names the hash a fingerprint is taken with, so that
	// one can be told from another if the hash ever has to change.
	fingerprintPrefix = "sha256:"
)

// Generate creates a key and a self-signed certificate for it, named for
// commonName, and returns both PEM-encoded.
func Generate(commonName string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial number: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Hour), // tolerate a little clock skew
		NotAfter:     now.Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}

	return []byte(EncodePEM(der)), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// LoadOrGenerate returns the key pair kept in dir, generating one named for
// commonName if there is none yet. The key is readable only by its owner.
func LoadOrGenerate(dir, commonName string) (tls.Certificate, error) {
	certPath := filepath.Join(dir, certFile)
	keyPath := filepath.Join(dir, keyFile)

	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err == nil {
		return pair, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return tls.Certificate{}, fmt.Errorf("load key pair from %s: %w", dir, err)
	}

	certPEM, keyPEM, err := Generate(commonName)
	if err != nil {
		return tls.Certificate{}, err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, fmt.Errorf("create %s: %w", dir, err)
	}
	// The key goes first: a certificate without its key is useless, but a
	// key without its certificate is only a pair still being written.
	if err := atomicfile.Write(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := atomicfile.Write(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}

	return tls.X509KeyPair(certPEM, keyPEM)
}

// Fingerprint identifies a certificate by the SHA-256 of its DER encoding,
// as "sha256:" and the hash in hex.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return fingerprintPrefix + hex.EncodeToString(sum[:])
}

// EncodePEM returns a DER-encoded certificate in PEM form.
func EncodePEM(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// ParsePEM parses a certificate written by EncodePEM.
func ParsePEM(s string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM-encoded certificate")
	}

	return x509.ParseCertificate(block.Bytes)
}

// ValidateFingerprint reports whether s is a fingerprint as Fingerprint
// writes one.
func ValidateFingerprint(s string) error {
	hash, ok := strings.CutPrefix(s, fingerprintPrefix)
	if !ok {
		return fmt.Errorf("fingerprint %q does not begin with %q", s, fingerprintPrefix)
	}
	if b, err := hex.DecodeString(hash); err != nil || len(b) != sha256.Size {
		return fmt.Errorf("fingerprint %q is not a hex-encoded SHA-256 hash", s)
	}

	return nil
}

// FingerprintOf returns the fingerprint of a key pair's own certificate.
func FingerprintOf(pair tls.Certificate) (string, error) {
	if len(pair.Certificate) == 0 {
		return "", errors.New("key pair has no certificate")
	}

	return Fingerprint(pair.Certificate[0]), nil
}

// VerifyPinned returns a tls.Config.VerifyConnection that accepts the peer
// only if its certificate has the given fingerprint. It is what a client uses
// in place of chain verification, which has no CA to verify against. Unlike
// VerifyPeerCertificate, it also runs on resumed sessions.
func VerifyPinned(fingerprint string) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("peer presented no certificate")
		}
		if got := Fingerprint(cs.PeerCertificates[0].Raw); got != fingerprint {
			return fmt.Errorf("the daemon presented certificate %s, not the pinned %s: "+
				"either something is impersonating it, or its key was replaced -- "+
				"check the fingerprint 'dicer info' shows on the host, and if the key "+
				"was replaced on purpose, re-create the remote from a new token", got, fingerprint)
		}

		return nil
	}
}
