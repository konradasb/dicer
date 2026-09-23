// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build e2e

package e2e

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestInstanceLifecycle boots a container image as a virtual machine, runs a
// command inside it and stops it again.
//
// This is the path everything else rests on: the image is pulled and
// converted to an EROFS root filesystem, a kernel is fetched, a TAP device is
// attached to the bridge, the hypervisor boots the guest, dicer-init brings
// userspace up, and the agent answers over vsock. A failure anywhere in that
// chain fails here, which is the point -- none of it can be tested without
// KVM, and all of it is what users see first.
func TestInstanceLifecycle(t *testing.T) {
	name := instanceName(t)

	// Spelled out rather than using createInstance, because the flags a user
	// would type are part of what this test is checking.
	env.dicer(t, "instance", "create", name,
		"--image", testImage,
		"--kernel", kernelName,
		"--network", networkName,
		"--vcpus", "1",
		"--memory", "512MiB",
		"--disk", "1GiB",
	)
	t.Cleanup(func() { env.deleteInstance(t, name) })

	// Creating an instance records a definition and boots nothing, which is
	// a promise the README makes explicitly.
	if created := env.instance(t, name); created.State != "Stopped" {
		t.Errorf("a freshly created instance is %q, want %q", created.State, "Stopped")
	}

	env.dicer(t, "instance", "start", name)

	running := env.waitForState(t, name, "Running")
	if running.IP == "" || running.IP == "-" {
		t.Errorf("a running instance has no address: %+v", running)
	}
	if !strings.HasPrefix(running.IP, "172.31.0.") {
		t.Errorf("address %q is not from the %s subnet", running.IP, subnet)
	}

	// Reaching the guest proves the whole chain, not just that a hypervisor
	// process exists: userspace is up and the agent is answering on vsock.
	// What the guest reports as its hostname is TestInstanceHostname's.
	env.exec(t, name, "true")

	// The root filesystem is the image's, not an empty disk.
	if out := env.exec(t, name, "cat", "/etc/alpine-release"); strings.TrimSpace(out) == "" {
		t.Error("the guest is not running the image's root filesystem: /etc/alpine-release is empty")
	}

	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")
}

// TestInstanceHypervisors boots the same image under every VMM the daemon
// carries.
//
// The two are meant to be interchangeable for the things Dicer promises of
// both -- boot, exec, networking -- and the abstraction in internal/hypervisor
// exists to keep them that way. Nothing but a real boot proves it holds.
func TestInstanceHypervisors(t *testing.T) {
	for _, hypervisor := range []string{"cloud-hypervisor", "firecracker"} {
		t.Run(hypervisor, func(t *testing.T) {
			name := instanceName(t)

			env.createInstance(t, name, "--hypervisor-type", hypervisor)
			running := env.startInstance(t, name)

			if running.IP == "" || running.IP == "-" {
				t.Errorf("a guest under %s has no address: %+v", hypervisor, running)
			}

			// Reaching userspace proves the boot went all the way through,
			// not just that a VMM process started.
			if out := env.exec(t, name, "cat", "/etc/alpine-release"); strings.TrimSpace(out) == "" {
				t.Errorf("the guest under %s did not boot its root filesystem", hypervisor)
			}

			env.dicer(t, "instance", "stop", name)
			env.waitForState(t, name, "Stopped")
		})
	}
}

// TestInstanceHostname checks that an instance's name reaches the guest
// kernel, not just /etc/hostname: a guest booted without an init system --
// the common case -- would otherwise report the name its kernel was compiled
// with.
func TestInstanceHostname(t *testing.T) {
	t.Run("defaults to the instance name", func(t *testing.T) {
		name := instanceName(t)

		env.createInstance(t, name)
		env.startInstance(t, name)

		if got := strings.TrimSpace(env.exec(t, name, "hostname")); got != name {
			t.Errorf("hostname = %q, want the instance name %q", got, name)
		}
	})

	t.Run("honours --hostname", func(t *testing.T) {
		const custom = "chosen-by-the-user"

		name := instanceName(t)

		env.createInstance(t, name, "--hostname", custom)
		env.startInstance(t, name)

		if got := strings.TrimSpace(env.exec(t, name, "hostname")); got != custom {
			t.Errorf("hostname = %q, want %q", got, custom)
		}

		// The file is what an init system would read on a reboot, so it has
		// to agree with the kernel.
		if got := strings.TrimSpace(env.exec(t, name, "cat", "/etc/hostname")); got != custom {
			t.Errorf("/etc/hostname = %q, want %q", got, custom)
		}
	})
}

