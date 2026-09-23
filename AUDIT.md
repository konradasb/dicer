# Go Idiom Audit — `github.com/dicer-sh/dicer`

Phase 1 (inventory). No code changed.

## Baseline

Recorded before any change. Everything below must still hold afterwards.

| Check | Result |
| --- | --- |
| `gofmt -l .` | *(empty)* |
| `go build ./...` (darwin/arm64) | clean |
| `go vet ./...` (darwin/arm64) | clean |
| `GOOS=linux GOARCH=amd64 go build ./...` | clean |
| `GOOS=linux GOARCH=amd64 go vet ./...` | clean |
| `staticcheck ./...` | *(empty)* |
| `golangci-lint run ./...` | `0 issues.` |
| `go test ./...` | all packages `ok` |

**The baseline is completely clean.** The repo already runs a strict, curated
`golangci-lint` config (`revive` with `exported`, `package-comments`,
`error-strings`, `receiver-naming`, `context-as-argument`; plus `errorlint`,
`godot`, `nilerr`, `contextcheck`, `forcetypeassert`, `gosec`, `perfsprint`,
`gci`). That config already enforces, mechanically and on every commit, most of
audit categories (c) naming, (d) receivers/shadowing, (f) error handling, and
much of (j).

Consequence: there is no bulk of mechanical cleanup to do. The findings below
are the ones a linter cannot see — placement, duplication, and API shape. There
are nine of them, and none is a defect.

> **Note on `go build ./...`:** on darwin it silently skips `internal/vm`,
> `internal/grpcapi`, `internal/daemon`, `internal/hostnet` and
> `internal/guest/agent`, which are Linux-only — roughly a third of the
> codebase. Both baselines above are recorded, and every verification run in
> Phases 2–3 must include the `GOOS=linux` pair.

## Working tree

`git status` reports **363 changed entries, 148 of them untracked, with a
partially staged index** (28 files staged as deletions). A large refactor is
already in flight and uncommitted.

This directly conflicts with the "small, focused commits, one concern per
commit" constraint: any commit I make would sweep that in-flight work in with
it. **I have not committed anything.** See the question at the end.

## Package map

Layering is a clean DAG — `cmd → daemon → grpcapi → vm → {hypervisor, image,
network} → leaf`. No cycles, no back-edges, no near-cycles. The root package is
the shared vocabulary; nothing in `internal/` is imported by a lower layer than
its owner.

