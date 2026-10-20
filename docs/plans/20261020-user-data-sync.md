# Sync user data with lampa-api (Plan 4)

## Overview
- Final roadmap plan (design §14 Plan 4). It connects Lampa's browser storage to the
  existing `/api/v1/user-data` document, so a signed-in user gets the same config and
  personal data on every device.
- Keep it as simple as possible. Config and user data must sync reliably. History,
  progress and torrent views are best-effort: they sync when they fit and are dropped
  otherwise, with no extra logic to force them through.
- **One whitelist, edited by hand.** `svtlv/storage-scope.json` is the only list of synced
  keys: `user` and `user_connection` (must sync) and a new `user_optional` (best-effort).
  The add-on reads this file at runtime and the backend stores a flat `{key: value}` map
  without knowing key names, so adding a key is a one-line JSON edit with no JS or Go change.
- The UI keeps using `localStorage` exactly like upstream Lampa; only the add-on talks to
  the API, so upstream bundle updates do not conflict with sync.
- A running device picks up changes from other devices by **polling with an ETag**
  (document hash): on screen return, every 60s while visible, and before each save.
  Changes are applied live, without a reload, also while a video is playing (the stream
  URL is built at playback start, so the current video keeps playing).
- Anonymous mode stays local-only, with no user-data request at all. Logout clears the
  synced keys (resolves design §16).
- Work: a new `svtlv/account-sync.js` add-on, the marked `index.html` seam, small
  `account.js` changes, a flat document + precondition + ETag in the backend. No change to
  `app.min.js`, `css/app.css`, `lang/*` or `assembly.json`.

## Context (from discovery)
- Backend: `GET/PUT/DELETE /api/v1/user-data` store one JSONB document per user with 8 fixed
  sections (`storage.Sections`), 1 MiB per section, 2 MiB body, schema version 1
  (`backend/pkg/storage/document.go`, `service.go`, `backend/pkg/api/userdata.go` with
  `orderedData`). `SensitiveSettings` (`crypto.go`) seals `torrserver_login/password` and the
  Jackett/Prowlarr keys found under `settings`. `404 user_data_not_found` when no document
  exists. PUT needs `Content-Type: application/json` (else 415), `X-Lampa-Csrf: 1` and a
  matching `Origin` (else 403). No production client writes documents yet; Plan 4 is the
  first writer.
- `X-Lampa-Expected-User` → `409 account_changed` exists only for torrent routes
  (`torrentIdentityMatches` in `backend/pkg/api/torrents.go`).
- `svtlv/storage-scope.json` (scopes `user`, `user_connection`, `device`, `connection`,
  `external`, `cache`; exact keys beat prefixes, longest `*` prefix wins) is checked by
  `TestStorageScope_matchBundle` (`backend/pkg/storage/crypto_test.go`, scope names at ~475)
  and `docs/tests/ownership-docs.test.cjs`. It ships in the `lampa-web` image.
- `svtlv/account.js` publishes `window.svtlv_account_state {status, generation, user_id}`
  and the `svtlv_account` Listener event, and rechecks the session on `{type: 'expired'}`.
  `logOutDone` publishes guest, then on the browser path navigates to `logout_url`. Its
  Account page text says data "stays on this device" (`account.js:36`, ru `:72`).
- `Lampa.Storage` (`app.min.js` ~49917–50010): `set` JSON-stringifies objects/arrays into
  `localStorage`, updates the memory cache (`readed`) and sends `change` (unless
  `nolisten`); `get` parses only values starting with `[`/`{` and converts `'true'`/digits
  itself; on `QuotaExceededError` the key moves to IndexedDB (`reserve`). Settings inputs
  and toggles save through `Storage.set(name, value)` (~49007, ~49019).
- `Lampa.Favorite.read()` re-reads `favorite` and sends `state:changed`. `torrserver_url` is
  read when playback starts, so changing it does not interrupt the current video.
- Playback saves `file_view` every 2 minutes (`saveTimeView`).
- Every authenticated request is revalidated against Keycloak with no cache (§5.3.1), so
  each poll costs one Keycloak call; 60s × a few devices is acceptable.
