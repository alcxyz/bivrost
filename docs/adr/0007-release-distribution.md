# ADR 0007: Reproducible release distribution

- Status: Proposed
- Date: 2026-09-20
- Scope: Future work; no implementation is claimed here.

## Proposal

Build versioned binaries for supported operating systems from a reproducible
release pipeline. Publish checksums beside each release on canonical GitHub
repository `alcxyz/bivrost`, under the MIT License. A Forgejo mirror may
republish or reference the same artifacts but is not the canonical release
source.

Optional artifact signing is a later extension. The initial pipeline must not
make signing keys or a signing service a hidden runtime dependency.

## Acceptance direction

The eventual pipeline should document its source revision, build inputs,
target matrix, checksum format, and verification command. Rebuilding from the
same inputs should produce equivalent artifacts or explain any intentional
reproducibility boundary.


## Open implementation choices

Evaluate release-please for version and changelog automation, with explicit
component outputs if a shared workflow needs multiple packages. Build from the
exact release revision, embed its version, and publish immutable artifacts
idempotently without replacing existing assets. The supported architecture
matrix, installers, provenance and signing remain to be selected. Downstream
Nix packaging may consume pinned source rather than release binaries.
