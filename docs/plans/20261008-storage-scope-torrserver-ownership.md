# Plan 3: Storage Classification and TorrServer Ownership

## Overview
- Plan 3 of `docs/settings-sync-backend-design.md` (§14), covering §10.3 (storage ownership audit) and §10.4 (TorrServer data ownership).
- **Storage classification**: every storage key the current bundle uses gets an explicit scope in one machine-readable file, `svtlv/storage-scope.json`. A Go drift test fails CI when an upstream pull adds an unclassified key, the same way `TestSensitiveSettings_matchUI` already guards settings inputs. Plan 4's sync adapter reads this file as its allowlist.
- **TorrServer ownership**: Lampa keeps talking to TorrServer directly, exactly as today. A new `svtlv/` add-on wraps the public `Lampa.Torserver` object so that, for a signed-in user, My Torrents shows only torrents that user added on that TorrServer. Ownership rows `(user, TorrServer, torrent hash)` live in `lampa-api` behind three small authenticated routes.
- The goal is to separate what each account sees, not to change Lampa's logic: add, play and remove behave exactly as now. Anonymous users see no change.
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
- maintain backward compatibility: anonymous Lampa and every existing API route unchanged

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

**Decisions made with the user (2026-10-08), not to be reopened during implementation:**

1. **The browser keeps calling TorrServer directly.** `lampa-api` never contacts TorrServer, so there is no proxy and no SSRF surface, and the backend never stores or sees TorrServer URLs.
2. **Ownership key is `(user_id, server_id, hash)`.** `server_id` identifies the TorrServer without a new setting: a hash of the normalized `torrserver_url`, or of `torrserver_url_two` when slot one is empty. If the TorrServer changes, its rows stop showing; switch back and they show again. Rows are never pruned automatically. This assumes both slots are addresses of the same TorrServer, which is how Lampa presents them ("link one / link two").
3. **Lampa's behavior is unchanged.** Add, playback and remove call TorrServer exactly as today. The add-on only (a) records a row after a successful add or playback-add, using the canonical hash TorrServer returned, (b) deletes the row after a successful remove, and (c) filters My Torrents before it renders.
4. **Remove still deletes from TorrServer (`action: rem`).** If two accounts own the same torrent, one removal deletes it for both; the other account's row remains but has nothing to show. This deliberately changes the §15 criterion "one user's removal does not remove the other's membership" and is recorded as a Plan 3 deviation.
5. **Torrents already on TorrServer have no owner** and stay hidden from signed-in users. There is no claim UI and no automatic assignment; anonymous users still see everything.
6. **The TorrServer URL and credentials are synced by Plan 4, not here.** Plan 3 classifies `torrserver_url`, `torrserver_url_two`, `torrserver_auth`, `torrserver_login` and `torrserver_password` as user-scoped connection keys, and `torrserver_use_link` as device-only. Plan 4's rule: on first login, if the device has values and the server has none, upload them; once the server has values, the server always wins until the user changes them. Until Plan 4, each device keys rows by its own URL, so devices with different slot-one URLs see different sets; after Plan 4 they converge.
7. **Shared `/viewed` timecodes stay out of the signed-in flow** (§10.4, §15). While signed in, `Torserver.viewed` reports failure (Lampa then renders without TorrServer marks) and `viewedSet` is a no-op. Lampa's own local `file_view*` progress keeps working.
8. **Signed-in, fail closed.** If the ownership lookup fails while the user is (or was last known to be) signed in, My Torrents shows the empty state plus a notification, never the unfiltered list.

**Simplifications chosen after review:** no per-user row cap (a Keycloak-gated family server does not need one, and dropping it removes the transaction and advisory lock); no `addon_keys` exemption (add-on keys are not classified, and anything unclassified is device-only by definition).

