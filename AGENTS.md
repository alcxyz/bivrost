# Bivrost development

Read the ADR index and applicable decisions before lasting design changes.

GitHub is canonical; Forgejo is a pull mirror.

- Branch feature work from `dev` and target feature PRs at `dev`.
- `main` is the release line. Only promote `dev` to `main`, with a new
  `VERSION`, after all checks pass.
- Squash feature PRs. Merge release promotions with a merge commit so `main`
  stays in `dev`'s history; never delete either long-lived branch.
- Never move published tags or replace published release assets.
- Keep deployment catalogues, credentials and organization-specific metadata
  outside this public repository. This covers code, test fixtures, commit
  messages, PR bodies and review comments: no names of real adopting
  organizations or their internal applications, real tenant or subscription
  IDs, or real cluster and host names. Use obvious placeholders such as
  `example.com` and patterned GUIDs like `00000000-...`. Generic terms such as
  "tenant ID" or "sandbox", Bivrost's own component names and public
  well-known service identifiers reveal nothing and need no scrubbing.
- Build `./cmd/bivrost`; run `gofmt`, relevant tests and release-script tests
  when changing packaging or CI.
