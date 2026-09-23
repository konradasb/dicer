---
title: Introduction
weight: 1
description: "Run virtual machines from container images, on one host."
---

Dicer runs virtual machines from container images, on one host.

It pulls an OCI image, converts it to a read-only root filesystem and boots it
as a VM under Cloud Hypervisor or Firecracker, with a writable overlay on top.
The daemon, `dicerd`, owns the host it runs on; the `dicer` command line, or
any client of its gRPC API, tells it what to run.

{{< section-cards >}}