- `index.html` seam: account.js (no wait) → torrserver-ownership.js → `startLampa()`.
  `torrserver-ownership.js` installs a `window.Lampa` accessor; other add-ons must poll.
- Tests: `node --test docs/tests/*.test.cjs` run the real add-on in `vm`; `acorn --ecma5`
  parses `svtlv/*.js`; Go `make test/race/lint` (native macOS with cgo + Docker works here).

## Development Approach
- **testing approach**: Regular (code first, then tests)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change
- maintain backward compatibility: a missing, failing or throwing `account-sync.js` must still boot Lampa

## Testing Strategy
- **unit tests**: `docs/tests/account-sync.test.cjs` runs the add-on in `vm` like
  `torrserver-ownership.test.cjs`, with fake `$.ajax`, `localStorage`, timers,
  `Lampa.Storage`, `Lampa.Listener`, `Lampa.Favorite`, `storage`/`visibilitychange`
  events and `location.reload`, and the real `svtlv/storage-scope.json`.
- Go: table-driven cases in `backend/pkg/storage/document_test.go`, `service_test.go`,
  `crypto_test.go` and `backend/pkg/api/userdata_test.go`.
- No e2e suite exists; browser and TV checks are in Post-Completion.

## Progress Tracking
- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix
- keep plan in sync with actual work done

## Solution Overview

```
            upstream Lampa (unchanged)
     UI ◄──► Lampa.Storage ◄──► localStorage
                                    ▲  │ change events
               boot apply / live    │  ▼
                 ┌──────── account-sync.js ◄──── svtlv/storage-scope.json
                 │ poll: GET If-None-Match → apply │      (the whitelist)
                 │ local change → GET + PUT dirty  │
                 └────────────────┬────────────────┘
                                  ▼
                lampa-api: flat {key: value}, no key names
                (except sealed credentials) → Postgres
```
**Note:** Lampa itself never talks to our API. The add-on is the only part that knows
which keys sync, and it learns that from one JSON file.

Four rules keep it simple and safe:

1. **Before boot, write raw; after boot, write through `Lampa.Storage`.** Server data is
   applied before boot when the device has synced before. After boot, account changes
   (login, other user) reload the page if anything changed; changes from other devices
   of the same account are applied live (rule 4).
2. **Save = fresh server copy + keys this page changed.** Every save first polls, puts this
   page's changed ("dirty") keys on top of the latest server document, and PUTs it. A page
   open for days cannot roll back what another device saved. If two devices change the
   same key, the last one wins (§7).
3. **The marker says whose data is on the device.** `localStorage['svtlv_sync_user']` holds
   the user ID. Data from another user, or guest data, is cleared before any apply.
4. **Poll with an ETag and apply live.** The ETag is the SHA-256 of the stored document. A
   `304` means nothing changed. On `200`, each synced key that differs and is not dirty
   here is written with `Lampa.Storage.set`, so screens react as if the user changed it.
   This also happens during playback.

Boot (only on a device that has synced before):

```
index.html seam
  account.js ─────────────── (loads, no wait)
  torrserver-ownership.js ── loaded
  account-sync.js ────────── try { svtlv_sync_boot(done) } catch → done()
       arm 3s timer first
       no marker → done() at once           (guest / first login: no request)
       in parallel: GET svtlv/storage-scope.json?v=<cache_version>
                    GET /api/v1/user-data  X-Lampa-Expected-User: <marker>
         200 → apply synced keys → ready    ─┐
         404 → ready, all local keys dirty   │
         401 → clear synced keys + marker    ├─ done() → startLampa()
         409 → clear synced keys + marker    │
         503 / error / timeout / no scope → not ready ─┘
```
**Note:** Lampa starts only after this finishes or gives up after 3 seconds, so it reads
server values straight from `localStorage`. The whitelist is fetched in parallel, so it
adds no wait. Clearing on 401/409 also needs the whitelist; without it nothing is
cleared, and the post-boot path retries. The header makes the server answer 409 when
the cookie belongs to someone other than the marker.

After boot, everything is driven by the current account state and the marker:

