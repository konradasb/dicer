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

An instance can run Docker like any Linux machine. Its Docker is its own.
Behind the hypervisor, it cannot see the host's Docker or another
instance's, and its containers cannot reach them.

## Setup

{{% steps %}}

### Import Dicer's kernel

Docker builds its networks from netfilter, NAT and bridges.
[Dicer's kernel](https://github.com/konradasb/dicer-kernel) has them from
`v6.18.53-1`. Skip this step if you imported it in the
[quickstart](../../getting-started/quickstart).

{{< tabs >}}
  {{< tab name="x86_64" >}}
  ```console
  $ dicer kernel import linux-6.18 --arch x86_64 \
      --url https://github.com/konradasb/dicer-kernel/releases/download/v6.18.53-1/vmlinux-x86_64 \
      --sha256 ca5db6c291deb8a409db1f1ab14cc55ef6d35504daf17fc0f5577ffc1662b669
  ```
  {{< /tab >}}
  {{< tab name="aarch64" >}}
  ```console
  $ dicer kernel import linux-6.18 --arch aarch64 \
      --url https://github.com/konradasb/dicer-kernel/releases/download/v6.18.53-1/Image-arm64 \
      --sha256 1ce335854bc05535584dd57638f10832db91c4a20cbb76bab7851890c3d14568
  ```
  {{< /tab >}}
{{< /tabs >}}

### Create a volume for Docker's data

```console
$ dicer volume create docker-data --size 20GiB
```

Size it for the images and containers Docker will keep. [Storage](#storage)
explains why Docker needs a volume.

### Run Docker

```console
$ dicer run -d --name docker --kernel linux-6.18 --vcpus 2 --memory 2GiB \
    --mount source=docker-data,target=/var/lib/docker \
    docker:27-dind
```

`docker:27-dind` is Docker's official Docker-in-Docker image, which runs the
Docker daemon as its main process. Here that daemon is the instance's
workload. Docker and its containers share the instance's memory, so give it
2 GiB or more.

### Use it

```console
$ dicer exec docker docker info --format '{{.Driver}}'
overlay2
$ dicer exec docker docker run --rm hello-world
```

`docker info` may take a few seconds to answer while `dockerd` starts.

{{% /steps %}}

## Storage

Without a volume, Docker's data would sit on the instance's root filesystem,
which is itself an overlay. Docker cannot stack its own overlays on it, so it
falls back to its `vfs` storage driver. That driver copies every image layer
in full, which makes pulls and builds slow and takes many times the space.

A volume is ext4, and on it Docker uses `overlay2`. The volume also keeps
Docker's images and containers after the instance is deleted, until the
volume itself is deleted. Only one running instance at a time can use a
volume read-write, so each instance that runs Docker needs a volume of its
own.

## Networking

### iptables

Dicer's kernel has iptables' legacy tables, not nftables. `docker:dind` uses
the legacy tables by itself. An image that installs Docker from a
distribution's packages may default to nftables, and then `dockerd` fails
with `Failed to initialize nft: Protocol not supported`. Switch such an
image to the legacy tables when you build it. On Debian or Ubuntu:

```dockerfile
RUN update-alternatives --set iptables /usr/sbin/iptables-legacy
```

### IPv6

The kernel has no IPv6. Docker warns that it cannot set up `ip6tables`, and
carries on over IPv4.

### Published ports

`docker run -p` publishes a container's port on the instance, not on the
host. To reach it from outside the host, publish the same port on the
instance too, with `dicer run -p`. For example, with `-p 8080:8080` added to
the `dicer run` above, this serves nginx on the host's port 8080:

```console
$ dicer exec docker docker run -d -p 8080:80 nginx:1.27
```

## Images

Each instance's Docker pulls its images for itself. Instances share no image
cache with each other or with the host. Where many instances pull the same
images, a pull-through registry cache near the host saves time and
bandwidth.
