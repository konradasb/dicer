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

A container runs one process. A virtual machine runs an init system as its
PID 1, and an image built for containers usually has none. Dicer runs an
instance's command in one of two init modes, `exec` and `systemd`, and by
default picks one for it.

| Mode | PID 1 | The instance ends when | Suits |
|---|---|---|---|
| `exec` | `dicer-init`, which runs the command | The command exits | An application image, such as `nginx` or `postgres` |
| `systemd` | systemd | systemd powers the machine off | An image of a whole operating system, booting `/sbin/init` |

## exec

The command runs as a container runs it. `dicer-init` stays the machine's
PID 1, and starts the command as PID 1 of a PID namespace of its own, with
its own mount namespace and `/proc`. `ps` in it shows its processes and no
others, and tools that look processes up in `/proc`, such as Docker's, find
them.

`dicer-init` supervises the command. When the command exits, `dicer-init`
reports its exit code to the host and ends the machine, so the instance ends
with it.

If the command cannot be started, the instance ends at once, with the exit
code a shell would give: 127 if the image does not have the command, or 126
if it cannot be run.

## systemd

The command is systemd, and it becomes the machine's PID 1, as on any Linux
machine. Dicer adds two units to it:

- `dicer-agent.service` runs the guest agent, for `dicer exec`, `dicer cp`
  and health checks.
- `dicer-exit.service` tells the host that the guest ended cleanly, as
  systemd powers the machine off.

The instance ends when systemd powers the machine off. A reboot or a halt
inside the guest is not a clean end, so it leaves the instance Failed,
unless its restart policy starts it again. See
[How an instance ends](../instances#how-an-instance-ends).

## auto

The default mode is `auto`. It picks `systemd` if the command is the systemd
binary, and `exec` otherwise.

The command is the instance's own, if it has one, and otherwise the image's
`ENTRYPOINT` and `CMD`. With neither, it is `/sbin/init`. The command is
looked up as the guest would run it: on the guest's default `PATH`, and
through any symlinks. So a Debian image's `/sbin/init`, a link to
`/lib/systemd/systemd`, counts as systemd.

If `auto` guesses wrong for an image, give the mode with `--init-mode exec`
or `--init-mode systemd`.
