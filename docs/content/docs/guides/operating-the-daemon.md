---
title: Operating the daemon
weight: 12
description: "Restart and upgrade dicerd without stopping guests, back it up, and remove it."
icon: cog
related:
  - /docs/guides/troubleshooting
  - /docs/reference/configuration
  - /docs/reference/files-and-environment
---

`dicerd` runs as a systemd service, `dicerd.service`, which the `dicer`
package or `install.sh` sets up. This guide covers running it day to day:
changing its configuration, restarting and upgrading it without disturbing
guests, backing it up, and removing it.

## The service

```console
$ sudo systemctl status dicerd
$ journalctl -u dicerd -f
```

The service starts at boot, and systemd restarts it if it fails.

## Restarting is safe

Each guest runs in a hypervisor process of its own, which outlives the
daemon. Stopping or restarting `dicerd` leaves every guest running, and the
next daemon to start adopts them as they are:

| State when the daemon stopped | What the next daemon does |
|---|---|
| Running or paused, and still is | Adopts it as it is. |
| Running or paused, but it ended while the daemon was down | Handles it like any instance that ends: its [restart policy](../restarts) applies. |
| Starting or stopping | Ends its hypervisor and marks it Failed, since it cannot finish what the old daemon was doing. |
| Restarting | Restarts it when the rest of its wait is over. |
| Standby | Leaves it on standby. |

While the daemon is down, guests keep running and their published ports
keep working, but nothing manages them. There are no restarts, no health
checks, no automatic standby and no API. An instance on standby cannot be
woken by a connection either, because the daemon is what listens on its
ports.

When the daemon is stopped, it waits up to 10 seconds for calls in flight,
such as `dicer logs -f` and `dicer exec` sessions, and then ends them.

## Changing the configuration

The daemon reads its configuration, `/etc/dicerd/config.yaml`, when it
starts. Every setting is described in the
[configuration reference](../../reference/configuration). Change the file,
check it, then restart the daemon:

```console
$ sudoedit /etc/dicerd/config.yaml
$ sudo dicerd validate
/etc/dicerd/config.yaml is valid.
$ sudo systemctl restart dicerd
```

`dicerd validate` checks the file as the daemon does when it starts. It
checks that every key and value is valid, that the TLS files and registry
credentials it names can be read, and that this host can give instances the
resources it allows. A problem is reported with its line number:

```console
$ sudo dicerd validate
Error: parse /etc/dicerd/config.yaml: yaml: unmarshal errors:
  line 12: field log_lvl not found in type daemon.Config
```

The daemon does not start with a configuration it cannot use, and its log
says why. Guests keep running in the meantime. Fix the file and start the
daemon again.

The API's TLS certificate and key are the one exception to the restart
rule. The daemon reloads them when the files change, so renewing them needs
no restart. A new `client_ca_file` does need one.

If you move `data_dir`, the packaged service cannot write to the new
directory until you allow it in a drop-in:

```ini {filename="/etc/systemd/system/dicerd.service.d/data-dir.conf"}
[Service]
ReadWritePaths=/srv/dicer
```

## Upgrading

Upgrade Dicer the same way you installed it:

{{< tabs >}}
  {{< tab name="Debian, Ubuntu" >}}
  ```console
  $ sudo apt update
  $ sudo apt install dicer
  ```

  If you changed the configuration, dpkg asks which version to keep.
  {{< /tab >}}
  {{< tab name="Fedora, RHEL, Rocky, AlmaLinux" >}}
  ```console
  $ sudo dnf upgrade dicer
  ```

  If you changed the configuration, rpm keeps yours and puts the new one
  beside it, as `config.yaml.rpmnew`.
  {{< /tab >}}
  {{< tab name="openSUSE" >}}
  ```console
  $ sudo zypper update dicer
  ```

  If you changed the configuration, rpm keeps yours and puts the new one
  beside it, as `config.yaml.rpmnew`.
  {{< /tab >}}
  {{< tab name="Ansible" >}}
  Set `dicerd_version` to the version you want in the playbook that runs
  the `konradasb.general.dicerd` role, and run it again:

  ```console
  $ ansible-playbook -i inventory dicer.yml
  ```

  The role keeps managing the configuration. To upgrade to each release as
  it comes out, set `dicerd_package_state: latest` instead of a version.
  {{< /tab >}}
  {{< tab name="From source" >}}
  Run `install.sh` again with the version you want:

  ```console
  $ curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash -s -- --ref v0.3.0
  ```

  It builds that version, stops the daemon, replaces `dicer` and `dicerd`,
  and starts the new daemon. The configuration is kept.
  {{< /tab >}}
{{< /tabs >}}

