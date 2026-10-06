---
title: Capacity
weight: 10
description: "How much CPU and memory instances may be given, overcommit and the reserve, what is in use, and resizing a running instance."
icon: scale
related:
  - /docs/guides/monitoring
  - /docs/reference/configuration
---
Each instance asks for vCPUs and memory, which are committed to it while it
runs. The daemon keeps count, and refuses a start the host has no room for,
rather than letting instances crowd each other out. This guide covers how
much room there is, how to change it, how to see what is in use, and how to
resize a running instance.

## What the host allows

When it starts, the daemon reads the host's logical CPUs and memory, and
works out how much instances may be given in total:

| | Instances may be given |
|---|---|
| vCPUs | the host's CPUs × `cpu_overcommit` |
| Memory | (the host's memory − `reserved_memory_bytes`) × `memory_overcommit` |

The defaults allow four vCPUs for each CPU, and all of the memory but 1 GiB,
with no memory overcommit:

```yaml {filename="/etc/dicerd/config.yaml"}
resources:
  cpu_overcommit: 4
  memory_overcommit: 1
  reserved_memory_bytes: 1073741824   # 1 GiB
```

On a host with 16 CPUs and 64 GiB, that is 64 vCPUs and 63 GiB. The
[configuration reference](../../reference/configuration#resources) describes
the settings. Restart the daemon after changing them. Running guests are
not affected.

## What is in use

`dicer info` shows how much is committed to instances, out of how much the
host allows:

```console
$ dicer info
…
           vCPU: █████░░░░░░░░░░░░░░░  4 of 16         25%
                 4 CPUs, 4× overcommit
         Memory: ██████████░░░░░░░░░░  15.5 of 31 GiB  50%
                 32 GiB, 1 GiB reserved
           Disk: ██░░░░░░░░░░░░░░░░░░  25 of 250 GiB   10%
                 40 GiB provisioned
```

`dicer ps --wide` shows what each instance was given, and
`dicer info --format json` gives the same totals for scripts, with the
instances they are committed to. They are also
[metrics](../monitoring#metrics): `dicer_instances_vcpus` and
`dicer_instances_memory_bytes`, against
`dicer_instances_vcpus_allocatable` and
`dicer_instances_memory_allocatable_bytes`.

## When resources are committed

An instance's vCPUs and memory are committed to it while it is **starting,
running or paused**, since a paused guest is still in memory. Nothing is
committed to an instance while it is stopped, failed, on
[standby](../standby), or waiting to be [restarted](../restarts).

So resources are checked when an instance starts, not when it is created:

- **Creating** an instance, or updating it, is refused only if it could
  never start on the host. That is, if it asks for more vCPUs than the host
  has CPUs, or more than the host allows in total.

  ```text
  Error: 999 vCPUs is more than the host's 4 CPUs
  ```

- **Starting** an instance is refused if what it asks for, added to what is
  already committed to other instances, is more than the host allows.
  Resuming an instance from standby or a memory snapshot is checked in the
  same way.

  ```text
  Error: instance "db" needs 2 vCPU, 8 GiB, but 12 vCPU, 26 GiB of the 16 vCPU, 31 GiB this host allows is committed
  ```

  Stop something, or give the instance less with `dicer update`, and start
  it again. A refused start is not retried, except for an instance that its
  [restart policy](../restarts) is restarting. For that instance, the
  refusal counts as another failure, and the policy tries again after its
  backoff.

- **Resizing** a running instance with `dicer resize` is refused in the
  same way if growing it would take more than the host allows. Only what
  the instance has now is committed to it, not its `--max-vcpus` and
  `--max-memory`. Creating an instance with maximums the host could never
  give is refused, as it is for its sizes. See
  [Resizing a running instance](#resizing-a-running-instance).

## Resizing a running instance

`dicer resize` changes a running instance's vCPUs and memory without
restarting it. It also updates the instance's definition, so the instance
keeps the new size when it next starts.

An instance can only grow into room set aside for it when it starts. That
room is `--max-vcpus` and `--max-memory`, given when the instance is created
or, while it is stopped, with `dicer update`.

```console
$ dicer run -d --name db --memory 1GiB --max-memory 8GiB --vcpus 2 --max-vcpus 8 postgres:17
$ dicer resize db --memory 4GiB --vcpus 4
```

The room set aside costs the host nothing until it is used, because only
what the instance has now is committed to it. Growing is refused when the
host has no room, just as a start is.

- **Memory** can be resized in steps of 2 MiB, from what the instance started
  with up to its maximum. Shrinking asks the guest to give memory back, and
  the guest can refuse if it is using it. On Firecracker, `dicer resize`
  waits for the guest to take the change. Cloud Hypervisor cannot tell when
  the guest has taken it.
- **vCPUs** can be resized on Cloud Hypervisor only, from 1 up to the
  maximum. Firecracker cannot add vCPUs to a running guest. The guest agent
  brings each added vCPU online, as udev would on a distribution.

The guest's kernel must support memory hotplug through virtio-mem
(`CONFIG_VIRTIO_MEM`) and CPU hotplug (`CONFIG_HOTPLUG_CPU`). Dicer's kernel
supports both. If the guest does not take a change, the larger of the old
and new sizes stays committed to the instance until its next start, when it
gets the new one. Each resize is recorded as a
[Resized event](../../reference/events#instances).

{{< callout type="info" >}}
  Firecracker places the extra memory at 512 GiB in the guest's address
  space. A host CPU with fewer than 40 bits of physical address cannot reach
  that far, so on such a host, Firecracker guests cannot be given more
  memory. Run `grep 'address sizes' /proc/cpuinfo` on the host to see how
  many bits it has.
{{< /callout >}}

## Choosing the overcommit

**vCPUs** are threads on the host, and share its CPUs as any threads do.
Most workloads leave their vCPUs idle much of the time, which is why the
default is four to a CPU. For busy workloads, such as builds, lower it.
With 1, every vCPU has a CPU of its own.

**Memory** is taken from the host as a guest touches it, not all at once
when it boots, and a guest does not give it back. Instances that don't use
all they were given leave memory spare, and a `memory_overcommit` above 1
lets other instances have it. But if the guests later use their memory
after all, the host runs out. Its kernel then kills a process to free some,
often a hypervisor, and that instance ends as a failure. Keep
`memory_overcommit` at 1 unless you know how your workloads use memory.

**The reserve** is memory kept back for everything that is not a guest: the
host's own processes, the daemon, and each hypervisor's overhead, which is
not counted as part of the instance. Raise it on a host that runs other
services, or many small instances.

## Disk

Disk is reported, not enforced. Overlay disks and volumes are
[sparse](../../concepts/storage): each takes up only what has been written
to it. So the size they were given, shown as *provisioned*, says little
about how much they will use. Nothing stops instances from filling the
disk. Watch it as you would on any server, with `dicer info` or the node
exporter. To keep the image store in check, see
[Managing images](../managing-images#reclaim-space-automatically).
