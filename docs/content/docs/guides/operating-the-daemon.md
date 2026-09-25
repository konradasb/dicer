---
title: Operating the daemon
weight: 11
description: "Restart and upgrade dicerd without stopping guests, back it up, and remove it."
icon: cog
related:
  - /docs/guides/troubleshooting
  - /docs/reference/configuration
  - /docs/reference/files-and-environment
---

`dicerd` runs as a systemd service, `dicerd.service`, as `install.sh` sets
it up. This guide covers running it day to day: changing its
configuration, restarting and upgrading it without disturbing guests,
backing it up, and removing it.

## The service

```console
$ sudo systemctl status dicerd
$ journalctl -u dicerd -f
```

The service starts at boot, and systemd restarts it if it fails.

## Restarting is safe

Each guest runs in a hypervisor process of its own, which outlives the
daemon. Stopping or restarting `dicerd` leaves every guest running, and the
daemon that starts next takes them over, as they were:

| An instance that was | When the daemon starts again, is |
|---|---|
| running or paused, and still is | taken over, as it is |
| running or paused, but ended while the daemon was down | handled as any instance that ends: its [restart policy](../restarts) applies |
| starting or stopping | stopped where it was, and marked Failed: the daemon cannot finish what it was doing |
| waiting to restart | restarted, after the rest of its wait |

While the daemon is down, guests keep running, but nothing manages them:
no restarts, no health checks, no API. Published ports and networking keep
working.

A daemon that is stopped waits up to 10 seconds for calls in flight, such
as `dicer logs -f` and `dicer exec` sessions, then ends them.

## Changing the configuration

The configuration, `/etc/dicerd/config.yaml`, is read when the daemon
starts; every setting is in the [configuration reference](../../reference/configuration).
Change it, check it, then restart the daemon:

```console
$ sudoedit /etc/dicerd/config.yaml
$ sudo dicerd validate
/etc/dicerd/config.yaml is valid.
$ sudo systemctl restart dicerd
```

`dicerd validate` checks the file as the daemon does when it starts: that
every key is one it knows, every value one it takes, the TLS files it names
readable, and the resources it allows possible on this host. A problem is
named with its line:

```console
$ sudo dicerd validate
Error: parse /etc/dicerd/config.yaml: yaml: unmarshal errors:
  line 12: field log_lvl not found in type daemon.Config
```

The one exception is the API's TLS certificate and key, which the daemon
reloads by itself when the files change, so renewing them needs no restart.

A configuration the daemon cannot use stops it from starting, and it says
why in its log. Guests keep running meanwhile; fix the file and start it
again.

## Upgrading

Run `install.sh` again, for the version you want:

```console
$ curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash -s -- --ref v0.2.0
```

It builds that version, stops the daemon, replaces `dicer` and `dicerd`,
and starts the new daemon, which takes the running guests over. The
configuration is kept. The service file is rewritten, so put changes of
your own to it in a drop-in, under `/etc/systemd/system/dicerd.service.d/`.

Guests go on running what they booted with: the new hypervisors, `dicer-init`
and agent reach an instance at its next start. `dicer version` shows the
client's version and the daemon's.

{{< callout type="info" >}}
  Starting the daemon also starts every stopped instance whose restart
  policy is `always`, and every one with `unless-stopped` that you did not
  stop yourself. An upgrade can therefore start instances.
{{< /callout >}}

## When the host reboots

A reboot ends every guest. When the daemon starts again, all instances are
stopped, and those whose restart policy is `always` or `unless-stopped` are
started. Give instances that should come back with the host one of those
policies.

To shut the host down cleanly, stop the instances first, so their workloads
shut down as they expect:

```console
$ dicer stop $(dicer ps -q --filter state=running)
```

## Backing up

Everything Dicer keeps is in two places: the configuration, in
`/etc/dicerd`, and its state, in `/var/lib/dicer`. See
[Files and environment](../../reference/files-and-environment) for what is
where.

The state directory holds instance disks and volumes, which are sparse
files: copy them in a way that keeps them sparse, or the copy takes their
full size. A disk copied while its guest runs is as consistent as one
after a power cut; for a copy you can rely on, stop the instances first:

```console
$ dicer stop $(dicer ps -q --filter state=running)
$ sudo systemctl stop dicerd
$ sudo tar --sparse -czf dicer-backup.tar.gz /etc/dicerd /var/lib/dicer
$ sudo systemctl start dicerd
```

Images and the layer cache under `/var/lib/dicer/images` and `oci-cache`
can be left out: the daemon pulls again what it needs.

To restore, stop the daemon, put both directories back where they were, and
start it. Instances come back stopped, apart from those their restart
policy starts.

## Uninstalling

Delete the instances first. `uninstall.sh` stops the daemon, and a guest
running then goes on running, unmanaged, with its bridge and firewall
rules left behind:

```console
$ dicer rm -f $(dicer ps -q)
$ curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/uninstall.sh | bash
```

This removes the binaries, the service and `/etc/dicerd`, TLS certificates
kept there included, and keeps `/var/lib/dicer`. `--purge` removes that too,
with every image, disk and volume in it; it cannot be undone:

```console
$ curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/uninstall.sh | bash -s -- --purge
```

Networks' bridges remain until the host reboots, or until removed with
`sudo ip link delete dicer-NAME`.