```
status signed_in(U):
   marker ≠ U (or none) → clear synced keys → GET:
        200 → apply → marker = U → reload if anything changed
        404 → marker = U → all local synced keys dirty → save
   marker = U, boot not ready → GET → apply → reload if changed
   marker = U, ready → uploads on, polling on
status guest and marker set → clear synced keys + marker → reload
                              (skip reload while account.js navigates to logout_url)
storage event on svtlv_sync_user (another tab) → reload
Storage 'change' on a synced key → mark dirty → 2s debounce → save
visible again │ every 60s while visible → poll
```
**Note:** "guest" means the confirmed guest state, from a logout or from an expired
session found at any time. The marker is written before any reload, so the reloaded
page takes the pre-boot path and does not loop.

Key decisions:
- **Raw strings.** Each document value is the exact `localStorage` string. The round trip
  is lossless and needs no knowledge of Lampa value types.
- **Whitelist only.** A key syncs only when `classify(key)` (same precedence as the Go
  guard: exact beats prefix, longest prefix wins) returns `user`, `user_connection` or
  `user_optional`. Import follows the same rule and accepts only string values, so a
  document can never push `plugins` or another device key. Unknown keys stay local.
- **Missing server keys.** With marker == U, a synced key present locally but absent on
  the server is marked dirty and uploaded. With marker ≠ U, local keys are cleared first,
  so guest data reaches an account only on a 404 first login (§6.3).
- **A dirty key with no `localStorage` value** (moved to IndexedDB by Lampa on quota) is
  skipped, so the server keeps its value.
- **A key removed from the whitelist** stops syncing; its old value stays in the server
  document but is never applied. No cleanup is needed.
- **No feature flag.** Rollback = revert + redeploy (agreed).

## Technical Details

### The whitelist (`svtlv/storage-scope.json`)

Draft lists, **the user decides the final ones** before Task 3:

| Scope | Keys | Guarantee |
|---|---|---|
| `user` | `language`, `tmdb_lang`, `favorite`, `mine_reactions`, `person_subscribes_id` | must sync |
| `user_connection` | `torrserver_url`, `torrserver_auth`, `torrserver_login`, `torrserver_password` | must sync (login/password sealed) |
| `user_optional` (new) | `file_view`, `online_view`, `online_watched_last`, `torrents_view`, `search_history`, `online_last_balanser`, `user_clarifys` | best-effort |
| moved out | `file_view_*` → `external` | CUB-profile progress; never synced (§10.3) |

How to add a key later:
1. Move the key in `svtlv/storage-scope.json` into `user` (must sync) or `user_optional`
   (best-effort). Prefix rules like `foo_*` also work.
2. If it is a secret, add it to `SensitiveSettings` in `backend/pkg/storage/crypto.go`
   so it is sealed (`TestSensitiveSettings_matchUI` points this out for settings inputs).
3. Run the tests, commit, deploy `lampa-web`. No add-on or other backend change.

### Document (schema version 2)

```json
{"schema_version": 2, "data": {
  "language": "ru",
  "torrserver_url": "http://ts:8090",
  "torrserver_password": "secret",
  "favorite": "{\"book\":[...],\"history\":[...]}",
  "file_view": "{...}"}}
```

Backend rules (no key names except `SensitiveSettings`):
- `data` is an object of string values; keys match `^[A-Za-z0-9_-]{1,128}$`.
- Each value ≤ `MaxValueBytes` (1 MiB, replaces `MaxSectionBytes`); body ≤ `MaxBodyBytes`
  (raised to 4 MiB in `appsettings.json`); depth checks stay.
- `SensitiveSettings` keys are taken from the top level of `data` and sealed as today.
- A stored row with `schema_version` 1 is treated as no document (`404`), so the first
  Plan 4 login uploads fresh. There is no migration, because no real client has written
  version 1 documents.

Size rule in the add-on: a `user_optional` value over 1 MiB is left out; a must-sync value
over 1 MiB blocks the upload with a Noty. If the body is over 4 MiB, `user_optional` keys
are left out largest first until it fits; if must-sync keys alone do not fit, Noty and no
upload. Measure UTF-8 bytes inside try/catch; a failure (lone surrogate) means skip + Noty.