// TestInstanceExecSurvivesAStopStartCycle boots a guest, stops it and boots it
// again.
//
// dicer-init installs its agent into the overlay disk on boot. An instance
// whose entrypoint is a plain shell ignores the ACPI shutdown, so stopping it
// kills the VMM outright: an agent that was not flushed to disk, or that is
// not reinstalled over a truncated copy, leaves exec broken for the life of
// the overlay disk.
func TestInstanceExecSurvivesAStopStartCycle(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name)
	env.startInstance(t, name)

	// Reaching the guest once puts the agent in the overlay disk, which is
	// what the second boot has to cope with.
	env.exec(t, name, "true")

	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")

	env.startInstance(t, name)

	if out := env.exec(t, name, "cat", "/etc/alpine-release"); strings.TrimSpace(out) == "" {
		t.Error("the guest is unreachable after a stop and start")
	}
}

// TestInstanceStopKeepsWrites checks that what a guest wrote survives a stop,
// even if it never synced it.
//
// A write sits in the guest's page cache until the kernel flushes it. Ending
// the VMM before that happens loses it, so a stop has to have the guest flush
// its filesystems first -- whatever its PID 1 does with a shutdown request.
func TestInstanceStopKeepsWrites(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name)
	env.startInstance(t, name)
	env.waitForAgent(t, name)

	env.exec(t, name, "sh", "-c", "echo kept > /root/marker")

	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")
	env.startInstance(t, name)
	env.waitForAgent(t, name)

	if out, _ := env.tryExec(t, name, "cat", "/root/marker"); strings.TrimSpace(out) != "kept" {
		t.Errorf("marker = %q after a stop and start, want %q", strings.TrimSpace(out), "kept")
	}
}

// TestInstanceHostFiles checks the path a credential takes from the host into
// a running guest.
//
// Dicer stores nothing: the file is read from the host at every start and
// handed over on the config disk. That makes the interesting property not
// "the bytes arrived" but "the bytes are re-read", which the second half of
// this test covers by editing the file between boots.
func TestInstanceHostFiles(t *testing.T) {
	var (
		name     = instanceName(t)
		hostPath = "/tmp/" + name + "-secret"
		first    = "first-value"
		second   = "rotated-value"
	)

	env.writeHostFile(t, hostPath, first)

	env.createInstance(t, name, "--host-file", "token="+hostPath)
	env.startInstance(t, name)

	if got := strings.TrimSpace(env.exec(t, name, "cat", "/run/secrets/token")); got != first {
		t.Fatalf("/run/secrets/token = %q, want %q", got, first)
	}

	// The README promises the file is read at every start, so whatever
	// manages it on the host stays in charge of it.
	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")

	env.writeHostFile(t, hostPath, second)
	env.startInstance(t, name)

	if got := strings.TrimSpace(env.exec(t, name, "cat", "/run/secrets/token")); got != second {
		t.Errorf("/run/secrets/token = %q after the host file changed, want %q", got, second)
	}
}

// TestInstanceStartOnBoot checks that an instance whose restart policy asks
// to be running comes up when the daemon does, and that one without does not.
//
// Starting on boot runs after the API is serving, so the daemon being up is
// not evidence that it has finished; the wait below is for the instance, not
// for the socket.
func TestInstanceStartOnBoot(t *testing.T) {
	var (
		auto   = instanceName(t) + "-auto"
		manual = instanceName(t) + "-manual"
	)

	env.createInstance(t, auto, "--restart", "always")
	env.createInstance(t, manual)

	// Neither is running yet: creating an instance records a definition.
	env.waitForState(t, auto, "Stopped")
	env.waitForState(t, manual, "Stopped")

	env.restartDaemon(t)

	env.waitForState(t, auto, "Running")

	if got := env.instance(t, manual); got.State != "Stopped" {
		t.Errorf("instance without a restart policy is %q after a daemon restart, want %q",
			got.State, "Stopped")
	}

	env.dicer(t, "instance", "stop", auto)
	env.waitForState(t, auto, "Stopped")
}

