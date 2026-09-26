# Release notes template

Leave out any section with nothing in it. Replace everything in angle
brackets; keep the headings as they are.

````markdown
<One or two sentences: what this release is about, for someone deciding
whether to upgrade. Name the one or two changes that matter most.>

## Breaking changes

- **<What changed, as a user sees it.>** <What to do about it: the flag,
  field or command to use instead, or the step to take when upgrading.>
  (#<N>)

## New features

- **<What you can now do.>** <One or two sentences: how, and a link to the
  documentation if a page covers it.> (#<N>)

## Fixes

- <The problem a user saw, in the past tense, and that it is fixed.> (#<N>)

## Upgrading

Upgrade the package, or run `install.sh` again with `--ref <TAG>`. Guests keep
running while the daemon restarts, and the new daemon takes them over. See
[Upgrading](https://dicer.sh/v<MAJOR.MINOR>/docs/guides/operating-the-daemon/#upgrading).

<Anything this upgrade needs beyond that: a configuration change, or a step
to take before or after. Leave out if there is nothing.>

## Verifying

```console
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity https://github.com/konradasb/dicer/.github/workflows/release.yaml@refs/tags/<TAG> \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums.txt
```

## Contributors

<@login, @login>, thank you.

**Full changelog**: https://github.com/konradasb/dicer/compare/<PREV>...<TAG>
````
