# Handoff — r-chat-helper

Read this first. It gives incoming context, what's already built, what's
verified, and what to do next. It assumes you do **not** have the prior
conversation in memory; it is written to be self-contained.

For persistent project conventions and invariants, see `AGENTS.md`. For the
live status, check `git log` and `go test ./...`.

---

## 1. What this project is

A thin, self-hosted **AI R tutor** for a university statistics course. Students
sign in with their college email (SSO), then chat with an AI that:

- spots errors in R code they paste,
- explains how R functions/packages work,
- points them to authoritative docs (CRAN / tidyverse / rdrr.io) and explains
  them (this is the **webfetch** tool),
- and, planned but not built, **run_r**: the AI can actually *execute* R to
  verify behavior.

Students use **RStudio** for their own development; this app is purely a chat
assistant. It is NOT a code-running environment for students (yet).

## 2. Where it came from (context you must not reintroduce)

This is a deliberate, fresh reimplementation and port of an earlier project at
`/Users/haris/Documents/Programming/sdp-helper`. That project was a full
deployment that spun up **one opencode server + keyproxy sidecar per student**
(measured ~475MB RAM each). We are **leaving that behind** for this class
because this class doesn't need agentic code execution.

**Invariants carried over / changed:**

- **There is NO opencode server, NO per-student containers, NO keyproxy.**
  Do not bring that machinery (or its config files) into this repo.
- **One shared class API key lives ONLY in the control plane**, read from
  `RC_PROVIDER_KEY` and injected as a Bearer token on each upstream call to
  `https://opencode.ai/zen/go/v1` (override the rail with `RC_UPSTREAM`; the
  Zen-only base URL is `https://opencode.ai/zen/v1`). The key never reaches the
  browser/UI. There are no per-student keys — see Section 6.
- **Model access is locked in code**: the client forces `LockedModelID`
  (`deepseek-v4-flash`) on every request; students can't pick a model.
- One shared upstream subscription; per-student control comes from soft budget
  caps. The cost engine prices each interaction from token usage at list rates
  synced daily from models.dev.
- Tool execution is bounded: webfetch obeys timeout + body cap + optional host
  allowlist. run_r (future) must be sandboxed/limited.
- Build locally, deploy artifacts; nothing compiled on the server.
- `sdp-helper` is retained (unmodified) as reference only. Keep it that way.

## 3. Current state (what's built and verified)

Fresh Go module, all ported/buildable, 15 unit tests passing, `go vet` clean.

**Ported from sdp-helper (adapted):**
- `control-plane/auth.go`, `oidc.go` — one-time-code SSO (OIDC via external
  SSO) → JWT in httpOnly cookie; 12h TTL; PKCE; nonce; admin-email allowlist.
- `control-plane/cost.go`, `rates.go` — cost engine. Daily models.dev rate
  sync (`SyncRates`). `RecordInteraction` prices a single message's tokens
  directly (list rates), stores frozen `usage_events`, feeds `SpendByStudent`
  for soft budget caps.
- `control-plane/store.go`, `db.go` — SQLite: `students`, `sessions`
  (app-managed now, not opencode), `messages`, `session_snapshots`,
  `usage_events`, `model_rates`.
- `control-plane/cmd/r-chat-helper/` — admin CLI: `serve`,
  `admin add-student/set-active/set-budget/list/sync-rates`.
- `control-plane/ui/` — embedded chat UI (login → conversation list → chat).
  Server-rendered shell with a small vanilla-JS client.

**Built new (this is the interesting/risky new surface):**
- `control-plane/goclient.go` — a **stateless** OpenAI-compatible chat client
  to the upstream (`<upstream>/chat/completions`). `send()` runs a **bounded
  multi-turn tool loop** (`maxTools`, default 12): it sends the message history
  + tool definitions, and if the model requests a tool call, it executes it and
  feeds results back, looping until the model produces a final answer. It
  injects the per-student key and forces the locked model.
- `control-plane/webfetch.go` — the `webfetch` tool: server-side HTTP GET,
  timeout, body cap, optional host-suffix allowlist. Currently the ONLY tool.
- Sessions + messages are persisted in SQLite by the control plane (no external
  runtime). User turn is saved, full history sent upstream with the student's
  key, assistant turn (with any tool outputs inlined) saved.

**Config** — `control-plane/config.go`; env prefix `RC_`
(e.g. `RC_PROVIDER_KEY`, `RC_UPSTREAM`, `RC_MODEL`, `RC_OIDC_ISSUER`,
`RC_JWT_SECRET`, `RC_WEBFETCH_ALLOW`). Defaults point at the real upstream.
In dev, `control-plane/.env` is auto-loaded at startup (godotenv); env values
set in the shell win over it.

## 4. Verified vs. unverified

**Verified (unit tests):** auth (cookie + disabled-student), full OIDC flow
against a **fake** provider, cost/rates derivation, and the new client: key
injection, forced model, the webfetch tool loop (model requests fetch → content
fed back → final answer), and the host allowlist.

**NOT yet verified (critical):**
- **App-level real-key e2e.** The upstream *contract* is now verified live via
  curl on both rails: a plain message round-trips, DeepSeek V4 Flash returns
  `tool_calls` for a `webfetch` tool in OpenAI-compatible shape, and `usage`
  comes back. What has NOT been run is the full `handleSend` path — compiled
  app + real key + SQLite + the webfetch fetch loop against the live upstream.
  This remains the #1 risk and the #1 next task.
