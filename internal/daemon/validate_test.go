// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// runValidate runs dicerd validate on path and returns what it printed.
func runValidate(t *testing.T, path string) (string, error) {
	t.Helper()

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"validate", "--config", path})

	err := cmd.Execute()
	return out.String(), err
}

func TestValidateAcceptsAGoodConfig(t *testing.T) {
	path := writeConfig(t, "log_level: debug\n")

	out, err := runValidate(t, path)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(out, "is valid") {
		t.Errorf("output = %q, want it to say the file is valid", out)
	}
}

// A typo is the mistake this is for: it names the key and where it is.
func TestValidateNamesAnUnknownKey(t *testing.T) {
	path := writeConfig(t, "log_level: info\nlog_levle: debug\n")

	_, err := runValidate(t, path)
	if err == nil {
		t.Fatal("validate accepted an unknown key")
	}
	if !strings.Contains(err.Error(), "log_levle") || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error = %q, want the key and its line", err)
	}
}

func TestValidateRejectsAnInvalidValue(t *testing.T) {
	if _, err := runValidate(t, writeConfig(t, "log_level: verbose\n")); err == nil {
		t.Error("validate accepted an invalid log level")
	}
}

// The daemon only reads the TLS files as it starts; validate reads them now.
func TestValidateReadsTheTLSFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.pem")
	path := writeConfig(t, "api:\n  tcp:\n    listen: 127.0.0.1:7443\n    tls:\n"+
		"      cert_file: "+missing+"\n      key_file: "+missing+"\n")

	if _, err := runValidate(t, path); err == nil {
		t.Error("validate accepted TLS files that do not exist")
	}
}

func TestValidateAMissingFileIsTheDefaults(t *testing.T) {
	out, err := runValidate(t, filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(out, "defaults") {
		t.Errorf("output = %q, want it to say the defaults apply", out)
	}
}
