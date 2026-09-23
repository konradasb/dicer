# Dicer

Run virtual machines from container images, on one host.

Dicer boots an OCI image as a real VM: it pulls the image, converts it to a
read-only EROFS root filesystem, and starts it under a hardware-isolated
hypervisor with a writable overlay on top. You get container ergonomics with
VM isolation.

```console
$ dicer instance create web \
    --image docker.io/library/nginx:1.27 \
    --kernel vmlinux-6.12 --network default \
    --vcpus 2 --memory 2GiB --disk 10GiB
Instance web created. Start it with: dicer start web

$ dicer instance start web
Instance web started (172.20.0.7)

$ dicer instance exec web -- sh
/ #
```

## Scope

Dicer manages **one machine**. There is no cluster, no scheduler, no overlay
network and no leader election — `dicerd` owns the host it runs on and nothing
else. To manage several hosts, run a daemon on each and point a client at
whichever one you mean; deciding *which* host a workload belongs on is a layer
above this one, deliberately.

This is roughly the shape of `libvirtd`, or of Fly.io's `flyd`: a host agent
with a precise, un-opinionated API, rather than an orchestrator.

## How it works

```
dicer  ──unix socket──▶  dicerd  ──▶  cloud-hypervisor  ──▶  guest
       ──mutual TLS───▶
                                      (or firecracker)
                           │                                   │
                           │  image → EROFS rootfs (read-only)  │
                           │  overlay.img       (read-write)    │
                           │  config.img        (boot config) ──┤
                           │  status.img        (how it ended) ◀┤
                           │  TAP on a bridge, NAT out          │
                           └──vsock──────────────────────────▶ dicer-agent
```

`dicer-init` runs as PID 1 inside the guest: it mounts the root filesystem,
applies the configuration handed over on the config disk, and starts the
workload. When the workload exits, so does the VM, and the exit code is
reported to the host on the status disk.

### How the guest starts the workload

| Mode | What runs | When |
|---|---|---|
| `exec` | The command, as PID 1 of its own PID namespace -- as in a container | Anything that is not systemd |
| `systemd` | systemd, as the machine's PID 1 | The command resolves to the systemd binary |

In `exec` mode `dicer-init` stays the machine's PID 1, outside the
namespace, with the guest agent. Anything that insists on being PID 1 -- the
s6-overlay `/init` of Home Assistant and the linuxserver.io images, tini,
dumb-init, runit -- is, and just works. The default, `auto`, decides inside
the guest once its root filesystem is mounted, following symlinks: Debian's
`/sbin/init` is systemd, Alpine's is not, and `dicer run debian-systemd --
bash` runs bash. `--init-mode exec|systemd` overrides it for an image it
gets wrong, such as one whose entrypoint script ends in `exec /sbin/init`.

**A stop is graceful.** `dicer stop` asks the guest to shut down: in `exec`
mode the workload gets SIGTERM, and s6 or anything else that handles it
stops its services in order; in `systemd` mode systemd powers the machine
off. The workload has 10 seconds, as `docker stop` gives it, before the VM
is ended regardless. `dicer rm -f` does not wait.

**Creating an instance records a definition; it does not boot it.** The two
steps are separate so a definition can be written, reviewed and kept in version
control before anything runs:

```console
$ dicer instance create web -f web.yaml     # records it
$ dicer instance start web                  # boots it
$ dicer instance create web -f web.yaml --start   # both, if you insist
```

Definitions are YAML files under `/var/lib/dicer`, readable and editable by
hand. Runtime state lives under `/run/dicer`, which a reboot clears — correctly,
since nothing is running after one.

## The command line

Every resource has a management command -- `dicer instance`, `dicer image`,
`dicer network`, `dicer volume`, `dicer kernel` -- and the everyday instance
and image commands also stand at the top level, spelled as Docker spells
them:

```console
$ dicer run --name web -p 8080:80 nginx:1.27   # instance create --start
$ dicer ps                                      # instance list
$ dicer exec web nginx -t                       # instance exec
$ dicer logs -f web                             # instance logs
$ dicer stop web db                             # instance stop, of both
$ dicer rm -f $(dicer ps -q -f label=env=dev)   # instance delete
```

`start`, `stop`, `restart`, `pause`, `resume`, `rm`, `rmi` and `inspect` take
several names, carry on past one that fails, and offer the closest name for
one that does not exist. Lists filter with `-f KEY=VALUE` (`name`, `state`,
`image`, `network`, `label`), print only names with `-q`, and take `--format`
as `table`, `json`, `yaml` or a Go template; `dicer ps` shows the essentials
unless given `--wide`, and `dicer ps --watch` keeps them on screen.
`dicer inspect --format json` prints the daemon's whole record of an instance.
Shell completion (`dicer completion bash|zsh|fish`) completes instance, image,
network, volume, kernel, snapshot and remote names from the daemon.

Every command takes `--debug` (`-D`), which traces each call to the daemon,
and `--timeout` to bound them.

### Default kernel and network

An instance that names no kernel or network gets the daemon's default. Unset,
that is the only kernel, or network, there is -- so a host with one of each
needs neither flag -- and with several, the daemon's configuration picks:

```yaml
defaults:
  kernel: vmlinux-6.12
  network: default
