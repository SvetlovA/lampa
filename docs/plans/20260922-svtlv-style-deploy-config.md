# Svtlv-style deploy, compose and file-based config for lampa-api

## Overview
- Addresses the six review threads on PR #2 by aligning the lampa-api delivery with the Svtlv
  conventions (`C:/Users/21art/Projects/svtlv`), written in Ralphex style
  (`C:/Users/21art/Projects/ralphex`) where the two differ in mechanics only.
- Review threads covered:
  1. `devops/docker-compose.yaml:48` — images use a `:dev` label with a `build:` block, and the
     workflow builds and swaps images the way Svtlv's `deploy-docker.yaml` does.
  2. `devops/docker-compose.yaml:56` — "what is `LAMPA_API_DATA_KEY`, why do we need it?".
     This is a question: it is answered in a compose comment and in the thread reply, and the
     key stays.
  3. `devops/docker-compose.yaml:65` — drop the `lampa_db` network, `default` is enough.
  4. `.github/workflows/deploy-docker.yaml:21` — replace all dispatch inputs with a single
     `environment` choice (Development / Test / Production, default Production), values from
     secrets; Test / Development read a local `.env`.
  5. `backend/Dockerfile:33` — ports must not collide with Svtlv, and the API port is published
     in compose.
  6. `backend/pkg/config/config.go:1` — config comes from files (defaults + per-environment
     overrides), secrets come from `.env`, the same logic for every environment.
- **Decision during planning: encrypted credentials stay, and the sealed set must mirror the
  current Lampa UI exactly.** An audit of every text-input setting in `app.min.js` found that
  the Parser screen's Prowlarr API keys (`prowlarr_key`, `prowlarr_key_two`) are stored in
  plain text today. They join the sealed set. A drift test pins the set to the UI, so an
  upstream pull that adds a secret field fails CI (Task 1).
- Added by request during planning: a `tests.yaml` workflow that runs the backend gates on every
  PR and on pushes to the default branch (`svtlvtv`).
- **Default environment is Test everywhere, as in Svtlv**: compose uses `${LAMPA_ENVIRONMENT:-Test}`
  and the binary falls back to Test when `LAMPA_ENVIRONMENT` is unset. Only the deploy workflow's
  `environment` input defaults to Production and writes that value into the server `.env`.
- **The first dispatch after this plan is the first time the API and DB run in production.**
  `svtlvtv` still deploys `lampa-web` only, and the repository has no `LAMPA_DB_PASSWORD` or
  `LAMPA_API_DATA_KEY` secret yet (verified with `gh secret list`). There is no volume or data to
  preserve. Both secrets must be created before that dispatch (see Post-Completion).
- No frontend change: `app.min.js`, `css/app.css`, `lang/*` and `index.html` are untouched (the
  drift test only reads `app.min.js`).

