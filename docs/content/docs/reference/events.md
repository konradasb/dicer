---
title: Events
weight: 6
description: "Every event Dicer records of instances, snapshots, images, networks, volumes and kernels, with its attributes."
icon: bell
---

The events the daemon records of what happened to the instances, snapshots,
images, networks, volumes and kernels on its host, and why.
[`dicer events`](../cli/dicer_events) shows them, and `dicer inspect` an
instance's last ten; each event below is named as they print it, and by its
`action` in `--format json`.

## Fields

| Field | Description |
|---|---|
| `time` | When it happened. |
| `kind` | What it is about: an instance, a snapshot, an image, a network, a volume or a kernel. |
| `id` | The resource's ID, which tells apart two resources that had the same name at different times. An image has none: it is known by its name. |
| `name` | The resource's name. An image's is its full reference, such as `docker.io/library/busybox:latest`, which `dicer events` shortens to `busybox:latest`. |
| `action` | What happened: one of the events below. |
| `message` | What happened, in a line for a person. |
| `attributes` | What happened, for a program: each event's are in its table. Every value is a string. |

## The file

The daemon keeps the events in `events.jsonl` in its
[`data_dir`](../configuration#data-dir), `/var/lib/dicer/events.jsonl` unless
set, one JSON object a line, so that they outlive a restart. It keeps the most
recent [`events.max_count`](../configuration#events-max-count), 10,000 unless
set, and, with [`events.max_age`](../configuration#events-max-age) set, none
older. `dicer events` asks the running daemon for them; with no daemon
running, the file can be read as it is, with `jq` say.

In the file, `kind` and `action` are lower case, `instance` and `died`;
`dicer events --format json` writes them as the API's names,
`EVENT_KIND_INSTANCE` and `EVENT_ACTION_DIED`:

```json
{"time":"2026-09-22T09:52:07Z","kind":"instance","id":"r4kq2x7m9c1v8b3n6p0z5wte","name":"grafana","action":"died","message":"Instance failed after running for 2h 14m: exit code 1","attributes":{"exit_code":"1"}}
```

## Instances

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Created | `EVENT_ACTION_CREATED` | `image` | The instance was created: its definition recorded, its image pulled first if need be. Nothing is booted. `image` is its image's reference. |
| Updated | `EVENT_ACTION_UPDATED` | | The instance's definition was changed with `dicer update`. The message lists what changed, and whether it takes effect on the next start or, for a running instance's restart policy, when it next ends. |
| Renamed | `EVENT_ACTION_RENAMED` | `previous_name` | The instance was given another name, its `name`; `previous_name` is the one it had. |
| Resized | `EVENT_ACTION_RESIZED` | `vcpus`, `memory_bytes` | The running instance was given other vCPUs or memory with `dicer resize`, and keeps them from its next start. `vcpus` and `memory_bytes` are what it has now. A resize the guest failed records none. |
| Started | `EVENT_ACTION_STARTED` | `ip`, `restart_count` | The instance's hypervisor started and the guest is booting. `ip` is its address. Started by its restart policy, it is Restarted in the message, and `restart_count` is how many times in a row the policy has restarted it; started otherwise, it has no `restart_count`. |
| Stopped | `EVENT_ACTION_STOPPED` | | The instance was stopped with `dicer stop`. The message says whether the guest shut down within the grace period or its hypervisor was shut down. |
| Paused | `EVENT_ACTION_PAUSED` | | The instance's vCPUs were halted with `dicer pause`; its memory is kept. |
| Resumed | `EVENT_ACTION_RESUMED` | | A paused instance's vCPUs were started again with `dicer resume`. |
| Exited | `EVENT_ACTION_EXITED` | `exit_code` | The guest ended cleanly, of its own accord: its program exited with code 0, which `exit_code` is. |
| Died | `EVENT_ACTION_DIED` | `exit_code` | The guest ended in failure, or the instance failed to start. The message says why: its program exited with another code, which `exit_code` is; the guest reset, after a kernel panic or a reboot; the hypervisor exited; its health check failed with a restart policy to act on it; a restart failed; or a start or stop was interrupted by the daemon restarting. `exit_code` is there only when the guest reported one. When the restart policy gives up, the message says so too. |
| Restarting | `EVENT_ACTION_RESTARTING` | `delay`, `restart_count` | The instance ended, Exited or Died just before, and its restart policy will start it again. `delay` is how long it waits first, as a Go duration, and `restart_count` which restart in a row this is. |
| Healthy | `EVENT_ACTION_HEALTHY` | | The instance's health check passed, after it had been starting or unhealthy. The message has the first line of the check's output. |
| Unhealthy | `EVENT_ACTION_UNHEALTHY` | `failing_streak` | The instance's health check failed `failing_streak` times in a row, its retries. The message has the first line of the last check's output. |
| Snapshot restored | `EVENT_ACTION_SNAPSHOT_RESTORED` | `snapshot` | The instance was put back as snapshot `snapshot` holds it. From a memory snapshot, it was started with its memory and disk rolled back to when the snapshot was taken; from a disk snapshot, its disk was rolled back, and it was left stopped. |
| Deleted | `EVENT_ACTION_DELETED` | | The instance was deleted: its definition and disks removed, and its address on its network released. Its snapshots are kept. |

An instance that crashes and is restarted by its policy records Died,
Restarting and Started in turn, so `dicer events --name NAME` is its
timeline: why it ended, how long it waited, and how it came back.

## Snapshots

Every snapshot event has among its attributes the `instance` it was taken of,
by the name it had then.

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Created | `EVENT_ACTION_CREATED` | `instance`, `kind`, `size_bytes`, `paused_seconds` | The snapshot was taken with `dicer snapshot create`. `kind` is `memory`, of a running or paused instance's memory and disk, or `disk`, of a stopped instance's disk alone; `size_bytes` is the space it takes. `paused_seconds` is how long a running instance was paused to take it, which the copy of its disk can make long where the filesystem cannot reflink; a snapshot that paused nothing has none. |
| Deleted | `EVENT_ACTION_DELETED` | `instance` | The snapshot was deleted, and its files with it. |

Restoring a snapshot is an event of its instance: Snapshot restored, above.

## Images

Every image event has its `digest` among its attributes, the image's
`sha256:` digest.

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Pulled | `EVENT_ACTION_PULLED` | `digest`, `size_bytes` | The image was pulled and made into a boot disk. `size_bytes` is the boot disk's size. The message says how much was downloaded, or that the layers were already cached. |
| Collected | `EVENT_ACTION_COLLECTED` | `digest`, `reason` | Garbage collection removed the image and its boot disk. `reason` is `unused`, when no instance had used it for longer than `images.gc_max_unused_age`; or `size`, when the image store was over `images.gc_max_size` and it was the least recently used. |
| Deleted | `EVENT_ACTION_DELETED` | `digest`, `by` | The image and its boot disk were removed. `by` is `user`, when it was removed with `dicer image delete`; or `prune`, when `dicer image prune` removed it, as no instance used it. |

## Networks

Every network event has its `subnet` and `gateway` among its attributes.

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Created | `EVENT_ACTION_CREATED` | `subnet`, `gateway` | The network was created with `dicer network create`. The message says too whether it is isolated. |
| Deleted | `EVENT_ACTION_DELETED` | `subnet`, `gateway` | The network was deleted, and the addresses it had allocated forgotten. |

## Volumes

Every volume event has its `size_bytes` among its attributes.

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Created | `EVENT_ACTION_CREATED` | `size_bytes` | The volume was created with `dicer volume create`, and formatted ext4. |
| Deleted | `EVENT_ACTION_DELETED` | `size_bytes` | The volume was deleted, and its data with it. |

## Kernels

Every kernel event has its `url` among its attributes.

| Event | `action` | Attributes | What it means |
|---|---|---|---|
| Imported | `EVENT_ACTION_IMPORTED` | `url`, `arch` | The kernel was imported with `dicer kernel import`: recorded by its URL, to be fetched when an instance first starts with it. `arch` is its architecture. The message says when it has no checksum to verify it by. |
| Fetched | `EVENT_ACTION_FETCHED` | `url`, `fetched_bytes` | The kernel was downloaded, or copied from a local path, and verified against its checksum if it has one. `fetched_bytes` is its size. |
| Deleted | `EVENT_ACTION_DELETED` | `url`, `arch` | The kernel was deleted, and its fetched copy with it, if it had been fetched. |