- **China-server permission.** The Go rail serves models from China-hosted
  infrastructure; the account must enable an explicit permission in the
  opencode console (the Go page) or requests fail with a permission error.
  Required before any live test on `zen/go/v1`.
- **Live SSO login** — only exercised against a fake provider. Needs the real
  client registered and a real SSO.
- **Deploy wiring** — no compose/Caddy/GHCR for THIS app yet (sdp-helper had
  them, but for the per-student-container topology; not reusable here).
- **Streaming to the browser** — the current chat turn is **non-streaming**
  (client blocks until the full turn completes, then returns it). A question
  to settle early (see Next decisions).
- **run_r** — not built.

## 5. Immediate next steps (in priority order)

1. **App-level real-key e2e (de-risks everything).** Enable the China
   permission in the console, set `RC_PROVIDER_KEY` (and `RC_UPSTREAM` if you
   want a non-default rail, plus `RC_WEBFETCH_ALLOW` to gate hosts), enroll one
   test student, start a session, and:
   - confirm a plain text message round-trips,
   - confirm a prompt that needs docs triggers a `webfetch` tool call and the
     loop completes,
   - inspect the persisted `usage_events` to sanity-check cost accounting
     against the Go usage page.
2. **Settle streaming vs non-streaming** — decide before building more of the
   chat UX whether the browser gets token streaming (add SSE) or a blocking
   result. This affects `goclient.go` (add a streaming path) and the UI.
3. **Deploy wiring** — single small Lightsail, Caddy TLS, GHCR push, compose.
   Port the *concept* (not the topology) from sdp-helper's `deploy/`. No
   keyproxy, no per-student containers, no docker runtime for students.
4. **run_r (later)** — a shared R runner container (base R + curated tidyverse
   core, ~1–2GB disk, ~300–500MB resident) + a second tool that executes a
   script and returns stdout/stderr/exit code. Reuse the existing tool-loop
   orchestration in `goclient.go`. Mandatory: hard timeout, kill, memory/pids/
   concurrency caps, output-size cap. Decide nightly: shared daemon w/
   subprocess limits vs `<docker run --rm>` per execution.
5. **Admin web UI** — student/session/cost/budget views + allowance gauge from
   `GET /zen/go/v1/usage`. (Currently: CLI only.)

## 6. Decisions/limits carried forward (so you don't re-litigate)

- **One shared control plane + one shared R runner** (when built) — memory is
  bounded by *concurrency*, not student headcount. Cheapest viable Lightsail
  tier; run_r does NOT push an extra tier by itself.
- **One shared class key** (`RC_PROVIDER_KEY`) — deliberately NOT per-student.
  There is no "Go key" vs "Zen key": workspace credentials are endpoint-agnostic;
  the endpoint (`RC_UPSTREAM`) selects the billing rail. Any later move to
  runtime, admin-switchable setups is a layered change (a `setups` table) and
  was deliberately deferred.
- **China permission.** The opencode account must enable "contact servers in
  China" (on the Go page of the console) or `zen/go/v1` rejects requests.
- **Go economics.** Go is $10/mo. DeepSeek V4 Flash's usage allotment is $30/mo
  worth of usage (a 3x multiplier, not 6x) plus a $12/5-hour window, and there
  is peak/off-peak pricing (peak: 01:00–04:00 and 06:00–10:00 UTC). The
  account-level "Use balance" option makes Go fall back to Zen credits after
  the allotment. The cost engine's list-rate pricing is a per-student proxy,
  not the provider's bill.
- **Reasoning model.** `deepseek-v4-flash` returns `reasoning_content`
  (chain-of-thought) in the message; the client intentionally drops it.
  Reasoning tokens are billed as output tokens, so the cost engine already
  accounts for them.
- **webfetch allowlist** default is EMPTY = allow any host. Decide whether to
  restrict to CRAN/tidyverse/rdrr.io for the class (`RC_WEBFETCH_ALLOW`).
- **Coding class is a separate concern** — handled elsewhere (opencode
  workspaces). Not this repo's job.
- **No code comments unless non-obvious**; keep deps minimal; small commits;
  never commit keys/.env/student data.

## 7. Repo layout

```
AGENTS.md                  project invariants + conventions + status
control-plane/             Go module (module github.com/haris/r-chat-helper/control-plane)
  config.go                RC_ env config
  app.go                   App wiring (config, db, oidc, client)
  auth.go, oidc.go         SSO login + JWT
  cost.go, rates.go        cost engine + models.dev sync
  store.go, db.go          SQLite storage
  goclient.go              upstream chat client + tool loop  (NEW, core)
  webfetch.go              webfetch tool (NEW)
  server.go                HTTP handlers / routes
  ui.go + ui/index.html    embedded chat UI
  cmd/r-chat-helper/       serve + admin CLI
```

## 8. Run it

```sh
cd control-plane && go test ./... && go build ./...
cp .env.example .env    # fill in RC_PROVIDER_KEY; auto-loaded at startup
go run ./cmd/r-chat-helper serve
go run ./cmd/r-chat-helper admin add-student -email E -id ID -name NAME
```
