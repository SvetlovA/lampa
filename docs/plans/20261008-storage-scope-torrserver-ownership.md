# Plan 3: Storage Classification and TorrServer Ownership

## Overview
- Plan 3 of `docs/settings-sync-backend-design.md` (§14), covering §10.3 (storage ownership audit) and §10.4 (TorrServer data ownership).
- **Storage classification**: every storage key the current bundle uses gets an explicit scope in one machine-readable file, `svtlv/storage-scope.json`. A Go drift test fails CI when an upstream pull adds an unclassified key, the same way `TestSensitiveSettings_matchUI` already guards settings inputs. Plan 4's sync adapter reads this file as its allowlist.
- **TorrServer ownership**: Lampa keeps talking to TorrServer directly, exactly as today. A new `svtlv/` add-on wraps the public `Lampa.Torserver` object so that, for a signed-in user, My Torrents shows only torrents that user added on that TorrServer. Ownership rows `(user, TorrServer, torrent hash)` live in `lampa-api` behind three small authenticated routes.
- Add and playback keep calling TorrServer directly. Signed-in removal deletes only the current user's PostgreSQL row. Anonymous My Torrents uses a device-local guest list; guest removal deletes only its local entry. Neither removal sends `action: rem` to TorrServer.
- No change to TorrServer, Jackett, `app.min.js`, `css/app.css` or `lang/*`, and no new settings, configuration or secrets.

## Context (from discovery)
- **TorrServer client** `Torserver` (`app.min.js` ~8232), exported as `Lampa.Torserver` (~56052). The inner functions (~7856–8089) are reached only through that object, so replacing its properties intercepts every caller:
  - `Torserver.my(success, fail)` → My Torrents (`mytorrents` component, ~43610): `POST <url>/torrents {action:list}`. It calls `success(result)` only when `result.length`, else `fail()` with no arguments (the caller passes `this.empty`). Rendering happens only inside `success`.
  - `Torserver.add(object, success)` → "Add to My Torrents" (`addToBase`, ~43313): `action:add, save_to_db:true`. No `fail` is passed and the response is ignored.
  - `Torserver.hash(object, success, fail)` → playback (~40415): the same `action:add` request with `save_to_db: torrserver_savedb`; it reads `json.hash`.
  - `Torserver.remove(hash)` → My Torrents long-press "remove" (~43665), with no callbacks: `action:rem`.
  - `Torserver.viewed(hash, success, fail)` (single caller ~40449) and `viewedSet(...)` (~40679, ~40832, no callbacks) → shared `/viewed` timecodes when `torrserver_tracktimecode` is on.
  - `Torserver.drop` drops a torrent from TorrServer memory on playback timeout; it does not touch the saved list.
  - Every `Torserver` request first calls `clear()`, which empties one shared request queue (`network$b`, `_calls = []` ~33391). Delaying an original call in time can therefore drop another in-flight TorrServer callback.
- **Android external client bypass**: with `Platform.is('android') && !internal_torrclient` (the Android default, ~47308), playback goes through `Android.openTorrent` / `Android.playHash` (~40370, ~40384), never through `Torserver.hash`. On that client only explicit "Add to My Torrents" records ownership.
- **Startup timing**: `Activity.init` restores the start activity after `setTimeout(last, 500)` (~45642). With `start_page` = My Torrents (or "last" on a saved mytorrents activity, or a deep link), `my()` can run before a late add-on is installed.
- `ip()` picks `torrserver_url` / `torrserver_url_two` by `torrserver_use_link`. `Lampa.Utils.checkEmptyUrl` (~4079) prefixes the **page's** scheme, so it is not usable for a device-independent id. `Lampa.Utils.hash` (~4340) is a 32-bit string hash returned as a decimal string.
- **Backend** `backend/` (Go, Ralphex style): `pkg/storage` (`PgStore`, `Service`, `Sealer`, `SensitiveSettings`, `Migrate`), `pkg/api` (`Server.routes`, `authenticate`, CSRF, `writeServiceError`, JSON error contract, moq mocks, `newTestServer*` helpers in `identity_test.go`), goose migrations in `backend/migrations/` (`migrations_test.go` checks file names only; DB migration tests live in `pkg/storage/migrate_test.go` via `pgtest`), wiring through `newAPI(cfg, svc, logger)` in `cmd/lampa-api/main.go`, `PgStore.Ping` used by the `database` health check, access log writing `r.URL.Path`.
- **Drift-test precedent**: `TestSensitiveSettings_matchUI` (`backend/pkg/storage/crypto_test.go:301`) reads `../../../app.min.js` and skips when it is absent.
- **Add-on precedent**: `svtlv/account.js` (ES5 IIFE, `window.svtlv_account_loaded` guard; `applySession` ~198 runs at boot, on the 12 h heartbeat and in `logOutDone` before the SSO redirect). The loader seam is the `<!-- svtlv:begin -->` block in `index.html`; the CI gate is `acorn --ecma5 svtlv/*.js` in `.github/workflows/tests.yaml` (the glob already covers new files).
- **Storage access in the bundle** goes through more than `Storage.get/set`:
  - `Storage.field/cache/add/remove/value/sync('k')`, sometimes with double quotes (`Storage.field("start_page")`).
  - Direct `localStorage.getItem/setItem` (`cub_mirrors`, `fix_widget`, `testsize`).
  - Keys registered only through the Params module's `select('k'` / `trigger('k'` (for example `account_password`, `cloud_token`, `cache_images`, `proxy_other`).
  - Settings templates `data-name=\"k\"`.
  - Keys built from variables (`file_view_<profile>`, `account_timeline_sync_<profile>`, `content_rows_<name>`).

