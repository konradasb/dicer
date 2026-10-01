// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package hostnet

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestIsFirewalldStartIsFirewalldTakingItsName(t *testing.T) {
	nameOwnerChanged := "org.freedesktop.DBus.NameOwnerChanged"
	tests := []struct {
		name   string
		signal *dbus.Signal
		want   bool
	}{
		{"firewalld starts", &dbus.Signal{Name: nameOwnerChanged, Body: []any{firewalldName, "", ":1.42"}}, true},
		{"firewalld stops", &dbus.Signal{Name: nameOwnerChanged, Body: []any{firewalldName, ":1.42", ""}}, false},
		{"another name", &dbus.Signal{Name: nameOwnerChanged, Body: []any{"org.example.Other", "", ":1.43"}}, false},
		{"another signal", &dbus.Signal{Name: firewalldName + ".Reloaded"}, false},
		{"a malformed body", &dbus.Signal{Name: nameOwnerChanged, Body: []any{firewalldName}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFirewalldStart(tt.signal); got != tt.want {
				t.Errorf("isFirewalldStart = %v, want %v", got, tt.want)
			}
		})
	}
}
