// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"slices"
	"strings"
	"testing"

	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

func TestRename(t *testing.T) {
	d := newFakeInstanceDaemon(&dicerdv1.Instance{Name: "web", ImageRef: "nginx:1.27", State: "Stopped"})
	serveInstanceDaemon(t, d)

	out, err := run(t, "rename", "web", "web-2")
	if err != nil {
		t.Fatalf("rename: %v\n%s", err, out)
	}
	if !strings.Contains(out, "renamed to web-2") {
		t.Errorf("rename said %q", out)
	}
	if !slices.Contains(d.calls, "rename web web-2") {
		t.Errorf("calls = %v, want the rename", d.calls)
	}

	// It answers to its new name afterwards, and not to its old one.
	if out, err := run(t, "inspect", "web-2"); err != nil {
		t.Errorf("inspect after rename: %v\n%s", err, out)
	}
	if _, err := run(t, "inspect", "web"); err == nil {
		t.Error("the old name still resolves")
	}
}

// A running instance is refused: its name is where its files are.
func TestRenameRefusesARunningInstance(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(
		&dicerdv1.Instance{Name: "web", ImageRef: "nginx:1.27", State: "Running"}))

	out, code := runErr(t, "rename", "web", "web-2")
	if code == 0 {
		t.Fatalf("renaming a running instance succeeded:\n%s", out)
	}
	if !strings.Contains(out, "running") {
		t.Errorf("error = %q, want it to say the instance is running", out)
	}
}

func TestRenameOntoATakenName(t *testing.T) {
	serveInstanceDaemon(t, newFakeInstanceDaemon(
		&dicerdv1.Instance{Name: "web", ImageRef: "nginx:1.27", State: "Stopped"},
		&dicerdv1.Instance{Name: "api", ImageRef: "nginx:1.27", State: "Stopped"}))

	out, code := runErr(t, "rename", "web", "api")
	if code == 0 {
		t.Fatalf("renaming onto a taken name succeeded:\n%s", out)
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("error = %q, want it to say the name is taken", out)
	}
}

func TestRenameNeedsTwoNames(t *testing.T) {
	out, code := runErr(t, "rename", "web")
	if code == 0 {
		t.Fatalf("rename with one argument succeeded:\n%s", out)
	}
	if !strings.Contains(out, "a new name") {
		t.Errorf("usage error = %q, want it to ask for a new name", out)
	}
}
