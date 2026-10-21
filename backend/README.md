# lampa-api

Go service for Lampa accounts, user data and TorrServer membership (Plans 1–4 of
[`docs/settings-sync-backend-design.md`](../docs/settings-sync-backend-design.md)). It signs
users in with the Svtlv Keycloak realm, keeps their session in a sealed cookie, stores one
JSONB document per user in PostgreSQL, seals client-selected values with AES-256-GCM,
records per-user torrent membership, and exposes the Svtlv two-tier health contract.

The API is reached only through `lampa-web`'s same-origin `/api/v1/` proxy; its port is not
published. The `svtlv/account.js` add-on uses the session and auth routes;
`svtlv/account-sync.js` applies user data before boot, polls with ETags for live changes
and saves dirty local keys with `If-Match`. Its whitelist and encryption policy come from
`svtlv/storage-scope.json`.
The `svtlv/torrserver-ownership.js` add-on uses the membership routes while continuing to
call TorrServer directly for adding, listing and playback. The API never contacts TorrServer
and receives neither its URL nor its credentials through the membership routes.

## Layout

```text
cmd/lampa-api/     composition root: config → pgx pool → migrations → auth → two listeners → shutdown
pkg/api/           routes, Authenticator seam, middleware (CSRF included), JSON error contract
pkg/auth/          cookie sealing, session cookie, Keycloak client, auth handlers, Authenticator
pkg/config/        layered appsettings files (embedded) with {ENV_VAR} placeholders
pkg/health/        tiered checks, /health and /health/critical
pkg/storage/       documents, credential sealing, torrent membership, postgres store, migrate
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
| `Api.MaxBodyBytes` | `4194304` | request body limit, > 0 |
| `Health.Listen` | `:8081` | health listener, must differ from the API one |
| `Database.Host` / `Port` | `localhost` / `5434` | `lampa-db` / `5432` in Test and Production |
| `Database.Name` / `User` | `lampa` / `lampa` | required |
| `Database.Password` | `{LAMPA_DB_PASSWORD}` | required, never logged |
| `Database.SSLMode` | `disable` | a libpq `sslmode` |
| `DataKey` | `{LAMPA_API_DATA_KEY}` | required, base64 of exactly 32 bytes, never logged |
| `Authentication.Keycloak.Authority` | `https://svtlv.fly.dev/realms/svtlv-test` | Test uses the same realm; Production uses `https://svtlv.fly.dev/realms/svtlv` |
| `Authentication.Keycloak.ClientId` | `svtlv-lampa` | required |
| `Authentication.Keycloak.ClientSecret` | `{LAMPA_KEYCLOAK_CLIENT_SECRET}` | required, never logged |
| `Authentication.Keycloak.RequireHttpsMetadata` | `false` | Production sets `true`, requiring an HTTPS Authority |

The login and logout return URLs use the host and scheme of each browser request. Apache preserves
the browser's Host and replaces `X-Lampa-Proto` with its own request scheme before proxying the API.
The API rejects malformed hosts, compares a supplied `Origin` with that request origin on writes,
and marks cookies `Secure` for HTTPS. Each host gets its own host-only cookie. Register every
browser-facing origin separately in Keycloak; do not use a wildcard redirect URI.

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

### Sealed values and sync policy

Schema v2 stores arbitrary JSON values under top-level `data` keys. Each PUT may include
an `encrypt` array of at most 64 keys. Present values on that list are sealed in place as
`{"$sealed":"base64(nonce+ciphertext)"}` with AES-256-GCM and `LAMPA_API_DATA_KEY`, bound
to the user ID; missing keys are ignored. GET decrypts them and omits unreadable values
with a `[WARN]` log and lists their names in `unreadable_keys` while returning the rest.
The field is omitted when all values are readable. Such a response is `200` even when
`If-None-Match` matches; it keeps the stored ETag so a client can restore surviving
local plaintext with a conditional PUT. Previously synchronized local values in the
frontend's `encrypt` list become dirty when omitted, and a live refresh schedules
their recovery save. While a cached response has unreadable keys, the client omits
`If-None-Match` on refresh and blocks replacement PUTs unless it can supply every
omitted synced value. This also detects repairs that preserve the plaintext ETag.
Client input containing `$sealed` objects is
reserved and rejected. There is no backend list of secret setting names or UI-input guard.

