---
title: Troubleshooting
weight: 12
description: "Why an instance will not start or stay up, and how to find out."
icon: support
related:
  - /docs/guides/working-inside-guests
  - /docs/guides/operating-the-daemon
  - /docs/guides/monitoring
---

Most problems announce themselves in one of three places: the error a
command prints, the instance's state and console, or the daemon's log. This
guide starts from what you see and works back to why.

## Where to look

| To learn | Run |
|---|---|
| Why an instance is not running, and how it last ended | `dicer inspect NAME` |
| What the guest said while booting and running | `dicer logs NAME` |
| Why a guest never booted at all | `dicer logs --source hypervisor NAME` |
| What happened to it, and when | `dicer events --name NAME` |
| What the daemon did | `journalctl -u dicerd` |
| What the command line sent and got back | `dicer --debug …` |

For more detail from the daemon, set `log_level: debug` in its
[configuration](../../reference/configuration) and restart it; running
guests are not affected.

## The command line cannot reach the daemon

```text
Error: there is no socket at /run/dicer/dicer.sock; is dicerd running?
```

The daemon is not running. Start it, and see why it stopped:

```console
$ sudo systemctl start dicerd
$ journalctl -u dicerd -n 50
```

```text
Error: connection error: desc = "transport: Error while dialing: dial unix /run/dicer/dicer.sock: connect: permission denied"
```

Only root and the `dicer` group can use the socket: run the command with
`sudo`, or [join the group](../remote-access#on-the-host-itself). Joining
takes effect at your next login.

For a remote daemon, check which one a command reaches with `dicer info`,
and see [Remote access](../remote-access). A TLS error names what did not
match: usually the daemon's certificate lacks the name or address you
connect to, which `--tls-server-name` or a new certificate fixes.

## A start is refused

The error says why; nothing was started.

```text
Error: 1 vCPU, 4 TiB is more than this host can give instances in total (16 vCPU, 30 GiB)
```

The host has too little room left, counting what running instances hold.
Stop something, give the instance less, or change the overcommit; see
[Capacity](../capacity).

```text
Error: 999 vCPUs is more than the host's 4 CPUs
```

An instance cannot have more vCPUs than the host has CPUs, whatever the
overcommit.

```text
Error: port 18080:80/tcp is already published by instance "web", which is running
```

Another instance, or a process on the host, has the port. Publish another,
or stop the other instance.

```text
Error: volume "pgdata" is attached to instance "db", which is running, …
```

A volume is used read-write by one running instance at a time; see
[Files and volumes](../files-and-volumes#sharing-a-volume).

```text
Error: image "no-such-image:1" not found on docker.io (or it is private)
```

The name or tag is wrong, or the registry wants a login; see
[Managing images](../managing-images#private-registries).

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
…
```

An exit code is the workload's own: read what it printed with
`dicer logs web`. It is the same failure the image would have in a
container, such as a missing setting or a wrong command.

An instance that ended in a kernel panic, a reset, or with its hypervisor
gone says so instead. The console's last lines usually tell which:

```console
$ dicer logs -n 30 web
```

A guest that powers itself off, or a workload that exits 0, ends
**Stopped**, not Failed: that is the workload finishing, not failing. Give
it a command that keeps running, or a [restart policy](../restarts).

## The command cannot be started

An instance whose command the image does not have, or cannot run, ends
straight away, **Failed**, as a shell would: with exit code 127 for a command
not found, and 126 for one that cannot be run. Its console says why:

```console
$ dicer inspect web
     Active: failed, exited (127) 3 seconds ago
$ dicer logs web | tail -2
dicer-init: start /usr/bin/app: exec: "/usr/bin/app": stat /usr/bin/app: no such file or directory
```

To look around the image, run it with a command that waits, find the right
one, and fix the instance:

```console
$ dicer run -d --name look ghcr.io/acme/app:2 sleep infinity
$ dicer exec look ls -l /usr/local/bin
$ dicer rm -f look
$ dicer update web -- /usr/local/bin/app
$ dicer start web
```

## An instance never boots

When `dicer logs` is empty or stops during the kernel's boot messages, the
guest did not get far. The hypervisor's own log says why:

```console
$ dicer logs --source hypervisor web
```

Look for:

- **A kernel that does not suit the hypervisor or the host.** The kernel
  must be built for the host's architecture and for a virtual machine;
  [Dicer's kernel](https://github.com/konradasb/dicer-kernel) works under both
  hypervisors. A kernel without EROFS boots, but the console then says
  `mount /dev/vda: no such device`. See [Kernels](../../concepts/kernels).
- **Kernel arguments that were replaced.** `--kernel-args` replaces the
  defaults whole, console and panic handling included.
- **`/dev/kvm` missing or not usable,** in which case no instance boots:
  check that the host supports virtualisation and that it is enabled.

The hypervisor's log is kept only while the instance runs, and is lost when
it stops.

## The network does not work

**The guest cannot reach the outside world.**

- Starting an instance fails with `IPv4 forwarding is not enabled` when the
  host does not forward. `install.sh` turns it on; to do it yourself:

  ```console
  $ sudo sysctl -w net.ipv4.ip_forward=1
  $ echo net.ipv4.ip_forward=1 | sudo tee /etc/sysctl.d/99-dicer.conf
  ```

- Traffic leaves through the interface of the host's default route. On a
  host with several, set `network.uplink_interface` in the configuration.
- Names are resolved with `8.8.8.8` unless the network says otherwise. If
  your network blocks it, create the network with `--nameservers`.

**A published port cannot be reached.**

- From the host itself, `localhost` does not reach published ports; use the
  host's own address, or the guest's.
- Check that the workload listens on all of the guest's addresses, not only
  its loopback: `dicer exec web ss -ltn`.
- Check the host's firewall lets the port in.

**Two instances cannot reach each other.** Instances on different networks
cannot, by design, and neither can instances on the same network created
with `--isolated`. See [Networking](../../concepts/networking#isolation).

## `dicer exec`, `cp` or health checks fail

These go through the guest's agent. `instance "web" is paused, not running`
means just that: resume it first. An `UNAVAILABLE` error means the agent
did not answer:

- The guest may still be booting: try again in a moment.
- In a guest that boots systemd, the agent is a unit, `dicer-agent.service`,
  and starts once systemd has; a guest stuck early in its boot has none.
  `dicer logs` shows how far it got.

## The daemon misbehaves

Restarting it is safe: running guests keep running, and the new daemon
takes them over.

```console
$ sudo systemctl restart dicerd
$ journalctl -u dicerd -n 100
```

If the daemon will not start, its log says why. The usual cause is an error
in `/etc/dicerd/config.yaml`, which `sudo dicerd validate` names with its
line, without starting anything.

## Reporting a problem

When asking for help, include the output of `dicer version` and
`dicer info`, the commands you ran and what they printed, and the relevant
part of `dicer logs`, `dicer logs --source hypervisor` and
`journalctl -u dicerd`.
