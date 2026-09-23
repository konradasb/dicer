// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"slices"
	"strings"
	"testing"
)

func TestJoinTokenRoundTrip(t *testing.T) {
	token := JoinToken{
		Name:        "laptop",
		Addresses:   []string{"192.0.2.1:7443", "dicer.example:7443"},
		Fingerprint: testFingerprint,
		Secret:      "s3cr3t",
	}

	encoded := token.String()
	if !strings.HasPrefix(encoded, joinTokenPrefix) {
		t.Errorf("encoded token = %q, want the %q prefix", encoded, joinTokenPrefix)
	}

	got, err := ParseJoinToken(encoded)
	if err != nil {
		t.Fatalf("ParseJoinToken: %v", err)
	}
	if got.Name != token.Name || got.Fingerprint != token.Fingerprint || got.Secret != token.Secret {
		t.Errorf("token = %+v, want %+v", got, token)
	}
	if !slices.Equal(got.Addresses, token.Addresses) {
		t.Errorf("addresses = %v, want %v", got.Addresses, token.Addresses)
	}

	// Copied from one terminal to another, a token picks up whitespace.
	if _, err := ParseJoinToken("  " + encoded + "\n"); err != nil {
		t.Errorf("ParseJoinToken with whitespace: %v", err)
	}
}

func TestParseJoinTokenRejects(t *testing.T) {
	valid := JoinToken{
		Name: "laptop", Addresses: []string{"192.0.2.1:7443"},
		Fingerprint: testFingerprint, Secret: "s",
	}

	for _, tc := range []struct {
		name  string
		input string
	}{
		{"no prefix", strings.TrimPrefix(valid.String(), joinTokenPrefix)},
		{"not base64", joinTokenPrefix + "!!!"},
		{"not a token at all", "hello"},
		{"no name", JoinToken{Addresses: valid.Addresses, Fingerprint: testFingerprint, Secret: "s"}.String()},
		{"no addresses", JoinToken{Name: "laptop", Fingerprint: testFingerprint, Secret: "s"}.String()},
		{"no secret", JoinToken{Name: "laptop", Addresses: valid.Addresses, Fingerprint: testFingerprint}.String()},
		{"bad fingerprint", JoinToken{
			Name: "laptop", Addresses: valid.Addresses, Fingerprint: "sha256:00", Secret: "s",
		}.String()},
	} {
		if _, err := ParseJoinToken(tc.input); err == nil {
			t.Errorf("%s: ParseJoinToken succeeded", tc.name)
		}
	}
}
