---
title: Files and volumes
weight: 2
description: "Configuration files from the host, volumes that outlive instances, and scratch space."
icon: folder
related:
  - /docs/concepts/storage
  - /docs/guides/running-workloads
  - /docs/guides/operating-the-daemon
---

Everything a guest writes goes to the instance's own disk, which lasts until
the instance is deleted. Mounts add three other kinds of storage, each for a
different job:

| Type | For | Lives |
|---|---|---|
| `file` | Configuration from the host | A copy, made at each start |
| `volume` | Data that must outlive the instance | Until the volume is deleted |
| `tmpfs` | Scratch space | In the guest's memory, until it stops |

Mounts are given with `--mount`, as comma-separated `key=value` pairs, once
per mount:

```text
--mount [type=volume|file|tmpfs,][source=...,]target=/path[,readonly]
```

The type is `volume` unless given. A target must be an absolute path, and
two mounts cannot share one. See [Storage](../../concepts/storage) for how
this fits with the instance's disk.

## Configuration files

A `file` mount puts a file from the host into the guest: a configuration
file, a certificate, a list of allowed users.

```console
$ dicer run -d --name web -p 8080:80 \
    --mount type=file,source=/etc/dicer/web/nginx.conf,target=/etc/nginx/nginx.conf,readonly \
    nginx:1.27
```

The source must be an absolute path on the host, and must exist. At every
start, the daemon reads it and hands the guest a copy, with the host file's
owner and permissions, in place of whatever the image had at the target. A
target that does not exist in the image is created.

The copy is the guest's own:

- **A change on the host reaches the guest at its next start**, not before.
  To apply an edited file, restart the instance:

  ```console
  $ dicer restart web
  ```

- **A change in the guest never reaches the host.** The copy is held in the
  guest's memory and is gone when it stops. Add `readonly` so the workload
  cannot change it by mistake.

A file mount is one file; the target cannot be a directory in the image. For
a directory of files, mount each, or put them on a volume.

{{< callout type="info" >}}
  The file's contents pass through the daemon's runtime directory, readable
  only by root, on their way to the guest. A secret mounted this way is as
  safe as the host's root account.
{{< /callout >}}

## Directories

A `directory` mount shares a directory on the host with the guest while it
runs: what either side changes, the other sees. It suits development, with
the source on the host, in your editor, and the workload in the guest:

```console
$ dicer run -d --name site -p 8000:8000 \
    --mount type=directory,source=.,target=/site \
    python:3.13 python -m http.server 8000 -d /site
```

Edit a file in the current directory, and the next request serves the
change.

A relative source is taken from the directory `dicer` runs in; it must be a
directory on the daemon's host. Files keep the owners and permissions they
have on the host, so the workload may need to run as the user that owns
them. `readonly` stops the guest writing to it: virtiofsd itself refuses
the writes, so it needs a virtiofsd that has `--readonly`, and an instance
asking for a read-only directory on a host whose virtiofsd has not cannot
start.

The daemon shares whatever directory it is asked to, read-write unless told
otherwise, as root. Anyone who can use its API can share `/`, so keep the
API to those you would give root, as for every other part of it.

Directory mounts need two things on the host:

- **Cloud Hypervisor**, the default hypervisor. An instance on Firecracker
  cannot mount a directory.
- **virtiofsd**, which serves the directory to the guest:
  `sudo apt install virtiofsd` or `sudo dnf install virtiofsd`. The daemon
  looks for it when it starts, so restart it after installing:
  `sudo systemctl restart dicerd`.

A few things work differently from a disk:

- **The guest is told of a change on the host when it next looks**, within
  a second, not at once. A tool that watches files for changes, such as a
  development server reloading on save, should poll rather than wait to be
  told; most have an option for it.
- **An instance that mounts a directory cannot be snapshotted.**
- **If virtiofsd stops while the guest runs**, the guest's reads and writes
  under the mount fail until the instance is restarted. Its log,
  `logs/virtiofsd-N.log` in the instance's directory under the daemon's
  `run_dir`, says why.

## Volumes

A volume is a disk of its own, for data that must outlive any one instance:
a database, uploads, a cache worth keeping.

```console
$ dicer volume create pgdata --size 20GiB
$ dicer run -d --name db \
    --mount source=pgdata,target=/var/lib/postgresql/data \
    -e PGDATA=/var/lib/postgresql/data/pgdata \
    -e POSTGRES_PASSWORD=secret \
    postgres:17
```

A new volume is an empty ext4 filesystem. Like every ext4 filesystem, it has
a `lost+found` directory at its root, which some software refuses: that is
why PostgreSQL is pointed at a directory inside it above.

A volume is sparse: it takes up only what has been written to it, whatever
its size. Its size is fixed when it is created; there is no resizing a
volume.

### Keeping and deleting volumes

Deleting an instance keeps the volumes it mounted. A volume is deleted only
by asking:

```console
$ dicer volume list
$ dicer volume rm pgdata
```

A volume that any instance is defined to mount, running or not, cannot be
deleted: delete the instance, or `dicer update` it to mount something else,
first.

### Sharing a volume

A volume can be used read-write by one running instance at a time. A second
instance mounting it is refused at start while the first runs, with an error
naming the instance that has it.

Mounted read-only by every instance that uses it, a volume can be shared by
any number of them. That suits data written once and read by many, such as a
model or a dataset:

```console
$ dicer run -d --mount source=models,target=/models,readonly ghcr.io/acme/inference:1
```

An instance can mount up to 22 volumes.

## Scratch space

A `tmpfs` mount is an empty directory held in the guest's memory, for files
that need not survive a stop, and that are faster in memory than on disk:

```console
$ dicer run -d --mount type=tmpfs,target=/cache ghcr.io/acme/app:2
```

It takes no source, and cannot be read-only. What is written there counts
against the instance's memory, so size `--memory` for it.

## When a mount fails

A mount the host can refuse, such as a missing host file or a volume in use,
stops the instance from starting, with the reason. A mount the guest cannot
make, such as a file mount onto a directory the image has, does not: the
guest boots without it, and says why in its console log:

```console
$ dicer logs web | grep 'mount failed'
```
