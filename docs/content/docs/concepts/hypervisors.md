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
be tried without giving up the old. `dicer info` lists them:

```console
$ dicer info
    Hypervisors: cloud-hypervisor v49.0.0 (default), v48.0.0
                 firecracker v1.17.0
```

An instance runs the newest version of its hypervisor unless it names one
with `--hypervisor-version`. A [snapshot](../../guides/snapshots) can only be
restored by the version that took it.

## Differences

| | Cloud Hypervisor | Firecracker |
|---|---|---|
| Kernel command line | `console=ttyS0 reboot=k panic=1` | the same, and `pci=off` |
| Ending a guest | powers off | resets, as it has no power button |

How a guest ends is `dicer-init`'s business, so the difference does not show:
an instance ends the same way under either.