| Package | Responsibility | Internal imports |
| --- | --- | --- |
| `.` (dicer) | The domain vocabulary — `Instance`, `Image`, `Volume`, `Network`, `Kernel` — plus the client that speaks it. | *(none)* |
| `cmd/dicer` · `dicerd` · `dicer-agent` · `dicer-init` | Four `main`s, ~20 lines each; all real work delegated. | `cli`, `daemon`, `guest/agent`, `guest/boot` |
| `internal/cli` | The `dicer` CLI: cobra commands, flag parsing, output. | `.`, `archive`, `cli/printer`, `cli/remote`, `image/reference` |
| `internal/cli/printer` | Renders values as table, JSON, YAML or Go template. | *(none)* |
| `internal/cli/remote` | The daemons the CLI knows how to reach, and its client key. | `.`, `atomicfile` |
| `internal/daemon` | Assembles and runs `dicerd`: config, listeners, service wiring. | 18 packages (the composition root) |
| `internal/grpcapi` | gRPC handlers: proto ⇄ domain at the server edge, auth, audit. | `.`, `access`, `certificate`, `events`, `filestore`, `guest`, `hostinfo`, `hypervisor`, `image`, `kernel`, `network`, `vm`, `volume` |
| `internal/vm` | Instance lifecycle — start, stop, pause, snapshot, supervise, recover. | `.`, `atomicfile`, `guest`, `hypervisor`, `image`, `network`, `process` |
| `internal/hypervisor` | The hypervisor interface, VM spec, process and vsock plumbing. | `process` |
| `internal/hypervisor/{firecracker,cloudhypervisor}` | Two implementations, each with an embedded binary. | `atomicfile`, `hypervisor`, `process` |
| `internal/image` | Pull, unpack to EROFS, index, GC, prune. | `.`, `atomicfile`, `image/reference`, `registry` |
| `internal/image/reference` | Parses and resolves image references. | *(none)* |
| `internal/registry` | OCI registry client (go-containerregistry + umoci). | `image/reference` |
| `internal/filestore` | YAML definitions on disk: instances, networks, volumes, kernels, access. | `.`, `atomicfile` |
| `internal/network` | Guest address allocation and interface naming. | `.`, `atomicfile` |
| `internal/hostnet` | Host-side networking: bridges, TAPs, iptables, traffic control. | `.`, `network` |
| `internal/access` | Caller identity and the trusted-client store. | `.`, `certificate` |
| `internal/certificate` | CA, leaf issuance, TLS config. | `atomicfile` |
| `internal/guest` | The guest contract shared by host and guest: config, status, exit. | `.` |
| `internal/guest/boot` | `dicer-init`: the in-guest boot sequence, ending in exec of the workload. | `.`, `guest` |
| `internal/guest/agent` | `dicer-agent`: in-guest exec, copy, probe, shutdown over vsock. | `.`, `archive`, `guest` |
| `internal/initrd` | Builds and caches the initramfs guests boot from. | `image/reference`, `registry` |
| `internal/kernel` · `volume` | Kernel fetch/cache; volume disk provisioning. | `.` |
| `internal/events` · `metrics` · `hostinfo` | Event log; Prometheus collectors; host CPU/mem/disk facts. | `.`, `atomicfile` / *(none)* / `.` |
| `internal/archive` · `atomicfile` · `process` | Tar streaming; atomic file replace; process spawn/attach. | *(none)* |

## Findings

Nine findings. Category letters refer to the brief.

### Won't fix — checked and clean

These categories were examined and produced nothing worth changing. Recording
them so the absence is a result, not an omission.

- **(a) Package naming** — every package is short, lowercase, single-word, no
  underscores, no `util`/`common`/`helpers`/`misc`, no stutter. `hostnet`,
  `hostinfo`, `atomicfile` and `filestore` are compounds but each is one
  concept and reads correctly at the call site (`hostnet.SetupBridge`).
- **(b) File naming** — no filename in the repo contains an uppercase letter or
  a double dash; all are snake_case. Build-tagged pairs (`clone_linux.go` /
  `clone_other.go`, `embed_amd64.go` / `embed_arm64.go`,
  `attach_linux.go` / `attach_other.go`) follow the stdlib convention.
