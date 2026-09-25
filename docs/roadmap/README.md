# Roadmap

GitHub issues track future work and validation. Accepted architecture decisions
live in the [ADR index](../adr/README.md); an accepted direction does not mean
its implementation is complete.

- [#1: define the plain command-execution lifecycle](https://github.com/alcxyz/bivrost/issues/1)
- [#2: add short-lived PIM JIT lifecycle](https://github.com/alcxyz/bivrost/issues/2)
- [#4: add Heimdal session runtime metadata](https://github.com/alcxyz/bivrost/issues/4)
- [#5: establish a Terraform baseline](https://github.com/alcxyz/bivrost/issues/5)
- [#6: complete Windows native Podman Machine QA](https://github.com/alcxyz/bivrost/issues/6)
- [#8: add Tesseract encrypted local configuration](https://github.com/alcxyz/bivrost/issues/8)
- [#10: incremental multi-cloud capability boundaries](https://github.com/alcxyz/bivrost/issues/10)
- [#21: remote commands through SSH](https://github.com/alcxyz/bivrost/issues/21)
- [#22: session-scoped browser proxy access](https://github.com/alcxyz/bivrost/issues/22)
- [#28: isolate session transports from unrelated local users](https://github.com/alcxyz/bivrost/issues/28)
- [#31: conditional Heimdal publication and rollback](https://github.com/alcxyz/bivrost/issues/31)

Completed foundation: [#3, release distribution](https://github.com/alcxyz/bivrost/issues/3).
Initial Heimdal publication and opt-in startup retrieval are on `dev`;
live Azure QA and ongoing publication/rollback remain open. Terraform container
metadata diagnostics have live macOS QA; this does not establish state or plan access.

## Metadata delivery order

The [Azure adoption example](../heimdal-adoption.md) describes reader/publisher
separation, independent state-maintenance PIM, and the intended CI workflow.
It distinguishes development and planned behavior from available features.

1. [Heimdal: session metadata](https://github.com/alcxyz/bivrost/milestone/1) delivers fresh per-connection
   metadata with the existing explicit local configuration fallback.
2. [Tesseract: encrypted local configuration](https://github.com/alcxyz/bivrost/milestone/2) adds optional
   encrypted profiles after cross-platform key management is settled.

These milestones include unfinished work, not promises of release dates.
Terraform, PIM and release distribution remain separately tracked above.
