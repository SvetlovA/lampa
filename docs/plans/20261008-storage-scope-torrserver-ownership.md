# Plan 3: Storage Classification and TorrServer Ownership

## Overview
- Plan 3 of `docs/settings-sync-backend-design.md` (§14), covering §10.3 (storage ownership audit) and §10.4 (TorrServer data ownership).
- **Storage classification**: every storage key the current bundle uses gets an explicit scope in one machine-readable file, `svtlv/storage-scope.json`. A Go drift test fails CI when an upstream pull adds an unclassified key, the same way `TestSensitiveSettings_matchUI` already guards settings inputs. Plan 4's sync adapter reads this file as its allowlist.
- **One TorrServer link**: remove the built-in second URL input and link selector. Migrate the currently active URL to `torrserver_url`; all TorrServer requests then use only that field.
- **TorrServer ownership**: Lampa keeps talking to the configured TorrServer directly. A new `svtlv/` add-on wraps the public `Lampa.Torserver` object so that, for a signed-in user, My Torrents shows only torrents that user added on that TorrServer. Ownership rows `(user, TorrServer, torrent hash)` live in `lampa-api` behind three small authenticated routes.
- Add and playback keep calling TorrServer directly. Signed-in removal deletes only the current user's PostgreSQL row. Anonymous My Torrents uses a device-local guest list; guest removal deletes only its local entry. Neither removal sends `action: rem` to TorrServer.
- This plan now includes a small, marked `app.min.js` change for the built-in one-link setting and request paths, plus its `assembly.json` stamp. The ownership add-on remains in `svtlv/`; no change to TorrServer, Jackett, `css/app.css` or `lang/*`, and no new user-facing settings, configuration or secrets. Upstream bundle updates will require reapplying this fork change.

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
- **Legacy two-link behavior**: `ip()` picks `torrserver_url` / `torrserver_url_two` by `torrserver_use_link`, falling back only when the selected field is empty. Other paths (parser, authentication and status check) use the selected field directly. The speed-test button is inserted after the second URL input. Remove or adjust all of these references in Task 0. `Lampa.Utils.checkEmptyUrl` (~4079) prefixes the **page's** scheme, so it is not usable for a device-independent id. `Lampa.Utils.hash` (~4340) is a 32-bit string hash returned as a decimal string.
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

**Decisions made with the user (2026-10-08; removal, guest policy and one-link scope revised 2026-10-11):**

1. **The browser keeps calling TorrServer directly.** `lampa-api` never contacts TorrServer, so there is no proxy and no SSRF surface, and the backend never stores or sees TorrServer URLs.
2. **Ownership key is `(user_id, server_id, hash)`.** `server_id` comes only from the normalized sole `torrserver_url`. Changing that URL hides its rows; restoring it shows them again. Rows are never pruned automatically. Task 7 resolves the collision review with a synchronous ES5 SHA-256 digest (64 lowercase hex characters), rather than the short `Lampa.Utils.hash`.
3. **Add and playback keep their existing TorrServer calls.** The add-on (a) records a row after a successful add or playback-add, using the canonical hash TorrServer returned, (b) handles signed-in removal through the ownership API only, and (c) filters My Torrents before it renders.
4. **Remove deletes only the current user's database row.** `DELETE` matches the authenticated `user_id`, `server_id` and torrent hash. It never calls `Torserver.remove` / `action: rem`, even when this is the last ownership row. If A and B own X, A removing X leaves X on TorrServer and visible to B. Physical deletion is an operator action outside this plan; there is no reference-count cleanup because guests and other clients can also use the torrent. This restores the original §10.4 / §15 membership-isolation requirement.
5. **Torrents already on TorrServer have no owner** and stay hidden from signed-in users and guests. There is no claim UI or automatic assignment; a guest must re-add a torrent to put its hash in that device's list.
6. **The sole TorrServer URL and credentials are synced by Plan 4, not here.** Plan 3 classifies `torrserver_url`, `torrserver_auth`, `torrserver_login` and `torrserver_password` as user-scoped connection keys. Legacy `torrserver_url_two` and `torrserver_use_link` are migration-only, device-local keys and are excluded from Plan 4. On first login, if the device has connection values and the server has none, Plan 4 uploads them; once the server has values, the server wins until the user changes them. Until Plan 4, devices configured with different URLs see different membership sets; Plan 4 converges them on one URL.
7. **Shared `/viewed` timecodes stay out of both account and guest flows** (§10.4, §15). `Torserver.viewed` reports failure (Lampa then renders without TorrServer marks) and `viewedSet` is a no-op. Lampa's own local `file_view*` progress keeps working.
8. **Lists fail closed.** If signed-in ownership lookup fails, guest storage is unavailable or corrupt, or account status is unresolved at startup, My Torrents shows an empty state plus a notification; it never shows the unfiltered TorrServer list. A previously confirmed guest remains in guest mode through a later session-refresh outage.

9. **One TorrServer link.** The UI has one TorrServer URL input and no link selector. Core torrent, parser, authorization and status-check paths use only `torrserver_url`, with no hidden fallback to a legacy second URL. A one-time migration preserves the endpoint the device was actually using.

**Simplifications chosen after review:** no per-user row cap (a Keycloak-gated family server does not need one, and dropping it removes the transaction and advisory lock); no `addon_keys` exemption (add-on keys are not classified, and anything unclassified is device-only by definition).

### One-link transition (agreed 2026-10-11)

This is an explicit fork change to the built-in TorrServer configuration, separate from the ownership add-on. Edit the readable `app.min.js` bundle directly, since its source repo is not checked out. Remove the `torrserver_use_link` selector and `torrserver_url_two` input from the built-in server template; keep one URL input labelled with an existing TorrServer translation. Do not remove Jackett/Prowlarr second links or their independent parser controls.

