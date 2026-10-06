---
title: Health checks
weight: 3
description: "Check that a workload is well, use an image's own check, and restart one that is not."
icon: heart
related:
  - /docs/guides/restarts
  - /docs/guides/monitoring
---

A running instance is not necessarily a working one: a server can be up but
answer nothing. A health check asks the workload, from inside the guest,
whether it is well, and records the answer.

## Add a check

A check uses one of three probes. The guest agent runs each probe inside the
guest:

| Flag | Healthy when |
|---|---|
| `--health-http PORT[/path]` | An HTTP GET to `127.0.0.1:PORT/path` answers 2xx or 3xx. Redirects are not followed. |
| `--health-tcp PORT` | A connection to `127.0.0.1:PORT` is accepted. |
| `--health-cmd 'COMMAND'` | The command, run by `/bin/sh`, exits 0. |

```console
$ dicer run -d --name api -p 8080:3000 --health-http 3000/healthz ghcr.io/acme/api:3
$ dicer run -d --name db --health-cmd 'pg_isready -U postgres' postgres:17
$ dicer run -d --name cache --health-tcp 6379 redis:7
```

The HTTP and TCP probes need nothing in the image, so they suit minimal
images with no shell. A command runs with the instance's environment, and
what it prints is kept as the check's output.

## Timing

| Flag | Default | Meaning |
|---|---|---|
| `--health-interval` | 10s | Time from the end of one probe to the start of the next. The first probe runs one interval after the instance starts. |
| `--health-timeout` | 5s | A probe that takes longer has failed. |
| `--health-retries` | 3 | Failures in a row that make the instance unhealthy. |
| `--health-start-period` | none | Time after a start in which failures do not count, for a workload that is slow to come up. A success counts at once. |

```console
$ dicer run -d --name search \
    --health-http 9200/_cluster/health \
    --health-interval 30s --health-start-period 2m \
    ghcr.io/acme/search:8
```

## What the results mean

```mermaid
stateDiagram-v2
  [*] --> starting: instance starts
  starting --> healthy: a probe succeeds
  starting --> unhealthy: retries failures in a row
  healthy --> unhealthy: retries failures in a row
  unhealthy --> healthy: a probe succeeds
```

An instance is `starting` until its first probe succeeds. One success makes
it `healthy`. After `--health-retries` failures in a row, outside the start
period, it is `unhealthy`. One success makes it `healthy` again.

Health is shown beside the instance's status:

```console
$ dicer ps --columns name,status
NAME  STATUS
api   Up 5 minutes (healthy)
$ dicer inspect api
…
     Health: healthy, checked 6 seconds ago
             http :3000/healthz every 10s
             timeout 5s, 3 retries
```

Each change of health is also an [event](../monitoring#events), `healthy` or
`unhealthy`, with the first line of the probe's output. See them with
`dicer events --name api`.

A paused instance is not probed. Each new start, of the instance or of the
daemon, begins again at `starting`.

## When an instance is unhealthy

What happens depends on the instance's [restart policy](../restarts):

- **`no`**, the default: nothing. The instance is reported unhealthy and
  keeps running, for you to look into.
- **`on-failure`, `unless-stopped` or `always`**: the daemon stops the
  instance, as `dicer stop` would, and records the end as a failure, with
  the reason "health check … failed 3 times in a row". The restart policy
  then starts it again, and the restart counts towards an `on-failure`
  limit.

An `on-failure:N` instance that has already been restarted `N` times in a
row is not stopped: its policy would not restart it, so it is reported
unhealthy and left running.

So a check and a restart policy together keep alive a workload that hangs
rather than exits:

```console
$ dicer run -d --name api --restart on-failure:5 --health-http 3000/healthz ghcr.io/acme/api:3
```

{{< callout type="info" >}}
  Docker only reports an unhealthy container, whatever its restart policy.
  Dicer restarts it, if its restart policy restarts failures.
{{< /callout >}}

## The image's own check

If the instance sets no check, the image's `HEALTHCHECK` is used, with its
timings. A check given with flags replaces the image's check entirely.
`--no-healthcheck` turns health checking off, including the image's check.

## Changing a check

A check is part of the instance's definition, so you change it with
`dicer update` while the instance is stopped. A check given to
`dicer update` replaces the old one entirely:

```console
$ dicer stop api
$ dicer update api --health-http 3000/readyz
$ dicer start api
```
