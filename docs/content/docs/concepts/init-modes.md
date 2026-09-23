---
title: Init modes
weight: 8
description: "How the guest runs its command: as a container would, or under systemd."
icon: terminal
related:
  - /docs/guides/working-inside-guests
  - /docs/concepts/instances
  - /docs/guides/health-checks
---

A container runs one process. A virtual machine has an init system as its
PID 1, and an image built for containers usually has none. Dicer runs an
instance's command in one of two ways.

## exec

The command runs as a container runs it: as PID 1 of a PID namespace of its
own, under `dicer-init`, which is the machine's PID 1. `dicer-init` supervises
it, and when it exits, reports its exit code and ends the machine. The
instance ends when its command does.

This is how an application image, such as `nginx` or `postgres`, runs.

## systemd

The command is systemd, and it becomes the machine's PID 1, as it would on
any Linux machine. Dicer adds two units to it: `dicer-agent.service`, for
`dicer exec`, `dicer cp` and health checks, and one that reports the end to
the host when the machine powers off. The instance ends when systemd powers
the machine off.

This is how an image of a whole operating system, booting `/sbin/init`, runs.

## auto

By default, the mode is `auto`: `systemd` if the command is the systemd
binary, and `exec` otherwise. The command is the instance's own, or the
image's `ENTRYPOINT` and `CMD`, or, if there is none, `/sbin/init`. It is
looked up as the guest would, on its `PATH` and through its symlinks, so a
Debian image's `/sbin/init` counts as systemd.

`--init-mode exec` or `--init-mode systemd` decides for an image where the
guess is wrong.
