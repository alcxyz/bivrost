# ADR 0003: Catalogue packaging boundary

- Status: Accepted
- Date: 2026-09-20

## Context

The reusable CLI needs a stable way to load named environment metadata, while
each deployment owns its subscriptions, resource names, private hosts, and
operational policy. Embedding downstream data in a public repository creates
copying and update pressure and can invite credentials into source control.

## Decision

The public package defines the catalogue shape and loading rules. A downstream
deployment supplies a generic JSON map through `BIVROST_CATALOGUE_FILE`.
Users may provide a per-environment file under
`XDG_CONFIG_HOME/bivrost/environments/`, and `--config PATH` selects an
explicit profile. These sources are profiles, not permission grants.

Downstream catalogue data is packaged and distributed separately. It is not
maintained as a fork of the public repository. Catalogue files contain
connection metadata only and never become a credential store: passwords,
tokens, private keys, and similar secrets are excluded.

## Consequences

The public project can be reused without publishing deployment details, and a
deployment can update its catalogue independently. Operators must distribute
and validate their own metadata and keep credentials in the identity and
credential systems intended for them.
