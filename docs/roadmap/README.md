# Roadmap

GitHub issues track future work and validation. Accepted architecture decisions
live in the [ADR index](../adr/README.md); an accepted direction does not mean
its implementation is complete.

- [#1: define the plain command-execution lifecycle](https://github.com/alcxyz/bivrost/issues/1)
- [#2: add short-lived PIM JIT lifecycle](https://github.com/alcxyz/bivrost/issues/2)
- [#3: publish reproducible releases](https://github.com/alcxyz/bivrost/issues/3)
- [#4: add Heimdal session runtime metadata](https://github.com/alcxyz/bivrost/issues/4)
- [#5: establish a Terraform baseline](https://github.com/alcxyz/bivrost/issues/5)
- [#6: complete Windows native Podman Machine QA](https://github.com/alcxyz/bivrost/issues/6)
- [#8: add Tesseract encrypted local configuration](https://github.com/alcxyz/bivrost/issues/8)

## Metadata delivery order

1. [Heimdal: session metadata](https://github.com/alcxyz/bivrost/milestone/1) delivers fresh per-connection
   metadata with the existing explicit local configuration fallback.
2. [Tesseract: encrypted local configuration](https://github.com/alcxyz/bivrost/milestone/2) adds optional
   encrypted profiles after cross-platform key management is settled.

These milestones describe future work, not released features or promised dates.
Terraform, PIM and release distribution remain separately tracked above.
