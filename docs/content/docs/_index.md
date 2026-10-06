---
title: Introduction
weight: 1
description: "Run virtual machines from container images, on one host."
---

Dicer runs virtual machines from container images, on one host.

It pulls an OCI image, converts it to a read-only root filesystem, and boots
it as a virtual machine under Cloud Hypervisor or Firecracker, with the
instance's own overlay disk laid over it. The daemon, `dicerd`, owns
the host it runs on. The `dicer` command line, or any other client of its
gRPC API, tells it what to run.

{{< section-cards >}}
