// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
)

func TestPortMappingOverlaps(t *testing.T) {
	tests := []struct {
		name string
		a, b PortMapping
		want bool
	}{
		{"same port", PortMapping{HostPort: 80}, PortMapping{HostPort: 80}, true},
		{"tcp by default", PortMapping{HostPort: 80}, PortMapping{HostPort: 80, Protocol: "tcp"}, true},
		{"other protocol", PortMapping{HostPort: 80}, PortMapping{HostPort: 80, Protocol: "udp"}, false},
		{"other port", PortMapping{HostPort: 80}, PortMapping{HostPort: 81}, false},
		{"every address and one", PortMapping{HostPort: 80}, PortMapping{HostIP: "10.0.0.1", HostPort: 80}, true},
		{
			"same address",
			PortMapping{HostIP: "10.0.0.1", HostPort: 80},
			PortMapping{HostIP: "10.0.0.1", HostPort: 80},
			true,
		},
		{
			"other address",
			PortMapping{HostIP: "10.0.0.1", HostPort: 80},
			PortMapping{HostIP: "10.0.0.2", HostPort: 80},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Overlaps(tt.b); got != tt.want {
				t.Errorf("%s overlaps %s = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			if got := tt.b.Overlaps(tt.a); got != tt.want {
				t.Errorf("%s overlaps %s = %v, want %v", tt.b, tt.a, got, tt.want)
			}
		})
	}
}

func TestValidatePorts(t *testing.T) {
	valid := []PortMapping{
		{HostPort: 8080, GuestPort: 80},
		{HostPort: 8080, GuestPort: 80, Protocol: "udp"},
		{HostIP: "10.0.0.1", HostPort: 443, GuestPort: 443},
		{HostIP: "10.0.0.2", HostPort: 443, GuestPort: 8443},
	}
	if err := validatePorts(valid); err != nil {
		t.Errorf("validatePorts(valid) = %v, want nil", err)
	}

	invalid := map[string][]PortMapping{
		"no host port":  {{GuestPort: 80}},
		"no guest port": {{HostPort: 80}},
		"protocol":      {{HostPort: 80, GuestPort: 80, Protocol: "sctp"}},
		"host IP":       {{HostIP: "not-an-ip", HostPort: 80, GuestPort: 80}},
		"IPv6 host IP":  {{HostIP: "::1", HostPort: 80, GuestPort: 80}},
		"loopback":      {{HostIP: "127.0.0.1", HostPort: 80, GuestPort: 80}},
		"duplicate":     {{HostPort: 80, GuestPort: 80}, {HostPort: 80, GuestPort: 81}},
		"wildcard overlap": {
			{HostIP: "10.0.0.1", HostPort: 80, GuestPort: 80},
			{HostPort: 80, GuestPort: 81},
		},
	}
	for name, ports := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := validatePorts(ports); !errors.Is(err, errdefs.ErrInvalidArgument) {
				t.Errorf("validatePorts(%+v) = %v, want an invalid argument error", ports, err)
			}
		})
	}
}

func TestNetworkValidate(t *testing.T) {
	valid := map[string]Network{
		"plain":                        {},
		"nameservers":                  {Nameservers: []string{"8.8.8.8"}},
		"internal without nameservers": {Internal: true},
	}
	for name, n := range valid {
		t.Run(name, func(t *testing.T) {
			if err := n.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}

	n := Network{Internal: true, Nameservers: []string{"8.8.8.8"}}
	if err := n.Validate(); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("Validate() of an internal network with nameservers = %v, want an invalid argument error", err)
	}
}
