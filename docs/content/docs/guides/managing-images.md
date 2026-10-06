---
title: Managing images
weight: 8
description: "Pull images ahead of time, use private registries, update tags and reclaim disk space."
icon: cloud-download
related:
  - /docs/concepts/images
  - /docs/guides/monitoring
  - /docs/reference/configuration
---

An image is pulled when an instance is created from it, and then kept on the
host. This guide covers pulling ahead of time, choosing when images are
pulled, private registries, following a tag that has moved, and getting disk
space back. See [Images](../../concepts/images) for what an image becomes on
the host.

## Pull ahead of time

Creating an instance from an image the host does not have pulls the image
first, which can take a while for a large one. Pull it beforehand to make
creating instances quick:

```console
$ dicer pull postgres:17
Image docker.io/library/postgres:17 pulled in 9.3s (sha256:3b3a5a9d1e4c, 151 MiB)
```

A pull resolves the reference to a digest, downloads the layers the host
does not already have, and converts the image to a disk. If the host already
has the image at that digest, the pull only asks the registry, and reports
that the image is up to date.

Images are pulled for the host's architecture: `linux/amd64` on an x86_64
host, and `linux/arm64` on an aarch64 one. An image with no build for that
architecture cannot be pulled.

## Choose when an image is pulled

`--pull` on `dicer run`, `dicer create` and `dicer compose up` says when the
image is pulled, as with `docker run --pull`:

```console
$ dicer run -d --pull always nginx:1.27    # follow the tag if it has moved
$ dicer run -d --pull never nginx:1.27     # use only what the host holds
```

| Policy | Pulls the image |
|---|---|
| `missing` (default) | Only if the host does not hold it. |
| `always` | Every time, so that a tag that has moved is followed. Nothing is downloaded if the host already has what the tag points to. |
| `never` | Never. Creating the instance is refused if the host does not hold the image. |

With `never`, creating an instance asks no registry anything, so it takes
only as long as defining the instance and booting it. Use it where creating
an instance must be quick, and pull the image beforehand. Over the API, the
policy is the create request's `pull_policy`.

## See what is held

```console
$ dicer images -c name,size,created,"last used"
NAME                           SIZE      CREATED      LAST USED
docker.io/library/postgres:17  151 MiB   2 days ago   2 minutes ago
docker.io/library/nginx:1.27   71.2 MiB  3 weeks ago  3 weeks ago
```

Names are shown in full, with the registry.

- `CREATED` is when the image was pulled to this host.
- `LAST USED` is the last time the image was pulled, an instance was created
  or started from it, or [garbage collection](#reclaim-space-automatically)
  found it in use.
- `DIGEST`, left out above, tells apart images pulled under the same name.

`dicer info` shows how full the disk that holds them is.

## Private registries

The daemon logs in to a registry with the credentials that its
[configuration](../../reference/configuration#registries) gives under
`registries`. Each entry is keyed by the registry's host, as an image's name
gives it: `docker.io`, `ghcr.io`, or `registry.example.com:5000`. A registry
that is not listed is pulled from anonymously.

```yaml {filename="/etc/dicerd/config.yaml"}
registries:
  docker.io:
    username: dicer-bot
    password_file: /etc/dicerd/secrets/docker-token
  ghcr.io:
    username: dicer-bot
    password_file: /etc/dicerd/secrets/ghcr-token
  123456789012.dkr.ecr.eu-west-1.amazonaws.com:
    credential_helper: ecr-login
```

A registry takes a `username`, and either a `password` or a `password_file`
that holds it. Prefer the file, readable only by root, so that the secret is
not in the configuration. The file is read at every pull, so a rotated
password needs no restart.

A registry whose credentials expire, such as Amazon ECR, takes a
`credential_helper` instead. It names a `docker-credential-<name>` program on
the daemon's `PATH`, which gives fresh credentials each time. The helper runs
as root, so it uses root's cloud credentials, such as an instance role.

## Update to a newer image

A tag such as `nginx:1.27` or `latest` means the image most recently pulled
under it on this host. Starting an instance never asks the registry whether
the tag has moved. Pulling does, and so does creating an instance with
`--pull always`:

```console
$ dicer pull nginx:1.27          # the tag now means the new image
$ dicer restart web              # web boots from it
$ dicer image prune              # the old one is no longer in use
```

Each image is kept by digest, so after the pull, both images are listed under
the same name until the old one is removed. A running instance keeps the
image it booted from until it next starts.

To pin an instance to one image, whatever the tag does, give a digest:

```console
$ dicer run -d --name web nginx:1.27@sha256:9d6b58feebd2…
```

## Remove images

An image is **in use** while an instance is defined to boot from it, a
running guest booted from it, or a snapshot needs it. `dicer image prune`
deletes every image that is not in use, with the layers that only those
images needed. It asks first, unless given `-f`:

```console
$ dicer image prune -f
```

`dicer rmi` deletes the images you name. Given a tag, it deletes the image
most recently pulled under it. Given a digest, it deletes that image. An
image in use is refused, unless you give `--force`. Then an instance defined
to boot from it pulls it again at its next start, and one running from it
keeps running.

```console
$ dicer rmi nginx:1.27
$ dicer rmi --force nginx@sha256:9d6b58feebd2…
```

## Reclaim space automatically

The daemon can remove images it no longer needs by itself. Set either limit,
or both, in its [configuration](../../reference/configuration#images):

```yaml {filename="/etc/dicerd/config.yaml"}
images:
  gc_max_unused_age: 168h   # remove images unused for a week
  gc_max_size: 50GiB        # remove the least recently used while the store is larger
  gc_interval: 1h           # how often to check (1h if unset)
```

Then restart the daemon, which leaves running guests alone:

```console
$ sudo systemctl restart dicerd
```

Only images that are not in use are removed. Images used in the last ten
minutes are kept too, so an image pulled for an instance that is about to be
created is safe. The size limit counts the image disks and the layer cache
together. Each image removed is recorded as an event:

```console
$ dicer events --kind image
```