```

`dicer info` shows what an instance gets by default.

## Hypervisors

`dicerd` carries the VMM binaries it runs, so there is nothing to install.
`dicer info` lists the hypervisors and versions yours has:

```console
$ dicer info
...
    Hypervisors: cloud-hypervisor v49.0.0 (default), v48.0.0
                 firecracker v1.17.0

$ dicer instance create web --image alpine:3.21 --kernel vmlinux \
    --network default --hypervisor-type firecracker
```

| | Cloud Hypervisor (default) | Firecracker |
|---|---|---|
| Boot, exec, volumes, networking | yes | yes |
| Pause and resume | yes | yes |
| Snapshots | yes | yes |
| Memory hotplug | yes | yes (virtio-mem) |
| vCPU hotplug, CPU pinning | yes | no |
| PCI and GPU passthrough | yes | no |

Either way the guest kernel must be an uncompressed image with virtio
drivers built in. Firecracker is stricter about what it will boot than Cloud
Hypervisor is, so a kernel that works under one may not work under the
other; the instance's `--kernel-args` default to whatever its hypervisor
needs.

## Images

Pulling reports what it is doing, and how far along it is:

```console
$ dicer image pull docker.io/library/ubuntu:24.04
Downloading 20.5 of 28.4 MiB (72%)
Image docker.io/library/ubuntu:24.04 pulled (sha256:4967544..., 47 MiB)
```

An instance boots the image the host holds under its reference; the image
is pulled only if the host has none. Starting an instance never asks a
registry about an image already here, so it works offline, and a tag that
has moved upstream changes nothing until it is pulled again.

Images are kept until you remove them. Deleting one an instance is defined to
boot from, a running instance booted from, or a snapshot was taken on is
refused unless you force it, and `prune` removes the rest along with the
layers cached for them:

```console
$ dicer image prune
Deleted docker.io/library/debian:13
Reclaimed 198 MiB from 1 image(s)
```

### Garbage collection

`dicerd` can do that on its own, as it goes. Set a limit in
`/etc/dicerd/config.yaml`, and every hour it removes the images nothing
uses that break it:

```yaml
images:
  gc_max_unused_age: 168h   # unused for a week
  gc_max_size: 50GiB        # images and layer cache, least recently used first
  gc_interval: 1h           # the default
