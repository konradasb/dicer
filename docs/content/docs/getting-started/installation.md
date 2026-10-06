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

Dicer runs on one Linux host. The daemon, `dicerd`, runs as a systemd
service, and the `dicer` command line talks to it. There are three ways to
install them:

- [From packages](#install-from-packages), on Debian, Ubuntu, Fedora, RHEL
  and its rebuilds, and openSUSE.
- [With Ansible](#install-with-ansible), which installs the same packages.
- [From source](#install-from-source), with the install script, on any other
  distribution.

## Requirements

**The host**

- Linux on x86_64 or aarch64, with systemd.
- KVM: `/dev/kvm` must exist. On a cloud virtual machine, nested
  virtualisation must be turned on.
- `erofs-utils`, for `mkfs.erofs`, and `e2fsprogs`, for `mke2fs`.
- `iptables`.
- Where firewalld runs, as on Fedora, RHEL and openSUSE, the `dicer`
  firewalld zone. The package and the install script both install it. See
  [Networking](../../concepts/networking#firewalld).

The package brings `erofs-utils`, `e2fsprogs` and `iptables` with it.

**To build from source**

- Go 1.25.5 or later, `git`, `make` and `curl`.

Distributions often carry an older Go, so install it from
[go.dev/dl](https://go.dev/dl/). Install everything else, the host's tools
included, with the distribution's package manager:

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

Check that KVM is there:

```console
$ ls -l /dev/kvm
crw-rw---- 1 root kvm 10, 232 Sep 24 09:12 /dev/kvm
```

## Install from packages

Releases are published to apt and RPM repositories at `pkg.dicer.sh`,
signed with Dicer's key. Add the repository, then install the `dicer`
package:

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

  The package enables and starts the daemon.
  {{< /tab >}}
  {{< tab name="Fedora, RHEL, Rocky, AlmaLinux" >}}
  On RHEL and its rebuilds, turn [EPEL](https://docs.fedoraproject.org/en-US/epel/)
  on first, for `erofs-utils`.

  ```console
  $ sudo curl -fsSL -o /etc/yum.repos.d/dicer.repo https://pkg.dicer.sh/rpm/dicer.repo
  $ sudo dnf install dicer
  $ sudo systemctl enable --now dicerd
  ```

  dnf asks you to accept the repository's key the first time. The package
  leaves enabling the daemon to the system's presets, so the last command
  enables and starts it.
  {{< /tab >}}
  {{< tab name="openSUSE" >}}
  ```console
  $ sudo zypper addrepo https://pkg.dicer.sh/rpm/dicer.repo
  $ sudo zypper install dicer
  $ sudo systemctl enable --now dicerd
  ```

  zypper asks you to trust the repository's key the first time. The package
  leaves enabling the daemon to the system's presets, so the last command
  enables and starts it.
  {{< /tab >}}
{{< /tabs >}}

The package installs:

- `dicer` and `dicerd`, in `/usr/bin`;
- the `dicerd` service;
- the configuration, `/etc/dicerd/config.yaml`;
- a sysctl setting that turns IPv4 forwarding on;
- the `dicer` firewalld zone;
- shell completions for bash, zsh and fish.

It also creates the `dicer` group. [Files and
environment](../../reference/files-and-environment) lists where each file
goes. The package files are also attached to each
[release](https://github.com/konradasb/dicer/releases).

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
`dicerd validate` before it replaces the file. When the file changes, the
role restarts the daemon, which leaves the instances running.

The TLS files are written under `/etc/dicerd/tls`, and only root can read
the private key. Keep the key in Ansible Vault. Each role's README lists
its variables, and so does `ansible-doc -t role konradasb.general.dicerd`.

Use the role on a host that has no Dicer yet, or that has the package. Don't
use it on a host the install script set up: the script's service, in
`/etc/systemd/system`, takes precedence over the package's, so the daemon
would go on running the binaries the script built.

## Install from source

```console
$ curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash
```

The script asks for `sudo` when it needs it. Then it:

1. checks for the build tools, `mkfs.erofs`, `mke2fs`, systemd and KVM;
2. turns on IPv4 forwarding, now and at every boot;
3. builds `dicer` and `dicerd` from the `main` branch, with the hypervisors
   and guest binaries that `dicerd` carries;
4. stops the daemon if it is running, and installs both binaries to
   `/usr/local/bin`;
5. creates the `dicer` group, whose members can use the daemon without
   `sudo`;
6. writes a configuration, `/etc/dicerd/config.yaml`, unless there is one;
7. installs `dicerd.service` and, where firewalld is installed, the `dicer`
   zone;
8. enables and starts the daemon.

`--ref` builds a branch, tag or commit instead of `main`:

```console
$ curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash -s -- --ref v0.3.0
```

## Use Dicer without `sudo`

The daemon's socket belongs to root and the `dicer` group. Join the group,
then start a new login shell, or run `newgrp dicer` in this one:

```console
$ sudo usermod -aG dicer $USER
$ newgrp dicer
```

The rest of these docs assume you are in the group. If you are not, run each
`dicer` command with `sudo`.

{{< callout type="warning" >}}
  A member of the `dicer` group can do anything with the daemon, which
  amounts to root on the host. Add only users you would give root.
{{< /callout >}}

## Verify

```console
$ systemctl status dicerd
$ dicer version
$ dicer info
```

The service should be `active (running)`. `dicer version` shows the
command line's version and the daemon's. `dicer info` shows the daemon, the
hypervisors it carries, its default kernel and network, and how much of the
host's CPU, memory and disk it may give instances.

To manage the host from another machine, see
[Remote access](../../guides/remote-access).

## Upgrade and remove

[Operating the daemon](../../guides/operating-the-daemon) shows how to
[upgrade](../../guides/operating-the-daemon#upgrading) Dicer, without
stopping running instances, and how to
[uninstall](../../guides/operating-the-daemon#uninstalling) it, for each way
of installing it.
