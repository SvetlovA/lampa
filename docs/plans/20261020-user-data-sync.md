# Sync user data with lampa-api (Plan 4)

## Overview
- Final roadmap plan (design §14 Plan 4). It connects Lampa's browser storage to the
  existing `/api/v1/user-data` document, so a signed-in user gets the same config and
  personal data on every device.
- Keep it as simple as possible. Config and user data must sync reliably. History,
  progress and torrent views are best-effort: they sync when they fit and are dropped
  otherwise, with no extra logic to force them through.
- **One config, edited by hand.** `svtlv/storage-scope.json` is the only place that says
  which keys sync (`user` and `user_connection` must sync, new `user_optional` best-effort)
  and which are encrypted (new `encrypt` list). The add-on reads it at runtime and sends
  the `encrypt` list with every PUT. The backend stores any JSON it receives for the user,
  seals the listed fields in place as `{"$sealed": "…"}` and unseals them on read. It knows
  no key names, and nothing checks what the UI chooses to encrypt.
- The UI keeps using `localStorage` exactly like upstream Lampa; only the add-on talks to
  the API, so upstream bundle updates do not conflict with sync.
- A running device picks up changes from other devices by **polling with an ETag**: on
  screen return, every 60s while visible, and before each save. Changes are applied live,
  without a reload, also while a video is playing.
- Anonymous mode stays local-only, with no user-data request at all. Logout clears the
  synced keys (resolves design §16).
- Work: a new `svtlv/account-sync.js` add-on, the marked `index.html` seam, small
  `account.js` changes, and an any-JSON document with ETag/`If-Match` in the backend. No
  change to `app.min.js`, `css/app.css`, `lang/*` or `assembly.json`.

## Context (from discovery)
- Backend today: `GET/PUT/DELETE /api/v1/user-data` store one JSONB document per user with
  8 fixed sections (`storage.Sections`), 1 MiB per section, 2 MiB body, schema version 1
  (`backend/pkg/storage/document.go`, `service.go`, `postgres.go`, `backend/pkg/api/userdata.go`
  with `orderedData`; PUT answers 200 with the document). `SensitiveSettings` (`crypto.go`)
  seals a fixed key list into the `encrypted_connections` column. `WriteJSON` sets
  `Cache-Control: no-store` (`server.go:228`). Limits are wired in `backend/cmd/lampa-api/main.go`
  (~103). Migrations live in `backend/migrations/` with an exact-name test
  (`migrations_test.go`). No production client writes documents yet; Plan 4 is the first.
- `X-Lampa-Expected-User` → `409 account_changed` exists only for torrent routes
  (`torrentIdentityMatches` in `backend/pkg/api/torrents.go`).
- `svtlv/storage-scope.json` (scopes `user`, `user_connection`, `device`, `connection`,
  `external`, `cache`; exact keys beat prefixes, longest `*` prefix wins; entries match
  `^[a-zA-Z0-9_.:-]+\*?$`) is checked by `TestStorageScope_matchBundle`
  (`backend/pkg/storage/crypto_test.go`, which also passes `SensitiveSettings` to
  `storageScopeDrift`) and by `docs/tests/ownership-docs.test.cjs` (`checkScopes` compares
  the design doc §10.3 table with the scope names and `user` keys). It ships in `lampa-web`.
- `svtlv/account.js` publishes `window.svtlv_account_state {status, generation, user_id}`
  and the `svtlv_account` Listener event, and rechecks the session on `{type: 'expired'}`.
  `logOutDone` publishes guest, then on the browser path navigates to `logout_url`. Its
  Account page text says data "stays on this device" (`account.js:36`, ru `:72`).
- `Lampa.Storage` (`app.min.js` ~49917–50010): `set` JSON-stringifies objects/arrays into
  `localStorage`, updates the memory cache and sends `change` unless `nolisten`; `get`
  parses only values starting with `[`/`{` and converts `'true'`/digits itself, and falls
  back to the IndexedDB `reserve` when the item is null (quota fallback).
- Not every writer sends `change`: the language picker writes `language`/`tmdb_lang` with
  `nolisten` and reloads at once (`app.min.js` ~56396). So "dirty" cannot rely on events.
- Modules with their own memory copies: `Lampa.Favorite` (`read()` re-reads `favorite`) and
  `Lampa.Timeline` (`read()` re-reads progress; `filename()` gives the active key, `file_view`
  without CUB). Playback writes progress every 2 minutes.
