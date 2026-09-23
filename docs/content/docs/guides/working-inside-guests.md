---
title: Working inside guests
weight: 6
description: "Run commands in a guest, copy files in and out, read its console and wait for it to end."
icon: terminal
related:
  - /docs/guides/troubleshooting
  - /docs/concepts/init-modes
---

A guest has no SSH server to set up and no network access to open: the
daemon reaches every guest through its agent, over vsock. This guide covers
running commands in a guest, copying files in and out, reading its console,
and waiting for it to end.

## Run a command

`dicer exec` runs a command in a running instance, and a shell if none is
given:

```console
$ dicer exec web
/ # nginx -t
nginx: configuration file /etc/nginx/nginx.conf test is successful
/ # exit
$ dicer exec web ls -la /usr/share/nginx/html
```

Flags go before the instance's name; everything after it is the command's.
The shell is `/bin/sh`, so an image without one needs a command given.

A command runs:

- as root, in `/`, unless `-w` gives another directory;
- with the instance's environment, and any `-e KEY=VALUE` given;
- beside the workload, not inside it: it sees every process in the guest,
  with `dicer-init` as PID 1.

```console
$ dicer exec -w /srv -e DEBUG=1 web ./check.sh
```

### Terminals, pipes and exit codes

A pseudo-terminal is used when both ends of this terminal are one, so a
shell is interactive, and not when either is piped, so output is not
mangled. `-t` and `-T` force it on or off:

```console
$ dicer exec -T web cat /var/log/nginx/error.log > error.log
$ tar -c ./site | dicer exec -T web tar -x -C /srv
```

`dicer exec` exits with the command's exit code, or, as a shell does, 127
for a command not found, 126 for one that cannot be run, and 124 for one
killed by `--timeout`:

```console
$ dicer exec --timeout 30 db pg_isready || echo "not ready: $?"
```

## Copy files

`dicer cp` copies a file or directory between this machine and a running
instance, with the instance's side written `NAME:PATH`:

```console
$ dicer cp ./site web:/usr/share/nginx/html
$ dicer cp web:/var/log/nginx/access.log .
```

It works as `cp -r` does: into the destination if that is a directory, in
its place if it is a file, and at it if nothing is there. A relative path in
the guest is taken from its root.

Modes, times and symbolic links are kept; ownership is not. What is copied
belongs to whoever receives it: root, in the guest, and you, on this
machine. Change it in the guest if the workload needs it:

```console
$ dicer cp ./uploads web:/srv/uploads
$ dicer exec web chown -R www-data: /srv/uploads
```

`dicer cp` needs the instance running. For a file the guest should have
from its start, use a [file mount](../files-and-volumes#configuration-files)
instead.

## Read the console

`dicer logs` shows the guest's serial console: the kernel's boot messages,
`dicer-init`'s, and everything the workload writes to its standard output
and error.

```console
$ dicer logs web              # all of it
$ dicer logs -n 50 web        # the last 50 lines
$ dicer logs -f web           # and keep following, until the instance stops
```

The log is kept with the instance, so it can be read after the instance
stops, to find out why. It holds the latest run: starting the instance
again starts a new log.

An image that boots systemd writes little to the console after booting:
its services log to the journal, which is read in the guest:

```console
$ dicer exec web journalctl -u nginx -n 50
```

`--source hypervisor` reads the hypervisor's own log instead, for a guest
that never got as far as booting. See [Troubleshooting](../troubleshooting).

## Wait for an instance to end

`dicer wait` waits until an instance stops, prints the exit code its
workload ended with, and exits with it:

```console
$ dicer run --name migrate ghcr.io/acme/api:3 ./migrate up
$ dicer wait migrate
0
```

It exits with 0 for a guest that powered itself off, and 125 for one that
ended without saying how, such as after a kernel panic. An instance that has
already stopped is not waited for, and one its [restart policy](../restarts)
starts again has not stopped, so the wait goes on. `--timeout` gives up
after a while.

To run a one-off job, wait for it, and clean up after it:

```console
$ dicer run --name migrate ghcr.io/acme/api:3 ./migrate up
$ dicer wait migrate && dicer rm migrate
```

This keeps a job that failed, and its log, for you to look at. `--rm` does
not combine with `wait`: a job that ends quickly is deleted before `wait`
finds it.
