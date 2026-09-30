# lampa-api

Go service for Lampa accounts and user data (Plans 1 and 2 of
[`docs/settings-sync-backend-design.md`](../docs/settings-sync-backend-design.md)). It signs
users in with the Svtlv Keycloak realm, keeps their session in a sealed cookie, stores one
JSONB document per user in PostgreSQL, seals TorrServer, Jackett and Prowlarr credentials with
AES-256-GCM, and exposes the Svtlv two-tier health contract.

The API is reached only through `lampa-web`'s same-origin `/api/v1/` proxy; its port is not
published. No client calls the user-data routes yet (Plan 4); the `svtlv/account.js` add-on
uses only the session and auth routes.

## Layout

```text
cmd/lampa-api/     composition root: config → pgx pool → migrations → auth → two listeners → shutdown
pkg/api/           routes, Authenticator seam, middleware (CSRF included), JSON error contract
pkg/auth/          cookie sealing, session cookie, Keycloak client, auth handlers, Authenticator
pkg/config/        layered appsettings files (embedded) with {ENV_VAR} placeholders
pkg/health/        tiered checks, /health and /health/critical
pkg/storage/       document validation, credential sealing, service, postgres store, migrate
pkg/storage/pgtest testcontainers postgres:18.6 helper for DB-backed tests
migrations/        embedded goose SQL migrations (applied on startup)
```

## Configuration

Settings come from JSON files embedded in the binary (`pkg/config/defaults/`), layered the
.NET way, as in Svtlv:

1. `appsettings.json` holds the shared defaults;
2. `appsettings.<Environment>.json`, when present, overrides only what differs. Test and
   Production point the DB at `lampa-db:5432`; Development has no file and uses the base one.

Both layers decode strictly into one struct, so an unknown or misspelled key is an error and a
key the environment file leaves out keeps its base value. There are no other env overrides;
the files are the single source of non-secret settings.

| Key | Default | Rule |
| --- | --- | --- |
| `Api.Listen` | `:5800` | API listener, host:port |
| `Api.MaxBodyBytes` | `2097152` | request body limit, > 0 |
| `Health.Listen` | `:8081` | health listener, must differ from the API one |
| `Database.Host` / `Port` | `localhost` / `5434` | `lampa-db` / `5432` in Test and Production |
| `Database.Name` / `User` | `lampa` / `lampa` | required |
| `Database.Password` | `{LAMPA_DB_PASSWORD}` | required, never logged |
| `Database.SSLMode` | `disable` | a libpq `sslmode` |
| `DataKey` | `{LAMPA_API_DATA_KEY}` | required, base64 of exactly 32 bytes, never logged |
| `Authentication.PublicURL` | `http://localhost:8092` | the origin browsers load Lampa from; Production uses `http://svtlv:8092` (no trailing slash) |
| `Authentication.Keycloak.Authority` | `https://svtlv.fly.dev/realms/svtlv-test` | Test uses the same realm; Production uses `https://svtlv.fly.dev/realms/svtlv` |
| `Authentication.Keycloak.ClientId` | `svtlv-lampa` | required |
| `Authentication.Keycloak.ClientSecret` | `{LAMPA_KEYCLOAK_CLIENT_SECRET}` | required, never logged |
| `Authentication.Keycloak.RequireHttpsMetadata` | `false` | Production sets `true`, requiring an HTTPS Authority |

`Authentication.PublicURL` gives the login redirect URI (`<PublicURL>/api/v1/auth/callback`), the only
`Origin` accepted on state-changing requests, and whether cookies are `Secure` (only for
`https`). It must equal the address saved on devices, or every POST fails the Origin check.
The current Test origin is for local Compose. A server deploy with `environment=Test` needs
its own reachable Test origin in `appsettings.Test.json` before the deploy guard will pass.

The environment comes from `LAMPA_ENVIRONMENT`:

| Environment | Where it runs |
| --- | --- |
| `Test` (default when unset) | containers from `devops/docker-compose.yaml` |
| `Production` | containers; only the deploy workflow's `environment` input selects it |
| `Development` | a local `go run` against the DB published on `localhost:5434`; inside a container it cannot reach the DB |

Secrets are `{ENV_VAR}` placeholders, resolved from the environment in a fixed order
(`Database.Password`, `DataKey`, `Authentication.Keycloak.ClientSecret`); only
those three keys are resolved, so a new placeholder needs its own entry in `config.Load`.
The public URL and Keycloak Authority are checked-in settings selected by `LAMPA_ENVIRONMENT`.
An unset or empty secret variable
fails startup with a message naming the variable and the key path, never the value. The
startup log prints the environment and redacts the DSN, the data key and the client secret.

Generate secrets with `openssl rand -hex 32` (`LAMPA_DB_PASSWORD`) and
`openssl rand -base64 32` (`LAMPA_API_DATA_KEY`).

### Sealed settings

`storage.SensitiveSettings` mirrors the secret inputs of the Lampa settings UI:
`torrserver_login`, `torrserver_password`, `jackett_key`, `jackett_key_two`, `prowlarr_key`
and `prowlarr_key_two`. They are removed from the `settings` section, sealed with AES-256-GCM
(bound to the user ID) into `encrypted_connections`, and merged back on read. Settings that
plugins register through `SettingsApi` are unknown to the backend and are stored as plain
settings.

`TestSensitiveSettings_matchUI` parses `../app.min.js` and fails when the UI gains a settings
input that is neither in `SensitiveSettings` nor in the test's list of plain inputs, or loses a
sealed one. An upstream pull that adds a settings input therefore fails CI until it is
classified as sealed or plain. The test
skips when `app.min.js` is absent (a `backend/`-only checkout).

## Build, test, lint

For CI-equivalent checks, use the default `Makefile` inside WSL Ubuntu (Go 1.26, `make`,
`gcc`, `golangci-lint v2.13.0`). The Windows host has no cgo toolchain, so `-race` cannot
run there:

```sh
wsl -d Ubuntu -- bash -lc 'cd /mnt/c/.../backend && make test'
```

For a native Windows build or non-race test run, use `WMakefile` from the repository root:

```powershell
make.exe -C ./backend -f WMakefile build
make.exe -C ./backend -f WMakefile test
```

`WMakefile` uses `cmd.exe` syntax and produces `.bin/lampa-api.exe`. Its `race` target exits
with a reminder to use WSL.

| Target | What it does |
| --- | --- |
| `make test` | race + coverage over all packages, prints coverage excluding `mocks/` |
| `make race` | race tests with a 300 s timeout |
| `make lint` | `golangci-lint run` |
| `make fmt` | `gofmt -s` + `goimports` (vendor and mocks skipped) |
| `make build` | `.bin/lampa-api` with `-X main.revision` |
| `make generate` | regenerate moq mocks |

DB-backed tests start `postgres:18.6` through testcontainers and need a running Docker engine.
Without Docker they skip; with `LAMPA_API_REQUIRE_DOCKER=1` (set in CI) they fail instead, so
they can never silently skip there. Dependencies are vendored: after changing them run
`go mod tidy && go mod vendor`.

WSL `git` cannot resolve this worktree's Windows `.git` path, so `make build` in WSL reports
`REV=latest`; CI and the Docker build are unaffected.

## Local run

`devops/docker-compose.yaml` builds both images locally (`lampa-web:dev`, `lampa-api:dev`) and
reads `devops/.env` (gitignored). Test is the default environment:

```sh
cd devops
cp .env.example .env                               # then replace the placeholder secrets
docker network create svtlv_monitoring_external    # once
docker compose -p lampa up -d --build
```

The API is not published: reach it through the web container's proxy, for example
`curl http://localhost:8092/api/v1/session` → `{"authenticated":false}`. Health stays
container-internal: `docker exec svtlvtv_lampa_api curl -s http://localhost:8081/health`.