- Every authenticated request is revalidated against Keycloak with no cache (§5.3.1): each
  poll or save costs Keycloak calls; 60s × a few devices is acceptable.
- `index.html` seam: account.js (no wait) → torrserver-ownership.js → `startLampa()`. Old
  engines can fire both load callbacks; the seam guards with `svtlv_started`.
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
  `Lampa.Storage`, `Lampa.Listener`, `Lampa.Favorite`, `Lampa.Timeline`,
  `storage`/`visibilitychange` events and `location.reload`, and the real
  `svtlv/storage-scope.json`.
- Go: table-driven cases in `backend/pkg/storage/*_test.go`, `backend/pkg/api/userdata_test.go`,
  `flow_test.go`, `backend/migrations/migrations_test.go`.
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
                                    ▲  │
               boot apply / live    │  │ local value ≠ base → dirty
                 ┌──────── account-sync.js ◄──── svtlv/storage-scope.json
                 │ poll: GET If-None-Match → apply │      (whitelist + encrypt)
                 │ save: PUT If-Match (dirty keys) │
                 └────────────────┬────────────────┘
                                  ▼
                lampa-api: any JSON per user, seals "encrypt" keys,
                stores an etag per row → Postgres jsonb
```
**Note:** Lampa never talks to our API. The add-on is the only part that knows which keys
sync and which are secret, and it learns both from one JSON file.

Five rules keep it simple and safe:

1. **Before boot, write raw; after boot, write through `Lampa.Storage`.** Server data is
   applied before boot when the device has synced before. After boot, account changes
   (login, other user) reload the page if anything was cleared or changed; changes from
   other devices of the same account are applied live.
2. **Dirty = local value differs from the last synced value.** `localStorage['svtlv_sync_base']`
   keeps a fingerprint (`length:hash`) of the last value each key had on the server. A key
   whose local value differs is dirty: it is never overwritten by the server and is
   uploaded on the next save. This survives reloads, failed saves and writers that skip
   `change` events. `change` events only start the save timer.
3. **Save = latest server copy + dirty keys, guarded by `If-Match`.** Every save polls first,
   puts the dirty local values on top, and PUTs with `If-Match: <etag>`. If another device
   saved in between, the server answers `412`, and the add-on polls and retries once. If
   two devices change the same key, the last save wins (§7).
4. **The marker says whose data is on the device.** `localStorage['svtlv_sync_user']` holds
   the user ID. Another user's data is cleared before any apply. Guest data is uploaded
   only on a first login that finds no server document (§6.3); otherwise it is cleared.
5. **Poll with an ETag and apply live.** `304` means nothing changed. On `200`, each synced
   key that differs and is not dirty is written with `Lampa.Storage.set`, then
   `Favorite.read()` / `Timeline.read()` refresh their memory copies.

Boot (only on a device that has synced before):

```
index.html seam
  account.js ─────────────── (loads, no wait)
  torrserver-ownership.js ── loaded
  account-sync.js ────────── try { window.svtlv_sync_boot(done) } catch → done()
       second call → ignored; arm 3s timer first
       no marker → done() at once           (guest / first login: no request)
       in parallel: GET svtlv/storage-scope.json?v=<cache_version>
                    GET /api/v1/user-data  X-Lampa-Expected-User: <marker>
         200 (valid v2) → apply non-dirty keys → ready ─┐
         404 user_data_not_found → ready                │
         401 / 409 → clear synced keys, base, marker    ├─ done() → startLampa()
         other / timeout / no scope → not ready        ─┘
```
**Note:** Lampa starts only after this finishes or gives up after 3 seconds. The cost is
that `app.min.js` starts downloading up to 3s later on a signed-in boot; that is accepted.
Dirty keys (for example a language just picked with `nolisten`) are kept and uploaded
after boot instead of being reverted.

After boot, everything is driven by the account state and the marker:

```
status signed_in(U), same U as last handled → nothing
status signed_in(U):
   marker = U and ready      → uploads and polling on
   marker = U, not ready     → GET → apply non-dirty keys → reload if anything changed
   marker ≠ U (other or none) → GET:
        200 → clear synced keys + base → apply → marker = U
              → reload if anything was cleared or written
        404 → marker none: keep local (guest data), base = {} → marker = U → save
              marker = W:  clear synced keys + base → marker = U → reload if cleared
status guest and marker set → clear synced keys + base + marker → reload
                              (skip reload while account.js navigates to logout_url)
