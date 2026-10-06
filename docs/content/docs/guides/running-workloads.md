---
title: Running workloads
weight: 1
description: "Define and start instances: commands, environment, sizing, rate limits, ports and standby."
icon: play
related:
  - /docs/guides/files-and-volumes
  - /docs/guides/restarts
  - /docs/guides/standby
  - /docs/guides/working-inside-guests
  - /docs/reference/cli
---

An instance is defined once, and can then be started and stopped as often as
you like. `dicer run` defines an instance and starts it in one step.
`dicer create` only defines it, so that you can start it later. This guide
assumes a [kernel](../../concepts/kernels) and a
[network](../../concepts/networking) are set up, as in the
[quickstart](../../getting-started/quickstart).

## Run an image

```console
$ dicer run -d --name web -p 8080:80 nginx:1.27
Instance web started in 1.1s (172.20.61.102)
  Shell: dicer exec web   Logs: dicer logs -f web   Stop: dicer stop web
```

`dicer run` pulls the image if the host does not have it, defines the
instance and boots it. With `-d`, it returns once the guest is running and
leaves the instance in the background. Without `--name`, the instance is
named after the image, with a random suffix, such as `nginx-k3x9`.

`--pull` says when the image is pulled. `--pull always` pulls it even if the
host has it, to follow a tag that has moved. `--pull never` uses only the
image the host already holds. See
[Managing images](../managing-images#choose-when-an-image-is-pulled).

Without `-d`, `dicer run` behaves like `docker run`. It writes the guest's
console until the instance stops, and then exits with the status the
workload ended with. This is how to run a one-off job:

```console
$ dicer run --rm alpine:3.21 echo hello
hello
```

`--rm` deletes the instance once it ends. Ctrl+C stops the instance, and a
second Ctrl+C stops waiting for it. Nothing you type reaches the guest. For
an interactive shell, run the instance with `-d` and use
[`dicer exec`](../working-inside-guests).

Flags go before the image. Everything after the image is the command, which
replaces the image's `ENTRYPOINT` and `CMD`:

```console
$ dicer run -d --name sleeper alpine:3.21 sleep infinity
```

## Wait for an instance to end

For an instance running in the background, `dicer wait` waits until it
stops and prints the exit code its workload ended with, as `docker wait`
does:

```console
$ dicer run -d --name migrate ghcr.io/acme/api:3 ./migrate up
$ dicer wait migrate
0
```

It prints 0 for a guest that powered itself off, and 125 for one that ended
without saying how, such as after a kernel panic. `dicer wait` itself
succeeds whatever the status, so a script reads the status from its output:
`status=$(dicer wait migrate)`. Given several instances, it waits for each in
turn and prints a line for each.

If the instance has already stopped, `dicer wait` prints its last status at
once, even if `--rm` has deleted it since. An instance that its
[restart policy](../restarts) starts again, or that is on standby, has not
stopped, so the wait goes on. `--timeout` gives up after a while, such as
`--timeout 10m`.

## Define now, start later

`dicer create` records an instance without booting it. The image is given
with `-i`, and the command after `--`:

```console
$ dicer create worker -i alpine:3.21 -e QUEUE=jobs -- /bin/worker --verbose
Instance worker created. Start it with: dicer start worker
$ dicer start worker
```

`dicer create --start` creates and starts the instance in one step, as
`dicer run` does. Every flag is listed in the
[command line reference](../../reference/cli/dicer_create).

## Environment

`-e KEY=VALUE` sets a variable in the guest. The image's own `ENV` variables
stay, unless one of the same name replaces them. `-e KEY`, with no value,
passes this shell's value of `KEY`, or nothing if it is not set.

`--env-file` reads variables from a file of `KEY=VALUE` lines. Blank lines
and lines starting with `#` are skipped. Values are taken as written, quotes
included. Variables given with `-e` win over those from a file.

```console
$ dicer run -d --env-file app.env -e LOG_LEVEL=debug ghcr.io/acme/app:2
```

## Sizing

| Flag | Default | |
|---|---|---|
| `--vcpus` | 1 | No more than the host has CPUs. |
| `-m`, `--memory` | 512MiB | |
| `--disk` | 10GiB | The size of the instance's overlay disk, which holds everything the guest writes. It is sparse, so it takes up only what the guest has written. See [Storage](../../concepts/storage#the-overlay-disk). |
| `--max-vcpus` | none | The most vCPUs the running instance can be [resized](../capacity#resizing-a-running-instance) to. Cloud Hypervisor only. |
| `--max-memory` | none | The most memory the running instance can be resized to. |

Sizes are written as `512MiB`, `2GiB` and so on. An instance's vCPUs and
memory are committed to it while it runs. A start that the host has no room for is refused.
See [Capacity](../capacity).

## Rate limits

You can limit how fast an instance uses disk and network, so that one busy
guest cannot starve the others of the host's disks or uplink:

| Flag | Limits |
|---|---|
| `--disk-rate` | Bytes per second each disk can be read and written at. |
| `--disk-iops` | Operations per second each disk can be read and written at. |
| `--download-rate` | Bytes per second the guest can receive. |
| `--upload-rate` | Bytes per second the guest can send. |

```console
$ dicer run -d --disk-rate 50MiB --disk-iops 1000 --upload-rate 10MiB postgres:17
```

Rates are written as sizes, such as `50MiB` or `50MiB/s`. Nothing is limited
unless you give a rate. The disk limits apply to each of the instance's disks
separately: its image, its overlay disk and each volume. They are not shared
between them. A guest may briefly exceed its network limits, by the burst
multipliers in the daemon's
[configuration](../../reference/configuration#network-upload-burst-multiplier).

A limit is a ceiling, not a reservation, so it is not counted against the
host's [capacity](../capacity). Change it with `dicer update`, where `0`
removes it. The change takes effect at the next start. An instance restored
from a memory [snapshot](../snapshots) keeps the disk limits it had when the
snapshot was taken.

## Ports

`-p` publishes a guest's port on the host, written as
`[hostIP:]hostPort:guestPort[/tcp|udp]`:

```console
$ dicer run -d -p 8080:80 -p 8443:443 nginx:1.27
$ dicer run -d -p 192.0.2.10:53:53/udp dns-server
```

Without a host address, the port is published on every address the host has
except loopback. So `curl localhost:8080` on the host itself does not reach
the guest. Use the host's own address, or the guest's. See
[Networking](../../concepts/networking#publishing-ports).

## Network, address and hostname

`--network` attaches the instance to a network other than the default, and
`--ip` gives it a fixed address on that network. Without `--ip`, the instance
is given an address when it first starts, and keeps it until it is deleted.

`--hostname` sets the guest's hostname. By default, it is the instance's
name.

## Hypervisor, kernel and init

Most instances need none of these flags. They are there for when the
defaults do not suit a workload.

| Flag | Default | See |
|---|---|---|
| `--hypervisor-type` | `cloud-hypervisor` | [Hypervisors](../../concepts/hypervisors) |
| `--hypervisor-version` | The daemon's default version | [Hypervisors](../../concepts/hypervisors#versions) |
| `--kernel`, `--kernel-args` | The daemon's default kernel | [Kernels](../../concepts/kernels) |
| `--init-mode` | `auto` | [Init modes](../../concepts/init-modes) |

## Labels

Labels are `KEY=VALUE` pairs of your own, for finding instances again:

```console
$ dicer run -d -l team=search -l tier=frontend nginx:1.27
$ dicer ps --filter label=team=search
```

## Standby when idle

`--standby-after` puts an instance on standby once it has been idle that
long, freeing its vCPUs and memory until a connection wakes it. See
[Standby](../standby).

## Changing an instance

`dicer update` changes a stopped instance, and the change takes effect when
it next starts. Only what you give changes. A list, such as `-e`, `-l`, `-p`
or `--mount`, replaces the old one whole.

```console
$ dicer stop web
$ dicer update web --memory 2GiB --vcpus 2
$ dicer start web
```

Only the restart policy and `--standby-after` can be changed while the
instance runs, and they apply at once. See
[Instances](../../concepts/instances#changing-and-deleting).

## Resizing a running instance

`dicer resize` changes a running instance's vCPUs and memory, within the
`--max-vcpus` and `--max-memory` it was given, as
[Capacity](../capacity#resizing-a-running-instance) describes.

## What next

- [Files and volumes](../files-and-volumes): give an instance data that
  outlives it.
- [Restarts](../restarts): keep it running.
- [Health checks](../health-checks): know when it is ready.
- [Working inside guests](../working-inside-guests): get a shell in it.
