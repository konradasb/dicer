---
title: Quickstart
weight: 2
description: "From a fresh install to a web server running in a virtual machine."
icon: lightning-bolt
related_title: Next steps
related:
  - /docs/guides/running-workloads
  - /docs/guides/files-and-volumes
  - /docs/guides/restarts
  - /docs/concepts/how-dicer-works
---

From a fresh [install](../installation) to a web server running in a
virtual machine. The commands assume you are in the `dicer` group, as
[Installation](../installation#use-dicer-without-sudo) sets up; if not, run
them with `sudo`.

{{% steps %}}

### Create a network

Instances need a network to be attached to. Create one, with a private
subnet of your choice:

```console
$ dicer network create default --subnet 172.20.0.0/16
```

Its gateway is the subnet's first address, `172.20.0.1`, on a bridge the
daemon creates. See [Networking](../../concepts/networking).

### Import a kernel

A container image has no kernel; a virtual machine needs one. Import
[Dicer's kernel](https://github.com/konradasb/dicer-kernel), a long-term
Linux release built for Dicer's guests:

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

It is downloaded the first time an instance boots with it, and checked
against the checksum. See
[Kernels](../../concepts/kernels).

### Run an image

With one network and one kernel, instances use them without being told:

```console
$ dicer run --name web -p 8080:80 nginx:1.27
Instance web started in 1.1s (172.20.61.102)
```

Dicer pulled `nginx:1.27`, converted it to a disk, and booted it as a
virtual machine, with its port 80 published on the host's port 8080.

### Reach it

From another machine, or from the host by the host's own address:

```console
$ curl -sI http://192.0.2.10:8080 | head -1
HTTP/1.1 200 OK
```

Published ports are not reachable through `localhost` on the host itself;
from the host, use its address or the guest's, which `dicer run` printed.

### Look inside

```console
$ dicer ps
$ dicer logs web
$ dicer exec web
```

`dicer logs` shows the guest's console: the kernel booting, then nginx.
`dicer exec` opens a shell in the guest; `exit` leaves it.

### Clean up

```console
$ dicer stop web
$ dicer rm web
```

{{% /steps %}}