**How it fits:**
```
Lampa UI ──Torserver.my/add/hash/remove──▶ svtlv/torrserver-ownership.js (wraps Lampa.Torserver)
                                              │ original call, unchanged ──▶ TorrServer (direct, as today)
                                              └ /api/v1/torrservers/{server_id}/hashes ──▶ lampa-api ──▶ PostgreSQL
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
| `DELETE /api/v1/torrservers/{server_id}/hashes/{hash}` | `204`, idempotent; deleting a missing row is not an error |
| other methods | `405 method_not_allowed` with a per-route `Allow` (`GET` for the list, `PUT, DELETE` for an item), JSON body, HEAD routed to 405 as for user data |

- Errors reuse the existing contract: `401`/`503 session_unavailable` from the authenticator, `400` validation codes and `503 storage_unavailable`, all through the existing `writeServiceError`. The user always comes from the session (`UserID(ctx)`), never from the request.
- **Access log privacy**: hashes reveal what a user watches, so the access log writes paths under `/api/v1/torrservers/` as `/api/v1/torrservers/…` (no server id, no hash). Other paths are logged as today.

**Backend types**
- `storage.TorrentStore` interface (its own `go:generate` line → `pkg/storage/mocks/torrent_store.go`): `ListHashes(ctx, userID, serverID) ([]string, error)`, `ClaimHash(ctx, userID, serverID, hash) error`, `ReleaseHash(ctx, userID, serverID, hash) error`, implemented by `PgStore` with single statements.
- `storage.Torrents` service (`NewTorrents(store, logger)`, `List`, `Claim`, `Release`): it canonicalizes the user id (`canonicalUserID`), validates and normalizes, and maps store failures to `ErrUnavailable` (logged like `Service.storeError`).
- `api.Torrents` interface plus moq mock `pkg/api/mocks/torrents.go`. `NewServer(cfg, svc, torrents, auth, authRoutes, logger)` takes it as a new required dependency. `methodNotAllowed` becomes a function of the `Allow` value.
- `PgStore.Ping` also probes `lampa_torrserver_hashes`, so the `database` health check catches a missing migration.

**Add-on `svtlv/torrserver-ownership.js`** (ES5 IIFE: protocol check, load guard `window.svtlv_torrserver_loaded`, every hook in try/catch so a failure leaves the original method in place)
- **Install early**: poll (200 ms, as `account.js`) only until `window.Lampa && Lampa.Torserver && Lampa.Storage` exist, then replace the properties. Replacing them needs nothing from app ready. After install, if the flag is set and `Lampa.Activity.active()` is a `mytorrents` activity that may already have rendered, `Lampa.Activity.replace()` it so it reloads through the wrapper.
- **`serverId()`**: take `Lampa.Storage.get('torrserver_url')`, or `torrserver_url_two` if that is empty. Trim, lowercase, strip a leading `http://` or `https://`, strip trailing `/`, then `Lampa.Utils.hash(...)`. The page scheme is never involved. With no URL the wrapper passes through, and Lampa shows its own "not configured" path.
- **Signed-in state** comes from `account.js`, which keeps a plain `localStorage` flag `svtlv_account_signed_in` (`'1'` or removed) on every `applySession`, written before its "user changed" check so a stale flag is always corrected. The ownership add-on reads the flag on each call:
  - flag absent → original Lampa behavior (anonymous);
  - flag set → signed-in path;
  - any ownership call answering `401` → remove the flag and send `Lampa.Listener.send('svtlv_account', {type: 'expired'})`. `account.js` follows that event and re-fetches the session, so its avatar and settings update. The current `my()` call still fails closed with a "session expired" Noty; the next call runs anonymously.
- **`my(success, fail)` signed in**: start the hashes GET **and** the original `my()` at the same moment, so upstream's `clear()` timing is unchanged. Wait for both to settle:
  - hashes failed (network, 5xx, 401) → Noty, then `fail()`;
  - original failed or empty → `fail()`;
  - otherwise keep only items whose lowercased `hash` is in the set → `success(filtered)`, or `fail()` when nothing is left.

  Nothing renders before both settle.
- **`add(object, success, fail)` / `hash(object, success, fail)` signed in**: call the original with the same arguments. In its success, if `json.hash` is present, `PUT` the row asynchronously; a failure only shows a Noty and never blocks playback. Then call the caller's `success(json)` if one was given. A failed TorrServer add creates no row, and `fail` is passed through untouched.
- **`remove(hash, success, fail)` signed in**: call the original. On its success, `DELETE` the row, then call the caller's `success` if given. On failure the row stays, because the torrent is still there.
- **`viewed(hash, success, fail)` signed in**: call `fail()` asynchronously. **`viewedSet(...)` signed in**: no-op.
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
- **Implementation Steps**: Go migration, store, service, API, wiring, tests; the `account.js` flag and expiry event; the ES5 add-on and its `index.html` line; the classification file with its drift test; documentation and design-doc deviations.
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

### Task 6: Publish the signed-in state from account.js

**Files:**
- Modify: `svtlv/account.js`

