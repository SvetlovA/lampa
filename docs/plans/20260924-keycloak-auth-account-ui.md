# Plan 2: Keycloak Authentication and the Account Add-on

## Overview
- Implements roadmap **Plan 2** from `docs/settings-sync-backend-design.md` §14, with the
  decisions recorded in §5.1–§5.6, §8, §10.5 and §12 of that document.
- Backend: replace `api.DenyAll` with real Keycloak authentication in `lampa-api` — the OAuth 2.0
  Device Authorization Grant for TVs, Authorization Code + PKCE for phones and computers,
  server-side PostgreSQL sessions behind an opaque `HttpOnly` cookie, `GET /api/v1/session`,
  logout, CSRF protection, and the `keycloak` advisory health check.
- Deployment: Apache `/api` proxy inside the `lampa-web` image (deferred from Plan 1), the API
  port no longer published, new Keycloak configuration and secret in Compose and the manual
  deploy workflow.
- Frontend: the first `svtlv/` add-on, `account.js`, loaded through one marked `index.html`
  include. It adds **Account** to Settings, puts the user's avatar in the header slot CUB's
  profile icon uses today (hiding CUB's icon, keeping every CUB function reachable from its
  menu), and runs sign-in / sign-out. It never calls the user-data API.
- No user data moves in this plan: settings, bookmarks and history stay in each device's
  `localStorage`. `app.min.js`, `css/app.css` and `lang/*` are not touched.
- UI reference: the mockup at https://claude.ai/artifact/YCNYMgMfV3qbFPZjLPgv5A (screens 1–8).
- **Plan 2 is done only after the Post-Completion Keycloak setup, production deploy, on-device
  checks on the `lampa-app/LAMPA` TV client and the rollback drill succeed.**

## Context (from discovery)
- **Auth seam**: `backend/pkg/api/identity.go` — `Authenticator.Authenticate(*http.Request)
  (string, error)`, `ErrUnauthenticated`, `DenyAll`, and the `authenticate` middleware that
  logs non-`ErrUnauthenticated` failures. `backend/cmd/lampa-api/main.go` wires `api.DenyAll{}`.
- **Routes / middleware**: `backend/pkg/api/server.go` `routes()` — user-data routes wrapped by
  `accessLog(recoverPanic(limitBody(withDeadline(mux))))`.
- **Config**: `backend/pkg/config/config.go` — embedded `appsettings.json` +
  `appsettings.<Environment>.json`, strict decoding, `{ENV_VAR}` placeholders resolved only by
  explicit `resolve(...)` calls (`Database.Password`, `DataKey`). A new placeholder field needs
  its own `resolve` call.
- **Storage / migrations**: `backend/migrations/00001_create_lampa_user_data.sql` (goose,
  embedded), `backend/pkg/storage/postgres.go` (pgx pool), `pkg/storage/pgtest` for
  testcontainers (`postgres:18.6`, skipped without Docker unless `LAMPA_API_REQUIRE_DOCKER=1`).
- **Health**: `backend/pkg/health/health.go` — critical/advisory tiers already implemented and
  tested with fakes; only `DatabaseCheck` is registered.
- **Web image**: root `Dockerfile` is `httpd:alpine3.15` with `COPY . htdocs/`; `.dockerignore`
  excludes `devops`, `backend`, `docs`, … so config for Apache cannot live in `devops/`.
- **Deploy**: `devops/docker-compose.yaml` publishes `lampa-api` on
  `${LAMPA_BIND_ADDRESS}:${LAMPA_API_PORT:-5800}` (Plan 1 deviation, to be removed);
  `.github/workflows/deploy-docker.yaml` validates secrets and writes the server `.env`;
  `.github/workflows/tests.yaml` runs lint/test/race and a compose `config` check against
  `devops/.env.example`. Keep CLAUDE.md's compose header-comment layout rules.
- **Lampa public API used by the add-on** (all on `window.Lampa`): `SettingsApi`, `Settings`,
  `Select`, `Modal`, `Noty`, `Loading`, `Platform` (`tv()`), `Utils.qrcode`, `Template`,
  `Controller`, `Storage`, `Lang`, `Head`, `Account.Profile.{icon,select,update}`,
  `Account.Modal.account()`, `Account.Permit`.
- **CUB header icon**: created by `Profile.init` (`app.min.js` ~23188) as
  `head__action selector open--profile` before `.full--screen`; not created when
  `lampa_settings.account_use` is false; emptied asynchronously by `Profile.update()`.
- **TV client**: `github.com/lampa-app/LAMPA` loads the configured HTTPS URL directly
  (`MainActivity.onBrowserInitCompleted` → `browser.loadUrl(LAMPA_URL)`), engines SysView
  (Android WebView) and XWalk (Crosswalk); no cookie code, WebView defaults apply.
