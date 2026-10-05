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
instance and boots it. `--pull always` pulls it even if the host has it, to
follow a tag that has moved, and `--pull never` uses only the image the host
holds; see [Managing images](../managing-images#choose-when-an-image-is-pulled). With `-d` it returns once the guest is running,
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
| `--max-vcpus` | none | The most vCPUs the running instance can be [resized](#resizing-a-running-instance) to. Cloud Hypervisor only. |
| `--max-memory` | none | The most memory the running instance can be resized to. |

Sizes are written as `512MiB`, `2GiB` and so on. An instance holds its vCPUs
and memory while it runs, and a start the host has no room for is refused;
see [Capacity](../capacity).

## Rate Limits

Disk and network can be held to a rate, so that one busy guest
cannot starve the others of the host's disk or uplink:

| Flag | |
|---|---|
| `--disk-rate` | Bytes per second each disk can be read and written at. |
| `--disk-iops` | Operations per second each disk can be read and written at. |
| `--download-rate` | Bytes per second the guest can receive. |
| `--upload-rate` | Bytes per second the guest can send. |

```console
$ dicer run -d --disk-rate 50MiB --disk-iops 1000 --upload-rate 10MiB postgres:17
```

Rates are written as sizes, `50MiB` or `50MiB/s`, and none is limited
unless given. The disk limits apply to each of the instance's disks on its
own, its image, its own disk and every volume, not to all of them together.
A guest may briefly exceed its network limits, by the burst multipliers in
the daemon's [configuration](../../reference/configuration#network-upload-burst-multiplier).

Limit is a ceiling, not a reservation: it is not counted against the
host's [capacity](../capacity). It is changed with `dicer update`, with 0
removing it, and takes effect on the next start. An instance restored from a
[snapshot](../snapshots) keeps the disk limits it had when the snapshot was
taken.

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

## Resizing a running instance

`dicer resize` changes a running instance's vCPUs and memory without
restarting it, and its definition with them, so it keeps them when it next
starts. It needs room to grow into, set aside when the instance starts:
`--max-vcpus` and `--max-memory`, given when it is created or, while it is
stopped, with `dicer update`.

```console
$ dicer run -d --name db --memory 1GiB --max-memory 8GiB --vcpus 2 --max-vcpus 8 postgres:17
$ dicer resize db --memory 4GiB --vcpus 4
```

The room set aside costs the host nothing until it is used: the instance
holds what it has, not its maximum, and growing is refused when the host has
no room, as a start is. See [Capacity](../capacity#when-resources-are-held).

- **Memory** can be resized from what the instance started with up to its
  maximum, in steps of 2 MiB. Shrinking asks the guest to give memory back,
  which it can refuse if it is using it. On Firecracker, `dicer resize`
  waits for the guest; Cloud Hypervisor cannot tell when the guest has taken
  the change.
- **vCPUs** can be resized on Cloud Hypervisor only, from 1 up to the
  maximum. Firecracker cannot add vCPUs to a running guest. The guest agent
  brings each added vCPU online, as udev would on a distribution.

Firecracker sets the memory aside at 512 GiB in the guest's address space,
which a host CPU with fewer than 40 bits of physical address cannot reach:
there, its guests cannot take more memory. `grep 'address sizes'
/proc/cpuinfo` on the host says how many it has.

The guest's kernel has to support it: memory hotplug through virtio-mem
(`CONFIG_VIRTIO_MEM`), and CPU hotplug (`CONFIG_HOTPLUG_CPU`), as Dicer's
kernel does. If the guest
does not take the change, the instance keeps holding the larger of the two
sizes, and has the new one from its next start. Each resize is recorded as a
[Resized event](../../reference/events#instances).

## What next

- [Files and volumes](../files-and-volumes): give an instance data that
  outlives it.
- [Restarts](../restarts): keep it running.
- [Working inside guests](../working-inside-guests): get a shell in it.
