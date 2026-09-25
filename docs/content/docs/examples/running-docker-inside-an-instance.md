---
title: Running Docker inside an instance
weight: 1
description: "Run Docker inside an instance, to build images and run containers in a machine of their own."
icon: cube
related:
  - /docs/guides/files-and-volumes
  - /docs/guides/working-inside-guests
  - /docs/concepts/kernels
  - /docs/guides/capacity
---

An instance can run Docker like any Linux machine. Its Docker is its own:
behind the hypervisor, it cannot see the host's Docker, or another
instance's, and its containers cannot reach them.

## Setup

{{% steps %}}

### Import Dicer's kernel

Docker builds its networks from netfilter, NAT and bridges, which
[Dicer's kernel](https://github.com/konradasb/dicer-kernel) has from
`v6.18.53-1`. Skip this if you imported it in the
[quickstart](../../getting-started/quickstart).

```console
$ sudo dicer kernel import linux-6.18 --arch x86_64 \
    --url https://github.com/konradasb/dicer-kernel/releases/download/v6.18.53-1/vmlinux-x86_64 \
    --sha256 ca5db6c291deb8a409db1f1ab14cc55ef6d35504daf17fc0f5577ffc1662b669
```

### Create a volume for Docker's data

```console
$ sudo dicer volume create docker-data --size 20GiB
```

Size it for the images and containers Docker will keep. See
[Storage](#storage) for why Docker needs one.

### Run Docker

```console
$ sudo dicer run --name docker --kernel linux-6.18 --vcpus 2 --memory 2GiB \
    --mount source=docker-data,target=/var/lib/docker \
    docker:27-dind
```

`docker:27-dind` is Docker's own image for running Docker as a machine's
workload. Docker, and what it runs, share the instance's memory: give it
2 GiB or more.

### Use it

```console
$ sudo dicer exec docker docker info --format '{{.Driver}}'
overlay2
$ sudo dicer exec docker docker run --rm hello-world
```

`docker info` may take a few seconds to answer while `dockerd` starts.

{{% /steps %}}

## Storage

Without a volume, Docker's data would sit on the instance's root filesystem,
which is itself an overlay. Docker cannot stack its own overlays on it, so it
falls back to its `vfs` storage driver, which copies every image layer in
full: slow to pull and build, and many times the size.

A volume is ext4, and on it Docker uses `overlay2`. It also keeps Docker's
images and containers when the instance is deleted, until the volume is. A
volume is used read-write by one running instance at a time, so each
instance running Docker needs one of its own.

## Networking

**iptables.** The kernel has iptables' legacy tables, not nftables.
`docker:dind` uses the legacy tables by itself. An image that installs
Docker from a distribution's packages may default to nftables, and then
`dockerd` fails with `Failed to initialize nft: Protocol not supported`.
Switch it to the legacy tables when building the image, on Debian or Ubuntu:

```dockerfile
RUN update-alternatives --set iptables /usr/sbin/iptables-legacy
```

**IPv6.** The kernel has none. Docker warns that it cannot set up
`ip6tables`, and carries on over IPv4.

**Published ports.** A container's port published with `docker run -p` is
published on the instance, not the host. To reach it from outside the host,
publish the same port on the instance too, with `dicer run -p`.

## Images

Each instance's Docker pulls its images for itself: instances share no image
cache with each other or the host. Where many instances pull the same images,
a pull-through registry cache near the host saves time and bandwidth.