- Before normal app startup reads TorrServer settings, migrate the **currently effective URL**: if legacy selection is `two`, choose secondary or primary; otherwise choose primary or secondary. Copy that value to `torrserver_url` only once. Store the original primary, secondary and selection in device-local `svtlv_torrserver_legacy_links` first, as a rollback record and migration marker. A later edit to the single URL must never be overwritten by old legacy keys on reload. If local persistence fails, notify the user and avoid claiming the migration succeeded.
- Leave `torrserver_url_two` and `torrserver_use_link` inert for rollback, but remove all normal runtime reads of them: `Torserver.ip`/`url`, TorrServer request authentication, TorrServer-backed parser/search paths, URL-change listeners and server status checks use `torrserver_url` only. Move the speed-test button's insertion point from the removed second input to the remaining input. An empty primary must stay unconfigured; it must not silently fall back to the old secondary key.
- Preserve the explicit URL scheme behavior of `Utils.checkEmptyUrl`. The sole URL and credentials become Plan 4's synced connection fields; the backup and legacy keys stay on this device and are not uploaded.
- Update `Manifest.app_version` and `assembly.json` together for the bundle change; leave `css_version` alone. Update `TestSensitiveSettings_matchUI`'s plain input inventory after removing the second URL input. No new translation keys or stylesheet changes are needed.

Migration examples:

| Before upgrade | Sole URL after upgrade |
|---|---|
| One selected; both configured | Previous primary |
| Two selected; both configured | Previous secondary |
| Selected field empty; other configured | Other configured URL |
| Both empty | Empty, unconfigured |

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
- Record a hash only after a successful original add/hash response, with the same Android external-client limitation as signed-in ownership. Task 7 records playback membership only when `torrserver_savedb` is enabled; explicit Add always records it. Transient playback does not create a dormant membership. Existing torrents are not automatically imported; re-adding one makes it visible locally.
- Removal updates only local storage and succeeds only if the write succeeds. If storage is unavailable/corrupt, show a notification and fail closed for the list; never fall back to the unfiltered list or physical deletion. Playback can continue when membership recording fails, with a notification.
- Keep guest data separate on login and logout; no automatic transfer into an account and no copying account hashes into guest storage. After logout, show the pre-existing guest list. Capture the guest/account mode and server at invocation, and ignore stale list responses after a mode change. Never redirect an in-flight account operation into guest storage after a `401`.
- An expired session fails closed for the current call. Mark account status as checking before re-fetching `/session`; subsequent calls wait until it resolves. A confirmed anonymous response selects guest mode. On an unavailable response, retain only a guest mode already confirmed during this page lifetime; otherwise fail closed. While initial session status is unknown, do not expose any list or route mutations using a guessed identity. Refresh the active My Torrents list when the session resolves.
- Suppress shared `/viewed` reads/writes for guests too; local Lampa progress continues to work.
- No automatic TorrServer cleanup when a guest removes a hash or clears storage. Guest references are not known to the backend, and other clients may also use those torrents.

Manual checks cover two browser profiles with independent guest lists, guest/account transitions, guest and account sharing X, guest removal preserving X for the account, clearing local storage, and API outages in guest mode.

### Manual TorrServer deletion and re-add

Membership is attached to the canonical torrent hash, not to a particular lifetime of the TorrServer record. Manually deleting a torrent from TorrServer does not delete its account rows or guest entries. The list intersection hides it while absent; re-adding the same hash makes every retained membership visible again, even when only one person performs the re-add.

| Sequence | Result under this plan |
|---|---|
| A and B own X; operator deletes X directly; A adds the same hash again | A's idempotent `PUT` retains A's row; B's old row also matches the returned TorrServer list, so both see X again |
| A owns X; operator deletes X; another client adds the same hash | A sees X again without taking an action |
| A removed X from My Torrents before the operator deletion; X is later re-added | A's row is gone, so X stays hidden from A until A adds it |
| Guest profile G owns X; operator deletes and later re-adds the same hash | G's retained local entry makes X visible again on that profile |
| Re-add creates a different canonical hash | Old memberships remain hidden; only a successful add that records the new hash creates visibility |

Do not automatically prune memberships merely because a hash is absent from one list response. TorrServer's public `Torserver.my()` calls `fail()` for both an empty list and a request error, so this add-on cannot distinguish confirmed deletion from a transient failure there. Even a reliable snapshot cannot prove a deletion was observed before another client re-added the hash. Strict “deletion resets everyone” semantics would need an operator action that clears account rows **and** a way to invalidate guest entries, or a new TorrServer record generation that all clients can observe. Those are outside Plan 3; the current behavior is hash-based continuity.

### Further corner cases to resolve during implementation