```

An image is in use while an instance is defined to boot from it, a guest
runs on it or a snapshot needs it, and one in use is never removed. `dicer
image ls` shows when each was last used; an image used within the last ten
minutes is kept whatever the limits, so the pull `dicer run` makes is not
collected before its instance exists. With no limit set, images are only
removed by a prune.
## When an instance ends

An instance ends on its own when its workload exits -- which is when the
entrypoint does, as with a container -- when its guest resets or powers off,
or when its hypervisor dies. `dicer ps` says how:

```console
$ dicer ps -a
NAME    IMAGE                    STATUS                        IP          PORTS
web     docker.io/library/nginx  Up 3 hours                    172.20.0.7  8080->80/tcp
job     docker.io/library/app    Exited (0) 5 minutes ago      172.20.0.9  -
worker  docker.io/library/app    Restarting (3) in 8 seconds   172.20.0.8  -
```

An end is clean only if the guest said so: the workload exited 0, or a guest
running systemd powered off. Anything else -- a non-zero exit, a kernel panic,
a reboot from inside, a hypervisor killed on the host -- is a failure.

What happens next is the instance's restart policy, as Docker has it:

| Policy | Restarts | Starts with the daemon |
|---|---|---|
| `no` (the default) | never | no |
| `on-failure[:N]` | after a failure, at most N times in a row | no |
| `unless-stopped` | after any end | unless a user stopped it |
| `always` | after any end | yes |

```console
$ dicer run --restart on-failure:5 --name worker app
$ dicer update worker --restart unless-stopped   # takes effect at the next end, even while running
```

Restarts back off: a second after the first failure, doubling to at most five
minutes. An instance that stays up for ten minutes starts the count again.
Stopping or starting an instance by hand cancels a pending restart.

The policy sees the workload end, not everything inside it fail. A process
the entrypoint started that dies, or is killed by the guest's OOM killer,
while the entrypoint carries on goes unnoticed by it; a health check is how
to catch that.

### Waiting for one to end

`dicer wait` blocks until an instance stops and exits with the status its
guest ended with, so a script can run a job and act on how it went:

```console
$ dicer run --name build --restart no builder:1.4
$ dicer wait build
0
```

It waits on the event stream rather than asking over and over, so a stop is
noticed as it happens. An instance its restart policy starts again has not
stopped, so the wait goes on. A guest that ended without reporting a status
-- its hypervisor was lost, or it never started -- exits `125`, the status a
shell uses for a command it could not run.

### Cleaning up after one

`--rm` deletes an instance once it stops, however it stopped:

```console
$ dicer run --rm --name build builder:1.4
$ dicer wait build; dicer ps   # gone
```

The daemon does the deleting, so it happens even if whoever started the
instance has gone -- and, unlike a client that does it, it still happens when
the guest ends at three in the morning. An instance that is stopped by hand is
deleted too: `--rm` says what happens when it stops, not how it stopped.

`--rm` and a restart policy that restarts are refused together, since only one
of the two could happen:

```console
$ dicer run --rm --restart always app
Error: an instance cannot be deleted when it stops and restarted when it stops:
the restart policy is always, so drop it or drop the request to delete it
```

## Events

The daemon records what happens on the host -- instances created, started,
stopped, crashed, restarted and found unhealthy, images pulled and collected
-- and keeps it, so it explains what happened while nobody was looking:

```console
$ dicer events --since 1h
2026-09-22 09:49:48  Instance  grafana      Unhealthy   Health check "http :3000/api/health" failed 3 times in a row: timed out after 5s
2026-09-22 09:49:48  Instance  grafana      Died        Instance failed after running for 2h13m4s: health check "http :3000/api/health" failed 3 times in a row: timed out after 5s
2026-09-22 09:49:48  Instance  grafana      Restarting  Back-off restarting failed instance in 1s (restart 1, policy always)
2026-09-22 09:49:50  Instance  grafana      Started     Restarted instance on cloud-hypervisor v49.0.0 in 1.8s (restart 1, policy always): 2 vCPUs, 1 GiB memory, IP 172.20.225.60, PID 48213
2026-09-22 11:20:52  Image     alpine:3     Collected   Garbage-collected image docker.io/library/alpine:3 (sha256:beefdbd8a1da): unused since 2026-08-23 10:02:11, longer than gc_max_unused_age 30d; 8.3 MiB boot disk removed
```

Like `dicer logs`, it prints what is kept and exits: `-f` follows new events,
`-n 20` shows the last twenty, `--since` takes a duration, a date or a time,
and `--kind` and `--name` narrow it down. `--format json` prints one object a
line, with attributes for scripts -- `exit_code`, `restart_count`, `digest`.
Every event says what happened in a sentence; `dicer inspect` ends with an
instance's last ten.

The most recent 10,000 are kept, in the data directory; `config.yaml` can
keep fewer, or drop them by age:

```yaml
events:
  max_count: 10000
  max_age: 720h
```

## Health checks

A health check asks whether the workload is doing its job, not only running.
The guest agent runs the probe inside the guest, so it works on an isolated
network and checks the service where it runs:

```console
$ dicer run --name grafana -p 3000:3000 --restart always \
    --health-http 3000/api/health grafana/grafana
$ dicer ps
NAME     IMAGE                    STATUS                  IP           PORTS
grafana  docker.io/grafana/...    Up 2 minutes (healthy)  172.20.0.9   3000->3000/tcp
```

| Probe | Flag | Healthy when |
|---|---|---|
| Command | `--health-cmd 'pg_isready -q'` | it exits 0; run by `/bin/sh` |
| HTTP | `--health-http PORT[/path]` | a GET on the guest's loopback answers 2xx or 3xx |
| TCP | `--health-tcp PORT` | a connection to the guest's loopback opens |

Timing is Docker's, with quicker defaults: `--health-interval` (10s),
`--health-timeout` (5s), `--health-retries` (3) and `--health-start-period`
(none) -- failures in the start period do not count, a success does. An image
that declares a `HEALTHCHECK` is checked as it says, unless the instance sets
its own or `--no-healthcheck`. A spec file takes the same as a block:

```yaml
healthcheck:
  http: 3000/api/health
  interval: 15s
