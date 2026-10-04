---
title: Monitoring
weight: 10
description: "Prometheus metrics, live instance stats, events, and the daemon's log and audit."
icon: chart-bar
related:
  - /docs/reference/metrics
  - /docs/reference/events
  - /docs/guides/troubleshooting
---

A Dicer host tells you what it is doing in four ways: Prometheus metrics,
for dashboards and alerts; `dicer stats`, for what each instance uses of the
host right now; events, for what happened to each instance, image, network
and volume; and the daemon's log, which includes an audit of every change
made through the API.

## Metrics

The daemon serves Prometheus metrics once enabled in its
[configuration](../../reference/configuration):

```yaml {filename="/etc/dicerd/config.yaml"}
metrics:
  enable: true
  listen: 127.0.0.1:9101
```

```console
$ sudo systemctl restart dicerd
$ curl -s 127.0.0.1:9101/metrics | grep '^dicer_instances{'
dicer_instances{state="running"} 3
dicer_instances{state="stopped"} 1
…
```

The endpoint, `/metrics`, has no authentication. It says how busy the host
is, what it runs and what each instance uses of it, but nothing about what
is inside the guests. Serve it
on an address only your Prometheus can reach, such as `0.0.0.0:9101` behind
a firewall, and scrape it:

```yaml {filename="prometheus.yml"}
scrape_configs:
- job_name: dicer
  static_configs:
  - targets: ["dicer1.example.com:9101"]
```

Every metric is listed in the [metrics reference](../../reference/metrics).

### Alerts worth having

```yaml {filename="dicer-alerts.yml"}
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

The lifecycle metrics count instances, but do not name them: to find which
instance failed, ask the host with `dicer ps --filter state=failed`, or read
its events.

The [instance stats](../../reference/metrics#instance-stats) do name them,
by `instance_id` and `name`, one series each while an instance runs. What an
instance keeps busy, and what it moves:

```promql
rate(dicer_instance_cpu_seconds_total[5m])            # host CPUs, 1 = one CPU
dicer_instance_resident_memory_bytes                  # host memory resident
rate(dicer_instance_network_receive_bytes_total[5m])  # bytes a second in
rate(dicer_instance_disk_written_bytes_total[5m])     # bytes a second to disk
```

Watch the host's disk as well, with the node exporter or the like: instance
disks are sparse, so the space they take grows as guests write. `dicer info`
shows how much is in use and how much has been promised.

## Instance stats

`dicer stats` shows what each running instance uses of the host, redrawn
every second:

```console
$ dicer stats
ID                        NAME  CPUPERC  MEMUSAGE           MEMPERC  NETIO                 BLOCKIO
k3x9m2p4q8r7s6t5u1v0w9x8  db    200.00%  392.9 MiB / 2 GiB  19.19%   491.2 KiB / 11.4 KiB  0 B / 212.6 MiB
nmd8u47u0r2pn1isdl6f2l16  web   0.00%    158.7 MiB / 1 GiB  15.50%   1.3 KiB / 0 B         0 B / 12.5 MiB
```

It is read on the host, from each instance's hypervisor process and its TAP
device, so it works for any image, with nothing installed in the guest:

- **CPUPerc** is a share of one host CPU: 200% is two kept busy. It counts the
  guest's vCPUs and the hypervisor's own threads, which emulate its devices.
- **MemUsage** is the hypervisor's resident host memory / the guest memory
  committed to the instance, and **MemPerc** the one as a share of the
  other. A guest's memory is backed as it first touches it, and stays
  resident when the guest frees it, so this grows towards the guest's memory
  and does not come down; it is what the instance costs the host, not what
  the guest is using now.
- **NetIO** is what the guest received / transmitted.
- **BlockIO** is what the hypervisor read from / wrote to the host's
  storage: the instance's disks, and its own files, such as the serial
  console log and a snapshot's memory. Reads the host serves from its page
  cache are not counted, and writes are counted as the hypervisor makes
  them, before they reach the disk.

NetIO and BlockIO are totals since the instance started. The column names
are also the fields of a `--format` template, and the keys of
`--format json`. Name instances to watch only those, and use `--no-stream`
to print the stats once, for scripts:
`dicer stats --no-stream --format json`. Prometheus has the same counters,
and network packets, drops and errors besides: see
[Metrics](../../reference/metrics).

## Events

The daemon records what happens to each instance, image, network and
volume: created, started, exited, died, restarting, healthy, unhealthy,
pulled, collected, deleted and more. Each event carries a message that says
why.

```console
$ dicer events --since 1h             # the last hour
$ dicer events --name web -n 20       # the last 20 about web
$ dicer events -f                     # and keep following
```

Events are kept on the host, across restarts of the daemon: the last 10,000
by default, which the `events` section of the configuration changes.

`--format json` writes one event a line, for scripts. To be told when an
instance dies:

```console
$ dicer events -f --format json | jq -r 'select(.action == "died") | .name'
```

## The daemon's log

The daemon logs to the journal:

```console
$ journalctl -u dicerd -f
```

`log_level: debug` in the configuration adds detail, for tracking a
problem down.
