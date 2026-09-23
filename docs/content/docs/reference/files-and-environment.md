---
title: Files and environment
weight: 7
description: "Where Dicer keeps things on the host and the client, and the environment variables it reads."
icon: folder-open
---

Where Dicer keeps things on the host and the client, and the environment
variables it reads. Paths are the defaults; the daemon's
[configuration](../configuration) moves the two directories with `data_dir`
and `run_dir`.

## The daemon's host

| Path | |
|---|---|
| `/usr/local/bin/dicerd`, `/usr/local/bin/dicer` | The daemon and the command line, as `install.sh` puts them. |
| `/etc/dicerd/config.yaml` | The daemon's configuration. `dicerd serve --config` names another. |
| `/etc/systemd/system/dicerd.service` | The service `install.sh` sets up. |
| `/var/lib/dicer` | Persistent state, `data_dir`: kept across reboots. |
| `/run/dicer` | Runtime state, `run_dir`: a tmpfs, which a reboot clears. |
| `/run/dicer/dicer.sock` | The API's socket, root's only. |

Everything under both directories belongs to root and is readable by root
alone. Change it only through the API, which keeps it consistent.

### `/var/lib/dicer`

```text
/var/lib/dicer/
├── instances/<name>/
│   ├── config.yaml           the instance's definition
│   ├── overlay.img           its writable disk (sparse)
│   ├── serial.log            its console log: dicer logs
│   └── snapshots/<snapshot>/ its snapshots
├── networks/<name>.yaml      network definitions
├── allocations/<network>.yaml   which instance has which address
├── volumes/
│   ├── <name>.yaml           volume definitions
│   └── <id>/disk.raw         volume disks (sparse)
├── kernels/
│   ├── <name>.yaml           kernel definitions
│   └── <id>/vmlinux          kernels, once downloaded
├── images/<digest>/
│   ├── disk.img              the image as a read-only EROFS disk
│   └── metadata.json         its name, configuration and when it was used
├── oci-cache/                downloaded layers, shared between images
├── tmp/                      images being unpacked
├── initrd/                   the guest initramfs, built from dicer-init and dicer-agent
├── bin/<hypervisor>/<version>/   the hypervisor binaries dicerd carries
└── events.jsonl              the events log: dicer events
```

Most of the space goes to instance disks, volumes and images. Disks are
sparse, so `ls -l` shows their size, and `du` what they take.

### `/run/dicer`

```text
/run/dicer/
├── dicer.sock                the API
└── instances/<id>/           one per instance that is running, or was since boot
    ├── state.json            its state: pid, address, how it last ended
    ├── hypervisor.sock       the hypervisor's API
    ├── vsock.sock            the channel to the guest's agent
    ├── config.img            the disk dicer-init reads its configuration from
    ├── status.img            the disk the guest reports how it ended on
    └── logs/vmm.log          the hypervisor's log: dicer logs --source hypervisor
```

A reboot clears it, and every instance is then stopped. `config.img` holds
the instance's environment and the contents of its file mounts.

## Inside a guest

| Path | |
|---|---|
| PID 1 | `dicer-init`, which runs from the initramfs, outside the guest's root; `ps` shows it as `/init`. |
| `/usr/local/bin/dicer-agent` | The agent `dicer exec`, `dicer cp` and health checks go through. |
| `/etc/systemd/system/dicer-agent.service`, `dicer-exit.service` | Units added to a guest that boots systemd. |
| `/dev/vda` … `/dev/vdd` | The image, the instance's disk, and the configuration and status disks. |
| `/dev/vde` onwards | Volumes, in the order they are mounted. |

## The client

| Path | |
|---|---|
| `~/.config/dicer/remotes.yaml` | Remotes, and which is current. The directory is `$DICER_CONFIG_DIR` if set, otherwise `dicer` in the user's configuration directory. |

## Environment variables

### Command line

| Variable | |
|---|---|
| `DICER_REMOTE` | The remote commands go to, a name or an address, unless `--remote` is given. See [Remote access](../../guides/remote-access#choose-which-daemon-to-talk-to). |
| `DICER_CONFIG_DIR` | Where remotes are kept. |
| `DICER_DEBUG` | Set to `1` or `true` to trace every call to the daemon on standard error, as `--debug` does. |
| `NO_COLOR` | Set to anything to turn colour off. Output that is not to a terminal never has any. |

`-e KEY`, without a value, passes the shell's value of `KEY` to a guest.

### Daemon

| Variable | |
|---|---|
| `DOCKER_CONFIG` | The directory of the Docker configuration file that registry credentials are read from. Without it, `~/.docker` of the user the daemon runs as. See [Managing images](../../guides/managing-images#private-registries). |
| `PATH` | Where `mkfs.erofs`, `mkfs.ext4`, `mke2fs`, `iptables` and any registry credential helpers are found. |
