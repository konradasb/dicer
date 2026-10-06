---
title: Snapshots
weight: 6
description: "Freeze an instance to disk and put it back exactly as it was."
icon: camera
related:
  - /docs/concepts/hypervisors
  - /docs/concepts/instances
  - /docs/guides/files-and-volumes
---

A snapshot freezes an instance to disk, so that you can put it back as it
was later. Take one before an upgrade or an experiment, or keep one as a
known-good point to return to. A snapshot can also be forked into new
instances, each a copy of the one it was taken of.

There are two kinds of snapshot, and the instance's state decides which you
get:

| Kind | Taken of | Holds | Restoring it |
|---|---|---|---|
| Memory | A running or paused instance | Its memory, device state and overlay disk | Resumes the guest where it was |
| Disk | A stopped or failed instance | Its overlay disk alone | Rolls the overlay disk back, and the guest boots from it afresh at its next start |

Neither kind holds the instance's [volumes](../files-and-volumes#volumes),
which are storage of their own.

## Taking a snapshot

```console
$ dicer snapshot create web before-upgrade
Snapshot before-upgrade of instance web created in 1.4s (memory, 212 MiB)
```

Without a name, the snapshot is named after the instance and the time, as in
`web-20261005t142000z`. Snapshot names are unique on the host, not per
instance, so snapshots of two different instances cannot share a name.

A running instance is paused while its memory is written and its overlay
disk is copied, then resumed. A paused instance is left paused. On a
filesystem that can reflink, such as XFS or Btrfs, copying the overlay disk
is instant and takes no space until the guest writes to it again. On one
that cannot, such as ext4, it is copied in full, and the guest stays paused
until the copy is done. The snapshot's [event](../../reference/events#snapshots)
records how long it was paused.

An instance on standby, or one that has never been started, cannot be
snapshotted.

