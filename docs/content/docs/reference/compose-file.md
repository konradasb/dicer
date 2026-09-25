---
title: Compose file
weight: 4
description: "Every key of the file dicer compose reads, with its default."
icon: template
related:
  - /docs/guides/compose
  - /docs/reference/cli/dicer_compose
---

`dicer compose` reads a project from a YAML file: its services, and the
networks and volumes they use. A key it does not support is refused, with
the reason, rather than ignored. `dicer compose config` checks a file
and shows it as it is read.

## The file

`dicer compose` looks for `dicer-compose.yaml`, `dicer-compose.yml`,
`compose.yaml` and `compose.yml`, in that order, in the current directory
and then in each directory above it. `-f` or `$DICER_COMPOSE_FILE` names
another.

| Key | |
|---|---|
| `name` | The project's name. Overridden by `-p` or `$DICER_COMPOSE_PROJECT_NAME`; the file's directory's name, lower case, if none gives one. |
| `services` | The project's services, by name. Required. |
| `networks` | Networks the services join, by name. |
| `volumes` | Volumes the services mount, by name. |
| `version` | Accepted and ignored. |
| `x-*` | Extensions: ignored, and where YAML anchors are defined to share settings between services. |

## Names

A project's instances are named `PROJECT-SERVICE`, and its networks and
volumes `PROJECT-NAME`, so each belongs visibly to its project and two
projects on a host do not collide. Names are letters, digits and hyphens:
`db_primary` cannot be a service's name.

Each instance is labelled `dicer.compose.project` and
`dicer.compose.service`, which is how `dicer compose` finds its instances
again, and `dicer.compose.config-hash`, a digest of its definition, which
is how `up` tells a changed service from one it can leave running. Labels
starting `dicer.compose.` are reserved for `dicer compose`.

## Services

