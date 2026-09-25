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
