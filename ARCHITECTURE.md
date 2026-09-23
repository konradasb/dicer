# Architecture

How Dicer's moving parts fit together, for someone about to change them. The
[README](README.md) says what Dicer does; [DEVELOPMENT.md](DEVELOPMENT.md) says
how to build and test it and where the code lives. This document explains the
mechanisms that span several packages, which no single file makes obvious.

- [How the guest boots](#how-the-guest-boots)
- [How an instance ends, and is restarted](#how-an-instance-ends-and-is-restarted)
- [Health checks](#health-checks)
- [Image garbage collection](#image-garbage-collection)
- [Events](#events)

## How the guest boots

`dicer-init` is the kernel's init: the machine's PID 1, from the initramfs. It
mounts the image read-only under a writable overlay, reads the config disk,
sets up the network, volumes and `/run/secrets`, installs the guest agent,
and then starts the workload in one of two modes.

| Mode | PID 1 of the machine | The workload | The agent |
|---|---|---|---|
| `exec` | `dicer-init` | PID 1 of its own PID namespace | a child of `dicer-init`, outside the namespace |
| `systemd` | systemd, `exec`ed by `dicer-init` | systemd's units | a unit injected into the image |

### exec: PID 1 in a namespace

The workload runs as a container runs it, as PID 1 of a PID namespace of its
own. That's one flag, `CLONE_NEWPID` on `SysProcAttr`
(`internal/guest/boot/exec.go`), and it's what makes s6-overlay's `/init`
work: it refuses to run unless `getpid()` is 1, and inside the namespace it
is. tini, dumb-init and runit get the PID 1 they expect too, with no
detection needed, and orphans in the namespace are reaped by the
workload's own init, as Docker users expect.

`dicer-init` stays PID 1 outside the namespace and keeps its jobs: the agent,
the exit code on the status disk, and ending the VM. When the namespace's
PID 1 exits, the kernel ends everything in the namespace. An init's shutdown
that signals every process it can see (s6's `kill -1`) can't reach the
agent, because the agent isn't in the namespace.

The workload shares the machine's `/proc`. It sees itself as PID 1, but
`/proc` lists the machine's PIDs. A fresh `/proc` would need a mount
namespace and a helper process to mount it before the `exec`, and nothing
has needed it yet.

**The console.** s6-linux-init takes the terminal over, and that hangs up
every file already open on it. `dicer-init`'s log therefore reopens the
console when a write fails (`console` in `boot/cmd.go`). Otherwise
everything it says after the workload starts, including why the guest
ended, would be lost.

### Choosing the mode

The host doesn't guess. It passes the instance's `init_mode`, `auto` by
default, and `dicer-init` resolves `auto` once the root filesystem is
mounted (`resolveMode` in `boot/mode.go`). Only then can it see what the
command is:

1. The command is the one that will run, `Config.Argv`: the instance's
   command if set, else the image's entrypoint and cmd, else `/sbin/init`.
2. A bare name is looked up on the guest's `PATH`, inside its root.
3. Symlinks are followed with `securejoin`, inside the guest's root. A link
   that points out of the root is followed from the root, as the guest
   would follow it, never into the initramfs.
4. It's systemd if the result is an executable `.../systemd/systemd`.

So Debian's `/sbin/init` is systemd, through two symlinks under merged
`/usr`. Alpine's and OpenRC's aren't. A replaced command (`-- bash`) runs as
itself. `--init-mode` overrides the decision when detection is wrong.

### systemd: handing PID 1 over

systemd must be the machine's PID 1, so `dicer-init` `exec`s it after
injecting three units: the agent, a oneshot that copies the injected files
into `/run/secrets` (systemd mounts a fresh `/run` over the one `dicer-init`
prepared), and `dicer-exit.service`, which reports exit code 0 as the
machine powers off.

### Graceful stop

`stopVMM` (in `internal/vm/supervise.go`) asks before it ends anything. The
agent's `Shutdown` RPC sends `guest.ShutdownSignal` (`SIGRTMIN+4`) to PID 1.
That's systemd's own "power off" signal, and `dicer-init` answers it the same
way, by forwarding SIGTERM to the workload. The agent doesn't need to know
which of the two is PID 1.

- **exec:** the workload gets SIGTERM, stops, and exits. `dicer-init`
  reports the code and halts, and the VMM exits.
- **systemd:** it powers off, and `dicer-exit.service` reports 0.

The host waits `stopGracePeriod` (10s, as `docker stop` does) for the VMM to
exit. After that, or when the guest can't be asked (it's paused, or its
agent predates `Shutdown`), it falls back to what a stop always did: flush
the disks, shut the VMM down, and kill it if it overstays. A forced delete
skips the grace period, as `docker rm -f` does. A stop for a failed health
check doesn't, since a workload that is merely slow may still be able to
save its state.

## How an instance ends, and is restarted

An instance can end without anyone asking it to: its workload exits, its guest
kernel panics or reboots, or its hypervisor (VMM) dies. The daemon has to
notice each of these, work out which one it was, and apply the instance's
restart policy.

Two facts make this harder than it looks:

- **Only the guest knows why it ended.** From the host, a VMM that exits looks
  the same whether its guest meant to go or crashed. Firecracker exits cleanly
  on a guest reset, and a guest reset is what a kernel panic turns into.
- **The host often can't read the VMM's exit status either.** VMMs outlive
  `dicerd` by design, and one adopted from a previous daemon isn't this
  daemon's child, so its exit status is gone (`process.ErrExitStatusUnknown`).

So the guest reports how it ended, somewhere the host can read after the VMM
has gone, and it doesn't matter whether `dicerd` was running at the time.

### The pieces

| Piece | Where | Role |
|---|---|---|
| Status disk | `internal/guest/status.go` | The guest→host report: a raw 4 KiB disk holding one JSON record |
| `dicer-init` | `internal/guest/boot/status.go`, `exec.go` | Counts boots, reports the exit code, ends the VM |
| `dicer-agent report-exit` | `internal/guest/agent/cmd.go` | Reports a power-off for a guest that runs systemd |
| `Starter.PowerOffEndsVM` | `internal/hypervisor` | Which way of halting makes a given VMM exit |
| Supervision | `internal/vm/supervise.go` | Watches each VMM, handles an exit, owns restart timers |
| Exit classification | `internal/vm/exit.go` | Turns the report and VMM status into an `Exit` |
| Restart policy | `internal/vm/restart.go` | Pure decision: restart or not, and after how long |
| Recovery | `internal/vm/recover.go` | Applies the same logic to ends missed while `dicerd` was down |

### The status disk

Every guest gets a fourth disk after the image, the overlay and the config
disk: `/dev/vdd` in the guest, `<rundir>/instances/<id>/status.img` on the
host. It has no filesystem. It holds a `guest.Status` encoded as JSON and
padded with zero bytes to 4 KiB:

```json
{"boots":1,"exit_code":3}
```

It's written at these points, and at no others:

| When | Who | Writes |
|---|---|---|
| Before a start | host (`writeGuestDisks`) | all zeroes: nothing has booted |
| Before a snapshot restore | host | `{"boots":1}`: the restored guest already booted once |
| Each boot, before anything else | `dicer-init` (`checkBoot`) | `boots` + 1 |
| The entrypoint exits (exec mode) | `dicer-init` (`reportExit`) | the exit code |
| systemd powers the guest off | `dicer-exit.service` → `dicer-agent report-exit` | exit code 0 |

Writes from the guest use `O_SYNC` and an fsync, because the machine may end
the moment the write returns. The host reads the disk only after the VMM has
exited, so the two sides never write it at the same time.

Volumes used to start at `vdd`. They now start at `vde`, and `buildInitConfig`
in `internal/vm/start.go` is what keeps the device names in step with the
order of `vmSpec`'s disks.

### Ending the VM from inside

When the workload exits, `dicer-init` (PID 1) reports the code, syncs, and
ends the machine with `reboot(2)`. It doesn't simply exit. Before, it waited
on the guest agent, which never exits, so a VM whose workload had died stayed
"Up" indefinitely.

Which `reboot(2)` command ends the VMM depends on the hypervisor:

| | Power off | Reset |
|---|---|---|
| Cloud Hypervisor | VMM exits | guest reboots **in place**, VMM keeps running |
| Firecracker | guest halts (no ACPI), VMM keeps running | VMM exits (`reboot=k`) |

The host tells the guest which to use. `Starter.PowerOffEndsVM()` becomes
`guest.Config.Halt` (`poweroff` or `reset`) on the config disk. That keeps
`internal/hypervisor` from importing `internal/guest`.

A kernel panic can't choose. With `panic=1 reboot=k` on the command line it
always resets, which on Cloud Hypervisor boots the guest again inside the same
VMM, and the host would never find out. The boot count catches this. The host
zeroes the disk before each start, so if `dicer-init` finds `boots` already at
1, this is a second boot of the same VMM, which can only follow a reset. It
records `boots: 2` and halts at once. Every way a guest can end therefore ends
its VMM, and that's the only event the host has to watch.

### From VMM exit to decision

```
VMM exits
   │  watcher goroutine (supervise)
   ▼
handleExit ── handle no longer registered? ──▶ ignore (a stop/delete asked for it)
   │
   ▼
readExit(status disk, p.Err())  ──▶  Exit{Code, Failure}
   │
   ▼
ended(inst, prev runtime, exit)
   │  release network, decide, write runtime
   ├──▶ Stopped      (clean end, policy says no restart)
   ├──▶ Failed       (failure, no restart, or retries used up)
   └──▶ Restarting   (timer scheduled at NextRestartAt)
                         │  timer fires
                         ▼
                      restart: admit + boot
                         ├──▶ Running
                         └──▶ boot failed → ended(failedExit) → decide again
```

An exit the daemon asked for (stop, delete, restore) never gets this far. The
operation deregisters the VMM handle under the instance lock before the VMM
goes, so the watcher finds a different handle, or none, and does nothing. That
rule already existed, and restart policies rely on it.

**Classification** (`classifyExit`) is deliberately strict: an end is clean
only if the guest said so.

| Status disk | VMM exit | `Exit` |
|---|---|---|
| `exit_code: 0` | anything | clean |
| `exit_code: N ≠ 0` | anything | failure: exit code N |
| `boots: 2`, no code | anything | failure: guest reset (kernel panic or reboot) |
| no code | unknown (adopted VMM) | failure: hypervisor exited, no exit code reported |
| no code | error (signal, non-zero) | failure: hypervisor exited unexpectedly |
| no code | clean | failure: guest ended, no exit code reported |

The last row matters. A Firecracker guest that panics resets, and Firecracker
then exits cleanly. Treating a clean VMM exit as a clean end would pass a
crash off as a deliberate stop.

**The decision** (`decide` in `internal/vm/restart.go`) is a pure function of the policy,
the `Exit`, the restarts in a row so far, and how long the last run lasted. It
has no clock or I/O of its own, so it's tested exhaustively on its own.

| Mode | Restarts after | Starts with the daemon |
|---|---|---|
| `no` (default) | never | no |
| `on-failure[:N]` | a failure, up to N in a row | no |
| `unless-stopped` | any end | unless `StoppedByUser` |
| `always` | any end | yes |

The backoff is `1s · 2^restarts`, capped at 5 minutes. A run of at least 10
minutes resets the count, so a crash after a long run starts again at one
second and doesn't use up the retry limit.

The policy is read from the definition as it is when the instance ends, not as
it was when it started. That's why `UpdateInstance` accepts a change to the
restart policy alone while an instance is running (`onlyRestartPolicyDiffers`
in `update.go`).

### Lifecycle states

`Restarting` is a state of its own. An instance in it holds no CPU, memory or
network, and its runtime record carries `NextRestartAt`.

| From | Event | To |
|---|---|---|
| Running, Paused | the guest ended cleanly, and the policy does not restart | Stopped |
| Running, Paused | the guest ended in failure, and the policy does not restart (or gave up) | Failed |
| Running, Paused | the guest ended, and the policy restarts | Restarting |
| Restarting | the timer fires and admission passes | Starting → Running |
| Starting (a restart) | the boot fails, and the policy restarts again | Restarting |
| Starting (a restart) | the boot fails, and the retries are used up | Failed |
| Restarting | a user stops it | Stopping → Stopped |
| Restarting | a user starts it | Starting (now, with the count reset) |

The runtime record (`internal/vm/runtime.go`, on the `/run` tmpfs) gains
`ExitCode`, `FinishedAt`, `RestartCount` and `NextRestartAt`. `RestartCount`
is carried from one run to the next, including through admission, which moves
Restarting to Starting in place. A start by a user resets it to zero.

`StoppedByUser` isn't runtime state. It's stored with the definition, because
it has to survive a host reboot, which clears `/run`. It's set by a user's
`Stop` and cleared by a user's `Start`, and only `unless-stopped` reads it.

### Timers and concurrency

A pending restart is a `time.AfterFunc` timer, held in `Manager.restarts` by
instance ID as a `*pendingRestart`. The rules mirror those for VMM handles:

- A timer is compared **by identity**. When it fires, it proceeds only if the
  map still holds that same `*pendingRestart`. A restart that was cancelled or
  replaced finds something else there and does nothing.
- It then takes the **instance lock**, re-reads the definition (which may have
  been edited or deleted), and goes ahead only if the state is still
  Restarting.
- `Start`, `Stop`, `Delete` and `RestoreSnapshot` call `cancelRestart` under the
  instance lock. A user acting on an instance takes over from its policy.
- A restart that fails to admit or boot is passed to `ended` as a failure. It
  counts against the retry limit and backs off like any other failure, so
  there's no tight retry loop.

`Close` closes `closing` and stops every timer under `restartsMu`. It waits for
restarts already under way (`restarting`) before waiting for the VMM watchers,
because a restart in progress may still register a new watcher. A timer checks
`closing` under the same mutex before counting itself in, so nothing is added
after the wait begins. An instance that was Restarting when the daemon stopped
stays Restarting on disk.

### When dicerd wasn't running

`Recover` runs before the API is served:

- **Running or Paused, VMM alive:** the VMM is adopted and watched as usual.
  If it exits later, its exit status is unknown and only the status disk can
  say what happened.
- **Running or Paused, VMM gone:** the instance ended while nobody was
  watching. `readExit` runs on the status disk, which survived because it is
  on the host, and the result goes through `ended` as if the end had been
  seen live. A failure is marked "while dicerd was not running".
- **Restarting:** rescheduled for its recorded `NextRestartAt`, or immediately
  if that has passed.

`StartOnBoot` then runs after the API is up. It starts every Stopped or Failed
instance whose policy is `always`, or `unless-stopped` without
`StoppedByUser`. After a host reboot `/run` is empty and every instance reads
as Stopped, so this is how instances come back after a reboot.

### What this does not cover

- **A workload that is alive but broken.** If a process the entrypoint started
  dies, or is OOM-killed, while the entrypoint keeps running, nothing ends.
  A [health check](#health-checks) catches this if it breaks the service;
  otherwise an init system in the guest (s6, systemd) should supervise those.
- **Guests booted before the status disk existed.** A snapshot taken earlier
  restores a `dicer-init` that never reports an exit code, so every end of
  such a guest is a failure.
- **A `reboot` inside a systemd guest.** Only a power-off is reported, so a
  reboot counts as an unclean end. With a restart policy, the effect is
  that the guest reboots.

## Health checks

A health check catches a workload that is still running but has stopped
doing its job. It reuses the restart machinery above: an instance found
unhealthy ends the same way as one that crashed.

### The pieces

| Piece | Where | Role |
|---|---|---|
| `dicer.HealthCheck`, `dicer.Health` | the root package | What a check is, and what it found |
| `healthCheckFromDocker` | `internal/image/healthcheck.go` | Converts an image's `HEALTHCHECK` (`CMD`, `CMD-SHELL`, `NONE`) |
| `AgentService.Probe` | `internal/guest/agent/probe.go` | Runs one probe inside the guest: exec, HTTP or TCP |
| The monitor | `internal/vm/health.go` | Decides when to probe, judges a run of failures, acts on unhealthy |

The check and the verdict are in the root package because both an image and an
instance carry a check, and the API returns both. What is done with them is
split by who does it: reading an image's belongs to `image`, and running the
check belongs to `vm`.

### Which check runs

`dicer.EffectiveHealthCheck(instance, image)` resolves the check when an
instance starts:

- the instance's own check, if it has one
- none, if the instance's check is `Disabled` (`--no-healthcheck`)
- otherwise the image's

Unset timings then take the defaults (10s interval, 5s timeout, 3 retries,
no start period). The resolved check is recorded in the runtime record, next
to `ImageDigest`. A daemon that adopts the VMM later probes it the same way,
even if the definition or the image's tag has changed since.

### Probing

The agent is stateless. `Probe` runs one probe within the timeout the host
gives it and answers `{healthy, output}`, with the output truncated to 4 KiB:

- **exec** runs the command in the same environment `dicer exec` gets.
- **http** sends a GET to `127.0.0.1:port/path` and counts 2xx and 3xx as
  healthy. It doesn't follow redirects: a redirect is already an answer.
- **tcp** connects to `127.0.0.1:port`.

Probing on the guest's loopback address means the check works on isolated
networks too, and tests the service where it runs rather than the network
path to it.

A probe that fails is a normal response, not an RPC error. An RPC error
means the agent couldn't be asked at all. The monitor counts that as a
failed probe, with one exception: `Unimplemented`, which comes from a guest
booted with an agent that predates `Probe`. That guest can't be checked,
which is no evidence it's unhealthy, so the result isn't counted, and its
next boot installs a current agent.

### The monitor

Health checking is part of supervision. `supervise()` registers a
`supervised{proc, health, stopMonitor}` for each VMM, and starts the monitor
goroutine next to the exit watcher when the run has a check. `forget()`
cancels it. Every way a run ends (stop, delete, crash, restore, or a restart
triggered by the monitor itself) therefore ends its checking, with no
separate bookkeeping. The monitor also stops on `Manager.closing`, because
`Close` waits for every watcher, and VMMs outlive the daemon.

Each probe starts one interval after the previous one finished, using a
timer that is reset rather than a ticker, so a slow probe never has the next
one queued behind it. A paused instance isn't probed: its vCPUs are halted,
and it hasn't failed.

The state is kept in memory, not in the runtime record. It's an observation
that the next probe rebuilds, and writing it to disk would cost a write per
instance every interval. After a daemon restart the status starts again at
`starting`, but the start period is still measured from the run's
`StartedAt`, so a daemon restart doesn't give a long-running workload a new
grace period.

### The verdict

`State.Observe` follows Docker's rules:

- A pass makes the instance healthy at once and resets the failure streak.
- A failure during the start period is recorded (its output is shown) but
  isn't counted.
- Otherwise `Retries` failures in a row make it unhealthy. Until then it
  keeps whatever status it had, so one failure doesn't flip a healthy
  instance.

### Unhealthy is a failure end

On every probe that finds the instance unhealthy (not only the first, since
the policy may have changed), `handleUnhealthy`:

1. takes the instance lock and checks the VMM handle is still the one it
   probed, by the same identity rule as exits;
2. re-reads the definition, because the current restart policy is the one
   that applies;
3. asks `decide()` what a failure would lead to.

If the policy would restart, it calls `stopVMM` (a graceful stop, which also
cancels the monitor) and then `ended(failedExit("the health check failed N
times in a row: …"))`. From there it's the normal path of the previous
section, with the same backoff, retry limit and `Restarting` state. If the
policy wouldn't restart (it's `no`, or `on-failure:N` has used its retries),
the instance is left running and reported unhealthy. Stopping an instance
that won't be started again would only make things worse.

### What this does not cover

- **Which process failed.** A check sees the service, not the processes
  behind it. An OOM-killed child that doesn't break the service goes
  unnoticed. Reporting in-guest OOM kills is on the [roadmap](ROADMAP.md).
- **Changing a running instance's check.** Like the rest of the definition,
  a check can only be changed while the instance is stopped, and the new one
  applies from the next start.
- **Old images.** Images cached before health checks existed have no
  `HEALTHCHECK` recorded. Pull them again to pick it up.

## Image garbage collection

Images pile up: every tag that moved, every image tried once. A prune
removes all the unused ones when someone asks. Garbage collection removes
them as it goes, by limits set in `config.yaml` (`images.gc_*`, off unless
one is set).

### What is in use

One function answers this for both prune and GC: `vm.Manager.ImagesInUse`.
An image is in use if any of these hold:

- an instance is defined to boot from it, as its reference resolves on this
  host now;
- a running guest booted from it (by digest, whatever the tag has moved to
  since);
- a snapshot was taken on it.

An image in use is never removed. An instance defined after the in-use set
was computed may lose its image to a pass that was already running. Its
next start pulls the image again, the same as for any image that isn't
held.

### When it was last used

Each image records `LastUsedAt`:

- set when the image is pulled;
- stamped to now for every image in use, at each pass;
- saved with the image's metadata, so it survives restarts.

An image recorded before this field existed counts as used when the daemon
first starts with it, rather than unused forever. That way an upgrade can't
immediately collect everything.

The index hands its `*Image` records out to readers, so it doesn't change
one in place: `markUsed` replaces the record with an updated copy.

A pass of `CollectGarbage`:

1. stamps the images in use;
2. removes every collectable image unused for longer than
   `gc_max_unused_age`;
3. while the store (the bootable disks plus the layer cache) is over
   `gc_max_size`, removes the least recently used collectable image and
   measures again. Images share cached layers, so what removing one frees
   is only known once it's gone.

An image is collectable if it isn't in use and wasn't used in the last ten
minutes (`gcGracePeriod`). That grace period covers the gap between the
pull and the create in `dicer run`. The selection (`expired` and
`collectable`) is pure, so it's tested on its own.

Removal goes through the same `remove` as a prune: the index entry, the
disk, and then the cached layers that no remaining image needs.

`RunGC` runs a pass at startup, once recovery is done, and then every
`gc_interval`. A pass that can't find out what is in use removes nothing.
It logs each removal with its reason, and the metrics count removals
(`dicer_image_gc_collected_total{reason}`) and bytes reclaimed.

## Events

Events record what happens to the resources on the host, for people
(`dicer events`, the Events block of `inspect`) and for programs (the
`GetEvents` stream).

### One kind of record for every kind of resource

An `events.Event` is about one resource, whatever its kind. It has a kind
(`instance`, `image`), an ID and a name, an action, a message for a person,
and attributes for a program. The kinds and actions are constants in
`internal/events`, the single list of everything Dicer reports. To report
on a new kind of resource (kernels, tokens, clients), declare its constants
there and have its manager call `Record`. The log, the API and the CLI need
no change.

### Who records

Each package that reports declares its own one-method `Events` interface
and has a discarding default, as it does for `Metrics`. Nothing depends on
the log itself.

- **`vm.Manager`** records every instance event. Creating an instance goes
  through `vm.Manager.Create` rather than straight to the store, so that
  every instance event has one source.
  - `ended` records `exited` (a clean end, with the code) or `died` (a
    failure, with the reason), then `restarting` if the policy restarts it.
  - A successful restart records `started` with its `restart_count`.
  - The health monitor records `healthy` and `unhealthy` when the verdict
    changes.
- **`image.Manager`** records `pulled`, but only for a real pull, not a
  cache hit. It records `deleted` (with `by=user` or `by=prune`) and
  `collected` (with the reason).

### The log

`events.Log` keeps the events in memory, and appends each one to
`<data_dir>/events.jsonl` as it's recorded, so they survive `dicerd`
restarting. When the log is opened, a line cut short by a crash mid-write
is skipped.

Retention works by count (`events.max_count`, 10,000 by default) and by
age (`events.max_age`, none by default). The file is rewritten with only
what is kept when the log is opened, and when it has grown past the count
by a quarter. It's never rewritten on every event.

`Record` never fails its caller. The event has already happened, and a
stop must not fail because the disk is full. A write error is logged.

### Following

`Subscribe(filter, limit)` returns the history and a live channel,
registered together under the log's lock. A client that replays and then
follows therefore sees each event exactly once, with no gap between the
history and the live stream.

`Record` never waits on a subscriber. Each one has a buffer of 256 events,
and one that falls further behind is cut off with `ErrFellBehind`, which the
API passes on as `RESOURCE_EXHAUSTED`. The lifecycle calls `Record` while
holding an instance's lock, so a slow `dicer events -f` must not be able to
stall a stop.

`GetEvents` serves both uses: the history on its own, or with `follow`, the
history and then everything new. `inspect` asks for an instance's last ten,
by ID, because a name can be reused after a delete.
