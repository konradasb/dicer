// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// composeFile is a project of three services: one others wait on to be
// healthy, a job they wait on to finish, and an app that waits on both. It
// joins the network and boots the kernel the other tests share, rather than
// making its own, so that it needs nothing the host does not already have.
const composeFile = `
services:
  db:
    image: %[1]s
    kernel: %[2]s
    disk: 1GiB
    command: sh -c "touch /tmp/ready; exec sleep infinity"
    healthcheck:
      test: test -f /tmp/ready
      interval: 1s
  migrate:
    image: %[1]s
    kernel: %[2]s
    disk: 1GiB
    command: sh -c "exit 0"
  app:
    image: %[1]s
    kernel: %[2]s
    disk: 1GiB
    command: sleep infinity
    environment:
      GREETING: %[3]s
    depends_on:
      db: {condition: service_healthy}
      migrate: {condition: service_completed_successfully}
networks:
  default:
    name: %[4]s
    external: true
`

// TestComposeProjectUpChangeAndDown brings a project up, waiting on its
// dependencies' conditions against real guests; changes one service and
// checks only it is recreated; and takes it down again.
func TestComposeProjectUpChangeAndDown(t *testing.T) {
	project := instanceName(t)
	dir := env.paths.root + "/compose/" + project

	env.writeComposeFile(t, dir, "hello")
	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()
		if _, err := env.runCompose(ctx, dir, "down"); err != nil {
			t.Logf("cleanup: dicer compose down: %v", err)
		}
	})

	if out := env.compose(t, dir, "up", "-d", "--wait"); !strings.Contains(out, "Every instance is ready") {
		t.Errorf("up -d --wait did not say the instances are ready:\n%s", out)
	}

	db := env.instance(t, project+"-db")
	if db.State != "Running" || db.Labels["dicer.compose.service"] != "db" {
		t.Errorf("db = %+v, want it running and labelled as the project's", db)
	}
	if db.Health.Status != "healthy" {
		t.Errorf("db's health = %q, want healthy: app waited on it", db.Health.Status)
	}
	if got := env.instance(t, project+"-migrate"); got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("migrate = %+v, want it to have exited 0", got)
	}
	if got := strings.TrimSpace(env.exec(t, project+"-app", "sh", "-c", "echo $GREETING")); got != "hello" {
		t.Errorf("app's GREETING = %q, want the file's", got)
	}

	// app finds db by its service's name, its hostname, from the network's
	// DNS.
	if out, err := env.tryExec(t, project+"-app", "nslookup", "db"); err != nil || !strings.Contains(out, db.IP) {
		t.Errorf("nslookup db from app = %v, want %s:\n%s", err, db.IP, out)
	}

	// A changed service is recreated; an unchanged one is left running.
	env.writeComposeFile(t, dir, "goodbye")
	appBefore := env.instance(t, project+"-app")
	env.compose(t, dir, "up", "-d", "--wait")

	if got := env.instance(t, project+"-db"); got.ID != db.ID {
		t.Errorf("db was recreated (%s, was %s), though it did not change", got.ID, db.ID)
	}
	if got := env.instance(t, project+"-app"); got.ID == appBefore.ID {
		t.Error("app was not recreated, though its environment changed")
	}
	if got := strings.TrimSpace(env.exec(t, project+"-app", "sh", "-c", "echo $GREETING")); got != "goodbye" {
		t.Errorf("app's GREETING = %q, want the changed file's", got)
	}

	if ps := env.compose(t, dir, "ps", "-q"); strings.Fields(ps)[0] != project+"-app" || len(strings.Fields(ps)) != 3 {
		t.Errorf("ps -q = %q, want the project's three instances", ps)
	}

	env.compose(t, dir, "down")
	for _, service := range []string{"app", "db", "migrate"} {
		if _, err := env.tryDicer(t, "instance", "show", project+"-"+service); !isNotFound(err) {
			t.Errorf("instance %s-%s is still there after down: %v", project, service, err)
		}
	}
	// The shared network was external, so down left it.
	env.dicer(t, "network", "show", networkName)
}

// writeComposeFile writes the project's file into dir on the host, with
// app's greeting.
func (e *environment) writeComposeFile(t *testing.T, dir, greeting string) {
	t.Helper()

	ctx, cancel := commandContext(t)
	defer cancel()

	content := fmt.Sprintf(composeFile, testImage, kernelName, greeting, networkName)
	script := fmt.Sprintf("mkdir -p %s && cat > %s/dicer-compose.yaml <<'EOF'\n%sEOF", dir, dir, content)
	if _, err := e.host.runShell(ctx, script); err != nil {
		t.Fatalf("write the compose file: %v", err)
	}
}

// compose runs dicer compose on the project in dir, failing the test if it
// does not succeed.
func (e *environment) compose(t *testing.T, dir string, args ...string) string {
	t.Helper()

	ctx, cancel := commandContext(t)
	defer cancel()

	out, err := e.runCompose(ctx, dir, args...)
	if err != nil {
		t.Fatalf("dicer compose %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// runCompose runs dicer compose against the daemon under test, on the
// project in dir.
func (e *environment) runCompose(ctx context.Context, dir string, args ...string) (string, error) {
	argv := append([]string{
		e.paths.dicer, "--remote", e.remote(), "compose", "-f", dir + "/dicer-compose.yaml",
	}, args...)
	return e.host.run(ctx, argv...)
}