- **Toolchain**: all `make` targets run inside WSL Ubuntu from `backend/` (Windows Go has no
  cgo, so `-race` fails there). Go style follows the local Ralphex checkout.

## Development Approach
- **testing approach**: Regular (code first, then tests in the same task)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - table-driven subtests, `testify/assert` + `testify/require`, `httptest` for handlers,
    `t.Helper()` in helpers; one test file per source file (`foo.go` → `foo_test.go`)
  - tests cover both success and error scenarios
  - the ES5 add-on has no JS test runner in this repo: its tasks are covered by the CI ES5
    syntax gate (Task 11) plus the manual checks listed in each task and in Post-Completion
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run `make test` (WSL, `backend/`) after each backend change; keep coverage ≥ 80% for new code
- maintain backward compatibility: anonymous Lampa and CUB must behave exactly as before

## Testing Strategy
- **unit tests**: required for every backend task (see Development Approach)
- **integration tests**: session store against PostgreSQL via `pkg/storage/pgtest`
  (testcontainers); OIDC flows against an `httptest` fake Keycloak (discovery, JWKS signed with
  a test RSA key, device-authorization and token endpoints)
- **frontend**: no e2e framework in this repo. CI parses `svtlv/*.js` as ECMAScript 5; behavior
  is verified manually in a desktop browser, a phone browser and the `lampa-app/LAMPA` TV client
  (Post-Completion)

## Progress Tracking
- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview
- **`pkg/auth`** (new) owns everything identity-related: cookie sealing, the PostgreSQL session
  store, the Keycloak/OIDC client, the auth HTTP handlers and the session `Authenticator`.
  `pkg/api` keeps owning the middleware chain and mounts the auth handlers next to user data.
- **Two login flows, one session**: both end in `auth.Sessions.Create(profile)` → random 32-byte
  cookie value, SHA-256 stored, profile (`sub`, `name`, `email`, `picture`) copied from the
  verified ID token. No Keycloak token is stored anywhere.
- **Device grant** (TV): `device_code` never leaves the server — it is sealed into an
  `HttpOnly` cookie scoped to `/api/v1/auth/device`; each poll is one token request (single-shot,
  not `oauth2.Config.DeviceAccessToken`, which blocks until done).
- **Authorization Code + PKCE** (phone/computer): state, nonce, verifier and return path in a
  sealed 10-minute cookie scoped to `/api/v1/auth/callback`; return path limited to local
  absolute paths.
- **Keys**: cookie sealing keys derived from `LAMPA_API_DATA_KEY` with stdlib `crypto/hkdf`
  (SHA-256) and one purpose label per cookie; AES-256-GCM. No new cookie secret.
- **CSRF**: middleware in `pkg/api` on every non-safe method: require `X-Lampa-Csrf: 1`, reject a
  present `Origin` that differs from the configured public origin; never credentialed CORS.
- **Frontend**: one ES5 file wired through public `window.Lampa` APIs; CUB stays fully
  functional and independent; the add-on disables itself on non-HTTPS origins (localhost
  excepted for development) or when the API is unreachable, leaving CUB's own icon visible.

