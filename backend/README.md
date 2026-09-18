# lampa-api

Go persistence service for Lampa user data (Plan 1 of
[`docs/settings-sync-backend-design.md`](../docs/settings-sync-backend-design.md)). It stores
one JSONB document per user in PostgreSQL, seals TorrServer / Jackett credentials with
AES-256-GCM, and exposes the Svtlv two-tier health contract.

User-data routes are deployed but inert: the only `Authenticator` wired in Plan 1 is
`api.DenyAll`, so every user-data request answers `401` until Keycloak arrives in Plan 2.

## Layout

```text
cmd/lampa-api/     composition root: config → pgx pool → migrations → two listeners → shutdown
pkg/api/           routes, Authenticator seam, middleware, JSON error contract
pkg/config/        typed LAMPA_API_* env config
pkg/health/        tiered checks, /health and /health/critical
pkg/storage/       document validation, credential sealing, service, postgres store, migrate
pkg/storage/pgtest testcontainers postgres:18.6 helper for DB-backed tests
migrations/        embedded goose SQL migrations (applied on startup)
```

## Configuration

| Variable | Default | Rule |
| --- | --- | --- |
| `LAMPA_API_LISTEN` | `:8080` | API listener, host:port |
| `LAMPA_API_HEALTH_LISTEN` | `:8081` | health listener, must differ from the API one |
| `LAMPA_API_DB_DSN` | — | required, never logged |
| `LAMPA_API_DATA_KEY` | — | required, base64 of exactly 32 bytes, never logged |
| `LAMPA_API_MAX_BODY_BYTES` | `2097152` | request body limit, > 0 |

Generate secrets with `openssl rand -base64 32` (data key) and `openssl rand -hex 32`
(DB password, `LAMPA_DB_PASSWORD` in Compose).

## Build, test, lint

The Windows host has no cgo toolchain, so `-race` cannot run there. Run every target inside
WSL Ubuntu (Go 1.26, `make`, `gcc`, `golangci-lint v2.13.0`):

```sh
wsl -d Ubuntu -- bash -lc 'cd /mnt/c/.../backend && make test'
```

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

`devops/docker-compose.local.yaml` adds `build: ../backend` on top of the production file.
Compose v1 interpolates each file before merging, so `LAMPA_API_IMAGE` must still be set
explicitly:

```sh
cd devops
docker network create svtlv_monitoring_external   # once
export LAMPA_API_IMAGE=lampa-api:local LAMPA_DOMAIN=localhost \
  LAMPA_DB_PASSWORD=$(openssl rand -hex 32) LAMPA_API_DATA_KEY=$(openssl rand -base64 32)
docker-compose -f docker-compose.yaml -f docker-compose.local.yaml up -d --build lampa-db lampa-api
```

Neither port is published to the host; query them from a container on the same network, e.g.
`docker exec svtlvtv_lampa_api curl -s http://localhost:8081/health`.

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

The user ID comes only from the `Authenticator`; no header, query or body can set it.

## Health

Served on `:8081`, never proxied through the public origin.

- `/health` runs all checks; `Svtlv.Monitoring.Service` polls it.
- `/health/critical` runs only critical checks; the Docker healthcheck curls it.

Response: `{status, totalDurationMs, checks[{name, status, description, durationMs, error}]}`.
Any critical failure → `Unhealthy` (503); an advisory failure → `Degraded` (200); otherwise
`Healthy` (200). The only check in Plan 1 is `database` (critical); the `keycloak` advisory
check arrives in Plan 2.

## Operational caveats

- **Back up `LAMPA_API_DATA_KEY` outside GitHub.** It seals every stored TorrServer / Jackett
  credential; losing or changing it makes them unreadable (`500 connections_unreadable`).
  Key rotation is not implemented.
- `POSTGRES_PASSWORD` applies only when the `lampa_db_data` volume is first initialized.
  To rotate `LAMPA_DB_PASSWORD`, run `ALTER ROLE lampa PASSWORD '...'` inside the database
  first, then update the secret and redeploy.
- Postgres 18+ images keep data under `/var/lib/postgresql/<major>/...`, so the volume mounts
  `/var/lib/postgresql`, not `.../data`.

## Deployment

`.github/workflows/deploy-docker.yaml` (`workflow_dispatch` only) runs race/coverage/lint
before building `ghcr.io/<repo>-api:sha-<sha>` and deploying with `docker-compose` over
Tailscale + SSH. Inputs:

| Input | Default | Meaning |
| --- | --- | --- |
| `deploy` | `true` | `false` = checks and image builds only |
| `web_image_tag` | empty | deploy an existing `sha-...` web image instead of building |
| `api_image_tag` | empty | deploy an existing `sha-...` API image, skipping backend checks |
| `api_enabled` | `true` | `false` = remove `lampa-api` / `lampa-db` (volume kept), deploy only `lampa-web` |

Rollback = dispatch with the previous `api_image_tag` / `web_image_tag`. Disable the feature
with `api_enabled: false`.
