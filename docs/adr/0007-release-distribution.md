# ADR 0007: Versioned release distribution

- Status: Accepted
- Date: 2026-09-20
- Updated: 2026-09-21

## Context

Bivrost needs installable binaries independently of the maintainer's Nix
integration. The maintained Go applications use GoReleaser snapshots on pull
requests, a checked-in version, and releases after main passes CI. Maintained
DMS plugins share the version-and-tag flow without GoReleaser binary packaging.

## Decision

Follow that existing pattern. `VERSION` selects the next release. A reviewed
change to main passes Linux, macOS and Windows tests plus archive validation
before creating `v<VERSION>` at that exact tested commit. Existing version tags
are never moved: later commits with the same version do not publish a new release.
No release-please bot or permanent development branch is introduced.

Use pinned GitHub Actions and an exact GoReleaser version. `.go-version` records
the build toolchain. Cross-compile with CGO disabled, trimmed paths and embedded
version. GitHub is the canonical binary distribution location; Forgejo retains
its existing source mirror role. Private catalogues, credentials and downstream
packaging never enter upstream archives.

Publish Linux and macOS amd64/arm64 archives and Windows amd64 ZIPs with the MIT
license, README, neutral example configuration and SHA-256 checksums. Automated
build/test support is not a claim of completed live platform QA on Windows.

Publication uses a draft until all expected assets have been uploaded and their
content verified. Retry only missing assets; differing or unexpected existing
assets stop publication. Published releases are left unchanged. Interrupted
publication can be retried by rerunning the same workflow revision, including
its checks, rather than retagging a newer commit.

Keep write permission restricted to the main release job. Pull requests only
build snapshots with read permission. A snapshot is a CI artifact, not an
installation recommendation or release. Signing, Homebrew and AUR publishing
remain follow-up work with separate credential and ownership setup. Existing
Nix packaging continues consuming pinned source.

## Reproducibility boundary

Source revision, toolchain, release-tool version and build flags are recorded in
the tag and workflow configuration. Binaries omit local paths, VCS dirty state
and variable build IDs. Pinning these inputs makes rebuild comparison possible;
a checksum manifest verifies downloaded contents but is not a signature.
Changes to compiler, archiver or build inputs may change bytes and require a
new release. Do not claim cross-toolchain byte identity.

## Alternatives considered

- A release-please workflow differs from the currently maintained app/plugin
  pattern and is not needed to establish a first binary release.
- Publishing on every main commit would remove the deliberate version boundary.
- Replacing existing release assets on retry would make a version ambiguous.
- Requiring Nix or package-manager publishing would block standalone consumers
  on unrelated distribution infrastructure.