// TestInstanceHypervisorCrashIsReported kills a running guest's hypervisor
// behind the daemon's back.
//
// A VMM can die without being asked to -- the OOM killer, a crash, an
// operator's kill -9 -- and the daemon has to notice when it happens, not the
// next time someone touches the instance: until then it would report a dead
// VM as running, refuse to start it, and keep its TAP device up.
func TestInstanceHypervisorCrashIsReported(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name)
	running := env.startInstance(t, name)
	env.waitForAgent(t, name)
	tap := env.tapOf(t, running.IP)

	env.killVMM(t, name)

	reason := env.waitForFailure(t, name)
	if !strings.Contains(reason, "exited unexpectedly") || !strings.Contains(reason, "signal: killed") {
		t.Errorf("failure reason = %q, want an unexpected exit by SIGKILL", reason)
	}

	// Host resources go with the VMM.
	if env.linkExists(t, tap) {
		t.Errorf("TAP device %s is still up after its hypervisor died", tap)
	}

	// And a failed instance starts again without being stopped first.
	env.startInstance(t, name)
	env.exec(t, name, "true")

	env.dicer(t, "instance", "stop", name)
	env.waitForState(t, name, "Stopped")
}

// TestInstanceRestartPolicy runs a workload that exits 3 under on-failure:1.
//
// It covers the whole way an end travels: dicer-init reporting the exit code
// on the status disk and ending the VM, the daemon reading it once the VMM
// is gone, the policy restarting the instance once, and giving up the second
// time with the exit code as the reason.
func TestInstanceRestartPolicy(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name, "--restart", "on-failure:1", "--", "sh", "-c", "sleep 1; exit 3")
	env.dicer(t, "instance", "start", name)

	reason := env.waitForFailure(t, name)
	if !strings.Contains(reason, "gave up after 1 restart") || !strings.Contains(reason, "exit code 3") {
		t.Errorf("failure reason = %q, want a give-up after one restart, for exit code 3", reason)
	}

	v := env.instance(t, name)
	if v.RestartCount != 1 || v.ExitCode == nil || *v.ExitCode != 3 {
		t.Errorf("restart count %d, exit code %v; want 1 restart and exit code 3", v.RestartCount, v.ExitCode)
	}
}

// TestInstanceHealthCheck runs a health check that fails while a file exists
// in the guest.
//
// It covers the probe's whole way: the agent running it inside the guest,
// the daemon's monitor adding the results up, and the verdict reaching the
// API. With no restart policy, an unhealthy instance is left running.
func TestInstanceHealthCheck(t *testing.T) {
	name := instanceName(t)

	env.createInstance(t, name,
		"--health-cmd", "test ! -e /tmp/sick", "--health-interval", "1s", "--health-retries", "2",
		"--", "sleep", "3600")
	env.startInstance(t, name)
	env.waitForAgent(t, name)

	env.waitForHealth(t, name, "healthy")

	env.exec(t, name, "touch", "/tmp/sick")
	v := env.waitForHealth(t, name, "unhealthy")
	if v.Health.FailingStreak < 2 || !strings.Contains(v.Health.LastOutput, "exited with code 1") {
		t.Errorf("health = %+v, want two failures and the command's exit", v.Health)
	}
	if v.State != "Running" {
		t.Errorf("state = %s, want an unhealthy instance with no restart policy left running", v.State)
	}

	env.exec(t, name, "rm", "/tmp/sick")
	env.waitForHealth(t, name, "healthy")
}

// waitForHealth polls until an instance's health check has found want.
func (e *environment) waitForHealth(t *testing.T, name, want string) instanceView {
	t.Helper()

	return e.waitForInstance(t, name, "health "+strconv.Quote(want), func(v instanceView) bool {
		return v.Health.Status == want
	})
}

// instanceRecord is what `dicer instance show --format json` prints: the
// daemon's whole record of an instance, spec and status apart, under the
// API's own field names.
//
// Only the fields the tests assert on are named. Decoding the record shape
// rather than a flat one is not optional: every field here is nested, so a
// flat struct decodes to its zero value and every assertion sees an empty
// state rather than a failure.
type instanceRecord struct {
	Spec struct {
		Name string `json:"name"`
	} `json:"spec"`

	Status struct {
		State        string `json:"state"`
		StateError   string `json:"state_error"`
		IP           string `json:"ip"`
		ExitCode     *int   `json:"exit_code"`
		RestartCount int    `json:"restart_count"`

		// Health is a value rather than a pointer so that an instance
		// with no check reads as the zero health instead of panicking.
		Health struct {
			Status        string `json:"status"`
			FailingStreak int    `json:"failing_streak"`
			LastOutput    string `json:"last_output"`
		} `json:"health"`
	} `json:"status"`
}