## Development Approach
- **testing approach**: Regular (code first, then tests in the same task)
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
- run tests after each change (`make test`, `make race`, `make lint` inside WSL Ubuntu from `backend/`)
- keep every existing API route compatible; anonymous torrent behavior follows the agreed device-local guest policy below

## Testing Strategy
- **unit tests**: required for every task. Table-driven Go tests; DB tests through `pgtest` testcontainers (`postgres:18.6`); handler tests with moq mocks; one `_test.go` per source file.
- **frontend**: there is no JS test runner. `svtlv/*.js` must pass the pinned `acorn --ecma5` gate; behavior is verified with the manual matrix in Task 7 and Post-Completion (browser and TV client).
- **e2e tests**: the project has none.

## Progress Tracking
- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- update plan if implementation deviates from original scope
- keep plan in sync with actual work done

## Solution Overview

**Decisions made with the user (2026-10-08; removal and guest policy revised 2026-10-11):**

1. **The browser keeps calling TorrServer directly.** `lampa-api` never contacts TorrServer, so there is no proxy and no SSRF surface, and the backend never stores or sees TorrServer URLs.
2. **Ownership key is `(user_id, server_id, hash)`.** `server_id` identifies the TorrServer without a new setting: a hash of the normalized `torrserver_url`, or of `torrserver_url_two` when slot one is empty. If the TorrServer changes, its rows stop showing; switch back and they show again. Rows are never pruned automatically. This assumes both slots are addresses of the same TorrServer, which is how Lampa presents them ("link one / link two").
3. **Add and playback keep their existing TorrServer calls.** The add-on (a) records a row after a successful add or playback-add, using the canonical hash TorrServer returned, (b) handles signed-in removal through the ownership API only, and (c) filters My Torrents before it renders.
4. **Remove deletes only the current user's database row.** `DELETE` matches the authenticated `user_id`, `server_id` and torrent hash. It never calls `Torserver.remove` / `action: rem`, even when this is the last ownership row. If A and B own X, A removing X leaves X on TorrServer and visible to B. Physical deletion is an operator action outside this plan; there is no reference-count cleanup because guests and other clients can also use the torrent. This restores the original §10.4 / §15 membership-isolation requirement.
5. **Torrents already on TorrServer have no owner** and stay hidden from signed-in users and guests. There is no claim UI or automatic assignment; a guest must re-add a torrent to put its hash in that device's list.
6. **The TorrServer URL and credentials are synced by Plan 4, not here.** Plan 3 classifies `torrserver_url`, `torrserver_url_two`, `torrserver_auth`, `torrserver_login` and `torrserver_password` as user-scoped connection keys, and `torrserver_use_link` as device-only. Plan 4's rule: on first login, if the device has values and the server has none, upload them; once the server has values, the server always wins until the user changes them. Until Plan 4, each device keys rows by its own URL, so devices with different slot-one URLs see different sets; after Plan 4 they converge.
7. **Shared `/viewed` timecodes stay out of both account and guest flows** (§10.4, §15). `Torserver.viewed` reports failure (Lampa then renders without TorrServer marks) and `viewedSet` is a no-op. Lampa's own local `file_view*` progress keeps working.
8. **Lists fail closed.** If signed-in ownership lookup fails, guest storage is unavailable or corrupt, or account status is unresolved at startup, My Torrents shows an empty state plus a notification; it never shows the unfiltered TorrServer list. A previously confirmed guest remains in guest mode through a later session-refresh outage.

