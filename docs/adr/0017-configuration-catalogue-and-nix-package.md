# ADR 0017: Catalogue in user configuration and a Nix package

- Status: Accepted
- Date: 2026-10-10

## Context

ADR 0003 made `BIVROST_CATALOGUE_FILE` the only way for a deployment to
supply its catalogue. A downstream Nix package therefore had to wrap the
binary to set that variable, so the deployment owned the build of Bivrost as
well as its data. That tied the Bivrost version to the downstream repository
and allowed only one installed build per machine, because the catalogue
existed only inside that wrapper. A variable exported by a login shell does
not reach terminals and multiplexers started before it was set.

## Decision

When `BIVROST_CATALOGUE_FILE` is unset or empty, Bivrost reads `catalogue.json`
in the user's `bivrost` configuration directory, beside the settings file
(`XDG_CONFIG_HOME` when set, otherwise the platform's native user
configuration directory). The variable still takes precedence. A missing
default file is an empty catalogue. An invalid file or broken symlink is an
error, as for local environment overrides, so a deployment that meant to
supply a catalogue never silently gets none. The file has the same format and
limits as the variable's file.

The repository also provides a Nix flake package. It wraps `bivrost` with the
Azure CLI (with the `bastion` and `ssh` extensions), OpenSSH, kubectl and
kubelogin, which sessions run. It contains no catalogue. Its version is
`VERSION` plus the source revision, so a release and a later development
commit with the same `VERSION` are told apart. Release archives (ADR 0007)
remain the distribution for everyone else, and Nix stays optional.

## Consequences

A deployment can ship its catalogue as a configuration file and leave the
choice of Bivrost build to the machine's owner. Several builds, such as a
release and a development build, can read the same configuration. ADR 0003's
boundary is unchanged: the catalogue stays downstream, holds metadata only,
and is not a permission grant. A file placed in the configuration directory is
trusted like a local environment override.

## Alternatives considered

- Keep the variable as the only source: deployments must keep wrapping the
  binary or rely on shell start-up files.
- Merge several catalogue files from a directory: no deployment needs more
  than one catalogue yet, and merge rules would have to settle conflicts.
- Leave Nix packaging to each deployment: every consumer repeats the runtime
  tool wrapping.
