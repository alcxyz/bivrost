# Draft issue: establish a Terraform baseline

Define a small, reviewable Terraform workflow that uses normal local commands
and the user's own Azure CLI identity. Make subscription discovery visible and
keep authorization changes outside the workflow.

## Acceptance criteria

- The design documents the exact user-visible local commands and their failure
  behavior.
- Subscription discovery is read-only and never grants roles or activates
  access.
- Bivrost discovery and diagnostics perform no implicit backend migration,
  state download, state inspection, or state lock. A user-invoked Terraform
  command retains ordinary Terraform behavior, including `plan` reads and
  locks.
- The baseline exposes ordinary `kubectl` and Terraform commands without an
  extra kube enable ceremony.
- If Azure Kubernetes is unavailable, an otherwise Terraform-capable session
  remains usable, reports the limitation clearly, and never falls back to the
  ambient Kubernetes context.
- A plain command-execution lifecycle proposal is documented while command
  syntax remains deliberately unsettled.
- The workflow has documentation and focused verification for these boundaries.
