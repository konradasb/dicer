---
title: Compose projects
weight: 14
description: "Describe several instances, their networks and volumes in one file, and run them together with dicer compose."
icon: view-grid
related:
  - /docs/reference/compose-file
  - /docs/reference/cli/dicer_compose
  - /docs/guides/health-checks
  - /docs/guides/files-and-volumes
---

An application is often several workloads: a web server, an API, a database
and a job that migrates it. `dicer compose` runs them from one file, each as
an instance of its own. The file says what each is, what it needs and what it waits for; `dicer compose up`
creates what is missing, recreates what has changed and starts the rest, in
order.

## A project

A project is a directory with a compose file in it: `dicer-compose.yaml`, or
`compose.yaml`. This one runs a web front end, an API and PostgreSQL, with
the database's data on a volume:

```yaml {filename="shop/dicer-compose.yaml"}
services:
  web:
    image: nginx:1.27
    ports: ["8080:80"]
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
    depends_on: [api]

  api:
    image: ghcr.io/acme/api:3
    environment:
      DATABASE_URL: postgres://shop:${DB_PASSWORD}@db/shop
    healthcheck:
      http: 3000/healthz
    depends_on:
      db: {condition: service_healthy}
      migrate: {condition: service_completed_successfully}

  migrate:
    image: ghcr.io/acme/api:3
    command: ./migrate up
    environment:
      DATABASE_URL: postgres://shop:${DB_PASSWORD}@db/shop
    depends_on:
      db: {condition: service_healthy}

  db:
    image: postgres:17
    environment:
      POSTGRES_USER: shop
      POSTGRES_PASSWORD: ${DB_PASSWORD:?set DB_PASSWORD in .env}
      PGDATA: /var/lib/postgresql/data/pgdata
    volumes:
      - pgdata:/var/lib/postgresql/data
    memory: 2GiB
    healthcheck:
      test: pg_isready -U shop
      interval: 5s

volumes:
  pgdata:
    size: 20GiB
```

`${DB_PASSWORD}` is taken from the environment, or from a `.env` file beside
the compose file:

```text {filename="shop/.env"}
DB_PASSWORD=change-me
```