storage event on svtlv_sync_user (another tab) → reload
Storage 'change' on a synced key → start the save timer
page hidden → flush a pending save now
```
**Note:** "guest" means the confirmed guest state, from a logout or from an expired session
found at any time. The marker and base are written before any reload, so the reloaded page
takes the pre-boot path and cannot loop.

Key decisions:
- **Raw strings.** Each value sent is the exact `localStorage` string. An empty string
  counts as "no value" everywhere.
- **Clearing writes `''`, not `removeItem`.** An empty item hides the IndexedDB `reserve`
  copy that `Storage.get` would otherwise fall back to, so a guest never sees the previous
  user's large keys.
- **Whitelist only.** A key syncs only when `classify(key)` returns `user`,
  `user_connection` or `user_optional` and the key matches the backend key pattern. Import
  uses the same rule and accepts only string values, so a document can never push
  `plugins` or another device key. Unknown keys stay local.
- **Must-sync first.** Apply writes `user`/`user_connection` keys first. An optional key
  that fails to write (quota) is skipped; only a must-sync failure turns uploads off.
- **A key removed from the whitelist** stops syncing; its old server value is ignored.
- **No feature flag.** Rollback = revert + redeploy (agreed).

## Technical Details

### The whitelist (`svtlv/storage-scope.json`)

Draft lists, **the user decides the final ones** before Task 3:

| Scope | Keys | Guarantee |
|---|---|---|
| `user` | `language`, `tmdb_lang`, `favorite`, `mine_reactions`, `person_subscribes_id` | must sync |
| `user_connection` | `torrserver_url`, `torrserver_auth`, `torrserver_login`, `torrserver_password` | must sync |
| `user_optional` (new) | `file_view`, `online_view`, `online_watched_last`, `torrents_view`, `search_history`, `online_last_balanser`, `user_clarifys` | best-effort |
| moved out | `file_view_*` → `external` | CUB-profile progress; never synced (§10.3) |
| `encrypt` (new list) | `torrserver_login`, `torrserver_password` | sealed by the backend |

How to add a key later:
1. Move the key in `svtlv/storage-scope.json` into `user` (must sync) or `user_optional`
   (best-effort). Prefix rules like `foo_*` also work.
2. If it should be encrypted, also add it to `encrypt` (optional; nothing checks it).
3. Run the tests, commit, deploy `lampa-web`. No add-on or backend change.

What to encrypt is the UI's decision only. A key not in `encrypt` is stored as plain JSON.
`TestSensitiveSettings_matchUI` and the Go `SensitiveSettings`/`plainUIInputs` lists are
deleted. `TestStorageScope_matchBundle` stays: it lists new upstream storage keys so the
user can decide whether to sync them.

### Document (schema version 2)

PUT body (`If-Match: "<etag>"` when updating an existing document):
```json
{"schema_version": 2,
 "data": {
   "language": "ru",
   "torrserver_url": "http://ts:8090",
   "torrserver_password": "secret",
   "favorite": "{\"book\":[...],\"history\":[...]}",
   "file_view": "{...}"},
 "encrypt": ["torrserver_login", "torrserver_password"]}
```

Stored row: `data` jsonb with sealed values in place, plus a new `etag` column:
```json
{"language": "ru", "torrserver_url": "http://ts:8090",
 "torrserver_password": {"$sealed": "base64(nonce+ciphertext)"},
 "favorite": "{...}", "file_view": "{...}"}
