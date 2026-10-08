---
title: Files and environment
weight: 7
description: "Where Dicer keeps things on the host and the client, and the environment variables it reads."
icon: folder-open
---

Where Dicer keeps things on the host and the client, and the environment
variables it reads. The paths below are the defaults. The daemon's
[configuration](../configuration) can move its two directories, with
`data_dir` and `run_dir`.

## The daemon's host

| Path | |
|---|---|
| `/usr/bin/dicerd`, `/usr/bin/dicer` | The daemon and the command line, as the package installs them. `install.sh` puts them in `/usr/local/bin`. |
| `/etc/dicerd/config.yaml` | The daemon's configuration. `dicerd serve --config` names another. |
| `/usr/lib/systemd/system/dicerd.service` | The service, as the package installs it. `install.sh` writes it to `/etc/systemd/system`. |
| `/usr/lib/sysctl.d/60-dicer.conf` | The package's setting that turns IPv4 forwarding on. `install.sh` writes `/etc/sysctl.d/99-dicer.conf`, if forwarding was off. |
| `/usr/lib/firewalld/zones/dicer.xml` | The `dicer` firewalld zone, as the package installs it. `install.sh` writes it to `/etc/firewalld/zones`, where firewalld is installed. See [Networking](../../concepts/networking#firewalld). |
| `/etc/dicerd/tls/` | Where the [Remote access](../../guides/remote-access) guide and the Ansible role keep the daemon's TLS files. Purging the deb package removes them with `/etc/dicerd`. |
| `/var/lib/dicer` | Persistent state, `data_dir`, kept across reboots. |
| `/run/dicer` | Runtime state, `run_dir`. It is a tmpfs, which a reboot clears. |
| `/run/dicer/dicer.sock` | The API's socket. The configuration that the package and `install.sh` write lets root and the `dicer` group use it. |

Both directories belong to root. Apart from the API's socket, nothing under
them is open to other users. Change their contents only through the API,
which keeps them consistent.

### `/var/lib/dicer`

```text
/var/lib/dicer/
├── instances/<name>/
│   ├── config.yaml           the instance's definition
│   ├── overlay.img           its overlay disk (sparse)
│   ├── serial.log            its console log: dicer logs
│   ├── hypervisor.log        its hypervisor's log: dicer logs --source hypervisor
│   └── standby/              on standby, its frozen memory and device state
├── snapshots/<name>/
│   ├── config.yaml           the snapshot's definition
│   ├── overlay.img           its copy of the instance's overlay disk
│   └── ...                   a memory snapshot's memory and device state
├── networks/<name>.yaml      network definitions
├── allocations/<network>.yaml   which instance has which address
├── volumes/
│   ├── <name>.yaml           volume definitions
│   └── <id>/disk.raw         volume disks (sparse)
├── kernels/
│   ├── <name>.yaml           kernel definitions
│   └── <id>/vmlinux          the kernels, the default one among them
├── images/<digest>/
│   ├── disk.img              the image as a read-only EROFS disk
│   └── metadata.json         its name, configuration and when it was used
├── oci-cache/                downloaded layers, shared between images
├── tmp/                      images being unpacked
├── initrd/<arch>/initrd      the guest initramfs, built from dicer-init and dicer-agent
├── bin/<hypervisor>/<version>/   the hypervisor binaries dicerd carries
└── events.jsonl              the events log: dicer events
```

Most of the space goes to overlay disks, volumes, images and snapshots.
Disks are sparse, so `ls -l` shows their size, and `du` shows the space they
take.

### `/run/dicer`

```text
/run/dicer/
├── dicer.sock                the API
└── instances/<id>/           one per instance that is running, or ended on its own since boot
    ├── state.json            its state: pid, address, how it last ended
    ├── hypervisor.sock       the hypervisor's API
    ├── vsock.sock            the channel to the guest's agent
    ├── config.img            the disk dicer-init reads its configuration from
    ├── status.img            the disk the guest reports how it ended on
    ├── overlay.img           a link to its overlay disk
    ├── serial.log            a link to its console log
    └── logs/vmm.log          a link to its hypervisor's log
```

A reboot clears this directory, and every instance is then stopped.
`config.img` holds the instance's environment and the contents of its file
mounts.

The hypervisor runs in the instance's directory here, and is given each of
the instance's files by its name alone. A memory snapshot therefore names no
directory of the instance's, so it can be restored into another instance,
as `dicer fork` does.

## Inside a guest

| Path | |
|---|---|
| PID 1 | `dicer-init`, which runs from the initramfs, outside the guest's root; `ps` shows it as `/init`. |
| `/usr/local/bin/dicer-agent` | The agent `dicer exec`, `dicer cp` and health checks go through. |
| `/etc/systemd/system/dicer-agent.service`, `dicer-exit.service` | Units added to a guest that boots systemd. |
| `/dev/vda` … `/dev/vdd` | The image, the overlay disk, and the configuration and status disks. |
| `/dev/vde` onwards | Volumes, in the order they are mounted. |

## The client

| Path | |
|---|---|
| `~/.config/dicer/remotes.yaml` | Remotes, and which is current. The directory is `$DICER_CONFIG_DIR` if set. Otherwise it is `dicer` in the user's configuration directory: `$XDG_CONFIG_HOME/dicer` or `~/.config/dicer` on Linux, and `~/Library/Application Support/dicer` on macOS. |

## Environment variables

### Command line

| Variable | |
|---|---|
| `DICER_REMOTE` | The remote that commands go to, as a name or an address, unless `--remote` is given. See [Remote access](../../guides/remote-access#choose-which-daemon-to-talk-to). |
| `DICER_CONFIG_DIR` | Where remotes are kept. |
| `DICER_DEBUG` | Set to `1` or `true` to trace every call to the daemon on standard error, as `--debug` does. |
| `DICER_COMPOSE_FILE` | The compose file `dicer compose` uses, unless `-f` is given. |
| `DICER_COMPOSE_PROJECT_NAME` | The compose project's name, unless `-p` is given. |
| `NO_COLOR` | Set to any non-empty value to turn colour off. Output that does not go to a terminal never has colour. |

`-e KEY`, without a value, passes the shell's value of `KEY` to a guest.
`dicer compose` also substitutes variables in the compose file from the
environment. See [Compose file](../compose-file#variables).

### Daemon

| Variable | |
|---|---|
| `PATH` | Where `mkfs.erofs`, `mke2fs`, `iptables` and any registry credential helpers are found. |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | The proxy that image pulls go through, if any. Set them for the service in a systemd drop-in. |
