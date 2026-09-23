// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dicer-sh/dicer"
)

func TestParseEnv(t *testing.T) {
	t.Setenv("DICER_TEST_FROM_SHELL", "shell-value")

	file := filepath.Join(t.TempDir(), "app.env")
	if err := os.WriteFile(file, []byte(
		"# comment\n\nFROM_FILE=file\nOVERRIDDEN=file\nDICER_TEST_FROM_SHELL\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := parseEnv([]string{
		"OVERRIDDEN=flag",
		"LIST=a,b,c",       // commas belong to the value
		"EQUALS=x=y",       // so does any = after the first
		"EMPTY=",           // an empty value is still a value
		"DICER_TEST_UNSET", // a KEY the shell does not have is left out
	}, []string{file})
	if err != nil {
		t.Fatalf("parseEnv: %v", err)
	}

	want := map[string]string{
		"FROM_FILE":             "file",
		"OVERRIDDEN":            "flag",
		"DICER_TEST_FROM_SHELL": "shell-value",
		"LIST":                  "a,b,c",
		"EQUALS":                "x=y",
		"EMPTY":                 "",
	}
	if !maps.Equal(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}

	for _, bad := range []string{"=value", "BAD KEY=1"} {
		if _, err := parseEnv([]string{bad}, nil); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}

	badFile := filepath.Join(t.TempDir(), "bad.env")
	if err := os.WriteFile(badFile, []byte("OK=1\n=oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseEnv(nil, []string{badFile}); err == nil || !strings.Contains(err.Error(), "bad.env:2") {
		t.Errorf("a bad line should be reported by file and line, got %v", err)
	}
}

func TestParseLabels(t *testing.T) {
	got, err := parseLabels([]string{"team=web", "canary", "expr=a=b"})
	if err != nil {
		t.Fatalf("parseLabels: %v", err)
	}
	if want := map[string]string{"team": "web", "canary": "", "expr": "a=b"}; !maps.Equal(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}
	if _, err := parseLabels([]string{"=x"}); err == nil {
		t.Error("a label with no key should be refused")
	}
}

func TestBuildCreateRequestEnvAndLabels(t *testing.T) {
	got, err := runBuild(t, "web", "-e", "A=1,2", "--env", "B=3", "-l", "team=web")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}
	if want := map[string]string{"A": "1,2", "B": "3"}; !maps.Equal(got.Env, want) {
		t.Errorf("env = %v, want %v", got.Env, want)
	}
	if want := map[string]string{"team": "web"}; !maps.Equal(got.Labels, want) {
		t.Errorf("labels = %v, want %v", got.Labels, want)
	}
}

func TestGenerateName(t *testing.T) {
	for _, tc := range []struct{ ref, prefix string }{
		{"nginx", "nginx-"},
		{"docker.io/library/nginx:1.27", "nginx-"},
		{"localhost:5000/team/api-server:v2", "api-server-"},
		{"ghcr.io/acme/Web_App@sha256:0123", "web-app-"},
		{"___", "instance-"},
	} {
		got := generateName(tc.ref)
		if !strings.HasPrefix(got, tc.prefix) || len(got) != len(tc.prefix)+4 {
			t.Errorf("generateName(%q) = %q, want %s and four characters", tc.ref, got, tc.prefix)
		}
	}

	if a, b := generateName("nginx"), generateName("nginx"); a == b {
		t.Errorf("two names for the same image are both %q", a)
	}
}

func TestWantTTY(t *testing.T) {
	for _, tc := range []struct {
		force, never, stdin, stdout, want bool
	}{
		{want: false},
		{stdin: true, stdout: true, want: true},
		// Output piped away: a TTY would mangle it.
		{stdin: true, stdout: false, want: false},
		{force: true, want: true},
		{never: true, stdin: true, stdout: true, want: false},
	} {
		if got := wantTTY(tc.force, tc.never, tc.stdin, tc.stdout); got != tc.want {
			t.Errorf("wantTTY(%+v) = %v", tc, got)
		}
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"sh", "-c", "echo 'hi' $HOME", ""})
	if want := `sh -c 'echo '\''hi'\'' $HOME' ''`; got != want {
		t.Errorf("shellJoin = %s, want %s", got, want)
	}
}

func TestInstanceFilters(t *testing.T) {
	inst := dicer.Instance{
		Spec: dicer.InstanceSpec{
			Name: "web-1", ImageRef: "nginx:1.27", NetworkName: "default",
			Labels: map[string]string{"team": "web"},
		},
		Status: dicer.InstanceStatus{State: dicer.StateRunning},
	}

	for _, tc := range []struct {
		filters []string
		want    bool
	}{
		{nil, true},
		{[]string{"name=web"}, true},
		{[]string{"state=RUNNING"}, true},
		{[]string{"state=stopped"}, false},
		{[]string{"state=stopped", "state=running"}, true},  // any value of one key
		{[]string{"state=running", "network=other"}, false}, // every key
		{[]string{"label=team"}, true},
		{[]string{"label=team=data"}, false},
		{[]string{"image=nginx"}, true},
	} {
		f, err := parseInstanceFilters(tc.filters)
		if err != nil {
			t.Fatalf("parseInstanceFilters(%q): %v", tc.filters, err)
		}
		if got := len(f.apply([]dicer.Instance{inst})) == 1; got != tc.want {
			t.Errorf("filters %q match = %v, want %v", tc.filters, got, tc.want)
		}
	}

	for _, bad := range []string{"state", "state=", "colour=red"} {
		if _, err := parseInstanceFilters([]string{bad}); err == nil {
			t.Errorf("filter %q should be refused", bad)
		}
	}
}
