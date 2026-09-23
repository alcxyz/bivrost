# Bivrost development

Read the ADR index and applicable decisions before lasting design changes.

GitHub is canonical; Forgejo is a pull mirror.

- Branch feature work from `dev` and target feature PRs at `dev`.
- `main` is the release line. Only promote `dev` to `main`, with a new
  `VERSION`, after all checks pass.
- Squash feature PRs. Merge release promotions with a merge commit and
  synchronize `main` back into `dev`; never delete either long-lived branch.
- Never move published tags or replace published release assets.
- Keep deployment catalogues, credentials and organization-specific metadata
  outside this public repository.
- Build `./cmd/bivrost`; run `gofmt`, relevant tests and release-script tests
  when changing packaging or CI.
