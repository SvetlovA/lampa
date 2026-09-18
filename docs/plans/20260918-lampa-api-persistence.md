# Plan 1: Build and Deploy the Go Persistence Service (lampa-api)

## Overview
- Implements roadmap **Plan 1** from `docs/settings-sync-backend-design.md` §14: an isolated Go
  service (`backend/`) that stores one JSONB document per user in PostgreSQL, with read /
  replace / delete, validation, encryption of connection credentials, and the Svtlv two-tier
  health contract (`:8081/health`, `:8081/health/critical`).
- Ships end to end in this plan: multi-stage image, Docker Compose services (`lampa-api`,
  `lampa-db`), Ralphex-style race/coverage/lint gates in the manual CI/CD workflow, the Docker
  critical healthcheck, deployment, a feature-disable switch and a verified rollback.
- Keycloak authentication is **not** part of this plan (Plan 2). User-data routes are deployed
  behind an `Authenticator` seam whose Plan 1 implementation denies everything: routes always
  answer `401` in production until Plan 2 plugs in `pkg/auth`. Tests inject a fake
  authenticator.
- No frontend change: `app.min.js`, `css/app.css`, `lang/*` and `index.html` are untouched.
  `lampa-web` keeps working exactly as today and does not depend on the new services.
- **Plan 1 is done only after the Post-Completion production deploy, monitoring registration
  and rollback drill succeed** (design §14 "Result").

## Context (from discovery)
- **Repo**: distribution fork of Lampa, no build system; static files served by
  `httpd:alpine3.15` (`Dockerfile`, `COPY . htdocs/`).
- **Deployment**: `devops/docker-compose.yaml` (single `lampa-web` service, external network
  `svtlv_monitoring_external`); `.github/workflows/deploy-docker.yaml` is `workflow_dispatch`
  only, builds `ghcr.io/<repo>:sha-<sha>`, copies **only** `docker-compose.yaml` + `.env` to
  `${DEPLOY_DIR}`, deploys via Tailscale + SSH with `docker-compose` (v1 CLI) on the server.