`api` and `migrate` reach the database as `db`, its service's name: see
[Names](#names). The [compose file reference](../../reference/compose-file)
has every key.

## Bring it up

```console
$ cd shop
$ dicer compose up -d
Network shop-default created (10.213.0.0/24)
Volume shop-pgdata created (20 GiB)
Image postgres:17 pulled in 14.2s (151 MiB)
Instance shop-db started in 1.3s (10.213.0.5)
Waiting for shop-db to be healthy
Instance shop-db is healthy
Instance shop-migrate started in 1.1s (10.213.0.2)
Waiting for shop-migrate to finish
Instance shop-migrate finished
Instance shop-api started in 1.2s (10.213.0.3)
Instance shop-web started in 1.1s (10.213.0.4)
```

`up` creates the project's network, `shop-default`, on a free subnet, and
its volume, pulls the images the host
does not have, and starts each service once those it depends on are ready.
Services that do not depend on each other start at the same time.

Everything is named after the project, which is the directory's name unless
the file's `name` or `-p` gives another: the instances are `shop-web`,
`shop-api` and so on, so they are the `dicer` command's to manage too.

Without `-d`, `up` stays attached: it writes each instance's console, each line marked with its instance's name, until
they all stop. Ctrl+C stops them.

`--wait` returns once every instance is running, and healthy if it is
checked, which suits a script or CI:

```console
$ dicer compose up -d --wait && ./run-integration-tests
```

## Look at it

```console
$ dicer compose ps
NAME           SERVICE   IMAGE                STATUS                     IP            PORTS
shop-api       api       ghcr.io/acme/api:3   Up 2 minutes (healthy)     10.213.0.3    -
shop-db        db        postgres:17          Up 2 minutes (healthy)     10.213.0.5   -
shop-migrate   migrate   ghcr.io/acme/api:3   Exited (0) 2 minutes ago   -             -
shop-web       web       nginx:1.27           Up 2 minutes               10.213.0.4    8080->80/tcp
$ dicer compose logs -f api
$ dicer compose exec db psql -U shop
```

## Change it

Edit the file and run `up` again. Each instance carries a digest of its
definition, in the label `dicer.compose.config-hash`, so `up` can tell
which services have changed. It recreates those, starts any that are
stopped, and leaves the rest running:

```console
$ dicer compose up -d
Instance shop-db is up to date
Instance shop-migrate started in 1.1s (10.213.0.2)
Waiting for shop-migrate to finish
Instance shop-migrate finished
Instance shop-api recreated in 1.4s (10.213.0.3)
Instance shop-web is up to date
```

`migrate` had finished, so it is started again: a job that others wait on
runs at each `up`, and should do no harm when there is nothing for it to do.

A recreated instance boots from a fresh disk, so keep what must survive on
a volume. `--force-recreate` recreates every instance, as after
`dicer compose pull` to take up an image whose tag has moved;
`--no-recreate` never does.

A service removed from the file leaves its instance behind. `up` and `down`
point these orphans out, and delete them with `--remove-orphans`.

## Take it down

```console
$ dicer compose down
Instance shop-web deleted in 1.1s
Instance shop-api deleted in 1.0s
Instance shop-migrate deleted in 2ms
Instance shop-db deleted in 1.2s
Network shop-default deleted
```

`down` deletes the instances, in the reverse of the order they start in,
and the networks the file declares. Volumes are kept, with their data,
until `down --volumes`. `stop` and `start` stop and start the instances
without deleting them.

## Names

The project has a network of its own, `shop-default`, unless the file
declares one called `default` or every service names another. On it, each
service is found by its name: `api` connects to `db`, which resolves to the
database's address. The daemon's DNS answers for the network's running
instances, by hostname, which is the service's name unless `hostname` gives
another, and by instance name, `shop-db`. Two projects with a `db` each do
not mix them up: each `db` is on its own project's network.

A service reaches the host itself as `host.dicer.internal`: see
[Networking](../../concepts/networking#the-host).

A project brought up by a Dicer from before services had names changes at
its next `up`: its services that name no network move to the project's
own, and those that set no `hostname` are given their service's name.
Both are changes to their definitions, so `up` recreates them, from fresh
disks and with new addresses, and creates the network first. Keep what must
survive on a volume, or pin a service where it was with `networks` and
`hostname`, before that `up`.

To share a network with instances outside the project, name it as an
external one:

```yaml
networks:
  default:
    name: default
    external: true
```

## Services are machines

Each service is a virtual machine, booted from its image, which shapes what
a service can say:

- **Images come from a registry.** Build and push the image, then name it.
- **`command` replaces the image's ENTRYPOINT and CMD**, as with `dicer
  run`. Give the whole command line, or `entrypoint` and `command`
  together.
- **A host path in `volumes` is shared or copied.** A directory is shared
  with the guest while it runs, which needs virtiofsd on the host; a file is
  copied into the guest each time it starts. With a remote daemon, the path
  is the daemon's host's, and a directory must be given with
  `type: directory`.
- **Each service joins one network.** A service that names none joins the
  project's network called `default`. A network the file gives no `subnet`
  gets a free one from `10.213.0.0/16`, one that no other network and none
  of the host's interfaces is on.
- **Volumes have a size**, 10 GiB unless given.
- **Sizes are the machine's.** `vcpus` (or `cpus`, a whole number),
  `memory` (or `mem_limit`) and `disk` size the virtual machine: 1 vCPU,
  512 MiB and 10 GiB unless given.
- **A service is one instance.**

A key `dicer compose` does not support is refused, with the reason, rather
than ignored. `dicer compose config` checks a file without touching the
daemon, and shows it as it is read. It shows variables as they are written,
`${DB_PASSWORD}`, so that its output can be shared without the values in
`.env`; `--interpolate` shows the values.
