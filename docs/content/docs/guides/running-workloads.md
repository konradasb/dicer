---
title: Running workloads
weight: 1
description: "Define and start instances: commands, environment, ports and sizing."
icon: play
related:
  - /docs/guides/files-and-volumes
  - /docs/guides/restarts
  - /docs/guides/working-inside-guests
  - /docs/reference/cli
---

An instance is defined once and then started, stopped and started again.
`dicer run` does both at once; `dicer create` only defines, for starting
later. This guide assumes a [kernel](../../concepts/kernels) and a
[network](../../concepts/networking) are set up, as in the
[quickstart](../../getting-started/quickstart).

## Run an image

```console
$ dicer run -d --name web -p 8080:80 nginx:1.27
Instance web started in 1.1s (172.20.61.102)
  Shell: dicer exec web   Logs: dicer logs -f web   Stop: dicer stop web
```

`dicer run` pulls the image if the host does not have it, defines the
instance and boots it. With `-d` it returns once the guest is running,
leaving the instance in the background. Without `--name`, the instance is
named after the image, with a random suffix.

Without `-d`, as with `docker run`, it writes the guest's console until the
instance stops, and exits with the status the workload ended with. That is
how to run a one-off job:

```console
$ dicer run --rm alpine:3.21 echo hello
hello
```

Ctrl+C stops the instance, and a second Ctrl+C stops waiting for it.
Nothing typed reaches the guest: for an interactive shell, run the instance
with `-d` and use [`dicer exec`](../working-inside-guests).

Flags go before the image. Everything after it is the command, which
replaces the image's `ENTRYPOINT` and `CMD`:

```console
$ dicer run -d --name sleeper alpine:3.21 sleep infinity
```

## Define now, start later

`dicer create` records an instance without booting it. Its image is given
with `-i`, and a command after `--`:

```console
$ dicer create worker -i alpine:3.21 -e QUEUE=jobs -- /bin/worker --verbose
$ dicer start worker
```

`--start` creates and starts in one step, as `run` does. Every flag is listed
in the [command line reference](../../reference/cli/dicer_create).

## Environment

`-e KEY=VALUE` sets a variable in the guest; the image's own `ENV` stays,
unless a variable of the same name replaces it. `-e KEY`, with no value,
passes this shell's value of `KEY`, and nothing if it is not set.

`--env-file` reads variables from a file of `KEY=VALUE` lines. Blank lines
and lines starting with `#` are skipped, and values are taken as written,
quotes included. Variables from `-e` win over those from a file.

```console
$ dicer run -d --env-file app.env -e LOG_LEVEL=debug ghcr.io/acme/app:2
```

## Sizing

| Flag | Default | |
|---|---|---|
| `--vcpus` | 1 | No more than the host has CPUs. |
| `-m`, `--memory` | 512MiB | |
| `--disk` | 10GiB | The instance's own writable disk. It is sparse, so it takes only what the guest writes. See [Storage](../../concepts/storage). |

Sizes are written as `512MiB`, `2GiB` and so on. An instance holds its vCPUs
and memory while it runs, and a start the host has no room for is refused;
see [Capacity](../capacity).

## Ports

A guest's port is published on the host with `-p`, as
`[hostIP:]hostPort:guestPort[/tcp|udp]`:

```console
$ dicer run -d -p 8080:80 -p 8443:443 nginx:1.27
$ dicer run -d -p 192.0.2.10:53:53/udp dns-server
```

Without a host address, the port is published on every address the host
has, except loopback: `curl localhost:8080` on the host itself does not
reach the guest. Use the host's own address, or the guest's. See
[Networking](../../concepts/networking#publishing-ports).

## Network and address

`--network` attaches the instance to a network other than the default, and
`--ip` gives it a fixed address on it. Otherwise it is given an address when
it first starts, and keeps it until it is deleted.

`--hostname` sets the guest's hostname, which is otherwise the instance's
name.

## Labels

Labels are `KEY=VALUE` pairs of your own, for finding instances again:

```console
$ dicer run -d -l team=search -l tier=frontend nginx:1.27
$ dicer ps --filter label=team=search
```

## Changing an instance

`dicer update` changes a stopped instance, and the change takes effect when
it next starts. Only what is given changes; a list given, such as `-e` or
`-p`, replaces the old one whole.

```console
$ dicer stop web
$ dicer update web --memory 2GiB --vcpus 2
$ dicer start web
```

The restart policy alone can be changed while the instance runs. See
[Instances](../../concepts/instances#changing-and-deleting).

## What next

- [Files and volumes](../files-and-volumes): give an instance data that
  outlives it.
- [Restarts](../restarts): keep it running.
- [Working inside guests](../working-inside-guests): get a shell in it.
