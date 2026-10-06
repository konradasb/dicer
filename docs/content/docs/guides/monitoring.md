---
title: Monitoring
weight: 11
description: "Prometheus metrics, live instance stats, events, and the daemon's log and audit."
icon: chart-bar
related:
  - /docs/reference/metrics
  - /docs/reference/events
  - /docs/guides/troubleshooting
---

A Dicer host tells you what it is doing in four ways:

| Source | What it is for |
|---|---|
| [Prometheus metrics](#metrics) | Dashboards and alerts. |
| [`dicer stats`](#instance-stats) | What each instance uses of the host right now. |
| [Events](#events) | What happened to each instance, snapshot, image, network, volume and kernel. |
| [The daemon's log](#the-daemons-log) | What the daemon did, including an audit of every change made through the API. |

## Metrics

The daemon serves Prometheus metrics once you enable them in its
[configuration](../../reference/configuration#metrics):

```yaml {filename="/etc/dicerd/config.yaml"}
metrics:
  enable: true
  listen: 127.0.0.1:9101
```

```console
$ sudo systemctl restart dicerd
$ curl -s 127.0.0.1:9101/metrics | grep '^dicer_instances{'
dicer_instances{state="failed"} 0
dicer_instances{state="paused"} 0
dicer_instances{state="restarting"} 0
dicer_instances{state="running"} 3
dicer_instances{state="standby"} 1
dicer_instances{state="starting"} 0
dicer_instances{state="stopped"} 1
dicer_instances{state="stopping"} 0
```

The endpoint, `/metrics`, has no authentication. It shows how busy the host
is, what it runs and what each instance uses, but nothing from inside the
guests. Serve it on an address that only your Prometheus can reach, such as
`0.0.0.0:9101` behind a firewall, and scrape it:

```yaml {filename="prometheus.yml"}
scrape_configs:
- job_name: dicer
  static_configs:
  - targets: ["dicer1.example.com:9101"]
```

Every metric is listed in the [metrics reference](../../reference/metrics).

### Alerts worth having

```yaml {filename="dicer-alerts.yml"}
groups:
- name: dicer
  rules:
  - alert: DicerInstanceFailed
    expr: dicer_instances{state="failed"} > 0
    for: 5m
    annotations:
      summary: "{{ $value }} instance(s) failed on {{ $labels.instance }}"

  - alert: DicerInstanceUnhealthy
    expr: dicer_instances_health{status="unhealthy"} > 0
    for: 5m

  - alert: DicerInstancesRestarting
    expr: increase(dicer_instance_restarts_total[15m]) > 3

  - alert: DicerMemoryNearlyCommitted
    expr: dicer_instances_memory_bytes / dicer_instances_memory_allocatable_bytes > 0.9
    for: 15m

  - alert: DicerNetworkNearlyFull
    expr: dicer_network_addresses_available < 10

  - alert: DicerOperationsFailing
    expr: rate(dicer_instance_operations_total{outcome="error"}[10m]) > 0
```

The lifecycle metrics count instances but do not name them. To find out
which instance failed, run `dicer ps --filter state=failed` on the host, or
read its [events](#events).

The [instance stats](../../reference/metrics#instance-stats) metrics do name
them, by `instance_id` and `name`. Each running or paused instance has one
series. These queries show what an instance keeps busy and how much data it
moves:

```promql
rate(dicer_instance_cpu_seconds_total[5m])            # host CPUs, 1 = one CPU
dicer_instance_resident_memory_bytes                  # host memory resident
rate(dicer_instance_network_receive_bytes_total[5m])  # bytes received a second
rate(dicer_instance_disk_written_bytes_total[5m])     # bytes written a second
```

Watch the host's disk as well, with the node exporter or similar. Overlay
disks and volumes are sparse, so the space they take grows as guests write. Standby and
memory snapshots also write each guest's memory to disk in full.
`dicer info` shows how much disk is in use and how much has been
provisioned.

## Instance stats

`dicer stats` shows what each running or paused instance uses of the host,
redrawn every second:

```console
$ dicer stats
ID                        NAME  CPUPERC  MEMUSAGE           MEMPERC  NETIO                 BLOCKIO
k3x9m2p4q8r7s6t5u1v0w9x8  db    200.00%  392.9 MiB / 2 GiB  19.19%   491.2 KiB / 11.4 KiB  0 B / 212.6 MiB
nmd8u47u0r2pn1isdl6f2l16  web   0.00%    158.7 MiB / 1 GiB  15.50%   1.3 KiB / 0 B         0 B / 12.5 MiB
```

The daemon reads these numbers on the host, from each instance's hypervisor
process and its TAP device. They work for any image, with nothing installed
in the guest.

| Column | What it shows |
|---|---|
| CPUPerc | A share of one host CPU, so 200% is two CPUs kept busy. It counts the guest's vCPUs and the hypervisor's own threads, which emulate its devices. |
| MemUsage | The hypervisor's resident host memory, then the guest memory committed to the instance. |
| MemPerc | The first MemUsage figure as a share of the second. |
| NetIO | Bytes the guest received, then bytes it transmitted. |
| BlockIO | Bytes the hypervisor read from the host's storage, then bytes it wrote. |

Memory is resident once the guest first touches it, and it stays resident
after the guest frees it. MemUsage therefore grows towards the guest's
memory and does not come down. It is what the instance costs the host, not
what the guest is using now.

BlockIO counts the instance's disks and the hypervisor's own files, such as
the serial console log and a snapshot's memory. It does not count reads the
host serves from its page cache. Writes are counted when the hypervisor
makes them, before they reach the disk.

NetIO and BlockIO are totals since the instance started. The column names
are also the fields of a `--format` template and the keys of
`--format json`. Name instances to watch only those, and use `--no-stream`
to print the stats once, for scripts:

```console
$ dicer stats --no-stream --format json
```

Prometheus has the same counters, and network packets, drops and errors as
well. See the [metrics reference](../../reference/metrics#instance-stats).

## Events

The daemon records what happens to each instance, snapshot, image, network,
volume and kernel: created, started, exited, died, restarting, healthy,
unhealthy, pulled, collected, fetched, deleted and more. Each event has a
message that says why. Every event and its attributes are listed in the
[events reference](../../reference/events).

```console
$ dicer events --since 1h             # the last hour
$ dicer events --name web -n 20       # the last 20 about web
$ dicer events -f                     # follow new events as they happen
```

The daemon keeps events on the host, so they survive a restart of the
daemon. It keeps the last 10,000 by default. To keep more, fewer, or only
recent ones, set the [`events`](../../reference/configuration#events)
section of the configuration.

`--format json` writes one event per line, for scripts. For example, to be
told when an instance dies:

```console
$ dicer events -f --format json | jq -r 'select(.action == "EVENT_ACTION_DIED") | .name'
```

## The daemon's log

The daemon writes its log to standard output, which systemd sends to the
journal:

```console
$ journalctl -u dicerd -f
```

Each line is a message followed by `key=value` attributes. Most lines about
an instance carry `instance=NAME`, which makes them easy to find:

```console
$ journalctl -u dicerd | grep 'instance=web'
```

Set `log_level: debug` in the configuration and restart the daemon for
more detail when tracking down a problem.

### The audit log

The daemon logs every API call that can change something, with
`component=audit`. Calls that only read, such as `Get` and `List` calls,
are left out. Each line names the method, its gRPC status code and how
long it took:

```console
$ journalctl -u dicerd | grep component=audit
time=2026-10-06T09:14:02.511Z level=INFO msg="api call" component=audit method=StopInstance code=OK duration=2.10412875s
```

The audit records what was called, but not by whom or on which resource.
For the resource, match the call to its [event](#events) by time.
