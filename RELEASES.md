# Releases

## Versioning

Dicer follows [Semantic Versioning](https://semver.org): a release is
`MAJOR.MINOR.PATCH`, tagged `vMAJOR.MINOR.PATCH`.

- **Major**: something that worked stops working, or works differently.
- **Minor**: something new, that leaves what worked working.
- **Patch**: fixes only.

What a version makes promises about:

- The API, `dicerd.v1` in `proto/`, its error codes included
- The command line's commands and flags, and its `--format json` and
  `--format yaml` output; not its tables and messages, which are for people
- The daemon's configuration file
- The daemon's state: a newer daemon of the same major version takes over the
  data directory, and the running guests, of an older one
- The Go package `github.com/konradasb/dicer`
- The names and labels of the Prometheus metrics

Anything under `internal/` is not covered.

Until 1.0.0, a minor release may break any of these, and says so in its
release notes; a patch release never does. A pre-release is tagged
`v1.2.0-rc.1`, and promises nothing.

## Creating a release

Releasing takes a maintainer, and `main` passing CI.

1. **Choose the version.** Look at what changed since the last release, by
   PR title: a `feat:` is a minor release, a `fix:` a patch release, and a
   breaking change a major one (a minor one before 1.0.0).

2. **Tag `main` and push the tag.**

   ```console
   git switch main && git pull
   git tag -a v0.2.0 -m v0.2.0
   git push origin v0.2.0
   ```

   The [release workflow](.github/workflows/release.yaml) builds a draft
   release with GoReleaser: `dicer` for Linux and macOS, `dicerd` for Linux,
   on amd64 and arm64, the `dicer` deb and rpm packages, with checksums
   signed by cosign, and SBOMs. Its notes are left empty.

3. **Write the release notes.** In Claude Code, in this repository:

   ```text
   /release-notes v0.2.0
   ```

   The [skill](.claude/skills/release-notes/SKILL.md) reads the commits and
   pull requests since the last release, writes the notes as its
   [template](.claude/skills/release-notes/template.md) lays them out, and
   shows them to you; once you approve them, it puts them in the draft.
   Breaking changes come first, each with what to do about it. Running the
   release workflow again replaces the draft, notes and all.

4. **Review the draft and publish it.** Check the notes as GitHub shows
   them. For a pre-release, tick *Set as a pre-release*.

   Publishing a release, not a pre-release, runs the
   [packages workflow](.github/workflows/packages.yaml), which adds its
   packages to the apt and dnf repository; [build](build/README.md) covers
   how, and setting it up. Check it succeeds.

5. **Publish its documentation.** Add the release to `docs/versions`, below
   `dev`, in a pull request:

   ```text
   dev   .
   v0.2  v0.2.0
   v0.1  v0.1.3
   ```

   Documentation is kept per minor version: a patch release updates its
   minor version's line instead, `v0.2  v0.2.1`. Once merged, the site shows
   the new version, and its root leads to it. Only do this after the tag is
   pushed, as the documentation is built from the tag.

A release is installed from source with the install script's `--ref`:

```console
curl -fsSL https://raw.githubusercontent.com/konradasb/dicer/main/scripts/install.sh | bash -s -- --ref v0.2.0
```

## Verifying a release

The checksums are signed with cosign by the release workflow itself, with no
key to keep. Check them, then the archives against them:

```console
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity https://github.com/konradasb/dicer/.github/workflows/release.yaml@refs/tags/v0.2.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums.txt
```