For Development, start only the DB and run the binary on the host; the base settings already
point at `localhost:5434` (published on loopback only):

```sh
cd devops && docker compose -p lampa up -d lampa-db
cd ../backend
LAMPA_ENVIRONMENT=Development LAMPA_DB_PASSWORD=... LAMPA_API_DATA_KEY=... \
  LAMPA_KEYCLOAK_CLIENT_SECRET=... go run ./cmd/lampa-api
```

### Testing sign-in locally

The service starts without Keycloak: discovery is lazy, so a missing Keycloak only makes
`/health` `Degraded`, device login answer `503 keycloak_unavailable` and browser login redirect
to `/#svtlv-login=failed`. `/api/v1/session` still answers.

- Automated tests never need Keycloak: `pkg/auth` and `cmd/lampa-api` run the real flows
  against an `httptest` fake (discovery, JWKS signed with a test RSA key, device-authorization,
  token and introspection endpoints).
- For a real login, configure the `svtlv-lampa` client in the selected Keycloak realm with
  the redirect URI `<Authentication.PublicURL>/api/v1/auth/callback`. The Authority must equal
  the `iss` Keycloak issues. To use a different realm locally, edit the appsettings file and
  rebuild the API; environment variables do not override non-secret settings.

## Authentication

Two login flows end in the same session:

- **TV** (OAuth 2.0 Device Authorization Grant): `device/start` returns a user code and a
  verification URL to open on a phone. The device code never leaves the server; it is sealed
  into the `lampa_device` cookie (Path `/api/v1/auth/device`). The TV polls `device/poll` at
  the returned `interval`, one token request per poll.
- **Phone and computer** (Authorization Code + PKCE): `auth/login?return=<path>` seals the
  state, nonce, verifier and return path into `lampa_login` (Path `/api/v1/auth/callback`,
  10 minutes) and redirects to Keycloak. The callback redirects to the return path with
  `#svtlv-login=ok`, or to `/#svtlv-login=failed` on any failure, never to a JSON page. Only a
  local absolute return path is kept; anything else becomes `/`.

### Sessions

Sessions are stateless: no session table, no purge job, no database access. The session is the
`lampa_session` cookie (`Path=/api; HttpOnly; SameSite=Lax`, plus `Secure` on `https`),
AES-256-GCM sealed JSON holding the profile copied from the verified ID token (`sub`, `name`,
`email`, `picture`), the Keycloak refresh token and the session times. Cookie keys are derived
from `LAMPA_API_DATA_KEY` with HKDF, one per purpose label (`lampa-session-v1`,
`lampa-login-v1`, `lampa-device-v1`); there is no separate cookie secret.

- Lifetimes are constants in `pkg/auth`: 30 days idle, 180 days absolute. Only
  `GET /api/v1/session` slides the idle expiry; the add-on calls it at every start and every
  12 hours.
- The whole `Set-Cookie` stays ≤ 4000 bytes: `name` and `email` are length-limited and an
  over-long `picture` is dropped, but the refresh token never is (a login whose cookie cannot
  fit fails).
- **Revoke every session**: bump the purpose label to `lampa-session-v2` in code and deploy.
  Never rotate `LAMPA_API_DATA_KEY` for this; it also seals stored credentials.

### Revalidation against Keycloak

Every authenticated request, `/session` included, makes one Keycloak call with the session's
refresh token: a refresh grant when due (after `min(24h, half the refresh token's lifetime)`),
otherwise introspection. A session ended in Keycloak, or a user disabled or deleted there, is
signed out on the next request (cookie cleared, `401`, or an anonymous `/session`).

Keycloak failures **fail open**: on a timeout (3 s), a network error or an unexpected answer
the session keeps working and a `[WARN]` is logged without token values; only new logins
break. Accepted costs:

