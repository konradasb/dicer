---
title: Working inside guests
weight: 7
description: "Run commands in a guest, list its processes, copy files in and out, and read its console."
icon: terminal
related:
  - /docs/guides/troubleshooting
  - /docs/concepts/init-modes
---

There is no SSH server to set up in a guest, and no network access to open.
The daemon reaches every guest through the guest agent, `dicer-agent`, over
vsock. This guide covers running commands in a guest, listing its processes,
copying files in and out, and reading its console.

`dicer exec`, `dicer top` and `dicer cp` need the instance to be running.
Resume a paused instance with `dicer resume`, and one on standby with
`dicer start`.

## Run a command

`dicer exec` runs a command in a running instance. Without a command, it
runs a shell:

```console
$ dicer exec web
/ # nginx -t
nginx: configuration file /etc/nginx/nginx.conf test is successful
/ # exit
$ dicer exec web ls -la /usr/share/nginx/html
```

Flags go before the instance's name. Everything after the name belongs to
the command. The shell is `/bin/sh`, so for an image without one, give a
command.

A command runs:

- as root, in `/`, unless `-w` gives another directory;
- with the instance's environment, plus any `-e KEY=VALUE` you give;
- beside the workload, not inside it.

```console
$ dicer exec -w /srv -e DEBUG=1 web ./check.sh
```

Running beside the workload means a command sees every process in the guest.
In the [exec init mode](../../concepts/init-modes#exec), `dicer-init` is PID
1, and the workload runs in a PID namespace of its own, where it sees itself
as PID 1. The same process therefore has a different PID in each view.

### Terminals, pipes and exit codes

`dicer exec` uses a pseudo-terminal when both its input and output are a
terminal, so that a shell is interactive. When either is piped, it does not,
so that output is not mangled. `-t` and `-T` force it on or off:

```console
$ dicer exec -T web cat /var/log/nginx/error.log > error.log
$ tar -c ./site | dicer exec -T web tar -x -C /srv
```

`dicer exec` exits with the command's exit code. As in a shell, it is 127
for a command that is not found and 126 for one that cannot be run. It is
124 for a command killed by `--timeout`, which is given in seconds:

```console
$ dicer exec --timeout 30 db pg_isready || echo "not ready: $?"
```

## List its processes

`dicer top` lists the processes running in a running instance. The guest
agent reads them from the guest's `/proc`, so it works for any image, with
no `ps` needed in it:

```console
$ dicer top web
PID  PPID  USER   STATE  STARTED         CPUTIME  RSS       COMMAND
1    0     root   S      12 minutes ago  210ms    9.8 MiB   /init
214  1     root   S      12 minutes ago  400ms    14.2 MiB  /usr/local/bin/dicer-agent
215  1     root   S      12 minutes ago  20ms     3.1 MiB   nginx: master process nginx -g daemon off;
216  215   nginx  S      12 minutes ago  1.87s    22.5 MiB  nginx: worker process
```

Every process in the guest is listed except the kernel's own threads.
`dicer-init` and the guest agent are listed too, because they run in the
guest. The PIDs are the ones `dicer exec` sees, so you can signal a process
found here:

```console
$ dicer exec web kill -HUP 215
```

| Column | Meaning |
|---|---|
| `STATE` | The kernel's code for the process: R running, S sleeping, D waiting on I/O, Z zombie, T stopped. |
| `CPUTIME` | The CPU time it has used since it started. |
| `RSS` | The guest memory it has resident. |

The column names are also the fields of a `--format` template, such as
`{{.PID}}` and `{{.CPUTime}}`, and the keys of `--format json`. For what the
instance as a whole uses of the host, see
[Instance stats](../monitoring#instance-stats).

## Copy files

`dicer cp` copies a file or directory between this machine and a running
instance. The instance's side is written `NAME:PATH`:

```console
$ dicer cp ./site web:/usr/share/nginx/html
$ dicer cp web:/var/log/nginx/access.log .
```

It works as `cp -r` does. If the destination is a directory, the copy goes
into it. If it is a file, the copy replaces it. If nothing is there, the
copy is made at that path. A relative path in the guest is taken from the
guest's root.

Modes, times and symbolic links are kept, but ownership is not. What is
copied belongs to whoever receives it: root in the guest, and you on this
machine. If the workload needs other ownership, change it in the guest:

```console
$ dicer cp ./uploads web:/srv/uploads
$ dicer exec web chown -R www-data: /srv/uploads
```

For a file the guest should have from the moment it starts, use a
[file mount](../files-and-volumes#configuration-files) instead.

## Read the console

`dicer logs` shows the guest's serial console. That holds the kernel's boot
messages, `dicer-init`'s messages, and everything the workload writes to its
standard output and error.

```console
$ dicer logs web              # all of it
$ dicer logs -n 50 web        # the last 50 lines
$ dicer logs -f web           # and keep following, until the instance stops
```

The log is kept with the instance, so you can read it after the instance
stops to find out why it did. It holds the latest run only: starting the
instance again starts a new log.

An image that boots systemd writes little to the console once it has booted.
Its services log to the journal, which you read in the guest:

```console
$ dicer exec web journalctl -u nginx -n 50
```

`--source hypervisor` reads the hypervisor's own log instead. It explains a
guest that never got as far as booting, and is discarded when the instance
stops. See [Troubleshooting](../troubleshooting).
