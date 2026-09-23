// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"slices"
	"strings"
	"testing"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestRename(t *testing.T) {
	d := newFakeInstanceDaemon(&dicerdv1.Instance{Name: "web", ImageRef: "nginx:1.27", State: stateStopped})
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