```

An instance goes `starting` → `healthy`, and `unhealthy` after its retries
fail in a row. **Unhealthy counts as a failure to the restart policy**:
under `on-failure`, `unless-stopped` or `always`, the VM is stopped and
restarted, with the same backoff and retry limit as a crash. Under `no`, or
once `on-failure:N` has used its retries, it is reported and left running --
stopping an instance that will not come back helps no one. `dicer inspect`
shows the check and what its last probe said.

A guest booted before health checks existed has an agent that cannot run
them; it is not counted unhealthy for it, and its next boot installs one that
can.

## Snapshots

A snapshot freezes a running instance to disk -- its memory, its device
state, and a copy of its overlay disk at the same moment -- so it can be put
back exactly where it was:

```console
$ dicer instance snapshot create web before-upgrade
Snapshot before-upgrade of instance web created (584 MiB)

$ dicer instance stop web
$ dicer instance snapshot restore web before-upgrade
Instance web restored from snapshot before-upgrade (172.20.0.7)
```

The disk is part of the snapshot on purpose. A guest resumed from memory
expects its filesystem as it left it, so restoring rolls the disk back too
and anything written since is discarded. On a copy-on-write filesystem the
copy is free; elsewhere only the used blocks are copied.

A snapshot is restored by the hypervisor version that took it, and belongs to
its instance: deleting the instance deletes its snapshots.

## Publishing ports

Guests sit behind NAT on a bridge local to the host, so nothing outside the
host can reach them. `--publish` (`-p`) forwards a host port to one in the
guest:

```console
$ dicer instance create web -f web.yaml -p 8080:80 -p 10.0.0.5:5353:53/udp
```

The form is `[hostIP:]hostPort:guestPort[/tcp|udp]`. The protocol defaults to
tcp, and the host IP to every address the host has. In a definition file, the
same strings go under `ports:`.

Ports belong to the definition and are applied at every start, to whatever
address the instance holds then. They are forwarded by iptables, not by a
process, so they keep working while `dicerd` restarts. A start is refused if
the host port is in use, whether by a process on the host or by another
instance.

Some traffic is not forwarded:

- **Loopback.** `localhost:8080` on the host does not reach the guest; use
  one of the host's own addresses. Forwarding loopback would mean letting
  guests address the host's loopback services too.
- **Other networks.** An instance on another network cannot reach a published
  port, because networks are isolated from each other. Instances on the same
  network reach each other directly by address, unless the network is
  isolated.

## Copying files

`dicer instance cp` copies a file or directory into a running instance, or
out of one. A path in an instance is written `NAME:PATH`:

```console
$ dicer instance cp ./app web:/srv
Copied ./app to web:/srv (1.2 MiB)

$ dicer instance cp web:/var/log/app.log .
Copied web:/var/log/app.log to . (48 KiB)
```

What is copied lands as `cp -r` would put it: into the destination if it is
a directory, in its place if it is a file, and at it if nothing is there.
Modes, times and symlinks are kept; ownership is not, so what is copied
belongs to whoever receives it.

Copying goes through the guest agent over vsock, like `exec`, so it works in
any image -- no shell or `tar` needed inside -- and for instances whose
network is isolated. Neither end trusts the other's archive: nothing in one
can be written outside the destination, whatever its paths or symlinks say.

## Resources

A start that would give instances more CPU or memory than the host has is
refused, and says why:

```console
$ dicer instance start big
Error: rpc error: code = ResourceExhausted desc = instance "big" needs 4 vCPU,
16 GiB, but 14 vCPU, 20 GiB of the 16 vCPU, 31 GiB this host allows is committed
```

An instance holds its vCPUs and memory from the moment its start is admitted
until it stops -- paused included, since a paused VM is still resident. What
the host allows is its CPUs and memory, less a reserve, times an overcommit:

```yaml
resources:
  cpu_overcommit: 4                  # vCPUs per CPU
  memory_overcommit: 1               # no memory overcommit
  reserved_memory_bytes: 1073741824  # 1 GiB kept for the host and VMM overhead