The frontend reads [`svtlv/storage-scope.json`](../svtlv/storage-scope.json) at runtime.
`user` and `user_connection` must sync; `user_optional` is best-effort. Exact keys beat
prefixes and the longest prefix wins. Unknown keys, player settings, plugins and other
connection credentials remain local. Both TorrServer URLs (`torrserver_url`,
`torrserver_url_two`), the shared authentication toggle (`torrserver_auth`), login and
password are portable connection fields; the shipped `encrypt` list selects login/password. Add a key to `user` or `user_optional` to sync it and also to `encrypt`
if it should be sealed. No JS or Go change is required.

The active-link selector `torrserver_use_link` is device-local: `one` (URL 1) by
default, or `two` (URL 2). Each client preserves its stored selection across reloads,
imports, polling, save retries, account replacement and logout. A selector-only change
sends no user-data PUT and never swaps or rewrites either URL. Both URLs share the same
authentication toggle and username/password pair. Logout clears the synced connection
values under the existing ownership rules while leaving the local selector intact.

Older schema-version-2 documents without URL 2 remain valid. The existing document-level
first-login rule still applies: guest values upload only if no account document exists;
an existing document replaces them, including clearing absent synced fields. Later URL
edits and explicit secondary clears sync normally as values and empty-string tombstones.
Refresh older clients after deployment before relying on durable URL-2 sync: their
whole-document writes can omit URL 2 because their allowlist excludes it. Mixed-version
preservation is outside this change.

Clearing or removing a synchronized setting sends an empty-string tombstone. Other
devices apply that empty value, hiding any IndexedDB reserve copy; a successful save
removes its dirty fingerprint. An explicit tombstone never triggers credential recovery.
Fingerprints are refreshed from shared localStorage before dirty checks so other tabs'
saves cannot make clean values appear dirty. A missing must-sync item still available
through Lampa's memory/IndexedDB fallback is persisted before export. This also handles
normal cached defaults. Only a failed write disables uploads, preventing false deletions.

`TestStorageScope_matchBundle` checks extracted bundle storage keys against that inventory.
New unclassified keys, missing/invalid scope data and stale rules fail CI; only a
backend-only checkout without `app.min.js` skips the guard. The backend validates the
shape of `encrypt`, not the frontend's choice of secrets.

## Build, test, lint

On macOS and Linux, use the default `Makefile` from the repository root:

```sh
make -C backend build
make -C backend version
```

Revision timestamps are formatted by Git in UTC, so GNU `date` is not required on macOS.

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

Run the frontend runtime and documentation checks from the repository root:

```sh
node --test tests/*.test.cjs
for file in svtlv/*.js app.min.js; do
  npx --yes acorn@8.14.0 --ecma5 --silent "$file"
done
```

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
  `<origin>/api/v1/auth/callback` as a Valid Redirect URI and `<origin>/` as a Valid Post Logout
  Redirect URI for **each** host and scheme users open. The Authority must equal
  the `iss` Keycloak issues. To use a different realm locally, edit the appsettings file and
  rebuild the API; environment variables do not override non-secret settings.

## Authentication

Two login flows end in the same session:

