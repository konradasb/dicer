---
title: Compose projects
weight: 15
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
an instance of its own. The file says what each service is, what it needs
and what it waits for. `dicer compose up` creates what is missing, recreates
what has changed and starts the rest, in order.

## A project

A project is a directory with a compose file in it, called
`dicer-compose.yaml` or `compose.yaml`. `dicer compose` looks for it in the
current directory and then in each directory above it, and `-f` names
another file. This one runs a web front end, an API and PostgreSQL, with the
database's data on a volume:

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

`${DB_PASSWORD}` is taken from the environment or, if it is not set there,
from a `.env` file beside the compose file:

```text {filename="shop/.env"}
DB_PASSWORD=change-me
```

`api` and `migrate` reach the database as `db`, its service's name. See
[Names](#names). The [compose file reference](../../reference/compose-file)
has every key.

## Bring it up

```console
$ cd shop
$ dicer compose up -d
Network shop-default created (10.213.0.0/24)
Volume shop-pgdata created (20 GiB)
Image docker.io/library/postgres:17 pulled in 14.2s (151 MiB)
Instance shop-db started in 1.3s (10.213.0.37)
Waiting for shop-db to be healthy
Instance shop-db is healthy
Instance shop-migrate started in 1.1s (10.213.0.182)
Waiting for shop-migrate to finish
Instance shop-migrate finished
Instance shop-api started in 1.2s (10.213.0.91)
Instance shop-web started in 1.1s (10.213.0.64)
```

`up` creates the project's network, `shop-default`, on a free subnet, and
its volume. It pulls the images the host does not have, and starts each
service once the services it depends on are ready. Services that do not
depend on each other start at the same time.

Everything is named after the project. The project's name is the
directory's name, unless the file's `name` or `-p` gives another. The
instances here are `shop-web`, `shop-api` and so on, and you can manage
them with the other `dicer` commands too.

Without `-d`, `up` stays attached. It writes each instance's console, with
each line marked with the instance's name, until they all stop. Ctrl+C
stops them.

`--wait` returns once every instance is running, and healthy if it has a
health check. It implies `-d`, and suits a script or CI:

```console
$ dicer compose up --wait && ./run-integration-tests
```

## Look at it

```console
$ dicer compose ps
NAME          SERVICE  IMAGE                          STATUS                    IP           PORTS
shop-api      api      ghcr.io/acme/api:3             Up 2 minutes (healthy)    10.213.0.91  -
shop-db       db       docker.io/library/postgres:17  Up 2 minutes (healthy)    10.213.0.37  -
shop-migrate  migrate  ghcr.io/acme/api:3             Exited (0) 2 minutes ago  -            -
shop-web      web      docker.io/library/nginx:1.27   Up 2 minutes              10.213.0.64  8080->80/tcp
$ dicer compose logs -f api
$ dicer compose exec db psql -U shop
```

## Change it

Edit the file and run `up` again. Each instance carries a digest of its
definition in the label `dicer.compose.config-hash`, so `up` can tell which
services have changed. It recreates those, starts any that are stopped, and
leaves the rest running:

```console
$ dicer compose up -d
Instance shop-db is up to date
Instance shop-migrate started in 1.1s (10.213.0.182)
Waiting for shop-migrate to finish
Instance shop-migrate finished
Instance shop-api recreated in 1.4s (10.213.0.203)
Instance shop-web is up to date
```

`migrate` had finished, so it is started again. A job that other services
wait on runs at every `up`, so it should do no harm when there is nothing
for it to do.

A recreated instance boots from a fresh overlay disk, so keep what must survive on
a volume. `--force-recreate` recreates every instance, which you need after
`dicer compose pull` to pick up an image whose tag has moved.
`--no-recreate` never recreates an instance.

A service removed from the file leaves its instance behind. `up` and `down`
point out these orphans, and delete them when given `--remove-orphans`.

## Take it down

```console
$ dicer compose down
Instance shop-web deleted in 1.1s
Instance shop-api deleted in 1s
Instance shop-migrate deleted in 2ms
Instance shop-db deleted in 1.2s
Network shop-default deleted
```

`down` deletes the instances, in the reverse of the order they start in,
and then the networks the file declares. Volumes and their data are kept
unless you run `down --volumes`. External networks and volumes are never
deleted. `stop` and `start` stop and start the instances without deleting
them.

## Names

Services that name no network join the project's network called `default`.
Unless the file declares `default` itself, it is a network of the
project's own, `shop-default`. On it, each service is found by its name:
`api` connects to `db`, which resolves to the database's address.

The daemon's DNS answers for the network's running instances in two ways:
by hostname, which is the service's name unless `hostname` gives another,
and by instance name, such as `shop-db`. Two projects that each have a `db`
do not mix them up, because each `db` is on its own project's network.

A service reaches the host itself as `host.dicer.internal`. See
[Networking](../../concepts/networking#the-host).

To share a network with instances outside the project, declare it as an
external network:

```yaml
networks:
  default:
    name: default
    external: true
```

## Services are machines

Each service is a virtual machine booted from its image. This shapes what a
service can do:

- **Images come from a registry.** Build and push the image, then name it
  with `image`. A `build` key is refused.
- **`command` replaces the image's ENTRYPOINT and CMD**, as with
  `dicer run`. Give the whole command line, or `entrypoint` and `command`
  together.
- **A host path in `volumes` is a file, not a directory.** It is copied into
  the guest each time the instance starts, so a change reaches the guest at
  its next start. With a remote daemon, the path is on the daemon's host.
- **Each service joins one network.** A network the file gives no `subnet`
  gets a free /24 from `10.213.0.0/16`, one that no other network and none
  of the host's interfaces is on.
- **Volumes have a size**, 10 GiB unless given.
- **Sizes are the machine's.** `vcpus` (or `cpus`, as a whole number),
  `memory` (or `mem_limit`) and `disk` size the virtual machine. They are
  1 vCPU, 512 MiB and 10 GiB unless given.
- **A service is one instance.** There is no `scale`.

## Checking a file

A key `dicer compose` does not support is refused with the reason, rather
than ignored. `dicer compose config` checks a file without contacting the
daemon, and shows it as it was read. It shows variables as they are
written, such as `${DB_PASSWORD}`, so that you can share its output without
the values in `.env`. `--interpolate` shows the values instead.
