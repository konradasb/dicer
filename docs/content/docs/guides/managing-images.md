---
title: Managing images
weight: 7
description: "Pull images ahead of time, use private registries, update tags and reclaim disk space."
icon: cloud-download
related:
  - /docs/concepts/images
  - /docs/guides/monitoring
  - /docs/reference/configuration
---

An image is pulled the first time an instance needs it, and kept. This guide
covers pulling ahead of time, private registries, updating what a tag
means, and getting the disk space back. See [Images](../../concepts/images)
for what an image becomes on the host.

## Pull ahead of time

A start that needs an image it does not have pulls it first, which can take
a while for a large image. Pull it beforehand to make starts quick:

```console
$ dicer pull postgres:17
```

A pull resolves the reference to a digest, downloads the layers the host
does not already have, and converts the image to a disk. Pulling an image
the host already has, at the same digest, only asks the registry.

Images are pulled for the host's architecture: `linux/amd64` on an x86_64
host, `linux/arm64` on an aarch64 one. An image with no build for it cannot
be pulled.

## See what is held

```console
$ dicer images -c name,size,created,"last used"
NAME                            SIZE       CREATED        LAST USED
docker.io/library/postgres:17   151 MiB    2 days ago     2 minutes ago
docker.io/library/nginx:1.27    71.2 MiB   3 weeks ago    3 weeks ago
```

Names are shown in full, with the registry. `CREATED` is when the image was
pulled to this host. `LAST USED` is when [garbage collection](#reclaim-space-automatically)
last found it in use; while that is off, it stays at the pull. The
`DIGEST` column, left out above, tells apart images pulled under the same
name. `dicer info` shows how full the disk holding them is.

## Private registries

The daemon logs in to registries as Docker does, reading the Docker
configuration file of the user it runs as. For the service `install.sh`
sets up, that is root's, `/root/.docker/config.json`:

```console
$ sudo docker login ghcr.io
```

Without Docker, write the file yourself; `auth` is `username:password`,
or `username:token`, in base64:

```json {filename="/root/.docker/config.json"}
{
  "auths": {
    "ghcr.io": { "auth": "YWNtZTpnaHBfLi4u" }
  }
}
```

Credential helpers the file names, such as `docker-credential-ecr-login`,
are used too, if they are on the daemon's `PATH`. The file is read at every
pull, so a change needs no restart.

To keep the file somewhere else, set `DOCKER_CONFIG` to its directory in a
drop-in for the service:

```ini {filename="/etc/systemd/system/dicerd.service.d/registry.conf"}
[Service]
Environment=DOCKER_CONFIG=/etc/dicerd/docker
```

## Update to a newer image

A tag such as `nginx:1.27` or `latest` means the image most recently pulled
under it on this host. Starting an instance never asks the registry
whether the tag has moved; pulling does:

```console
$ dicer pull nginx:1.27          # the tag now means the new image
$ dicer restart web              # web boots from it
$ dicer image prune              # the old one is no longer in use
```

Each image is kept by digest, so after the pull both are listed under the
same name until the old one is removed. An instance keeps the image it
booted from until it next starts.

To pin an instance to one image, whatever the tag does, give a digest:

```console
$ dicer run --name web nginx:1.27@sha256:9d6b58feebd2…
```

## Remove images

An image is **in use** while an instance is defined to boot from it, a
running guest booted from it, or a snapshot needs it. `dicer image prune`
deletes every image not in use, with the layers only they needed:

```console
$ dicer image prune
```

`dicer rmi` deletes named images. Given a tag, it deletes the image most
recently pulled under it; given a digest, that one. An image in use is
refused, unless with `--force`: an instance defined to boot from it pulls it
again at its next start, and one running from it keeps running.

```console
$ dicer rmi nginx:1.27
$ dicer rmi --force nginx@sha256:9d6b58feebd2…
```

## Reclaim space automatically

The daemon can remove images it no longer needs by itself. Set either limit,
or both, in its [configuration](../../reference/configuration):

```yaml {filename="/etc/dicerd/config.yaml"}
images:
  gc_max_unused_age: 168h   # remove images unused for a week
  gc_max_size: 50GiB        # remove the least recently used while the store is larger
  gc_interval: 1h           # how often to check
```

Then restart the daemon, which leaves running guests alone:

```console
$ sudo systemctl restart dicerd
```

Only images not in use are removed, and none used in the last ten minutes,
so an image pulled for an instance about to be created is safe. The size
limit counts the image disks and the layer cache together. Each image
removed is an event:

```console
$ dicer events --kind image
```