A memory snapshot is refused for an instance that can write to a volume.
See [Volumes and frozen guests](../files-and-volumes#volumes-and-frozen-guests).

## Restoring

```console
$ dicer stop web
$ dicer snapshot restore before-upgrade
Instance web restored from snapshot before-upgrade in 612ms (172.20.0.5)
```

The instance must not be running or paused. Restoring discards whatever it
has written to its overlay disk since the snapshot. If the restore fails,
the instance keeps the overlay disk it had.

Restoring a disk snapshot only rolls the overlay disk back, and leaves the
instance stopped:

```console
$ dicer snapshot restore db-nightly
Disk of instance db restored from snapshot db-nightly in 48ms; start it to boot from it
```

### Restoring a memory snapshot

A memory snapshot can be restored only into the instance it was taken of,
with the same address and mounts. The guest wakes up still using them. If
the instance's [network or static IP](../running-workloads#network-address-and-hostname)
or its mounts have changed since, the restore is refused. [Fork](#forking)
the snapshot instead. The guest's clock stood still in the snapshot, so it
is set to the current time as the guest wakes.

The guest is restored with the vCPUs and memory it had, so the host needs
room for them. Renaming the instance doesn't stop its snapshots from being
restored.

Only the [hypervisor version](../../concepts/hypervisors) that took a
memory snapshot can restore or fork it. Once that version is deprecated, a
later release of Dicer removes it, and the snapshot can then no longer be
used. The
[deprecation policy](../../concepts/hypervisors#support-and-deprecation)
says when that happens. Disk snapshots don't depend on a hypervisor
version.

### How fast a restore is

When you restore a memory snapshot, the guest resumes straight away. Dicer
doesn't wait to restore the guest's memory first. Instead, each page of
memory is restored from the snapshot the first time the guest uses it. This
means a guest with a lot of memory restores as quickly as a small one. The
guest runs a little slower until its memory is restored:

- **Cloud Hypervisor** also restores the rest of the memory in the
  background, which takes a few seconds. If you snapshot the guest or put it
  on standby during that time, Dicer waits for the memory to be restored.
  The guest keeps running while Dicer waits.
- **Firecracker** restores only the pages the guest uses. The other pages
  stay in the snapshot file. Forks of the same snapshot share those pages in
  the host's page cache.

Restoring memory on demand needs Cloud Hypervisor v53 or later, and a host
kernel with userfaultfd support. The kernels of all major distributions have
it. Without either, Cloud Hypervisor restores all of the memory before the
guest resumes.

You can delete a snapshot while a guest is still restoring memory from it.
The snapshot's disk space is freed when the guest no longer needs it. Under
Cloud Hypervisor, that is when all the memory has been restored. Under
Firecracker, it is when the guest stops.

## Forking

A fork is a new instance made as a copy of the instance a snapshot was
taken of. Use it to run another copy of a service as it is now, or to stamp
out instances from one you set up once and snapshotted.

```console
$ dicer snapshot fork before-upgrade web-2
Instance web-2 forked from snapshot before-upgrade in 804ms (172.20.0.7)
```

The fork has the same definition and overlay disk as the source instance,
but an identity of its own:

- its own ID and name;
- its own address and MAC, on the same network, or on another given with
  `--network`, at an address given with `--ip` if you want a fixed one;
- no published host ports, since two instances cannot publish the same
  port. Give it ports of its own with `-p`.

It makes no difference whether the source instance is still running, or
still exists.

**A memory snapshot's fork runs at once**, resumed where the snapshot's
guest was, with its processes, open files and memory. It wakes up with the
source instance's address, so Dicer keeps it off the network until the guest agent
has given it its own name and address. Connections the guest had open when
the snapshot was taken are left using the old address, and will not work.

**A disk snapshot's fork is stopped**, and boots from the snapshot's copy
of the overlay disk when you start it:

```console
$ dicer snapshot fork db-nightly db-test
Instance db-test forked from snapshot db-nightly in 31ms; start it to boot it
```

A fork mounts the same volumes as the source instance. Read-only volumes
can be shared by both. A volume the source instance can write to can be
used by only one running instance at a time, so the fork can start only
while the source instance is stopped. See [Sharing a volume](../files-and-volumes#sharing-a-volume).

A memory snapshot taken before Dicer supported forking cannot be forked,
because its guest agent cannot take another identity. Restart the instance
and take a new snapshot.

### Forking an instance

To copy an instance as it is now, fork it directly, without taking a
snapshot first:

```console
$ dicer fork web web-2
Instance web-2 forked from instance web in 1.1s (172.20.0.8)
```

This is the same as taking a snapshot of `web` and forking it, except that
no snapshot is kept. `dicer fork` is short for `dicer instance fork`, and
takes the same flags as `dicer snapshot fork`.

The fork of a running or paused instance runs. As with a snapshot, a
running instance is paused while its memory is written and its overlay
disk is copied. A stopped instance's fork is stopped, and boots from a copy
of its overlay disk. An instance that can write to a volume can be forked
only while it is stopped, for the same reason as a
[memory snapshot](../files-and-volumes#volumes-and-frozen-guests).

## Listing and deleting

```console
$ dicer snapshot list
NAME            KIND    INSTANCE  HYPERVISOR                MEMORY   SIZE     CREATED
before-upgrade  memory  web       cloud-hypervisor v53.0.0  512 MiB  212 MiB  3 minutes ago
db-nightly      disk    db        -                         -        1.1 GiB  9 hours ago
$ dicer snapshot list --instance web
$ dicer snapshot show before-upgrade
$ dicer snapshot delete before-upgrade
```

`MEMORY` is the guest's memory, and `SIZE` is the space the snapshot takes
on disk. With reflinks, that is only what has changed since it was taken.

A snapshot outlives the instance it was taken of. Deleting the instance
keeps its snapshots until you delete them too. You can still fork them, but
there is no longer an instance to restore them into.