- **(c) Function naming** — `revive`'s `exported` rule passes; no `Get` prefix
  on getters outside the deliberate `Definitions.GetInstance` interface (which
  mirrors the store's own vocabulary); initialisms are consistently `ID`,
  `IP`, `TAP`, `TTY`.
- **(d) Variable naming** — `revive`'s `receiver-naming` and `predeclared`
  pass. Receivers are consistent within each type.
- **(f) Error handling** — `errorlint`, `nilerr`, `errcheck` and `errname` all
  pass. Every `_ =` discard I sampled carries a comment saying why
  (`iptables.go:167` "fails if the chain already exists, which is fine"). The
  single `panic` (`internal/vm/vm.go:289`) guards a genuinely impossible
  `sync.Map` type confusion — correct use. The one capitalized error string,
  `internal/hostinfo/meminfo.go:45`, begins with the proper noun `MemTotal`
  (a `/proc/meminfo` field), which is correct.
- **(h) Concurrency** — 16 goroutines total, each in a package that owns its
  lifetime; `contextcheck` passes, so no inherited context is dropped; `ctx` is
  first parameter everywhere outside generated code. Shared state is guarded
  (`internal/vm/vm.go` holds six named locks with a comment each).
- **(i) Architecture** — DAG confirmed above. Zero `init()` functions outside
  generated protobuf. Every package-level `var` is an immutable lookup table
  (`validTransitions`, `dicerChains`, `spinnerFrames`, compiled regexps) except
  finding 2 below. The four `main`s hold no business logic.

### SAFE

**1. `internal/cli/instance.go` is four concerns in one 1098-line file** — (b), (j)
`internal/cli/instance.go:26-1092`. The file holds, in order: presentation
(`printableInstance`, `instanceStatus`, `formatPorts`, `firstLine`, L26–144);
command construction (`newInstance*Command`, L146–341); flag→spec parsing
(`addInstanceSpecFlags` through `generateName`, L343–1090, and the bulk of the
file); and the YAML instance-file format (`instanceFile`, `volumeMountFile`,
`fileMountFile`, `readInstanceFile`, `(*instanceFile).spec`, L179–216 + 796–888,
split across the file). It is 2.5× the next-largest file in the package.
*Rule:* Effective Go — group files by concept.
*Fix:* split, within the package, into `instance.go` (commands + presentation),
`instance_flags.go` (flag parsing), `instance_file.go` (the YAML form). Pure
file moves; no identifier changes, no API change.

**2. `access.Local` is an assignable package-level var** — (i)
`internal/access/identity.go:20`. `var Local = Identity{}` is the only
package-level mutable value in the tree that is not an immutable table. Any
file in `internal/access` can reassign it, and `Identity`'s zero value is
already the local identity, so the var earns nothing.
*Rule:* Google Go Style — avoid mutable package-level state.
*Fix:* replace with `func LocalIdentity() Identity { return Identity{} }`,
which also parallels the existing `ClientIdentity(name)`. Two call sites in
`internal/grpcapi`. `internal/`-only, so no public API change.

**3. `shortDigest` is duplicated byte-for-byte, comment included** — (j)
`internal/image/manager.go:452` and `internal/cli/image.go:41` are identical
down to the doc comment.
*Rule:* Effective Go — don't repeat yourself.
*Fix:* see finding 7 — the honest resolution here may be to keep both. Listed
as SAFE only in the sense that deduplicating is mechanical *if* we decide to;
the decision itself is finding 7.

### NEEDS-REVIEW

**4. Boolean flag parameters on exported client methods** — (e)
`client_instance.go:161` `DeleteInstance(ctx, name string, force bool)`;
`client_resource.go:40` `DeleteImage(ctx, ref string, force bool)`.
At the call site these read `DeleteInstance(ctx, "web", true)`, which says
nothing.
*Rule:* Google Go Style — avoid boolean parameters that let a caller flip
behaviour they can't name.
*Fix:* an options struct or a `DeleteInstanceForce` sibling.
**These are exported on the module's public API.** Recommend **won't fix** —
the churn is not worth it for a two-method wart, and `force bool` is the
established idiom in this problem domain (docker, kubectl). Needs your call.

**5. Unexported boolean flag parameters** — (e)
`internal/vm/delete.go:21` `force`; `internal/vm/supervise.go:117` `graceful`;
`internal/vm/start.go:152` `stopped`; `internal/cli/instance.go:343`
`withDefaults`, `:387` `start`; `internal/cli/output.go:28` `columns`;
`internal/hostnet/tap.go:76` `isolated`; `internal/archive/archive.go:214`
`isDir`; `internal/guest/agent/exec.go:181` `tty`.
Same rule as 4, but all internal, so fixable without API impact.
*Fix:* named constant pairs (`gracefulStop`/`immediateStop`) at the call sites,
or small option types. Recommend fixing only `graceful` and `force`, whose call
sites genuinely read ambiguously; the rest (`isDir`, `tty`) are self-evident
from the parameter name at a one-line call. Needs your call on scope.

**6. Host/guest proto conversion exists twice** — (j)
`convert_instance.go:105` / `internal/grpcapi/convert.go:102`
(`restartPolicyToProto`); `convert_health.go:51` / `internal/grpcapi/health.go:20`
(`healthCheckFromProto`); and the same pattern across the other `convert_*.go`
pairs.
This looks like duplication but `doc.go:7-12` states it as a deliberate
design: "The generated protobuf messages are not part of it: they are how the
two talk, converted at each edge and seen by neither side's code." The two
copies also differ — the grpcapi side returns `error` where the client side
does not, because the server validates and the client trusts.
*Recommendation:* **won't fix**, and add a one-line pointer to `doc.go` from
`internal/grpcapi/convert.go` so the next reader doesn't file this as a bug.

**7. Small helpers duplicated across package boundaries** — (j)
`shortDigest` (finding 3); `truncate` (`internal/vm/health.go:293`,
`internal/guest/agent/probe.go:156`); `firstLine` (`internal/cli/instance.go:137`,
`internal/vm/health.go:244`); `plural`, `size`, `duration`
(`internal/cli/*` and `internal/vm/events.go`); `ptr[T]`
(`client_instance.go:249`, `internal/hypervisor/cloudhypervisor/config.go:117`).
Deduplicating these requires either a new shared package — which would have to
be named something like `internal/util`, exactly what the brief forbids — or
making `internal/cli` (client-side) import `internal/image` (daemon-side),
which would breach the layering that finding (i) confirms is currently clean.
*Recommendation:* **won't fix.** "A little copying is better than a little
dependency" (Go proverb) is the right call here, and the alternative is
strictly worse by the same standards. Needs your agreement.

**8. `internal/cli/remote.go` and `internal/cli/remote/` share a name** — (b)
The file `internal/cli/remote.go` (package `cli`) sits beside the directory
`internal/cli/remote/` (package `remote`), and the file imports the package.
Legal, and arguably correct — the file is the *command*, the package is the
*store* — but it reads as a mistake on first encounter.
*Fix:* rename the file to `remote_cmd.go`, or leave it and add a header
comment. Low value either way; recommend the header comment.

**9. `parseVersion` duplicated between hypervisor backends** — (j)
`internal/hypervisor/cloudhypervisor/version.go:29` and
`internal/hypervisor/firecracker/version.go:29`.
Same shape, different binaries and different output formats to parse.
*Recommendation:* **won't fix** — the two hypervisors are deliberately
independent implementations behind `hypervisor.Hypervisor`, and coupling their
version parsing would be the wrong kind of sharing.

## Summary

The codebase is in unusually good shape. Baseline is clean on `gofmt`, `go
vet`, `staticcheck`, `golangci-lint` and `go test`, on both darwin and linux
targets. Architecture is a clean DAG with consumer-defined interfaces
(`internal/vm/vm.go:42-107` is a textbook example), no `init()` abuse, no
mutable global state bar one, and package doc comments that explain *why*
rather than restate the package name.

Of nine findings, **three are worth doing** (1, 2, and — if you want it — the
`graceful`/`force` half of 5). Four I recommend closing as won't-fix with a
reason (4, 6, 7, 9), because the idiomatic fix is worse than the wart. Two are
cosmetic (3, 8).

There is no Phase 2 worth the name here: the mechanical cleanup this brief
anticipates has already been done and is held in place by the linter config.

**Architectural note:** the one thing I'd flag beyond style is that
`internal/daemon` imports 18 packages as the composition root. That is correct
for a composition root, but it means `daemon` is where a layering mistake would
first become possible. It's worth keeping `initServices`
(`internal/daemon/daemon.go:224`, 94 lines) as pure wiring and resisting logic
creeping into it.

---

## Before Phase 2 — two things I need from you

1. **The working tree.** 363 changed entries, 148 untracked, index partially
   staged. I can't make focused commits on top of that without entangling your
   in-flight refactor. Should I commit onto it anyway, work on a branch, or
   wait until you've landed what's in progress?

2. **Approvals.** Findings 1 and 2 I'd apply as-is. Findings 4, 6, 7 and 9 I
   recommend closing as won't-fix. Finding 5 needs a scope decision. Say which
   you want and I'll proceed.
