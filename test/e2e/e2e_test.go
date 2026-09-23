// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Environment variables configuring a run. Only the host is required.
const (
	hostEnv      = "DICER_E2E_HOST"
	userEnv      = "DICER_E2E_SSH_USER"
	keyEnv       = "DICER_E2E_SSH_KEY"
	kernelURLEnv = "DICER_E2E_KERNEL_URL"
	keepEnv      = "DICER_E2E_KEEP"
)

// Defaults for a run that only names a host.
const (
	defaultUser = "root"

	// defaultKernelURL is the kernel the installer recommends, so the tests
	// boot what a new user would boot.
	defaultKernelURL = "https://github.com/kernel/linux/releases/download/" +
		"ch-6.12.8-kernel-1.4-202602101/vmlinux-x86_64"

	// testImage is small, boots quickly and has a shell, which is all the
	// guest-side assertions need.
	testImage = "docker.io/library/alpine:3.21"

	// networkName and kernelName are shared by every test: they are
	// provisioned once, are read-only afterwards, and nothing asserts on
	// their contents.
	networkName = "e2e"
	kernelName  = "e2e"

	// subnet is small on purpose. It is private to this daemon's data
	// directory, so it cannot collide with a real network on the host.
	subnet = "172.31.0.0/24"

	// gatewayIP is the bridge's address on that subnet, which is the first
	// assignable one.
	gatewayIP = "172.31.0.1"
)

// paths are where the daemon under test lives on the host. All of them sit
// under a prefix of their own; see the package comment.
type paths struct {
	root    string
	dicer   string
	dicerd  string
	config  string
	socket  string
	dataDir string
	runDir  string
	unit    string

	// metrics is the address the daemon serves its Prometheus endpoint on.
	// Loopback on the host under test, so nothing is exposed by running
	// these tests.
	metrics string

	// api is the address the daemon serves the API on over mutual TLS.
	// Loopback too, for the same reason: the tests reach it from the host.
	api string

	// clientConfig is the configuration directory of the CLI acting as a
	// remote client on the host, apart from root's own.
	clientConfig string
}

func newPaths() paths {
	const root = "/opt/dicer-e2e"

	return paths{
		root:    root,
		dicer:   root + "/dicer",
		dicerd:  root + "/dicerd",
		config:  root + "/config.yaml",
		socket:  "/run/dicer-e2e/dicer.sock",
		dataDir: "/var/lib/dicer-e2e",
		runDir:  "/run/dicer-e2e",
		unit:    "dicer-e2e",
		metrics: "127.0.0.1:9101",

		api:          "127.0.0.1:17443",
		clientConfig: root + "/client",
	}
}

// environment is the host with a daemon running on it. One is built by
// TestMain and shared by every test, because installing a daemon and pulling
// a kernel costs more than the tests do.
type environment struct {
	host  *host
	paths paths
	keep  bool
}

// env is the shared environment. Tests reach it directly: there is exactly
// one host under test, and threading it through every helper would buy
// nothing.
var env *environment

func TestMain(m *testing.M) {
	addr := os.Getenv(hostEnv)
	if addr == "" {
		fmt.Fprintf(os.Stderr, "%s is not set; skipping the end-to-end tests\n", hostEnv)
		os.Exit(0)
	}

	env = &environment{
		host: &host{
			addr: addr,
			user: envOr(userEnv, defaultUser),
			key:  os.Getenv(keyEnv),
		},
		paths: newPaths(),
		keep:  os.Getenv(keepEnv) != "",
	}

	code, err := run(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "end-to-end setup failed: %v\n", err)
		os.Exit(1)
	}

	os.Exit(code)
}

// run sets the host up, runs the tests and tears it down again. It exists so
// that the teardown is deferred rather than duplicated down every path out of
// TestMain, which os.Exit would otherwise skip.
func run(m *testing.M) (code int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if err := env.deploy(ctx); err != nil {
		return 0, err
	}
	defer env.teardown(ctx)

	if err := env.provision(ctx); err != nil {
		return 0, err
	}

	return m.Run(), nil
}

// deploy builds the binaries, installs them on the host and starts the
// daemon.
func (e *environment) deploy(ctx context.Context) error {
	hostArch, err := e.host.arch(ctx)
	if err != nil {
		return err
	}

	binaries, err := build(ctx, hostArch)
	if err != nil {
		return err
	}

	// A previous run that was killed rather than torn down would still be
	// serving on the socket, and its data directory would still hold state.
	e.teardown(ctx)

	if _, err := e.host.run(ctx, "mkdir", "-p", e.paths.root, e.paths.runDir); err != nil {
		return fmt.Errorf("create %s: %w", e.paths.root, err)
	}

	for remote, local := range map[string]string{
		e.paths.dicer:  filepath.Join(binaries, "dicer"),
		e.paths.dicerd: filepath.Join(binaries, "dicerd"),
	} {
		if err := e.host.upload(ctx, local, remote); err != nil {
			return err
		}
	}

	if err := e.writeConfig(ctx); err != nil {
		return err
	}

	return e.startDaemon(ctx)
}

