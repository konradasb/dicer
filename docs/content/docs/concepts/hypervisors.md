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

Both run the same images and kernels. Both support pausing, standby,
snapshots, forks and rate limits. The few things that differ are listed under
[Differences](#differences).

## Versions

A daemon carries more than one version of a hypervisor, so that a new one can
be tried without giving up the old. `dicer info` lists them, the default
first:

```console
$ dicer info
    Hypervisors: cloud-hypervisor v53.0.0 (default), v49.0.0 (deprecated), v48.0.0 (deprecated)
                 firecracker v1.17.0
```

An instance runs the default version of its hypervisor unless it names one
with `--hypervisor-version`. When the default changes, an instance that
names no version moves to the new one the next time it boots. Resuming it
from [standby](../instances#standby), or restoring or forking a memory
[snapshot](../../guides/snapshots), does not move it, because only the
version that froze the guest can resume it.

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

Dicer says when something uses a deprecated version. `dicer info` marks it,
and the API reports it in `HypervisorInfo.deprecated_versions`. `dicer run`,
`dicer create`, `dicer update` and `dicer compose up` warn when an instance
names it. The daemon logs a warning whenever it boots an instance on it.
Each time the daemon starts, it also logs a warning for each instance, guest
and memory snapshot that still uses it.

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
| An instance that names it | Refuses to start | Name another with `dicer update --hypervisor-version`, or `--hypervisor-version ""` for the default |
| A running instance | Keeps running, but cannot be paused, resized, snapshotted or put on standby, and stopping it ends its hypervisor without a clean shutdown | Restart it |
| An instance on standby | Cannot be resumed; stopping it discards the guest | Start it, then restart it |
| A memory snapshot | Cannot be restored or forked | Restore or fork it, restart the instance, and take a new snapshot |

Disk snapshots need no hypervisor and are unaffected.

## Differences

| | Cloud Hypervisor | Firecracker |
|---|---|---|
| Resizing vCPUs with `dicer resize` | Yes | No: it cannot add vCPUs to a running guest |
| Resizing memory with `dicer resize` | Returns without waiting, as it cannot tell when the guest has taken the change | Waits for the guest to take the change |
| Restoring a memory snapshot | v53: on demand, then the rest in the background. v48 and v49: all of the memory before the guest resumes | On demand, only the pages the guest uses |
| Kernel command line | `console=ttyS0 reboot=k panic=1` | The same, and `pci=off` |
| How the guest ends its hypervisor | It powers off | It resets, as Firecracker has no power button |

How a guest ends its hypervisor is `dicer-init`'s business, so that
difference does not show: an instance ends the same way under either.

Firecracker places the memory a guest can grow into at 512 GiB in the
guest's address space. A host CPU with fewer than 40 bits of physical
address cannot reach it, so there Firecracker's guests cannot be given more
memory. See [Resizing a running instance](../../guides/capacity#resizing-a-running-instance)
and [How fast a restore is](../../guides/snapshots#how-fast-a-restore-is).
