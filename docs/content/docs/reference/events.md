---
title: Events
weight: 6
description: "Every event Dicer records of instances, snapshots, images, networks, volumes and kernels, with its attributes."
icon: bell
---

The daemon records events about what happens to the instances, snapshots,
images, networks, volumes and kernels on its host, and why.
[`dicer events`](../cli/dicer_events) shows them, and `dicer inspect` shows
an instance's last ten. Each event below is listed by the name those
commands print, and by its `action` in `--format json`.

## Fields

| Field | Description |
|---|---|
| `time` | When it happened. |
| `kind` | What it is about: an instance, a snapshot, an image, a network, a volume or a kernel. |
| `id` | The resource's ID. It tells apart two resources that had the same name at different times. Images have no ID, and are known by their name. |
| `name` | The resource's name. An image's name is its full reference, such as `docker.io/library/busybox:latest`, which `dicer events` shortens to `busybox:latest`. |
| `action` | What happened: one of the events below. |
| `message` | What happened, in one line for a person to read. |
| `attributes` | What happened, for a program. Each event's attributes are listed in its table. Every value is a string. |

## The events file

The daemon keeps events in `events.jsonl` in its
[`data_dir`](../configuration#data-dir), which is
`/var/lib/dicer/events.jsonl` by default. It writes one JSON object per
line, so events survive a restart of the daemon. It keeps the most recent
[`events.max_count`](../configuration#events-max-count), 10,000 by default.
With [`events.max_age`](../configuration#events-max-age) set, it also drops
events older than that.

`dicer events` asks the running daemon for events. When no daemon is
running, you can read the file directly, with `jq` for example.

The file writes `kind` and `action` in lower case, such as `instance` and
`died`. `dicer events --format json` writes them as the API names them,
such as `EVENT_KIND_INSTANCE` and `EVENT_ACTION_DIED`:

```json
{"time":"2026-09-22T09:52:07Z","kind":"instance","id":"r4kq2x7m9c1v8b3n6p0z5wte","name":"grafana","action":"died","message":"Instance failed after running for 2h 14m: exit code 1","attributes":{"exit_code":"1"}}
```

## Instances

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Created | `EVENT_ACTION_CREATED` | `image`, `snapshot`, `source_instance` | The instance was created: its definition was recorded, after pulling its image if needed. Nothing is booted yet. `image` is its image's reference. An instance forked with `dicer snapshot fork` has `snapshot`, the snapshot it is a copy of. One forked with `dicer fork` has `source_instance`, the instance it is a copy of. |
| Updated | `EVENT_ACTION_UPDATED` | | The instance's definition was changed with `dicer update`. The message lists what changed and when it takes effect: at the next start, or, for a running instance's restart policy, when it next ends. |
| Renamed | `EVENT_ACTION_RENAMED` | `previous_name` | The instance was renamed. `name` is its new name, and `previous_name` is the one it had before. |
| Resized | `EVENT_ACTION_RESIZED` | `vcpus`, `memory_bytes` | The running instance was given more or fewer vCPUs or memory with `dicer resize`, and keeps them from its next start. `vcpus` and `memory_bytes` are what it has now. A resize that the guest failed records no event. |
| Started | `EVENT_ACTION_STARTED` | `ip`, `restart_count`, `snapshot`, `source_instance`, `woken_by_port` | The instance's hypervisor started and the guest is booting or resuming. `ip` is its address. See [How an instance was started](#how-an-instance-was-started) for the other attributes. |
| Stopped | `EVENT_ACTION_STOPPED` | | The instance was stopped with `dicer stop`. The message says whether the guest shut down within the grace period or its hypervisor was ended. For an instance on standby, it says that what it froze was discarded. |
| Paused | `EVENT_ACTION_PAUSED` | | The instance's vCPUs were halted with `dicer pause`. Its memory is kept. |
| Resumed | `EVENT_ACTION_RESUMED` | | A paused instance's vCPUs were started again with `dicer resume`. |
| Standby | `EVENT_ACTION_STANDBY` | `size_bytes`, `idle_seconds` | The instance was put on standby: its guest was frozen to disk and its hypervisor ended, releasing its CPU and memory. `size_bytes` is the disk space the frozen guest takes. An instance put on standby for being idle for its `standby_after` has `idle_seconds`, how long it had been idle. One put on standby with `dicer standby` has none. Resuming it records Started. |
| Exited | `EVENT_ACTION_EXITED` | `exit_code` | The guest ended cleanly, of its own accord: its program exited with code 0, which is `exit_code`. |
| Died | `EVENT_ACTION_DIED` | `exit_code` | The guest ended in failure, or the instance failed to start. See [Why an instance died](#why-an-instance-died). |
| Restarting | `EVENT_ACTION_RESTARTING` | `delay`, `restart_count` | The instance ended, recorded as Exited or Died just before, and its restart policy will start it again. `delay` is how long it waits first, as a Go duration. `restart_count` is which restart in a row this is. |
| Healthy | `EVENT_ACTION_HEALTHY` | | The instance's health check passed, after it had been starting or unhealthy. The message has the first line of the check's output. |
| Unhealthy | `EVENT_ACTION_UNHEALTHY` | `failing_streak` | The instance's health check failed `failing_streak` times in a row, which is its number of retries. The message has the first line of the last check's output. |
| Snapshot restored | `EVENT_ACTION_SNAPSHOT_RESTORED` | `snapshot` | The instance was put back as the snapshot named `snapshot` holds it. From a memory snapshot, it was started with its memory and overlay disk rolled back to when the snapshot was taken, and no Started event is recorded. From a disk snapshot, only its overlay disk was rolled back, and it was left stopped. |
| Deleted | `EVENT_ACTION_DELETED` | | The instance was deleted: its definition and disks were removed, and its address on its network was released. Its snapshots are kept. |

### How an instance was started

A Started event's attributes say how the instance came to start:

| How | Attributes | Message begins |
|---|---|---|
| Started by its restart policy | `restart_count`: how many times in a row the policy has restarted it | Restarted instance |
| Started any other way | none of these | Started instance |
| Forked from a memory snapshot, and resumed | `snapshot`: the snapshot it was forked from | Started instance from memory snapshot … |
| Forked from a running or paused instance | `source_instance`: the instance it was forked from | Started instance from instance … |
| Resumed from standby by `dicer start` | none of these | Resumed instance from standby |
| Woken from standby by a connection | `woken_by_port`: the published host port the connection came to | Resumed instance from standby, woken by a connection |

### Why an instance died

A Died event's message says why the instance failed:

- its program exited with a non-zero code, which `exit_code` is;
- the guest reset, after a kernel panic or a reboot;
- the hypervisor exited;
- its health check failed, and its restart policy acts on that;
- it failed to start, or to resume from standby;
- a start or stop was interrupted by the daemon restarting.

`exit_code` is present only when the guest reported one. When the restart
policy gives up, the message says so too.

An instance that crashes and is restarted by its policy records Died,
Restarting and Started in turn. `dicer events --name NAME` is therefore its
timeline: why it ended, how long it waited, and how it came back.

## Snapshots

Every snapshot event has an `instance` attribute: the instance the snapshot
was taken of, by the name it had at the time.

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Created | `EVENT_ACTION_CREATED` | `instance`, `kind`, `size_bytes`, `paused_seconds` | The snapshot was taken with `dicer snapshot create`. `kind` is `memory`, of a running or paused instance's memory and overlay disk, or `disk`, of a stopped or failed instance's overlay disk alone. `size_bytes` is the disk space it takes. `paused_seconds` is how long a running instance was paused to take it. Copying the overlay disk can make that long where the filesystem cannot reflink. A snapshot that paused nothing has no `paused_seconds`. |
| Deleted | `EVENT_ACTION_DELETED` | `instance` | The snapshot was deleted, along with its files. |

Restoring a snapshot is recorded as an event of its instance: Snapshot
restored, above.

## Images

Every image event has a `digest` attribute: the image's `sha256:` digest.

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Pulled | `EVENT_ACTION_PULLED` | `digest`, `size_bytes` | The image was pulled and made into a boot disk. `size_bytes` is the boot disk's size. The message says how much was downloaded, or that the layers were already cached. |
| Collected | `EVENT_ACTION_COLLECTED` | `digest`, `reason` | Garbage collection removed the image and its boot disk. `reason` is `unused` when no instance had used the image for longer than `images.gc_max_unused_age`. It is `size` when the image store was over `images.gc_max_size` and this image was the least recently used. |
| Deleted | `EVENT_ACTION_DELETED` | `digest`, `by` | The image and its boot disk were removed. `by` is `user` when it was removed with `dicer image delete`, or `prune` when `dicer image prune` removed it because no instance used it. |

## Networks

Every network event has `subnet` and `gateway` attributes.

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Created | `EVENT_ACTION_CREATED` | `subnet`, `gateway` | The network was created with `dicer network create`. The message also says whether it is isolated. |
| Deleted | `EVENT_ACTION_DELETED` | `subnet`, `gateway` | The network was deleted, and the addresses it had allocated were released. |

## Volumes

Every volume event has a `size_bytes` attribute.

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Created | `EVENT_ACTION_CREATED` | `size_bytes` | The volume was created with `dicer volume create`, and formatted as ext4. |
| Deleted | `EVENT_ACTION_DELETED` | `size_bytes` | The volume was deleted, along with its data. |

## Kernels

Every kernel event has a `url` attribute.

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Imported | `EVENT_ACTION_IMPORTED` | `url`, `arch` | The kernel was imported with `dicer kernel import`. It is recorded by its URL, and fetched when an instance first starts with it. `arch` is its architecture. The message says when it has no checksum to verify it against. |
| Fetched | `EVENT_ACTION_FETCHED` | `url`, `fetched_bytes` | The kernel was downloaded, or copied from a local path, and verified against its checksum if it has one. `fetched_bytes` is its size. |
| Deleted | `EVENT_ACTION_DELETED` | `url`, `arch` | The kernel was deleted, along with its fetched copy if it had one. |