```

vCPUs are overcommitted four to a CPU by default, since an idle vCPU costs
the host nothing. Memory is not: nothing takes memory back from a guest that
uses it, and a host that runs out kills VMs. A definition that could never
fit -- more vCPUs than the host has CPUs, or more memory than it allows in
total -- is refused when it is created, not at its first start.

`dicer info` shows how full the host is:

```console
$ dicer info
  ┌───────┐
 ╱ ●   ● ╱│
┌───────┐ │
│ ●   ● │●│  dicer v0.5.0
│   ●   │ ┘  compute-1
│ ●   ● │╱   2 running, 1 stopped (3 defined) · 2 healthy
└───────┘
...
           vCPU: █████░░░░░░░░░░░░░░░  4 of 16         25%
                 4 CPUs, 4× overcommit
         Memory: ██████████░░░░░░░░░░  15.5 of 31 GiB  50%
                 32 GiB, 1 GiB reserved
           Disk: ██░░░░░░░░░░░░░░░░░░  25 of 250 GiB   10%
                 40 GiB provisioned
```

On a terminal the bars are green, turning yellow past 70% and red past 90%.

Disk is reported, not enforced. Instance disks and volumes are sparse, so what
they have been promised routinely exceeds the disk without anything being
wrong; what matters is the filesystem filling, which no check at start time
can prevent. Watch the free space instead.

## Requirements

- Linux with KVM (`/dev/kvm`)
- `erofs-utils` (`mkfs.erofs`) and `e2fsprogs` (`mke2fs`)
- IPv4 forwarding enabled

## Install

```console
curl -fsSL https://raw.githubusercontent.com/dicer-sh/dicer/main/scripts/install.sh | bash
```

Then create a network and import a kernel — see the output of the installer,
or `dicer <command> --help` for anything else.

## Configuration

`dicerd` reads `/etc/dicerd/config.yaml`. Every setting has a working default,
so the file is optional; see [`example.yml`](example.yml) for what can be set.
A key it does not recognise is an error rather than ignored, so a typo cannot
quietly leave a setting at its default.

## Remote access

Locally, the API is a Unix socket and its file permissions decide who may use
it. To manage a host from another machine, have the daemon listen on TCP too:

```yaml
api:
  tcp:
    listen: 0.0.0.0:7443
```

Every TCP connection is mutual TLS, with no certificate authority: the daemon
and each client generate their own keys, which never leave their machines,
and each recognises the other by its certificate's fingerprint. A client is
trusted by enrolling with a one-time token, created on the host:

```console
host$ dicer token create laptop
Enrolment token for laptop, valid until 2026-09-21 15:04:05:

dicer1.eyJuYW1lIjoibGFwdG9wIiwiYWRkcmVzc2VzIjpbIjE5Mi4wLjIuMTo3NDQzIl0s...

laptop$ dicer remote create prod dicer1.eyJuYW1lIjoibGFwdG9wIiwiYWRkcmVzc2VzIjpbIjE5Mi4wLjIuMTo3NDQzIl0s...
Enrolled with the daemon at 192.0.2.1:7443 as "laptop"
Remote prod created. Use it with: dicer remote use prod

laptop$ dicer --remote prod instance list
```

The token carries the daemon's addresses, its fingerprint -- the client
refuses any daemon without it -- and a secret that works once, for an hour by
default. The daemon keeps only a hash of the secret.

`dicer remote use` sets the remote commands go to; `--remote` or
`$DICER_REMOTE` picks one for a single command, and the built-in `local`
remote is this machine's daemon. `dicer client list` shows who a daemon trusts,
and `dicer client delete` revokes a client: its next request is refused.
`dicer token list` shows the tokens not yet used, and `dicer token delete`
withdraws one. Every call that changes something is logged with who made it.

Every trusted client may do everything, as a local user of the socket may.

### Keys

There are two kinds of key, and each stays on the machine that made it:

| Key | Where | Trusted by |
|---|---|---|
| The daemon's | `/var/lib/dicer/tls/` | Every client that enrolled with it, which pinned its fingerprint |
| Each user's | `~/.config/dicer/client/` | Every daemon it enrolled with, which recorded its certificate |

`dicer info` shows the daemon's fingerprint, and `dicer remote list` the one a
client pinned: comparing the two, over a channel you already trust, is how to
check a client is talking to the right daemon. `dicer client show NAME` shows
the certificate a client enrolled with; `--pem` prints it.

Only fingerprints decide trust. The names and dates inside a certificate are
shown for reference and not enforced, so no certificate ever expires out from
under a working setup.

**The daemon's key does not rotate, and cannot change without every client
noticing.** That is the point of pinning it: a daemon with a different key is,
to a client, indistinguishable from an impostor, and is refused. The one good
reason to replace the key is that it may have been stolen -- and then a
rotation the old key vouches for would be worthless, since whoever stole it
could vouch too. Trust has to be re-established out of band, which is what
enrolling with a fresh token does. To replace it:

```console
host$ sudo systemctl stop dicerd
host$ sudo rm -r /var/lib/dicer/tls
host$ sudo systemctl start dicerd        # generates a new key
host$ dicer info                         # note the new fingerprint