## Technical Details
- **Config** (`appsettings.json`, new `Auth` section):
  ```json
  "Auth": {
    "PublicURL": "{LAMPA_PUBLIC_URL}",
    "Issuer": "{LAMPA_KEYCLOAK_ISSUER}",
    "ClientID": "svtlv-lampa",
    "ClientSecret": "{LAMPA_KEYCLOAK_CLIENT_SECRET}"
  }
  ```
  `PublicURL` (e.g. `https://lampa.example`) gives the redirect URI
  (`<PublicURL>/api/v1/auth/callback`) and the allowed `Origin`. `PublicURL` and `Issuer` must
  be absolute `https` URLs in Production. **Deviation from design §3.1 / CLAUDE.md** ("only
  secrets are placeholders"): `PublicURL` and `Issuer` are not secret but are placeholders
  because the Lampa domain is deliberately kept out of the public repository (`LAMPA_DOMAIN` is a
  GitHub secret). The deploy workflow derives `LAMPA_PUBLIC_URL=https://${LAMPA_DOMAIN}` so the
  two can never disagree (a mismatch would 403 every POST through the Origin check);
  `LAMPA_KEYCLOAK_ISSUER` is a repository variable; only the client secret is a secret.
  Recorded under design §14 in Task 14.
- **Session lifetimes are constants** in `pkg/auth` (30-day idle, 180-day absolute, design
  §5.3), not configuration: tests inject a clock, and embedded config cannot be changed on the
  server without a redeploy anyway.
- **Migration `00002_create_lampa_session.sql`**: table from design §5.3 plus index on
  `user_id` and on `expires_at` (purge).
- **Session cookie**: `lampa_session=<base64url 32 bytes>; Path=/api; HttpOnly; Secure;
  SameSite=Lax; Max-Age=<idle seconds>`. Sliding: every successful lookup moves
  `last_seen_at` and `expires_at = min(now + idle, created_at + max)`; `GET /api/v1/session`
  re-sends the cookie with `Max-Age = expires_at - now` (never past the absolute cap). The
  add-on calls it on every app start **and every 12 hours while Lampa stays open**, so a TV
  left running for weeks keeps a live browser cookie. Lookups throttle DB writes (update
  `last_seen_at` at most once a minute per session).
- **Session status on failure**: a database error while resolving an existing cookie answers
  `503 session_unavailable`, never `{"authenticated":false}`; only a missing, unknown or
  expired cookie is anonymous. The add-on treats `503`/network errors as "service unavailable"
  (keeps its last state, no sign-out).
- **Cookies for flows**: `lampa_login` (Path `/api/v1/auth/callback`, Max-Age 600) and
  `lampa_device` (Path `/api/v1/auth/device`, Max-Age = device `expires_in`), both
  `HttpOnly; Secure; SameSite=Lax` (Lax, not Strict: the callback arrives as a cross-site
  top-level redirect from Keycloak), AES-256-GCM sealed JSON with an expiry inside the sealed
  payload.
- **Browser-facing login failures**: Keycloak down at `/auth/login`, a missing/expired
  `lampa_login` at `/callback`, a state mismatch or a Keycloak `error` parameter all redirect to
  `/#svtlv-login=failed` (clearing `lampa_login`), never a JSON page; success redirects to the
  return path with `#svtlv-login=ok`. The add-on reads and removes that fragment to show its
  Noty.
- **Auth failures on protected routes**: the `authenticate` middleware answers `401` only for
  `ErrUnauthenticated`; any other error (session store down) is logged and answers
  `503 session_unavailable`, so a client is never told it is signed out because the database
  failed (design §5.3; Plan 4's adapter depends on it).
- **Responses**: `GET /api/v1/session` → `200 {"authenticated":false}` or
  `200 {"authenticated":true,"user":{id,name,email,picture}}`;
  `POST /api/v1/auth/device/start` → `200 {user_code, verification_uri,
  verification_uri_complete, expires_in, interval}`;
  `POST /api/v1/auth/device/poll` → `202 {"status":"pending","interval":n}`,
  `200 {"authenticated":true,"user":…}` + session cookie, `403 access_denied`,
  `410 expired`, `400 no_device_login` (missing/invalid cookie);
  `POST /api/v1/auth/logout` → `204`, cookie cleared; errors use the existing
  `writeError` JSON shape.
- **Name / picture**: `name` falls back to `preferred_username`, then `email`; `picture` is
  accepted only as an absolute `https` URL, else stored empty.
- **Purge**: a goroutine in `main` deletes rows with `expires_at < now()` every hour; stops
  with the process context.
- **Keycloak health**: advisory check that fetches the issuer's
  `/.well-known/openid-configuration` (short timeout); failure → `/health` `Degraded`,
  `/health/critical` unaffected. Descriptions never include URLs with secrets.
- **Apache proxy** (web `Dockerfile`, written inline with `RUN` so no new top-level directory
  ships in `htdocs`): enable `mod_proxy`/`mod_proxy_http`, `ProxyPass /api/v1/
  http://lampa-api:5800/api/v1/ disablereuse=On` (the old httpd resolves the backend once per
  worker; without it an API container restart with a new IP gives 502s until `lampa-web`
  restarts; traffic is tiny), `ProxyPassReverse`, `ProxyPreserveHost On`; nothing else under
  `/api` is proxied and port `8081` is never proxied.
- **Add-on strings**: an object `{en: {...}, ru: {...}}` inside `account.js`, selected by
  `Lampa.Storage.get('language', 'ru')`; not added to `lang/*` (design §10.1).

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): code, tests, CI, Compose, workflow and doc
  changes in this repository.
- **Post-Completion** (no checkboxes): Keycloak client creation, GitHub secrets, deploy,
  on-device verification, monitoring and rollback drill.

## Implementation Steps

### Task 1: Add the Auth configuration section

**Files:**
- Modify: `backend/pkg/config/config.go`
- Modify: `backend/pkg/config/defaults/appsettings.json`
- Modify: `backend/pkg/config/config_test.go`
- Modify: `backend/cmd/lampa-api/main_test.go`

- [ ] **pre-flight (before any code)**: on the server, `docker exec svtlvtv_lampa_api curl -fsS <issuer>/.well-known/openid-configuration` and compare its `issuer` with the realm's public issuer; record the result in Context. If the container cannot reach the public issuer (hairpin NAT, Keycloak only on the Svtlv network), stop and add a separate discovery URL to this plan and design §5.6 before continuing
- [ ] add `Auth` settings (PublicURL, Issuer, ClientID, ClientSecret) to `settings`/`Config`, strict decoding unchanged
- [ ] resolve `{LAMPA_PUBLIC_URL}`, `{LAMPA_KEYCLOAK_ISSUER}` and `{LAMPA_KEYCLOAK_CLIENT_SECRET}` with explicit `resolve` calls; missing values fail naming the variable, never the value
- [ ] validate: absolute URLs (https required in Production), non-empty client ID/secret; `String()`/`GoString()` redact the client secret
- [ ] update `main_test.go`'s `testSettings` fixture with an `Auth` section and its env maps with the three new variables, so the existing run tests keep passing
- [ ] write tests for successful load per environment (Test, Production) including redaction
- [ ] write tests for errors: missing variables, http URL in Production, unknown key
- [ ] run `make test` and `make lint` - must pass before next task

### Task 2: Add OIDC dependencies and cookie sealing

**Files:**
- Modify: `backend/go.mod`, `backend/go.sum`, `backend/vendor/`
- Create: `backend/pkg/auth/seal.go`
- Create: `backend/pkg/auth/seal_test.go`

- [ ] add `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2`; `go mod tidy && go mod vendor`
- [ ] create `CookieSealer` (named apart from `storage.Sealer`, both are built in `main`) in `pkg/auth/seal.go`: HKDF-SHA256 (`crypto/hkdf`) key per purpose label (`lampa-login-v1`, `lampa-device-v1`) from the data key, AES-256-GCM seal/open of JSON payloads with an embedded expiry
- [ ] reject expired, tampered, truncated and cross-purpose values with distinct wrapped errors
- [ ] write tests for round trip and per-purpose key separation
- [ ] write tests for tampering, truncation, expiry and wrong purpose
- [ ] run `make test` and `make lint` - must pass before next task

### Task 3: Add the session table and PostgreSQL session store

**Files:**
- Create: `backend/migrations/00002_create_lampa_session.sql`
- Modify: `backend/migrations/migrations_test.go`
- Modify: `backend/pkg/storage/migrate_test.go`
- Create: `backend/pkg/auth/sessions.go`
- Create: `backend/pkg/auth/sessions_test.go`

- [ ] add the goose migration for `lampa_session` (design §5.3) with indexes on `user_id` and `expires_at`, plus its down section
- [ ] implement `Sessions` over the pgx pool: `Create(ctx, Profile) (token string, err)`, `Lookup(ctx, token) (Session, error)` with sliding expiry (30-day idle constant) capped by the 180-day absolute constant and write throttling, `Delete(ctx, token)`, `PurgeExpired(ctx) (int64, error)`
- [ ] store only SHA-256 of the token; generate tokens with `crypto/rand`; return `ErrNoSession` (wrapping `api.ErrUnauthenticated`) for missing/expired rows and wrapped errors for DB failures
- [ ] write integration tests with `pgtest`: create/lookup, sliding and absolute cap (injected clock), delete, purge, unknown token
- [ ] write tests for DB failure paths; add the new file to `migrations_test.go`'s `TestFS` expectations; in `pkg/storage/migrate_test.go` exercise up and down of `00002` with a goose provider against `pgtest` (`storage.Migrate` itself only goes up)
- [ ] run `make test` and `make lint` - must pass before next task

### Task 4: Add the Keycloak OIDC client

**Files:**
- Create: `backend/pkg/auth/keycloak.go`
- Create: `backend/pkg/auth/keycloak_test.go`

- [ ] create `Keycloak` with **lazy discovery**: construction never calls Keycloak; the first use runs `oidc.NewProvider` under a mutex and caches the provider, `oauth2.Config` (client id/secret, redirect URI, scopes `openid profile email`) and ID-token verifier; a failed discovery returns an error and is retried on the next use, so a Keycloak outage never stops the API
- [ ] implement `AuthCodeURL(ctx, state, nonce, verifier)` with S256 PKCE and `Exchange(ctx, code, verifier, nonce) (Profile, error)` that verifies the ID token and nonce
- [ ] implement `StartDevice(ctx)` via `oauth2.Config.DeviceAuth` **passing the client secret explicitly** (`oauth2.SetAuthURLParam("client_secret", …)`: upstream `deviceauth.go` sends only `client_id` and `scope`, and Keycloak authenticates confidential clients at its device endpoint)
- [ ] implement a single-shot `PollDevice(ctx, deviceCode)` as a hand-rolled, client-authenticated `grant_type=urn:ietf:params:oauth:grant-type:device_code` token request (`oauth2.Config.DeviceAccessToken` sleeps one interval before its first request and blocks until done), mapping `authorization_pending`, `slow_down`, `expired_token`, `access_denied` to typed results; the device-flow ID token carries no nonce, so it is verified without the nonce check
- [ ] extract `Profile{UserID, Name, Email, Picture}`: `sub` must parse as UUID, name falls back to `preferred_username` then `email`, `picture` only if absolute https
- [ ] write tests against an `httptest` fake Keycloak (discovery, JWKS with a test RSA key, device and token endpoints) for the success paths of both flows; the fake **rejects device and token requests without the client secret**, as a confidential client in Keycloak does
- [ ] write tests for errors: bad signature, wrong audience/issuer, nonce mismatch, non-UUID `sub`, each device error code; lazy discovery: Keycloak down → error → Keycloak back → next call works
- [ ] run `make test` and `make lint` - must pass before next task

### Task 5: Add the session Authenticator and 503 for store failures

**Files:**
- Create: `backend/pkg/auth/authenticator.go`
- Create: `backend/pkg/auth/authenticator_test.go`
- Modify: `backend/pkg/api/identity.go`
- Modify: `backend/pkg/api/identity_test.go`
- Modify: `backend/pkg/api/server.go`
- Modify: `backend/pkg/api/server_test.go`

- [ ] implement `api.Authenticator` over `Sessions`: read the `lampa_session` cookie, `Lookup`, return the user id; missing/invalid/expired → `api.ErrUnauthenticated`; DB errors wrapped
- [ ] change the `authenticate` middleware: `401 unauthenticated` only for `ErrUnauthenticated`; any other error is logged and answers `503 session_unavailable` (design §5.3)
- [ ] export `api.WriteError` / `api.WriteJSON` (keep the JSON shape) so `pkg/auth` handlers reuse them (`pkg/auth` already imports `pkg/api`, no cycle)
- [ ] add cookie helpers `SetSessionCookie(w, token, maxAge)` / `ClearSessionCookie(w)` with the attributes from Technical Details
- [ ] write tests for valid session, no cookie, malformed cookie, expired session, cookie attributes
- [ ] write tests for DB failure: the authenticator returns a non-`ErrUnauthenticated` error and the middleware answers `503`; update existing `identity_test.go` expectations
- [ ] run `make test` and `make lint` - must pass before next task

### Task 6: Add the auth HTTP handlers

**Files:**
- Create: `backend/pkg/auth/handlers.go`
- Create: `backend/pkg/auth/handlers_test.go`

- [ ] `GET /api/v1/session`: anonymous or profile JSON; on a valid session re-send the cookie with `Max-Age` = remaining lifetime (clamped to the absolute cap); a DB error on an existing cookie → `503 session_unavailable`
- [ ] `GET /api/v1/auth/login?return=`: validate the return path (local absolute path only, default `/`), seal login state into `lampa_login`, redirect to Keycloak; `GET /api/v1/auth/callback`: open the cookie, check state, exchange, create session, set cookie, clear `lampa_login`, redirect to the return path + `#svtlv-login=ok`; every failure redirects to `/#svtlv-login=failed` (Technical Details), never a JSON page
- [ ] `POST /api/v1/auth/device/start` and `POST /api/v1/auth/device/poll` per Technical Details; success clears `lampa_device` and sets the session cookie
- [ ] `POST /api/v1/auth/logout`: delete the row if present, clear the cookie, `204` also when already signed out
- [ ] write tests (fakes for `Sessions` and `Keycloak` via consumer-side interfaces, moq into `mocks/` if useful) for every success path
- [ ] write tests for errors: open-redirect attempts (`//evil`, `https://evil`, `\\evil`, `/\evil`), state mismatch, missing/expired flow cookies, Keycloak down at login, each device result, DB failures, `GET /session` DB error → `503` (never `authenticated:false`), `Max-Age` clamped near the absolute cap
- [ ] run `make test` and `make lint` - must pass before next task

### Task 7: Add CSRF protection and mount the auth routes

**Files:**
- Create: `backend/pkg/api/csrf.go`
- Create: `backend/pkg/api/csrf_test.go`
- Modify: `backend/pkg/api/server.go`
- Modify: `backend/pkg/api/server_test.go`, `backend/pkg/api/userdata_test.go`, `backend/pkg/api/flow_test.go`
- Modify: `backend/cmd/lampa-api/main.go`, `backend/cmd/lampa-api/main_test.go`

- [ ] add middleware: for non-GET/HEAD/OPTIONS require `X-Lampa-Csrf: 1` and reject a present `Origin` differing from `ServerConfig.PublicOrigin` (`403 csrf_rejected`); absent `Origin` alone is allowed; no CORS headers are ever emitted
- [ ] extend `ServerConfig`/`NewServer` to accept the public origin and an auth `http.Handler`, mounted for `/api/v1/auth/` and `/api/v1/session` inside the existing middleware chain
- [ ] keep `main` compiling and green: pass `cfg.Auth.PublicURL` and a temporary not-found auth handler (replaced in Task 8)
- [ ] update existing user-data tests to send the header
- [ ] write tests for accepted requests (header present, same origin, no origin) and rejections (no header, wrong value, foreign origin, `Origin: null`)
- [ ] write tests that the auth routes are reachable through the chain and keep body limits/deadlines
- [ ] run `make test` and `make lint` - must pass before next task

### Task 8: Wire authentication, purge and the Keycloak health check in main

**Files:**
- Modify: `backend/cmd/lampa-api/main.go`
- Modify: `backend/cmd/lampa-api/main_test.go`
- Modify: `backend/pkg/health/health.go`
- Modify: `backend/pkg/health/health_test.go`

- [ ] add `health.KeycloakCheck` (advisory) fetching the issuer discovery document with a short timeout
- [ ] in `main`, build `CookieSealer`, `Sessions`, the lazily-discovering `Keycloak`, the handlers and the `Authenticator`; replace `api.DenyAll{}` and the temporary auth handler
- [ ] start the hourly purge goroutine bound to the process context and **join it before `pool.Close()`**
- [ ] write tests for the Keycloak check (healthy, unreachable, bad status) and its `Degraded` aggregation in `/health`
- [ ] write tests for main wiring: API starts with Keycloak down, user-data returns `401` without a session and `200` with one (fake Keycloak), purge goroutine exits before the pool closes
- [ ] run `make test` and `make lint` - must pass before next task

### Task 9: Add the Apache /api proxy and update Compose

**Files:**
- Modify: `Dockerfile`
- Modify: `devops/docker-compose.yaml`
- Modify: `devops/.env.example`

- [ ] in the web `Dockerfile`, enable `mod_proxy`/`mod_proxy_http` and append the `/api/v1/` `ProxyPass … disablereuse=On` / `ProxyPassReverse` / `ProxyPreserveHost` block to `httpd.conf` with `RUN` (no new top-level directory in `htdocs`)
- [ ] Compose: stop publishing the `lampa-api` port; pass `LAMPA_PUBLIC_URL`, `LAMPA_KEYCLOAK_ISSUER`, `LAMPA_KEYCLOAK_CLIENT_SECRET` with `:?` guards; update the "non-secret settings live in those files" comment for the two non-secret placeholders; keep the header-comment layout rules (`image:` before `build:`, only `context:`/`args:` between `build:` and `dockerfile:`)
- [ ] update `devops/.env.example` (new variables with placeholders, drop `LAMPA_API_PORT`)
- [ ] verify: `docker compose -f devops/docker-compose.yaml --env-file devops/.env.example config --quiet`; local stack up → `curl http://localhost:8092/api/v1/session` returns `{"authenticated":false}` through the proxy; `:8081` is not reachable through it; `docker restart svtlvtv_lampa_api` and the same curl still works (no stale backend IP)
- [ ] run `make test` - must pass before next task

### Task 10: Add Keycloak settings to the deploy workflow

**Files:**
- Modify: `.github/workflows/deploy-docker.yaml`

- [ ] derive `LAMPA_PUBLIC_URL=https://${LAMPA_DOMAIN}`; read `LAMPA_KEYCLOAK_ISSUER` from `vars.LAMPA_KEYCLOAK_ISSUER` and `LAMPA_KEYCLOAK_CLIENT_SECRET` from `secrets`; add them to the required list, validation (`https://` issuer, non-empty secret) and the server `.env`
- [ ] drop the `LAMPA_API_PORT=5800` line from the generated server `.env`
- [ ] keep the post-strip compose guard passing and the 3-minute `svtlvtv_lampa_api` health wait
- [ ] verify with `actionlint` (or a dry parse) and by running the validation step's bash locally with good and bad values
- [ ] run the compose `config` check again - must pass before next task

### Task 11: Add the add-on loader seam, settings entry and ES5 CI gate

**Files:**
- Create: `svtlv/account.js`
- Create: `svtlv/account.css`
- Modify: `index.html`
- Modify: `.github/workflows/tests.yaml`

- [ ] add a clearly marked `<!-- svtlv:begin -->…<!-- svtlv:end -->` block to `index.html`: the stylesheet as a `<link>`, the script via `putScript('svtlv/account.js?v=' + cache_version, function () {}, function () {})` with a **no-op `onerror`** (without it `putScript` shows the `.no-network` overlay after 3 failed tries, breaking design §10.1) or a plain `<script>` tag
- [ ] `account.js` (ES5 IIFE) readiness: poll until `window.Lampa` exists, then `if (window.appready) init(); else Lampa.Listener.follow('app', function (e) { if (e.type == 'ready') init(); })`; `try/catch` around init
- [ ] gate on `location.protocol === 'https:'` (or `http:` with host `localhost`/`127.0.0.1` for local development, where browsers accept `Secure` cookies); `GET /api/v1/session` via `$.ajax` with timeout on start and every 12 hours (cookie heartbeat); if the first call fails disable itself, later failures (`503`, network) keep the last state
- [ ] register `SettingsApi.addComponent({component: 'account_lampa', name: 'Account', before: 'interface', icon})` and render signed-out / signed-in rows with Lampa's `settings-param` classes from `Lampa.Settings.listener.follow('open', …)` when `e.name == 'account_lampa'` (screens 2 and 4); en/ru strings inside the file
- [ ] add a `frontend` job to `tests.yaml` that parses `svtlv/*.js` as ECMAScript 5 with an exactly pinned acorn (e.g. `npx --yes acorn@8.14.0 --ecma5 --silent`); the gate checks syntax only, so review for post-ES5 APIs (`fetch`, `Object.assign`, `Array.prototype.includes`, `Promise` outside Lampa's polyfill) by hand
- [ ] manual check in a desktop browser: entry appears before Interface with CUB on and with `lampa_settings.account_use = false`; API down → no entry, no console errors from boot; rename `svtlv/account.js` → Lampa boots normally with no overlay
- [ ] run the ES5 check locally - must pass before next task

### Task 12: Add the header avatar icon and its menu

**Files:**
- Modify: `svtlv/account.js`
- Modify: `svtlv/account.css`

- [ ] create a `head__action selector open--account` icon before `.full--screen` (same slot as CUB) and hide `.head .open--profile` via `account.css` only while the add-on is active
- [ ] render the icon: Account `picture` (set only via `img.src` after the https check) → initials → `Lampa.Account.Profile.icon()` when only CUB is signed in → plain profile icon; refresh on sign-in/out and on `Lampa.Storage.listener` `account` changes
- [ ] on `hover:enter` open a `Lampa.Select` menu per design §10.5, for every state: none (sign-in chooser Account / CUB), Account only (profile row, "Sign in to CUB", "Account settings", "Log out"), CUB only (CUB's own profile list via `Lampa.Account.Profile.select()` plus "Sign in to Account"), both (profile row, "Switch CUB profile", "Account settings", "Log out"); return focus to `head` on back
- [ ] gate every CUB item on `window.lampa_settings.account_use`, read at call time (CLAUDE.md), so a build with CUB disabled never offers CUB
- [ ] escape every server-provided string (`name`, `email`) with `$('<i>').text(v).html()` before it reaches `Select`, `Template` or settings rows (they render HTML)
- [ ] never write `account*` storage keys; read CUB state only via `Lampa.Account.Permit`
- [ ] manual check: all four states match screens 7–8; CUB profile switching still works; `account_use = false` shows no CUB items
- [ ] run the ES5 check - must pass before next task

### Task 13: Add sign-in and sign-out flows to the add-on

**Files:**
- Modify: `svtlv/account.js`
- Modify: `svtlv/account.css`

- [ ] TV (`Lampa.Platform.tv()`): `POST /api/v1/auth/device/start` with `X-Lampa-Csrf: 1`, open `Lampa.Modal` (`size: 'full'`) using the `account-modal-split` layout: `Lampa.Utils.qrcode(verification_uri_complete, …)`, grouped `user_code`, pending status and countdown (screen 3A); poll at the server interval; handle pending/slow_down/denied/expired; back cancels polling
- [ ] elsewhere: navigate to `/api/v1/auth/login?return=<current path>` (screen 3B / 6); on start, read and remove a `#svtlv-login=ok|failed` fragment and show the matching Noty
- [ ] on success: `Lampa.Noty.show` "Signed in as <escaped email>" (Noty renders HTML), refresh icon and settings (`Lampa.Settings.update()` when open)
- [ ] log out: `Lampa.Select` confirmation (screen 5) → `POST /api/v1/auth/logout` with the CSRF header → refresh; network failure shows a Noty and keeps state
- [ ] manual check in desktop and phone browsers against a local Keycloak or the Test realm: redirect login, a failed login, logout, CUB untouched; a display name containing HTML is shown literally
- [ ] run the ES5 check - must pass before next task

### Task 14: Update backend and repository documentation

**Files:**
- Modify: `backend/README.md`
- Modify: `CLAUDE.md`
- Modify: `AGENTS.md`
- Modify: `docs/settings-sync-backend-design.md`

- [ ] `backend/README.md`: auth routes, config keys and placeholders, cookie/session behavior, 401 vs 503, local testing with a fake or local Keycloak
- [ ] `CLAUDE.md` and its mirror `AGENTS.md`: remove "user-data routes answer 401 until Plan 2" and the published API port; update "placeholders resolved only in `Database.Password` and `DataKey`"; document the `svtlv/` add-on rules (ES5 gate, marked `index.html` block, no `app.min.js` edits) and that `svtlv/` is a top-level directory that intentionally ships in `lampa-web` (the `.dockerignore` rule); the `/api` proxy
- [ ] design: update §3.1's "published on the same host port" sentence; record Plan 2 deviations under §14 (non-secret placeholders `LAMPA_PUBLIC_URL`/`LAMPA_KEYCLOAK_ISSUER`, session lifetimes as constants, any others found)
- [ ] run `make test` and `make lint` - must pass before next task

### Task 15: Verify acceptance criteria
- [ ] verify every Overview item is implemented and design §5, §8, §10.5, §12 match the code
- [ ] verify edge cases: Keycloak down (API up, `/health` Degraded, existing sessions work, login recovers when it returns), expired session, DB down (`503`, not `401`), open-redirect attempts, CSRF rejections, `account_use=false`, missing add-on file
- [ ] run full test suite in WSL: `cd backend && make test && make race && make lint`
- [ ] run the ES5 check and the compose `config` check
- [ ] verify coverage ≥ 80% for new backend code (excluding mocks)

### Task 16: [Final] Update documentation
- [ ] update `backend/README.md` if needed (the root `README.md` is upstream-owned, design §10.1)
- [ ] update CLAUDE.md / AGENTS.md if new patterns discovered
- [ ] move this plan to `docs/plans/completed/` **only after every Post-Completion item below
      has succeeded** (Keycloak client, production deploy, on-device TV checks on both engines,
      rollback drill); until then the plan stays in `docs/plans/`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Keycloak setup** (design §5.6):
- create the confidential client `svtlv-lampa` in realm `svtlv`: Standard flow on, OAuth 2.0
  Device Authorization Grant on, direct access grants off, PKCE method `S256` required, redirect
  URI `https://<lampa-domain>/api/v1/auth/callback`, no web origins
- optional `picture` user-attribute mapper for avatars
- the issuer reachability pre-flight is done in Task 1; re-check after the deploy

**Deployment**:
- add repository variable `LAMPA_KEYCLOAK_ISSUER` and secret `LAMPA_KEYCLOAK_CLIENT_SECRET`
  (`LAMPA_PUBLIC_URL` is derived from `LAMPA_DOMAIN`)
- confirm the outer TLS proxy forwards `/api/v1/*`, the `Origin` header and `Set-Cookie`
  unchanged
- merge to `svtlvtv`, dispatch the deploy workflow (Production), wait for `svtlvtv_lampa_api`
  healthy; confirm `Svtlv.Monitoring.Service` shows the `keycloak` advisory check

**Manual verification**:
- desktop browser and phone browser: redirect login, avatar/initials in the header, logout
  confirmation, session survives a browser restart
- `lampa-app/LAMPA` on the Android TV box, with the saved URL `https://<lampa-domain>`: device
  login via QR and via typed code, denied and expired codes, logout; **restart the app and
  confirm the session persists — on both the SysView (WebView) and XWalk engines**
- CUB coexistence: sign in to CUB and Account in either order, switch CUB profile from the
  Account menu, sign out of each independently
- `lampa_settings.account_use = false`: the Account icon and entry still work, no CUB items
- a file://-origin shell (if available) shows no Account UI and keeps CUB's icon
- session expiry: `docker exec svtlvtv_lampa_db psql -U lampa -c "update lampa_session set
  expires_at = now() - interval '1 minute' where user_id = '<id>'"`, reload, confirm re-login is
  required
- security review: cookie attributes in devtools, CSRF rejection with a foreign `Origin`, no
  Keycloak tokens in storage or logs, HTML in the Keycloak display name shown literally

**Rollback drill**:
- revert the merge commit on `svtlvtv`, redeploy, confirm Lampa works anonymously and CUB's own
  icon is back. The `lampa_session` table stays: the vendored goose resolver ignores an applied
  version higher than the embedded set (`vendor/github.com/pressly/goose/v3/internal/gooseutil/
  resolve.go`), so the Plan 1 image starts against a database at version 2 — confirm it in the
  drill. Sessions created before the revert become valid again on a Plan 2 redeploy only because
  the table is retained; running the `00002` down migration would drop them