- **Ralphex baseline** re-checked 2026-09-18: `master` = `a736d5e` (unchanged from design).
  Local reference checkout is registered in Orca as repo `ralphex` →
  `C:/Users/21art/Projects/ralphex`; read its code, `CLAUDE.md` ("Code Style", "Before
  Submitting a PR") and `CONTRIBUTING.md` when in doubt about an idiom. Baseline: `go 1.26.0`,
  `testify v1.12.1`, `golangci-lint v2.13.0` via `golangci/golangci-lint-action@v9`,
  `actions/setup-go@v7` (`go-version: "1.26"`), `actions/checkout@v7`; Makefile targets
  `build/test/lint/fmt/race`; image `ghcr.io/umputun/baseimage/buildgo` →
  `ghcr.io/umputun/baseimage/app` (latest release `v1.21.1`).
- **Local toolchain**: Windows host has Go 1.26.0 but `CGO_ENABLED=0` and no gcc, so `-race`
  (inside `make test`/`make race`) cannot run there. WSL `Ubuntu` is installed and Docker Desktop
  is available → all `make` targets run inside WSL Ubuntu (see Task 1).
- **Lampa credential keys** (from `app.min.js`): `torrserver_login`, `torrserver_password`,
  `jackett_key`, `jackett_key_two`.
- **Chosen dependency versions** (latest at planning time): `github.com/jackc/pgx/v5 v5.11.0`,
  `github.com/pressly/goose/v3 v3.28.0`, `github.com/testcontainers/testcontainers-go v0.44.0`
  (+ `modules/postgres`), `github.com/stretchr/testify v1.12.1`; tools `moq`, `goimports` as
  `go tool` dependencies.
- **PostgreSQL**: latest stable `postgres:18.6` (19 is only `19beta3`), used both in
  Compose and in testcontainers so tests and production run the same version. Debian (glibc)
  variant, not Alpine: matches the Svtlv stack (`postgres:17`), avoids musl locale/collation
  quirks; the ~40 MB size saving is irrelevant on the server. Exact patch pinned so the DB only
  changes via a commit; bump manually after reading release notes.

## Development Approach
- **testing approach**: Regular (code first, then tests in the same task)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - table-driven subtests, `testify/assert` + `testify/require`, `httptest` for handlers,
    `t.Helper()` in helpers, `t.TempDir()` for filesystem work
  - one test file per source file (`foo.go` → `foo_test.go`)
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run `make test` and `make lint` inside `backend/` (in WSL) after each change
- maintain backward compatibility: `lampa-web` behavior and its deployment must not change

## Go Style (taken from the Ralphex code, not just its docs)
Every task follows these; reviewers check them against the Ralphex checkout.
- **comments**: all lowercase except godoc on exported identifiers; godoc starts with the name
  (`// NewService creates ...`), extra godoc lines continue lowercase
  (`// returns an error if ...`).
- **constructors**: `NewX(cfg XConfig, deps...) (*X, error)` validating inputs; config structs
  named `<Thing>Config` with per-field trailing comments and private default helpers
  (`func (c ServerConfig) host() string` pattern); private struct fields for state.
- **servers**: Ralphex `pkg/web/server.go` shape — the server owns its `http.Server`, sets
  `ReadHeaderTimeout`, a goroutine calls `Shutdown` on `<-ctx.Done()`, `http.ErrServerClosed`
  maps to `nil`. **Deviations** (recorded): `Serve(ctx, ln net.Listener) error` is the core and
  `Start(ctx)` only does `net.Listen` + `Serve`, so tests can bind `127.0.0.1:0`; `Serve` waits
  for `Shutdown` to finish before returning, so `main` never closes the DB pool under
  in-flight handlers.
- **main**: `var revision = "unknown"` set via `-ldflags "-X main.revision=..."` with a
  `resolveVersion()` fallback to `debug.ReadBuildInfo` VCS data; `main()` builds
  `signal.NotifyContext(ctx, SIGINT, SIGTERM)` and calls `run(ctx, ...) error`; only `main`
  calls `os.Exit`.
- **errors**: wrap as `fmt.Errorf("<operation>: %w", err)` (`"parse template: %w"` style,
  lowercase, no "failed to"); sentinel/`errors.New` messages lowercase; `errors.Is/As`, never
  string matching.
- **logging**: stdlib `log` with level prefixes `[INFO]`, `[WARN]`, `[ERROR]` — **no `slog`,
  no logging library** (Ralphex has none). Packages that log take a consumer-side
  `Logger interface { Printf(format string, args ...any) }`; tests pass a moq mock or a
  `log.New(&buf, "", 0)` to assert on output. Request-derived strings are logged with `%q`
  (log-injection safe, keeps gosec G706 enabled).
- **interfaces**: defined in the consuming package, as small as possible; mocks via
  `//go:generate go tool moq -out mocks/<name>.go -pkg mocks -skip-ensure -fmt goimports . <Iface>`
  into a `mocks/` subdirectory; mock files excluded from coverage. When the mocked interface
  references the package's own types, the test that uses the mock is an external
  `package <name>_test` to avoid an import cycle.
- **tests**: one `foo_test.go` per `foo.go` (no `foo_something_test.go`); names
  `TestType_Method` / `TestFunc`; table cases as `tests := []struct{name string; ...}` +
  `for _, tc := range tests { t.Run(tc.name, ...) }`; `require` for preconditions, `assert`
  for checks; `httptest.NewRequest(method, path, http.NoBody)` + `httptest.NewRecorder()` with
  `resp := w.Result(); defer resp.Body.Close()`; helpers call `t.Helper()`; any files under
  `t.TempDir()`.
- **misc**: regexes compiled once at package level; deferred cleanup for every resource;
  context first for blocking calls; no new dependency without a direct need; lint noise is
  excluded in `.golangci.yml`, never silenced with `_, _ =`.

## Testing Strategy
- **unit tests**: required for every task; `make test` = `go test -race -coverprofile` over all
  packages, coverage reported excluding `mocks/`; target ≥ 80 % for new code.
- **integration tests**: PostgreSQL-backed tests use `pgtest.DB(t)` (Task 5): a lazy,
  `sync.Once`-per-test-binary testcontainers `postgres:18.6` with migrations applied.
  Without Docker it calls `t.Skip`, **unless `LAMPA_API_REQUIRE_DOCKER=1`, then `t.Fatal`** —
  CI sets that variable, so DB tests can never silently skip there. Pure unit tests in the same
  package never touch the container. Tests isolate by generating random user UUIDs (no
  truncation), so `t.Parallel()` is safe.
- **container / deployment checks**: `docker build`, compose validation with the real v1 CLI
  (`docker/compose:1.29.2` image — Docker Desktop's `docker-compose` is v2 and is more lenient),
  a local `docker-compose up` smoke with `curl` against both health endpoints, `actionlint` on
  the workflow.
- **e2e**: the project has no UI e2e suite; Plan 1 has no UI changes.

## Progress Tracking
- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview
- **Layout** (design §3.1), additive only:
  ```text
  backend/
  ├── cmd/lampa-api/        # main.go: config → logger → pgx pool → migrate → servers → shutdown
  ├── pkg/api/              # routes, Authenticator seam, middleware, JSON error contract, handlers
  ├── pkg/config/           # typed env config, defaults, validation
  ├── pkg/health/           # tiered checks, Svtlv report JSON, /health + /health/critical
  ├── pkg/storage/          # Document + validation, connection sealing, service, pg store, migrate
  │   ├── mocks/            # moq-generated Store mock
  │   └── pgtest/           # testcontainers helper shared by storage and cmd tests
  ├── migrations/           # 00001_*.sql + migrations.go (go:embed FS)
  ├── .golangci.yml  Makefile  Dockerfile  .dockerignore  go.mod  go.sum  vendor/
  ```
  `pkg/auth/` is intentionally not created — it arrives with Keycloak in Plan 2.
- **Authenticator seam**: `pkg/api` defines
  `type Authenticator interface { Authenticate(r *http.Request) (userID string, err error) }`.
  An api middleware calls it and alone stores the returned ID in the request context; handlers
  read it with `api.UserID(ctx)`. Plan 1 wires `api.DenyAll{}` (always `ErrUnauthenticated` →
  `401`). Plan 2's `pkg/auth` just implements the interface — no change to `pkg/api`. No request
  header, query or body can set the user ID.
- **Migrations**: goose `Provider` (no goose global state) over `stdlib.OpenDBFromPool(pool)`
  with `goose.WithSessionLocker(lock.NewPostgresSessionLocker())`, applying embedded SQL on
  startup before any listener opens. Failure = `run` returns error, process exits non-zero,
  `restart: unless-stopped` retries it.
- **Two listeners**: public API `:8080` (not published to host, reachable only on Docker
  networks — `/api` proxying from `lampa-web` is added in Plan 2 when login needs it); health
  `:8081`, never proxied. `run` starts both in goroutines under one cancelable context; the first
  failure cancels the other; after both return, the pool closes.
- **Encryption**: before writing, `settings` keys in the sensitive list are removed from `data`,
  marshaled as a JSON object, sealed with AES-256-GCM and stored in `encrypted_connections`;
  reads decrypt and merge them back so the API returns one logical document (design §4).
  AAD = format byte ‖ 16 raw bytes of the user UUID (case-insensitive by construction), so a
  ciphertext copied to another user's row fails to decrypt.
- **Health**: `database` (critical) = pool ping + `SELECT 1 FROM lampa_user_data LIMIT 0`.
  The advisory tier and `Degraded` aggregation are fully implemented and tested with fakes, but
  the `keycloak` advisory check is added in Plan 2, when a Keycloak issuer URL exists
  (**deviation from design §9.1**, recorded here and in the design doc). Checks run
  sequentially, each under its own timeout (two checks don't need concurrency).

## Technical Details

### Configuration (env, prefix `LAMPA_API_`)
| Variable | Default | Rule |
| --- | --- | --- |
| `LAMPA_API_LISTEN` | `:8080` | host:port |
| `LAMPA_API_HEALTH_LISTEN` | `:8081` | host:port, must differ from API listen |
| `LAMPA_API_DB_DSN` | — | required, never logged |
| `LAMPA_API_DATA_KEY` | — | required, base64 of exactly 32 bytes, never logged |
| `LAMPA_API_MAX_BODY_BYTES` | `2097152` (2 MiB) | > 0 |

Package constants (no env knob until a real need appears): max section size 1 MiB, max JSON
depth 32, shutdown timeout 15 s, per-health-check timeout 3 s, HTTP server timeouts below.
`config.Load(lookup func(string) (string, bool)) (Config, error)` — `os.LookupEnv` in `main`, a
map-backed func in tests. `Config.String()` redacts DSN and key.

### Schema (`migrations/00001_create_lampa_user_data.sql`)
```sql
-- +goose Up
create table lampa_user_data (
    user_id uuid primary key,
    schema_version integer not null,
    data jsonb not null,
    encrypted_connections bytea null,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);
-- +goose Down
drop table lampa_user_data;
```
Upsert: `insert ... on conflict (user_id) do update set schema_version, data,
encrypted_connections, updated_at = now() returning created_at, updated_at`.

### Document (`pkg/storage/document.go`)
- sections (fixed, in order): `settings, favorites, bookmarks, scores, subscriptions, progress,
  history, other`.
- `data` must be a JSON object; unknown top-level keys → `invalid_document`; missing sections
  are stored as `{}` so reads always return all eight; every section must be a JSON object.
- input must be valid UTF-8 and contain no `\u0000` escape — PostgreSQL `jsonb` rejects both
  (SQLSTATE `22021` / `22P05`), and they must surface as `400 invalid_document`, not `503`.
- depth measured with a streaming `json.Decoder` token walk (no recursion on untrusted input);
  per-section and total byte limits.
- only `schema_version == 1` accepted (`unsupported_schema_version` otherwise).

### Connection sealing (`pkg/storage/crypto.go`)
- sensitive `settings` keys: `torrserver_login`, `torrserver_password`, `jackett_key`,
  `jackett_key_two` — one exported list, extended in Plan 3 as the exporter discovers more.
- blob format: `0x01` (format version) ‖ 12-byte random nonce ‖ GCM ciphertext+tag;
  AAD = `0x01` ‖ 16-byte user UUID.
- no sensitive keys present → `encrypted_connections = NULL`.
- decrypt failure → service error `ErrConnectionsUnreadable` (`500 connections_unreadable`,
  logged without data).

### API contract (`pkg/api`)
| Request | Success | Errors |
| --- | --- | --- |
| `GET /api/v1/user-data` | `200` `{schema_version, data, updated_at}` | `401 unauthenticated`, `404 user_data_not_found`, `500 connections_unreadable`, `503 storage_unavailable` |
| `PUT /api/v1/user-data` body `{schema_version, data}` | `200` stored document (same shape as GET) | `400 invalid_document` / `unsupported_schema_version` / `invalid_json`, `401`, `413 request_too_large`, `415 unsupported_media_type` (JSON only — also a cheap CSRF barrier for Plan 2), `503` |
| `DELETE /api/v1/user-data` | `204` (idempotent) | `401`, `503` |
| any other method on `/api/v1/user-data` | — | `405 method_not_allowed` + `Allow: GET, PUT, DELETE` |
| any other path | — | `404 not_found` |
| any panic | — | `500 internal_error` |

Error body: `{"error": {"code": "user_data_not_found", "message": "..."}}` — message short and
generic, never echoes input. `ServeMux` writes plain-text 404/405 by itself, so the router
registers method patterns (`GET /api/v1/user-data`, …) **plus** a method-less
`/api/v1/user-data` fallback (JSON 405 with `Allow`) and a `/` fallback (JSON 404).
Middleware order (outermost first): access log (method, `%q` path, status, duration; no bodies,
headers or cookies) → recover → body limit (`http.MaxBytesReader`) → authenticator → handler.

### Health (`pkg/health`)
- `Check{Name, Tier (Critical|Advisory), Run func(ctx) error}` plus a fixed OK description and
  fixed failure error string per check.
- `/health` runs all, `/health/critical` runs critical only; each check under its own timeout.
- aggregation: any critical failure → `Unhealthy` (503); else any advisory failure → `Degraded`
  (200); else `Healthy` (200). Per-check status: failed critical → `Unhealthy`, failed advisory
  → `Degraded`.
- JSON exactly per design §9.2 (`status`, `totalDurationMs`, `checks[{name, status,
  description, durationMs, error}]`), `error: null` when OK; errors are fixed short strings
  (e.g. `"database unreachable"`), never raw driver errors (they can contain host/user).

### HTTP server hardening
`ReadHeaderTimeout 5s`, `ReadTimeout 15s`, `WriteTimeout 15s`, `IdleTimeout 60s`,
`MaxHeaderBytes 16 KiB`; shutdown timeout 15 s.

### Compose (`devops/docker-compose.yaml`, copied to the server)
- `lampa-db`: `postgres:18.6`, `container_name: svtlvtv_lampa_db`,
  `restart: unless-stopped`, `POSTGRES_DB=lampa`, `POSTGRES_USER=lampa`,
  `POSTGRES_PASSWORD=${LAMPA_DB_PASSWORD:?}`, volume `lampa_db_data:/var/lib/postgresql`
  (**PG 18+ image layout** — not `.../data`), healthcheck
  `["CMD-SHELL", "pg_isready -h 127.0.0.1 -U lampa -d lampa"]` (TCP, so the entrypoint's
  socket-only init server does not count as ready) with `interval: 5s`, `timeout: 5s`,
  `retries: 12`, `start_period: 30s`; network `lampa_db` (`internal: true`) only; no ports.
- `lampa-api`: `image: ${LAMPA_API_IMAGE:?}` and **no `build:`** (the server has no `backend/`
  directory and compose v1 validates build paths), `container_name: svtlvtv_lampa_api`,
  `restart: unless-stopped`, `depends_on: {lampa-db: {condition: service_healthy}}`,
  env DSN `postgres://lampa:${LAMPA_DB_PASSWORD}@lampa-db:5432/lampa?sslmode=disable`,
  `LAMPA_API_DATA_KEY=${LAMPA_API_DATA_KEY:?}`, healthcheck exactly per design §9.3, networks
  `default` (for `lampa-web` → `/api` in Plan 2), `lampa_db`, `monitoring_external`; no ports.
- `lampa-web`: unchanged.
- `devops/docker-compose.local.yaml` (never copied to the server): adds
  `build: {context: ../backend}` and a default `LAMPA_API_IMAGE=lampa-api:local` for local runs
  via `docker-compose -f docker-compose.yaml -f docker-compose.local.yaml ...`.
- note: `POSTGRES_PASSWORD` applies only on first init of `lampa_db_data`; rotating
  `LAMPA_DB_PASSWORD` later requires `ALTER ROLE` inside the DB first.

### Workflow (`.github/workflows/deploy-docker.yaml`)
`workflow_dispatch` inputs:
| Input | Default | Meaning |
| --- | --- | --- |
| `deploy` | `true` | `false` = checks + image build only, no remote steps, no `:svtlvtv` tag move |
| `web_image_tag` | empty | deploy this existing `sha-...` web image instead of building one |
| `api_image_tag` | empty | deploy this existing `sha-...` API image instead of building one (also skips `backend-checks`) |
| `api_enabled` | `true` | `false` = feature off: remote removes `lampa-api` + `lampa-db` containers (volume kept) and deploys only `lampa-web` |

Rollback = redeploy previously built images by tag (no rebuild of old commits). Disabling the
feature = `api_enabled: false` (design §13: returns Lampa to anonymous, local-only behavior).

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): code, tests, Dockerfile, Compose, workflow,
  docs inside this repo.
- **Post-Completion** (no checkboxes): GitHub secrets, first production deploy, Monitoring
  registration in the Svtlv.Monitoring.Service repo, rollback drill, key backup, and only then
  marking Plan 1 complete.

## Implementation Steps

### Task 1: Scaffold the backend module and Ralphex tooling

**Files:**
- Create: `backend/go.mod`, `backend/go.sum`, `backend/vendor/`
- Create: `backend/Makefile`
- Create: `backend/.golangci.yml`
- Create: `backend/.gitignore`
- Create: `backend/cmd/lampa-api/main.go`
- Create: `backend/cmd/lampa-api/main_test.go`
- Modify: `.dockerignore`

- [x] prepare WSL Ubuntu: Go 1.26.x, `make`, `gcc` (for `-race`), `golangci-lint v2.13.0`;
      confirm `docker ps` works from WSL (Docker Desktop WSL integration)
      - installed Go 1.26.8 (`/usr/local/go`, PATH via `/etc/profile.d/go.sh`), make 4.3, gcc 13.3,
        golangci-lint 2.13.0; run make targets with `wsl -d Ubuntu -- bash -lc 'cd ... && make test'`
      - ⚠️ `docker ps` not confirmed: Docker Desktop (WSL integration for `Ubuntu` already enabled in
        its settings) would not start from the non-interactive agent session. Start Docker Desktop
        manually before Task 5, otherwise `pgtest` tests skip locally
      - ⚠️ WSL `git` cannot resolve this worktree's Windows `.git` path, so `make build`/`make version`
        in WSL report `REV=latest`; CI and the Docker build are not affected
- [x] re-check Ralphex: `git -C C:/Users/21art/Projects/ralphex pull --ff-only` (Orca repo
      `ralphex`); if `master` moved past `a736d5e`, diff `go.mod`/`.golangci.yml`/`Makefile`/
      `.github/workflows/ci.yml` and update versions and the Go Style section here
      - still `a736d5e` on 2026-09-18, nothing to update
- [x] `go mod init` (module path `github.com/SvetlovA/lampa/backend` — confirm the GitHub owner
      first), `go 1.26.0`; add `testify v1.12.1`; `go get -tool` for `moq` and `goimports`;
      `go mod tidy && go mod vendor`
      - owner confirmed from `origin` (`github.com/SvetlovA/lampa`); tools: `moq v0.7.1`,
        `x/tools v0.50.0` (goimports)
- [x] Makefile from Ralphex: `build` (→ `.bin/lampa-api`, `-X main.revision`), `test`
      (race + coverage excluding mocks), `lint`, `fmt` (via `go tool goimports`), `race`,
      `version`, `generate` (`go generate ./...`); drop e2e/site/docker targets; **deviation**:
      `race` timeout `300s` (first-run Postgres image pull exceeds Ralphex's `60s`); **deviation**:
      `TIMESTAMP` uses GNU `date -u -d @<ts>` (Ralphex's `date -r` is BSD/macOS-only); added
      `backend/.gitattributes` (`eol=lf`) so `core.autocrlf=true` checkouts don't break make in WSL
- [x] copy Ralphex `.golangci.yml`, then drop gosec `G204`/`G702` (subprocess) and `G706` (log
      injection — we log with `%q` instead) exclusions; keep `G705` (JSON responses) and the
      rest that apply; comment every remaining Lampa-specific suppression
      - also dropped `G703` (path traversal, no user paths); kept `G115`, `G118`, `G705`
- [x] `main.go` stub copying Ralphex `cmd/ralphex/main.go` shape: `var revision = "unknown"`,
      `resolveVersion()` (ldflags → build info VCS → `unknown`), `run(ctx, args, lookup,
      stdout) error` split from `main()` for testability
- [x] add `backend` and `docs` to root `.dockerignore` so Go sources and design docs are no
      longer copied into the public `lampa-web` web root
- [x] write `main_test.go` for `run` (prints revision with `--version`, returns error on unknown
      argument)
- [x] run `make test` and `make lint` - must pass before Task 2

### Task 2: Typed environment configuration

**Files:**
- Create: `backend/pkg/config/config.go`
- Create: `backend/pkg/config/config_test.go`

- [x] `Config` struct + `Load(lookup)` with defaults and validation from the configuration
      table; errors name the variable and wrap with `%w`, never include values
- [x] decode `LAMPA_API_DATA_KEY` into a `[32]byte`; redact DSN/key in `String()`
- [x] tests: defaults, every override, each invalid value (missing required, bad base64, wrong
      key length, non-positive body limit, equal listen addresses)
      - empty values are treated as unset (compose `VAR=`); sentinels `ErrMissing`/`ErrInvalid`;
        `GoString` also redacts so `%#v` is safe; `.golangci.yml` excludes gosec `G101` in tests
- [x] test that `String()`/`%v`/`%+v` output contains neither DSN nor key
- [x] run `make test` and `make lint` - must pass before Task 3

### Task 3: Document model and validation

**Files:**
- Create: `backend/pkg/storage/document.go`
- Create: `backend/pkg/storage/document_test.go`

- [x] `Document{SchemaVersion int; Data map[string]json.RawMessage; UpdatedAt time.Time}`,
      section list, `Limits{MaxBodyBytes, MaxSectionBytes, MaxDepth}`
- [x] `ParseDocument(raw []byte, limits) (Document, error)` — valid UTF-8, no `\u0000`, valid
      JSON, schema version 1, allowed sections only, each section an object, per-section size,
      token-walk depth check, fill missing sections with `{}`
- [x] typed validation error (`*ValidationError{Code}`) so the API maps it via `errors.As`
- [x] tests: valid full/partial documents, unknown section, non-object section, array/scalar
      `data`, wrong schema version, invalid JSON, invalid UTF-8, `\u0000` escape, depth at/over
      limit, section at/over limit, deeply nested input does not blow the stack
      - also rejects unpaired surrogate escapes (`\ud800`-`\udfff`; jsonb refuses them with
        `22P02`, they would otherwise surface as `503`); unknown envelope fields →
        `invalid_document`; body over `MaxBodyBytes` → `request_too_large`;
        `Document.String()` prints no content
- [x] run `make test` and `make lint` - must pass before Task 4

### Task 4: Connection credential sealing

**Files:**
- Create: `backend/pkg/storage/crypto.go`
- Create: `backend/pkg/storage/crypto_test.go`

- [ ] `NewSealer(key [32]byte) (*Sealer, error)` using AES-256-GCM
- [ ] `Split(userID, doc) (clean Document, blob []byte, err)` — move sensitive `settings` keys
      into a sealed blob (AAD = format byte ‖ 16-byte UUID), `nil` blob when none present
- [ ] `Merge(userID, doc, blob) (Document, error)` — decrypt and put the keys back into
      `settings`; reject unknown format version
- [ ] tests: round trip, no sensitive keys → nil blob, clean `data` never contains the
      plaintext values, wrong key fails, blob under another user ID fails (AAD), same UUID in
      upper/lower case decrypts, tampered byte fails, truncated/empty blob fails, unknown
      version byte fails
- [ ] run `make test` and `make lint` - must pass before Task 5

### Task 5: Migrations, test database helper and PostgreSQL store

**Files:**
- Create: `backend/migrations/00001_create_lampa_user_data.sql`
- Create: `backend/migrations/migrations.go`
- Create: `backend/migrations/migrations_test.go`
- Create: `backend/pkg/storage/pgtest/pgtest.go`
- Create: `backend/pkg/storage/pgtest/pgtest_test.go`
- Create: `backend/pkg/storage/migrate.go`
- Create: `backend/pkg/storage/migrate_test.go`
- Create: `backend/pkg/storage/postgres.go`
- Create: `backend/pkg/storage/postgres_test.go`

- [ ] add `pgx/v5`, `goose/v3`, `testcontainers-go` + `modules/postgres`; tidy + vendor
- [ ] `migrations.FS` via `//go:embed *.sql`; test that the FS lists the expected files
- [ ] `storage.Migrate(ctx, pool)` via `goose.NewProvider(goose.DialectPostgres,
      stdlib.OpenDBFromPool(pool), migrations.FS, goose.WithSessionLocker(...))`
- [ ] `pgtest.DB(t) *pgxpool.Pool`: `sync.Once` container start (`postgres:18.6`),
      migrations applied once, `t.Skip` without Docker unless `LAMPA_API_REQUIRE_DOCKER=1`
      (then `t.Fatal`); container terminated by testcontainers' reaper
- [ ] `PgStore` with `Get(ctx, userID) (Record, error)` (`ErrNotFound`), `Upsert(ctx, Record)
      (Record, error)`, `Delete(ctx, userID) error`, `Ping(ctx) error` (ping + schema probe)
- [ ] tests: migrate is idempotent, **two concurrent `Migrate` calls both succeed** (locker),
      get missing → `ErrNotFound`, insert then get, upsert replaces and bumps `updated_at` but
      keeps `created_at`, delete + delete again, two users isolated, `Ping` fails on a closed
      pool; every test uses fresh random UUIDs and may run `t.Parallel()`
- [ ] run `make test` and `make lint` - must pass before Task 6

### Task 6: User-data service

**Files:**
- Create: `backend/pkg/storage/service.go`
- Create: `backend/pkg/storage/service_test.go` (external `package storage_test`)
- Create: `backend/pkg/storage/mocks/store.go` (generated)

- [ ] consumer-side `Store` interface + `//go:generate go tool moq -out mocks/store.go -pkg
      mocks -skip-ensure -fmt goimports . Store`
- [ ] `NewService(store, sealer, limits, logger)` validating its inputs;
      `Get(ctx, userID)`, `Replace(ctx, userID, raw []byte)`, `Delete(ctx, userID)`
- [ ] parse `userID` as a UUID up front (defense in depth — the seam should already guarantee
      it); map store errors to `ErrNotFound` / `ErrUnavailable`, wrap others with `%w`
- [ ] tests with the moq store (external test package, so `mocks` → `storage` import is not a
      cycle): happy paths, validation errors pass through untouched, sealer split on write +
      merge on read, store failures → `ErrUnavailable`, bad user ID rejected before touching
      the store, log output contains no document content
- [ ] one `pgtest` round trip through the real `PgStore` confirming encrypted keys are absent
      from the `data` column
- [ ] run `make test` and `make lint` - must pass before Task 7

### Task 7: Svtlv-compatible health reports

**Files:**
- Create: `backend/pkg/health/health.go`
- Create: `backend/pkg/health/health_test.go`

- [ ] `Check`, `Tier`, `Status` types; `NewReporter(checks, timeout)`
- [ ] `Handler(criticalOnly bool)` producing the §9.2 JSON and the Healthy/Degraded→200,
      Unhealthy→503 mapping; each check under its own timeout
- [ ] `DatabaseCheck(pinger)` critical check with fixed description/error strings
- [ ] tests (table-driven): all healthy, advisory fail → Degraded/200 on `/health` and Healthy
      on `/health/critical`, critical fail → Unhealthy/503 on both, timeout → failed check,
      exact JSON field names/casing and `"error": null`, error text never contains the
      underlying driver error
- [ ] run `make test` and `make lint` - must pass before Task 8

### Task 8: HTTP API, Authenticator seam and error contract

**Files:**
- Create: `backend/pkg/api/server.go`
- Create: `backend/pkg/api/server_test.go`
- Create: `backend/pkg/api/identity.go`
- Create: `backend/pkg/api/identity_test.go`
- Create: `backend/pkg/api/userdata.go`
- Create: `backend/pkg/api/userdata_test.go`

- [ ] `identity.go`: `Authenticator` interface, `ErrUnauthenticated`, `DenyAll` type, the
      middleware that stores the authenticated ID under an unexported context key, `UserID(ctx)`
- [ ] `server.go`: `ServerConfig` + `NewServer(cfg, svc, auth, logger) (*Server, error)`,
      `Serve(ctx, ln)` / `Start(ctx)` per the Go Style server deviation, `Handler()` accessor;
      method-pattern routes + JSON 405/404 fallbacks; middleware order per Technical Details;
      JSON error writer
- [ ] `userdata.go`: GET/PUT/DELETE handlers per the API contract table; `Content-Type` check on
      PUT; `*http.MaxBytesError` → 413
- [ ] tests via `httptest` with a fake authenticator: every status code in the contract,
      `DenyAll` → 401 on all three routes, `POST` → JSON 405 with `Allow`, unknown path → JSON
      404, a `user_id` in body/query/header is ignored, panic → logged 500 JSON, access log
      contains no body or cookie values; `Serve` on a `127.0.0.1:0` listener returns only after
      in-flight requests finish
- [ ] run `make test` and `make lint` - must pass before Task 9

### Task 9: Composition root and lifecycle

**Files:**
- Modify: `backend/cmd/lampa-api/main.go`
- Modify: `backend/cmd/lampa-api/main_test.go`

- [ ] wire config → stdlib `log` (`[INFO]/[WARN]/[ERROR]` prefixes) → pgx pool →
      `storage.Migrate` → sealer → service → API server (`DenyAll`) + health server with the
      hardening timeouts
- [ ] `main()` = `signal.NotifyContext` + `run(ctx, ...)`; `run` starts both servers in
      goroutines under one cancelable context, the first error cancels the other, waits for
      both, then closes the pool; only `main` calls `os.Exit(1)`
- [ ] tests: invalid config fails fast with no DB contact; `pgtest`-backed start on
      `127.0.0.1:0` listeners → `/health/critical` 200 and `GET /api/v1/user-data` 401 →
      context cancel shuts down cleanly; a bind failure on one listener stops both and returns
      the error
- [ ] run `make test` and `make lint` - must pass before Task 10

### Task 10: Backend container image

**Files:**
- Create: `backend/Dockerfile`
- Create: `backend/.dockerignore`

- [ ] verify `ghcr.io/umputun/baseimage/buildgo` ships Go ≥ 1.26 and `baseimage/app:v1.21.1`
      contains `curl` and drops to non-root `app` (check with `docker run --rm <img> id`, the
      image starts as root and switches in `init.sh`); if either fails, use `golang:1.26-alpine`
      builder / add `curl` and record the deviation here
- [ ] multi-stage build from vendored sources (`-mod=vendor`), `-X main.revision` from
      `GIT_BRANCH`/`GITHUB_SHA` args, OCI labels (`source`, `revision`, `description`),
      `EXPOSE 8080 8081`; **deviation**: pinned base tags instead of Ralphex's `:latest`
      (design §3.1 requires pinned versions)
- [ ] `backend/.dockerignore` excludes only local artifacts (`.bin/`, `coverage*.out`); keep
      `vendor/` and all sources so `-mod=vendor` builds see a consistent module
- [ ] verify: `docker build backend` succeeds; `docker run --rm <img> /srv/lampa-api --version`
      prints the revision; runtime user is `app`; note image size in this plan
- [ ] run `make test` and `make lint` - must pass before Task 11

### Task 11: Docker Compose services

**Files:**
- Modify: `devops/docker-compose.yaml`
- Create: `devops/docker-compose.local.yaml`

- [ ] add `lampa-db` and `lampa-api` exactly per the Compose section; add `lampa_db` internal
      network and `lampa_db_data` volume; leave `lampa-web` byte-for-byte unchanged
- [ ] add `docker-compose.local.yaml` with the local `build` + image default
- [ ] validate the production file with the real v1 CLI:
      `docker run --rm -v "$PWD/devops:/w" -w /w --env-file <sample.env> docker/compose:1.29.2 config -q`
      (sample env: `LAMPA_DOMAIN`, `LAMPA_API_IMAGE`, `LAMPA_DB_PASSWORD`, `LAMPA_API_DATA_KEY`)
- [ ] local smoke: `docker network create svtlv_monitoring_external` (once), `up -d lampa-db
      lampa-api` with both files → both `healthy`; from a container on `lampa-api`'s network
      `curl` `/health` (200 JSON) and `/health/critical` (200); stop `lampa-db` → both endpoints
      503 `Unhealthy` immediately, container `unhealthy` after ≈ 2.5 min (30 s × 5 retries);
      start `lampa-db` → recovers
- [ ] verify `lampa-web` still starts and serves `/` with `lampa-api` stopped
- [ ] run `make test` and `make lint` - must pass before Task 12

### Task 12: Manual CI/CD workflow: gates, image, deploy, disable, rollback

**Files:**
- Modify: `.github/workflows/deploy-docker.yaml`

- [ ] add the `deploy`, `web_image_tag`, `api_image_tag`, `api_enabled` inputs from Technical
      Details; validate tag inputs against `^sha-[0-9a-f]{40}$`
- [ ] new `backend-checks` job (skipped when `api_image_tag` is set): `setup-go@v7` with
      `go-version: "1.26"` + `cache-dependency-path: backend/go.sum`,
      `golangci-lint-action@v9` with `version: v2.13.0` and `working-directory: backend`,
      `make test` + `make race` in `backend/` with `LAMPA_API_REQUIRE_DOCKER=1`
- [ ] build jobs: web image only when `web_image_tag` is empty; API image
      `ghcr.io/<repo>-api:sha-<sha>` from `backend/` only when `api_image_tag` is empty and
      after `backend-checks`; push the moving `:svtlvtv` tags only when `deploy` is true
- [ ] validate new secrets `LAMPA_DB_PASSWORD` (`^[0-9a-f]{64}$`) and `LAMPA_API_DATA_KEY`
      (base64 → 32 bytes) when `api_enabled`; write `LAMPA_API_IMAGE`, `LAMPA_DB_PASSWORD`,
      `LAMPA_API_DATA_KEY` into `.env` (still `chmod 600`)
- [ ] remote steps only when `deploy`: print `docker-compose version`; `config --quiet`;
      if `api_enabled` → `pull lampa-web lampa-api`, `up -d --no-build --remove-orphans lampa-db
      lampa-api lampa-web`, poll `docker inspect` health of `svtlvtv_lampa_api` (≤ 3 min) and
      on failure print `docker-compose logs --tail=50 lampa-api` and fail; else →
      `rm -sf lampa-api lampa-db` (volume kept) and `up -d --no-build lampa-web`
- [ ] verify: `actionlint` passes; one dispatch with `deploy: false` goes green through checks
      and both image builds without touching the server

### Task 13: Verify acceptance criteria
- [ ] Plan-1 slice of design §15: users isolated (store + service tests), temporary DB failure
      yields 503 without crashing, Docker evaluates `/health/critical`, `/health` returns full
      JSON, `lampa-web` unaffected, no edits to `app.min.js`/`css/app.css`/`lang/*`/`index.html`
      (`git diff --stat svtlvtv...` shows only `backend/`, `devops/`, the workflow, `docs/` and
      `.dockerignore`)
- [ ] verify edge cases: oversize body, deep nesting, invalid UTF-8 / `\u0000`, tampered
      ciphertext, concurrent migrations
- [ ] run full suite in WSL: `cd backend && make test && make lint && make race`
- [ ] verify coverage ≥ 80 % excluding mocks (`make test` output)
- [ ] walk Ralphex `CLAUDE.md` → "Before Submitting a PR" checklist and this plan's Go Style
      section against the diff (lowercase comments, one test file per source, moq in `mocks/`,
      consumer-side interfaces, no `slog`, `make fmt` leaves no changes)
- [ ] `grep` logs of a local run to confirm no DSN, key, credentials or document content appear

### Task 14: [Final] Update documentation
- [ ] create `backend/README.md`: env vars, WSL/make targets, local run with both compose
      files, API and health contract summary, data-key backup/rotation caveat, DB password
      rotation caveat
- [ ] update `CLAUDE.md`: repo now contains a Go module under `backend/` (build/test/lint in
      WSL, testcontainers needs Docker), the `.dockerignore` rule for new top-level dirs, and the
      workflow inputs for disable/rollback
- [ ] update `docs/settings-sync-backend-design.md`: record deviations (keycloak advisory check
      and `/api` proxy deferred to Plan 2; Postgres 18.6 data path; rollback by image tag)
- [ ] move this plan to `docs/plans/completed/` **only after** the Post-Completion deploy,
      monitoring registration and rollback drill have succeeded; then mark Plan 1 done in the
      design doc

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Secrets and first deploy:**
- add repository secrets `LAMPA_DB_PASSWORD` (`openssl rand -hex 32`) and `LAMPA_API_DATA_KEY`
  (`openssl rand -base64 32`)
- **back up `LAMPA_API_DATA_KEY` outside GitHub** — losing it makes every stored TorrServer /
  Jackett credential unrecoverable; rotation is not implemented in Plan 1
- dispatch the workflow on `svtlvtv` with defaults; confirm `svtlvtv_lampa_api` and
  `svtlvtv_lampa_db` are `healthy`, the run log shows the server's `docker-compose version`, and
  the Lampa site still loads

**Monitoring registration (Svtlv.Monitoring.Service repo):**
- add the `lampa-api` target from design §9.4 (`http://lampa-api:8081/health`,
  `Evaluator: HealthReport`, `TreatDegradedAs: Alert`)
- verify it resolves `lampa-api` over `svtlv_monitoring_external`; stop `lampa-db` briefly and
  confirm an alert fires, then start it and confirm recovery

**Server smoke checks:**
- `docker exec svtlvtv_lampa_api curl -s -w ' %{http_code}\n' http://localhost:8080/api/v1/user-data`
  → `401` JSON (routes deployed but inert until Plan 2)
- ports 8080/8081/5432 are not published on the host (`docker port svtlvtv_lampa_api` and
  `docker port svtlvtv_lampa_db` print nothing)

**Disable and rollback drill:**
- dispatch with `api_enabled: false` → `lampa-api`/`lampa-db` containers removed, `lampa-web`
  keeps serving, `lampa_db_data` volume still present
- dispatch with `api_image_tag` = the previous `sha-...` API image → that version comes up
  healthy with existing data intact; then dispatch normally to return to the latest build