// instanceView is the flat view of a record the assertions are written
// against: what an instance is called and how it is doing, without the
// nesting the wire needs.
type instanceView struct {
	Name         string
	State        string
	StateError   string
	IP           string
	ExitCode     *int
	RestartCount int
	Health       struct {
		Status        string
		FailingStreak int
		LastOutput    string
	}
}

// view flattens a record into what the tests assert on.
func (r instanceRecord) view() instanceView {
	v := instanceView{
		Name:         r.Spec.Name,
		State:        r.Status.State,
		StateError:   r.Status.StateError,
		IP:           r.Status.IP,
		ExitCode:     r.Status.ExitCode,
		RestartCount: r.Status.RestartCount,
	}
	v.Health.Status = r.Status.Health.Status
	v.Health.FailingStreak = r.Status.Health.FailingStreak
	v.Health.LastOutput = r.Status.Health.LastOutput

	return v
}

// instanceName returns a name unique to this test, so that a run which fails
// before its cleanup cannot collide with the next one.
func instanceName(t *testing.T) string {
	t.Helper()

	// Instance names are validated, so the test name has to be reduced to
	// something that passes: lower case, no underscores.
	name := strings.ToLower(t.Name())
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, "/", "-")

	return "e2e-" + name
}

// instance reads an instance's current state through the CLI.
func (e *environment) instance(t *testing.T, name string) instanceView {
	t.Helper()

	out := e.dicer(t, "instance", "show", name, "--format", "json")

	records := rows[instanceRecord](t, out, "instance "+name)
	if len(records) != 1 {
		t.Fatalf("instance show %s returned %d rows, want 1:\n%s", name, len(records), out)
	}

	return records[0].view()
}

// createInstance defines an instance with the defaults these tests share, and
// removes it when the test ends. Extra flags are appended, so a test can
// override a default or add one of its own.
func (e *environment) createInstance(t *testing.T, name string, extra ...string) {
	t.Helper()

	args := append([]string{
		"instance", "create", name,
		"--image", testImage,
		"--kernel", kernelName,
		"--network", networkName,
		"--vcpus", "1",
		"--memory", "512MiB",
		"--disk", "1GiB",
	}, extra...)

	e.dicer(t, args...)
	t.Cleanup(func() { e.deleteInstance(t, name) })
}

// startInstance starts an instance and waits for it to be running.
func (e *environment) startInstance(t *testing.T, name string) instanceView {
	t.Helper()

	e.dicer(t, "instance", "start", name)

	return e.waitForState(t, name, "Running")
}

// waitForState polls until an instance reaches the wanted state.
//
// Most operations are synchronous -- `instance start` returns once the guest
// is running -- so this is for the transitions nothing blocks on, and for
// making a failure report the state it was stuck in rather than timing out
// with no explanation.
func (e *environment) waitForState(t *testing.T, name, want string) instanceView {
	t.Helper()

	return e.waitForInstance(t, name, strconv.Quote(want), func(v instanceView) bool {
		return v.State == want
	})
}

// waitForFailure polls until an instance is Failed, and returns why.
func (e *environment) waitForFailure(t *testing.T, name string) string {
	t.Helper()

	v := e.waitForInstance(t, name, "Failed", func(v instanceView) bool {
		return v.State == "Failed"
	})
	return v.StateError
}

// waitForInstance polls until an instance satisfies done, which want
// describes for the failure message.
func (e *environment) waitForInstance(
	t *testing.T, name, want string, done func(instanceView) bool,
) instanceView {
	t.Helper()

	var last instanceView
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); {
		last = e.instance(t, name)
		if done(last) {
			return last
		}
		time.Sleep(time.Second)
	}

	t.Fatalf("instance %s is %q after 2m, want %s", name, last.State, want)
	return last
}

