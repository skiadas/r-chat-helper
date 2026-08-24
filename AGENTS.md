# AGENTS.md

Guidance for agents and humans working in this repository.

## Project

A thin, self-hosted AI R tutor for a university statistics course: students
sign in with their college email and chat with a tutor that spots errors in R
code, explains functions, and fetches live documentation via a `webfetch` tool.
A `run_r` execution tool is planned but not yet built. For orientation, see the
README; for invariants, read below.

## Hard invariants (do not break)

1. **The shared class API key lives only in the control plane.** Read from
   `RC_PROVIDER_KEY`; injected as a Bearer token on every upstream call to
   `https://opencode.ai/zen/go/v1` (override with `RC_UPSTREAM`). Never reaches
   the UI or browser.
2. **Model access is locked in code.** The client forces `LockedModelID` on
   every request; students cannot select another model.
3. **One shared upstream subscription.** Per-student control comes from the
   control plane's soft budget caps, not upstream limits.
4. **Tool execution is bounded.** `webfetch` obeys timeouts, a body-size cap,
   and an optional host allowlist. When `run_r` is added, it must be
   sandboxed/limited and never allowed to damage the host.
5. **Build in CI, deploy artifacts.** Nothing is compiled on the server; the
   server pulls built images.

## Architecture

- `control-plane/` — Go single static binary + SQLite (OIDC auth, storage,
  cost engine, upstream client, embedded chat UI, admin CLI).
- Session lifecycle (draft → commit on two assistant turns → background title)
  lives in `session_lifecycle.go`; treat it as a behavioral contract.
- Session growth is part of that contract: a send is refused with
  `409 session_full` once the last turn's prompt tokens reach
  `RC_SESSION_MAX_TOKENS` (default 120k); the model may signal
  `suggest_new_topic` (a suggestion only, surfaced to the UI — never an
  action); `from-summary`/`from-topic` seed fresh sessions, and a session
  created from a summary (`has_summary=1`) can never generate another
  (once-only).

## Conventions

- Go. Keep dependencies minimal: prefer the standard library for small tasks;
  use small, well-maintained libraries when warranted.
- **Never start, stop, or restart Docker/OrbStack (or any other local
  service) on the dev machine.** Other services depend on it running. If you
  need Docker and it is not running, ask the user to start it.
- Never build/run Docker locally to verify workflow or Dockerfile changes;
  CI is the source of truth for those. Local verification is `go build` /
  `go test` only.
- No code comments unless they explain non-obvious decisions; prefer clear code
  and this documentation.
- Commit in small, reviewable increments; never commit keys, student data, or
  `.env` files.
- `RC_` env prefix for control-plane config (see `control-plane/config.go`).
- Open work is tracked in `TODO.md` (kept out of git by choice).

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