- while Keycloak is slow or down, each authenticated request waits up to the 3-second timeout;
- a user disabled while Keycloak is unreachable keeps access until it answers again;
- logout clears only this device's cookie and leaves the Keycloak session alone (a phone login
  shares its browser's SSO session with other Svtlv apps), so a copied cookie stays valid until
  that Keycloak session ends or the cookie expires. The device itself cannot be signed back in by
  a request still in flight (another tab) renewing its cookie after the logout: logout also sets
  `lampa_logout` (the logout time, `Max-Age` = the absolute timeout), and a session created up to
  that time is refused; the next login deletes it.

### 401 vs 503

On protected routes, no session, an expired or tampered cookie, or a session Keycloak revoked
→ `401 unauthenticated`. Any other `Authenticator` failure → `503 session_unavailable`, so a
client is never told it is signed out because a dependency failed. The add-on treats `503` and
network errors as "service unavailable" and keeps its state.

### CSRF

Every request other than `GET`, `HEAD` and `OPTIONS` needs `X-Lampa-Csrf: 1` and, when it has
an `Origin`, exactly `Auth.PublicURL`; otherwise it gets `403 csrf_rejected` before any handler
runs. An absent `Origin` is allowed (old TV WebViews omit it on same-origin requests). No CORS
headers are ever sent.

### Keycloak prerequisites

In realm `svtlv`, the confidential client `svtlv-lampa`: Standard flow and OAuth 2.0 Device
Authorization Grant on, direct access grants off, PKCE `S256` required, redirect URI
`<Authentication.PublicURL>/api/v1/auth/callback`, no web origins, no `offline_access` default scope,
and optionally a `picture` user-attribute mapper for avatars. Revalidation also relies on realm
settings shared with Svtlv, which revalidates the same way: SSO Session Idle ≥ 31 days, SSO
Session Max ≥ 180 days, the client's session idle/max unset or no shorter, and **Revoke Refresh
Token off**.

The configured issuer must equal the `iss` Keycloak issues, every endpoint its discovery
document advertises (token, device authorization, JWKS, introspection) must be reachable from
the `lampa-api` container, and the authorization and verification pages must open on users'
phones and browsers.

## API

| Request | Success | Errors |
| --- | --- | --- |
| `GET /api/v1/session` | `200` `{"authenticated":false}` or `{"authenticated":true,"user":{id,name,email,picture}}` | none: a revoked or invalid session is anonymous |
| `GET /api/v1/auth/login?return=<path>` | `302` to Keycloak | `302 /#svtlv-login=failed` |
| `GET /api/v1/auth/callback` | `302 <return>#svtlv-login=ok` + session cookie | `302 /#svtlv-login=failed` |
| `POST /api/v1/auth/device/start` | `200` `{user_code, verification_uri, verification_uri_complete, expires_in, interval}` | `503 keycloak_unavailable` |
| `POST /api/v1/auth/device/poll` | `202` `{status, interval}` (`pending` or `slow_down`); `200` signed-in body + session cookie | `400 no_device_login`, `403 access_denied`, `410 expired`, `500 login_failed`, `503 keycloak_unavailable` |
| `POST /api/v1/auth/logout` | `204`, cookie cleared, `lampa_logout` set | — |
| `GET /api/v1/user-data` | `200` `{schema_version, data, updated_at}` | `401`, `404 user_data_not_found`, `500 connections_unreadable`, `503 storage_unavailable` / `session_unavailable` |
| `PUT /api/v1/user-data` `{schema_version, data}` | `200` stored document | `400 invalid_document` / `unsupported_schema_version` / `invalid_json`, `401`, `413 request_too_large`, `415 unsupported_media_type`, `503` |
| `DELETE /api/v1/user-data` | `204`, idempotent | `401`, `503` |

