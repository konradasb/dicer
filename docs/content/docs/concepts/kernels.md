---
title: Kernels
weight: 4
description: "Why every instance boots a kernel of its own, the default kernel, and how to import another."
icon: chip
related:
  - /docs/concepts/hypervisors
  - /docs/concepts/images
  - /docs/guides/troubleshooting
---

A container image holds no kernel, because containers share their host's. A
virtual machine needs one of its own, so every instance boots a kernel, kept
apart from its image.

## The default kernel

The daemon defines a kernel named `default`: a release of
[Dicer's kernel](#kernel-requirements) for the host's architecture, which
`dicerd` carries inside its own binary, as it carries the hypervisors. An
instance that names no kernel boots it. The daemon puts it on the host when
it starts, so it needs no network, and puts it back if its copy goes
missing or is damaged.

A new version of Dicer may carry a newer release. When the daemon is
upgraded, it replaces the default kernel with the new one. Instances that
use the default kernel boot the new one the next time they start. A running
instance keeps the kernel it booted, and so does one restored from a
snapshot or resumed from standby.

The default kernel cannot be deleted, and no other kernel can be imported
under its name.

## Importing a kernel

To boot a kernel of your own, import it by name, for an architecture, from
a file on the machine you run `dicer` on:

```console
$ dicer kernel import custom-6.18 ./vmlinux-6.18 --arch x86_64 \
    --sha256 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
```

The file is sent to the daemon, up to 512 MiB, and the import returns once
the kernel is on the daemon's host, so an instance can boot it at once.
With `--sha256`, the kernel is checked against that checksum, and nothing is
imported if it does not match. An empty file is refused. The daemon never
reads a kernel from a path on its own host.

A kernel stays on the host for as long as it is listed, and is checked
against its checksum each time an instance boots it. If its copy goes
missing or is damaged, the instance fails to start, and says so: delete the
kernel and import it again. The daemon puts the default kernel back when it
next starts.

The kernel's architecture must be the host's: `x86_64` or `aarch64`.

## Kernel requirements

[dicer-kernel](https://github.com/konradasb/dicer-kernel) publishes the kernel
Dicer is tested with, for x86_64 and aarch64. It is a long-term Linux release
from kernel.org, built with Cloud Hypervisor's configuration plus what
Dicer's guests need. It boots under both [hypervisors](../hypervisors). Each
release lists its checksums in a `SHA256SUMS` file signed with cosign, and
carries the kernel's source and configuration.

You can use a kernel of your own if it has what Dicer's guests rely on:

- EROFS with LZ4 compression, to mount the image;
- ext4, for the overlay disk and volumes;
- overlayfs, to lay the overlay disk over the image;
- virtio block, network and vsock devices, and virtio-mmio for Firecracker;
- a serial console.

The configuration that dicer-kernel adds, in its `dicer.config`, is a good
place to start.

## Choosing a kernel

An instance names its kernel with `--kernel`. An instance that names none
boots the [default kernel](#the-default-kernel). Importing other kernels
does not change that.

## Kernel arguments

An instance boots with the kernel command line its hypervisor needs:

| Hypervisor | Default arguments |
|---|---|
| Cloud Hypervisor | `console=ttyS0 reboot=k panic=1` |
| Firecracker | `console=ttyS0 reboot=k panic=1 pci=off` |

`--kernel-args` replaces the default whole, so keep these arguments when you
add your own. Without `panic=1`, for example, a kernel panic hangs the guest
instead of ending the instance.
