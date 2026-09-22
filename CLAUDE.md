# scrumboy

Fork of `markrai/scrumboy` (Go backend + web/mobile clients). PRs go upstream: work in a
`scrumboy-wt-*` worktree branched from `upstream/main` (remote `upstream` = `markrai/scrumboy`,
`origin` = `jordanfelle/scrumboy`), open the PR against `markrai/scrumboy`. Every commit needs a
DCO `Signed-off-by` trailer (`git commit -s`) — upstream CI runs a required `dco` check. Never
include a Claude session link or "Generated with Claude Code" footer in commits/PRs here — this is
a public repo (see `feedback_no_session_links_public_repos.md` in auto-memory).

This file itself lives only on `origin/main` (our fork) — it is not part of any upstream PR diff,
since it's our own tooling notes, not something to hand `markrai/scrumboy` in a PR.

See `project_scrumboy.md` in auto-memory for the broader workflow (periodic restart needs, etc.).

## Feature → file pointers

- **Service-flagged API tokens** (shutterpaws-tech#2343, PR branch `service-account-tokens`) —
  `POST /api/me/tokens` accepts an optional `isService` bool (`internal/httpapi/routing_me.go`,
  `internal/httpapi/json.go`). On `DeleteUser` (`internal/store/auth.go`), the departing user's
  active service tokens are reassigned to the requesting owner **and revoked in the same step**
  (`reassignServiceAPITokens` in `internal/store/apitokens.go`) — the audit record survives
  offboarding, but the secret dies with its original holder (reassigning without revoking would
  let the old owner authenticate as the new one). Migration `072_add_api_token_service_flag.sql`.