**Simplifications chosen after review:** no per-user row cap (a Keycloak-gated family server does not need one, and dropping it removes the transaction and advisory lock); no `addon_keys` exemption (add-on keys are not classified, and anything unclassified is device-only by definition).

### Anonymous users: agreed policy (2026-10-11)

Guests have a device-local My Torrents list. The three ownership routes remain authenticated; there are no anonymous database users or shared guest account. Guest removal cannot delete a signed-in user's torrent.

| Operation | Signed in | Anonymous guest |
|---|---|---|
| List | TorrServer list intersected with the user's database hashes | TorrServer list intersected with this browser's guest hashes for this server |
| Explicit add / playback-add | Original TorrServer call; record returned canonical hash in PostgreSQL | Original TorrServer call; record returned canonical hash locally |
| Remove | Delete only the user's database row | Delete only the local guest hash |
| Physical deletion | Operator action outside Lampa's ownership flow | Same; never call `action: rem` |

Guest implementation details:
- Store a versioned map of `server_id` to canonical hashes in plain `localStorage['svtlv_torrserver_guest_hashes']`, outside `Lampa.Storage` and Plan 4's sync allowlist. It identifies a browser profile/origin, not a person: people sharing that profile share its guest list. No cross-device sync; clearing browser data loses the list, not the TorrServer torrents.
- Record a hash only after a successful original add/hash response, with the same Android external-client limitation as signed-in ownership. Membership alone does not make a torrent persistent when `torrserver_savedb` is off. Existing torrents are not automatically imported; re-adding one makes it visible locally.
- Removal updates only local storage and succeeds only if the write succeeds. If storage is unavailable/corrupt, show a notification and fail closed for the list; never fall back to the unfiltered list or physical deletion. Playback can continue when membership recording fails, with a notification.
- Keep guest data separate on login and logout; no automatic transfer into an account and no copying account hashes into guest storage. After logout, show the pre-existing guest list. Capture the guest/account mode and server at invocation, and ignore stale list responses after a mode change. Never redirect an in-flight account operation into guest storage after a `401`.
- An expired session fails closed for the current call. Mark account status as checking before re-fetching `/session`; subsequent calls wait until it resolves. A confirmed anonymous response selects guest mode. On an unavailable response, retain only a guest mode already confirmed during this page lifetime; otherwise fail closed. While initial session status is unknown, do not expose any list or route mutations using a guessed identity. Refresh the active My Torrents list when the session resolves.
- Suppress shared `/viewed` reads/writes for guests too; local Lampa progress continues to work.
- No automatic TorrServer cleanup when a guest removes a hash or clears storage. Guest references are not known to the backend, and other clients may also use those torrents.

Manual checks cover two browser profiles with independent guest lists, guest/account transitions, guest and account sharing X, guest removal preserving X for the account, clearing local storage, and API outages in guest mode.

**How it fits:**
```
Lampa UI ──Torserver.my/add/hash/remove──▶ svtlv/torrserver-ownership.js (wraps Lampa.Torserver)
                                              │ list/add/play calls ──▶ TorrServer (direct, as today)
                                              ├ signed-in membership ──▶ lampa-api ──▶ PostgreSQL
                                              └ guest membership ──▶ device localStorage
```

## Technical Details

**Migration `00002_create_lampa_torrserver_hashes.sql`**
```sql
create table lampa_torrserver_hashes (
    user_id uuid not null,
    server_id text not null check (server_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    hash text not null check (hash ~ '^([0-9a-f]{40}|[0-9a-f]{64})$'),
    created_at timestamptz not null default now(),
    primary key (user_id, server_id, hash)
);
```
There is no foreign key to `lampa_user_data`: a user can own torrents without having a document. `DELETE /api/v1/user-data` does not touch ownership rows. Rollback to the previous API is safe: goose does not fail on a database version newer than the binary.

**Validation (in `pkg/storage`)**
- `server_id`: `^[A-Za-z0-9_-]{1,64}$`, opaque to the backend (today a decimal hash string). Otherwise `400 invalid_server_id`.
- `hash`: trimmed, lowercased, must match 40 or 64 hex (BitTorrent v1/v2). Otherwise `400 invalid_hash`.