Every POST, PUT and DELETE can also answer `403 csrf_rejected` (see [CSRF](#csrf)). `HEAD`
never starts a login or slides a session.

Other methods get `405 method_not_allowed` with `Allow`, other paths `404 not_found`, panics
`500 internal_error`. Errors are `{"error": {"code": "...", "message": "..."}}` with short,
generic messages.

`data` holds exactly the sections `settings, favorites, bookmarks, scores, subscriptions,
progress, history, other` (missing ones are stored as `{}`), each a JSON object; only
`schema_version` 1 is accepted. Sections are capped at 1 MiB and nesting at depth 32.

A value PostgreSQL still refuses to store (a SQLSTATE class 22 data exception, for example a
number outside jsonb's numeric range) is also `400 invalid_document`, not `503`. Every request
runs under a 10 s deadline, so a stalled database answers `503 storage_unavailable` before the
15 s write timeout drops the connection.

The user ID comes only from the `Authenticator` (the session's Keycloak `sub`); no header,
query or body can set it.

## Health

Served on `:8081`, container-internal (never published, Svtlv convention) and never proxied
through the public origin: `lampa-web` proxies only `/api/v1/`.

- `/health` runs all checks; `Svtlv.Monitoring.Service` polls it.
- `/health/critical` runs only critical checks; the Docker healthcheck curls it.

Response: `{status, totalDurationMs, checks[{name, status, description, durationMs, error}]}`.
Any critical failure → `Unhealthy` (503); an advisory failure → `Degraded` (200); otherwise
`Healthy` (200). Checks: `database` (critical) and `keycloak` (advisory). `keycloak` passes
only on a `200` discovery document whose `issuer` equals `Auth.Issuer` (redirects not followed,
64 KiB cap), so a wrong issuer shows as `Degraded` before any login fails. Sessions, `/session`
and logout work with either dependency down; user data needs the database.

## Operational caveats

- **Back up `LAMPA_API_DATA_KEY` outside GitHub.** It seals every stored TorrServer, Jackett
  and Prowlarr credential; losing or changing it makes them unreadable (`500 connections_unreadable`).
  Key rotation is not implemented.
- `POSTGRES_PASSWORD` applies only when the `lampa_db_data` volume is first initialized.
  To rotate `LAMPA_DB_PASSWORD`, run `ALTER ROLE lampa PASSWORD '...'` inside the database
  first, then update the secret and redeploy.
- Postgres 18+ images keep data under `/var/lib/postgresql/<major>/...`, so the volume mounts
  `/var/lib/postgresql`, not `.../data`.

## CI and deployment

`.github/workflows/tests.yaml` runs lint, `make test` and `make race` (with
`LAMPA_API_REQUIRE_DOCKER=1`), a compose `config` check against `devops/.env.example` and an
ES5 syntax parse of the `svtlv/*.js` add-ons (pinned `acorn@8.14.0 --ecma5`) on every PR and on
pushes to `svtlvtv`.

`.github/workflows/deploy-docker.yaml` (`workflow_dispatch` only, default branch only) has one
input, `environment` (Development / Test / Production, default Production), written into the
server `.env` as `LAMPA_ENVIRONMENT`; Development fails the `prepare` job, since it only works
for a local `go run`. The public URL and Keycloak Authority come from the selected appsettings
files. `LAMPA_KEYCLOAK_CLIENT_SECRET` remains a required secret. With an `http://` public URL,
`LAMPA_BIND_ADDRESS` must be a Tailscale address; it must equal the URL host when that host is
an IP address. The `svtlv` hostname is also accepted. It builds `ghcr.io/<owner>/lampa-web` and
`ghcr.io/<owner>/lampa-api` (branch, `<branch>-<sha>` and `latest` tags), swaps the `:dev`
images for `:latest`, strips the `build:` blocks with `sed`, and deploys over Tailscale + SSH,
waiting up to 3 minutes for `svtlvtv_lampa_api` to be healthy. It does not run the tests
(same as Svtlv); `tests.yaml` is the gate.

There are no rollback inputs: roll back by reverting the commit and dispatching again.
