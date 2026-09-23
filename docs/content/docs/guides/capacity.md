---
title: Capacity
weight: 9
description: "How much CPU and memory instances may be given, overcommit and the reserve, and what is in use."
icon: scale
related:
  - /docs/guides/monitoring
  - /docs/reference/configuration
---
Each instance asks for vCPUs and memory, and holds them while it runs. The
daemon keeps count, and refuses a start the host has no room for, rather
than letting instances crowd each other out. This guide covers how much
room there is, how to change it, and how to see what is in use.

## What the host allows

When it starts, the daemon reads the host's logical CPUs and memory, and
works out what instances may be given in total:

| | Instances may be given |
|---|---|
| vCPUs | the host's CPUs × `cpu_overcommit` |
| Memory | (the host's memory − `reserved_memory_bytes`) × `memory_overcommit` |

The defaults allow four vCPUs to each CPU, and all the memory but 1 GiB, with
no overcommit:

```yaml {filename="/etc/dicerd/config.yaml"}
resources:
  cpu_overcommit: 4
  memory_overcommit: 1
  reserved_memory_bytes: 1073741824   # 1 GiB
```

On a host with 16 CPUs and 64 GiB, that is 64 vCPUs and 63 GiB. See the
[configuration reference](../../reference/configuration) for the settings,
and restart the daemon after changing them; running guests are not
affected.

## What is in use

`dicer info` shows how much is held, of how much:

```console
$ sudo dicer info
…
           vCPU: █████░░░░░░░░░░░░░░░  4 of 16         25%
                 4 CPUs, 4× overcommit
         Memory: ██████████░░░░░░░░░░  15.5 of 31 GiB  50%
                 32 GiB, 1 GiB reserved
           Disk: ██░░░░░░░░░░░░░░░░░░  25 of 250 GiB   10%
                 40 GiB provisioned
```

`dicer ps --wide` shows what each instance was given, and
`dicer info --format json` lists the instances holding resources, for
scripts. The same numbers are [metrics](../monitoring#metrics):
`dicer_instances_vcpus` and `dicer_instances_memory_bytes`, against
`dicer_instances_vcpus_allocatable` and
`dicer_instances_memory_allocatable_bytes`.

## When resources are held

An instance holds its vCPUs and memory while it is **starting, running or
paused**: a paused guest still sits in memory. A stopped or failed instance
holds nothing, and neither does one waiting to be [restarted](../restarts).

So resources are checked when an instance starts, not when it is created:

- **Creating** an instance, or updating it, is refused only if it could never
  start on the host: more vCPUs than the host has CPUs, or more than the
  host allows in total.

  ```text
  Error: 999 vCPUs is more than the host's 4 CPUs
  ```

- **Starting** one is refused if what running instances hold, and what it
  asks for, would pass what the host allows.

  ```text
  Error: instance "db" needs 2 vCPU, 8 GiB, but 12 vCPU, 26 GiB of the 16 vCPU, 31 GiB this host allows is committed
  ```

  Stop something, or give the instance less with `dicer update`, and start
  it again. A refused start is not retried, except for an instance its
  [restart policy](../restarts) is restarting: there, it counts as another
  failed end, and the policy tries again after its backoff.

## Choosing the overcommit

**vCPUs** are threads on the host, and share its CPUs as any threads do.
Most workloads leave their vCPUs idle much of the time, which is why four to
a CPU is the default. For busy workloads, such as builds, lower it; with 1,
every vCPU has a CPU of its own.

**Memory** is taken from the host as a guest touches it, not all at once
when it boots, and a guest does not give it back. Instances that do not use
what they were given leave room, which a `memory_overcommit` above 1 lets
other instances have. But if the guests then use their memory after all,
the host runs out: its kernel kills a process to free some, often a
hypervisor, and that instance ends as a failure. Keep it at 1 unless you
know how your workloads use memory.

**The reserve** is memory kept back for everything that is not a guest: the
host's own processes, the daemon, and each hypervisor's overhead, which is
not counted as the instance's. Raise it on a host that runs other services,
or many small instances.

## Disk

Disk is reported, not enforced. Instance disks and volumes are
[sparse](../../concepts/storage): each takes up only what has been written
to it, so what they were given, shown as *provisioned*, says little about
what they will use. Nothing stops instances from filling the disk; watch it
as you would any server's, with `dicer info` or the node exporter, and see
[Managing images](../managing-images#reclaim-space-automatically) to keep
the image store in check.
