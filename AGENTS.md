# AGENTS.md

Guidance for agents and humans working in this repository.

## Project

A thin, self-hosted chat platform for a university statistics course. Students
sign in with their college email (one-time code / SSO) and chat with an AI R
tutor: paste R code and get error-spotting, ask how R functions work, and get
pointed to and explained current documentation. The AI can fetch live docs via
a `webfetch` tool; a `run_r` execution tool is planned but not yet built.

This is a deliberately different product from the earlier, opencode-based
`/Users/haris/Documents/Programming/sdp-helper` project. That codebase is kept
for reference; do not bring back its machinery here. There is **no opencode
server**, no per-student containers, no `keyproxy`. Students must never see
provider names or API keys.

## Hard invariants (do not break)

1. **The shared class API key lives only in the control plane.** It is read
   from `RC_PROVIDER_KEY` and injected as a Bearer token on each upstream call
   to `https://opencode.ai/zen/go/v1` (override the base URL with `RC_UPSTREAM`).
   The key never reaches the student-facing surface (UI or browser).
2. **Model access is locked in code.** The client forces the locked model on
   every request (`LockedModelID`); students cannot select another model.
3. **One shared upstream subscription.** Per-student control comes from the
   control plane's soft budget caps, not from upstream limits. The cost engine
   prices each interaction from token deltas against list rates synced daily.
4. **Tool execution is bounded.** The `webfetch` tool must obey timeouts, a
   body-size cap, and an optional host allowlist. When `run_r` is added, it
   must be sandboxed/limited and never allowed to damage the host.
5. **Build locally, deploy artifacts.** Nothing is compiled on the server.

## Architecture summary

- `control-plane/` — Go single static binary + SQLite. Auth (OIDC via external
  SSO with one-time codes → JWT in an httpOnly cookie), student/session/message
  storage, cost/budget engine, a direct client to the upstream chat endpoint,
  and the embedded chat UI. Admin CLI.
- `control-plane/ui/` — minimal embedded chat UI (sign-in → conversation list →
  chat), served by the control plane.
- `control-plane/session_lifecycle.go` — the conversation lifecycle policy
  (draft → commit → background title), kept out of the handlers.

### Session lifecycle (behavioral contract — do not casually change)

A session starts as a **draft** when created. It commits — becoming visible in
the sidebar — once it holds **two assistant turns**, at which point a
background, budgeted model call tries to title it (a failure leaves it
untitled; students can rename). The commit policy lives in
`advanceSession` in `session_lifecycle.go`. On login the student is restored to
their most recent session, draft or committed (`GET /api/me/sessions/current`).
Soft-delete (`deleted_at`) hides a session and rejects further sends while
keeping the row and messages for admins. Only committed, non-deleted sessions
are listed in the sidebar.

Data flow per student message: control plane loads the conversation, calls
`<upstream>/chat/completions` with the shared class key and the locked model,
runs the (bounded) tool loop for `webfetch` calls the model makes, persists the
assistant turn, and records frozen usage.

Upstream contract facts that matter when touching `goclient.go` / cost code:
- The upstream reports usage **per interaction**, so costs are priced directly
  (`RecordInteraction`), not by diffing stored snapshots.
- `deepseek-v4-flash` returns `reasoning_content` (chain-of-thought) which the
  client deliberately drops; those tokens are billed as output tokens.
- The Go rail requires the account's "contact servers in China" permission
  enabled in the opencode console.

`run_r` (future): one shared R runner container executing scripts the model
invokes, with hard timeouts/limits. The tool loop orchestration written for
`webfetch` will be reused. It should start with read-only introspection tools
(`sessionInfo`, package/export/data listing) so answers can be grounded in the
actual course environment before graduating to code execution.

## Conventions

- Go. Keep dependencies minimal (coreos/go-oidc, golang-jwt, modernc.org/sqlite,
  x/oauth2, joho/godotenv).
- No code comments unless they explain non-obvious decisions; prefer clear code
  and this documentation.
- Commit in small, reviewable increments; never commit keys, student data, or
  `.env` files.
- `RC_` env prefix for control-plane config (see `control-plane/config.go`).

## Common commands

```sh
go test ./...          # run tests (control-plane)
go build ./...         # build Go binaries locally

# control plane (single binary)
go run ./cmd/r-chat-helper serve
go run ./cmd/r-chat-helper admin add-student -email E -id ID -name NAME [-budget USD]
go run ./cmd/r-chat-helper admin set-active -email E off
go run ./cmd/r-chat-helper admin list
go run ./cmd/r-chat-helper admin sync-rates
```

## Deploy

Single small VM (Lightsail-class). Image is built in CI and published to
`ghcr.io/skiadas/r-chat-helper` (tagged `<sha>` + `latest` on push to `main`);
the server pulls via `scripts/deploy.sh` on a cron and restarts only on digest
change. `compose.yml` runs the app with SQLite on a `data` volume, and Caddy
behind the `proxy` profile terminates TLS for `rchat-helper.harisskiadas.com`.
The OIDC client must be registered on the SSO with the exact redirect URI
`https://rchat-helper.harisskiadas.com/auth/callback`. Dev login (`RC_DEV_EMAIL`)
is only active while no OIDC client secret is set.

## Status

Port (from `sdp-helper`) complete, plus substantial new surface; 25 Go tests,
`go vet` clean.

Verified live (dev mode, real key): the full `handleSend` path — compiled app +
real `RC_PROVIDER_KEY` + SQLite — with multi-turn exchanges, webfetch HTML→text
conversion, usage/cost recording, and the session lifecycle end-to-end (draft →
commit on second assistant turn, background titled, rename, soft-delete).
Rendered markdown and prompt-side conciseness + course-environment anchoring
spot-checked live.

Unit-tested: auth (cookie/disabled), OIDC flow against a fake provider,
cost/rates, the client/webfetch loop (key injection, model lock, tool loop,
allowlist, HTML-to-text), session lifecycle store semantics, markdown escaping,
and dev-login bypass.

Remaining:

- **Live SSO login** — requires registering the r-chat-helper OIDC client on
  `sso.harisskiadas.com` (exact redirect URI) and setting the secret.
- **Ship deploy** — compose/Caddy/GHCR wiring is committed but the Lightsail VM,
  cron, DNS, and .env provisioning are not yet run.
- **Streaming vs non-streaming** decision — currently the chat turn is
  non-streaming (blocking response); SSE would affect goclient + the UI.
- **run_r** — shared R runner, starting with read-only introspection tools.
- **Admin web UI** — budget/cost/session views (currently CLI only).