// writeConfig installs the daemon configuration, which points every path at
// this run's own prefix.
func (e *environment) writeConfig(ctx context.Context) error {
	config := fmt.Sprintf(`data_dir: %s
run_dir: %s
api:
  socket:
    path: %s
  tcp:
    listen: %s
log_level: debug
metrics:
  enabled: true
  listen: %s
  path: /metrics
`, e.paths.dataDir, e.paths.runDir, e.paths.socket, e.paths.api, e.paths.metrics)

	// A heredoc keeps the file's content out of the command line, where it
	// would have to survive two levels of shell quoting.
	script := fmt.Sprintf("cat > %s <<'DICER_E2E_EOF'\n%sDICER_E2E_EOF\n", e.paths.config, config)
	if _, err := e.host.runShell(ctx, script); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

// startDaemon runs dicerd as a transient systemd unit and waits for its
// socket.
//
// systemd owns it rather than a backgrounded shell so that its output lands
// in the journal, where a failing test can quote it.
//
// KillMode=process mirrors the unit scripts/install.sh writes, and is not a
// detail: systemd's default kills the whole cgroup, which would take every
// VMM down with the daemon. A harness that left it out would test a
// deployment nobody runs, and would report the recovery path as broken when
// it is the test that is.
func (e *environment) startDaemon(ctx context.Context) error {
	if _, err := e.host.run(ctx,
		"systemd-run", "--unit", e.paths.unit, "--collect",
		"--property", "KillMode=process",
		"--description", "Dicer end-to-end test daemon",
		e.paths.dicerd, "serve", "--config", e.paths.config,
	); err != nil {
		return fmt.Errorf("start the daemon: %w", err)
	}

	return e.waitForDaemon(ctx)
}

// waitForDaemon waits for the API socket to appear.
func (e *environment) waitForDaemon(ctx context.Context) error {
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if _, err := e.host.run(ctx, "test", "-S", e.paths.socket); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}

	return fmt.Errorf("the daemon did not open %s within 30s:\n%s", e.paths.socket, e.journal(ctx))
}

// stopDaemon stops the daemon, leaving its instances running.
func (e *environment) stopDaemon(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), commandTimeout)
	defer cancel()

	if _, err := e.host.run(ctx, "systemctl", "stop", e.paths.unit); err != nil {
		t.Fatalf("stop the daemon: %v", err)
	}

	// Whatever the test does next, the daemon has to be back for the
	// tests after it.
	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()
		if err := e.startStoppedDaemon(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup: start the daemon: %v\n", err)
		}
	})
}

// startDaemonForTest starts a daemon stopped by stopDaemon.
func (e *environment) startDaemonForTest(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), commandTimeout)
	defer cancel()

	if err := e.startStoppedDaemon(ctx); err != nil {
		t.Fatalf("start the daemon: %v", err)
	}
}

// startStoppedDaemon starts the daemon's unit again. With KillMode=process
// a stopped unit stays loaded for as long as a VMM is left in its cgroup, and
// can simply be started; once the last one is gone the transient unit is
// collected and has to be created afresh. Starting one that is already
// running is a no-op.
func (e *environment) startStoppedDaemon(ctx context.Context) error {
	if _, err := e.host.run(ctx, "systemctl", "start", e.paths.unit); err != nil {
		return e.startDaemon(ctx)
	}
	return e.waitForDaemon(ctx)
}

// restartDaemon restarts the daemon and waits for it to serve again. Running
// instances are expected to survive it; that is what recovery is for.
func (e *environment) restartDaemon(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), commandTimeout)
	defer cancel()

	if _, err := e.host.run(ctx, "systemctl", "restart", e.paths.unit); err != nil {
		t.Fatalf("restart the daemon: %v", err)
	}
	if err := e.waitForDaemon(ctx); err != nil {
		t.Fatalf("daemon did not come back: %v", err)
	}
}

// remote is how the CLI names the daemon under test: by its socket's
// address, which needs no remote configured on the host.
func (e *environment) remote() string {
	return "unix://" + e.paths.socket
}

// provision creates the network and kernel every test boots against.
func (e *environment) provision(ctx context.Context) error {
	if _, err := e.host.run(ctx,
		e.paths.dicer, "--remote", e.remote(),
		"network", "create", networkName, "--subnet", subnet,
	); err != nil {
		return fmt.Errorf("create the network: %w", err)
	}

	if _, err := e.host.run(ctx,
		e.paths.dicer, "--remote", e.remote(),
		"kernel", "import", kernelName,
		"--arch", "x86_64", "--url", envOr(kernelURLEnv, defaultKernelURL),
	); err != nil {
		return fmt.Errorf("import the kernel: %w", err)
	}

	return nil
}

// teardown stops the daemon and removes everything this run put on the host.
//
// Failures are reported rather than returned: teardown runs when something
// has already gone wrong as often as not, and the tests' own result is the
// more interesting one.
func (e *environment) teardown(ctx context.Context) {
	if e.keep {
		fmt.Fprintf(os.Stderr, "%s is set; leaving the daemon on %s running\n", keepEnv, e.host.addr)
		return
	}

	// stop leaves nothing behind for --collect to reap, and reset-failed
	// clears a unit that exited badly, either of which would make the next
	// run's systemd-run fail with "unit already exists".
	if _, err := e.host.run(ctx, "systemctl", "stop", e.paths.unit); err != nil {
		// Not an error worth reporting on the common path: the unit does
		// not exist before the first run.
		_, _ = e.host.run(ctx, "systemctl", "reset-failed", e.paths.unit)
	}

	if _, err := e.host.run(ctx, "rm", "-rf", e.paths.root, e.paths.dataDir, e.paths.runDir); err != nil {
		fmt.Fprintf(os.Stderr, "teardown: remove state: %v\n", err)
	}
}

// build compiles the binaries for the host's architecture and returns the
// directory holding them.
//
// It shells out to make because dicerd embeds the guest binaries and the
// hypervisors, and reproducing that here would be a second build system that
// drifts from the first.
func build(ctx context.Context, arch string) (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, "make", "build", "GOARCH="+arch)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=linux")

	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build for %s: %w\n%s", arch, err, out)
	}

	return filepath.Join(root, "bin"), nil
}

// repoRoot returns the repository root, derived from this file's own path so
// that the tests do not care what directory they are run from.
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot locate the test source")
	}

	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), nil
}

// envOr returns an environment variable, or a fallback when it is unset.
func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}

	return fallback
}
