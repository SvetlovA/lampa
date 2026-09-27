# Plan 2: Keycloak Authentication and the Account Add-on

## Overview
- Implements roadmap **Plan 2** from `docs/settings-sync-backend-design.md` §14, with the
  decisions recorded in §5.1–§5.6, §8, §10.5 and §12 of that document.
- Backend: replace `api.DenyAll` with real Keycloak authentication in `lampa-api` — the OAuth 2.0
  Device Authorization Grant for TVs, Authorization Code + PKCE for phones and computers,
  stateless sealed `HttpOnly` session cookies (no session table) revalidated against Keycloak
  on every authenticated request, `GET /api/v1/session`, logout, CSRF protection, and the
  `keycloak` advisory health check.
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
- **TV client**: `github.com/lampa-app/LAMPA` loads the configured URL directly
  (`MainActivity.onBrowserInitCompleted` → `browser.loadUrl(LAMPA_URL)`), engines SysView
  (Android WebView) and XWalk (Crosswalk); no cookie code, WebView defaults apply.
- **Toolchain**: all `make` targets run inside WSL Ubuntu from `backend/` (Windows Go has no
  cgo, so `-race` fails there). Go style follows the local Ralphex checkout.
- ⚠️ **Keycloak pre-flight (Task 1) not yet run**: it needs shell access to the production
  server, which the automated run does not have. The Keycloak address is still undecided and
  checks (a)–(c) are unverified; run them before the production deploy (Post-Completion) and
  record the result here. If they fail, design the transport path (design §5.6) before deploying.
- **Config as implemented (Task 1)**: `config.Config.Auth` (`config.Auth{PublicURL, Issuer,
  ClientID, ClientSecret, SecureCookies}`). `PublicURL` is stricter than planned: it must be
  exactly the lowercase `scheme://host[:port]` a browser sends as `Origin` (no trailing slash,
  no default port), so the Task 7 Origin check can compare strings. `Issuer` keeps its path and
  any trailing slash as given (go-oidc compares it byte for byte) and rejects user, query and
  fragment.
- **Cookie sealing as implemented (Task 2)**: `auth.CookieSealer` (`NewCookieSealer(dataKey)`,
  `Seal(purpose, payload, expiresAt)`, `Open(purpose, value, now, out)`). A key is derived per
  `Purpose` in the package's `purposes` list (Task 3 adds `lampa-session-v1` there). Value =
  unpadded base64url of `0x01 | 4-byte SHA-256(label) tag | 12-byte nonce | GCM(JSON {exp, data})`;
  the cleartext header is authenticated as additional data and exists only so a cross-purpose
  value fails as `ErrCookieWrongPurpose` instead of `ErrCookieTampered` (the fixed overhead is
  ~45 bytes before base64, which counts against the 4000-byte cookie bound). All open failures
  wrap `ErrCookieInvalid`; an authentic payload of the wrong JSON shape does not (caller bug).
- **Session cookie as implemented (Task 3)**: `auth.Cookies` (`NewCookies(sealer, secure)`) owns
  every auth cookie: `IssueSession`/`ReplaceTokens`/`RenewSession` return the `Session` actually
  written (picture possibly dropped), `OpenSession`, `ClearSession`, and the unexported
  `newCookie(name, path, value, maxAge)` Task 6 uses for `lampa_login`/`lampa_device` (constants
  `LoginCookie`/`LoginPath`/`DeviceCookie`/`DevicePath`). Session times are second-precision UTC.
  Name is cut to 64 runes; an email over 254 bytes is dropped rather than truncated (like the
  picture). Errors: `ErrNoSession` (wraps `api.ErrUnauthenticated`, and the sealer error),
  `ErrSessionTooLarge` (nothing written), `ErrInvalidSession` (non-UUID sub, missing refresh token
  or expiry, or already expired, on write). A session with under a second of idle time left is
  treated as expired so `Max-Age` is never 0.
- **Keycloak client as implemented (Task 4)**: `auth.Keycloak` (`NewKeycloak(KeycloakConfig{Issuer,
  ClientID, ClientSecret, RedirectURL})`) with `AuthCodeURL`, `Exchange`, `StartDevice` (returns
  `DeviceStart` with `ExpiresIn`/`Interval` durations, interval defaulting to 5s),
  `PollDevice` (returns `DeviceResult{Status, Profile, Tokens}`, statuses `DevicePending`,
  `DeviceSlowDown`, `DeviceExpired`, `DeviceDenied`, `DeviceAuthorized`), `Refresh` and
  `Introspect` (return a `Verdict`: `VerdictActive`/`VerdictRevoked`, `VerdictUnknown` only with an
  error). Every method bounds its call (discovery included) to 3 s; discovery waits on a
  one-slot channel instead of a mutex, so a queued caller still honors its own context during an
  outage. Errors from `oauth2.RetrieveError` are rewritten to status and error code only.