### Poll, live apply and save

The add-on keeps the last server document and its ETag in memory (`lastDoc`, `lastEtag`).

```
poll (one at a time; skipped while the page is hidden)
GET user-data  If-None-Match: lastEtag
   304 → nothing to do
   200 → lastDoc, lastEtag = response
         for each synced key: server ≠ local and key not dirty →
              applying = true
              Lampa.Storage.set(key, raw starts with [ or { ? JSON.parse(raw) : raw)
              applying = false
         'favorite' written → Lampa.Favorite.read()
```
**Note:** the add-on ignores `change` events while `applying` is set, so applied values are
not uploaded again. `language`/`tmdb_lang` are applied too, but the UI language changes
only on the next start; that is accepted.

```
save (2s after the last change; one at a time; changes during a save → one more)
poll ─┬─ 304/200 → base = lastDoc
      └─ 404     → base = full local export
overlay dirty keys (local string; skip if missing) → size rule
PUT (Content-Type json, X-Lampa-Csrf: 1, X-Lampa-Expected-User: U)
   204 → lastDoc = sent doc, lastEtag = response ETag, clear the dirty keys sent
```
**Note:** saving starts with a poll, so other devices' changes are applied first and the
PUT only adds this page's own changes. The PUT response carries the new ETag, so the next
poll does not download the page's own write.

A TorrServer URL changed on another device during playback is applied at the next poll:
settings, My Torrents and the ownership add-on switch to the new server, while the
current video keeps streaming from the URL it started with. A playlist item built earlier
may still point at the old server; that is accepted.

### Errors (GET or PUT)

| Case | Behavior |
|---|---|
| network / 5xx | keep local and dirty keys, Noty "Not saved to Account" once per failure streak, retry on next change or poll |
| 401 / 409 | stop uploads and polling, drop timers, send `svtlv_account {type: 'expired'}` so account.js rechecks |
| 400 / 403 / 413 / 415 | Noty, keep local, no retry loop |
| boot GET or whitelist fetch did not succeed | boot with local; one Noty after boot; post-boot retry when `signed_in` |
| whitelist missing or invalid after boot | sync off for this page, Lampa works locally |
| pre-boot `setItem` fails (quota) | stop apply, uploads off for this page, Noty |

Unsaved changes are lost when the session ends (401 or logout), including a change still
inside the 2s debounce. This is accepted.

Strings: inline `{en, ru}` object like `account.js` (no `lang/*` keys). Public surface:
`window.svtlv_sync_boot(done)` (calls `done` exactly once) and `window.svtlv_sync_loaded`.
Network calls use `$.ajax` with an explicit `timeout`. ES5 only. Nothing is defined on
non-HTTP(S) origins (`^https?:$` guard).

## What Goes Where
- **Implementation Steps**: backend document/precondition/ETag, whitelist, add-on, seam,
  account.js changes, tests, CI, docs.
- **Post-Completion**: deploy, two-device browser/TV checks, rollback check.

## Implementation Steps

### Task 1: Flat user-data document (schema version 2)

**Files:**
- Modify: `backend/pkg/storage/document.go`, `document_test.go`
- Modify: `backend/pkg/storage/crypto.go`, `crypto_test.go`
- Modify: `backend/pkg/storage/service.go`, `service_test.go`
- Modify: `backend/pkg/api/userdata.go`, `userdata_test.go`
- Modify: `backend/pkg/config/defaults/appsettings.json`, `backend/pkg/config/*.go` (limit rename), `config_test.go`

- [ ] `ParseDocument`: `schema_version` must be 2; `data` is an object of string values with keys matching `^[A-Za-z0-9_-]{1,128}$`; each value ≤ `MaxValueBytes`; remove `Sections` and the section filling
- [ ] rename `MaxSectionBytes` → `MaxValueBytes` (1 MiB); raise `Api.MaxBodyBytes` to 4194304 in `appsettings.json`
- [ ] sealing: take `SensitiveSettings` keys from the top level of `data` instead of `settings`; reading merges them back as before
- [ ] `Service.Get`: a stored row with `schema_version` ≠ 2 answers "not found"
- [ ] `userdata.go`: drop `orderedData`; encode `data` with sorted keys so equal documents give equal bytes
- [ ] write tests: valid flat document; rejected non-string value, bad key, oversize value, oversize body, version 1; sealed keys never appear in JSONB and come back on read; a version-1 row reads as 404
- [ ] run `make fmt test race lint` in `backend/` - must pass before task 2

