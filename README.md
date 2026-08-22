# r-chat-helper

A thin, self-hosted AI R tutor for a university statistics course. Students
sign in with their college email (one-time-code SSO) and chat with a tutor
that spots errors in R code, explains R functions and packages, and fetches
live documentation via a `webfetch` tool. A `run_r` execution tool is planned
but not yet built.

This is a deliberately different product from the earlier, opencode-based
`sdp-helper` project: no opencode server, no per-student containers, no
keyproxy. One shared class API key lives in the control plane; model access is
locked in code; per-student control comes from soft budget caps.

For agents working in this repo, see `AGENTS.md`.

## Run it (dev)

```sh
cd control-plane && go test ./... && go build ./...
cp .env.example .env    # fill in RC_PROVIDER_KEY; auto-loaded at startup
go run ./cmd/r-chat-helper serve
go run ./cmd/r-chat-helper admin add-student -email E -id ID -name NAME
```

Dev mode without SSO: set `RC_DEV_EMAIL` in `.env` (plus
`RC_COOKIE_SECURE=false` for plain http); clicking "Sign in" logs in as that
identity. The email must be enrolled via `admin add-student` (or listed in
`RC_ADMIN_EMAILS` for the admin role). Dev login is only active while no OIDC
client secret is set, so it can never bypass real SSO in a deployed setup.

## Deploy

A single small VM (Lightsail-class). The image is built in CI and published to
`ghcr.io/skiadas/r-chat-helper` (tagged `<sha>` + `latest` on push to `main`);
the server pulls via `scripts/deploy.sh` on a cron and restarts only on digest
change. `compose.yml` runs the app with SQLite on a `data` volume, and Caddy
behind the `proxy` profile terminates TLS for `rchat-helper.harisskiadas.com`.

The OIDC client must be registered on the SSO with the exact redirect URI
`https://rchat-helper.harisskiadas.com/auth/callback`. See `.env.example` for
the full server environment, and `TODO.md` for what's still pending before
the first real deployment.