- **Authenticator as implemented (Task 5)**: `auth.SessionAuthenticator`
  (`NewSessionAuthenticator(cookies, keycloak, logger)`) with an unexported
  `session(w, r) (Session, error)` that Task 6's `/session` handler reuses before `RenewSession`
  (it returns the session as re-issued after a refresh). Revoked sessions fail with
  `ErrSessionRevoked` (wraps `api.ErrUnauthenticated`) after `ClearSession`. `api.Authenticator` is
  now `Authenticate(w, r)`; `api.WriteError` / `api.WriteJSON` are exported; the middleware answers
  `503 session_unavailable` for any non-`ErrUnauthenticated` error.

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
- **integration tests**: OIDC flows against an `httptest` fake Keycloak (discovery, JWKS signed with
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
- **`pkg/auth`** (new) owns everything identity-related: cookie sealing, the stateless session
  cookie, the Keycloak/OIDC client, the auth HTTP handlers and the session `Authenticator`.
  `pkg/api` keeps owning the middleware chain and mounts the auth handlers next to user data.
- **Two login flows, one session**: both end in `auth.IssueSession(w, profile, tokens)` → a sealed
  `lampa_session` cookie carrying the profile (`sub`, `name`, `email`, `picture`) copied from the
  verified ID token plus its two expiries and the Keycloak refresh token. **Sessions are stateless
  (user decision 2026-09-26)**: no session table, no purge job, no DB access for auth. The
  refresh token is the only Keycloak token kept, and only sealed inside the cookie.
- **Revalidation (user decision 2026-09-27, design §5.3.1)**: every authenticated request makes
  one Keycloak call with that refresh token (a refresh grant when due, else introspection), so a
  session ended, or a user disabled or deleted, in Keycloak is signed out on the next request.
  Keycloak failures fail open: the session keeps working, only new logins break.
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
  functional and independent; the add-on disables itself on non-HTTP(S) origins (`file://`,
  app origins) or when the API is unreachable, leaving CUB's own icon visible.
- **Tailscale over HTTP** (user decision 2026-09-26, design §5.1): Lampa is reached only inside
  the Tailscale network at `http://<tailscale-ip>:8092`, so every cookie sets `Secure` only when
  `PublicURL` is `https://`. Keycloak may also be HTTP inside the tailnet; the configured
  issuer must equal the `iss` Keycloak actually issues (checked by the Task 1 pre-flight).

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
  `PublicURL` (today `http://<tailscale-ip>:8092`) gives the redirect URI
  (`<PublicURL>/api/v1/auth/callback`), the allowed `Origin` and whether cookies are `Secure`.
  `PublicURL` is an absolute `http`/`https` origin (no path); `Issuer` is an absolute
  `http`/`https` URL that must equal the `iss` Keycloak issues. **Deviation from design §3.1 / CLAUDE.md** ("only
  secrets are placeholders"): `PublicURL` and `Issuer` are not secret but are placeholders
  because the Lampa domain is deliberately kept out of the public repository (`LAMPA_DOMAIN` is a
  GitHub secret). `LAMPA_PUBLIC_URL` and `LAMPA_KEYCLOAK_ISSUER` are repository variables
  (`LAMPA_PUBLIC_URL` must equal the address saved on devices — a mismatch would 403 every POST
  through the Origin check); only the client secret is a secret.
  Recorded under design §14 in Task 14.
- **Session lifetimes are constants** in `pkg/auth` (30-day idle, 180-day absolute, design
  §5.3), not configuration: tests inject a clock, and embedded config cannot be changed on the
  server without a redeploy anyway.
- **Session cookie** (design §5.3): `lampa_session=<base64url(AES-256-GCM(JSON))>; Path=/api;
  HttpOnly; SameSite=Lax; Max-Age=<seconds to idle expiry>` plus `Secure` when `PublicURL` is
  https, sealed with the HKDF purpose
  `lampa-session-v1`. Payload `{sub, name, email, picture, created_at, idle_expires_at,
  refresh_token, refreshed_at, refresh_expires_at}`.
- **Sliding**: only `GET /api/v1/session` slides the idle expiry. It first rejects a cookie past
  `idle_expires_at` or past `created_at + 180d`, then re-issues with `created_at` preserved,
  `idle_expires_at = min(now + 30d, created_at + 180d)` and a matching `Max-Age`. Other API
  requests validate and revalidate but re-issue the cookie only after a Keycloak refresh. The
  add-on calls `/session` on every app start **and every 12 hours while Lampa stays open**, so a
  TV left running keeps its session.
- **Revalidation** (design §5.3.1), in the `Authenticator` and in `/session`, after the expiry
  checks (one revalidation call; the first use after a restart may add a discovery call; refresh
  through `golang.org/x/oauth2`, introspection through a small `net/http` adapter, Task 4): refresh is due when `now >= refreshed_at + min(24h, (refresh_expires_at -
  refreshed_at) / 2)` → `refresh_token` grant, re-issue the cookie with the new refresh token
  (`created_at` preserved); otherwise introspect the refresh token
  (`token_type_hint=refresh_token`, `introspection_endpoint` from discovery). Client
  credentials, 3-second timeout, redirects disabled, no success cache. Introspection `200
  {"active":false}` or refresh `400 invalid_grant` → revoked: clear the cookie, `401` (or
  anonymous `/session`). Introspection `200 {"active":true}`, or refresh `200` with a non-empty
  `refresh_token` and a positive `refresh_expires_in` → valid. Everything else (timeout,
  network error, other status, malformed/incomplete body, `active` missing or not a JSON
  boolean, `invalid_client`, a response over 64 KiB, a refreshed cookie that no longer fits) →
  Keycloak failure: keep the existing session and cookie, log `[WARN]` without token values. Cookies without a refresh token (none
  exist before this plan ships) are rejected.
- **Size bound**: the whole serialized `Set-Cookie` value (`http.Cookie.String()`, name and
  attributes included) stays ≤ 4000 bytes, checked on issue, `ReplaceTokens` and renewal
  (browsers cap a cookie at 4096); `name`/`email` are length-limited and an over-long `picture`
  URL is dropped, never truncated. The refresh token is never dropped or truncated: if the cookie
  still does not fit at login, the login fails (`/#svtlv-login=failed` or the device poll's
  error) and is logged; after a refresh it counts as a Keycloak failure (existing session kept).
- **Revoke all**: bump the purpose label to `lampa-session-v2` in code; never rotate
  `LAMPA_API_DATA_KEY` (it also seals user data at rest). Per-user revocation is done in
  Keycloak (disable or delete the user, or end their sessions) and takes effect on the next
  request; ending one Keycloak session revokes every app session tied to it (browser logins on
  one device can share one SSO session).
  Accepted costs: logout clears only this device's copy and leaves the Keycloak session alone (a
  phone login shares its browser's SSO session with other Svtlv apps), so a copied cookie stays
  valid until that Keycloak session ends or the cookie expires; a user disabled while Keycloak
  is unreachable keeps access until it answers again.
- **No DB dependency**: sessions, `/session` and logout work with PostgreSQL down, and with
  Keycloak down (revalidation fails open; each request then waits up to the 3-second timeout).
- **Unavailable vs signed out**: the add-on treats `503`/network errors from the API as "service
  unavailable" (keeps its last state, no sign-out).
- **Cookies for flows**: `lampa_login` (Path `/api/v1/auth/callback`, Max-Age 600) and
  `lampa_device` (Path `/api/v1/auth/device`, Max-Age = device `expires_in`), both
  `HttpOnly; SameSite=Lax` plus `Secure` on https (Lax, not Strict: the callback arrives as a cross-site
  top-level redirect from Keycloak), AES-256-GCM sealed JSON with an expiry inside the sealed
  payload.
- **Browser-facing login failures**: Keycloak down at `/auth/login`, a missing/expired
  `lampa_login` at `/callback`, a state mismatch or a Keycloak `error` parameter all redirect to
  `/#svtlv-login=failed` (clearing `lampa_login`), never a JSON page; success redirects to the
  return path with `#svtlv-login=ok`. The add-on reads and removes that fragment to show its
  Noty.
- **Auth failures on protected routes**: the `authenticate` middleware answers `401` only for
  `ErrUnauthenticated`; any other error is logged and answers `503 session_unavailable`, so a
  client is never told it is signed out because a dependency failed (design §5.3).
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

- [x] **pre-flight (before any code)** (skipped - not automatable: needs shell access to the production server; ⚠️ must be run before the production deploy, see Context): decide the Keycloak address (public HTTPS or HTTP on the tailnet), then on the server run `docker exec svtlvtv_lampa_api curl -fsS <issuer>/.well-known/openid-configuration` and check (a) the returned `issuer` equals `<issuer>` exactly (a realm with a fixed public HTTPS hostname returns `https://…` even over a Tailscale address), (b) every advertised endpoint the API calls (`token_endpoint`, `device_authorization_endpoint`, `jwks_uri`) is reachable from the container, (c) the `authorization_endpoint` users open is reachable from a phone and a browser on the tailnet (the device `verification_uri` only comes from a device authorization response, so it is checked after the client exists — Post-Completion). `go-oidc` uses the advertised endpoints as-is, and `oidc.InsecureIssuerURLContext` only changes which issuer is expected, not those URLs. Keep the shared `svtlv` realm's hostname/issuer unchanged (other Svtlv clients depend on it) unless changing it is separately approved. Record the result in Context; if (a)–(c) cannot all hold, stop and design an explicit transport path in this plan and design §5.6 before continuing
- [x] add `Auth` settings (PublicURL, Issuer, ClientID, ClientSecret) to `settings`/`Config`, strict decoding unchanged
- [x] resolve `{LAMPA_PUBLIC_URL}`, `{LAMPA_KEYCLOAK_ISSUER}` and `{LAMPA_KEYCLOAK_CLIENT_SECRET}` with explicit `resolve` calls; missing values fail naming the variable, never the value
- [x] validate: `PublicURL` an absolute `http`/`https` origin without path, `Issuer` an absolute `http`/`https` URL, non-empty client ID/secret; expose `SecureCookies = PublicURL is https`; `String()`/`GoString()` redact the client secret
- [x] update `main_test.go`'s `testSettings` fixture with an `Auth` section and its env maps with the three new variables, so the existing run tests keep passing
- [x] write tests for successful load per environment (Test, Production) including redaction
- [x] write tests for errors: missing variables, `PublicURL` with a path or another scheme, non-http(s) issuer, unknown key
- [x] run `make test` and `make lint` - must pass before next task

### Task 2: Add OIDC dependencies and cookie sealing

**Files:**
- Modify: `backend/go.mod`, `backend/go.sum`, `backend/vendor/`
- Create: `backend/pkg/auth/seal.go`
- Create: `backend/pkg/auth/seal_test.go`

- [x] add `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2`; `go mod tidy && go mod vendor` (added with `go get` + `go mod vendor`: both are still `// indirect` and `go mod tidy` would drop them until Task 4 imports them; run `go mod tidy && go mod vendor` there)
- [x] create `CookieSealer` (named apart from `storage.Sealer`, both are built in `main`) in `pkg/auth/seal.go`: HKDF-SHA256 (`crypto/hkdf`) key per purpose label (`lampa-login-v1`, `lampa-device-v1`) from the data key, AES-256-GCM seal/open of JSON payloads with an embedded expiry
- [x] reject expired, tampered, truncated and cross-purpose values with distinct wrapped errors
- [x] write tests for round trip and per-purpose key separation
- [x] write tests for tampering, truncation, expiry and wrong purpose
- [x] run `make test` and `make lint` - must pass before next task

### Task 3: Add the stateless session cookie

**Files:**
- Create: `backend/pkg/auth/session.go`
- Create: `backend/pkg/auth/session_test.go`

- [x] define `Profile{UserID, Name, Email, Picture}`, `Tokens{RefreshToken, RefreshExpiresAt}` and the session payload `{sub, name, email, picture, created_at, idle_expires_at, refresh_token, refreshed_at, refresh_expires_at}`; 30-day idle and 180-day absolute constants
- [x] implement `IssueSession(w, profile, tokens, now)` (new session: `created_at = refreshed_at = now`), `ReplaceTokens(w, session, tokens, now)` (after a refresh: new refresh token, `refreshed_at = now`, `created_at`/`idle_expires_at` unchanged), `OpenSession(r, now) (Session, error)` (rejects missing, tampered, idle-expired, absolute-expired and refresh-token-less cookies with `ErrNoSession` wrapping `api.ErrUnauthenticated`) and `RenewSession(w, session, now)` (preserves `created_at`, `idle_expires_at = min(now+30d, created_at+180d)`, `Max-Age` matching); sealed with `CookieSealer` purpose `lampa-session-v1`
- [x] bound the serialized cookie (`Cookie.String()` ≤ 4000 bytes, attributes included) on issue, `ReplaceTokens` and renewal: length-limit `name`/`email`, drop an over-long `picture`, never truncate a URL, never drop or truncate the refresh token (fail with a distinct error instead)
- [x] set `Secure` from `SecureCookies` on every cookie (session, `lampa_login`, `lampa_device`); add `ClearSession(w)` with the same attributes and `Max-Age=-1`
- [x] write tests (injected clock): issue/open round trip, renewal preserves `created_at`, `ReplaceTokens` keeps both expiries, idle expiry, absolute cap reached through repeated renewals, `Max-Age` values, cookie attributes with `Secure` on for https and off for http
- [x] write tests for errors: tampered, truncated, wrong purpose label, oversized profile (picture dropped, cookie within bound with a realistic ~1 KB refresh token), cookie that cannot fit, missing refresh token, non-UUID `sub` rejected
- [x] run `make test` and `make lint` - must pass before next task

### Task 4: Add the Keycloak OIDC client

**Files:**
- Create: `backend/pkg/auth/keycloak.go`
- Create: `backend/pkg/auth/keycloak_test.go`

- [x] create `Keycloak` with **lazy discovery**: construction never calls Keycloak; the first use runs `oidc.NewProvider` under a mutex and caches the provider, `oauth2.Config` (client id/secret, redirect URI, scopes `openid profile email`) and ID-token verifier; a failed discovery returns an error and is retried on the next use, so a Keycloak outage never stops the API
- [x] implement `AuthCodeURL(ctx, state, nonce, verifier)` with S256 PKCE and `Exchange(ctx, code, verifier, nonce) (Profile, Tokens, error)` that verifies the ID token and nonce and returns the refresh token with its expiry (`refresh_expires_in`); scopes never include `offline_access`
- [x] implement `StartDevice(ctx)` via `oauth2.Config.DeviceAuth` **passing the client secret explicitly** (`oauth2.SetAuthURLParam("client_secret", …)`: upstream `deviceauth.go` sends only `client_id` and `scope`, and Keycloak authenticates confidential clients at its device endpoint)
- [x] implement a single-shot `PollDevice(ctx, deviceCode)` as a hand-rolled, client-authenticated `grant_type=urn:ietf:params:oauth:grant-type:device_code` token request (`oauth2.Config.DeviceAccessToken` sleeps one interval before its first request and blocks until done), mapping `authorization_pending`, `slow_down`, `expired_token`, `access_denied` to typed results; the device-flow ID token carries no nonce, so it is verified without the nonce check; success returns `Profile` and `Tokens` like `Exchange`
- [x] extract `Profile{UserID, Name, Email, Picture}`: `sub` must parse as UUID, name falls back to `preferred_username` then `email`, `picture` only if absolute https
- [x] implement `Refresh(ctx, refreshToken) (Tokens, Verdict, error)` with **`golang.org/x/oauth2`** (the Go team's OAuth 2.0 library, outside the standard library; user decision 2026-09-27 to use established libraries): `Config.TokenSource(ctx, &oauth2.Token{RefreshToken: rt, Expiry: <past>}).Token()` so the grant always runs; `Endpoint.AuthStyle` set explicitly to `oauth2.AuthStyleInHeader` (auto-detection would retry with the other style and break the one-call rule); `Revoked` only when `errors.As(err, *oauth2.RetrieveError)` has **both** `Response.StatusCode == 400` and `ErrorCode == "invalid_grant"` (the library also returns `RetrieveError` for 5xx and for a `200` error body); the new refresh token read from the raw `tok.Extra("refresh_token")` and required non-empty (the library falls back to the old token when the response has none); `refresh_expires_in` from `tok.Extra` (a JSON number arrives as `float64`) required finite, positive, integral and ≤ 3650 days; never log `err`, `RetrieveError.Body` or `ErrorDescription` (they embed response data)
- [x] implement `Introspect(ctx, refreshToken) (Verdict, error)` as a **narrow `net/http` adapter** (`net/http` is the Go standard library): `golang.org/x/oauth2` does not expose RFC 7662 introspection, and the `github.com/zitadel/oidc/v3` `rs.Introspect` helper sends only `token`; smaller RFC 7662 libraries exist but none established enough to add a dependency for one request — without `token_type_hint=refresh_token` Keycloak 26.5.7 (`TokenIntrospectionEndpoint`) introspects it as an access token. One form `POST` (`token`, `token_type_hint=refresh_token`) to the discovery `introspection_endpoint` with the same Basic client authentication, body read through `io.LimitReader` and rejected over 64 KiB, `active` decoded as `*bool` so a missing, `null`, string or number value is an error (Go's zero `false` must never sign anyone out); `Active`/`Revoked` only for a `200` with a real boolean
- [x] shared by both: one `http.Client` reused and passed to `x/oauth2` via the `oauth2.HTTPClient` context value, the overall 3-second timeout from the request context, `CheckRedirect` returning `http.ErrUseLastResponse`, and a transport wrapper that fails any response body over 64 KiB (`x/oauth2`'s own 1 MiB `LimitReader` truncates rather than rejects); every other outcome is an error (Keycloak failure); error messages never contain token values
- [x] ➕ run `go mod tidy && go mod vendor` once `go-oidc`/`oauth2` are imported (Task 2 left them `// indirect`, which pulls `go-jose` in)
- [x] write tests against an `httptest` fake Keycloak (discovery, JWKS with a test RSA key, device and token endpoints) for the success paths of both flows; the fake **rejects device and token requests without the client secret**, as a confidential client in Keycloak does
- [x] write tests for errors: bad signature, wrong audience/issuer, nonce mismatch, non-UUID `sub`, each device error code; lazy discovery: Keycloak down → error → Keycloak back → next call works
- [x] write tests for revalidation against the fake (which rejects introspection and refresh requests without client authentication, and records every request so the one-call rule and `token_type_hint` are asserted): introspection `active` true/false, refresh success, `invalid_grant` → `Revoked`; and failures: `5xx`, `401 invalid_client`, a `302` (not followed), timeout, non-JSON body, an over-limit body, `active` missing/`null`/`"false"`/`0`, refresh `200` with an empty or missing refresh token (not silently replaced by the old one), a zero, negative, fractional, non-numeric or overflowing `refresh_expires_in`, a `5xx` or `200` error body from the token endpoint (not `Revoked`), a token response over 64 KiB
- [x] run `make test` and `make lint` - must pass before next task

### Task 5: Add the session Authenticator and 503 for non-auth failures

**Files:**
- Create: `backend/pkg/auth/authenticator.go`
- Create: `backend/pkg/auth/authenticator_test.go`
- Modify: `backend/pkg/api/identity.go`
- Modify: `backend/pkg/api/identity_test.go`
- Modify: `backend/pkg/api/server.go`
- Modify: `backend/pkg/api/server_test.go`

- [x] change the seam to `Authenticate(w http.ResponseWriter, r *http.Request) (string, error)` (it must clear or re-issue the cookie); update `DenyAll` and the middleware
- [x] implement `api.Authenticator` over `OpenSession` plus revalidation (Technical Details): return `sub`; missing/tampered/expired → `api.ErrUnauthenticated`; revoked → clear the cookie and `api.ErrUnauthenticated`; refreshed → `ReplaceTokens`; Keycloak failure → `sub` plus a `[WARN]`; no DB access
- [x] change the `authenticate` middleware: `401 unauthenticated` only for `ErrUnauthenticated`; any other error is logged and answers `503 session_unavailable` (design §5.3)
- [x] export `api.WriteError` / `api.WriteJSON` (keep the JSON shape) so `pkg/auth` handlers reuse them (`pkg/auth` already imports `pkg/api`, no cycle)
- [x] write tests (fake Keycloak via a consumer-side interface, injected clock) for valid session with introspection, refresh due (cookie re-issued, `created_at` kept), no cookie, malformed cookie, expired session, revoked by introspection and by `invalid_grant` (cookie cleared), Keycloak failure (session kept, no cookie change), a refreshed cookie that no longer fits (session kept), a cookie without a refresh token (rejected); two parallel requests with one due cookie against a fake with rotation off: both refresh, both keep `created_at`, and requests with the old and the new refresh token are both accepted
- [x] write tests for the middleware: `ErrUnauthenticated` → `401`, any other error from a fake authenticator → `503` and logged; update existing `identity_test.go` expectations
- [x] run `make test` and `make lint` - must pass before next task

### Task 6: Add the auth HTTP handlers

**Files:**
- Create: `backend/pkg/auth/handlers.go`
- Create: `backend/pkg/auth/handlers_test.go`

- [ ] `GET /api/v1/session`: anonymous or profile JSON; revalidate like the `Authenticator` (revoked → clear cookie, anonymous); on a valid session `RenewSession` (the only place that slides the idle expiry), carrying a refreshed token when one was issued; always `200`
- [ ] `GET /api/v1/auth/login?return=`: validate the return path (local absolute path only, default `/`), seal login state into `lampa_login`, redirect to Keycloak; `GET /api/v1/auth/callback`: open the cookie, check state, exchange, create session, set cookie, clear `lampa_login`, redirect to the return path + `#svtlv-login=ok`; every failure redirects to `/#svtlv-login=failed` (Technical Details), never a JSON page
- [ ] `POST /api/v1/auth/device/start` and `POST /api/v1/auth/device/poll` per Technical Details; success clears `lampa_device` and sets the session cookie with the returned refresh token
- [ ] `POST /api/v1/auth/logout`: `ClearSession`, `204` also when already signed out
- [ ] write tests (fake `Keycloak` via a consumer-side interface, moq into `mocks/` if useful) for every success path
- [ ] write tests for errors: open-redirect attempts (`//evil`, `https://evil`, `\\evil`, `/\evil`), state mismatch, missing/expired flow cookies, Keycloak down at login, each device result, expired/tampered/revoked session on `/session` → anonymous `200` and a cleared cookie, Keycloak failure on `/session` → still signed in, `Max-Age` clamped near the absolute cap, a login whose cookie cannot fit → failure redirect
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

### Task 8: Wire authentication and the Keycloak health check in main

**Files:**
- Modify: `backend/cmd/lampa-api/main.go`
- Modify: `backend/cmd/lampa-api/main_test.go`
- Modify: `backend/pkg/health/health.go`
- Modify: `backend/pkg/health/health_test.go`

- [ ] add `health.KeycloakCheck` (advisory) fetching the issuer discovery document with a short timeout
- [ ] in `main`, build `CookieSealer`, the lazily-discovering `Keycloak`, the handlers and the `Authenticator`; replace `api.DenyAll{}` and the temporary auth handler
- [ ] write tests for the Keycloak check (healthy, unreachable, bad status) and its `Degraded` aggregation in `/health`
- [ ] write tests for main wiring: API starts with Keycloak down, user-data returns `401` without a session and `200` with one (fake Keycloak), the next request after the fake revokes the session returns `401`, an existing session keeps working when the fake Keycloak is stopped, `/session` and logout work with the DB down
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

- [ ] read `LAMPA_PUBLIC_URL` and `LAMPA_KEYCLOAK_ISSUER` from `vars.*` and `LAMPA_KEYCLOAK_CLIENT_SECRET` from `secrets`; add them to the required list, validation (`PublicURL` an `http(s)://host[:port]` origin, `http(s)://` issuer, non-empty secret) and the server `.env`
- [ ] add a pre-deploy guard: when `LAMPA_PUBLIC_URL` is `http://`, require `LAMPA_BIND_ADDRESS` to be set explicitly, to be a Tailscale address (`100.64.0.0/10` or `fd7a:115c:a1e0::/48`) and to equal the `PublicURL` host, and `LAMPA_PORT` to equal its port; reject empty, `0.0.0.0`, LAN and public binds before anything is built or stopped (the workflow currently defaults an unset bind to `0.0.0.0`). Local `devops/.env` development is unaffected
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
- [ ] gate on `location.protocol` being `http:` or `https:` (file/app origins stay anonymous); `GET /api/v1/session` via `$.ajax` with timeout on start and every 12 hours (cookie heartbeat); if the first call fails disable itself, later failures (`503`, network) keep the last state
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

- [ ] `backend/README.md`: auth routes, config keys and placeholders, stateless cookie sessions, per-request Keycloak revalidation (fail-open) and the accepted costs, the Keycloak realm prerequisites, 401 vs 503, local testing with a fake or local Keycloak
- [ ] `CLAUDE.md` and its mirror `AGENTS.md`: remove "user-data routes answer 401 until Plan 2" and the published API port; update "placeholders resolved only in `Database.Password` and `DataKey`"; document the `svtlv/` add-on rules (ES5 gate, marked `index.html` block, no `app.min.js` edits) and that `svtlv/` is a top-level directory that intentionally ships in `lampa-web` (the `.dockerignore` rule); the `/api` proxy
- [ ] design: update §3.1's "published on the same host port" sentence; record Plan 2 deviations under §14 (non-secret placeholders `LAMPA_PUBLIC_URL`/`LAMPA_KEYCLOAK_ISSUER`, session lifetimes as constants, the `Authenticate(w, r)` seam change, any others found)
- [ ] run `make test` and `make lint` - must pass before next task

### Task 15: Verify acceptance criteria
- [ ] verify every Overview item is implemented and design §5, §8, §10.5, §12 match the code
- [ ] verify edge cases: Keycloak down (API up, `/health` Degraded, existing sessions work, login recovers when it returns), session revoked in Keycloak (next request `401`), expired session, DB down (sessions still work; user data `503`), open-redirect attempts, CSRF rejections, `account_use=false`, missing add-on file
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
  URI `http://<tailscale-ip>:8092/api/v1/auth/callback` (= `LAMPA_PUBLIC_URL` + `/api/v1/auth/callback`),
  no web origins
- optional `picture` user-attribute mapper for avatars
- realm prerequisites for revalidation (design §5.3.1), checked explicitly, not assumed: SSO
  Session Idle ≥ 31 days, SSO Session Max ≥ 180 days, the `svtlv-lampa` client's session
  idle/max unset or no shorter, **Revoke Refresh Token off**; the client does not get
  `offline_access` as a default scope. These realm settings are shared with Svtlv, which applies
  the same revalidation
- the issuer pre-flight is done in Task 1; re-check after the deploy. Once the client exists,
  start one device authorization and confirm its `verification_uri` opens on the phone. With an HTTP (tailnet)
  issuer, the phone used for the TV device login must be on Tailscale

**Deployment**:
- add repository variables `LAMPA_PUBLIC_URL` (`http://<tailscale-ip>:8092`) and
  `LAMPA_KEYCLOAK_ISSUER`, and secret `LAMPA_KEYCLOAK_CLIENT_SECRET`
- confirm the server `.env` binds Lampa to the Tailscale address (`LAMPA_BIND_ADDRESS`), and that
  `http://<server-public-or-lan-ip>:8092` is unreachable: cookies are not `Secure` over HTTP,
  so the Lampa port must never be exposed outside the tailnet
- merge to `svtlvtv`, dispatch the deploy workflow (Production), wait for `svtlvtv_lampa_api`
  healthy; confirm `Svtlv.Monitoring.Service` shows the `keycloak` advisory check

**Manual verification**:
- desktop browser and phone browser: redirect login, avatar/initials in the header, logout
  confirmation, session survives a browser restart
- `lampa-app/LAMPA` on the Android TV box, with the saved URL exactly `LAMPA_PUBLIC_URL`: device
  login via QR and via typed code, denied and expired codes, logout; **restart the app and
  confirm the session persists — on both the SysView (WebView) and XWalk engines**
- CUB coexistence: sign in to CUB and Account in either order, switch CUB profile from the
  Account menu, sign out of each independently
- `lampa_settings.account_use = false`: the Account icon and entry still work, no CUB items
- a file://-origin shell (if available) shows no Account UI and keeps CUB's icon
- session expiry: covered by unit tests with an injected clock; on-device, delete the
  `lampa_session` cookie in devtools and confirm the add-on shows signed out
- revocation, each on a signed-in browser and on the TV: end the user's session in the Keycloak
  admin console → the next `/session` (reload, or wait for the heartbeat) shows signed out; the
  same after disabling the user, and after deleting a test user. Stop Keycloak briefly → the
  signed-in session keeps working and `[WARN]` lines appear; start it → no re-login needed (an
  outage longer than the refresh token or SSO idle lifetime ends the session anyway)
- security review: cookie attributes in devtools (`HttpOnly`, `SameSite=Lax`, no `Secure` on the http URL), CSRF rejection with a foreign `Origin`, no
  Keycloak tokens in Lampa storage, the database or logs (the refresh token exists only sealed in
  `lampa_session`), HTML in the Keycloak display name shown literally

**Rollback drill**:
- revert the merge commit on `svtlvtv`, redeploy, confirm Lampa works anonymously and CUB's own
  icon is back. Sessions issued by Plan 2 become valid again if Plan 2 is redeployed before they
  expire (they are stateless cookies)