**API (all behind `authenticate`; PUT/DELETE need `X-Lampa-Csrf: 1`, as the existing CSRF middleware already enforces)**

| Route | Result |
|---|---|
| `GET /api/v1/torrservers/{server_id}/hashes` | `200 {"hashes": ["<hex>", …]}`, sorted, `[]` when none |
| `PUT /api/v1/torrservers/{server_id}/hashes/{hash}` | `204`, idempotent (`insert … on conflict do nothing`) |
| `DELETE /api/v1/torrservers/{server_id}/hashes/{hash}` | `204`, idempotent; deletes only this session user's row; a missing row is not an error; no TorrServer request |
| other methods | `405 method_not_allowed` with a per-route `Allow` (`GET` for the list, `PUT, DELETE` for an item), JSON body, HEAD routed to 405 as for user data |

- Errors reuse the existing contract: `401`/`503 session_unavailable` from the authenticator, `400` validation codes and `503 storage_unavailable`, all through the existing `writeServiceError`. The user always comes from the session (`UserID(ctx)`), never from the request.
- **Access log privacy**: hashes reveal what a user watches, so the access log writes paths under `/api/v1/torrservers/` as `/api/v1/torrservers/…` (no server id, no hash). Other paths are logged as today.

**Backend types**
- `storage.TorrentStore` interface (its own `go:generate` line → `pkg/storage/mocks/torrent_store.go`): `ListHashes(ctx, userID, serverID) ([]string, error)`, `ClaimHash(ctx, userID, serverID, hash) error`, `ReleaseHash(ctx, userID, serverID, hash) error`, implemented by `PgStore` with single statements.
- `storage.Torrents` service (`NewTorrents(store, logger)`, `List`, `Claim`, `Release`): it canonicalizes the user id (`canonicalUserID`), validates and normalizes, and maps store failures to `ErrUnavailable` (logged like `Service.storeError`).
- `api.Torrents` interface plus moq mock `pkg/api/mocks/torrents.go`. `NewServer(cfg, svc, torrents, auth, authRoutes, logger)` takes it as a new required dependency. `methodNotAllowed` becomes a function of the `Allow` value.
- `PgStore.Ping` also probes `lampa_torrserver_hashes`, so the `database` health check catches a missing migration.

**Add-on `svtlv/torrserver-ownership.js`** (ES5 IIFE: protocol check, load guard `window.svtlv_torrserver_loaded`, guarded hooks; errors in list or remove fail closed and never fall back to an unfiltered list or the original destructive remove)
- **Install early**: poll (200 ms, as `account.js`) only until `window.Lampa && Lampa.Torserver && Lampa.Storage` exist, then replace the properties. Replacing them needs nothing from app ready. If `Lampa.Activity.active()` is a `mytorrents` activity that may already have rendered, `Lampa.Activity.replace()` it so it reloads through the wrapper. Repeat that refresh when account status resolves or changes.
- **`serverId()`**: take `Lampa.Storage.get('torrserver_url')`, or `torrserver_url_two` if that is empty. Trim, lowercase, strip a leading `http://` or `https://`, strip trailing `/`, then `Lampa.Utils.hash(...)`. The page scheme is never involved. With no URL, list/add/playback use Lampa's existing "not configured" path; remove fails with a notification in either mode and never calls the original remove.
- **Account state** comes from `account.js`. It publishes a current-page state (`checking`, `signed_in`, `guest`, or `unavailable`) and a generation that changes on transitions; the ownership add-on uses that state for every call and ignores responses from an older generation. `checking` or `unavailable` blocks list exposure and add/remove mutations. A successful anonymous `/session` response or successful logout selects `guest`; a successful authenticated response selects `signed_in`. A refresh failure retains a guest state already confirmed during this page lifetime, but never turns an unknown or previously signed-in state into guest. An ownership `401` marks state `checking` immediately, sends `Lampa.Listener.send('svtlv_account', {type: 'expired'})`, and `account.js` re-fetches `/session`; the current call fails closed. Keep the existing `svtlv_account_signed_in` flag synchronized if needed by other add-ons, but do not infer guest status from its absence.
- **`my(success, fail)`**: in signed-in mode, start the hashes GET **and** the original `my()` at the same moment, so upstream's `clear()` timing is unchanged. In guest mode, read and validate the local hash set before starting the original `my()`. Wait for the membership source and TorrServer list to settle:
  - signed-in lookup failed (network, 5xx, 401) or guest storage failed → Noty, then `fail()`;
  - original failed or empty → `fail()`;
  - otherwise keep only items whose lowercased `hash` is in the set → `success(filtered)`, or `fail()` when nothing is left.

  Nothing renders before both settle.
