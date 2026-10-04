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
the `dicer` command line beside it. On Debian, Ubuntu, Fedora, RHEL and its
rebuilds, and openSUSE, install the `dicer` package, which has both and the
service, by hand or with Ansible; elsewhere, the install script builds them
from source and sets the daemon up.

## Requirements

**The host**

- Linux on x86_64 or aarch64, with systemd.
- KVM: `/dev/kvm` must exist. On a cloud virtual machine, that means one
  with nested virtualisation enabled.
- `erofs-utils`, for `mkfs.erofs`, and `e2fsprogs`, for `mke2fs`.
- `iptables`.
- With firewalld (Fedora, RHEL and its rebuilds, openSUSE), its `dicer`
  zone, which the package and the install script install: see
  [Networking](../../concepts/networking#firewalld).

The package brings `erofs-utils`, `e2fsprogs` and `iptables` with it.

**To build**, for the install script

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

## Install from packages

Releases are published to an apt and a dnf repository at `pkg.dicer.sh`,
signed with Dicer's key. Add it, then install `dicer`:

{{< tabs >}}
  {{< tab name="Debian, Ubuntu" >}}
  ```console
  $ sudo install -d -m 0755 /etc/apt/keyrings
  $ curl -fsSL https://pkg.dicer.sh/gpg.key | sudo gpg --dearmor -o /etc/apt/keyrings/dicer.gpg
  $ echo "deb [signed-by=/etc/apt/keyrings/dicer.gpg] https://pkg.dicer.sh/deb stable main" \
      | sudo tee /etc/apt/sources.list.d/dicer.list
  $ sudo apt update
  $ sudo apt install dicer
  ```

  The daemon is enabled and started.
  {{< /tab >}}
  {{< tab name="Fedora, RHEL, Rocky, AlmaLinux" >}}
  On RHEL and its rebuilds, turn [EPEL](https://docs.fedoraproject.org/en-US/epel/)
  on first, for `erofs-utils`.

  ```console
  $ sudo curl -fsSL -o /etc/yum.repos.d/dicer.repo https://pkg.dicer.sh/rpm/dicer.repo
  $ sudo dnf install dicer
  $ sudo systemctl enable --now dicerd
  ```

  dnf asks you to accept the repository's key the first time.
  {{< /tab >}}
  {{< tab name="openSUSE" >}}
  ```console
  $ sudo zypper addrepo https://pkg.dicer.sh/rpm/dicer.repo
  $ sudo zypper install dicer
  $ sudo systemctl enable --now dicerd
  ```

  zypper asks you to trust the repository's key the first time.
  {{< /tab >}}
{{< /tabs >}}

The package installs `dicer` and `dicerd` to `/usr/bin`, the service,
`/etc/dicerd/config.yaml`, and a sysctl setting that turns IPv4 forwarding
on, and creates the `dicer` group. The package files are also attached to
each [release](https://github.com/konradasb/dicer/releases).

## Install with Ansible

The [`konradasb.general`](https://github.com/konradasb/ansible-collection-general)
collection sets hosts up with [Ansible](https://docs.ansible.com), from the
packages above. Install it where you run Ansible:

```console
$ ansible-galaxy collection install konradasb.general
```

It has two roles:

- **`konradasb.general.dicerd`** sets a host up as the package does: it adds
  the repository, turning EPEL on where it is needed, installs `dicer`,
  writes `/etc/dicerd/config.yaml`, and starts the daemon.
- **`konradasb.general.dicer`** installs only the command line, from the
  release archives, on a Linux or macOS machine that manages hosts
  elsewhere, and writes its users' remotes.

Example playbook:

```yaml {filename="dicer.yml"}
- name: Set up the Dicer hosts
  hosts:
  - dicer_hosts
  become: true
  roles:
  - role: konradasb.general.dicerd
    vars:
      dicerd_version: 0.3.0
      dicerd_tls_certificate: "{{ lookup('file', 'tls/' ~ inventory_hostname ~ '.pem') }}"
      dicerd_tls_private_key: "{{ lookup('file', 'tls/' ~ inventory_hostname ~ '-key.pem') }}"
      dicerd_tls_client_ca: "{{ lookup('file', 'tls/ca.pem') }}"
      dicerd_group_members:
      - alice
      dicerd_config:
        api:
          tcp:
            listen: 0.0.0.0:7443
```

```console
$ ansible-playbook -i inventory dicer.yml
```

`dicerd_config` holds the keys of the
[configuration](../../reference/configuration). The role checks it with
`dicerd validate` before it replaces the file, and restarts the daemon when
it changes, which leaves the instances running. The TLS files are written
under `/etc/dicerd/tls`, the private key readable only by root: keep it in
Ansible Vault. Each role's README lists its variables, as does
`ansible-doc -t role konradasb.general.dicerd`.

The role installs the package, so use it on a host that has no Dicer yet, or
has the package: a host the install script set up keeps its own service,
which starts the binaries the script built.

## Install from source

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

## Verify

```console
$ systemctl status dicerd
$ dicer version
$ dicer info
```

`dicer info` shows the daemon, the hypervisors it carries, and how much of
the host's CPU, memory and disk it may give instances. To manage the host
from another machine, see [Remote access](../../guides/remote-access).

## Upgrade and remove

Upgrading the package, running the playbook again for a newer version, or
running the install script again, upgrades Dicer without stopping the
instances that are running. See
[Operating the daemon](../../guides/operating-the-daemon#upgrading) for each.
