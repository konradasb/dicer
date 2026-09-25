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
`compose.yaml`. This one runs a web front end, an API and PostgreSQL, on a
network of their own, with the database's data on a volume:

```yaml {filename="shop/dicer-compose.yaml"}
services:
  web:
    image: nginx:1.27
    ports: ["8080:80"]
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
    networks: [backend]
    depends_on: [api]

  api:
    image: ghcr.io/acme/api:3
    environment:
      DATABASE_URL: postgres://shop:${DB_PASSWORD}@172.30.0.10/shop
    networks: [backend]
    healthcheck:
      http: 3000/healthz
    depends_on:
      db: {condition: service_healthy}
      migrate: {condition: service_completed_successfully}

  migrate:
    image: ghcr.io/acme/api:3
    command: ./migrate up
    environment:
      DATABASE_URL: postgres://shop:${DB_PASSWORD}@172.30.0.10/shop
    networks: [backend]
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
    networks:
      backend: {ipv4_address: 172.30.0.10}
    memory: 2GiB
    healthcheck:
      test: pg_isready -U shop
      interval: 5s

networks:
  backend:
    subnet: 172.30.0.0/24

volumes:
  pgdata:
    size: 20GiB
```

`${DB_PASSWORD}` is taken from the environment, or from a `.env` file beside
the compose file:

```text {filename="shop/.env"}
DB_PASSWORD=change-me
```

The [compose file reference](../../reference/compose-file) has every key.

## Bring it up

```console
$ cd shop
$ dicer compose up -d
Network shop-backend created (172.30.0.0/24)
Volume shop-pgdata created (20 GiB)
Image postgres:17 pulled in 14.2s (151 MiB)
Instance shop-db started in 1.3s (172.30.0.10)
Waiting for shop-db to be healthy
Instance shop-db is healthy
Instance shop-migrate started in 1.1s (172.30.0.2)
Waiting for shop-migrate to finish
Instance shop-migrate finished
Instance shop-api started in 1.2s (172.30.0.3)
Instance shop-web started in 1.1s (172.30.0.4)
```

`up` creates the project's network and volume, pulls the images the host
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
shop-api       api       ghcr.io/acme/api:3   Up 2 minutes (healthy)     172.30.0.3    -
shop-db        db        postgres:17          Up 2 minutes (healthy)     172.30.0.10   -
shop-migrate   migrate   ghcr.io/acme/api:3   Exited (0) 2 minutes ago   -             -
shop-web       web       nginx:1.27           Up 2 minutes               172.30.0.4    8080->80/tcp
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
Instance shop-migrate started in 1.1s (172.30.0.2)
Waiting for shop-migrate to finish
Instance shop-migrate finished
Instance shop-api recreated in 1.4s (172.30.0.3)
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
Network shop-backend deleted
```

`down` deletes the instances, in the reverse of the order they start in,
and the networks the file declares. Volumes are kept, with their data,
until `down --volumes`. `stop` and `start` stop and start the instances
without deleting them.

## Reaching other services

Services have no names the others can resolve: a service is reached by its
address. Give the ones
others connect to a fixed address on the project's network, with
`ipv4_address`, as `db` has above, and pass it to the rest in their
environment.

## Services are machines

Each service is a virtual machine, booted from its image, which shapes what
a service can say:

- **Images come from a registry.** Build and push the image, then name it.
- **`command` replaces the image's ENTRYPOINT and CMD**, as with `dicer
  run`. Give the whole command line, or `entrypoint` and `command`
  together.
- **A host path in `volumes` is a file, not a directory**, copied into the
  guest each time it starts; a change reaches the guest at its next start.
  With a remote daemon, the path is the daemon's host's.
- **Each service joins one network.** A service that names none joins the
  file's network called `default`, if it declares one, or else the
  daemon's default network. A network needs a `subnet`.
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
