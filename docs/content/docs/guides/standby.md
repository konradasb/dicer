---
title: Standby
weight: 5
description: "Free an idle instance's CPU and memory, and wake it when a connection arrives."
icon: moon
related:
  - /docs/concepts/instances
  - /docs/guides/capacity
  - /docs/guides/snapshots
---

An instance on [standby](../../concepts/instances#standby) has its guest
frozen to disk and its hypervisor ended, so no CPU or memory is committed to
it. Starting it resumes it where it was. Standby suits instances that sit
idle for long stretches, such as preview environments and internal tools.
This guide covers putting an instance on standby by hand, letting the daemon
do it when the instance is idle, and waking it when a connection arrives.

## Put an instance on standby

`dicer standby` puts a running or paused instance on standby:

```console
$ dicer standby web
Instance web put on standby in 1.3s
```

`dicer start` resumes it where it left off. Its processes carry on as if
nothing had happened, except that its clock is set to the current time.
Resuming usually doesn't wait for the guest's memory to be restored: each
page is [restored when the guest first uses it](../snapshots#how-fast-a-restore-is).
Like a start, resuming needs room on the host for the instance's vCPUs and
memory. See [Capacity](../capacity#when-resources-are-committed).
Connections the instance had open to other machines have probably been
closed by the other end in the meantime.

`dicer stop` discards the frozen guest, and the instance boots afresh the
next time it starts. `dicer restart` does both.

## Automatic standby

An instance given `--standby-after`, or `standby_after` in a compose file, is
put on standby once it has been idle that long. The shortest timeout is one
minute.

```console
$ dicer run -d --name preview -p 8080:80 --standby-after 15m nginx:1.27
```

The daemon samples each such running instance once a minute, from the host,
as `dicer stats` sees it. A minute is idle if the guest used under 5% of one
vCPU and its network carried under one packet a second. That leaves room for
background traffic such as NTP. The instance is put on standby after an
unbroken run of idle minutes as long as its `standby_after`, and one busy
minute starts the count again. A paused instance is never put on standby,
because it was paused on purpose.

An instance that waits for work it fetches itself, such as by polling a
queue or a CI service, uses so little while it waits that it may look idle.
Don't give such an instance `--standby-after`.

`dicer update --standby-after` changes the timeout while the instance runs,
and `0` turns it off. The change applies at once.

`dicer events --name preview` shows when the instance was put on standby,
and how long it had been idle.

## Waking on a connection

An instance with `--standby-after` also wakes up by itself. While it is on
standby, the daemon listens on the TCP ports it publishes with `-p`. When a
connection arrives on one of them, the daemon resumes the instance, which
takes a second or so, and then relays the connection to the guest. The
client sees a slow first connection rather than a refused one. Once the
instance is running, new connections reach the guest directly, as usual. A
connection that arrives at the exact moment the daemon hands the ports back
to the instance can be refused.

Some traffic does not wake an instance:

- UDP, on any port.
- Connections to the guest's own IP address, from the host or from other
  instances.
- Connections to the host's loopback address, for a port published on all
  addresses.

An instance without `--standby-after` is never woken by a connection, even
if it was put on standby with `dicer standby`. The Started event of an
instance woken by a connection names the port the connection came to. See
[Events](../../reference/events#how-an-instance-was-started).

## Disk space

A frozen guest takes as much disk space as the instance's memory, because
all of its memory is written out. It is kept in the instance's directory,
beside its overlay disk, until the instance is started or stopped. See
[Monitoring](../monitoring) to keep an eye on the host's disk.

## While it is on standby

An instance on standby cannot be changed or renamed until it is stopped,
apart from its restart policy and `--standby-after`.

It stays on standby across a reboot of the host, and its restart policy
does not start it when the host boots. A release of Dicer can drop the
hypervisor version that froze it, and the guest can then no longer be
resumed. See
[Before a version is removed](../../concepts/hypervisors#before-a-version-is-removed).

An instance that can write to a volume can be put on standby, because it
keeps the volume to itself while it is frozen. See
[Volumes and frozen guests](../files-and-volumes#volumes-and-frozen-guests).

## Troubleshooting

### An instance does not wake on a connection

Check that the instance has `--standby-after`, and that the connection is
TCP, to one of its published host ports. Some traffic never wakes it, as
[Waking on a connection](#waking-on-a-connection) lists.

If a connection is closed straight away, the daemon could not resume the
instance, often because the host has no room for its vCPUs and memory. The
daemon's log says why, in a warning such as
`cannot wake an instance on standby`. The instance stays on standby, and the
next connection tries again. The guest also has 10 seconds after it resumes
to accept the connection. If it does not, the connection is closed and the
daemon logs `the woken guest did not accept the connection that woke it`.

If another program on the host took one of the instance's ports while it
was on standby, connections to that port cannot wake it. The daemon logs
`cannot listen to wake an instance on standby` when that happens.

While the daemon is stopped, nothing listens on the ports, and no instance
on standby can be woken. Start the instance with `dicer start`.
