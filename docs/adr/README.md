# Architecture decision records

These records describe Bivrost's public boundaries and planned directions.
Future and proposed records are planning constraints, not claims that the
described work already exists.

| ID | Decision | Status |
| --- | --- | --- |
| [0001](0001-session-ownership-and-user-configuration.md) | Session ownership and user configuration isolation | Accepted |
| [0002](0002-optional-acr-podman-only.md) | Optional ACR access through Podman only | Accepted |
| [0003](0003-catalogue-packaging-boundary.md) | Reusable public catalogue package and private downstream data | Accepted |
| [0004](0004-terraform-baseline.md) | Terraform baseline and local Azure discovery | Accepted; baseline implemented |
| [0005](0005-session-runtime-metadata.md) | Heimdal session-only runtime metadata | Accepted; initial publication and retrieval implemented (opt-in, experimental) |
| [0006](0006-pim-jit-lifecycle.md) | Short-lived PIM activation lifecycle | Accepted future direction |
| [0007](0007-release-distribution.md) | Versioned release distribution | Accepted |
| [0008](0008-tesseract-local-configuration.md) | Tesseract encrypted local configuration | Accepted future direction |
| [0009](0009-cloud-provider-boundaries.md) | Incremental cloud provider boundaries | Accepted |
| [0010](0010-managed-session-switch.md) | Managed session switching | Accepted |
| [0011](0011-go-package-layout.md) | Go package boundaries and command entry point | Accepted |
| [0012](0012-opt-in-session-publication.md) | Opt-in Kubernetes session publication | Accepted |
| [0013](0013-heimdal-adopter-lifecycle.md) | Heimdal administrator, user and CI lifecycle | Accepted future direction |
| [0014](0014-heimdal-bootstrap-reference.md) | Heimdal bootstrap reference implementation | Accepted for reference example; CLI generation and live QA pending |
| [0015](0015-authentication-browser-boundary.md) | Authentication browser selection and prompts | Accepted; live Entra and Windows WAM QA pending |
| [0016](0016-single-command-execution.md) | Single command execution in a temporary session | Accepted |
