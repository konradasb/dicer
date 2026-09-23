// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package certificate

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrGenerateKeepsItsIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")

	first, err := LoadOrGenerate(dir, "dicerd")
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}
	second, err := LoadOrGenerate(dir, "dicerd")
	if err != nil {
		t.Fatalf("LoadOrGenerate again: %v", err)
	}

	// Clients pin the fingerprint, so a restart that made a new key would
	// lock every one of them out.
	a, _ := FingerprintOf(first)
	b, _ := FingerprintOf(second)
	if a != b {
		t.Errorf("fingerprint changed between loads: %s, then %s", a, b)
	}

	info, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("key mode = %o, want 600", mode)
	}
}

func TestFingerprintIsValid(t *testing.T) {
	pair, err := LoadOrGenerate(t.TempDir(), "client")
	if err != nil {
		t.Fatal(err)
	}

	fingerprint, err := FingerprintOf(pair)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateFingerprint(fingerprint); err != nil {
		t.Errorf("ValidateFingerprint(%q) = %v", fingerprint, err)
	}
}

func TestValidateFingerprintRejects(t *testing.T) {
	for _, s := range []string{
		"",
		"md5:0011",
		"sha256:not-hex",
		"sha256:00", // too short
	} {
		if err := ValidateFingerprint(s); err == nil {
			t.Errorf("ValidateFingerprint(%q) = nil, want an error", s)
		}
	}
}

func TestVerifyPinned(t *testing.T) {
	pinned, err := LoadOrGenerate(t.TempDir(), "server")
	if err != nil {
		t.Fatal(err)
	}
	other, err := LoadOrGenerate(t.TempDir(), "impostor")
	if err != nil {
		t.Fatal(err)
	}

	fingerprint, _ := FingerprintOf(pinned)
	verify := VerifyPinned(fingerprint)

	if err := verify(peer(t, pinned)); err != nil {
		t.Errorf("pinned certificate rejected: %v", err)
	}
	if err := verify(peer(t, other)); err == nil {
		t.Error("a different certificate was accepted")
	}
	if err := verify(tls.ConnectionState{}); err == nil {
		t.Error("no certificate was accepted")
	}
}

// peer returns the connection state of a handshake with pair's owner.
func peer(t *testing.T, pair tls.Certificate) tls.ConnectionState {
	t.Helper()
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
}