- **`add(object, success, fail)` / `hash(object, success, fail)`**: in either known mode, call the original with the same arguments. On success, use `json.hash` to `PUT` the signed-in row or update guest storage, then call the caller's `success(json)` if given. Membership failures show a Noty but do not block playback. A failed TorrServer add creates no membership, and `fail` passes through untouched. If account state changes before the response, do not write the hash into the new mode's store.
- **`remove(hash, success, fail)`**: in signed-in mode, issue only the ownership `DELETE`, capturing server and account context at invocation. In guest mode, remove only the hash in that server's local list. Never call the original remove, including on failure, `401`, missing configuration or an account-state change. On success, call `success` if given. On failure, show a Noty and call `fail` if given; reconcile the list because a signed-in timeout may have committed. The upstream long-press handler immediately calls `item.disable()` without callbacks: after a failed request, reload the still-active originating My Torrents activity through the public API; do not navigate back if the user has left it. If membership lookup also fails, retain the fail-closed empty/error state. A later successful add/playback-add can claim the hash again.
- **`viewed(hash, success, fail)` in either known mode**: call `fail()` asynchronously. **`viewedSet(...)`**: no-op. For unresolved state, also avoid shared `/viewed` operations.
- **Requests**: same-origin `$.ajax` like `account.js` (the session cookie goes along automatically); timeout 15 s; `X-Lampa-Csrf: 1` on PUT/DELETE.
- **`index.html`**: one more `putScript('svtlv/torrserver-ownership.js?v=' + cache_version, …)` line inside the existing `svtlv:begin/end` block. No stylesheet.

**Storage classification `svtlv/storage-scope.json`**
```json
{
  "version": 1,
  "scopes": {
    "user":            ["favorite", "mine_reactions", "file_view_*", "..."],
    "user_connection": ["torrserver_url", "torrserver_url_two", "torrserver_auth", "torrserver_login", "torrserver_password"],
    "device":          ["player", "torrserver_use_link", "interface_size", "..."],
    "connection":      ["jackett_url", "jackett_key", "prowlarr_key", "tmdb_proxy*", "..."],
    "external":        ["account_use", "account_password", "cloud_token", "account*", "..."],
    "cache":           ["activity", "recomends_*", "..."]
  },
  "notes": { "<key>": "why, when not obvious" }
}
```
- A trailing `*` marks a prefix entry. `user` is Plan 4's export allowlist and `user_connection` is synced by Plan 4 under decision 6. Everything else, and any key not in the file, never leaves the device (§10.3: the safe default is local).
- **Matching precedence**: an exact entry beats any prefix, and the longest matching prefix wins. Two entries match equally only on a real duplicate, which the test rejects.

**Drift test `TestStorageScope_matchBundle`** (in `backend/pkg/storage/crypto_test.go`, beside the other bundle guard, sharing a `readBundle(t)` helper)
- **Extraction**, accepting `'` or `"` in each pattern:
  - `Storage.(get|set|field|cache|add|remove|value|sync)(\s*['"]k`
  - `(local|session)Storage.(getItem|setItem|removeItem)(\s*['"]k`
  - Params registrations `^\s*(select|trigger)\(\s*['"]k` (line-anchored, as the Params module is written)
  - settings templates `data-name=\\"k\\"`
  - literal fragments of concatenated keys, `['"]k_['"]\s*\+`
- **Rules**:
  - every extracted key resolves (by precedence) to exactly one entry;
  - every exact entry is extracted from the bundle;
  - every prefix entry's literal prefix appears in the bundle, so stale prefixes fail too;
  - every `SensitiveSettings` key is in `user_connection` or `connection`;
  - the test fails if `svtlv/storage-scope.json` is missing or invalid, and skips only when `app.min.js` is absent.
- Keys built only from variables (`file_view_<profile>`, `account_timeline_sync_<profile>`, `content_rows_<name>`) are inventoried by hand from their read/write sites, and the test guards them only through their literal fragment. A comment in the test says so.

## What Goes Where
- **Implementation Steps**: Go migration, store, service, API, wiring and tests; `account.js` session-state publishing and expiry event; the ES5 add-on with guest storage and its `index.html` line; the classification file with its drift test; documentation and design-doc deviations.
- **Post-Completion**: deploy through the manual workflow, then two-account and two-device smoke checks, TV-client checks and the rollback drill.

