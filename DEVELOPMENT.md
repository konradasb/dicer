# Development

## Prerequisites

- Go, at the version in `go.mod`
- `make`, `curl` and `zstd`
- To run the daemon: a Linux host with KVM, `erofs-utils`, `e2fsprogs` and
  `iptables`

Every other tool is pinned in the `Makefile` and installed into `bin/` on
first use. `make help` lists every target.

## Building

```console
make build
```

Builds `dicer` for your machine and `dicerd` for Linux into `bin/`, with the
binaries `dicerd` embeds. `make guest-binaries` builds `dicer-init` and
`dicer-agent`, and downloads the default kernel. `make host-binaries`
downloads the hypervisors. `make build GOARCH=arm64` targets the other
architecture.

## Testing

```console
make test               # unit tests, with the race detector
make test-integration   # also the tests that reach a public registry
```

The daemon is Linux-only; most of the rest builds and tests on macOS too. To
cover the Linux-only packages from a Mac:

```console
GOOS=linux go vet ./...
docker run --rm -v "$PWD":/src -w /src golang:1.27 make test
```

### On a real host

Hypervisors, host networking and guest boot need a Linux host with KVM.
`make deploy` builds for Linux, installs `dicer` and `dicerd` on a host that
already runs the service, and restarts it:

```console
make deploy DEPLOY_HOST=root@my-kvm-host
```

The end-to-end tests install `dicer` and `dicerd` under a prefix of their
own, then boot guests under both hypervisors and check what happened inside
them. They need a host of their own: a daemon of theirs beside another would
share its default network's bridge and its firewall rules, so they refuse to
run while another `dicerd` is running.

```console
make test-e2e DICER_E2E_HOST=my-kvm-host
```

| Variable | Default | |
|---|---|---|
| `DICER_E2E_HOST` | — | Required |
| `DICER_E2E_SSH_USER` | `root` | |
| `DICER_E2E_SSH_KEY` | your SSH configuration and agent | |
| `DICER_E2E_SSH_PORT` | your SSH configuration, or 22 | |
| `DICER_E2E_KERNEL_URL` | the default kernel's release, for the host's architecture | Where the host downloads the kernel the tests import. It must be for the host's architecture. |
| `DICER_E2E_KEEP` | — | Leave the daemon running afterwards |

They are behind the `e2e` build tag, so `go test ./...` and CI skip them. The
host needs KVM, `erofs-utils`, `e2fsprogs` and systemd, and root.

## Linting and formatting

```console
make lint            # golangci-lint and buf lint
make fmt             # gofmt, goimports, gci and buf format
make breaking        # buf breaking, against main
make vuln            # govulncheck: known vulnerabilities in code the binaries call
make lint-workflows  # actionlint and zizmor, on .github/ (zizmor needs pipx)
```

Lint should report zero issues. A suppression says why it is correct.

## Code generation

```console
make generate
```

Regenerates the gRPC code from `proto/` and the Cloud Hypervisor client from
the OpenAPI spec in `specs/`. Generated files are committed; do not edit them.
CI fails if they are out of date.

## Hypervisor versions

