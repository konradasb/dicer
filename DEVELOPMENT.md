# Development

## Prerequisites

- Go, at the version in `go.mod`
- `make` and `curl`
- To run the daemon for real: a Linux machine with KVM, `erofs-utils` and
  `e2fsprogs`

Everything else — buf, golangci-lint, oapi-codegen, addlicense — is pinned in
the `Makefile` and installed into `bin/` on first use. `make help` lists every
target.

## Building

```console
make build
```

This builds the guest binaries `dicer-init` and `dicer-agent`, downloads the
Cloud Hypervisor and Firecracker binaries, and builds `dicer` for your
machine and `dicerd` for Linux, all into `bin/`. `dicerd` embeds the guest
and hypervisor binaries for its own architecture only; `make build
GOARCH=arm64` targets the other one.

## Testing

```console
make test               # unit tests, with the race detector
make test-integration   # also the tests that pull from a public registry
```

The daemon is Linux-only, but most of it is not: everything except
`internal/daemon`, `internal/hostnet` and `internal/guest/{boot,agent}` builds
and tests natively on macOS, and `go build ./...` skips the Linux-only
packages rather than failing on them. To cover those too, cross-compile or run
the suite in a container:

```console
GOOS=linux go vet ./...
docker run --rm -v "$PWD":/src -w /src golang:1.25 make test
```

## Linting and formatting

```console
make lint   # golangci-lint and buf lint
make fmt    # gofmt, goimports, gci and buf format
```

The linter set in `.golangci.yml` is curated rather than `default: all`, and
the file explains why anything notable is excluded. It should report zero
issues; if you add a suppression, say what makes it correct — `nolintlint`
will ask.

## Code generation

```console
make generate
```