## Implementation Steps

### Task 1: Add the TorrServer ownership migration

**Files:**
- Create: `backend/migrations/00002_create_lampa_torrserver_hashes.sql`
- Modify: `backend/migrations/migrations_test.go`
- Modify: `backend/pkg/storage/migrate_test.go`

- [ ] add the goose Up/Down migration with the table, primary key and check constraints from Technical Details
- [ ] update `TestFS`'s expected file list in `migrations_test.go`
- [ ] in `migrate_test.go` (pgtest): `Migrate` creates `lampa_torrserver_hashes`, a second run is a no-op, and the check constraints reject a bad `hash` and a bad `server_id`
- [ ] run `make test` - must pass before task 2

### Task 2: Add the ownership store to PgStore

**Files:**
- Create: `backend/pkg/storage/torrents.go`
- Create: `backend/pkg/storage/torrents_test.go`
- Create: `backend/pkg/storage/mocks/torrent_store.go`
- Modify: `backend/pkg/storage/postgres.go`
- Modify: `backend/pkg/storage/postgres_test.go`

- [ ] add the `TorrentStore` interface with its own `go:generate` line, and implement `ListHashes` (sorted), `ClaimHash` (`insert … on conflict do nothing`) and `ReleaseHash` on `PgStore`
- [ ] extend `PgStore.Ping` to probe `lampa_torrserver_hashes`
- [ ] write DB tests: list empty and sorted, claim idempotent, the same hash under two users and under two servers, release keeps the other user's row, release of a missing row is ok
- [ ] write tests for store failures (closed pool → wrapped error) and the extended `Ping`
- [ ] regenerate mocks, then run `make test` and `make race` - must pass before task 3

### Task 3: Add the Torrents service with validation

**Files:**
- Modify: `backend/pkg/storage/torrents.go`
- Modify: `backend/pkg/storage/torrents_test.go`

- [ ] add the `Torrents` service (`NewTorrents`, `List`, `Claim`, `Release`) and `ValidationError` codes `invalid_server_id` / `invalid_hash`; map store failures to `ErrUnavailable`
- [ ] canonicalize the user id, validate `server_id`, trim, lowercase and validate `hash`
- [ ] write table-driven tests for valid inputs (uppercase hash lowered, v1 and v2 lengths, nil-dependency constructor errors)
- [ ] write tests for an invalid user id, server id and hash, and for store unavailability (with the mock)
- [ ] run `make test` - must pass before task 4

### Task 4: Add the ownership API routes

**Files:**
- Create: `backend/pkg/api/torrents.go`
- Create: `backend/pkg/api/torrents_test.go`
- Create: `backend/pkg/api/mocks/torrents.go`
- Modify: `backend/pkg/api/server.go`
- Modify: `backend/pkg/api/server_test.go`
- Modify: `backend/pkg/api/identity_test.go`
- Modify: `backend/pkg/api/flow_test.go`

- [ ] add the `Torrents` interface with a `go:generate` moq line, the new required `NewServer` dependency (nil → error), and the GET/PUT/DELETE routes with `{server_id}` / `{hash}` path values and JSON 405/HEAD fallbacks with a per-route `Allow`
- [ ] answer through the existing `writeServiceError` (validation → 400 with its code, `ErrUnavailable` → 503, unexpected → logged 500), and redact `/api/v1/torrservers/` paths in `accessLog`
- [ ] write handler tests: the 200 list shape (`{"hashes":[]}` when empty), 204 PUT/DELETE, the user id taken only from the session, error mappings
- [ ] write middleware tests: 401 anonymous, CSRF required on PUT/DELETE, 405 + the right `Allow`, HEAD → 405, an access log line without the hash; update `newTestServer*` helpers, the `NewServer` nil-dependency tests and the flow test
- [ ] run `make test`, `make race`, `make lint` - must pass before task 5

### Task 5: Wire the Torrents service in main

**Files:**
- Modify: `backend/cmd/lampa-api/main.go`
- Modify: `backend/cmd/lampa-api/main_test.go`

- [ ] construct `storage.NewTorrents(store, logger)` and pass it through `newAPI` to `api.NewServer`
- [ ] extend the wiring test to hit the new route through the composed handler (401 anonymous)
- [ ] run `make test` - must pass before task 6

### Task 6: Publish resolved account and guest state from account.js