- **TV** (OAuth 2.0 Device Authorization Grant): `device/start` returns a user code and a
  verification URL to open on a phone. The backend sends an S256 PKCE challenge to Keycloak;
  the device code and verifier are sealed into the `lampa_device` cookie (Path
  `/api/v1/auth/device`) and are never exposed in plaintext to the browser. The TV polls
  `device/poll` at the returned `interval`, one token request with the verifier per poll.
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
- TV logout clears the local session and pending login cookies. Browser logout also sends the
  refresh token to Keycloak's end-session endpoint and visits its logout page, ending SSO for
  apps in that browser. Other browsers keep their sessions; a TV approved from that browser may
  share the Keycloak user session and be signed out too. When Keycloak is down,
  local logout still succeeds and the add-on reports that browser SSO logout could not complete.
  A copied TV cookie stays valid until its Keycloak session ends or the cookie expires. A request
  still in flight (another tab) cannot sign the device back in with a renewed cookie: logout sets
  `lampa_logout` (the logout time, `Max-Age` = the absolute timeout), and a session created up to
  that time is refused; the next login deletes it.

### 401 vs 503

On protected routes, no session, an expired or tampered cookie, or a session Keycloak revoked
→ `401 unauthenticated`. Any other `Authenticator` failure → `503 session_unavailable`, so a
client is never told it is signed out because a dependency failed. Session lookup failures
publish unavailable for signed-in or unresolved identity and fail closed for My Torrents.
A previously confirmed guest retains guest mode during a later lookup outage; an outage
never confirms logout or transfers account memberships. Membership lookup failures fail
closed without changing the resolved session identity.

### CSRF

Every request other than `GET`, `HEAD` and `OPTIONS` needs `X-Lampa-Csrf: 1` and, when it has
an `Origin`, exactly the scheme and Host of that request; otherwise it gets `403 csrf_rejected` before any handler
runs. An absent `Origin` is allowed (old TV WebViews omit it on same-origin requests). No CORS
headers are ever sent.

### Keycloak prerequisites

In realm `svtlv`, the confidential client `svtlv-lampa`: Standard flow and OAuth 2.0 Device
Authorization Grant on, direct access grants off, PKCE `S256` required, exact redirect URI
`<origin>/api/v1/auth/callback` and Valid Post Logout Redirect URI `<origin>/` for each browser
address, no web origins, no `offline_access` default scope,
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
| `POST /api/v1/auth/logout` | `204`, local cookies cleared, `lampa_logout` set | — |
| `POST /api/v1/auth/logout?sso=1` | `200` `{sso_logged_out,logout_url?}`, local cookies cleared; browser follows `logout_url` | — |
| `GET /api/v1/user-data` | `200` v2 `{schema_version, data, updated_at}` + `ETag`, or `304` on matching `If-None-Match` | `401`, `404 user_data_not_found`, `409 account_changed`, `503 storage_unavailable` / `session_unavailable` |
| `PUT /api/v1/user-data` `{schema_version, data, encrypt?}` | `204`, no body, new `ETag`; conditional update with `If-Match`, otherwise upsert | `400 invalid_document` / `unsupported_schema_version` / `invalid_json`, `401`, `409 account_changed`, `412 precondition_failed`, `413 request_too_large`, `415 unsupported_media_type`, `503` |
| `DELETE /api/v1/user-data` | `204`, idempotent | `401`, `409 account_changed`, `503` |
| `GET /api/v1/torrservers/{server_id}/hashes` | `200` `{"hashes":[...]}`; sorted, empty array when no claims exist | `400 invalid_server_id`, `401`, `503 storage_unavailable` / `session_unavailable` |
| `PUT /api/v1/torrservers/{server_id}/hashes/{hash}` | `204`, idempotent claim, no body required | `400 invalid_server_id` / `invalid_hash`, `401`, `503` |
| `DELETE /api/v1/torrservers/{server_id}/hashes/{hash}` | `204`, idempotent removal of only this user's claim | `400 invalid_server_id` / `invalid_hash`, `401`, `503` |