## Context (from discovery)
- **Lampa UI settings audit** (`app.min.js`): all 16 settings rendered as text inputs
  (`data-type="input"`) are listed below. Every masked secret field carries `data-string="true"`;
  there are exactly 5 such fields. CUB account tokens are excluded, per design §2 ("tokens owned
  by another service").

  | Screen | Keys | Sealed |
  |---|---|---|
  | TorrServer | `torrserver_url`, `torrserver_url_two`, `torrserver_login`, `torrserver_password` | login, password |
  | Parser / Jackett | `jackett_url`, `jackett_url_two`, `jackett_key`, `jackett_key_two` | both keys |
  | Parser / Prowlarr | `prowlarr_url`, `prowlarr_url_two`, `prowlarr_key`, `prowlarr_key_two` | both keys (**new**) |
  | Other | `tmdb_proxy_api`, `tmdb_proxy_image`, `device_name`, `player_nw_path` | no |

  Plugins can register their own input settings via `SettingsApi`. Those are unknown to the
  backend and are stored as plain settings.
- **Current Lampa state**
  - `backend/pkg/storage/crypto.go`: `SensitiveSettings` holds 4 keys (no Prowlarr). `Sealer`
    (`Split`/`Merge`, AES-256-GCM bound to the user id) seals them into
    `Record.EncryptedConnections` ↔ column `encrypted_connections`.
  - `backend/pkg/config/config.go`: env-only (`LAMPA_API_LISTEN`, `_HEALTH_LISTEN`, `_DB_DSN`,
    `_DATA_KEY`, `_MAX_BODY_BYTES`), defaults hardcoded (`:8080`, `:8081`, 2 MiB);
    listen ports accept 0–65535. `Load(lookup)` is called from `cmd/lampa-api/main.go` `run()`;
    `main_test.go` drives `run()` through env maps (testcontainers DSN, `127.0.0.1:0` listeners).
  - `devops/docker-compose.yaml`: `lampa-web` (build + `${LAMPA_IMAGE}`), `lampa-db`
    (`postgres:18.6`, internal `lampa_db` network), `lampa-api` (image only, no build, no
    published port); `devops/docker-compose.local.yaml` adds the API build for local runs.
  - `.github/workflows/deploy-docker.yaml`: 4 inputs (`deploy`, `web_image_tag`,
    `api_image_tag`, `api_enabled`), immutable `sha-<40 hex>` tags, secret validation in
    `prepare`, backend checks inside the deploy run, `concurrency: lampa-production-deploy`,
    remote `docker-compose config --quiet`, 3-minute health wait on `svtlvtv_lampa_api`.
    Image names: `ghcr.io/svetlova/lampa` (web) and `ghcr.io/svetlova/lampa-api`.
  - `backend/Dockerfile`: `EXPOSE 8080 8081`.
  - Root `.dockerignore` already excludes `devops`, `backend`, `docs`, `.github`; the repo has
    no root `.gitignore`.
  - Repository secrets: `DEPLOY_DIR`, `GHCR_PAT`, `LAMPA_DOMAIN`, `LAMPA_PORT`, `SERVER_HOST`,
    `SSH_PRIVATE_KEY`, `SSH_USER`, `TAILSCALE_AUTHKEY`; no repository variables.
  - Docs describing the old mechanics:
    - root `README.md`: local override, `LAMPA_API_IMAGE`, `api_enabled`/`api_image_tag`;
    - `backend/README.md`: env table;
    - `docs/settings-sync-backend-design.md`: `LAMPA_API_*` env config, and credential lists
      that name only TorrServer/Jackett;
    - `CLAUDE.md` and `AGENTS.md`.
- **Svtlv conventions**
  - `devops/docker-compose.yaml`: every built service has `build: {context: .., dockerfile: …}` +
    `image: <name>:dev`; `DOTNET_ENVIRONMENT: ${DOTNET_ENVIRONMENT:-Test}`; secrets passed as
    `${VAR}`; health on container-internal `8081` (never published); DB published on a host port
    (`5433:5432`).
  - `.github/workflows/deploy-docker.yaml`: single `environment` choice input; `prepare` →
    matrix `build` (`docker/metadata-action`: branch, `{{branch}}-sha`, `latest` on default
    branch) → `deploy`: `sed` swaps `image: x:dev` → `ghcr.io/<owner>/svtlv-x:latest`, deletes
    `/build:/,/dockerfile:/`, copies compose, writes `.env` from secrets (including
    `DOTNET_ENVIRONMENT`), then `pull` → `down` → ensure `svtlv_monitoring_external` → `up -d` →
    `image prune`.
  - `tests.yaml`: `pull_request` + `push` on the default branch + `workflow_dispatch`, concurrency
    cancels superseded PR runs only.
  - Config: `appsettings.json` holds the shared defaults, including `0.0.0.0` Kestrel endpoints
    (e.g. Scheduler `:5400`, health `:8081`). `appsettings.{Environment}.json` overrides only
    hostnames and connection strings. Secrets are `{ENV_VAR}` placeholders filled by
    `AppSettingsParser`.
  - Ports in use: app ports 5100–5700 (one per service; 5100–5400 published), health 8081
    (internal), published 80, 443, 4317, 5100, 5200, 5300, 5400, 5433, 8080 (Keycloak), 9000,
    9092, 9094, 9096, 18888, 18891. Lampa web already publishes 8092.
  - Its compose sets `name: devops`; locally a Lampa stack started from `devops/` would get the
    same default project name.
- **Ralphex conventions**: embedded defaults via `//go:embed` under `pkg/config/defaults/`,
  JSON struct tags on config types, lowercase comments, stdlib-first, one `_test.go` per source
  file, `ci.yml` runs race + coverage + `golangci-lint v2.13.0`.
- Repo default branch: `svtlvtv`.

## Development Approach
- **testing approach**: Regular (code first, then tests)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new functions/methods
  - write unit tests for modified functions/methods
  - add new test cases for new code paths
  - update existing test cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change: `make test` / `make race` / `make lint` **inside WSL Ubuntu**
  from `backend/` (Windows Go has no cgo, `-race` fails there)
- non-Go tasks (compose, workflows) are verified with `docker-compose config`, a local run of the
  deploy `sed` transform plus its guard, and `actionlint` (in WSL) where available

## Testing Strategy
- **unit tests**:
  - `pkg/storage`: sealing of the two Prowlarr keys, plus a drift test that parses `app.min.js`;
  - `pkg/config`: table tests over an `fstest.MapFS` (layering, environment selection,
    placeholders, validation, redaction), plus one test that loads the real embedded files for
    every environment;
  - `cmd/lampa-api`: tests switch from env-driven config to an injected settings FS.
- **integration**: existing testcontainers tests (`LAMPA_API_REQUIRE_DOCKER=1` in CI).
- **e2e**: none in this repo.
- **infra checks**: `docker-compose -f devops/docker-compose.yaml --env-file devops/.env.example
  config --quiet`; the same after applying the workflow's `sed` transform and guard to a copy; a
  local `docker-compose up -d --build` smoke run in Test. CI runs compose v2, which is more
  permissive than the server's v1, so the first real dispatch is the actual v1 check.

## Progress Tracking
- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview
- **Sealed settings mirror the UI (new)**: `SensitiveSettings` becomes `torrserver_login`,
  `torrserver_password`, `jackett_key`, `jackett_key_two`, `prowlarr_key` and
  `prowlarr_key_two`.
  - `Sealer`, the blob format, `encrypted_connections` and `LAMPA_API_DATA_KEY` are unchanged.
    `Merge` already ignores unknown sealed keys, so the list change needs no blob-version bump.
  - `TestSensitiveSettings_matchUI` reads `../../../app.min.js` and asserts two things:
    - every `data-type="input"` setting with `data-string="true"` is in `SensitiveSettings`;
    - every `SensitiveSettings` key still exists as an input in the UI.
  - The test skips when the file is absent, so a `backend/`-only checkout still passes. CI
    checks out the whole repo, so it catches upstream drift.
- **Config (thread 6)**: `backend/pkg/config/defaults/appsettings.json` holds the shared defaults:
  container listeners `:5800` / `:8081`, the local DB (`localhost:5434`), limits and placeholder
  secrets. `appsettings.Test.json` and `appsettings.Production.json` override only the DB host
  and port (`lampa-db:5432`), as Svtlv overrides only hosts and connection strings.
  - Files are embedded with `//go:embed` (Ralphex).
  - They are decoded in order, base then environment file, into the **same** struct with
    `DisallowUnknownFields`, so missing keys keep their base values (.NET layering) without a
    generic map merge.
  - `{ENV_VAR}` placeholders in `Database.Password` and `DataKey` are then resolved from the
    environment in that fixed order (Svtlv `AppSettingsParser`).
  - The environment comes from `LAMPA_ENVIRONMENT` (`Development` / `Test` / `Production`,
    default `Test`). No other env overrides exist, so the files are the single source of
    non-secret settings.
  - **Development means a local `go run` against the DB published on `localhost:5434`.** Inside
    a container it cannot reach the DB, and the README and the workflow input say so.
- **Ports (thread 5)**: the API moves to container port **5800**, the next free port in Svtlv's
  per-service 5x00 series, published as `${LAMPA_API_PORT:-5800}:5800`. The old 8080 is
  Keycloak's published host port. Health stays on **8081**, container-internal and never
  published, exactly Svtlv's convention; container ports cannot collide across containers,
  only published host ports can. The DB is published as `127.0.0.1:${LAMPA_DB_PORT:-5434}:5432`
  so a Development `go run` can reach it, bound to loopback so the server never exposes it
  (Svtlv's 5433 stays untouched).
- **Compose (threads 1–3)**:
  - `lampa-web` → `image: lampa-web:dev` and `lampa-api` → `image: lampa-api:dev`, both with
    `build:` blocks laid out for the `/build:/,/dockerfile:/` strip (rules in a header comment).
  - The `lampa_db` network is removed; `lampa-db` runs on `default`.
  - `lampa-api` gets `LAMPA_ENVIRONMENT`, `LAMPA_DB_PASSWORD` and `LAMPA_API_DATA_KEY`, plus a
    comment explaining the data key. The secrets keep their `:?` required markers, so a missing
    value fails `config` instead of starting a crash-looping container.
  - `docker-compose.local.yaml` is deleted because the base file now builds locally.
  - `devops/.env.example` is committed; `devops/.env` is gitignored.
- **Deploy (thread 4)**: Svtlv shape with `environment` as the only input.
  - `api_enabled` is dropped: the API is only used by logged-in users and denies everything
    otherwise, so it is effectively off without a login.
  - Rollback inputs are dropped: to roll back, revert the commit and redeploy.
  - Kept from the current workflow because they are safety checks, not inputs: secret
    validation in `prepare` (without the `api_enabled`/`deploy` branches), the
    `lampa-production-deploy` concurrency group, remote `docker-compose config --quiet`, and
    the 3-minute `svtlvtv_lampa_api` health wait with a log dump.
  - Added: a default-branch guard, because the deploy pulls `:latest` and only the default
    branch publishes it. Also a post-`sed` guard, so a compose layout that breaks the strip
    fails the run instead of deploying a mangled file.
- **Tests workflow (new)**: `tests.yaml` runs lint, `make test`, `make race` and a compose
  config check on every PR and on pushes to `svtlvtv`. The backend checks move out of the deploy
  workflow, as in Svtlv. As a result, deploy does not gate on tests (accepted, same as Svtlv).

## Technical Details
- **Drift test parsing**: the templates are JS string literals with escaped quotes. The test
  matches `data-type=\\"input\\" data-name=\\"([a-z0-9_]+)\\"([^>]*)` and treats a match as
  secret when the captured attributes contain `data-string=\\"true\\"`. Current result: 16 inputs,
  5 secret. The result must be non-empty, so a template format change cannot pass silently.
- **appsettings shape** (PascalCase keys like Svtlv; strict decoding, unknown keys are errors):
  ```json
  {
    "Api": { "Listen": ":5800", "MaxBodyBytes": 2097152 },
    "Health": { "Listen": ":8081" },
    "Database": { "Host": "localhost", "Port": 5434, "Name": "lampa", "User": "lampa",
                  "Password": "{LAMPA_DB_PASSWORD}", "SSLMode": "disable" },
    "DataKey": "{LAMPA_API_DATA_KEY}"
  }
  ```
  `appsettings.Test.json` / `appsettings.Production.json`:
  `{ "Database": { "Host": "lampa-db", "Port": 5432 } }`. There is no
  `appsettings.Development.json`: Development uses the base file, and .NET also treats the
  environment file as optional.
- **Loader**: `config.Load(fsys fs.FS, lookup func(string) (string, bool)) (Config, error)`;
  `config.Defaults` exposes the embedded `fs.FS`. Steps:
  1. Resolve the environment: unset or empty → `Test`; unknown → `ErrInvalid` listing the
     allowed names.
  2. Decode `appsettings.json` (required) and then `appsettings.<env>.json` (optional) into the
     same file struct, with `DisallowUnknownFields` and a check for trailing data.
  3. Resolve placeholders `\{([A-Z][A-Z0-9_]*)\}` in `Database.Password`, then `DataKey`, with
     substring substitution. An unset or empty variable → `ErrMissing` naming the variable and
     the key path, never the value.
  4. Validate:
     - listen addresses are host:port with port 0–65535, and API listen ≠ health listen;
     - `Database.Port` is 1–65535;
     - `MaxBodyBytes` > 0;
     - `Database.Host`, `Name`, `User` and `Password` are required;
     - the data key is base64 of exactly 32 bytes.
  5. Build `Config`.
- **Config struct** keeps its current fields so `start()` is unchanged: `Environment` (new),
  `Listen`, `HealthListen`, `DBDSN`, `DataKey`, `MaxBodyBytes`. `DBDSN` is built with
  `url.URL{Scheme: "postgres", User: url.UserPassword(user, pass), Host: net.JoinHostPort(host,
  port), Path: "/" + name, RawQuery: "sslmode=…"}`, so any password characters are escaped.
  `String()` / `GoString()` keep redacting the DSN and key and add the environment.
- **Composition root**: `run(ctx, args, settings fs.FS, lookup, out)`; `main` passes
  `config.Defaults` and `os.LookupEnv`, tests pass an `fstest.MapFS` whose `Database` section
  points at the testcontainer and whose listeners are `127.0.0.1:0`.
- **Compose `sed`-strip rules** (header comment in `devops/docker-compose.yaml`):
  1. `image:` sits outside the `build:`…`dockerfile:` range; it goes before `build:`.
  2. Only `context:` and `args:` (with its entries) may sit between `build:` and `dockerfile:`.
  3. No other line, comments included, may contain `build:`.
- **Local `.env` (`devops/.env.example`)**: `COMPOSE_PROJECT_NAME=lampa`, which avoids sharing
  Svtlv's local `devops` project, plus `LAMPA_ENVIRONMENT=Test`, `LAMPA_DOMAIN`,
  `LAMPA_PREFIX`, `LAMPA_PORT=8092`, `LAMPA_API_PORT=5800`, `LAMPA_DB_PORT=5434`,
  `LAMPA_DB_PASSWORD` and `LAMPA_API_DATA_KEY` (placeholders plus the `openssl` commands).
- **Images**: `ghcr.io/<owner lowercase>/lampa-web` (new; replaces `ghcr.io/svetlova/lampa`) and
  `ghcr.io/<owner lowercase>/lampa-api` (same name as today). Tags come from
  `docker/metadata-action@v6`: `type=ref,event=branch`, `type=sha,prefix={{branch}}-`,
  `type=raw,value=latest,enable={{is_default_branch}}`. The matrix cannot read secrets, so both
  builds get the union of build args (`domain`, `prefix`, `CI`, `GIT_BRANCH`, `GITHUB_SHA`), and
  BuildKit only warns about the ones a Dockerfile does not declare.
- **Server `.env`** written by the deploy job (no `COMPOSE_PROJECT_NAME`, so the server project
  stays the `DEPLOY_DIR` basename and volume/container names are stable):
  - `LAMPA_ENVIRONMENT=${{ inputs.environment }}`
  - `LAMPA_PORT=${LAMPA_PORT:-8092}` from the existing secret
  - `LAMPA_API_PORT=5800` literal; there is no secret for it
  - `LAMPA_DB_PASSWORD` and `LAMPA_API_DATA_KEY` from secrets
  - `LAMPA_DOMAIN` is not needed there, because the `sed` strip removes the web build args.
- **Post-`sed` guard**: `if grep -nE '^\s*(build|context|dockerfile|args):|:dev\s*$'
  devops/docker-compose.yaml; then exit 1; fi`.

## What Goes Where
- **Implementation Steps**: sealed-settings completion + drift test, Go config + tests,
  composition root, Dockerfile + compose files, both workflows, docs.
- **Post-Completion**: creating the two secrets, replies on the six PR threads, the first real
  dispatch, the Svtlv monitoring entry for lampa-api, and cleanup of the old
  `ghcr.io/svetlova/lampa` package.

## Implementation Steps

### Task 1: Seal every credential the Lampa UI can store

**Files:**
- Modify: `backend/pkg/storage/crypto.go`
- Modify: `backend/pkg/storage/crypto_test.go`

- [x] add `prowlarr_key` and `prowlarr_key_two` to `SensitiveSettings`; update its doc comment
      (TorrServer, Jackett and Prowlarr credentials; mirrors the UI's secret inputs)
- [x] extend `TestSealer_Split` / `TestSealer_RoundTrip` so both Prowlarr keys are removed from
      `settings`, sealed, and restored by `Merge`
- [x] add `TestSensitiveSettings_matchUI` (parsing from Technical Details):
  - every UI secret input is in `SensitiveSettings`;
  - every `SensitiveSettings` key exists as a UI input;
  - at least one input is found;
  - skip when `app.min.js` is absent.
- [x] check that a blob sealed with the old 4-key list still merges (compatibility; `Merge`
      ignores unknown keys)
- [x] run `make test` + `make race` + `make lint` in WSL — must pass before task 2

### Task 2: File-based config with environments and placeholders

**Files:**
- Create: `backend/pkg/config/defaults/appsettings.json`
- Create: `backend/pkg/config/defaults/appsettings.Test.json`
- Create: `backend/pkg/config/defaults/appsettings.Production.json`
- Modify: `backend/pkg/config/config.go`
- Modify: `backend/pkg/config/config_test.go`

- [x] add the three appsettings files with the shape from Technical Details
- [x] embed them (`//go:embed defaults/appsettings*.json`) and expose `Defaults fs.FS`
- [x] implement environment resolution (`LAMPA_ENVIRONMENT`, default `Test`, allowed
      Development/Test/Production)
- [x] implement layered strict decoding: base, then optional environment file, into one struct
- [x] implement placeholder resolution for `Database.Password`, then `DataKey`, with key-path
      errors that never print values
- [x] build `Config` (escaped DSN via `url.URL`, decoded data key) and keep validation +
      redacted `String`/`GoString`, now including `Environment`
- [x] rewrite `config_test.go` as table tests over `fstest.MapFS`: default environment, each
      environment override, layering keeps untouched nested keys, substring placeholder, DSN
      escaping of a password with `@:/?#%`
- [x] write error-case tests: unknown environment, missing base file, malformed JSON, trailing
      data, unknown key, unset/empty placeholder variable (exact message names variable, not
      value), bad listen, same listen, listen port 0 accepted, DB port 0 rejected, non-positive
      body limit, missing DB fields, bad data key, redaction of DSN and key
- [x] write a test that loads the real `Defaults` for all three environments with placeholders
      supplied (guards the shipped files, including key casing)
- [x] run `make test` + `make lint` in WSL — must pass before task 3
- ➕ pulled forward from Task 3 to keep the build green: `run` already takes `settings fs.FS` (`main`
      passes `config.Defaults`), and `main_test.go` uses a `testSettings` MapFS helper for the
      config-error cases and `TestRun_bindFailure`. Task 3 still owns the startup-log check and
      the exact `ErrMissing` test for the embedded defaults.
- ⚠️ the DB-backed `cmd/lampa-api` tests (`TestRun_bindFailure`, `TestStart_*`) skip locally (no
      Docker in WSL or on Windows); CI runs them with `LAMPA_API_REQUIRE_DOCKER=1`

### Task 3: Wire the settings FS into the composition root

**Files:**
- Modify: `backend/cmd/lampa-api/main.go`
- Modify: `backend/cmd/lampa-api/main_test.go`

- [x] change `run` to take `settings fs.FS`; `main` passes `config.Defaults` and `os.LookupEnv`
- [x] confirm the startup log carries the environment (via `Config.String()`)
- [x] update `main_test.go`:
  - the config-error cases become settings-FS / env cases: missing data key variable, bad key,
    same listen;
  - the end-to-end run and the bind-failure test use an `fstest.MapFS` pointing `Database` at
    the testcontainer, with `127.0.0.1:0` listeners.
- [x] add a test that `run` with the embedded defaults and an empty env fails with the exact
      `ErrMissing` message for `LAMPA_DB_PASSWORD` (first in the fixed resolution order)
- ➕ `TestRun_logsEnvironment` checks the startup log names the environment (default `Test`,
      explicit `Production`) without a DB: the settings point at a closed port, so `run` stops at
      the migration right after logging the config
- [x] run `make test` + `make race` + `make lint` in WSL — must pass before task 4 (Docker was
      reachable from WSL this time, so the DB-backed tests ran with `LAMPA_API_REQUIRE_DOCKER=1`;
      total coverage 93.3%)

### Task 4: Compose in Svtlv shape and container ports

**Files:**
- Modify: `backend/Dockerfile`
- Modify: `devops/docker-compose.yaml`
- Delete: `devops/docker-compose.local.yaml`
- Create: `devops/.env.example`
- Create: `devops/.gitignore`

- [x] Dockerfile: `EXPOSE 5800 8081` and update the comment ("5800 user-data API, 8081
      container-internal health, never published — Svtlv convention")
- [x] header comment with the three `sed`-strip rules
- [x] `lampa-web`: `image: lampa-web:dev` then `build` (context `..`, `args`, `dockerfile`)
- [x] `lampa-api`: `image: lampa-api:dev` then `build` (context `../backend`, `dockerfile:
      Dockerfile`), with this environment:
  - `LAMPA_ENVIRONMENT: ${LAMPA_ENVIRONMENT:-Test}`;
  - `LAMPA_DB_PASSWORD` and `LAMPA_API_DATA_KEY`, keeping their `:?` markers;
  - `LAMPA_API_DB_DSN` removed (the DSN now comes from appsettings);
  - a comment on what the data key protects: AES-256-GCM sealing of the TorrServer login and
    password and the Jackett and Prowlarr API keys inside each user document; losing the key
    makes them unreadable, so back it up outside GitHub.
- [x] publish `${LAMPA_API_PORT:-5800}:5800`; healthcheck on `localhost:8081/health/critical`
      unchanged
- [x] remove the `lampa_db` network; `lampa-db` publishes `127.0.0.1:${LAMPA_DB_PORT:-5434}:5432`
      and keeps its `:?` password marker
- [x] do not add a top-level `name:` (keeps server volume/network names; compose v1 safety)
- [x] add `devops/.env.example` (keys from Technical Details) and `devops/.gitignore` with `.env`
- [x] verify:
  - `docker-compose --env-file .env.example config --quiet`;
  - apply the deploy `sed` transform plus guard to a copy, then `config --quiet` again;
  - `docker run` of the built API image without env exits with the `ErrMissing` message naming
    `LAMPA_DB_PASSWORD`;
  - `docker-compose up -d --build` with a local `.env` in Test: API healthy, a user-data route on
    `localhost:5800` answers 401.
- ➕ the header comment cannot quote the strip command itself: a comment containing the build key
      with its colon would open a `sed` range and delete everything up to the next `dockerfile:`
- ➕ `.env.example` uses a non-zero placeholder data key (base64 of a 32-byte marker string);
      an all-zero key logs as `DataKey:[unset]` because redaction treats the zero key as absent
- ⚠️ a build block without a `dockerfile:` line makes the `sed` range run to end of file; the
      post-`sed` guard does not catch that (only leftover keys and `:dev` images); Task 6 may
      add a check that the stripped file still ends with the `volumes:` section
- verified locally (compose v5.5.1): base and stripped files pass `config --quiet` (stripped
      without `LAMPA_DOMAIN`), the guard is clean, `docker run lampa-api:dev` exits 1 with
      `Database.Password: LAMPA_DB_PASSWORD: required value is not set`, and `up -d` in Test
      gives a healthy API with GET/PUT/DELETE `/api/v1/user-data` → 401 on `localhost:5800`,
      8081 unreachable from the host, web 200 on 8092, DB on `127.0.0.1:5434`

### Task 5: Tests workflow

**Files:**
- Create: `.github/workflows/tests.yaml`

- [ ] triggers: `pull_request` (all PRs), `push` to `svtlvtv`, `workflow_dispatch`;
      Svtlv concurrency (cancel superseded PR runs only); `permissions: contents: read`
- [ ] backend job (`defaults.run.working-directory: backend`, `LAMPA_API_REQUIRE_DOCKER=1`):
  - checkout of the whole repo, so the drift test sees `app.min.js`;
  - `setup-go@v7` 1.26 with `cache-dependency-path: backend/go.sum`;
  - `golangci-lint-action@v9` `v2.13.0` with `with: working-directory: backend`, because the
    action ignores the run default;
  - `make test`, `make race`.
- [ ] compose job: `docker compose -f devops/docker-compose.yaml --env-file
      devops/.env.example config --quiet` (keeps `.env.example` in step with compose; v2 only)
- [ ] run `actionlint` on the file (WSL) — must be clean before task 6

### Task 6: Deploy workflow in Svtlv shape

**Files:**
- Modify: `.github/workflows/deploy-docker.yaml`

- [ ] single input `environment` (choice Development/Test/Production, default Production; the
      description notes that Development is for local `go run` only). Drop `deploy`,
      `web_image_tag`, `api_image_tag` and `api_enabled`.
- [ ] keep `concurrency: {group: lampa-production-deploy, cancel-in-progress: false}`; scope
      `packages: write` to the build job
- [ ] `prepare`:
  - lowercase image prefix `<owner>/lampa`;
  - fail unless `github.ref_name == github.event.repository.default_branch`;
  - keep "Validate required configuration" without the `api_enabled`/`deploy` branches: all
    required secrets including `LAMPA_DB_PASSWORD` and `LAMPA_API_DATA_KEY`, the `DEPLOY_DIR`
    safety check, the port range check and the format checks for both secrets.
- [ ] `build` matrix (`web`: context `.`, `Dockerfile`; `api`: context `backend`,
      `backend/Dockerfile`) with `metadata-action` tags, gha cache scoped per service, and the
      union of build args
- [ ] `deploy`, in order:
  1. Tailscale → SSH.
  2. `sed`: swap `lampa-web:dev` / `lampa-api:dev` to `ghcr.io/<prefix>-{web,api}:latest` and
     strip `/build:/,/dockerfile:/`.
  3. Post-`sed` guard.
  4. scp the compose file; write `.env` (keys from Technical Details, `umask 077`, `chmod 600`,
     atomic `.next` + `mv` as today).
  5. `docker login`.
  6. Remote: `config --quiet` → `pull` → `down` → ensure `svtlv_monitoring_external` →
     `up -d --no-build` → 3-minute health wait on `svtlvtv_lampa_api` with a log dump →
     `image prune -f`.
- [ ] keep the existing secret names (`DEPLOY_DIR`, `GHCR_PAT`, `LAMPA_DOMAIN`, `LAMPA_PORT`,
      `LAMPA_DB_PASSWORD`, `LAMPA_API_DATA_KEY`, `SERVER_HOST`, `SSH_*`, `TAILSCALE_AUTHKEY`,
      `vars.LAMPA_PREFIX` optional)
- [ ] run `actionlint`; dry-run the `sed` block and guard locally against
      `devops/docker-compose.yaml` and diff the result

### Task 7: Verify acceptance criteria
- [ ] each of the six review threads maps to a concrete change (or a reply for the question)
- [ ] `SensitiveSettings` matches the UI audit table, and the drift test passes against the
      current `app.min.js`
- [ ] a grep over `backend/` (excluding `vendor/`), `devops/`, `.github/`, `docs/`, `README.md`,
      `CLAUDE.md` and `AGENTS.md` (excluding `docs/plans/completed/`) finds none of:
  - `LAMPA_API_LISTEN`, `_HEALTH_LISTEN`, `_DB_DSN`, `_MAX_BODY_BYTES`;
  - `LAMPA_API_IMAGE`, `api_enabled`, `api_image_tag`, `docker-compose.local.yaml`.
- [ ] published host ports 8092, 5800 and loopback 5434 are absent from Svtlv's list
- [ ] Test is the default in compose and binary; Production only via the workflow input
- [ ] full suite in WSL: `make test`, `make race`, `make lint`; coverage for `pkg/config` and
      `pkg/storage` ≥ the current level (backend total was 83.7%)

### Task 8: [Final] Update documentation
- [ ] `backend/README.md`: replace the env table with the appsettings layering, environments
      (Development = local `go run` only), placeholders, ports, local Test/Development workflow
      with `devops/.env`; list the six sealed settings and the drift test
- [ ] root `README.md`: file table (no local override), local compose run with `.env.example`,
      deploy section (single `environment` input, required secrets, no rollback inputs,
      rollback = revert and redeploy)
- [ ] `docs/settings-sync-backend-design.md`:
  - config section → appsettings + placeholders, ports;
  - credential wording → TorrServer, Jackett **and Prowlarr**, sealed set mirrors the UI's
    secret inputs;
  - note that plugin-defined settings are stored as plain settings.
- [ ] `CLAUDE.md` and `AGENTS.md` lampa-api sections: new workflow input, `tests.yaml`, removed
      rollback/`api_enabled`, `.env.example`, no `docker-compose.local.yaml`, ports, the
      `sed`-strip layout rules, the drift test (an upstream pull that adds a secret input fails
      it until `SensitiveSettings` is updated)
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Before the first dispatch**
- create the repository secrets `LAMPA_DB_PASSWORD` (`openssl rand -hex 32`) and
  `LAMPA_API_DATA_KEY` (`openssl rand -base64 32`); back up the data key outside GitHub
- merge `lampa-backend` into `svtlvtv` (the deploy runs only from the default branch)

**PR #2 thread replies** (resolve manually after pushing)
- compose:56: `LAMPA_API_DATA_KEY` is the AES-256-GCM key that seals the TorrServer login and
  password and the Jackett and Prowlarr API keys inside each user document. The DB never holds
  them in plain text. Losing the key makes them unreadable, so back it up outside GitHub. The
  sealed set now mirrors every secret input in the Lampa UI, and a test fails if upstream adds a
  new one.
- Dockerfile:33: the API moves to 5800, which is free in Svtlv's 5x00 series (8080 is
  Keycloak's published port). Health stays on 8081 on purpose: it is the Svtlv
  container-internal convention and is never published. The API is published as
  `${LAMPA_API_PORT:-5800}`.

**Manual verification**
- first dispatch with `environment=Production` from `svtlvtv`:
  - `lampa-web` and `lampa-api` images are pushed with `latest`;
  - the server `.env` has `LAMPA_ENVIRONMENT=Production`;
  - `svtlvtv_lampa_api` is healthy;
  - `<project>_lampa_db_data` is created.
- second dispatch: `docker volume ls` shows the same `lampa_db_data` volume reused and the API
  stays healthy
- a Test dispatch shows `environment=Test` in the lampa-api startup log

**External system updates**
- Svtlv monitoring (`appsettings.json` in `Svtlv.Monitoring.Service`): optionally add a
  `lampa-api` URL probe on `http://lampa-api:8081/health/critical` over
  `svtlv_monitoring_external`
- after the first successful deploy, delete the old web package `ghcr.io/svetlova/lampa`
  (renamed to `lampa-web`). **`ghcr.io/svetlova/lampa-api` keeps its name — do not delete it.**