**Files:**
- Modify: `svtlv/account.js`

- [ ] publish a current-page account state and generation before the initial `/session` request (`checking`), after each authenticated/anonymous result, after successful logout, and when an ownership `401` starts session re-checking; notify the ownership add-on on transitions. A refresh failure may retain only a guest state already confirmed in this page lifetime; an initial failure and a formerly signed-in state fail closed
- [ ] keep `localStorage['svtlv_account_signed_in']` (`'1'` or removed) synchronized in `applySession` (try/catch, plain `localStorage`), but do not use a missing flag as proof of guest status. Do not persist the readiness state across reloads
- [ ] follow `Lampa.Listener` `svtlv_account` `{type:'expired'}` and re-fetch the session through the existing `fetchSession`/`applySession` path; prevent stale responses from restoring a previous identity after the transition
- [ ] update the header comment to document state publication and expiry handling
- [ ] check that `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js` passes
- [ ] manually check startup, sign-in, sign-out, session expiry, and session/API outages: account state and avatar change together; an initial outage never selects guest merely because the flag is absent

### Task 7: Add the TorrServer ownership add-on

**Files:**
- Create: `svtlv/torrserver-ownership.js`
- Modify: `index.html` (inside the `svtlv:begin/end` block only)

- [ ] add the IIFE skeleton (protocol check, load guard, early install as soon as `Lampa.Torserver` exists, guarded hooks that fail closed for list/remove, re-open an already active `mytorrents` activity) and `serverId()`
- [ ] implement the versioned `localStorage['svtlv_torrserver_guest_hashes']` map, canonical hash validation, per-server lookup/write, corruption/unavailable handling, and guest list filtering; keep it outside `Lampa.Storage` and Plan 4 sync
- [ ] wrap `my` (account: parallel hashes GET + original; guest: local hashes + original; filter before `success`; fail closed with a Noty), `add` and `hash` (record canonical `json.hash` in the store for the captured mode), `remove` (database DELETE or guest local deletion only; never original remove; reconcile the optimistic card state on failure), `viewed` and `viewedSet` (kept out in both modes); callbacks are invoked only if given, with the same arguments
- [ ] handle `401` from any ownership call (switch state to checking, send `svtlv_account` `expired`, fail closed for the current list); ignore late responses from an earlier account generation and refresh an active My Torrents list after state transitions
- [ ] add the `putScript` line to the seam block
- [ ] check that `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js` passes
- [ ] manual matrix in a browser with devtools:
  - two browser profiles: each guest sees only its local hashes; guest removal changes only its local list and sends no `action: rem`;
  - signed in: only own rows;
  - `lampa-api` stopped: empty plus a Noty;
  - session expired: current list fails closed; after a confirmed anonymous `/session` response, next open shows only the device guest list;
  - start page = My Torrents: filtered on the first render;
  - add/play retain their TorrServer calls; account removal sends only the ownership DELETE and guest removal writes only local storage, with no `action: rem`;
  - A and B own X: A removes X, B still sees and plays X; removing the last ownership row also keeps X on TorrServer;
  - failed or timed-out removal, and unavailable/corrupt guest storage: notification and list reconciliation, no fallback to TorrServer deletion or unfiltered display;
  - guest/account switches keep lists separate; clearing guest storage removes only the guest list; initial API outage fails closed, while an already confirmed guest can keep using its local list through a later outage;
  - remove from the long-press menu (no callbacks) works.

### Task 8: Classify every storage key with a drift test

**Files:**
- Modify: `backend/pkg/storage/crypto_test.go`
- Create: `svtlv/storage-scope.json`

- [ ] factor the bundle read into `readBundle(t)`, shared with `TestSensitiveSettings_matchUI`
- [ ] write the extraction and precedence helpers with table-driven tests (each pattern: single and double quotes, `localStorage`, Params `select`/`trigger`, `data-name`, a concatenated fragment; exact beats prefix, longest prefix wins, a duplicate is rejected)
- [ ] add `TestStorageScope_matchBundle` with the rules from Technical Details, and run it against an empty scope file: its failure output is the inventory
- [ ] classify every key by meaning at its read/write sites (§10.3) until the test passes; record non-obvious choices in `notes` (`language`/`tmdb_lang`, `plugins*`, `menu_sort`/`menu_hide`/`start_page`, `torrserver_savedb`, `file_view_*` CUB profile suffix, keys built from variables)
- [ ] audit `Storage.sync` and the CUB account write paths, and note in `notes` which `user` keys CUB also syncs (for example `online_last_balanser`) so Plan 4 can avoid overwriting them
- [ ] run `make test` and `make lint` - must pass before task 9

