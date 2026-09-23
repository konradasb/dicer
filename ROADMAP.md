# Roadmap

This document outlines the high-level direction for Dicer. It is not a
commitment or a schedule — priorities shift with real-world usage.

For detailed tracking, see the [GitHub Issues](https://github.com/konradasb/dicer/issues)
and [Milestones](https://github.com/konradasb/dicer/milestones).

---

## Current focus

Dicer manages virtual machines on a single host. The near-term work is making
that one job solid rather than widening it.

## Planned

Roughly in the order they are expected to land. Several are already half-built
underneath: the hypervisor layer supports them, and the API does not yet
expose them.

### Running workloads

| Feature | Description |
|---------|-------------|
| Disk and network rate limits | Per-instance disk I/O and bandwidth limits. Both hypervisors support disk limits and the host network layer supports shaping; neither is exposed |
| Live resize | Change a running instance's memory, and its vCPUs on Cloud Hypervisor. Both hypervisors implement it; there is no RPC |

### Snapshots and cloning

| Feature | Description |
|---------|-------------|
| Standby | Snapshot and stop in one operation, freeing a VM's memory while keeping it ready to resume; optionally automatic for idle instances |
| Lazy restore | Restore a snapshot with guest memory paged in on demand (userfaultfd), so resuming takes milliseconds rather than the time to read all of memory |
| Fork | Copy a stopped, standing-by or running instance into a new one with a fresh address, MAC and TAP device |

### Devices and hypervisors

| Feature | Description |
|---------|-------------|
| GPU passthrough | VFIO and mediated GPU devices, for which the VM specification already has a shape. Cloud Hypervisor only |

### Observability and tooling

| Feature | Description |
|---------|-------------|
| Per-guest metrics | CPU, memory, disk and network usage per instance, read from the VMM, and `dicer stats` / `dicer top` to watch it live |
| Tracing | OpenTelemetry traces across the API and lifecycle operations |

## How to influence the roadmap

- Open an [issue](https://github.com/konradasb/dicer/issues/new) to propose a
  feature or share a use case
- Comment on existing issues to signal demand
- Submit a pull request — working code is the fastest path to inclusion

Maintainers review roadmap items periodically. Community interest and
real-world usage are the primary drivers of prioritisation.
