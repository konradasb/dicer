// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestCopyInAndOutOfAnInstance copies a directory into a running guest and
// back out again, all on the host: through the CLI, the daemon's relay, the
// vsock and the guest agent, which no unit test runs together.
func TestCopyInAndOutOfAnInstance(t *testing.T) {
	name := instanceName(t)
	env.createInstance(t, name)
	env.startInstance(t, name)
	env.waitForAgent(t, name)

	work := env.paths.root + "/copy-" + name
	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()
		_, _ = env.host.run(ctx, "rm", "-rf", work)
	})
	if _, err := env.host.runShell(t.Context(),
		"mkdir -p "+work+"/app/bin && printf 'hello\\n' > "+work+"/app/bin/run && chmod 755 "+work+"/app/bin/run"); err != nil {
		t.Fatal(err)
	}

	// In, into an existing directory.
	env.dicer(t, "instance", "cp", work+"/app", name+":/tmp")
	if out := env.exec(t, name, "cat", "/tmp/app/bin/run"); strings.TrimSpace(out) != "hello" {
		t.Errorf("guest file = %q, want hello", out)
	}
	if out := env.exec(t, name, "stat", "-c", "%a", "/tmp/app/bin/run"); strings.TrimSpace(out) != "755" {
		t.Errorf("guest file mode = %q, want 755", out)
	}

	// Out, to a new path.
	env.exec(t, name, "sh", "-c", "echo changed > /tmp/app/bin/run")
	env.dicer(t, "instance", "cp", name+":/tmp/app", work+"/back")
	out, err := env.host.run(t.Context(), "cat", work+"/back/bin/run")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "changed" {
		t.Errorf("copied-out file = %q, want the guest's change", out)
	}
}
