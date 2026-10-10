---
title: Troubleshooting
weight: 13
description: "Why an instance will not start or stay up, and how to find out."
icon: support
related:
  - /docs/guides/working-inside-guests
  - /docs/guides/operating-the-daemon
  - /docs/guides/monitoring
  - /docs/guides/standby
---

Most problems show up in one of three places: the error a command prints,
the instance's state and console, or the daemon's log. This guide starts
from what you see and works back to the cause.

## Where to look

| To find out | Run |
|---|---|
| Whether the host can run instances at all, and what to fix if not | `dicer doctor` |
| Why an instance is not running, and how it last ended | `dicer inspect NAME` |
| What the guest printed while booting and running | `dicer logs NAME` |
| Why a guest never booted at all | `dicer logs --source hypervisor NAME` |
| What happened to an instance, and when | `dicer events --name NAME` |
| What the daemon did | `journalctl -u dicerd` |
| What the command line sent and got back | `dicer --debug …` |

For more detail from the daemon, set `log_level: debug` in its
[configuration](../../reference/configuration#log-level) and restart it.
Running guests are not affected.

## The command line cannot reach the daemon

```text
Error: there is no socket at /run/dicer/dicer.sock; is dicerd running?
```

The daemon is not running. Start it, and read its log to see why it
stopped:

```console
$ sudo systemctl start dicerd
$ journalctl -u dicerd -n 50
```

```text
Error: connection error: desc = "transport: Error while dialing: dial unix /run/dicer/dicer.sock: connect: permission denied"
```

Only root and members of the `dicer` group can use the socket. Run the
command with `sudo`, or [join the group](../remote-access#on-the-host-itself).
Joining takes effect the next time you log in.

For a remote daemon, check which daemon a command reaches with
`dicer info`, and see [Remote access](../remote-access). The error says
what went wrong:

- *unauthenticated*: the daemon refused the token. It was deleted or
  rotated, or was never this daemon's. The daemon's log says which, in a
  `call refused` line. Make a new token on the host with
  `dicer token create`, and add the remote again.
- *certificate fingerprint mismatch*: the daemon's certificate was
  replaced, or the address reaches another machine. Compare the fingerprint
  `dicer info` shows on the host with the one the error wants.
- *needs a token*: the remote has none. Add it with `dicer remote create`,
  or set `$DICER_TOKEN`.

## A start is refused

The error says why, and nothing was started.

```text
Error: instance "web" needs 4 vCPU, 8 GiB, but 14 vCPU, 26 GiB of the 16 vCPU, 30 GiB this host allows is committed
```

The host has too little room left for the instance, counting what is
committed to other instances. Stop something, give the instance less, or change the
overcommit. See [Capacity](../capacity).

```text
Error: 1 vCPU, 4 TiB is more than this host can give instances in total (16 vCPU, 30 GiB)
```

The instance asks for more than the host allows all instances together, so
it could never start. Give it less, or change the overcommit.

```text
Error: 999 vCPUs is more than the host's 4 CPUs
```

An instance cannot have more vCPUs than the host has CPUs, whatever the
overcommit.

```text
Error: port 18080:80/tcp is already published by instance "web", which is running
```

Another instance has the port. An instance on standby keeps its ports too.
Publish a different port, or stop the other instance.

```text
Error: cannot publish port 18080:80/tcp: something on the host is already using it (…)
```

A process on the host has the port. Publish a different port, or stop the
process.

```text
Error: volume "pgdata" is attached to instance "db", which is running, and a volume can be shared only while every instance mounts it read-only
```

Only one running instance at a time can use a volume read-write. See
[Sharing a volume](../files-and-volumes#sharing-a-volume).

```text
Error: image "no-such-image:1" not found on docker.io (or it is private)
```

The name or tag is wrong, or the registry needs you to log in. See
[Private registries](../managing-images#private-registries).

```text
Error: cannot pull image "…": no child with platform linux/amd64 in index …
```

The image has no build for the host's architecture.

## An instance ends right after starting

`dicer inspect` says how it ended:

```console
$ dicer inspect web
● web — docker.io/library/nginx:1.27

     Active: failed, exited (1) 5 seconds ago
             exit code 1
…
```

An exit code comes from the workload itself. Read what it printed with
`dicer logs web`. It is the same failure the image would have in a
container, such as a missing setting or a wrong command.

If the instance ended in a kernel panic or a reset, or its hypervisor
exited, `dicer inspect` says so instead. The last lines of the console
usually tell you which:

```console
$ dicer logs -n 30 web
```

A workload that exits with code 0, or a guest that powers itself off, ends
**Stopped**, not Failed. That is the workload finishing, not failing. Give
it a command that keeps running, or a [restart policy](../restarts).

## An instance fails with exit code 127 or 126

When the image does not have the instance's command, or cannot run it, the
instance fails straight away, with the exit code a shell would give: 127
when the command is not found, and 126 when it cannot be run. The console
says why:

```console
$ dicer inspect web
     Active: failed, exited (127) 3 seconds ago
$ dicer logs web | tail -2
dicer-init: start /usr/bin/app: exec: "/usr/bin/app": stat /usr/bin/app: no such file or directory
```

To look around the image, run it with a command that waits. Find the right
command, then fix the instance:

```console
$ dicer run -d --name look ghcr.io/acme/app:2 sleep infinity
$ dicer exec look ls -l /usr/local/bin
$ dicer rm -f look
$ dicer update web -- /usr/local/bin/app
$ dicer start web
```

## An instance never boots

When `dicer logs` is empty, or stops during the kernel's boot messages, the
guest did not get far. The hypervisor's own log says why:

```console
$ dicer logs --source hypervisor web
```

Look for:

- **A kernel that does not suit the hypervisor or the host.** The kernel
  must be built for the host's architecture and for a virtual machine.
  [Dicer's kernel](https://github.com/konradasb/dicer-kernel) works under
  both hypervisors. A kernel without EROFS boots, but the console then
  says `mount /dev/vda: no such device`. See
  [Kernel requirements](../../concepts/kernels#kernel-requirements).
- **Kernel arguments that were replaced.** `--kernel-args` replaces the
  defaults entirely, including the console and panic handling. See
  [Kernel arguments](../../concepts/kernels#kernel-arguments).
- **`/dev/kvm` missing or not usable.** Then no instance boots. Check that
  the host supports virtualisation and that it is enabled.

The hypervisor's log is kept with the instance until the instance is
deleted. Each start adds to it, so the latest failure is at the end:

```console
$ dicer logs --source hypervisor -n 50 web
```

## An instance does not wake on a connection

See [Standby](../standby#an-instance-does-not-wake-on-a-connection) for what
wakes an instance, and what the daemon logs when it cannot.

## The network does not work

### A guest cannot reach the outside world

- A guest on an [internal network](../../concepts/networking#internal-networks)
  cannot reach it, by design. `dicer network ls` shows which networks are
  internal.
- Starting an instance fails with `IPv4 forwarding is not enabled` when
  the host does not forward packets. The package and `install.sh` turn
  forwarding on, but another setting may have turned it off again. To turn
  it on yourself:

  ```console
  $ sudo sysctl -w net.ipv4.ip_forward=1
  $ echo net.ipv4.ip_forward=1 | sudo tee /etc/sysctl.d/99-dicer.conf
  ```

- Traffic leaves through the interface of the host's default route. On a
  host with several interfaces, set
  [`network.uplink_interface`](../../reference/configuration#network-uplink-interface)
  in the configuration.
- The daemon looks up names outside the network on the guests' behalf. It
  asks `8.8.8.8` unless the network names other nameservers. If your
  network blocks it, create the network with `--nameservers`.
- On a host that runs firewalld (Fedora, RHEL and its rebuilds, openSUSE),
  the network's bridge must be in the `dicer` zone, or firewalld turns the
  guests' traffic away. [firewalld](../../concepts/networking#firewalld)
  shows how to check it, and how to install the zone if the host has none.

### A guest cannot resolve names

Guests ask the daemon's DNS server on their network's gateway, as
`cat /etc/resolv.conf` in the guest shows. Suppose every lookup fails with
`Temporary failure in name resolution`, even for other instances' names,
and the guest's connections to port 53 on the gateway fail with
`No route to host`. Then the host's firewall is turning them away.

On hosts that run firewalld, the `dicer` zone lets DNS in. Check that the
bridge is in it, as [firewalld](../../concepts/networking#firewalld) shows.

With [`network.dns: false`](../../reference/configuration#network-dns) in
the daemon's configuration, guests ask the network's nameservers directly
instead, and can find each other only by address.

### A published port cannot be reached

- On the host itself, `localhost` does not reach published ports. Use the
  host's own address, or the guest's.
- Check that the workload listens on all of the guest's addresses, not only
  on its loopback address: `dicer exec web ss -ltn`.
- Check that the host's firewall lets the port in.

### A guest cannot reach a service on the host

`host.dicer.internal` is the host's address on the guest's network. The
service must listen on that address or on all of the host's addresses. A
service listening only on `127.0.0.1` cannot be reached from a guest. The
host's firewall must also let the port in. With firewalld, add the port to
the `dicer` zone, as [Networking](../../concepts/networking#firewalld)
shows. A guest on an
[internal network](../../concepts/networking#internal-networks) cannot reach
the host at all, and the daemon's API cannot be reached from any guest.

### Two instances cannot reach each other

Instances on different networks cannot reach each other, by design. Nor can
instances on the same network if it was created with `--isolated`. See
[Isolation](../../concepts/networking#isolation).

## `dicer exec`, `cp` or health checks fail

These go through the guest's agent, which runs only while the instance is
running:

- `instance "web" is paused, not running`: resume it first, with
  `dicer resume web`.
- `instance "web" is standby, not running`: the instance is on standby.
  Resume it first, with `dicer start web`.

An error with `code = Unavailable` in it, such as
`open guest exec stream: rpc error: code = Unavailable …`, means the agent
did not answer:

- The guest may still be booting. Try again in a moment.
- In a guest that boots systemd, the agent is a unit,
  `dicer-agent.service`, and starts only once systemd has. A guest stuck
  early in its boot has no agent yet. `dicer logs` shows how far it got.

## The daemon misbehaves

Restarting the daemon is safe. Running guests keep running, and the new
daemon adopts them.

```console
$ sudo systemctl restart dicerd
$ journalctl -u dicerd -n 100
```

If the daemon will not start, its log says why. The usual cause is an error
in `/etc/dicerd/config.yaml`. `sudo dicerd validate` names the line,
without starting anything. See
[Operating the daemon](../operating-the-daemon#changing-the-configuration).

## Reporting a problem

When you ask for help, include:

- the output of `dicer version` and `dicer info`;
- the commands you ran and what they printed;
- the relevant parts of `dicer logs`, `dicer logs --source hypervisor` and
  `journalctl -u dicerd`.
