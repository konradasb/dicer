---
title: Getting started
weight: 1
description: "Install Dicer on a Linux host with KVM, and boot a first instance from a container image."
icon: play
related_title: Learn more
related:
  - /docs/concepts/how-dicer-works
  - /docs/concepts/instances
---

Dicer runs container images as virtual machines: each workload gets a
kernel of its own, behind a hypervisor, and is still started with one
command from an image you already have. It suits workloads that need more
isolation than a container gives, or a whole machine: untrusted code, CI
runners, services that need their own kernel.

Getting started takes two steps: install Dicer on a Linux host with KVM,
then boot a first instance from a container image.

{{< section-cards >}}
