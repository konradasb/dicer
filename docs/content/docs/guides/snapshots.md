---
title: Snapshots
weight: 5
description: "Freeze an instance to disk and put it back exactly as it was."
icon: camera
related:
  - /docs/concepts/hypervisors
  - /docs/concepts/instances
  - /docs/guides/files-and-volumes
---

A snapshot freezes an instance to disk, to put it back as it was later: before
an upgrade, before an experiment, or as a known-good point to return to.

| Kind | Taken of | Holds | Restoring it |
|---|---|---|---|
| Memory | A running or paused instance | Its memory, device state and disk | Resumes the guest where it was |
| Disk | A stopped instance | Its disk alone | Rolls the disk back; the guest boots from it afresh |

Neither holds the instance's [volumes](../files-and-volumes#volumes), which
are their own storage.

## Taking a snapshot

```console
$ dicer snapshot create web before-upgrade
Snapshot before-upgrade of instance web created in 1.4s (memory, 212 MiB)
```

Without a name, the snapshot is named after the instance and the time, as in
`web-20261005t142000z`. Names are the host's: two instances' snapshots cannot
share one.

A running instance is paused while its memory is written and its disk
copied, and resumed afterwards; a paused one is left paused. On a filesystem
that can reflink, XFS or Btrfs, the copy of the disk takes no time and no
space until the guest writes to it again. On one that cannot, such as ext4,
the disk is copied in full, and the guest stays paused for all of it: the
[event](../../reference/events#snapshots) of the snapshot says for how long.

A memory snapshot is refused for an instance that can write to a volume: the
volume would move on while the snapshot did not, and the guest restored from
it would remember a volume that is no longer there. Stop the instance for a
disk snapshot instead, or mount its volumes `readonly`.

## Restoring

```console
$ dicer stop web
$ dicer snapshot restore before-upgrade
Instance web restored from snapshot before-upgrade in 0.6s (10.88.0.5)
```

The instance must be stopped. Whatever it has written to its disk since the
snapshot is discarded; if the restore fails, the instance keeps the disk it
had.

A memory snapshot can be restored only where it was taken: the guest wakes
with the address and mounts it had, so an instance whose
[network or static IP](../running-workloads) or mounts have changed since
refuses it. The snapshot is restored with the vCPUs and memory the guest had,
and needs room for them on the host. It needs too the
[hypervisor version](../../concepts/hypervisors) that took it, which is kept
across upgrades only as long as the daemon carries that version.

Renaming an instance keeps its snapshots restorable.

## Listing and deleting

```console
$ dicer snapshot list
Name             Kind     Instance  Hypervisor               Memory   Size      Created
before-upgrade   memory   web       cloud-hypervisor v49.0.0 512 MiB  212 MiB   3 minutes ago
db-nightly       disk     db        -                        -        1.1 GiB   9 hours ago
$ dicer snapshot list --instance web
$ dicer snapshot delete before-upgrade
```

A snapshot outlives the instance it was taken of: deleting the instance keeps
its snapshots until they are deleted themselves, though there is then no
instance to restore them into. Their size is what they take on disk, which
with reflinks is only what has changed since.
