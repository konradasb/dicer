// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"testing"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestEnumNamesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		value dicerdv1.RestartMode
		name  string
	}{
		{dicerdv1.RestartMode_RESTART_MODE_NO, "no"},
		{dicerdv1.RestartMode_RESTART_MODE_ON_FAILURE, "on-failure"},
		{dicerdv1.RestartMode_RESTART_MODE_UNLESS_STOPPED, "unless-stopped"},
		{dicerdv1.RestartMode_RESTART_MODE_UNSPECIFIED, ""},
	} {
		if got := enumName(tc.value); got != tc.name {
			t.Errorf("enumName(%v) = %q, want %q", tc.value, got, tc.name)
		}
		if tc.name == "" {
			continue
		}
		if got, err := parseEnum[dicerdv1.RestartMode]("restart policy", tc.name); err != nil || got != tc.value {
			t.Errorf("parseEnum(%q) = %v, %v; want %v", tc.name, got, err, tc.value)
		}
	}

	if got := enumName(dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR); got != "cloud-hypervisor" {
		t.Errorf("enumName = %q, want cloud-hypervisor", got)
	}
}

func TestParseEnumRejects(t *testing.T) {
	for _, s := range []string{"", "unspecified", "sometimes"} {
		if _, err := parseEnum[dicerdv1.RestartMode]("restart policy", s); err == nil {
			t.Errorf("parseEnum(%q) should fail", s)
		}
	}
	want := []string{"no", "on-failure", "unless-stopped", "always"}
	if got := enumNames[dicerdv1.RestartMode](); len(got) != len(want) {
		t.Errorf("enumNames = %v, want %v", got, want)
	}
}