| Case | Consequence | Plan check / proposed handling |
|---|---|---|
| Playback calls `Torserver.hash` with `torrserver_savedb=false` (the default) | It can return a hash without creating a saved My Torrents entry. Recording that hash now creates a dormant membership that may become visible if someone saves the hash later | Verify TorrServer's actual list behavior. Prefer recording playback ownership only when `save_to_db` is enabled; explicit Add always requests `save_to_db:true`. Test both toggle values and re-add by another user |
| Legacy devices had two links configured and selected either one | The selected URL may differ from `torrserver_url`; blindly removing the selector would change the endpoint | Task 0 migrates the effective URL (selected link, or the other if empty) into `torrserver_url` before normal app use and preserves the old pair locally for rollback |
| Two different normalized server URLs produce the same `Lampa.Utils.hash` value | The 32-bit ID can merge otherwise separate membership lists | Treat the ID as a convenience, not a collision-resistant identity. Check whether a stronger ES5-compatible digest is warranted before shipping; test that distinct configured servers do not share rows |
| `app.min.js` loads before the add-on, or the add-on fails to load | A restored My Torrents screen can render the unfiltered upstream list before the wrapper installs; refreshing it afterward cannot undo disclosure | Verify first-render ordering on fast/cached and slow add-on loads. The first render must be gated until the wrapper is installed; if the current loader seam cannot guarantee this, revise the loading integration before deployment. Missing add-on remains an explicit operational limitation |
| TorrServer add succeeds but the response has no usable canonical hash, or the membership write fails | Playback can proceed, but the torrent will not appear in the user's list. Explicit Add currently shows its success toast without checking ownership persistence | Verify the real add response for explicit Add and playback. Notify on missing/invalid hash or failed membership write; do not claim a successful My Torrents save unless membership was persisted. Keep playback nonblocking |
| User signs out or switches accounts while a list, add, or remove is in flight | A late callback could render the old user's list, record a hash under the wrong mode, or refresh the new user's screen | Capture mode, user generation and server at invocation; start authenticated requests immediately with the current cookie. Suppress stale callbacks and reconcile the current activity after a transition. Test A → guest → B while each operation is pending |
| Two guest tabs add different hashes at nearly the same time | Read/modify/write of one `localStorage` map can lose one update | Re-read immediately before each write, listen for cross-tab `storage` changes, and test concurrent adds/removes. If simultaneous writes still lose data, document that guest lists are best-effort or use a stronger local store |
| TorrServer is unreachable, or its list is empty | `Torserver.my()` calls `fail()` in both cases; membership must stay intact | Show empty/error without pruning account or guest entries. Verify that entries return when TorrServer returns |

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
- `server_id`: `^[A-Za-z0-9_-]{1,64}$`, opaque to the backend (Task 7 uses a 64-character SHA-256 hex string). Otherwise `400 invalid_server_id`.
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
- **Install early**: the loader waits for this add-on before starting the bundle. An ES5 accessor on `window.Lampa` installs the wrappers synchronously when the bundle publishes its public API, before app startup. Poll (200 ms, as `account.js`) as a fallback only until `window.Lampa && Lampa.Torserver && Lampa.Storage` exist, then replace the properties. Replacing them needs nothing from app ready. If `Lampa.Activity.active()` is a `mytorrents` activity that may already have rendered, `Lampa.Activity.replace()` it so it reloads through the wrapper. Repeat that refresh when account status resolves or changes.
- **`serverId()`**: take only `Lampa.Storage.get('torrserver_url')`. Trim, lowercase, strip a leading `http://` or `https://`, strip trailing `/`, then synchronously SHA-256 the UTF-8 normalized value (ES5 implementation, no Web Crypto or Promise dependency). The page scheme is never involved. With no URL, list/add/playback use Lampa's existing "not configured" path; remove fails with a notification in either mode and never calls the original remove.
- **Account state** comes from `account.js`. It publishes a current-page state (`checking`, `signed_in`, `guest`, or `unavailable`) and a generation that changes on transitions; the ownership add-on uses that state for every call and ignores responses from an older generation. `checking` or `unavailable` blocks list exposure and add/remove mutations. A successful anonymous `/session` response or successful logout selects `guest`; a successful authenticated response selects `signed_in`. A refresh failure retains a guest state already confirmed during this page lifetime, but never turns an unknown or previously signed-in state into guest. An ownership `401` marks state `checking` immediately, sends `Lampa.Listener.send('svtlv_account', {type: 'expired'})`, and `account.js` re-fetches `/session`; the current call fails closed. Keep the existing `svtlv_account_signed_in` flag synchronized if needed by other add-ons, but do not infer guest status from its absence.
- **`my(success, fail)`**: in signed-in mode, start the hashes GET **and** the original `my()` at the same moment, so upstream's `clear()` timing is unchanged. In guest mode, read and validate the local hash set before starting the original `my()`. Wait for the membership source and TorrServer list to settle:
  - signed-in lookup failed (network, 5xx, 401) or guest storage failed → Noty, then `fail()`;
  - original failed or empty → `fail()`;
  - otherwise keep only items whose lowercased `hash` is in the set → `success(filtered)`, or `fail()` when nothing is left.

  Nothing renders before both settle.
- **`add(object, success, fail)` / `hash(object, success, fail)`**: in either known mode, call the original with the same arguments. On success, use `json.hash` to `PUT` the signed-in row or update guest storage. Playback records only when `torrserver_savedb` was enabled at invocation, and calls success immediately without waiting for account persistence. Explicit Add waits for persistence before calling success; a missing canonical hash or failed persistence shows a Noty and calls fail if supplied, preventing the built-in false success toast. Membership failures do not block playback. A failed TorrServer add creates no membership, and `fail` passes through untouched. If account state changes before the response, do not write the hash into the new mode's store.
- **`remove(hash, success, fail)`**: in signed-in mode, issue only the ownership `DELETE`, capturing server and account context at invocation. In guest mode, remove only the hash in that server's local list. Never call the original remove, including on failure, `401`, missing configuration or an account-state change. On success, call `success` if given. On failure, show a Noty and call `fail` if given; reconcile the list because a signed-in timeout may have committed. The upstream long-press handler immediately calls `item.disable()` without callbacks: after a failed request, reload the still-active originating My Torrents activity through the public API; do not navigate back if the user has left it. If membership lookup also fails, retain the fail-closed empty/error state. A later successful add/playback-add can claim the hash again.
- **`viewed(hash, success, fail)` in either known mode**: call `fail()` asynchronously. **`viewedSet(...)`**: no-op. For unresolved state, also avoid shared `/viewed` operations.
- **Requests**: same-origin `$.ajax` like `account.js` (the session cookie goes along automatically); timeout 15 s; `X-Lampa-Csrf: 1` on PUT/DELETE.
- **`index.html`**: load `svtlv/torrserver-ownership.js` inside the existing `svtlv:begin/end` block, then start the existing local/remote loader once on load or missing-file fallback. To guarantee ordering, the original loader invocation outside the seam is wrapped in a marked `startLampa` function; a marked readyState check ignores premature `loading` events. No stylesheet or bundle edit.

