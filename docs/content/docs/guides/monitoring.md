---
title: Monitoring
weight: 10
description: "Prometheus metrics, events, and the daemon's log and audit."
icon: chart-bar
related:
  - /docs/reference/metrics
  - /docs/reference/events
  - /docs/guides/troubleshooting
---

A Dicer host tells you what it is doing in three ways: Prometheus metrics,
for dashboards and alerts; events, for what happened to each instance and
image; and the daemon's log, which includes an audit of every change made
through the API.

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
dicer_instances{state="Running"} 3
dicer_instances{state="Stopped"} 1
…
```

The endpoint, `/metrics`, has no authentication. It says how busy the host
is and what it runs, but nothing about what is inside the guests. Serve it
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
  expr: dicer_instances{state="Failed"} > 0
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

Metrics count instances, but do not name them: to find which instance
failed, ask the host with `dicer ps --filter state=failed`, or read its
events.

Watch the host's disk as well, with the node exporter or the like: instance
disks are sparse, so the space they take grows as guests write. `dicer info`
shows how much is in use and how much has been promised.

## Events

The daemon records what happens to each instance and image: created,
started, exited, died, restarting, healthy, unhealthy, pulled, collected
and more. Each event carries a message that says why.

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
