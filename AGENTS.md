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

Data flow per student message: control plane loads the conversation, calls
`<upstream>/chat/completions` with the shared class key and the locked model,
runs the (bounded) tool loop for `webfetch` calls the model makes, persists the
assistant turn, and records frozen usage.

`run_r` (future): one shared R runner container executing scripts the model
invokes, with hard timeouts/limits. The tool loop orchestration written for
`webfetch` will be reused.

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

## Status

Phase 1 (this repo's start) — the port is done:

- Ported from `sdp-helper`: OIDC auth w/ one-time codes + JWT cookies; the
  cost/budget engine (token-diff pricing from the daily models.dev rate sync,
  per-student soft caps); the student/session/usage storage; the admin CLI; and
  the embedded chat UI.
- Built new: a stateless Go client to the upstream chat endpoint that injects
  the configured class key and forces the locked model, with a bounded
  multi-turn tool loop supporting the `webfetch` tool (timeout + body cap +
  optional host allowlist). Sessions and chat messages are now stored in SQLite
  by the control plane (no external agent runtime).
- Unit-tested: auth (cookie/disabled), OIDC flow against a fake provider,
  cost/rates, and the new client/webfetch loop (key injection, model lock,
  tool loop, allowlist).

Remaining:

- Deploy wiring (compose + Caddy + GHCR) for this single-VM app.
- App-level real-key e2e: the upstream contract (path, Bearer auth, `tool_calls`
  shape, usage fields) was verified live via curl on both `zen/v1` and
  `zen/go/v1`, but the full `handleSend` path against the live upstream with a
  real key is still exercising. Note: the Go rail requires the account's
  "contact servers in China" permission enabled in the opencode console.
- `run_r`: a shared R runner + the model-invoked execution tool, reusing the
  webfetch tool-loop orchestration with a sandbox/limits.
- Live SSO login once the client is registered; admin web UI (budget/cost
  views).