The new daemon adopts the running guests. An upgrade replaces the service
file, so put any changes of your own to it in a drop-in under
`/etc/systemd/system/dicerd.service.d/`.

Running guests keep the hypervisor, `dicer-init` and agent they booted
with. An instance gets the new ones the next time it boots. `dicer version`
shows the version of both the client and the daemon.

A release can drop an old hypervisor version. Instances on standby and
memory snapshots that still use it can then no longer be resumed. Read
[Before a version is removed](../../concepts/hypervisors#before-a-version-is-removed)
before you upgrade.

{{< callout type="info" >}}
  Starting the daemon also starts every stopped instance whose restart
  policy is `always`, and every one with `unless-stopped` that you did not
  stop yourself. An upgrade can therefore start instances.
{{< /callout >}}

## When the host reboots

A reboot ends every running guest. When the daemon starts again, those
instances are stopped, and the ones whose restart policy is `always` or
`unless-stopped` are started. Give that policy to instances that should
come back with the host.

An instance on standby is frozen to disk, so it is still on standby after
the reboot. Its restart policy does not start it, and `dicer start` resumes
it where it was.

To shut the host down cleanly, stop the instances first, so their workloads
can shut down as they expect:

```console
$ dicer stop $(dicer ps -q --filter state=running)
```

## Backing up

Dicer keeps everything in two places: its configuration in `/etc/dicerd`,
and its state in `/var/lib/dicer`. See
[Files and environment](../../reference/files-and-environment) for what is
where.

The state directory holds overlay disks, volumes and snapshots, which are
sparse files. Copy them in a way that keeps them sparse, or the copy takes
their full size. A disk copied while its guest runs is only as consistent
as one after a power cut. For a copy you can rely on, stop the instances
first:

```console
$ dicer stop $(dicer ps -q --filter state=running)
$ sudo systemctl stop dicerd
$ sudo tar --sparse -czf dicer-backup.tar.gz /etc/dicerd /var/lib/dicer
$ sudo systemctl start dicerd
```

You can leave out the images and the layer cache, `/var/lib/dicer/images`
and `/var/lib/dicer/oci-cache`, because the daemon pulls again what it
needs. It pulls an instance's image by name, though, so if the tag has
moved since, the instance boots the newer image.

To restore, stop the daemon, put both directories back where they were, and
start it. Instances come back stopped, apart from those their restart
policy starts.

## Uninstalling

Delete the instances first. Removing Dicer stops the daemon, and any guest
still running then keeps running, unmanaged, with its bridge and firewall
rules left behind:

```console
$ dicer rm -f $(dicer ps -q)
```

Then remove Dicer the same way you installed it:

{{< tabs >}}
  {{< tab name="Debian, Ubuntu" >}}
  ```console
  $ sudo apt purge dicer
  ```

  This removes the binaries, the service and `/etc/dicerd`, including any
  TLS certificates kept there. `apt remove` keeps `/etc/dicerd`.
  {{< /tab >}}
  {{< tab name="Fedora, RHEL, Rocky, AlmaLinux" >}}
  ```console
  $ sudo dnf remove dicer
  ```

  This removes the binaries and the service. If you changed the
  configuration, it is kept as `/etc/dicerd/config.yaml.rpmsave`, with any
  TLS certificates beside it.
  {{< /tab >}}
  {{< tab name="openSUSE" >}}
  ```console
  $ sudo zypper remove dicer
  ```

  This removes the binaries and the service. If you changed the
  configuration, it is kept as `/etc/dicerd/config.yaml.rpmsave`, with any
  TLS certificates beside it.
  {{< /tab >}}
  {{< tab name="From source" >}}
  ```console
  $ curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/uninstall.sh | bash
  ```

  This removes the binaries, the service, the `dicer` group and
  `/etc/dicerd`, including any TLS certificates kept there. Add
  `-s -- --purge` after `bash` to remove `/var/lib/dicer` as well.
  {{< /tab >}}
{{< /tabs >}}

Otherwise `/var/lib/dicer` is kept, with every image, overlay disk, volume and
snapshot in it. To remove it too, which cannot be undone:

```console
$ sudo rm -rf /var/lib/dicer
```

Network bridges remain until the host reboots. Each is named `dicer-NAME`
after its network, or `dbr-` and a hash when the name is too long. To
remove one sooner, run `sudo ip link delete dicer-NAME`.
