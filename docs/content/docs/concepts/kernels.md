---
title: Kernels
weight: 4
description: "Why every instance boots a kernel of its own, and how to import one."
icon: chip
related:
  - /docs/concepts/hypervisors
  - /docs/concepts/images
  - /docs/guides/troubleshooting
---

A container image holds no kernel, because containers share their host's. A
virtual machine needs one of its own, so every instance boots a kernel you
choose, kept apart from its image.

## Importing a kernel

A kernel is imported by name, from a URL, for an architecture:

```console
$ dicer kernel import linux-6.18 --arch x86_64 \
    --url https://github.com/konradasb/dicer-kernel/releases/download/v6.18.53-1/vmlinux-x86_64 \
    --sha256 ca5db6c291deb8a409db1f1ab14cc55ef6d35504daf17fc0f5577ffc1662b669
```

Importing only records the kernel. It is downloaded the first time an
instance boots with it, and kept. With `--sha256`, the download is checked
against that checksum. The URL can also be a `file://` URL or an absolute
path on the host.

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
gets the daemon's default kernel. That is the kernel set as
[`defaults.kernel`](../../reference/configuration#defaults-kernel) in the
daemon's configuration, or, if that is unset and there is exactly one
kernel, that one. `dicer info` shows which applies.

## Kernel arguments

An instance boots with the kernel command line its hypervisor needs:

| Hypervisor | Default arguments |
|---|---|
| Cloud Hypervisor | `console=ttyS0 reboot=k panic=1` |
| Firecracker | `console=ttyS0 reboot=k panic=1 pci=off` |

`--kernel-args` replaces the default whole, so keep these arguments when you
add your own. Without `panic=1`, for example, a kernel panic hangs the guest
instead of ending the instance.
