---
title: Installation
weight: 1
description: "What a host needs, and how to install Dicer and check it works."
icon: download
related_title: Next steps
related:
  - /docs/getting-started/quickstart
  - /docs/reference/configuration
---

Dicer runs on one Linux host, as a daemon, `dicerd`, managed by systemd, with
the `dicer` command line beside it. The install script builds both from
source and sets the daemon up.

## Requirements

**The host**

- Linux on x86_64 or aarch64, with systemd.
- KVM: `/dev/kvm` must exist. On a cloud virtual machine, that means one
  with nested virtualisation enabled.
- `erofs-utils`, for `mkfs.erofs`, and `e2fsprogs`, for `mkfs.ext4` and
  `mke2fs`.
- `iptables`.

**To build**

- Go 1.25 or later, `git`, `make` and `curl`.

To install them with the distribution's package manager:

{{< tabs >}}
  {{< tab name="Debian, Ubuntu" >}}
  ```console
  $ sudo apt install erofs-utils e2fsprogs iptables git make curl
  ```
  {{< /tab >}}
  {{< tab name="Fedora" >}}
  ```console
  $ sudo dnf install erofs-utils e2fsprogs iptables-nft git make curl
  ```
  {{< /tab >}}
  {{< tab name="Rocky, AlmaLinux" >}}
  `erofs-utils` is in [EPEL](https://docs.fedoraproject.org/en-US/epel/),
  so turn it on first. On RHEL itself, EPEL's page says how.

  ```console
  $ sudo dnf install epel-release
  $ sudo dnf install erofs-utils e2fsprogs iptables-nft git make curl
  ```
  {{< /tab >}}
  {{< tab name="Arch" >}}
  ```console
  $ sudo pacman -S --needed erofs-utils e2fsprogs iptables-nft git make curl
  ```
  {{< /tab >}}
  {{< tab name="openSUSE" >}}
  ```console
  $ sudo zypper install erofs-utils e2fsprogs iptables git make curl
  ```
  {{< /tab >}}
{{< /tabs >}}

and Go from [go.dev/dl](https://go.dev/dl/), as distributions often carry an
older one.

Check that KVM is there:

```console
$ ls -l /dev/kvm
crw-rw---- 1 root kvm 10, 232 Sep 24 09:12 /dev/kvm
```

## Install

```console
$ curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash
```

The script asks for `sudo` when it needs it, then:

1. checks the requirements above;
2. builds `dicer` and `dicerd`, with the hypervisors and guest binaries
   `dicerd` carries, from the `main` branch;
3. installs both to `/usr/local/bin`;
4. writes a configuration, `/etc/dicerd/config.yaml`, unless there is one;
5. turns on IPv4 forwarding, now and at every boot;
6. creates the `dicer` group, whose members can use the daemon without
   `sudo`;
7. installs `dicerd.service`, enables it and starts it.

`--ref` builds a branch, tag or commit instead of `main`:

```console
$ curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash -s -- --ref v0.2.0
```

## Use Dicer without `sudo`

The daemon's socket belongs to root and the `dicer` group. Join the group,
then start a new login shell, or run `newgrp dicer` in this one:

```console
$ sudo usermod -aG dicer $USER
$ newgrp dicer
```

The docs' commands assume you have: without it, run each `dicer` command
with `sudo`.

{{< callout type="warning" >}}
  A member of the `dicer` group can do anything with the daemon, which is as
  much as root on the host. Add only whom you would give root.
{{< /callout >}}

## Check it

```console
$ systemctl status dicerd
$ dicer version
$ dicer info
```

`dicer info` shows the daemon, the hypervisors it carries, and how much of
the host's CPU, memory and disk it may give instances. To manage the host
from another machine, see [Remote access](../../guides/remote-access).

## Upgrade and remove

Running the install script again upgrades Dicer, without stopping the
instances that are running. `uninstall.sh` removes it. See
[Operating the daemon](../../guides/operating-the-daemon#upgrading) for both.