- [ ] in `applySession`, before the "user changed" check, set or remove `localStorage['svtlv_account_signed_in']` (try/catch; plain `localStorage`, never `Lampa.Storage`). `logOutDone` already calls `applySession({authenticated:false})`, so logout needs no extra code
- [ ] follow `Lampa.Listener` `svtlv_account` `{type:'expired'}` and re-fetch the session through the existing `fetchSession`/`applySession` path
- [ ] update the header comment (the add-on now also publishes the signed-in flag)
- [ ] check that `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js` passes
- [ ] manual check in a browser: the flag follows sign-in and sign-out, and a cookie removed by hand plus an `expired` event clears it and the avatar

### Task 7: Add the TorrServer ownership add-on

**Files:**
- Create: `svtlv/torrserver-ownership.js`
- Modify: `index.html` (inside the `svtlv:begin/end` block only)

- [ ] add the IIFE skeleton (protocol check, load guard, early install as soon as `Lampa.Torserver` exists, try/catch per hook, re-open an already active `mytorrents` activity) and `serverId()`
- [ ] wrap `my` (parallel hashes GET + original, filter before `success`, fail closed with a Noty), `add` and `hash` (row after a successful add, from `json.hash`), `remove` (delete the row after success), `viewed` and `viewedSet` (kept out while signed in); callbacks are invoked only if given, with the same arguments
- [ ] handle `401` from any ownership call (remove the flag, send `svtlv_account` `expired`, fail closed for the current list)
- [ ] add the `putScript` line to the seam block
- [ ] check that `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js` passes
- [ ] manual matrix in a browser with devtools:
  - anonymous: My Torrents unchanged;
  - signed in: only own rows;
  - `lampa-api` stopped: empty plus a Noty;
  - session expired: Noty, avatar cleared, next open anonymous;
  - start page = My Torrents: filtered on the first render;
  - add, play and remove still behave as before;
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
  - removal by one account (decision 4);
  - ownership API down while signed in (fail closed), and an expired session;
  - anonymous unchanged;
  - add-on file missing (Lampa still boots);
  - the scheme-free `serverId` matches across an http and an https origin.
- [ ] run the full suite in WSL from `backend/`: `make test`, `make race`, `make lint`
- [ ] run the ES5 gate: `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js`
- [ ] verify coverage stays at the Plan 1/2 level for `pkg/storage` and `pkg/api`

### Task 10: [Final] Update documentation
- [ ] `backend/README.md`: ownership routes in "API", the new table, the access-log redaction, and in "Operational caveats" the view-separation caveat, the two-slot assumption (decision 2) and the Android external-client limitation
- [ ] `docs/settings-sync-backend-design.md`:
  - §4 (new table) and §8 (routes);
  - the §10.3 table aligned with `storage-scope.json` (torrserver keys → user connection, `torrserver_use_link` device-only, the Plan 4 rule from decision 6);
  - §10.4 and §15 for decisions 2, 4, 5 and the Android limitation;
  - "Deviations recorded while implementing Plan 3" under §14.
- [ ] `CLAUDE.md`: mention `svtlv/torrserver-ownership.js` and `storage-scope.json` in the Svtlv add-ons section, and `TestStorageScope_matchBundle` next to `TestSensitiveSettings_matchUI` (an upstream pull that adds a storage key fails CI until it is classified)
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Deployment**
- Merge to `svtlvtv`, dispatch `deploy-docker.yaml` (Production). The migration runs on API start; no new secrets, inputs or Compose changes. Confirm `svtlvtv_lampa_api` turns healthy and `/health` shows `database` passing (it now also probes the new table).
- Rollback: revert and redeploy. The extra table is harmless to the previous API version; drop it manually only if the feature is abandoned.

**Manual verification (two accounts, two devices: browser and the `lampa-app/LAMPA` TV client, both WebView engines)**
- Account A adds torrent X, account B adds X and Y: A sees X only, B sees X and Y, and anonymous sees everything.
- A removes X: it is removed from TorrServer (as today); B's X disappears too (decision 4).
- Change `torrserver_url` on one device: My Torrents is empty for signed-in users there; restore it and the rows return.
- Stop `lampa-api` while signed in: My Torrents shows empty plus a notification, never the full list; playback still works.
- `torrserver_tracktimecode` on: signed-in playback shows no TorrServer marks and writes none; anonymous behaves as before.
- On the Android TV client with the external torrent client: playback does not record ownership; only "Add to My Torrents" does.
- Start page = My Torrents on the TV: the first render is already filtered.
- Day one: existing torrents are hidden from signed-in users until they re-add them (decision 5).

**Security note**
- This is application view separation, not access control: anyone with the TorrServer URL or credentials can still list and remove everything (§10.4).