Every POST, PUT and DELETE can also answer `403 csrf_rejected` (see [CSRF](#csrf)). `HEAD`
never starts a login or slides a session.

Other methods get `405 method_not_allowed` with `Allow`, other paths `404 not_found`, panics
`500 internal_error`. Errors are `{"error": {"code": "...", "message": "..."}}` with short,
generic messages.

### User-data document v2

Example PUT body (`Content-Type: application/json`, `X-Lampa-Csrf: 1`):

```json
{
  "schema_version": 2,
  "data": {
    "language": "ru",
    "torrserver_url": "http://ts:8090",
    "torrserver_url_two": "http://192.168.1.10:8090",
    "torrserver_auth": "true",
    "torrserver_login": "user",
    "torrserver_password": "secret",
    "favorite": "{\"book\":[],\"history\":[]}"
  },
  "encrypt": ["torrserver_login", "torrserver_password"]
}
```

Only `schema_version` 2 is accepted. `data` must be an object; top-level keys match
`^[A-Za-z0-9_.:-]{1,128}$`. Values may be any JSON type except objects containing a
reserved `$sealed` member, even nested. `encrypt` is optional and contains at most 64
keys matching the same pattern. There are no fixed sections or per-section size limits.
The whole encoded body is capped at 4194304 bytes (4 MiB), nesting at depth 32 including
the envelope. Invalid UTF-8, NUL escapes and unpaired surrogates are rejected.

Migration `00003_user_data_v2.sql` drops `encrypted_connections` and adds
`etag text not null default ''`. Version-1 rows read as `404 user_data_not_found`.
GET returns the decrypted `data`, `schema_version` and `updated_at`, without `encrypt`.

GET and PUT return a quoted `ETag` containing SHA-256 of canonical plaintext `data`
before sealing (sorted nested object keys, compact JSON and normalized string escapes).
`encrypt` and timestamps do not affect it. GET with matching `If-None-Match` returns
`304` with no body; GET retains `Cache-Control: no-store`. Tag comparison ignores `W/`.

PUT with `If-Match` updates only the existing row with that tag, atomically. An absent
or changed row returns `412 precondition_failed` without a write. Without `If-Match`,
PUT upserts. Success is `204` with no response body and the new ETag. The add-on polls
before saving, overlays dirty local keys on the latest server document and retries one
412 after polling again; unrelated keys survive concurrent edits. The same key uses last
successful save wins. A first save after a 404 exports all local synced data without
`If-Match` and does not perform a read-back GET.

A value PostgreSQL still refuses to store (a SQLSTATE class 22 data exception, for example a
number outside jsonb's numeric range) is also `400 invalid_document`, not `503`. Every request
runs under a 10 s deadline, so a stalled database answers `503 storage_unavailable` before the
15 s write timeout drops the connection.

The user ID comes only from the `Authenticator` (the session's Keycloak `sub`); no header,
query or body can set it.

User-data GET/PUT/DELETE and membership requests may send `X-Lampa-Expected-User` as
an identity precondition. Both add-ons always send their captured user ID. If it differs from the cookie-authenticated
identity (for example, another tab signed in as someone else), the API returns
`409 account_changed` before accessing storage. Ownership fails closed and rechecks
`/api/v1/session` without retrying the operation; sync stops saves and polling and sends
the Account `expired` event for the same session recheck on 401/409. Omitting the header preserves the
existing API behavior; the header never authenticates a caller or selects another user.
Identity, either URL, local selector and guest-storage changes also discard inactive My Torrents screens so
returning from a detail screen fetches a newly filtered list.

Membership routes accept an opaque `server_id` of 1–64 ASCII letters, digits, underscores
or hyphens. The add-on derives a 64-character lowercase SHA-256 ID from the effective raw
endpoint returned by public `Lampa.Torserver.ip()`, trimmed, lowercased, stripped of its
HTTP(S) scheme and trailing slashes. The page's scheme does not affect the ID, and existing
URL-1 memberships keep their IDs. Different normalized hosts, paths or ports produce
different IDs even when both links reach the same physical server. Switching links hides
one membership set and switching back restores it; adding under one address never claims
ownership under the other. Missing public API or both URLs empty leaves ownership
unconfigured. Changes to either URL or the local selector refresh My Torrents; pending
responses remain bound to the captured account and effective server. The backend
trims and lowercases a `hash`, then requires 40 or 64 hexadecimal characters.

### Torrent membership storage and logging

Migration `00002_create_lampa_torrserver_hashes.sql` creates:

```sql
create table lampa_torrserver_hashes (
    user_id uuid not null,
    server_id text not null check (server_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    hash text not null check (hash ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    created_at timestamptz not null default now(),
    primary key (user_id, server_id, hash)
);
```

This table is independent of `lampa_user_data`: deleting a user-data document does not
delete memberships. Claims preserve the original row on conflict; releases match the
authenticated user, server and hash. There is no row cap or automatic pruning. Removing
even the last membership never deletes the torrent from TorrServer. Operator deletion hides
retained memberships while the torrent is absent; re-adding the same hash makes them visible
again. A membership explicitly removed through Lampa stays hidden until claimed again.

Access and panic logs replace every path beginning `/api/v1/torrservers/` with
`/api/v1/torrservers/…`, including malformed and failed requests. Neither server IDs nor
torrent hashes appear in those request paths. Bodies, headers, cookies and query strings
are never included in access logs.

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
and local logout work with either dependency down; browser SSO logout needs Keycloak. User data
and torrent membership need the database. The critical database check probes both
`lampa_user_data` and `lampa_torrserver_hashes`, so a missing membership table fails health.

## Operational caveats

- **Back up `LAMPA_API_DATA_KEY` outside GitHub.** It seals values selected by the client's
  `encrypt` list; losing or changing it causes those values to be omitted on GET with a
  warning. Remaining values are still readable. Key rotation is not implemented.
- `POSTGRES_PASSWORD` applies only when the `lampa_db_data` volume is first initialized.
  To rotate `LAMPA_DB_PASSWORD`, run `ALTER ROLE lampa PASSWORD '...'` inside the database
  first, then update the secret and redeploy.
- Postgres 18+ images keep data under `/var/lib/postgresql/<major>/...`, so the volume mounts
  `/var/lib/postgresql`, not `.../data`.
- **My Torrents provides application view separation, not TorrServer access control.**
  Anyone with its URL or credentials can still list or delete all torrents directly. A
  missing or disabled ownership add-on leaves the original shared list and deletion UI
  available; verify that the file loads on supported clients after deployment.
- Guests use a versioned, device-local hash map in
  `localStorage['svtlv_torrserver_guest_hashes']`, partitioned by server ID. Browser profiles
  have independent lists; people sharing a profile share its list. Clearing browser data
  loses memberships without deleting torrents. Login and logout never transfer hashes
  between account and guest storage. Guest removal changes only that map; account removal
  changes only the authenticated user's database row. Neither sends `action: rem`.
- Lists intersect membership with the live TorrServer list before rendering. An unresolved
  session, failed account lookup or corrupt/unavailable guest storage shows an empty state
  and a notification. An initial session outage never guesses guest identity; a guest already
  confirmed in this page lifetime remains a guest during a later refresh outage. A `401`
  fails the current call and rechecks the session. Add/playback failures cannot silently
  switch account operations into guest writes. Direct playback remains available while
  identity is unresolved or the API is down; membership recording is skipped if identity
  was unresolved at invocation or changed before TorrServer answered.
- Preserve the original upstream two-link settings and per-path URL behavior after bundle
  updates. Torrent operations, streams and speed tests use `Torserver.ip()` / `url()`: the
  selected nonempty URL wins, otherwise the other nonempty URL is used; both empty means
  unconfigured. Parser/search and generic authentication use the selected field directly.
  With an empty selected field, search reports an unconfigured TorrServer and generic
  requests receive no Basic auth even if torrent operations fall back. Status checks use
  the changed field on either URL edit and the selected field on selector changes/settings
  open, with no fallback; they send the shared credentials explicitly when auth is enabled.
  Explicit URL schemes retain upstream `Utils.checkEmptyUrl` behavior. These are the original
  upstream differences, not a common routing policy. The separate marked patch still removes
  TrackTimecode (the toggle and all shared `/viewed` reads/writes); playback progress stays in
  Lampa's local `file_view*`. Reapply that removal and its version/build stamp after upstream
  updates without removing the two-link controls or changing their routing.
- Account sync polls every 60 seconds while visible and on screen return, applying clean
  remote changes live, including during playback. A normal save costs a poll GET plus a PUT;
  each authenticates against Keycloak, even a 304 (no cache). Dirty fingerprints in
  `svtlv_sync_base` survive reload and network failures; `svtlv_sync_user` identifies their
  owner. A local `svtlv_sync_revision` changes on each baseline write or clear to reject
  delayed responses even when fingerprint contents repeat. Imports persist the owner
  before writing account values, so partial failures still permit cleanup. Equal-data
  login and the successful pre-boot shortcut refresh this tab's caches before enabling
  uploads. Logout/confirmed expiry
  clears synced keys with empty strings to mask IndexedDB reserve copies and drops
  the owner and fingerprints, retaining a fresh local revision. The last validated
  scopes in `svtlv_sync_scope` allow cleanup after a scope fetch failure; this cached
  policy never authorizes synchronization. Device settings and
  cloud data remain. Boot with a marker waits at most 3 seconds before starting Lampa;
  no marker sends no user-data request.
- Optional history/progress values are dropped largest-first when the encoded request would
  exceed 4 MiB. CUB writers, Clear cache and manual backup restore are not coordinated with
  account sync; the next boot GET may restore server values. `file_view_*` belongs to CUB
  and never syncs; plain `file_view` is best-effort. Browser/TV deployment checks remain
  in the completed Plan 4 checklist.
- On Android with the external torrent client (`internal_torrclient` disabled), playback
  bypasses `Lampa.Torserver.hash` and cannot record membership. Only explicit Add to My
  Torrents records it. With the internal client, playback records membership only when
  `torrserver_savedb` was enabled; transient playback does not claim a saved entry.

## CI and deployment

`.github/workflows/tests.yaml` runs lint, `make test` and `make race` (with
`LAMPA_API_REQUIRE_DOCKER=1`), a compose `config` check against `devops/.env.example` and an
ES5 syntax parse of the `svtlv/*.js` add-ons (pinned `acorn@8.14.0 --ecma5`) on every PR and on
pushes to `svtlvtv`. The frontend job also runs account-state, account-sync and ownership runtime tests,
including documentation checks against the routes, migration SQL and scope inventory.

`.github/workflows/deploy-docker.yaml` (`workflow_dispatch` only, default branch only) has one
input, `environment` (Development / Test / Production, default Production), written into the
server `.env` as `LAMPA_ENVIRONMENT`; Development fails the `prepare` job, since it only works
for a local `go run`. The public URL and Keycloak Authority come from the selected appsettings
files. `LAMPA_KEYCLOAK_CLIENT_SECRET` remains a required secret. The release web port binds to
`100.105.140.19:8092` on the `svtlv` Tailscale host. It builds `ghcr.io/<owner>/lampa-web` and
`ghcr.io/<owner>/lampa-api` (branch, `<branch>-<sha>` and `latest` tags), swaps the `:dev`
images for `:latest`, strips the `build:` blocks with `sed`, and deploys over Tailscale + SSH,
waiting up to 3 minutes for `svtlvtv_lampa_api` to be healthy. It does not run the tests
(same as Svtlv); `tests.yaml` is the gate.

There are no rollback inputs: roll back by reverting the commit and dispatching again.