**Storage classification `svtlv/storage-scope.json`**
```json
{
  "version": 1,
  "scopes": {
    "user":            ["favorite", "mine_reactions", "file_view_*", "..."],
    "user_connection": ["torrserver_url", "torrserver_auth", "torrserver_login", "torrserver_password"],
    "device":          ["player", "torrserver_url_two", "torrserver_use_link", "svtlv_torrserver_legacy_links", "interface_size", "..."],
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
- **Implementation Steps**: the one-link core change and local migration; Go migration, store, service, API, wiring and tests; `account.js` session-state publishing and expiry event; the ES5 add-on with guest storage and its `index.html` line; the classification file with its drift test; documentation and design-doc deviations.
- **Post-Completion**: deploy through the manual workflow, then two-account and two-device smoke checks, TV-client checks and the rollback drill.

## Implementation Steps

### Task 0: Reduce built-in TorrServer settings to one URL

**Files:**
- Modify: `app.min.js` (only the marked fork changes for the settings template, migration and TorrServer-related readers)
- Modify: `assembly.json`
- Modify: `backend/pkg/storage/crypto_test.go`
- Create: `backend/pkg/storage/testdata/torrserver-one-link.test.cjs`

- [x] add the one-time effective-URL migration and local rollback record from the transition section; ensure an edited sole URL wins on every later load and a failed persistence is visible
- [x] remove the second TorrServer URL input and selector; use `torrserver_url` in `Torserver.ip`/`url`, request authentication, TorrServer parser/search, URL-change listeners and checks; move the speed-test insertion anchor to the remaining input
- [x] keep Jackett/Prowlarr second links and unrelated parser selectors unchanged; check that an empty primary never falls back to a legacy second URL
- [x] bump `Manifest.app_version` and its `assembly.json` copies/stamp; adjust `plainUIInputs` to the remaining TorrServer input
- [x] run the relevant backend bundle-guard test and `make test`; manually check the four migration rows above, changing the URL after migration, authenticated requests, parser/search, speed test, and the single settings row on browser and TV before task 1 (manual browser/TV checks skipped - require external clients and a configured TorrServer; not automatable in this workspace)

Task 0 validation and implementation notes:
- `node --test backend/pkg/storage/testdata/torrserver-one-link.test.cjs`: 37 checks pass against the actual bundle functions, including all legacy URL combinations, later edits/clearing, IndexedDB reserve/cache handling, write failures, corrupt backups, startup sequencing, authenticated requests, parser/search, speed test and status hooks.
- The backup is a plain localStorage journal with a completion marker. An incomplete journal never reapplies a legacy URL on a later load; an unresolved save stays visible until the sole URL is checked, so even an edit back to the original primary or an empty URL wins.
- `TestSensitiveSettings_matchUI` and the new `TestTorrserverSingleURL_matchBundle` pass. The latter guards legacy-key isolation, the single settings input, unrelated parser controls, template balance and matching assembly versions/stamps.
- `LAMPA_API_REQUIRE_DOCKER=1 make test`, `LAMPA_API_REQUIRE_DOCKER=1 make race`, and `make lint` pass natively on macOS with cgo and Docker; WSL is unavailable on this host. Backend coverage is 93.8% overall (`pkg/storage`: 94.1%, `pkg/api`: 96.7%).
- `npx --yes acorn@8.14.0 --ecma5 --silent app.min.js svtlv/*.js` and `git diff --check` pass. App version is now 3.3.5; CSS version remains 3.3.4.

### Task 1: Add the TorrServer ownership migration

**Files:**
- Create: `backend/migrations/00002_create_lampa_torrserver_hashes.sql`
- Modify: `backend/migrations/migrations_test.go`
- Modify: `backend/pkg/storage/migrate_test.go`

- [x] add the goose Up/Down migration with the table, primary key and check constraints from Technical Details
- [x] update `TestFS`'s expected file list in `migrations_test.go`
- [x] in `migrate_test.go` (pgtest): `Migrate` creates `lampa_torrserver_hashes`, a second run is a no-op, and the check constraints reject a bad `hash` and a bad `server_id`
- [x] run `make test` - must pass before task 2

Task 1 validation and implementation notes:
- Added the schema exactly as specified, with no foreign key to the user-data table. Tests verify ownership without a user-data document, the default creation timestamp, and preservation of existing rows on a second migration run.
- PostgreSQL tests cover 17 valid/invalid server-id and v1/v2 hash cases, duplicate membership rejection, concurrent/canceled migrations, and goose rollback/reapplication while preserving the existing user-data table.
- `LAMPA_API_REQUIRE_DOCKER=1 go test ./migrations ./pkg/storage -run 'TestFS|TestMigrate' -v`, `LAMPA_API_REQUIRE_DOCKER=1 make test` (including race detection), `make lint`, and `git diff --check` pass. Validation ran natively on macOS with cgo and Docker because WSL is unavailable on this host. Coverage remains 93.8% overall (`pkg/storage`: 94.1%, `pkg/api`: 96.7%).

### Task 2: Add the ownership store to PgStore

**Files:**
- Create: `backend/pkg/storage/torrents.go`
- Create: `backend/pkg/storage/torrents_test.go`
- Create: `backend/pkg/storage/mocks/torrent_store.go`
- Modify: `backend/pkg/storage/postgres.go`
- Modify: `backend/pkg/storage/postgres_test.go`

- [x] add the `TorrentStore` interface with its own `go:generate` line, and implement `ListHashes` (sorted), `ClaimHash` (`insert … on conflict do nothing`) and `ReleaseHash` on `PgStore`
- [x] extend `PgStore.Ping` to probe `lampa_torrserver_hashes`
- [x] write DB tests: list empty and sorted, claim idempotent, the same hash under two users and under two servers, release keeps the other user's row, release of a missing row is ok
- [x] write tests for store failures (closed pool → wrapped error) and the extended `Ping`
- [x] regenerate mocks, then run `make test` and `make race` - must pass before task 3

Task 2 validation and implementation notes:
- Added `TorrentStore` and its generated moq mock. `PgStore` lists hashes in order with a non-nil empty slice, claims idempotently without changing `created_at`, and releases only the specified user/server/hash row.
- Database tests cover v1/v2 hash sorting, concurrent duplicate claims, user/server/hash isolation, missing-row release, and membership independence from user documents. Closed-pool, canceled-context, and row-decoding failures preserve their underlying errors and return no partial list.
- `Ping` probes both application tables; tests verify that either missing table fails health with the wrapped PostgreSQL error.
- `LAMPA_API_REQUIRE_DOCKER=1 make test`, `LAMPA_API_REQUIRE_DOCKER=1 make race`, targeted store tests with race detection, `make lint`, and `git diff --check` pass. Mocks were regenerated with `go generate ./pkg/storage`. Validation ran natively on macOS with cgo and Docker because WSL is unavailable on this host.
- The three new store methods and `Ping` have 100% statement coverage. Overall backend coverage remains 93.8%; `pkg/storage` increases to 94.4%, and `pkg/api` remains 96.7%.

### Task 3: Add the Torrents service with validation

**Files:**
- Modify: `backend/pkg/storage/torrents.go`
- Modify: `backend/pkg/storage/torrents_test.go`

- [x] add the `Torrents` service (`NewTorrents`, `List`, `Claim`, `Release`) and `ValidationError` codes `invalid_server_id` / `invalid_hash`; map store failures to `ErrUnavailable`
- [x] canonicalize the user id, validate `server_id`, trim, lowercase and validate `hash`
- [x] write table-driven tests for valid inputs (uppercase hash lowered, v1 and v2 lengths, nil-dependency constructor errors)
- [x] write tests for an invalid user id, server id and hash, and for store unavailability (with the mock)
- [x] run `make test` - must pass before task 4

Task 3 validation and implementation notes:
- Added the `Torrents` service with required store/logger dependencies. All operations canonicalize UUIDs and validate opaque server IDs without changing their case or trimming them; claim/release trim and lowercase 40- or 64-character hexadecimal hashes before calling the store.
- Validation failures reach neither the store nor the logger. Store failures are logged with an operation name and wrapped as `ErrUnavailable`, preserving their underlying cause. Failed lists discard partial results; successful empty lists always return a non-nil slice.
- Mock-based table-driven tests cover constructor dependencies, valid server boundaries, UUID/hash canonicalization, invalid UUID/server/hash inputs, repeated claim/release calls, context forwarding, database failures, cancellation/deadlines, and wrapped store errors.
- Targeted service tests with race detection, `LAMPA_API_REQUIRE_DOCKER=1 make test`, `LAMPA_API_REQUIRE_DOCKER=1 make race`, `make lint`, and `git diff --check` pass. Validation ran natively on macOS with cgo and Docker because WSL is unavailable on this host.
- All new service methods and validation helpers have 100% statement coverage. Overall backend coverage is 94.0%; `pkg/storage` increases to 95.2%, and `pkg/api` remains 96.7%.

### Task 4: Add the ownership API routes

**Files:**
- Create: `backend/pkg/api/torrents.go`
- Create: `backend/pkg/api/torrents_test.go`
- Create: `backend/pkg/api/mocks/torrents.go`
- Modify: `backend/pkg/api/server.go`
- Modify: `backend/pkg/api/server_test.go`
- Modify: `backend/pkg/api/identity_test.go`
- Modify: `backend/pkg/api/identity.go` and `backend/pkg/api/userdata.go` (error-log path redaction)
- Modify: `backend/cmd/lampa-api/main.go` and `backend/cmd/lampa-api/main_test.go` (required constructor compatibility)
- Modify: `backend/pkg/api/flow_test.go`

- [x] add the `Torrents` interface with a `go:generate` moq line, the new required `NewServer` dependency (nil → error), and the GET/PUT/DELETE routes with `{server_id}` / `{hash}` path values and JSON 405/HEAD fallbacks with a per-route `Allow`
- [x] answer through the existing `writeServiceError` (validation → 400 with its code, `ErrUnavailable` → 503, unexpected → logged 500), and redact `/api/v1/torrservers/` paths in `accessLog`
- [x] write handler tests: the 200 list shape (`{"hashes":[]}` when empty), 204 PUT/DELETE, the user id taken only from the session, error mappings
- [x] write middleware tests: 401 anonymous, CSRF required on PUT/DELETE, 405 + the right `Allow`, HEAD → 405, an access log line without the hash; update `newTestServer*` helpers, the `NewServer` nil-dependency tests and the flow test
- [x] run `make test`, `make race`, `make lint` - must pass before task 5

Task 4 validation and implementation notes:
- The required `NewServer` dependency breaks its production caller unless it is adapted in the same commit. Task 4 therefore includes the minimal `NewTorrents` construction, `newAPI` argument forwarding and existing caller-test updates. Task 5 retains verification of this wiring and its new composed-handler route test.
- Request-path redaction also applies to authentication, unexpected-service-error and panic logs, preventing those paths from exposing server ids or hashes.

- Generated the `api.Torrents` moq mock and added handler/middleware tests for empty and populated lists, session-only user ids, path arguments, 204 mutations, wrapped error mappings, deadlines, authentication failures, CSRF, per-route `Allow`, HEAD and log privacy. A real-service flow test covers v1/v2 canonicalization, sorted results, idempotent claims/releases, user/server isolation, independence from user-data deletion and fail-closed store outages.
- `LAMPA_API_REQUIRE_DOCKER=1 make test`, `LAMPA_API_REQUIRE_DOCKER=1 make race`, `make lint` and `git diff --check` pass. Validation ran natively on macOS with cgo and Docker because WSL is unavailable on this host. Lint found two test-only issues (cookie attributes and assertion style), both corrected before the final race/lint checks.
- All three new handlers and the path-redaction helper have 100% statement coverage. Overall backend coverage is 94.1%; `pkg/api` increases to 97.1%, and `pkg/storage` remains 95.2%.

### Task 5: Wire the Torrents service in main

**Files:**
- Modify: `backend/cmd/lampa-api/main.go`
- Modify: `backend/cmd/lampa-api/main_test.go`

- [x] construct `storage.NewTorrents(store, logger)` and pass it through `newAPI` to `api.NewServer`
- [x] extend the wiring test to hit the new route through the composed handler (401 anonymous)
- [x] run `make test` - must pass before task 6

Task 5 validation and implementation notes:
- Verified the production wiring added in Task 4: `start` constructs `storage.NewTorrents` from the PostgreSQL store and logger, and `newAPI` forwards it to `api.NewServer`. Updated the constructor comment to cover both services.
- Extended the database-backed startup test to require a JSON 401 from the ownership list route. Added composed-handler tests for anonymous rejection without service/Keycloak calls, authenticated forwarding of the session user and server id to the supplied Torrents service, and a store outage returning JSON 503 without partial hashes.
- Targeted wiring tests with race detection, `LAMPA_API_REQUIRE_DOCKER=1 make test`, `LAMPA_API_REQUIRE_DOCKER=1 make race`, `make lint`, and `git diff --check` pass. Validation ran natively on macOS with cgo and Docker because WSL is unavailable on this host. Overall coverage remains 94.1%; `pkg/storage` remains 95.2%, and `pkg/api` remains 97.1%.

### Task 6: Publish resolved account and guest state from account.js

**Files:**
- Modify: `svtlv/account.js`
- Create: `docs/tests/account-state.test.cjs`
- Modify: `.github/workflows/tests.yaml`

- [x] publish a current-page account state and generation before the initial `/session` request (`checking`), after each authenticated/anonymous result, after successful logout, and when an ownership `401` starts session re-checking; notify the ownership add-on on transitions. A refresh failure may retain only a guest state already confirmed in this page lifetime; an initial failure and a formerly signed-in state fail closed
- [x] keep `localStorage['svtlv_account_signed_in']` (`'1'` or removed) synchronized in `applySession` (try/catch, plain `localStorage`), but do not use a missing flag as proof of guest status. Do not persist the readiness state across reloads
- [x] follow `Lampa.Listener` `svtlv_account` `{type:'expired'}` and re-fetch the session through the existing `fetchSession`/`applySession` path; prevent stale responses from restoring a previous identity after the transition
- [x] update the header comment to document state publication and expiry handling
- [x] check that `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js` passes
- [x] manually check startup, sign-in, sign-out, session expiry, and session/API outages: account state and avatar change together; an initial outage never selects guest merely because the flag is absent (manual browser/TV checks skipped - require live sign-in sessions and external clients; not automatable in this workspace)

Task 6 validation and implementation notes:
- `window.svtlv_account_state` exposes `{status, generation, user_id}` before app readiness and the initial request. Each checking/resolution publishes a new snapshot through `Lampa.Listener` `svtlv_account` `{type: 'state', state: snapshot}`. The ownership add-on can read the snapshot immediately and follow these events without using persisted readiness.
- Every session recheck invalidates earlier GET callbacks; device sign-in and successful logout also invalidate earlier responses. Avatar and the guarded plain-localStorage flag update before state listeners run. An outage retains only a currently confirmed guest, never an initial unknown state or a guest identity from before an intervening sign-in.
- Initial outages keep the heartbeat and expiry listener available; a later successful check activates the account UI once. Expiry during logout immediately blocks ownership without starting a session renewal; the existing pending-GET drain is preserved, and failed logout resumes checking when needed.
- Added 21 Node built-in tests executing the full actual add-on with controlled public Lampa APIs, HTTP callbacks and timers. They cover early/delayed startup, absent/stale flags, malformed responses, outages, expiry races, concurrent refreshes, avatar/flag publication ordering, storage errors, logout/SSO ordering, TV device sign-in, reload isolation and non-HTTP origins. The frontend CI job runs this test without new dependencies; `docs/` is already excluded from the web image.
- `node --test docs/tests/account-state.test.cjs`, the pinned ES5 gate, `LAMPA_API_REQUIRE_DOCKER=1 make test`, `LAMPA_API_REQUIRE_DOCKER=1 make race`, `make lint`, and `git diff --check` pass. Backend checks ran natively on macOS with cgo and Docker because WSL is unavailable here. Coverage remains 94.1% overall (`pkg/storage`: 95.2%, `pkg/api`: 97.1%). Live browser/TV verification remains in Post-Completion.

### Task 7: Add the TorrServer ownership add-on

**Files:**
- Create: `svtlv/torrserver-ownership.js`
- Modify: `index.html` (seam plus marked loader deferral/readyState guard required for first-render ordering)
- Create: `docs/tests/torrserver-ownership.test.cjs`
- Modify: `.github/workflows/tests.yaml` (run the ownership runtime tests)

- [x] add the IIFE skeleton (protocol check, load guard, early install as soon as `Lampa.Torserver` exists, guarded hooks that fail closed for list/remove, re-open an already active `mytorrents` activity) and `serverId()`
- [x] implement the versioned `localStorage['svtlv_torrserver_guest_hashes']` map, canonical hash validation, per-server lookup/write, corruption/unavailable handling, and guest list filtering; keep it outside `Lampa.Storage` and Plan 4 sync
- [x] wrap `my` (account: parallel hashes GET + original; guest: local hashes + original; filter before `success`; fail closed with a Noty), `add` and `hash` (record canonical `json.hash` in the store for the captured mode), `remove` (database DELETE or guest local deletion only; never original remove; reconcile the optimistic card state on failure), `viewed` and `viewedSet` (kept out in both modes); callbacks are invoked only if given, with the same arguments
- [x] handle `401` from any ownership call (switch state to checking, send `svtlv_account` `expired`, fail closed for the current list); ignore late responses from an earlier account generation and refresh an active My Torrents list after state transitions
- [x] resolve the corner-case table above before release: saved versus transient playback hashes, first-render ordering, missing canonical hashes, account changes during requests, and concurrent guest storage writes
- [x] add the `putScript` line to the seam block
- [x] check that `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js` passes
- [x] manual matrix in a browser with devtools (skipped - live sign-in, TorrServer, browser profiles and TV clients are external; not automatable in this workspace; controlled runtime tests cover the local behavior):
  - two browser profiles: each guest sees only its local hashes; guest removal changes only its local list and sends no `action: rem`;
  - signed in: only own rows;
  - `lampa-api` stopped: empty plus a Noty;
  - session expired: current list fails closed; after a confirmed anonymous `/session` response, next open shows only the device guest list;
  - start page = My Torrents: filtered on the first render;
  - add/play retain their TorrServer calls; account removal sends only the ownership DELETE and guest removal writes only local storage, with no `action: rem`;
  - A and B own X: A removes X, B still sees and plays X; removing the last ownership row also keeps X on TorrServer;
  - operator deletes X directly, then A re-adds the same hash: retained B and guest memberships reappear; a user who removed their membership does not reappear;
  - failed or timed-out removal, and unavailable/corrupt guest storage: notification and list reconciliation, no fallback to TorrServer deletion or unfiltered display;
  - guest/account switches keep lists separate; clearing guest storage removes only the guest list; initial API outage fails closed, while an already confirmed guest can keep using its local list through a later outage;
  - upgrade a device that used link two, then edit the sole URL and reload: the migrated active URL is preserved once, and old link values never take over again;
  - playback with `torrserver_savedb` off and on, add without a usable returned hash, and explicit Add with an ownership write failure;
  - race initial My Torrents startup against delayed add-on loading, account switches against pending callbacks, and two guest tabs adding different hashes;
  - remove from the long-press menu (no callbacks) works.

Task 7 validation and implementation notes:
- Added the ES5 ownership add-on with fail-closed list filtering, account PUT/DELETE and versioned guest storage, captured account generations/server ids, expiry rechecks, deferred removal reconciliation, storage-change refreshes, and disabled shared viewed reads/writes. Original add/playback requests remain direct; no removal path calls the original destructive method.
- Corner-case decisions: playback claims only when `torrserver_savedb` is enabled at invocation; explicit Add always claims and reports success only after persistence. Missing canonical hashes and membership errors notify without blocking playback. Stale operations never move between accounts and guest storage, and old 401 responses cannot invalidate a newer identity.
- Collision review: replaced the proposed 32-bit decimal id with a synchronous ES5 SHA-256 implementation. Tests match Node's independent SHA-256 implementation for ASCII, Unicode and multi-block inputs and demonstrate separation of two URLs that collide under Lampa's hash. There was no earlier ownership frontend to migrate; any manually seeded decimal server ids remain stored but are not used by this add-on.
- First-render ordering required a small change outside the loader seam: defer the existing boot selection into a marked `startLampa` function, called once after ownership load or its missing-file fallback. The add-on intercepts the bundle's public API publication, with 200 ms polling for incomplete APIs/engines without window accessors. The marked readyState check blocks premature loading events. Tests execute the actual bundle API publisher and the full index loader, including delayed load, duplicate events, missing files, Android remote loading and both fallbacks. Unsupported window-accessor compatibility still needs the external TV matrix.
- Guest writes re-read and validate the entire map immediately before each write and verify persistence. Sequential interleavings across tabs preserve other hashes/servers; storage events refresh guest views. Truly simultaneous cross-process read/modify/write updates remain best-effort because localStorage provides no atomic transaction. This follows the corner-case table's documented best-effort option.
- Added 75 Node built-in runtime tests for the actual add-on and loader; CI runs them without new dependencies. Combined with the existing account-state and one-link tests, all 133 checks pass. Tests cover membership isolation, invalid/corrupt/unavailable data, first-use gating, callback arguments, 401/outage races, guest/account transitions, canonical hashes, saved/transient playback, failed optimistic removal, clearing storage and retained membership after operator deletion/re-add.
- `LAMPA_API_REQUIRE_DOCKER=1 make test`, `LAMPA_API_REQUIRE_DOCKER=1 make race`, `make lint`, the pinned ES5 gate, and `git diff --check` pass. Backend validation ran natively on macOS with cgo and Docker because WSL is unavailable here. Coverage remains 94.1% overall (`pkg/storage`: 95.2%, `pkg/api`: 97.1%). Actual TorrServer response formats/list behavior and live browser/TV verification remain in Post-Completion.

### Task 8: Classify every storage key with a drift test

**Files:**
- Modify: `backend/pkg/storage/crypto_test.go`
- Create: `svtlv/storage-scope.json`

- [x] factor the bundle read into `readBundle(t)`, shared with `TestSensitiveSettings_matchUI`
- [x] write the extraction and precedence helpers with table-driven tests (each pattern: single and double quotes, `localStorage`, Params `select`/`trigger`, `data-name`, a concatenated fragment; exact beats prefix, longest prefix wins, a duplicate is rejected)
- [x] add `TestStorageScope_matchBundle` with the rules from Technical Details, and run it against an empty scope file: its failure output is the inventory
- [x] classify every key by meaning at its read/write sites (§10.3) until the test passes; record non-obvious choices in `notes` (`language`/`tmdb_lang`, `plugins*`, `menu_sort`/`menu_hide`/`start_page`, `torrserver_savedb`, `file_view_*` CUB profile suffix, keys built from variables)
- [x] audit `Storage.sync` and the CUB account write paths, and note in `notes` which `user` keys CUB also syncs (for example `online_last_balanser`) so Plan 4 can avoid overwriting them
- [x] run `make test` and `make lint` - must pass before task 9

Task 8 validation and implementation notes:
- Shared `readBundle(t)` across all three bundle guards. The storage guard skips only a backend-only checkout without `app.min.js`; a missing/invalid classification file fails. Version, scope arrays, unknown fields, entry syntax, duplicate rules and trailing JSON are validated.
- Ran the new guard against an empty six-scope file first: it reported 215 unclassified keys/fragments. The completed inventory has 215 exact/prefix entries: 13 user rules, four user connection fields, 106 device rules, 32 local connection rules, 16 external-service rules and 44 cache rules. Conservative settings-button and non-storage concatenation matches remain excluded from export and are explained in `notes`.
- Extraction tests cover each Storage method, local/session storage, bare and qualified Params registrations, bare CUB sync registrations, both quote forms, escaped/unescaped templates, whitespace, punctuated names, concatenated fragments and deduplication. Matching tests cover exact/longest-prefix precedence, real duplicate rejection, stale entries/prefixes and sensitive-key restrictions. Policy checks keep plugins, player/menu/startup settings, legacy links, CUB state/cursors and unknown add-on keys outside the export allowlist.
- Audited variable-built progress, CUB trackers, content rows, worker update timestamps and the migration journal. Explicit literal guards cover bases not reached through the automatic patterns. `file_view_*` requires active-CUB-profile mapping in Plan 4; arbitrary plugin, console and remote-configuration keys stay local unless explicitly classified.
- Audited CUB's six Storage.sync registrations, bookmark and timeline writers, person subscriptions, reactions and account/plugin state. Notes identify overlapping user-data writers and CUB's manual whole-localStorage backup/restore, which can replace values independently of automatic sync. This classification prepares Plan 4; it does not change those upstream writers.
- Targeted helper/bundle tests, `LAMPA_API_REQUIRE_DOCKER=1 make test`, `LAMPA_API_REQUIRE_DOCKER=1 make race`, `make lint` and `git diff --check` pass. Checks ran natively on macOS with cgo and Docker because WSL is unavailable here. Lint's test-table allocation finding was corrected. Coverage remains 94.1% overall (`pkg/storage`: 95.2%, `pkg/api`: 97.1%).

### Task 9: Verify acceptance criteria
- [x] verify that the Overview and all nine Solution Overview decisions are implemented
- [x] verify edge cases (automated checks pass; live browser/TV and real TorrServer checks skipped - require external clients and a configured server, not automatable in this workspace):
  - only one TorrServer URL input is shown; no link selector remains; URL checks, search/parser, authentication and speed test use the sole URL;
  - all four legacy migration combinations preserve the previously active endpoint; the backup remains device-local, and an empty sole URL has no legacy fallback;
  - the same hash for two users, and the same user on two servers;
  - a TorrServer URL change hides rows, and changing back restores them;
  - removal by one account preserves the other account's row and the TorrServer torrent; removing the last row also preserves the torrent (decision 4);
  - ownership API down while signed in (fail closed), and an expired session;
  - guest add/list/remove uses only local membership and never leaks signed-in hashes; two browser profiles have independent lists;
  - guest/account transitions, guest plus account ownership of X, cleared/corrupt local storage, and API outages follow the agreed state rules;
  - direct TorrServer deletion hides a retained hash, and re-adding that hash restores visibility only for memberships that still exist;
  - add-on file missing (Lampa still boots; upstream My Torrents and remove remain unprotected until the add-on is restored);
  - the scheme-free `serverId` matches across an http and an https origin.
- [x] run the full suite from `backend/`: `make test`, `make race`, `make lint` (WSL-specific execution skipped - unavailable on this macOS host; all commands pass natively with cgo and Docker required)
- [x] run the ES5 gate: `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js`
- [x] verify coverage stays at the Plan 1/2 level for `pkg/storage` and `pkg/api`

Task 9 validation and acceptance audit notes:
- Audited the Overview and all nine decisions against the implementation and tests. The scope inventory/drift guard supplies Plan 4's allowlist; the four sole-URL connection fields and first-login precedence are recorded in `storage-scope.json`. Actual connection synchronization remains Plan 4 work, as decision 6 requires.
- Decisions 1–5: original add/playback/list calls still go directly to TorrServer; SHA-256 ids partition membership by normalized sole URL; canonical returned hashes drive claims; account removal issues only an authenticated membership DELETE, including the last row; pre-existing unowned torrents stay hidden. Go service/API/PostgreSQL tests verify authenticated identity, user/server isolation, idempotency, validation, outages, CSRF and path redaction.
- Decisions 7–9: wrappers suppress shared viewed reads/writes for every account state; lists fail closed until identity and membership resolve; the marked one-link patch removes the second input/selector and all normal legacy reads. The actual-bundle tests cover migration combinations, later edits/clearing, persistence failures, authentication, parser/search, status and speed-test paths. Legacy keys and the rollback journal stay device-local.
- Added a shared-server acceptance scenario running the actual add-on for two accounts and two independent guest profiles against controlled external services. It verifies account/guest isolation, guest removal preserving account membership, same-account server changes/restoration, removal of the last account row without physical deletion, and operator deletion/re-add restoring only retained account/guest memberships. Added an explicit HTTP/HTTPS page-origin equivalence test for scheme-free server ids.
- Existing runtime tests cover initial/refresh API outages, expiry and stale generations, guest/account transitions without copying hashes, cleared/corrupt/unavailable guest storage, saved versus transient playback, pending removal reconciliation, suppressed timecodes, first-render publication ordering and missing-file boot fallback. Missing/disabled add-ons intentionally leave upstream shared list/removal behavior available; browser/TV engine compatibility and actual TorrServer response formats remain in Post-Completion.
- Added a Go regression test showing that response control reaches the underlying writer through access logging while ownership paths stay redacted. Assertions run outside the handler, as required by the lint gate. API coverage increases from 97.1% to 97.5%, exceeding the earlier plans' 97.3–97.4%; storage remains 95.2%, exceeding the earlier 94.1–94.4%. Overall coverage is 94.2% excluding mocks.
- All 135 frontend runtime checks pass (`node --test docs/tests/account-state.test.cjs docs/tests/torrserver-ownership.test.cjs backend/pkg/storage/testdata/torrserver-one-link.test.cjs`). `LAMPA_API_REQUIRE_DOCKER=1 make test`, `LAMPA_API_REQUIRE_DOCKER=1 make race`, `make lint`, the pinned ES5 gate, Compose configuration validation and `git diff --check` pass. Backend checks ran on macOS with Go 1.27.2, cgo and Docker; WSL/CI's Go 1.26 execution and live browser/TV checks remain external verification.

### Task 10: [Final] Update documentation
- [ ] `backend/README.md`: ownership routes in "API", the new table, the access-log redaction, and in "Operational caveats" the view-separation caveat, device-local guest membership, the one-link migration and the Android external-client limitation
- [ ] `docs/settings-sync-backend-design.md`:
  - §4 (new table) and §8 (routes);
  - the §10.3 table aligned with `storage-scope.json` (sole `torrserver_url` and credentials → user connection; legacy link keys and backup → device-only; the Plan 4 rule from decision 6);
  - §10.4 and §15 for decisions 2, 4, 5 and 9, guest device-local membership, session readiness, and the Android limitation;
  - "Deviations recorded while implementing Plan 3" under §14; record the one-link core change and the revised guest policy, while membership-only removal agrees with the original design.
- [ ] `CLAUDE.md`: mention the targeted one-link core patch, `svtlv/torrserver-ownership.js` and `storage-scope.json` in the Svtlv add-ons section, and `TestStorageScope_matchBundle` next to `TestSensitiveSettings_matchUI` (an upstream pull that adds a storage key fails CI until it is classified)
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Deployment**
- Merge to `svtlvtv`, dispatch `deploy-docker.yaml` (Production). The migration runs on API start; no new secrets, inputs or Compose changes. Confirm `svtlvtv_lampa_api` turns healthy and `/health` shows `database` passing (it now also probes the new table).
- Rollback: revert and redeploy. The extra table is harmless to the previous API version; drop it manually only if the feature is abandoned.

**Manual verification (two accounts, two devices: browser and the `lampa-app/LAMPA` TV client, both WebView engines)**
- Upgrade devices with link one selected and with link two selected: each keeps using the URL it previously used. The settings page shows one URL field; changing or clearing it never restores a legacy link. Confirm authenticated requests, search/parser and speed test use that field.
- Account A adds torrent X, account B adds X and Y: A sees X only and B sees X and Y. A guest who has not added them sees neither; a guest who adds X sees X only on that device.
- A removes X: only A's database row disappears; B still sees and plays X, and X remains on TorrServer. B then removes X: the torrent still remains on TorrServer (decision 4).
- Delete X directly on TorrServer while A, B and a guest profile have retained memberships: X disappears from all filtered lists. Re-add the same hash from A or another client: X reappears for every retained membership. A user who explicitly removed their membership stays hidden.
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
