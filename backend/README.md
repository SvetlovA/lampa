# lampa-api

Go persistence service for Lampa user data (Plan 1 of
[`docs/settings-sync-backend-design.md`](../docs/settings-sync-backend-design.md)). It stores
one JSONB document per user in PostgreSQL, seals TorrServer, Jackett and Prowlarr credentials
with AES-256-GCM, and exposes the Svtlv two-tier health contract.

User-data routes are deployed but inert: the only `Authenticator` wired in Plan 1 is
`api.DenyAll`, so every user-data request answers `401` until Keycloak arrives in Plan 2.

## Layout

```text
cmd/lampa-api/     composition root: config → pgx pool → migrations → two listeners → shutdown
pkg/api/           routes, Authenticator seam, middleware, JSON error contract
pkg/config/        layered appsettings files (embedded) with {ENV_VAR} secret placeholders
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

The environment comes from `LAMPA_ENVIRONMENT`:

| Environment | Where it runs |
| --- | --- |
| `Test` (default when unset) | containers from `devops/docker-compose.yaml` |
| `Production` | containers; only the deploy workflow's `environment` input selects it |
| `Development` | a local `go run` against the DB published on `localhost:5434`; inside a container it cannot reach the DB |

Secrets are `{ENV_VAR}` placeholders, resolved from the environment in a fixed order
(`Database.Password`, then `DataKey`). An unset or empty variable fails startup with a message
naming the variable and the key path, never the value. The startup log prints the environment
and redacts the DSN and the data key.

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
docker-compose up -d --build
```

The API is published on `localhost:5800` (`LAMPA_API_PORT`) and answers `401` on user-data
routes. Health stays container-internal:
`docker exec svtlvtv_lampa_api curl -s http://localhost:8081/health`.

For Development, start only the DB and run the binary on the host; the base settings already
point at `localhost:5434` (`LAMPA_DB_PORT`, published on loopback only):

```sh
cd devops && docker-compose up -d lampa-db
cd ../backend
LAMPA_ENVIRONMENT=Development LAMPA_DB_PASSWORD=... LAMPA_API_DATA_KEY=... go run ./cmd/lampa-api
```

## API

| Request | Success | Errors |
| --- | --- | --- |
| `GET /api/v1/user-data` | `200` `{schema_version, data, updated_at}` | `401`, `404 user_data_not_found`, `500 connections_unreadable`, `503 storage_unavailable` |
| `PUT /api/v1/user-data` `{schema_version, data}` | `200` stored document | `400 invalid_document` / `unsupported_schema_version` / `invalid_json`, `401`, `413 request_too_large`, `415 unsupported_media_type`, `503` |
| `DELETE /api/v1/user-data` | `204`, idempotent | `401`, `503` |

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

The user ID comes only from the `Authenticator`; no header, query or body can set it.

## Health

Served on `:8081`, container-internal (never published, Svtlv convention) and never proxied
through the public origin.

- `/health` runs all checks; `Svtlv.Monitoring.Service` polls it.
- `/health/critical` runs only critical checks; the Docker healthcheck curls it.

Response: `{status, totalDurationMs, checks[{name, status, description, durationMs, error}]}`.
Any critical failure → `Unhealthy` (503); an advisory failure → `Degraded` (200); otherwise
`Healthy` (200). The only check in Plan 1 is `database` (critical); the `keycloak` advisory
check arrives in Plan 2.

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
`LAMPA_API_REQUIRE_DOCKER=1`) plus a compose `config` check against `devops/.env.example` on
every PR and on pushes to `svtlvtv`.

`.github/workflows/deploy-docker.yaml` (`workflow_dispatch` only, default branch only) has one
input, `environment` (Development / Test / Production, default Production), written into the
server `.env` as `LAMPA_ENVIRONMENT`; Development fails the `prepare` job, since it only works
for a local `go run`. It builds `ghcr.io/<owner>/lampa-web` and
`ghcr.io/<owner>/lampa-api` (branch, `<branch>-<sha>` and `latest` tags), swaps the `:dev`
images for `:latest`, strips the `build:` blocks with `sed`, and deploys over Tailscale + SSH,
waiting up to 3 minutes for `svtlvtv_lampa_api` to be healthy. It does not run the tests
(same as Svtlv); `tests.yaml` is the gate.

There are no rollback inputs: roll back by reverting the commit and dispatching again.