// vmmPID returns the PID of an instance's hypervisor, as the daemon recorded
// it. The CLI does not print it, so it is read from the host: the definition
// maps the name to the ID, and the runtime state under that ID holds the PID.
func (e *environment) vmmPID(t *testing.T, name string) int {
	t.Helper()

	ctx, cancel := commandContext(t)
	defer cancel()

	script := fmt.Sprintf(
		`id=$(sed -n 's/^id: *//p' %s/instances/%s/config.yaml | tr -d "\"'") && `+
			`sed -n 's/.*"hypervisor_pid": *\([0-9]*\).*/\1/p' %s/instances/"$id"/state.json`,
		e.paths.dataDir, name, e.paths.runDir)

	out, err := e.host.runShell(ctx, script)
	if err != nil {
		t.Fatalf("read the hypervisor PID of %s: %v", name, err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("instance %s has no recorded hypervisor PID (got %q)", name, out)
	}

	return pid
}

// killVMM kills an instance's hypervisor with SIGKILL, from outside the
// daemon -- the way an operator, the OOM killer or a VMM bug would end it.
func (e *environment) killVMM(t *testing.T, name string) int {
	t.Helper()

	pid := e.vmmPID(t, name)

	ctx, cancel := commandContext(t)
	defer cancel()

	if _, err := e.host.run(ctx, "kill", "-9", strconv.Itoa(pid)); err != nil {
		t.Fatalf("kill the hypervisor of %s: %v", name, err)
	}

	return pid
}

// processAlive reports whether a PID is running on the host.
func (e *environment) processAlive(t *testing.T, pid int) bool {
	t.Helper()

	ctx, cancel := commandContext(t)
	defer cancel()

	_, err := e.host.run(ctx, "kill", "-0", strconv.Itoa(pid))
	return err == nil
}

// linkExists reports whether a network interface exists on the host.
func (e *environment) linkExists(t *testing.T, name string) bool {
	t.Helper()

	ctx, cancel := commandContext(t)
	defer cancel()

	_, err := e.host.run(ctx, "ip", "link", "show", name)
	return err == nil
}

// tapOf returns the TAP device an instance's address is attached through.
func (e *environment) tapOf(t *testing.T, ip string) string {
	t.Helper()

	for _, a := range e.allocations(t, networkName) {
		if a.IP == ip {
			return a.TAP
		}
	}

	t.Fatalf("no allocation for %s on %s", ip, networkName)
	return ""
}

// waitForAgent blocks until the guest's agent answers.
//
// `instance start` returns once the VM is running, which is before the guest
// kernel has finished booting userspace. Waiting here, rather than retrying
// whatever a test wanted to run, keeps the two apart: once this returns, a
// failing command has failed on its own merits.
func (e *environment) waitForAgent(t *testing.T, name string) {
	t.Helper()

	var lastErr error
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); {
		_, err := e.tryDicer(t, "instance", "exec", name, "--", "true")
		if err == nil {
			return
		}

		lastErr = err
		time.Sleep(2 * time.Second)
	}

	t.Fatalf("the agent in %s never answered: %v", name, lastErr)
}

// exec runs a command inside a guest and returns its output, failing the test
// if the command does not succeed.
func (e *environment) exec(t *testing.T, name string, command ...string) string {
	t.Helper()

	out, err := e.tryExec(t, name, command...)
	if err != nil {
		t.Fatalf("exec %s in %s: %v", strings.Join(command, " "), name, err)
	}

	return out
}

// tryExec runs a command inside a guest and returns its error rather than
// failing, for tests asserting on what the command itself does.
func (e *environment) tryExec(t *testing.T, name string, command ...string) (string, error) {
	t.Helper()

	e.waitForAgent(t, name)

	return e.tryDicer(t, append([]string{"instance", "exec", name, "--"}, command...)...)
}

// deleteInstance removes an instance, and only logs a failure so that it can
// be used for cleanup after a test has already failed.
func (e *environment) deleteInstance(t *testing.T, name string) {
	t.Helper()

	ctx, cancel := cleanupContext()
	defer cancel()

	if _, err := e.runDicer(ctx, "instance", "delete", name, "--force"); err != nil && !isNotFound(err) {
		t.Logf("cleanup: delete instance %s: %v", name, err)
	}
}

// writeHostFile puts a file on the host under test and removes it afterwards.
// It lives here because injecting host files into a guest is the only thing
// these tests need one for.
func (e *environment) writeHostFile(t *testing.T, path, content string) {
	t.Helper()

	ctx, cancel := commandContext(t)
	defer cancel()

	if _, err := e.host.run(ctx, "sh", "-c", "printf '%s' "+quoteCommand([]string{content})+" > "+path); err != nil {
		t.Fatalf("write %s on the host: %v", path, err)
	}

	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()

		if _, err := e.host.run(ctx, "rm", "-f", path); err != nil {
			t.Logf("cleanup: remove %s: %v", path, err)
		}
	})
}