- `make generate-proto` regenerates the gRPC code from `proto/` with buf. The
  API follows [Google's AIPs](https://google.aip.dev); `buf.yaml` lists the two
  buf rules that conflict with them.
- `make generate-hypervisor-client` regenerates the Cloud Hypervisor client
  from the OpenAPI spec vendored under `specs/`.

Generated files (`*.pb.go`, `*_gen.go`) are committed and excluded from
linting. Do not edit them; CI fails if they are out of date.

## Layout

```
*.go                    package dicer: the domain types, and the client
                        instance.go image.go volume.go network.go kernel.go
                        client*.go convert*.go errors.go
cmd/                    main packages, one line of wiring each
  dicer/                the CLI
  dicerd/               the daemon
  dicer-init/           guest PID 1
  dicer-agent/          guest exec and copy agent
proto/                  protobuf API definitions and generated code
internal/
  daemon/               configuration, wiring, process lifecycle
  cli/                  CLI commands and output formatting
  grpcapi/              gRPC handlers
  vm/                   instance lifecycle: starting, stopping, supervising
  filestore/            the definition store, as YAML on disk
  network/              networks, address allocation and host device names (portable)
  hostnet/              bridges, TAP devices, iptables, traffic shaping (Linux)
  image/                OCI pull and conversion to a bootable disk
  kernel/ volume/       the other resources a VM references
  initrd/ registry/     initramfs building and registry access
  hypervisor/           the VMM abstraction and its drivers
    cloudhypervisor/    Cloud Hypervisor, the default
    firecracker/        Firecracker
  process/              supervision of the VMM processes
  guest/                the host↔guest contract
    boot/               guest PID 1 implementation
    agent/              guest exec and copy agent implementation
  access/ certificate/  remote-access enrolment and TLS identities
  metrics/              the Prometheus metrics
  archive/              the tar streams file copies travel as
  version/              build identity
  atomicfile/ hostinfo/ small focused helpers
scripts/                the install and uninstall scripts users run
```

How the mechanisms that span several packages work -- such as how an
instance ends and is restarted -- is in [ARCHITECTURE.md](ARCHITECTURE.md).

Five rules hold the layout together:

- **The root package is the vocabulary.** `dicer.Instance`, `dicer.Image` and
  the rest are what the daemon stores, what the API carries and what the
  client returns. Everything else in the module may import it; it imports
  nothing but `proto/` and a leaf or two, so it cannot depend on the daemon.
- **The wire format does not leak.** Only the root package and
  `internal/grpcapi` import the generated protobuf types: the client converts
  what it sends and reads back, the daemon converts what it reads and sends,
  and the two are inverses, so no conversion is written twice. Nothing else
  would notice if the wire format were replaced.
- **The guest does not import the host.** `dicer-init` and `dicer-agent` run
  inside the thing being isolated and are embedded in the daemon's binary, so
  they keep to their own tree, the API vocabulary and a few leaf helpers.
- **Storage depends on the domain, never the reverse.** `vm` declares the
  `Definitions` interface it needs; `filestore` imports it to implement it.
  That is why the lifecycle can be tested against in-memory fakes.
- **One `Manager` per resource package.** `filestore`, `image`, `kernel`,
  `volume` and `vm` each expose exactly one, built with `NewManager(Config)`.
  The package name says which resource; the methods say what can be done to
  it -- `Create` or `Pull`, `Get`, `List`, `Delete`, `Path`. Interfaces are
  named for what they provide rather than for the `Manager` behind them:
  `vm.Images`, `vm.Kernels`, `vm.Volumes`, `vm.Definitions`.

## Adding a hypervisor

A driver implements `hypervisor.Starter`, to launch a VMM and boot a guest,
and `hypervisor.Hypervisor`, to control a running one. Operations a VMM does
not have return `errors.ErrUnsupported`, and its `Capabilities` say so in
advance.

Each driver embeds its own binaries in its own `embed.go`, and launches them
detached with `hypervisor.StartProcess`, passing whatever flags its VMM
needs.

`internal/daemon/starters.go` lists the drivers, and `hypervisor.Types()` is
what the API validates against; a type in one and not the other is a test
failure.

The Firecracker driver's tests show the two levels worth having: the
translation from Dicer's VM specification is tested as data, and the API
calls against a fake VMM on a Unix socket. Its `TestAgainstRealFirecracker`
goes further and drives the embedded binary, which works for everything
except booting, since that is what needs KVM.

## Testing against a real host

The hypervisor, networking and guest-boot paths need a Linux box with KVM and
root. A full manual pass is:

```console
sudo ./bin/dicerd serve --config example.yml &
./bin/dicer network create default --subnet 172.20.0.0/16
./bin/dicer kernel import vmlinux --arch x86_64 --url <kernel-url>
./bin/dicer instance create test --image alpine:3.21 --kernel vmlinux \
    --network default --vcpus 1 --memory 512MiB --disk 1GiB
./bin/dicer instance start test
./bin/dicer instance exec test -- sh
```

Restarting `dicerd` while `test` is running should re-adopt it rather than
report it stopped; that is the recovery path, and it is worth checking by hand
after any change to `internal/vm`.

## End-to-end tests

`test/e2e` automates that pass against a real machine. It builds the binaries
for the host's architecture, installs them over SSH, runs a daemon, boots a
guest and asserts on what happened inside it:

```console
make test-e2e DICER_E2E_HOST=10.10.0.101
```

| Variable | Default | |
|---|---|---|
| `DICER_E2E_HOST` | — | Required; without it the tests skip |
| `DICER_E2E_SSH_USER` | `root` | |
| `DICER_E2E_SSH_KEY` | — | Defaults to your SSH configuration and agent |
| `DICER_E2E_KERNEL_URL` | the kernel the installer recommends | |
| `DICER_E2E_KEEP` | — | Leave the daemon running afterwards, to poke at it |

The tests are behind the `e2e` build tag, so `go test ./...` and CI ignore
them. They need a host with KVM, `erofs-utils`, `e2fsprogs` and systemd, and
they need root on it.

What they cover: booting a guest under both hypervisors, reaching it over
vsock, the guest's hostname, guest networking out through NAT, address
allocation and release, host files injected at every start, volumes outliving
their instances, snapshot and restore of a guest's memory, re-adoption of a
running VM across a daemon restart, starting on boot, image pull/prune and the
protection of an image in use, and the metrics endpoint against instances that
really exist.

Everything is installed under its own prefix — `/opt/dicer-e2e`,
`/var/lib/dicer-e2e`, `/run/dicer-e2e` and a `dicer-e2e` systemd unit — so a
real installation on the same machine is left alone, and a run that is killed
rather than torn down is cleaned up by the next one. Failures quote the
daemon's journal.

## Licence headers

Every source file carries an MIT header. `make license-check` checks them and
`make license` adds missing ones.