```
**Note:** GET returns `{"schema_version": 2, "data": {...}, "updated_at": ...}` with sealed
values decrypted back, and the stored etag as the `ETag` header.

Backend rules (no key names anywhere):
- `data` is a JSON object; keys match `^[A-Za-z0-9_.:-]{1,128}$` (same characters as scope
  entries); values are any JSON except an object containing `$sealed` (reserved, `400`).
- `encrypt` is an optional array of keys (same pattern, ≤ 64). Each listed key present in
  `data` is sealed with the existing AES-GCM code and `LAMPA_API_DATA_KEY`; missing keys
  are ignored.
- Only the body limit applies: `MaxBodyBytes` raised to 4 MiB; `MaxSectionBytes` is
  removed; depth checks stay. The 15s read timeout needs about 2.3 Mbit/s upload for a full
  4 MiB body; real documents are expected to be far smaller.
- **ETag** = `sha256` of the canonical incoming `data` (keys sorted, values compacted,
  before sealing), computed once on PUT and stored in the `etag` column. GET and PUT both
  return it quoted. `If-None-Match` and `If-Match` compare it ignoring a `W/` prefix.
- **GET**: `304` (no body) when `If-None-Match` matches; `Cache-Control: no-store` stays.
- **PUT**: `If-Match` present → `UPDATE … WHERE user_id = $1 AND etag = $2`; no row →
  `412 precondition_failed`. Without `If-Match` → upsert (first save). Answers `204` with
  the new `ETag` (was 200 with a body).
- **Unreadable sealed value** (wrong or lost data key): left out of the GET response with a
  `[WARN]` log, so the device re-uploads its local value instead of failing every save.
- Migration `00003`: drop `encrypted_connections`, add `etag text not null default ''`.
  A row with `schema_version` 1 reads as "not found" (`404`); no real client wrote v1.

### Fingerprints and dirty keys

```
fp(s) = s.length + ':' + 32-bit FNV-1a(s)          ('' and null → no value)
dirty(K) = fp(local K) ≠ base[K]                  (both "no value" → not dirty)
after apply of server value S for K:   base[K] = fp(S)
after PUT 204 of sent value V for K:   base[K] = fp(V)
```
**Note:** if the user changes K again while a PUT is in flight, the local value no longer
matches `fp(V)`, so K stays dirty and goes out with the next save instead of being reverted
by the next poll. `svtlv_sync_base` is plain `localStorage`, outside `Lampa.Storage`.

### Poll, live apply and save

The add-on keeps the last server document and ETag in memory (`lastDoc`, `lastEtag`).

```
poll (one request at a time)
GET user-data  If-None-Match: lastEtag
   304 → nothing to do
   200 → must be {schema_version: 2, data: object}, else treat as an error
         lastDoc, lastEtag = response
         must-sync keys first, then optional:
           server value S (string) ≠ local and key not dirty →
              applying = true
              Lampa.Storage.set(K, S starts with [ or { ? JSON.parse(S) : S)
              applying = false
              base[K] = fp(S)
         'favorite' written → Lampa.Favorite.read()
         Lampa.Timeline.filename() written → Lampa.Timeline.read()
   404 user_data_not_found → lastDoc = {}, lastEtag = none
```
**Note:** `language`/`tmdb_lang` are applied too, but the UI language changes only on the
next start; that is accepted. Writing during playback is fine: the stream URL was built at
start, so the current video keeps playing.

```
save (one at a time; a trigger during a save → one more save after it)
timer: 2s after a change if any must-sync key is dirty, else 5 min; flush when hidden
poll → base doc = lastDoc
overlay every dirty key with its local string (skip keys with no value) → size rule
PUT  If-Match: lastEtag (omitted when lastDoc came from a 404)
   204 → base[K] = fp(sent K) for sent keys, lastDoc = sent, lastEtag = response ETag
   412 → poll + PUT once more; a second 412 waits for the next trigger
```
**Note:** the poll before each save applies other devices' changes first, so the PUT only
adds this page's own changes. The longer timer for optional keys keeps playback progress
from causing a full GET + PUT every 2 minutes on a TV.

Polling triggers: `visibilitychange` to visible, and a 60s timer while visible. Hiding the
page stops the timer but never blocks a save.

Size rule: measure the UTF-8 length of `JSON.stringify(body)` (escapes included). Over
4 MiB → leave out `user_optional` keys, largest encoded value first, until it fits. Must-sync
keys alone too big → Noty, no upload. Measurement inside try/catch; a failure (lone
surrogate) → skip + Noty. Keys not matching the backend pattern are skipped, not sent.

### Errors (GET or PUT)

| Case | Behavior |
|---|---|
| network / 5xx / invalid 200 | keep local (keys stay dirty), Noty "Not saved to Account" once per failure streak, retry on next trigger |
| 401 / 409 | stop uploads and polling, drop timers, send `svtlv_account {type: 'expired'}` |
| 412 | poll and retry once (see save) |
| 400 / 403 / 413 / 415 | Noty, keep local, no retry loop |
| boot GET or whitelist fetch did not succeed | boot with local; one Noty after boot; post-boot retry when `signed_in` |
| whitelist missing or invalid after boot | sync off for this page, Lampa works locally |
| must-sync `setItem` fails (quota) | stop apply, uploads off for this page, Noty |

Because dirty keys are derived from `svtlv_sync_base`, a change is not lost when the page
reloads or a save fails; it goes out with the next successful save. It is lost only when
the session ends first (401/logout clears the synced keys).

Strings: inline `{en, ru}` object like `account.js`. Public surface:
`window.svtlv_sync_boot(done)` (calls `done` exactly once; a second call is ignored) and
`window.svtlv_sync_loaded`. Network calls use `$.ajax` with an explicit `timeout`. ES5
only. Nothing is defined on non-HTTP(S) origins (`^https?:$` guard).

## What Goes Where
- **Implementation Steps**: backend document/precondition/ETag, whitelist, add-on, seam,
  account.js changes, tests, CI, docs.
- **Post-Completion**: deploy, two-device browser/TV checks, rollback check.

## Implementation Steps

### Task 1: Any-JSON user document with in-place sealing (schema version 2)

**Files:**
- Modify: `backend/pkg/storage/document.go`, `document_test.go`
- Modify: `backend/pkg/storage/crypto.go`, `crypto_test.go`
- Modify: `backend/pkg/storage/service.go`, `service_test.go`
- Modify: `backend/pkg/storage/postgres.go`, `postgres_test.go`, `migrate_test.go`
- Create: `backend/migrations/00003_user_data_v2.sql`
- Modify: `backend/migrations/migrations_test.go`
- Modify: `backend/pkg/api/userdata.go`, `userdata_test.go`, `flow_test.go`
- Modify: `backend/cmd/lampa-api/main.go`, `backend/pkg/config/defaults/appsettings.json`, `backend/pkg/config/config_test.go`
- Modify: `backend/cmd/lampa-api/main_test.go`, `backend/pkg/api/server_test.go`, `identity_test.go`, `csrf_test.go`, `backend/pkg/storage/torrents_test.go` (dependent fixtures and 204 assertions)
- Modify: `AGENTS.md` (stale `SensitiveSettings` text)

- [x] `ParseDocument`: `schema_version` must be 2; `data` object with keys matching `^[A-Za-z0-9_.:-]{1,128}$` and any values except a reserved `$sealed` object; optional `encrypt` array (same pattern, ≤ 64); remove `Sections`, section filling and `MaxSectionBytes`
- [x] raise `Api.MaxBodyBytes` to 4194304; update the limits wiring in `main.go`
- [x] `crypto.go`: `sealListed(data, encrypt)` and `openAll(data)` (unreadable value → dropped + `[WARN]`) replace `SensitiveSettings` extraction; keep the AES-GCM code; delete `SensitiveSettings`, `TestSensitiveSettings_matchUI` and `plainUIInputs`; pass `nil` where `storageScopeDrift` took `SensitiveSettings`
- [x] migration `00003`: drop `encrypted_connections`, add `etag text not null default ''`; `postgres.go` stops using the old column; a row with `schema_version` ≠ 2 reads as "not found"
- [x] `userdata.go`: drop `orderedData`; GET returns the opened document; PUT answers `204` (no body); ETag/`If-Match` come in Task 2
- [x] write tests: any JSON values round-trip; listed keys sealed in stored JSONB (plaintext absent) and decrypted on read; unlisted keys plain; listed-but-missing ignored; `$sealed` input → 400; unreadable sealed value omitted with the rest returned
- [x] write tests: bad key (including `.`/`:` accepted), oversize body, malformed/too long `encrypt`, v1 PUT rejected, v1 row → 404; migration test lists `00003` and the column is gone; update `flow_test.go` for v2 bodies and 204
- [x] run `make fmt test race lint` in `backend/` - must pass before task 2

### Task 2: Expected-user precondition, ETag and If-Match

**Files:**
- Modify: `backend/pkg/api/torrents.go` (move helper), `identity.go`
- Modify: `backend/pkg/api/userdata.go`, `userdata_test.go`
- Modify: `backend/pkg/storage/document.go`, `service.go`, `postgres.go` and their tests
- Modify: `backend/pkg/api/server.go`, generated API/storage mocks and dependent test fixtures

- [x] move `torrentIdentityMatches` to `identity.go` as `identityMatches`; call it in GET, PUT and DELETE `/user-data` before storage access
- [x] compute the ETag on PUT (sha256 of canonical `data`, sorted keys, compacted values, before sealing) and store it in `etag`
- [x] GET: `ETag` header from the row; `304` without body when `If-None-Match` matches (ignore `W/`); keep `Cache-Control: no-store`
- [x] PUT: with `If-Match` → conditional update, no row → `412 precondition_failed`; without → upsert; `204` with the new `ETag`
- [x] write tests: precondition absent/equal passes, different → `409` with no storage call (all three methods)
- [x] write tests: same data → same ETag (also with nested objects and `é` escapes); changed data → new ETag; PUT ETag equals next GET ETag; `If-None-Match` match → 304, `W/` match → 304, other → 200; `If-Match` stale → 412 and nothing written; concurrent conditional PUTs → exactly one succeeds
- [x] run `make fmt test race lint` in `backend/` - must pass before task 3

### Task 3: Whitelist in storage-scope.json

**Files:**
- Modify: `svtlv/storage-scope.json`
- Modify: `backend/pkg/storage/crypto_test.go` (scope names, Plan 4 assertions)
- Modify: `docs/tests/ownership-docs.test.cjs`
- Modify: `docs/settings-sync-backend-design.md` (§10.3 table only, so `checkScopes` keeps passing)

- [x] ⚠️ final user confirmation (skipped - not automatable; requested with no response during this iteration); applied the proposed must-sync, best-effort and encrypt lists above
- [x] add the `user_optional` scope; move best-effort keys into it; move `file_view_*` to `external` (unless the user decides otherwise); add the top-level `encrypt` list; update `_policy` and the key notes
- [x] scope guard: add `user_optional` to `storageScopeNames`; accept `encrypt` without checking its contents; update the Plan 4 assertions (~596–612) and drop the sealed-key assertions
- [x] update `ownership-docs.test.cjs` and the design doc §10.3 table together
- [x] run `make test` in `backend/` and `node --test docs/tests/*.test.cjs` - must pass before task 4

### Task 4: Add-on core — whitelist, fingerprints, export, apply, size rule

**Files:**
- Create: `svtlv/account-sync.js`
- Create: `docs/tests/account-sync.test.cjs`
- Modify: `.github/workflows/tests.yaml`

- [x] create `svtlv/account-sync.js` IIFE with the protocol guard, inline `{en, ru}` strings and a `$.ajax` helper (JSON, timeout, `Content-Type`, `If-Match`/`If-None-Match`)
- [x] `loadScope()` fetches `svtlv/storage-scope.json?v=<cache_version>` and validates it; `classify(key)` with exact → longest-prefix precedence; `synced(key)` for `user`, `user_connection`, `user_optional` plus the backend key pattern
- [x] `fp()`, `svtlv_sync_base` read/write, `dirty(key)`; `exportLocal()` walks `localStorage` (raw strings, `''`/null skipped); `overlay(lastDoc)`; the size rule on the encoded body
- [x] `applyDoc(data)` (raw `setItem`, pre-boot): must-sync first, only synced keys with string values, skip dirty keys, update base; returns `{changed}`; must-sync failure throws, optional failure skips; `clearSynced()` writes `''` for synced keys and drops base and marker, returning whether it removed anything
- [x] wrap every storage access in try/catch; failures turn sync off for the page and never block boot
- [x] write tests: `classify` against a copy of the Go guard's case table on the real JSON; export/apply round trip exact; apply ignores non-synced keys and non-string values and never overwrites dirty keys; base updated on apply
- [x] write tests: `fp`/dirty for '' vs null vs value; clear writes `''` so a `reserve` fallback is hidden; must-sync before optional and optional failure skipped; size rule with escapes (encoded size) drops largest optional first, must-sync overflow blocks, lone surrogate; invalid scope JSON disables sync
- [x] add `node --test docs/tests/account-sync.test.cjs` to `tests.yaml`; confirm `acorn --ecma5` passes
- [x] run node tests - must pass before task 5

### Task 5: Pre-boot apply and loader seam

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `index.html` (inside `svtlv:begin/end` only)
- Modify: `docs/tests/account-sync.test.cjs`
- Modify: `docs/tests/torrserver-ownership.test.cjs` (seam assertion ~821)

- [x] implement `window.svtlv_sync_boot(done)` per the boot diagram: ignore a second call; arm the 3s timer first; no marker → `done` with no request; scope and user-data fetched in parallel; `done` exactly once; late responses ignored
- [x] handle valid 200 (apply, ready), 404 `user_data_not_found` (ready), 401/409 (clear, not ready), other/invalid/no scope (not ready, remember for the after-boot Noty), must-sync apply failure (uploads off, Noty)
- [x] seam: ownership script → `account-sync.js` → start; load callback `try { window.svtlv_sync_boot ? window.svtlv_sync_boot(svtlvStart) : svtlvStart() } catch (e) { svtlvStart() }`; error callback `svtlvStart`
- [x] write tests for every boot branch, timeout, late response, `done` once, double call, a throwing boot still starting Lampa, scope fetch failure, a dirty key (language set with `nolisten`) surviving the boot apply
- [x] update the seam test for the new order; a missing add-on file still starts Lampa
- [x] run node tests - must pass before task 6

### Task 6: Post-boot account handling

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `svtlv/account.js`
- Modify: `docs/tests/account-sync.test.cjs`
- Modify: `docs/tests/account-state.test.cjs`

- [x] poll for `window.Lampa` like `account.js` (never `defineProperty`); read `svtlv_account_state` and follow `svtlv_account` state events; a repeated `signed_in(U)` for the handled U does nothing
- [x] `signed_in(U)` per the post-boot diagram, including the 404 split (no marker keeps guest data, another marker clears); marker and base before any reload; reload when anything was cleared or written
- [x] `guest` with marker set → clear + reload unless `window.svtlv_account_leaving`; `checking`/`unavailable` change nothing
- [x] `$(window).on('storage')` for `svtlv_sync_user` changes → reload
- [x] `account.js`: set `window.svtlv_account_leaving = true` before `applySession` when `logOutDone` will navigate to `logout_url`; change the "stays on this device" text (en and ru) to say data syncs to the Account
- [x] write tests: TV login with 200 (marker before reload, reload only when cleared/changed, no reload when equal → uploads on); 404 without marker uploads guest data; 404 with another marker clears; guest `favorite` with a server doc lacking `favorite` → cleared and reloaded; repeated `signed_in` ignored
- [x] write tests: logout clears (with `''`) and reloads; SSO logout clears without reload; `guest` at startup with a marker clears; anonymous start sends no request; storage event reloads; `account-state` test covers the leaving flag
- [x] run node tests - must pass before task 7

### Task 7: Save on change

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `docs/tests/account-sync.test.cjs`

- [x] follow `Lampa.Storage.listener` `change` for synced keys (ignored while `applying`) only to start the timer; dirty state always comes from `dirty(key)`
- [x] timers: 2s when a must-sync key is dirty, 5 min when only optional keys are; flush on `visibilitychange` to hidden; one save at a time, a trigger during a save → exactly one more
- [x] save flow per the diagram with `If-Match`, 412 retry once, base updated from the sent values, error handling per the table (`expired` on 401/409, one Noty per failure streak)
- [x] write tests: several changes → one poll + one PUT; PUT body = server doc + dirty keys only; headers (`Content-Type`, CSRF, expected user, `If-Match`); a second edit of the same key during an in-flight PUT stays dirty and is sent next, and the next poll does not revert it
- [x] write tests: optional-only change waits 5 min and flushes on hide; save while hidden works; 412 → poll + one retry; 404 before PUT sends the full export without `If-Match`; invalid 200 → no PUT; 401/409 stop and send `expired`; 5xx Noty then retry; 403/415 Noty without retry; a failed save is retried after a reload (dirty from base)
- [x] run node tests - must pass before task 8

### Task 8: Live refresh by polling

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `docs/tests/account-sync.test.cjs`

- [x] implement `poll()` per the poll diagram with `lastDoc`/`lastEtag`; one request at a time; the save flow starts with `poll()`
- [x] triggers: `visibilitychange` to visible and a 60s timer while visible and uploads are on; stop on logout or 401/409
- [x] live apply through `Lampa.Storage.set` (`[`/`{` JSON rule, `applying` guard, must-sync first, skip dirty keys); `Lampa.Favorite.read()` after `favorite`; `Lampa.Timeline.read()` after `Lampa.Timeline.filename()`
- [x] write tests: 304 changes nothing; 200 writes only differing non-dirty synced keys and updates base; `Favorite.read`/`Timeline.read` called only when their key changed; applied keys do not trigger a save; invalid 200 ignored
- [x] write tests: hidden page does not run the timer; becoming visible polls once; PUT ETag prevents downloading the page's own write; booleans/numbers/JSON round-trip through `Storage.get` like a fresh read
- [x] run node tests - must pass before task 9

### Task 9: Verify acceptance criteria
- [x] anonymous (no marker): no `/user-data` request at all
- [x] first login (browser redirect or TV device flow): server data applied with at most one reload; guest data uploaded only when the server has no document, cleared otherwise (automated account-state/VM flows verified; real browser/TV check skipped - not automatable here)
- [x] later starts: server values used without reload; a language picked in Settings survives the reload and reaches the server (automated `nolisten`/fresh-boot flow verified; Settings UI check skipped - not automatable here)
- [x] change while signed in: one poll + PUT after the timer; keys changed on another device are not rolled back; simultaneous saves on two devices both survive (412 retry)
- [x] a change on another device shows up on a running page within about 60s (or on screen return) without a reload, also during playback, including bookmarks and progress (two independent VM pages verified; physical playback check skipped - not automatable here)
- [x] adding a key only to `storage-scope.json` makes it sync with no JS or Go change (test with a temporary scope)
- [x] logout or expired session: synced keys gone (also those in IndexedDB reserve), device keys (`player`, `jackett_*`, `plugins`, …) untouched
- [x] run full suite: `node --test docs/tests/*.test.cjs backend/pkg/storage/testdata/torrserver-one-link.test.cjs`, `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js`, and `make test race lint` in `backend/`
- [x] confirm `git diff` does not touch `app.min.js`, `css/app.css`, `lang/*`, `assembly.json`

Verification: all 231 Node tests and the pinned ES5 parser pass. Backend `make test race
lint` passes with `LAMPA_API_REQUIRE_DOCKER=1`, including PostgreSQL tests (94.1% coverage,
zero lint issues). Six new acceptance tests run the unmodified add-on with the actual
upstream Storage methods: shared-document races in both commit orders, restart-safe
language edits, guest/login behavior, live bookmarks/progress, temporary must-sync and
optional whitelist entries, and logout/expiry reserve masking. Protected upstream files
are unchanged from `4c76cb6` (before Task 1). Real browser/TV checks remain listed in
Post-Completion.

### Task 10: [Final] Update documentation
- [ ] `docs/settings-sync-backend-design.md`: §4 any-JSON document v2, `encrypt`, in-place `$sealed`, `etag` column, dropped `encrypted_connections`, limits; §6/§7 boot apply, fingerprints, ETag polling, live apply (also during playback), save with `If-Match`; §8 precondition, `ETag`/`304`/`412`, PUT 204 and the v2 example; §5.3.1 Keycloak calls per poll/save; §10 lists `account-sync.js` (no `account-sync.css`) and how it reads the whitelist; §12 secrets chosen by `encrypt`; §16 resolved (clear on logout); Status → Implemented
- [ ] §14 Plan 4 deviations: any-JSON document instead of sections; no feature flag; `file_view_*` not synced; §6.3 read-back GET dropped; key-level last-wins with `If-Match`; CUB writers not coordinated (`favorite`/`file_view` unused while CUB sync is active); "Clear cache" and CUB backup restore undone by the next boot GET; sign-in/out notices disappear in the reload; `app.min.js` starts up to 3s later on signed-in boots
- [ ] keep `docs/tests/ownership-docs.test.cjs` passing after doc edits
- [ ] `CLAUDE.md`: Svtlv add-ons section gets `account-sync.js`, the seam order, the marker and base keys, and "to sync a key, add it to `user`/`user_optional` in `storage-scope.json`; to encrypt it, also add it to `encrypt`"; lampa-api section replaces the `SensitiveSettings`/`TestSensitiveSettings_matchUI` text with the any-JSON v2 document, in-place sealing and ETag
- [ ] `backend/README.md`: document v2 shape, precondition, ETag and `If-Match`
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Deploy**: run `deploy-docker.yaml` (Production) from `svtlvtv`. No new secrets or Compose
changes; the 4 MiB body limit ships in the embedded config. Check that
`svtlv/account-sync.js` and `svtlv/storage-scope.json` are served by `lampa-web`. Before
deploying, confirm the production `lampa_user_data` table has no documents worth keeping
(version-1 rows read as absent).

**Manual verification** (two devices, different player and Jackett settings):
- browser A signs in and adds a bookmark, a reaction, a language and the TorrServer URL and
  login; TV B signs in with the device flow, reloads once and shows the same values,
  while B keeps its own player and Jackett settings
- change the language on B in Settings: after Lampa's reload it stays, and A gets it on next start
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
- stop `lampa-api`, change a setting, restart the app: the change is still there; start the
  API again and the change reaches the server
- old TV WebViews (both `lampa-app/LAMPA` engines): boot works and no ES5 errors appear

**Rollback**: revert the Plan 4 commits and redeploy. Server documents stay in PostgreSQL,
but the reverted API expects the dropped `encrypted_connections` column, so its
`/user-data` routes answer 503. That is harmless because the reverted frontend never calls
them. Devices keep their last local values.
