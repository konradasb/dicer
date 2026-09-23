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

A container image holds no kernel: containers share their host's. A virtual
machine needs one of its own, so every instance boots a kernel you choose,
kept apart from its image.

## Importing a kernel

A kernel is imported by name, from a URL, for an architecture:

```console
$ dicer kernel import linux-6.18 --arch x86_64 \
    --url https://github.com/konradasb/dicer-kernel/releases/download/v6.18.53-1/vmlinux-x86_64 \
    --sha256 ca5db6c291deb8a409db1f1ab14cc55ef6d35504daf17fc0f5577ffc1662b669
```

Importing records the kernel; it is downloaded the first time an instance
boots with it, and kept. With `--sha256`, the download is checked against it.
A URL may also be a `file://` URL or an absolute path on the host.

The kernel's architecture must be the host's: `x86_64` or `aarch64`.

## Kernel requirements

[dicer-kernel](https://github.com/konradasb/dicer-kernel) publishes the kernel
Dicer is tested with, for x86_64 and aarch64: a long-term Linux release from
kernel.org, with Cloud Hypervisor's configuration and what Dicer's guests need
on top. It boots under both [hypervisors](../hypervisors). Each release lists
its checksums in a `SHA256SUMS` signed with cosign, and carries the kernel's
source and configuration.

A kernel of your own works too, if it has what Dicer's guests rely on:

- EROFS, with compression, to mount the image;
- overlayfs, to lay the instance's disk over it;
- virtio's block, network and vsock devices, and virtio-mmio for Firecracker;
- a serial console.

The configuration dicer-kernel adds, in its `dicer.config`, is a good place
to start.

## Choosing a kernel

An instance names its kernel with `--kernel`. One that names none gets the
daemon's default: the kernel its configuration names, or, if there is exactly
one kernel, that one. `dicer info` shows which applies.

## Kernel arguments

An instance boots with the command line its hypervisor needs, such as
`console=ttyS0 reboot=k panic=1`. `--kernel-args` replaces it whole, so keep
those arguments when adding your own.
