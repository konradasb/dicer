// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package network

import (
	"strings"
	"testing"
)

func TestTAPName(t *testing.T) {
	a, b := TAPName("instance-a"), TAPName("instance-b")

	if a != TAPName("instance-a") {
		t.Error("TAPName is not deterministic")
	}
	if a == b {
		t.Errorf("TAPName collides for distinct IDs: %s", a)
	}
	if len(a) > maxInterfaceName || !strings.HasPrefix(a, "tap-") {
		t.Errorf("TAPName = %q, want tap- prefix and at most %d bytes", a, maxInterfaceName)
	}
}

func TestBridgeName(t *testing.T) {
	tests := []struct {
		network string
		want    string
	}{
		{"default", "dicer-default"},
		{"123456789", "dicer-123456789"},
	}
	for _, tt := range tests {
		if got := BridgeName(tt.network); got != tt.want {
			t.Errorf("BridgeName(%q) = %q, want %q", tt.network, got, tt.want)
		}
	}
}

func TestBridgeNameLongNamesDoNotCollide(t *testing.T) {
	a, b := BridgeName("production-a"), BridgeName("production-b")

	if a == b {
		t.Fatalf("BridgeName collides for distinct networks: %s", a)
	}
	for _, name := range []string{a, b} {
		if len(name) > maxInterfaceName {
			t.Errorf("BridgeName = %q, longer than %d bytes", name, maxInterfaceName)
		}
	}
}
