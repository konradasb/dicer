---
title: Metrics
weight: 5
description: "Every Prometheus metric the daemon serves, with its labels."
icon: chart-bar
---

The Prometheus metrics the daemon serves at `/metrics`, once
[enabled](../configuration) with `metrics.enable`. Every metric of Dicer's
own is named `dicer_`. See [Monitoring](../../guides/monitoring) for
scraping and alerts.

The endpoint also serves the Go runtime's `go_*` metrics and the daemon
process's `process_*` metrics, and speaks OpenMetrics to a scraper that asks
for it.

## Daemon

| Metric | Type | Labels | |
|---|---|---|---|
| `dicer_build_info` | gauge | `version`, `commit`, `go_version` | Always 1; the build is in its labels. |
| `dicer_start_time_seconds` | gauge | | When the daemon started, in seconds since the Unix epoch. |

## Instances

| Metric | Type | Labels | |
|---|---|---|---|
| `dicer_instances` | gauge | `state` | Instances defined on the host, by state. Every state is present, at 0 if none. |
| `dicer_instances_health` | gauge | `status` | Instances whose health is checked, by what the check found. |
| `dicer_instances_vcpus` | gauge | | vCPUs held by instances that are starting, running or paused. |
| `dicer_instances_vcpus_allocatable` | gauge | | vCPUs instances may hold in total. A start beyond it is refused. |
| `dicer_instances_memory_bytes` | gauge | | Memory held by instances that are starting, running or paused. |
| `dicer_instances_memory_allocatable_bytes` | gauge | | Memory instances may hold in total. A start beyond it is refused. |
| `dicer_instance_operations_total` | counter | `operation`, `outcome` | Lifecycle operations, by how they ended. |
| `dicer_instance_operation_duration_seconds` | histogram | `operation` | How long lifecycle operations took. |
| `dicer_instance_restarts_total` | counter | | Instances started again by their restart policy. |

The allocatable amounts are the host's CPUs and memory, less the reserve,
multiplied by the overcommit, as the `resources` section of the
configuration sets them.

## Images

| Metric | Type | Labels | |
|---|---|---|---|
| `dicer_images` | gauge | | Images held on the host. |
| `dicer_image_disk_bytes` | gauge | | Size of the disks those images were converted to. |
| `dicer_image_pulls_total` | counter | `outcome` | Pulls that reached a registry. A pull of an image already held is not counted. |
| `dicer_image_pull_duration_seconds` | histogram | | How long a pull took, from resolving the reference to a disk ready to boot. |
| `dicer_image_downloaded_bytes_total` | counter | | Compressed layer bytes downloaded from registries. |
| `dicer_image_conversion_duration_seconds` | histogram | | How long converting an image to its disk took. |
| `dicer_image_cache_lookups_total` | counter | `result` | Pulls by whether the host already held the image. |
| `dicer_image_gc_collected_total` | counter | `reason` | Images garbage collection removed. |
| `dicer_image_gc_reclaimed_bytes_total` | counter | | Disk space garbage collection gave back, disks and cached layers together. |

## Networks

| Metric | Type | Labels | |
|---|---|---|---|
| `dicer_network_addresses_allocated` | gauge | `network` | Addresses given to instances on a network. |
| `dicer_network_addresses_available` | gauge | `network` | Addresses a network has still to give. |

## API

| Metric | Type | Labels | |
|---|---|---|---|
| `dicer_grpc_requests_total` | counter | `method`, `code` | API calls, by method and the gRPC status code they ended with. |
| `dicer_grpc_request_duration_seconds` | histogram | `method` | How long API calls took. For a stream, such as `logs -f`, it is how long the client stayed. |

## Label values

| Label | Values |
|---|---|
| `state` | `Stopped`, `Starting`, `Running`, `Paused`, `Stopping`, `Restarting`, `Failed` |
| `status` | `starting`, `healthy`, `unhealthy` |
| `operation` | `start`, `stop`, `pause`, `resume`, `delete`, `create_snapshot`, `restore_snapshot`, `delete_snapshot` |
| `outcome` | `success`, `error` |
| `result` | `hit`, `miss` |
| `reason` | `unused`, `size` |
| `network` | A network's name. |
| `method` | A full gRPC method, such as `/dicerd.v1.DaemonService/StartInstance`. |
| `code` | A gRPC status code, such as `OK` or `NotFound`. |
