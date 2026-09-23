// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"errors"
	"testing"
)

func TestParsePortMapping(t *testing.T) {
	tests := []struct {
		in   string
		want PortMapping
	}{
		{"8080:80", PortMapping{HostPort: 8080, GuestPort: 80}},
		{"8080:80/udp", PortMapping{HostPort: 8080, GuestPort: 80, Protocol: "udp"}},
		{"10.0.0.1:53:53/udp", PortMapping{HostIP: "10.0.0.1", HostPort: 53, GuestPort: 53, Protocol: "udp"}},
	}
	for _, tt := range tests {
		got, err := ParsePortMapping(tt.in)
		if err != nil {
			t.Errorf("ParsePortMapping(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParsePortMapping(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestParsePortMappingRejectsMalformed(t *testing.T) {
	for _, in := range []string{"", "80", "a:80", "80:b", "0:80", "80:0", "65536:80", "1:2:3:4"} {
		if _, err := ParsePortMapping(in); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("ParsePortMapping(%q) = %v, want an invalid argument error", in, err)
		}
	}
}

func TestPortMappingStringRoundTrips(t *testing.T) {
	for _, in := range []string{"8080:80/tcp", "10.0.0.1:53:53/udp"} {
		p, err := ParsePortMapping(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.String(); got != in {
			t.Errorf("String() = %q, want %q", got, in)
		}
	}
}

func TestPortMappingOverlaps(t *testing.T) {
	tests := []struct {
		a, b PortMapping
		want bool
	}{
		{PortMapping{HostPort: 80}, PortMapping{HostPort: 80}, true},
		{PortMapping{HostPort: 80}, PortMapping{HostPort: 80, Protocol: "tcp"}, true},
		{PortMapping{HostPort: 80}, PortMapping{HostPort: 80, Protocol: "udp"}, false},
		{PortMapping{HostPort: 80}, PortMapping{HostPort: 81}, false},
		{PortMapping{HostPort: 80}, PortMapping{HostIP: "10.0.0.1", HostPort: 80}, true},
		{PortMapping{HostIP: "10.0.0.1", HostPort: 80}, PortMapping{HostIP: "10.0.0.1", HostPort: 80}, true},
		{PortMapping{HostIP: "10.0.0.1", HostPort: 80}, PortMapping{HostIP: "10.0.0.2", HostPort: 80}, false},
	}
	for _, tt := range tests {
		if got := tt.a.Overlaps(tt.b); got != tt.want {
			t.Errorf("%s overlaps %s = %v, want %v", tt.a, tt.b, got, tt.want)
		}
		if got := tt.b.Overlaps(tt.a); got != tt.want {
			t.Errorf("%s overlaps %s = %v, want %v", tt.b, tt.a, got, tt.want)
		}
	}
}

func TestValidatePorts(t *testing.T) {
	valid := []PortMapping{
		{HostPort: 8080, GuestPort: 80},
		{HostPort: 8080, GuestPort: 80, Protocol: "udp"},
		{HostIP: "10.0.0.1", HostPort: 443, GuestPort: 443},
		{HostIP: "10.0.0.2", HostPort: 443, GuestPort: 8443},
	}
	if err := ValidatePorts(valid); err != nil {
		t.Errorf("ValidatePorts(valid) = %v", err)
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
		if err := ValidatePorts(ports); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s: ValidatePorts = %v, want an invalid argument error", name, err)
		}
	}
}