### Task 2: Expected-user precondition and ETag

**Files:**
- Modify: `backend/pkg/api/torrents.go` (move helper), `identity.go`
- Modify: `backend/pkg/api/userdata.go`, `userdata_test.go`

- [ ] move `torrentIdentityMatches` to `identity.go` as `identityMatches`; torrent routes keep using it
- [ ] call `identityMatches` in GET, PUT and DELETE `/user-data`, before storage access
- [ ] GET: `ETag: "<sha256 hex of the response body>"`, `304` with no body when `If-None-Match` matches, `Cache-Control: no-cache`
- [ ] PUT: return the new document's ETag in the `204`, equal to what the next GET returns
- [ ] write tests: precondition absent/equal passes, different → `409 account_changed` with no storage call (all three methods)
- [ ] write tests: ETag stable for the same document and changed after a PUT; matching `If-None-Match` → `304`, other/missing → `200`; PUT ETag equals the next GET ETag
- [ ] run `make fmt test race lint` in `backend/` - must pass before task 3

### Task 3: Whitelist in storage-scope.json

**Files:**
- Modify: `svtlv/storage-scope.json`
- Modify: `backend/pkg/storage/crypto_test.go` (scope names, Plan 4 assertions)
- Modify: `docs/tests/ownership-docs.test.cjs`

- [ ] ⚠️ get the user's final key lists (must sync vs best-effort) and apply them
- [ ] add the `user_optional` scope; move best-effort keys into it; move `file_view_*` to `external` (unless the user decides otherwise); update `_policy` and the key notes
- [ ] guard: add `user_optional` to `storageScopeNames`; sealed keys may live only in `user_connection` or `connection`; update the Plan 4 assertions (~596–612)
- [ ] update `ownership-docs.test.cjs` for the new scope
- [ ] run `make test` in `backend/` and `node --test docs/tests/*.test.cjs` - must pass before task 4

### Task 4: Add-on core — whitelist, export, apply, size rule

**Files:**
- Create: `svtlv/account-sync.js`
- Create: `docs/tests/account-sync.test.cjs`
- Modify: `.github/workflows/tests.yaml`

- [ ] create `svtlv/account-sync.js` IIFE with the protocol guard, inline `{en, ru}` strings and a `$.ajax` helper (JSON, timeout, `Content-Type` on PUT)
- [ ] `loadScope()` fetches `svtlv/storage-scope.json?v=<cache_version>` and validates it; `classify(key)` uses exact → longest prefix precedence; `synced(key)` is true for `user`, `user_connection`, `user_optional`
- [ ] `exportLocal()` walks `localStorage` keys, keeps synced ones (raw strings, skip null); `overlay(base, dirtyKeys)`; the size rule with `user_optional` dropped largest first
- [ ] `applyDoc(data)` (pre-boot, raw `setItem`): only synced keys with string values; returns `{changed, missingOnServer}`; `setItem` failure throws; `clearSynced()` removes synced keys and the marker
- [ ] wrap every storage access in try/catch; failures turn sync off for the page and never block boot
- [ ] write tests: `classify` matches the Go guard on the real JSON (exact, prefix, longest prefix, unknown → local); export/apply round trip exact; apply ignores non-synced keys and non-string values; `changed`/`missingOnServer` correct
- [ ] write tests: overlay keeps server values for non-dirty keys and for dirty keys missing locally; oversize optional dropped largest first; oversize must-sync blocks; UTF-8 measurement and lone-surrogate failure; invalid scope JSON disables sync
- [ ] add `node --test docs/tests/account-sync.test.cjs` to `tests.yaml`; confirm `acorn --ecma5` passes
- [ ] run node tests - must pass before task 5