| Key | |
|---|---|
| `image` | The image to boot. Required. |
| `container_name` | The instance's name, instead of `PROJECT-SERVICE`. Changing it replaces the instance at the next `up`. |
| `hostname` | The guest's hostname. Defaults to the instance's name. |
| `command` | The command to run, as a list, or a string split as a shell would split it. **Replaces the image's ENTRYPOINT and CMD**, as `dicer run` does. |
| `entrypoint` | Put before `command`. Together they replace the image's ENTRYPOINT and CMD. |
| `environment` | Variables, as a map or a list of `KEY=VALUE`. A `KEY` with no value takes the environment's, and is left out if it has none. |
| `env_file` | Files of `KEY=VALUE` lines, read in order, relative to the project directory. `environment` wins over them. |
| `labels` | Labels, as a map or a list of `KEY=VALUE`. |
| `ports` | Ports to publish. See [Ports](#ports). |
| `volumes` | Volumes, host files and tmpfs to mount. See [Mounts](#mounts). |
| `tmpfs` | Paths to mount an empty tmpfs at, as a string or a list. A tmpfs takes no options. |
| `networks` | The network to join, as a list of one name or a map. See [Networks](#networks). |
| `depends_on` | Services to start first. See [Dependencies](#dependencies). |
| `restart` | `no`, `always`, `unless-stopped`, `on-failure` or `on-failure:N`. See [Restarts](../../guides/restarts). |
| `healthcheck` | How to check the workload's health. See [Health checks](#health-checks). |
| `vcpus` | The machine's vCPUs. Default 1. `cpus` is taken too, if it is a whole number. |
| `memory` | The machine's memory, such as `512MiB` or `2g`. Default 512 MiB. `mem_limit` is taken too. |
| `disk` | The size of the instance's disk. Default 10 GiB. |
| `kernel` | The kernel to boot. Defaults to the daemon's. |
| `kernel_args` | Kernel command line arguments. |
| `hypervisor` | `cloud-hypervisor`, the default, or `firecracker`. |
| `hypervisor_version` | A version of it the daemon ships. Defaults to the newest. |
| `init_mode` | `auto`, the default, `exec` or `systemd`. See [Init modes](../../concepts/init-modes). |

`vcpus`, `memory`, `disk`, `kernel`, `kernel_args`, `hypervisor`,
`hypervisor_version` and `init_mode` are Dicer's own, and have the meaning
of `dicer run`'s flags of the same names.

### Ports

The short form is `[HOST_IP:]HOST_PORT:GUEST_PORT[/tcp|udp]`, as
`dicer run -p` takes it. An IPv6 host address goes in brackets:
`[::1]:8080:80`. The long form:

```yaml
ports:
  - target: 80          # the guest's port
    published: "8080"   # the host's
    host_ip: 127.0.0.1  # optional
    protocol: tcp       # optional; tcp or udp
```

A port must be published on a port of the host: `"80"` alone, with no host
port, is refused, as are ranges.

### Mounts

The short form is `SOURCE:TARGET[:ro]`:

- A `SOURCE` that is a name is a volume, which must be declared under the
  file's `volumes`.
- A `SOURCE` starting `/`, `.` or `~` is a host file, relative to the
  project directory. **It must be a file, not a directory**: it is copied
  into the guest each time the instance starts, as with `dicer run --mount
  type=file`. With a remote daemon, the path is on the daemon's host.

A `TARGET` alone, an anonymous volume, is refused: name the volume. The
long form:

```yaml
volumes:
  - type: volume        # volume, bind (a host file) or tmpfs
    source: pgdata
    target: /var/lib/postgresql/data
    read_only: false
```

See [Files and volumes](../../guides/files-and-volumes).

### Networks

An instance joins one network. A service names it with a list of one name,
or a map, which can give it a fixed address:

```yaml
networks:
  backend:
    ipv4_address: 172.30.0.10
```

A service that names none joins the file's network called `default`, if it
declares one, and otherwise the daemon's default network, as `dicer run`
does. Services have no DNS names: another service reaches one by its
address.

### Dependencies

`depends_on` is a list of services, or a map of them to the condition to
wait for:

| Condition | Waits until the service |
|---|---|
| `service_started` | Is running. The default. |
| `service_healthy` | Is healthy. It must have a health check, of its own or its image's. |
| `service_completed_successfully` | Has ended, with exit code 0. |

A service is not started if one it depends on fails to come up, is
unhealthy or ends with another code. Services that depend on each other
in a cycle are refused.

### Health checks

A check is a command, `test`, or one of the probes the guest's agent runs
itself, `http` and `tcp`, which suit images with no shell. Give exactly one
of them:

| Key | |
|---|---|
| `test` | `["CMD", ARG...]` runs a command; `["CMD-SHELL", LINE]`, or a string, runs a line with `/bin/sh`; `["NONE"]` checks nothing, not even as the image says to. |
| `http` | `PORT[/path]`: healthy when a GET answers 2xx or 3xx. |
| `tcp` | `PORT`: healthy when a connection is accepted. |
| `interval` | Time between checks, such as `30s`. Default 10s. |
| `timeout` | Time a check may take. Default 5s. |
| `start_period` | Time after a start in which failed checks do not count. |
| `retries` | Failed checks in a row that make the instance unhealthy. Default 3. |
| `disable` | `true` checks nothing, as `test: ["NONE"]`. |

See [Health checks](../../guides/health-checks).

## Networks

| Key | |
|---|---|
| `subnet` | The network's subnet, such as `172.30.0.0/24`. Required, unless the network is external. |
| `gateway` | Its gateway. Defaults to the subnet's first address. |
| `ipam` | Another way of giving `subnet` and `gateway`: `ipam: {config: [{subnet: ..., gateway: ...}]}`, with one entry. |
| `mtu` | Its MTU. Default 1500. |
| `nameservers` | The nameservers its instances use. Default `8.8.8.8`. |
| `isolated` | Stops its instances reaching each other. |
| `external` | Use a network that already exists, named by the key or `name`, and never create or delete it. It takes no other settings. |
| `name` | The daemon's name for it, instead of `PROJECT-NAME`. |

See [Networking](../../concepts/networking).

## Volumes

| Key | |
|---|---|
| `size` | The volume's size, such as `20GiB`. Default 10 GiB. |
| `external` | Use a volume that already exists, named by the key or `name`, and never create or delete it. |
| `name` | The daemon's name for it, instead of `PROJECT-NAME`. |

A volume may be declared with nothing: `volumes: {data: }`.

## Variables

Values, not keys, may hold variables, which are taken from the environment,
then from `.env` in the project directory, or the file `--env-file` names:

| | |
|---|---|
| `$VAR`, `${VAR}` | The value, or nothing if it is not set. |
| `${VAR:-default}` | `default` if `VAR` is not set or is empty. |
| `${VAR-default}` | `default` if `VAR` is not set. |
| `${VAR:?message}` | Fails, saying `message`, if `VAR` is not set or is empty. |
| `${VAR?message}` | Fails if `VAR` is not set. |
| `${VAR:+other}` | `other` if `VAR` is set and not empty, otherwise nothing. |
| `${VAR+other}` | `other` if `VAR` is set, otherwise nothing. |
| `$$` | A `$`. |

A default, message or other may hold variables itself:
`${PORT:-${DEFAULT_PORT}}`.

An unquoted value takes the type of what it becomes, so `cpus: ${CPUS:-2}`
is a number and `external: ${EXTERNAL:-true}` a boolean. A quoted one,
`"${CPUS}"`, is always a string.

`dicer compose config` shows variables as they are written, not their
values, though it substitutes them to check the file; `--interpolate` shows
the values. The values reach the daemon all the same: an instance's
environment is part of its definition, which `dicer inspect` shows to
anyone who can use the daemon.

## Not supported

These keys are refused, and `dicer compose config` says why:
`build`, `deploy`, `scale`, `profiles`, `extends`, `secrets`, `configs`,
`privileged`, `cap_add`, `cap_drop`, `devices`, `network_mode`, `links`,
`extra_hosts`, `dns`, `user`, `working_dir`, `stdin_open`, `tty`, `init`,
`platform`, `pull_policy`, `logging`, `ulimits`, `sysctls`, `shm_size`,
`expose` and `stop_signal`. Most describe what a virtual machine already
has, such as its isolation from the host, or something Dicer has no
counterpart of.
