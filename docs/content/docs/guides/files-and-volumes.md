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

Everything a guest writes goes to the instance's overlay disk, which lasts
until the instance is deleted. Mounts add three other kinds of storage, each
for a different job: a [file](#configuration-files) mount for configuration
from the host, a [volume](#volumes) for data that must outlive the instance,
and a [tmpfs](#scratch-space) mount for scratch space.
[Storage](../../concepts/storage#mounts) compares how long each lasts.

Each mount is given with its own `--mount` flag, as comma-separated
`key=value` pairs:

```text
--mount [type=volume|file|tmpfs,][source=...,]target=/path[,readonly]
```

The type is `volume` unless you give another. The target must be an absolute
path, other than `/`, and two mounts cannot share a target. `src`, `dst` and
`ro` are accepted as short forms of `source`, `target` and `readonly`.

## Configuration files

A `file` mount puts a file into the guest, such as a configuration file, a
certificate or a list of allowed users.

```console
$ dicer run -d --name web -p 8080:80 \
    --mount type=file,source=./nginx.conf,target=/etc/nginx/nginx.conf,readonly \
    nginx:1.27
```

The source is a file on the machine you run `dicer` on, which need not be
the daemon's host. `dicer` reads it and sends its contents to the daemon,
which keeps them with the instance. An instance's file mounts can hold at most 1 MiB
between them. Put anything larger in a volume or the image.

At every start, the guest gets a copy, with the file's permissions, owned
by root. The copy replaces whatever the image had at the target. If the
image has nothing there, the target is created.

The copy belongs to the guest:

- **A change to the file you sent reaches the guest only when you send it
  again.** Update the mount while the instance is stopped, and start it:

  ```console
  $ dicer stop web
  $ dicer instance update web \
      --mount type=file,source=./nginx.conf,target=/etc/nginx/nginx.conf,readonly
  $ dicer start web
  ```

  `--mount` on `update` replaces every mount, so give them all.

- **A change in the guest never reaches the daemon.** The copy is held in
  the guest's memory and is gone when the instance stops. Add `readonly` so
  that the workload cannot change it by mistake.

A file mount is a single file, so its target cannot be a directory in the
image. For a directory of files, mount each file, or put them on a volume.

{{< callout type="info" >}}
  The daemon keeps the file's contents in the instance's definition, under
  `/var/lib/dicer`, and passes them to the guest through its runtime
  directory. Only root can read either. A secret mounted this way is as safe
  as the host's root account.
{{< /callout >}}

## Volumes

A volume is a disk of its own, for data that must outlive any one instance,
such as a database, uploads, or a cache worth keeping.

```console
$ dicer volume create pgdata --size 20GiB
$ dicer run -d --name db \
    --mount source=pgdata,target=/var/lib/postgresql/data \
    -e PGDATA=/var/lib/postgresql/data/pgdata \
    -e POSTGRES_PASSWORD=secret \
    postgres:17
```

A new volume is an empty ext4 filesystem. Like every ext4 filesystem, it has
a `lost+found` directory at its root. Some software refuses to use a
directory that is not empty, which is why PostgreSQL is pointed at a
directory inside the volume above.

A volume is sparse: it takes up only what has been written to it, whatever
its size. Its size is fixed when it is created, and a volume cannot be
resized.

An instance can mount up to 22 volumes, and each volume only once.

### Keeping and deleting volumes

Deleting an instance keeps the volumes it mounted. A volume is deleted only
when you ask:

```console
$ dicer volume list
$ dicer volume rm pgdata
```

A volume that any instance is defined to mount, running or not, cannot be
deleted. First delete that instance, or use `dicer update` to change its
mounts.

[Snapshots](../snapshots) do not include volumes, because a volume is
storage of its own.

### Sharing a volume

Only one instance at a time can use a volume read-write. If another instance
mounts the same volume, its start is refused while the first instance is
running, paused or on standby. The error names the instance that has the
volume.

A volume that every instance mounts read-only can be shared by any number of
them. That suits data written once and read by many, such as a model or a
dataset:

```console
$ dicer run -d --mount source=models,target=/models,readonly ghcr.io/acme/inference:1
```

### Volumes and frozen guests

A memory [snapshot](../snapshots) and [standby](../standby) both freeze a
guest, with all it knows of the volumes it mounts. Neither holds the volumes
themselves, so a volume the guest can write to must not change while the
guest is frozen:

- **A memory snapshot** is refused for an instance that can write to a
  volume. The volume would carry on changing after the snapshot, and a guest
  restored from it would remember a volume that no longer exists. Either
  stop the instance and take a disk snapshot, or mount its volumes
  `readonly`. For the same reason, such an instance can be forked only while
  it is stopped.
- **Standby** is allowed. An instance on standby keeps the volumes it can
  write to, so no other instance can change them before it resumes.

## Scratch space

A `tmpfs` mount is an empty directory held in the guest's memory. It is for
files that need not survive a stop, and that are faster in memory than on
disk:

```console
$ dicer run -d --mount type=tmpfs,target=/cache ghcr.io/acme/app:2
```

A tmpfs mount takes no source and cannot be read-only. Anyone in the guest
can write to it, as with `/tmp`. What is written there counts against the
instance's memory, so size `--memory` to allow for it.

## When a mount fails

Some mounts are checked on the host, such as a volume that is missing or in
use. If one of those fails, the instance does not start, and the
error says why.

Other mounts are made by the guest, such as a file mount onto a directory
the image has. If one of those fails, the guest boots without it, and its
console log says why:

```console
$ dicer logs web | grep 'mount failed'
```