### Task 5: Pre-boot apply and loader seam

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `index.html` (inside `svtlv:begin/end` only)
- Modify: `docs/tests/account-sync.test.cjs`
- Modify: `docs/tests/torrserver-ownership.test.cjs` (seam assertion ~821)

- [ ] implement `window.svtlv_sync_boot(done)` per the boot diagram: arm the 3s timer first; no marker → `done` with no request; scope and user-data fetched in parallel; `done` exactly once; late responses ignored
- [ ] handle 200 (apply, ready), 404 (ready, all local keys dirty), 401/409 (clear, not ready), other or no scope (not ready, remember for the after-boot Noty), apply failure (uploads off, Noty)
- [ ] seam: ownership script → `account-sync.js` → start; load callback `try { svtlv_sync_boot ? svtlv_sync_boot(svtlvStart) : svtlvStart() } catch (e) { svtlvStart() }`; error callback `svtlvStart`
- [ ] write tests for every boot branch, timeout, late response, `done` once, a throwing boot still starting Lampa, scope fetch failure
- [ ] update the seam test for the new order; a missing add-on file still starts Lampa
- [ ] run node tests - must pass before task 6

### Task 6: Post-boot account handling

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `svtlv/account.js`
- Modify: `docs/tests/account-sync.test.cjs`
- Modify: `docs/tests/account-state.test.cjs`

- [ ] poll for `window.Lampa` like `account.js` (never `defineProperty`); read `svtlv_account_state` and follow `svtlv_account` state events
- [ ] `signed_in(U)` per the post-boot diagram: clear when marker ≠ U; GET; apply; marker before any reload; reload only if `changed`; on 404 set marker and save all local keys; mark `missingOnServer` keys dirty
- [ ] `guest` with marker set → clear + reload unless `window.svtlv_account_leaving`; `checking`/`unavailable` change nothing
- [ ] `$(window).on('storage')` for `svtlv_sync_user` changes → reload
- [ ] `account.js`: set `window.svtlv_account_leaving = true` before `applySession` when `logOutDone` will navigate to `logout_url`; change the "stays on this device" text (en and ru) to say data syncs to the Account
- [ ] write tests: TV login with 200 (marker before reload, reload only when changed, no reload when equal → uploads on), with 404 (save local, no reload), another user's marker cleared first, first login with 200 clears guest keys
- [ ] write tests: logout clears and reloads; SSO logout clears without reload; `guest` at startup with a marker clears; anonymous start sends no request; storage event reloads; `account-state` test covers the leaving flag
- [ ] run node tests - must pass before task 7

### Task 7: Save on change

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `docs/tests/account-sync.test.cjs`

- [ ] follow `Lampa.Storage.listener` `change`; mark synced keys dirty only while uploads are on and `applying` is false
- [ ] 2s debounce; save flow per the diagram; one save at a time; changes during a save schedule exactly one more
- [ ] error handling per the Errors table, including `expired` on 401/409 and one Noty per failure streak
- [ ] write tests: several changes → one poll + one PUT; non-synced key → nothing; PUT body = server doc + dirty keys only; headers and `Content-Type`; change during a save → one follow-up
- [ ] write tests: 404 before PUT uses the full local export; 401/409 stop uploads and send `expired`; 5xx one Noty then retry; 403/415 Noty without retry; no saves before ready or after logout
- [ ] run node tests - must pass before task 8

### Task 8: Live refresh by polling

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `docs/tests/account-sync.test.cjs`

- [ ] implement `poll()` per the poll diagram with `lastDoc`/`lastEtag`; one request at a time; the save flow starts with `poll()`
- [ ] triggers: `visibilitychange` to visible, a 60s timer only while visible and uploads are on, the start of each save; stop on logout or 401/409
- [ ] live apply through `Lampa.Storage.set` with the `[`/`{` JSON rule and the `applying` guard; `Lampa.Favorite.read()` after `favorite`; skip dirty keys; no special case for playback
- [ ] write tests: 304 changes nothing; 200 writes only differing non-dirty synced keys and calls `Favorite.read` only when `favorite` changed; applied keys do not trigger a PUT; dirty keys win on the next save
- [ ] write tests: hidden page does not poll; becoming visible polls once; PUT ETag prevents downloading the page's own write; booleans/numbers/JSON round-trip through `Storage.get` like a fresh read
- [ ] run node tests - must pass before task 9