### Task 9: Verify acceptance criteria
- [ ] verify that the Overview and the eight Solution Overview decisions are implemented
- [ ] verify edge cases:
  - the same hash for two users, and the same user on two servers;
  - a TorrServer URL change hides rows, and changing back restores them;
  - removal by one account preserves the other account's row and the TorrServer torrent; removing the last row also preserves the torrent (decision 4);
  - ownership API down while signed in (fail closed), and an expired session;
  - guest add/list/remove uses only local membership and never leaks signed-in hashes; two browser profiles have independent lists;
  - guest/account transitions, guest plus account ownership of X, cleared/corrupt local storage, and API outages follow the agreed state rules;
  - add-on file missing (Lampa still boots; upstream My Torrents and remove remain unprotected until the add-on is restored);
  - the scheme-free `serverId` matches across an http and an https origin.
- [ ] run the full suite in WSL from `backend/`: `make test`, `make race`, `make lint`
- [ ] run the ES5 gate: `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js`
- [ ] verify coverage stays at the Plan 1/2 level for `pkg/storage` and `pkg/api`

### Task 10: [Final] Update documentation
- [ ] `backend/README.md`: ownership routes in "API", the new table, the access-log redaction, and in "Operational caveats" the view-separation caveat, device-local guest membership, the two-slot assumption (decision 2) and the Android external-client limitation
- [ ] `docs/settings-sync-backend-design.md`:
  - §4 (new table) and §8 (routes);
  - the §10.3 table aligned with `storage-scope.json` (torrserver keys → user connection, `torrserver_use_link` device-only, the Plan 4 rule from decision 6);
  - §10.4 and §15 for decisions 2, 4, 5, guest device-local membership, session readiness, and the Android limitation;
  - "Deviations recorded while implementing Plan 3" under §14; membership-only removal agrees with the original design and must not be listed as a deviation.
- [ ] `CLAUDE.md`: mention `svtlv/torrserver-ownership.js` and `storage-scope.json` in the Svtlv add-ons section, and `TestStorageScope_matchBundle` next to `TestSensitiveSettings_matchUI` (an upstream pull that adds a storage key fails CI until it is classified)
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Deployment**
- Merge to `svtlvtv`, dispatch `deploy-docker.yaml` (Production). The migration runs on API start; no new secrets, inputs or Compose changes. Confirm `svtlvtv_lampa_api` turns healthy and `/health` shows `database` passing (it now also probes the new table).
- Rollback: revert and redeploy. The extra table is harmless to the previous API version; drop it manually only if the feature is abandoned.

**Manual verification (two accounts, two devices: browser and the `lampa-app/LAMPA` TV client, both WebView engines)**
- Account A adds torrent X, account B adds X and Y: A sees X only and B sees X and Y. A guest who has not added them sees neither; a guest who adds X sees X only on that device.
- A removes X: only A's database row disappears; B still sees and plays X, and X remains on TorrServer. B then removes X: the torrent still remains on TorrServer (decision 4).
- Fail or time out A's removal request: a notification appears and the list reconciles against the API; no `action: rem` request is sent.
- Change `torrserver_url` on one device: My Torrents is empty for signed-in users there; restore it and the rows return.
- Stop `lampa-api` while signed in: My Torrents shows empty plus a notification, never the full list; playback still works.
- `torrserver_tracktimecode` on: signed-in and guest playback show no shared TorrServer marks and write none; local progress still works.
- On the Android TV client with the external torrent client: playback does not record ownership; only "Add to My Torrents" does.
- Start page = My Torrents on the TV: the first render is already filtered.
- Day one: existing torrents are hidden from signed-in users and guests until they re-add them (decision 5).
- Use two guest browser profiles: one guest's add/remove changes only that profile's list. Guest removal leaves a signed-in owner's X playable and leaves X on TorrServer.
- Sign in, sign out, expire a session, clear guest storage, and interrupt `/session`: no account hashes move into guest storage, stale responses do not expose another mode, a confirmed guest keeps its local list during a later API outage, and an initial outage shows the fail-closed state.

**Security note**
- This is application view separation, not access control: anyone with the TorrServer URL or credentials can still list and remove everything (§10.4). A missing or disabled add-on leaves the original shared UI behavior available, so deployment must verify the file loads on supported clients.
