# ADR 0007: Versioned release distribution

- Status: Accepted
- Date: 2026-09-20
- Updated: 2026-09-30, 2026-10-21

## Context

Bivrost needs installable binaries independently of the maintainer's Nix
integration. The maintained Go applications use GoReleaser snapshots on pull
requests, a checked-in version, and releases after main passes CI. Maintained
DMS plugins share the version-and-tag flow without GoReleaser binary packaging.

## Decision

Use `dev` as the default development branch. Feature PRs target `dev`.
A release promotion PR from this repository's `dev` to protected `main` must
increase `VERSION` to an untagged version. It passes Linux, macOS and Windows
tests plus archive validation
before creating `v<VERSION>` at that exact tested commit. Existing version tags
are never moved: later commits with the same version do not publish a new release.
Only `main` publishes releases; `dev` produces test results and snapshots.
Required promotion checks reject direct feature PRs to `main`, fork branches
named `dev`, missing version bumps and reused tags. Feature PRs and release
promotions merge by fast-forward, matching the maintained public applications:
the reviewed head is pushed to the base branch, which GitHub records as merging
the PR, so `main` equals `dev` at each promotion and never holds a commit
missing from `dev`. The 2026-09-30 update replaced squash promotions, whose
required back-merge into `dev` caused repeated reconciliation work; the
2026-10-21 update replaced squash feature merges and promotion merge commits,
which GitHub writes as new commits, with fast-forward.
No release-please bot is introduced.

This supersedes the initial direct-to-main development flow. Existing tags and
releases remain immutable. The migration restores the last released application
behavior on `main`; unfinished routing work is reapplied to `dev` afterward.

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
installation recommendation or release. Signing remains follow-up work.
Existing Nix packaging continues consuming pinned source.

## Homebrew and AUR packages

The 2026-10-21 update adds a Homebrew cask in `alcxyz/homebrew-tap` and the
`bivrost-bin` AUR package, matching the other released Go applications.
GoReleaser generates both from the same archives, but does not push them
(`skip_upload`). Once the release is published, `scripts/release.py packages`
publishes them:

- It requires a published release whose tag still resolves to the tested
  commit, and a checksum manifest identical to the published one. A rerun
  rebuilds the archives, so it stops instead of describing different bytes.
- The cask and PKGBUILD must name the release version and carry only digests
  from that manifest.
- Only the newest published release publishes packages, so rerunning an
  older release's workflow cannot roll the tap or the AUR back.
- The cask goes to the tap with a short-lived token from the
  `alcxyz-release-tap` GitHub App, minted before tagging and limited to the
  tap's contents. The PKGBUILD and `.SRCINFO` go to the AUR with the AUR account
  key in the shared `AUR_KEY` secret, checked before tagging, and only to a
  host presenting one of AUR's published host keys. Unchanged files are left alone, so rerunning the same
  workflow revision completes a partial publication.

The cask clears the macOS quarantine flag after installation because the
binaries are unsigned. A failed package step leaves the published release
unchanged; rerun the workflow once the tap or AUR recovers.

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
- Letting GoReleaser push the cask and AUR package while uploading assets would
  publish them before the draft release is verified and made public.
- A Homebrew formula (`brews`) is deprecated in GoReleaser in favour of casks.
