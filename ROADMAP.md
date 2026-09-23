# Roadmap

This document outlines the high-level direction for Dicer. It is not a
commitment or a schedule — priorities shift with real-world usage.

For detailed tracking, see the [GitHub Issues](https://github.com/dicer-sh/dicer/issues)
and [Milestones](https://github.com/dicer-sh/dicer/milestones).

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
| Scheduled snapshots | Snapshots taken on a schedule, with retention |
| Fork | Copy a stopped, standing-by or running instance into a new one with a fresh address, MAC and TAP device |

### Networking

| Feature | Description |
|---------|-------------|
| HTTP ingress | Host-name routing to instances (`{instance}.example.com`) with TLS and automatic certificates, built on port forwarding |
| Egress policy | Per-instance allowlists for outbound traffic; optionally a proxy that adds credentials from host files to outbound requests, so they never enter the guest |
| Instance DNS | Instances on a network reaching each other by name |
| IPv6 | Guest addressing and NAT for IPv6 |
| Routed TAP | A routed TAP device instead of a bridge, for hosts running thousands of instances |

### Devices and hypervisors

| Feature | Description |
|---------|-------------|
| GPU passthrough | VFIO and mediated GPU devices, for which the VM specification already has a shape. Cloud Hypervisor only |
| QEMU | A QEMU backend, including its minimal `microvm` machine type |

### Observability and tooling

| Feature | Description |
|---------|-------------|
| Per-guest metrics | CPU, memory, disk and network usage per instance, read from the VMM, and `dicer stats` / `dicer top` to watch it live |
| Tracing | OpenTelemetry traces across the API and lifecycle operations |

### Command line

The Docker-style commands that the CLI cannot offer until the API supports
them:

| Feature | Description |
|---------|-------------|
| Clearing lists on update | A way for `dicer update` to empty a list or map -- ports, volumes, files, env, labels. An empty one in `UpdateInstanceRequest` means "leave it unchanged" |

## Out of scope

These are deliberate exclusions, not gaps:

- **Clustering.** No scheduler, no consensus, no membership. `dicerd` owns the
  host it runs on. Placing workloads across hosts is a layer above Dicer.
- **Overlay networking.** Networks are host-local. Connecting VMs across hosts
  means connecting the *hosts* — WireGuard, a VPN, a routed fabric — and
  letting the bridges route over it.
- **Replica management.** No replica sets, no rolling updates. Dicer starts the
  instances you define.
- **A secret store.** Dicer exposes host files to guests; it does not keep
  encrypted copies of them. See the README.

## How to influence the roadmap

- Open an [issue](https://github.com/dicer-sh/dicer/issues/new) to propose a
  feature or share a use case
- Comment on existing issues to signal demand
- Submit a pull request — working code is the fastest path to inclusion

Maintainers review roadmap items periodically. Community interest and
real-world usage are the primary drivers of prioritisation.