# For each client: forget it, and invite it again under the same name.
host$ dicer client delete laptop
host$ dicer token create laptop

laptop$ dicer remote delete prod
laptop$ dicer remote create prod <token>
```

Every client is re-enrolled, not only re-pointed at the new key: after a
suspected compromise, nothing trusted before it should be trusted after it
without being re-established. Losing `/var/lib/dicer/tls` in a reinstall has
the same effect as replacing the key and is recovered from the same way; back
it up with the rest of the data directory to avoid it.

A client whose key may have been stolen is revoked on every daemon that trusts
it with `dicer client delete`. Then remove `~/.config/dicer/client/` so a new
key is generated, and enrol again.

## Metrics

`dicerd` always records Prometheus metrics; serving them is off by default,
because unlike the API the endpoint has nothing to authenticate with:

```console
$ cat /etc/dicer/dicerd.yml
metrics:
  enabled: true
  listen: 127.0.0.1:9101
$ curl -s localhost:9101/metrics | grep '^dicer_instances'
dicer_instances{state="Running"} 3
dicer_instances{state="Stopped"} 1
```

What is exported: instance counts by state and the vCPUs and memory committed
to live ones; instances by health; lifecycle operations and their durations
by outcome; restarts made by restart policies; address
pool usage per network; gRPC calls by method and response code; image pulls,
downloads, conversions and cache hits; and the usual Go runtime and process
collectors.

The one worth an alert before anything else is the address pool. A subnet is
finite, and when it fills every subsequent start fails:

```
dicer_network_addresses_allocated{network="default"} 41
dicer_network_addresses_available{network="default"} 212
```

The gauges are read when a scrape arrives rather than kept up to date as
things change — a VMM can die without telling the daemon, so counting the
real state per scrape is the only version that cannot drift.

There is no authentication. Bind it to loopback and let a local scraper reach
it, or put something in front.

## Secrets

Dicer does not store secrets. It exposes host files to a guest at
`/run/secrets/<name>`, reading them at every start:

```console
$ dicer instance create web -f web.yaml \
    --host-file db-password=/etc/dicer/db-password
```

Whatever manages that file on the host — sops, vault-agent, systemd
credentials, a root-owned file — stays in charge of it. A daemon that owns a
single machine has no business keeping an encrypted store whose key sits on the
same disk as the ciphertext.

## Renaming an instance

A stopped instance can be renamed. It keeps its ID, its disks, its snapshots
and the address it holds: only what you call it changes.

```console
$ dicer rename web web-old
```

A running instance is refused. Its name is where its files are kept on the
host, and its guest took its hostname from the old name when it booted, so a
rename could not fully take effect until it is started again.

## Go client

Everything the CLI does, a Go program can do. The client is the module's root
package, and speaks the same types the daemon stores:

```go
import "github.com/dicer-sh/dicer"

c, err := dicer.NewClient()
defer c.Close()

inst, err := c.RunInstance(ctx, dicer.InstanceSpec{
    Name:        "web",
    ImageRef:    "nginx:1.27",
    VCPUs:       2,
    MemoryBytes: 1 << 30,
    Ports:       []dicer.PortMapping{{HostPort: 8080, GuestPort: 80}},
})

fmt.Println(inst.Status.State, inst.Status.IP)
```

An `Instance` is in two halves: `Spec` is what was asked for and persists,
`Status` is what the host made of it. Errors arrive in classes, so what went
wrong is matched rather than read:

```go
if _, err := c.GetInstance(ctx, "web"); errors.Is(err, dicer.ErrNotFound) {
    ...
}
```

A daemon on another machine is reached the way the CLI reaches one — over
mutual TLS, pinned to its fingerprint — with `dicer.WithRemote` and
`dicer.WithIdentity`. See [pkg.go.dev](https://pkg.go.dev/github.com/dicer-sh/dicer).

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md).

## License

Dicer is licensed under the MIT License. See [LICENSE](LICENSE) for more
information.