`dicerd` embeds each hypervisor version it carries, and keeps them by the
[deprecation policy](docs/content/docs/concepts/hypervisors.md#support-and-deprecation).

To add a version:

1. Add it last to `CH_VERSIONS` or `FC_VERSIONS` in the `Makefile`, and to
   the driver's `version.go` as its `DefaultVersion`, first in
   `supportedVersions`.
2. For Cloud Hypervisor, run `make update-hypervisor-spec generate`. The
   client is generated from the newest version's spec but also talks to
   the older versions. They ignore fields they don't know, so a new field
   can go to every version only if the older ones work without it. If they
   don't, send it only to the versions that have it.
3. Run the end-to-end tests, which boot guests on the default version.
4. In the hypervisors page, add it to the table of versions as the default,
   and mark the one it replaces deprecated in the coming release.

To remove a version once the policy allows, take it out of the same places,
and mark the pull request breaking (`!`), as removing one is.

## Layout

```
*.go                    package dicer: connecting Go programs to a daemon
cmd/                    main packages: dicer, dicerd, dicer-init, dicer-agent
proto/                  the API: protobuf definitions and generated code
internal/
  daemon/               configuration, wiring, process lifecycle
  cli/                  the command line, dicer compose included
  compose/              compose files: reading them, and the requests they make
  grpcserver/           the API: its servers, and the handlers, which call the managers
  token/                the tokens that authenticate clients of the TCP listener
  instance/             instances and snapshots, and their lifecycle
  health/               health checks: running probes, and judging their results
  filestore/            resource definitions as YAML on disk, and their references
  network/ hostnet/     networks and addresses; bridges, TAP devices, iptables
  dns/                  each network's nameserver: guests' names, and forwarding
  image/ registry/      pulling images and converting them to disks
  kernel/ volume/       the other resources an instance uses
  virtiofs/ hostfs/     sharing host directories with guests, through the virtiofsd it embeds
  initrd/               the guest's initramfs
  hypervisor/           the hypervisor interface, and its two drivers
  process/              supervising hypervisor processes
  guest/                the host–guest contract, and dicer-init and dicer-agent
  event/ metric/        the event log, and serving the Prometheus metrics
  archive/              the tar streams file copies travel as
  errdefs/ naming/      error classes, and the rule resource names follow
  defaults/ version/    host paths, and build identity
  diskfile/             making, growing, copying and measuring disk files
  humanize/             writing sizes, durations and counts for people
  atomicfile/ hostinfo/ small helpers
test/e2e/               the end-to-end tests
docs/                   the documentation site
tools/docgen/           generates the documentation's reference pages
specs/                  the Cloud Hypervisor OpenAPI spec
scripts/                the install and uninstall scripts
build/                  the Linux packages, and their apt and dnf repository
```

Each resource's definition lives in the package named for it, beside the
code that manages it: `instance.Spec`, `network.Network`, `image.Image`.

## Documentation site

`docs/` is built with [Hugo](https://gohugo.io) and the
[Hextra](https://imfing.github.io/hextra/) theme, and published at
<https://dicer.sh>. It needs Go and Hugo, not Node.

```console
make docs-serve     # http://localhost:1313, rebuilt on every change
make docs           # the site, into docs/public
make docs-gen       # the generated reference pages
make docs-versions  # every version, into docs/site, as it is published
```

- Pages are Markdown under `docs/content/docs/`; each needs a `description`
  and an `icon` in its front matter, for the cards that link to it. A section's
  `_index.md` lists its pages with `{{< section-cards >}}`, and a page's
  `related` lists the pages to suggest at its end.
- The command line, configuration, compose file, metrics and API reference
  are generated by `make docs-gen`, and committed. Edit what they are
  generated from; CI fails if they are stale:
  - the command line from the commands in `internal/cli`: their `Long`,
    `Example` and flag usage;
  - the configuration from the doc comments on `internal/daemon`'s `Config`
    and the types under it. A key without one fails `make docs-gen`, and
    `internal/daemon`'s tests fail if a default the comments state is not
    the daemon's;
  - the compose file the same way, from `internal/compose`'s `rawFile`, and
    the keys it refuses from its `unsupported`;
  - the metrics from the `Description`s each package lists in its
    `MetricDescriptions`. The package serves them as a collector, and its
    tests fail if that serves anything else (`metrictest.CheckDescriptions`).
    A package with metrics of its own is added to `tools/docgen`'s list, and
    the daemon registers its collector;
  - the API from `proto/dicerd/v1`, with `tools/docgen/api.md.tmpl`.
- `docs/versions` lists the versions the site is built for, newest first.
  [RELEASES.md](RELEASES.md) says when to add one.

## Licence headers

Every source file carries an MIT header. `make license-check` checks them and
`make license` adds missing ones.

## Releases

[RELEASES.md](RELEASES.md) covers versioning and how to cut a release.