### Task 9: Verify acceptance criteria
- [ ] anonymous (no marker): no `/user-data` request at all
- [ ] first login (browser redirect or TV device flow): server data applied with at most one reload; guest data uploaded only when the server has no document
- [ ] later starts: server values used without reload
- [ ] change while signed in: one poll + PUT after debounce; keys changed on another device are not rolled back
- [ ] a change on another device shows up on a running page within about 60s (or on screen return) without a reload, also during playback
- [ ] adding a key only to `storage-scope.json` makes it sync with no JS or Go change (try it in a test with a temporary scope)
- [ ] logout or expired session: synced keys gone, device keys (`player`, `jackett_*`, `plugins`, …) untouched
- [ ] run full suite: `node --test docs/tests/*.test.cjs backend/pkg/storage/testdata/torrserver-one-link.test.cjs`, `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js`, and `make test race lint` in `backend/`
- [ ] confirm `git diff` does not touch `app.min.js`, `css/app.css`, `lang/*`, `assembly.json`

### Task 10: [Final] Update documentation
- [ ] `docs/settings-sync-backend-design.md`: §4 flat document v2 and limits; §6/§7 boot apply, ETag polling, live apply (also during playback) and "fresh server copy + dirty keys"; §8 precondition, `ETag`/`304`, PUT ETag and the v2 example; §5.3.1 one Keycloak call per poll; §10 lists `account-sync.js` (no `account-sync.css`) and how it reads the whitelist; §10.3 adds `user_optional` and "how to add a key"; §16 resolved (clear on logout); Status → Implemented
- [ ] §14 Plan 4 deviations: flat document instead of sections; no feature flag; `file_view_*` not synced; §6.3 read-back GET dropped; key-level last-wins; CUB writers not coordinated (`favorite`/`file_view` unused while CUB sync is active); "Clear cache" and CUB backup restore undone by the next boot GET; unsaved changes lost on 401/logout; sign-in/out notices disappear in the reload
- [ ] keep `docs/tests/ownership-docs.test.cjs` passing after doc edits
- [ ] `CLAUDE.md`: Svtlv add-ons section gets `account-sync.js`, the seam order, the marker key, and "to sync a key, add it to `user`/`user_optional` in `storage-scope.json`"; lampa-api section notes the flat v2 document
- [ ] `backend/README.md`: document v2 shape, precondition and ETag
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Deploy**: run `deploy-docker.yaml` (Production) from `svtlvtv`. No new secrets or Compose
changes; the 4 MiB body limit ships in the embedded config. Check that `svtlv/account-sync.js`
and `svtlv/storage-scope.json` are served by `lampa-web`. Before deploying, confirm the
production `lampa_user_data` table has no documents worth keeping (version-1 rows read as
absent).

**Manual verification** (two devices, different player and Jackett settings):
- browser A signs in and adds a bookmark, a reaction, a language and the TorrServer URL and
  login; TV B signs in with the device flow, reloads once and shows the same values,
  while B keeps its own player and Jackett settings
- leave B playing a video; add a bookmark on A; B's progress saves must not remove it
- with B idle on the bookmarks screen, add a bookmark on A: it appears on B within about
  60s without a reload; switching B's app away and back shows it immediately
- B plays a torrent; change the TorrServer URL on A: within about 60s B's settings show the
  new URL while its video keeps playing; My Torrents then shows the new server
- add one more key to `user` in `storage-scope.json`, deploy, and check it syncs
- first login on a device with guest data and no server document uploads that data;
  with an existing server document the guest data is cleared instead
- logout on the TV clears bookmarks; browser SSO logout returns to a clean guest page;
  a second browser tab reloads when the first tab logs out
- stop `lampa-api`: Lampa boots within ~3s with local data and shows the sync Noty;
  restart the API and the next change saves
- old TV WebViews (both `lampa-app/LAMPA` engines): boot works and no ES5 errors appear

**Rollback**: revert the Plan 4 commits and redeploy. Server documents stay in
PostgreSQL. Devices keep their last local values.
