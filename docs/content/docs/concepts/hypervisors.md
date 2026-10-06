---
title: Hypervisors
weight: 7
description: "Cloud Hypervisor and Firecracker: choosing one, and their versions."
icon: server
related:
  - /docs/concepts/kernels
  - /docs/guides/snapshots
  - /docs/guides/troubleshooting
---

Each instance runs under a hypervisor: the virtual machine monitor that runs
its guest. Dicer carries two, built into `dicerd`:

- **[Cloud Hypervisor](https://www.cloudhypervisor.org)**, the default.
- **[Firecracker](https://firecracker-microvm.github.io)**, with
  `--hypervisor-type firecracker`.

Both run the same images and kernels, and support everything Dicer does with
an instance, pausing and snapshots included.

## Versions

A daemon carries more than one version of a hypervisor, so that a new one can
be tried without giving up the old. `dicer info` lists them, the default
first:

```console
$ dicer info
    Hypervisors: cloud-hypervisor v53.0.0 (default), v49.0.0, v48.0.0
                 firecracker v1.17.0
```

An instance runs the default version of its hypervisor unless it names one
with `--hypervisor-version`. When the default changes, an instance that
names no version moves to the new one the next time it boots. Resuming it
from [standby](../instances) or a memory [snapshot](../../guides/snapshots)
does not move it, because only the version that froze the guest can resume
it.

### Support and deprecation

- **The default is the newest release Dicer supports.** A new hypervisor
  release is added, as the default, once it passes Dicer's end-to-end tests.
  A patch release is a version of its own.
- **An older version is deprecated once it is no longer the default**, and
  kept for what still uses it: instances that name it, guests running or
  frozen on it, and memory snapshots it took.
- **A deprecated version is kept for at least two minor releases of Dicer
  after the one that deprecated it.** One deprecated in 0.4.0 is still there
  in 0.5 and 0.6, and is removed in 0.7.0 at the earliest. The release notes
  say when a version is deprecated, and list its removal among the breaking
  changes.
- **A version with a security flaw its project will not fix may be removed
  sooner**, in any release, whose notes say so.

The versions this release of Dicer carries:

| Hypervisor | Version | Status |
|---|---|---|
| Cloud Hypervisor | v53.0.0 | Default |
| Cloud Hypervisor | v49.0.0 | Deprecated in 0.4.0 |
| Cloud Hypervisor | v48.0.0 | Deprecated in 0.4.0 |
| Firecracker | v1.17.0 | Default |

### Before a version is removed

Move off a deprecated version before upgrading to a release without it.
After the upgrade, what still uses it is stuck:

| Still on the version | After the upgrade | Before it |
|---|---|---|
| An instance that names it | Refuses to start | Name another with `dicer update --hypervisor-version`, or none for the default |
| A running instance | Keeps running, but cannot be paused, resized, snapshotted or put on standby, and stopping it ends its hypervisor without a clean shutdown | Restart it |
| An instance on standby | Cannot be resumed; stopping it discards the guest | Start it, then restart it |
| A memory snapshot | Cannot be restored or forked | Restore or fork it, restart the instance, and take a new snapshot |

Disk snapshots need no hypervisor and are unaffected.

## Differences

| | Cloud Hypervisor | Firecracker |
|---|---|---|
| Kernel command line | `console=ttyS0 reboot=k panic=1` | the same, and `pci=off` |
| Ending a guest | powers off | resets, as it has no power button |

How a guest ends is `dicer-init`'s business, so the difference does not show:
an instance ends the same way under either.
