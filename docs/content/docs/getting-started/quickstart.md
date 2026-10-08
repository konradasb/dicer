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

This page takes you from a fresh [install](../installation) to a web server
running in a virtual machine. The commands assume you are in the `dicer`
group, as [Installation](../installation#use-dicer-without-sudo) sets up. If
you are not, run them with `sudo`.

{{% steps %}}

### Run an image

```console
$ dicer run -d --name web -p 8080:80 nginx:1.27
Instance web started in 1.1s (172.20.61.102)
```

Dicer pulled `nginx:1.27`, showing its progress, and converted it to a
disk. Then it booted the image as a virtual machine, with the
guest's port 80 published on the host's port 8080. The address in brackets
is the guest's.

The instance joined the `default` network and booted the `default` kernel.
The daemon sets up both itself, and an instance uses them unless it names
others. See [Networking](../../concepts/networking) and
[Kernels](../../concepts/kernels).

### Reach it

Reach the published port from another machine, or from the host by the
host's own address. Here the host is `192.0.2.10`:

```console
$ curl -sI http://192.0.2.10:8080 | head -1
HTTP/1.1 200 OK
```

A published port is not reachable through `localhost` on the host itself.
From the host, use the host's address, or the guest's address and port:

```console
$ curl -sI http://172.20.61.102 | head -1
HTTP/1.1 200 OK
```

### Look inside

```console
$ dicer ps
$ dicer logs web
$ dicer exec web
```

`dicer ps` lists the instances. `dicer logs` shows the guest's console: the
kernel booting, then nginx. `dicer exec` opens a shell in the guest, and
`exit` leaves it.

### Clean up

```console
$ dicer stop web
$ dicer rm web
```

{{% /steps %}}
