# Sync user data with lampa-api (Plan 4)

## Overview
- Final roadmap plan (design §14 Plan 4). It connects Lampa's browser storage to the
  existing `/api/v1/user-data` document, so a signed-in user gets the same config and
  personal data on every device.
- Keep it as simple as possible. Config and user data must sync reliably. History,
  progress and torrent views are best-effort: they sync when they fit and are dropped
  otherwise, with no extra logic to force them through.
- Anonymous mode stays local-only, with no user-data request at all. Device settings
  never leave the device. Logout clears the synced keys (resolves design §16).
- A running device picks up changes from other devices by **polling with an ETag**
  (document hash): on screen return, every 60s while visible, and before each save.
  Changes are applied live, without a reload, also while a video is playing (the stream
  URL is built at playback start, so the current video keeps playing).
- The UI keeps using `localStorage` exactly like upstream Lampa; only the add-on talks to
  the API. The synced keys stay an explicit allowlist (`storage-scope.json` + the add-on
  table). The backend knows no Lampa keys except the sealed credentials, so adding a key
  later is a frontend-only edit.
- All work is in a new `svtlv/account-sync.js` add-on, the marked `index.html` seam,
  small `account.js` changes and small backend changes (precondition, ETag/304). No change
  to `app.min.js`, `css/app.css`, `lang/*` or `assembly.json`.

## Context (from discovery)
- Backend is done: `GET/PUT/DELETE /api/v1/user-data`, 8 fixed sections, 1 MiB per
  section, 2 MiB body, `settings.torrserver_login/password` sealed
  (`backend/pkg/storage/document.go`, `crypto.go`, `backend/pkg/api/userdata.go`).
  `404 user_data_not_found` when no document exists. PUT needs `Content-Type: application/json`
  (else 415), `X-Lampa-Csrf: 1` and a matching `Origin` (else 403).
- `X-Lampa-Expected-User` → `409 account_changed` exists only for torrent routes
  (`torrentIdentityMatches` in `backend/pkg/api/torrents.go`).
- `svtlv/storage-scope.json` lists 13 `user` and 4 `user_connection` keys.
- `svtlv/account.js` publishes `window.svtlv_account_state {status, generation, user_id}`
  and the `svtlv_account` Listener event, and rechecks the session on
  `{type: 'expired'}` (used by the ownership add-on). `logOutDone` publishes guest, then on
  the browser path navigates to the Keycloak `logout_url`. Its Account page text still says
  data "stays on this device" (`account.js:36`, ru `:72`).
- `Lampa.Storage` (`app.min.js` ~49917–50010): `set` JSON-stringifies objects/arrays into
  `localStorage` and sends `change` (unless `nolisten`); reads are cached in memory
  (`readed`); on `QuotaExceededError` the key is removed from `localStorage` and kept in
  IndexedDB (`reserve`). So data written after boot needs a reload, and a missing
  `localStorage` key does not mean Lampa has no value.
- Playback saves `file_view` every 2 minutes (`saveTimeView`), so a playing page writes often.
- Live apply is possible through public APIs: `Lampa.Storage.set(key, value)` updates the
  memory cache and sends `change`; `Storage.get` parses only values starting with `[`/`{`
  and converts `'true'`/digits itself, so passing the raw string (JSON-parsed only for
  `[`/`{`) behaves exactly like a fresh read. `Lampa.Favorite.read()` re-reads `favorite`
  and sends `state:changed`, so bookmark screens update.
- `torrserver_url` is read when playback starts (`Storage.get`/`field` at call time); the
  stream URL is built once, so changing it does not interrupt the current video.
- Every authenticated request is revalidated against Keycloak with no cache (design §5.3.1),
  so each poll costs one Keycloak call. 60s × a few devices is acceptable; seconds is not.
- `index.html` seam: account.js (no wait) → torrserver-ownership.js → `startLampa()`.
  `torrserver-ownership.js` installs a `window.Lampa` accessor; other add-ons must poll for
  `window.Lampa`, never redefine it.
- Tests: `node --test docs/tests/*.test.cjs` run the real add-on in `vm` with stubbed
  `Lampa`, `$.ajax` and storage; `acorn --ecma5` parses `svtlv/*.js`; Go `make test/race/lint`.

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
  `Lampa.Storage.listener`, `Lampa.Listener`, `storage` window events and `location.reload`.
- Go: table-driven cases in `backend/pkg/api/userdata_test.go`.
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
                 ┌──────── account-sync.js ────────┐
                 │ poll: GET If-None-Match → apply │
                 │ local change → GET + PUT dirty  │
                 └────────────────┬────────────────┘
                                  ▼
                           lampa-api / Postgres
```
**Note:** Lampa itself never talks to our API. The add-on keeps its local store in step
with the server, so upstream bundle updates do not conflict with sync.

Four rules keep it simple and safe:

1. **Before boot, write raw; after boot, write through `Lampa.Storage`.** Server data is
   applied before boot when the device has synced before. After boot, account changes
   (login, other user) reload the page if anything changed; changes from other devices
   of the same account are applied live (rule 4).
2. **Save = fresh server copy + keys this page changed.** Every save GETs the document, puts
   this page's changed ("dirty") keys on top, and PUTs it. A page open for days cannot roll
   back what another device saved. If two devices change the same key, the last one wins (§7).
3. **The marker says whose data is on the device.** `localStorage['svtlv_sync_user']` holds
   the user ID. Data from another user, or guest data, is cleared before any apply.
4. **Poll with an ETag and apply live.** The server's ETag is the SHA-256 of the stored
   document. A `304` means nothing changed. On `200`, each table key that differs and is
   not dirty here is written with `Lampa.Storage.set`, so screens react as if the user
   changed it. This also happens during playback; there is no waiting.

Boot (only on a device that has synced before):

```
index.html seam
  account.js ─────────────── (loads, no wait)
  torrserver-ownership.js ── loaded
  account-sync.js ────────── try { svtlv_sync_boot(done) } catch → done()
       arm 3s timer first
       no marker → done() at once           (guest / first login: no request)
       GET /api/v1/user-data  X-Lampa-Expected-User: <marker>
         200 → apply table keys → ready     ─┐
         404 → ready, all local keys dirty   │
         401 → clear synced keys + marker    ├─ done() → startLampa()
         409 → clear synced keys + marker    │
         503 / error / timeout → not ready  ─┘
```
**Note:** Lampa starts only after this finishes or gives up after 3 seconds, so it reads
server values straight from `localStorage`. The header makes the server answer 409 when
the cookie belongs to someone other than the marker, so one user's data never lands under
another's marker.

After boot, everything is driven by the current account state and the marker:

```
status signed_in(U):
   marker ≠ U (or none) → clear synced keys → GET:
        200 → apply → marker = U → reload if anything changed
        404 → marker = U → all local synced keys dirty → save
   marker = U, boot not ready → GET → apply → reload if changed
   marker = U, ready → uploads on
status guest and marker set → clear synced keys + marker → reload
                              (skip reload while account.js navigates to logout_url)
storage event on svtlv_sync_user (another tab) → reload
Storage 'change' on a table key → mark dirty → 2s debounce → save
marker = U, ready: visible again │ every 60s while visible → poll (rule 4)
```
**Note:** "guest" here means the confirmed guest state, whether it came from a logout or
from an expired session found at any time. The marker is written before any reload, so
the reloaded page takes the pre-boot path and does not loop.

Key decisions:
- **Raw strings.** Each document value is the exact `localStorage` string. The round trip
  is lossless and needs no knowledge of Lampa value types. The sealed keys still sit under
  `settings` as JSON strings.
- **Import is an allowlist.** Apply writes only table keys, read from their own section,
  and only string values. Anything else in the document is ignored.
- **Missing server keys.** With marker == U, a table key present locally but absent on the
  server is marked dirty and uploaded. With marker ≠ U, local keys are cleared first, so
  guest data reaches an account only on a 404 first login (§6.3).
- **A dirty key with no `localStorage` value** (moved to IndexedDB by Lampa on quota) is
  skipped, so the server keeps its value.
- **No feature flag.** Rollback = revert + redeploy (agreed).

## Technical Details

Key → section table. **Draft: the user decides the final list of synced keys** before
Task 2 starts. The final list is recorded in `storage-scope.json` (`user` /
`user_connection`) and in this add-on table, and a test keeps the two equal. Keys left out
stay device-local. Changing the list later is a frontend-only edit (JSON + table), with no
Go change:

| Section | Keys | Guarantee |
|---|---|---|
| `settings` | `language`, `tmdb_lang`, `torrserver_url`, `torrserver_auth`, `torrserver_login`, `torrserver_password` | must sync |
| `favorites` | `favorite` (bookmarks, likes, watch-history tabs) | must sync |
| `scores` | `mine_reactions` | must sync |
| `subscriptions` | `person_subscribes_id` | must sync |
| `progress` | `file_view`, `online_view`, `online_watched_last`, `torrents_view` | best-effort |
| `history` | `search_history`, `online_last_balanser`, `user_clarifys` | best-effort |
| `bookmarks`, `other` | unused, sent as `{}` | — |

**Note:** `file_view_<cub profile>` keys are skipped on purpose. Design §10.3 forbids
importing another CUB profile's progress, and skipping is simpler than mapping.

Document example (PUT body):
```json
{"schema_version": 1, "data": {
  "settings": {"language": "ru", "torrserver_url": "http://ts:8090"},
  "favorites": {"favorite": "{\"book\":[...],\"history\":[...]}"},
  "progress": {"file_view": "{...}"}, "history": {}, "scores": {}, "subscriptions": {},
  "bookmarks": {}, "other": {}}}
```

The add-on keeps the last server document and its ETag in memory (`lastDoc`, `lastEtag`).

Poll / live apply (one request at a time; skipped while the page is hidden):

```
GET user-data  If-None-Match: lastEtag
   304 → nothing to do
   200 → lastDoc, lastEtag = response
         for each table key: server ≠ local and key not dirty →
              applying = true
              Lampa.Storage.set(key, raw starts with [ or { ? JSON.parse(raw) : raw)
              applying = false
         'favorite' written → Lampa.Favorite.read()
```
**Note:** the add-on ignores `change` events while `applying` is set, so applied values
are not uploaded again. `language` and `tmdb_lang` are applied too, but the UI language
changes only on the next start; that is accepted. A key missing on the server is left
alone here (it is handled by the "missing server keys" rule after login).

Save flow (one at a time; changes during a save schedule one more save):

```
poll (above) ─┬─ 304/200 → base = lastDoc
              └─ 404     → base = full local export
overlay dirty keys (local string; skip if missing)
size rule → PUT (Content-Type json, X-Lampa-Csrf: 1, X-Lampa-Expected-User: U)
   204 → lastDoc = sent doc, lastEtag = response ETag, clear the dirty keys sent
```
**Note:** saving starts with a poll, so changes from other devices are applied first and
the PUT only adds this page's own changes on top. The PUT response carries the new ETag,
so the next poll does not download the page's own write.

What happens when the TorrServer URL is changed on another device during playback:

```
TV: playing (stream URL built from old torrserver_url)
phone: changes torrserver_url → PUT
TV poll: 200 → apply now → Storage 'change' torrserver_url
      → settings, My Torrents and the ownership add-on switch to the new server
TV: current video keeps streaming from the old URL
```
**Note:** the update is applied at once, even during playback. The playing video is not
interrupted because its URL was built at start. Anything opened next uses the new URL;
a playlist item built earlier may still point at the old server, which is accepted.

Size rule: if a section is over 1 MiB, or the body is over 2 MiB, drop `history`, then
`progress`, and try again. If a must-sync section is still too big, skip the upload and
show a Noty. Measure UTF-8 bytes inside try/catch; a failure (lone surrogate) means skip +
Noty. A playing page saves about every 2 minutes (one GET + one PUT); this is accepted.

Errors (GET or PUT):

| Case | Behavior |
|---|---|
| network / 5xx | keep local and dirty keys, Noty "Not saved to Account" once per failure streak, retry on next change |
| 401 / 409 | stop uploads, drop the timer, send `svtlv_account {type: 'expired'}` so account.js rechecks |
| 400 / 403 / 413 / 415 | Noty, keep local, no retry loop |
| boot GET did not succeed (timeout/503/500) | boot with local; one Noty after boot; post-boot GET when `signed_in` |
| pre-boot `setItem` fails (quota) | stop apply, uploads off for this page, Noty |

Unsaved changes are lost when the session ends (401 or logout), including a change still
inside the 2s debounce. This is accepted.

Strings: inline `{en, ru}` object like `account.js` (no `lang/*` keys).

Public surface: `window.svtlv_sync_boot(done)`, which calls `done` exactly once, and
`window.svtlv_sync_loaded`. Network calls use `$.ajax` with an explicit `timeout` (no
`fetch`, no `xhr.timeout`). ES5 only. Nothing is defined on non-HTTP(S) origins
(`^https?:$` guard), so `file://` shells boot as before.

## What Goes Where
- **Implementation Steps**: backend precondition, add-on, seam, account.js changes, tests, CI, docs.
- **Post-Completion**: deploy, two-device browser/TV checks, rollback check.

## Implementation Steps

### Task 1: Expected-user precondition on user-data routes

**Files:**
- Modify: `backend/pkg/api/torrents.go` (move helper)
- Modify: `backend/pkg/api/identity.go`
- Modify: `backend/pkg/api/userdata.go`
- Modify: `backend/pkg/api/userdata_test.go`
- Modify: `backend/pkg/api/torrents_test.go` (only if the rename touches it)

- [ ] move `torrentIdentityMatches` to `identity.go` as `identityMatches` and keep torrent routes using it
- [ ] call `identityMatches` in GET, PUT and DELETE `/user-data`, before storage access
- [ ] GET: set `ETag: "<sha256 hex of the response body>"`; answer `304` with no body when `If-None-Match` matches; set `Cache-Control: no-store` (or `no-cache`) so no proxy or browser cache answers for the server
- [ ] PUT: return the new document's ETag in the `204` response, computed the same way a following GET would compute it
- [ ] write tests: header absent passes, header equal passes, header different → `409 account_changed` with no storage call, for all three methods
- [ ] write tests: ETag is stable for the same document and changes after a PUT; matching `If-None-Match` → `304`, other or missing → `200`; PUT ETag equals the next GET ETag; sealed fields do not change the hash between reads
- [ ] run `make fmt`, `make test`, `make race`, `make lint` in `backend/` - must pass before task 2

### Task 2: Add-on core — key table, export, apply, size rule

**Files:**
- Create: `svtlv/account-sync.js`
- Create: `docs/tests/account-sync.test.cjs`
- Modify: `.github/workflows/tests.yaml`

- [ ] ⚠️ get the user's final list of synced keys (and which are must-sync vs best-effort); move any removed keys out of `user`/`user_connection` in `storage-scope.json` and keep `TestStorageScope_matchBundle` passing
- [ ] create `svtlv/account-sync.js` IIFE with the protocol guard, the section table, inline `{en, ru}` strings and a `$.ajax` helper (JSON, timeout, `Content-Type` on PUT)
- [ ] implement `exportLocal()` (raw strings, skip null, every section present), `overlay(base, dirtyKeys)` and the size rule
- [ ] implement `applyDoc(data)`: only table keys from their own section with string values; returns `{changed, missingOnServer}`; a `setItem` failure throws to the caller; `clearSynced()` removes table keys and the marker
- [ ] wrap every storage access in try/catch; storage failures turn sync off for the page and never block boot
- [ ] write tests: table equals storage-scope `user`+`user_connection` (minus `file_view_*` if still listed); export/apply round trip is exact; apply ignores unknown keys, wrong sections and non-string values; `changed` and `missingOnServer` are correct
- [ ] write tests: overlay keeps server values for non-dirty keys and for dirty keys missing locally; oversize best-effort section is dropped; oversize must-sync blocks upload; UTF-8 measurement and lone-surrogate failure
- [ ] add `node --test docs/tests/account-sync.test.cjs` to `tests.yaml`; confirm `acorn --ecma5` passes on the new file
- [ ] run node tests - must pass before task 3

### Task 3: Pre-boot apply and loader seam

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `index.html` (inside `svtlv:begin/end` only)
- Modify: `docs/tests/account-sync.test.cjs`
- Modify: `docs/tests/torrserver-ownership.test.cjs` (seam assertion ~821)

- [ ] implement `window.svtlv_sync_boot(done)` per the boot diagram: arm the 3s timer first; no marker → `done` with no request; `done` called exactly once; late responses ignored
- [ ] handle 200 (apply, ready), 404 (ready, all local keys dirty), 401/409 (clear, not ready), other (not ready, remember for the after-boot Noty), apply failure (uploads off, Noty)
- [ ] change the seam: ownership script → `account-sync.js` → start; the load callback runs `try { svtlv_sync_boot ? svtlv_sync_boot(svtlvStart) : svtlvStart() } catch (e) { svtlvStart() }`; the error callback calls `svtlvStart`
- [ ] write tests for every boot branch, the timeout, the late response, `done` once, and a throwing boot still starting Lampa
- [ ] update the seam test for the new order; check that a missing add-on file still starts Lampa
- [ ] run node tests - must pass before task 4

### Task 4: Post-boot account handling

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `svtlv/account.js`
- Modify: `docs/tests/account-sync.test.cjs`
- Modify: `docs/tests/account-state.test.cjs`

- [ ] poll for `window.Lampa` like `account.js` (never `defineProperty`); read `svtlv_account_state` and follow `svtlv_account` state events
- [ ] `signed_in(U)` per the post-boot diagram: clear when marker ≠ U; GET; apply; set marker before any reload; reload only if `changed`; on 404 set marker and save all local keys; mark `missingOnServer` keys dirty
- [ ] `guest` with marker set → clear + reload unless `window.svtlv_account_leaving`; `checking`/`unavailable` change nothing
- [ ] `$(window).on('storage')` for `svtlv_sync_user` changes → reload
- [ ] `account.js`: set `window.svtlv_account_leaving = true` before `applySession` when `logOutDone` will navigate to `logout_url`; change the "stays on this device" text (en and ru) to say data syncs to the Account
- [ ] write tests: TV login with 200 (marker before reload, reload only when changed, no reload when equal → uploads on), with 404 (save local, no reload), another user's marker (cleared first), first login with 200 clears guest keys
- [ ] write tests: logout clears and reloads; SSO logout clears without reload; `guest` at startup with a marker clears; anonymous start sends no request; storage event reloads; `account-state` test covers the leaving flag
- [ ] run node tests - must pass before task 5

### Task 5: Save on change

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `docs/tests/account-sync.test.cjs`

- [ ] follow `Lampa.Storage.listener` `change`; mark table keys dirty only while uploads are on
- [ ] 2s debounce; save flow per the diagram; one save at a time; changes during a save schedule exactly one more
- [ ] error handling per the Errors table, including `expired` on 401/409 and one Noty per failure streak
- [ ] write tests: several changes → one GET + one PUT; non-table key → nothing; PUT body = server doc + dirty keys only; headers and `Content-Type` present; change during a save → one follow-up
- [ ] write tests: 404 before PUT uses the full local export; 401/409 stop uploads and send `expired`; 5xx shows one Noty and the next change retries; 403/415 show a Noty without retry; no saves before ready or after logout
- [ ] run node tests - must pass before task 6

### Task 6: Live refresh by polling

**Files:**
- Modify: `svtlv/account-sync.js`
- Modify: `docs/tests/account-sync.test.cjs`

- [ ] implement `poll()` per the poll diagram with `lastDoc`/`lastEtag`; one request at a time; the save flow (Task 5) starts with `poll()`
- [ ] triggers: `visibilitychange` to visible, a 60s timer that runs only while visible and uploads are on, and the start of each save; stop the timer on logout or 401/409
- [ ] live apply through `Lampa.Storage.set` with the `[`/`{` JSON rule and the `applying` guard; `Lampa.Favorite.read()` after `favorite`; skip dirty keys
- [ ] write tests: 304 changes nothing; 200 writes only differing non-dirty table keys and calls `Favorite.read` only when `favorite` changed; applied keys do not trigger a PUT; dirty keys are kept and win on the next save
- [ ] write tests: hidden page does not poll; becoming visible polls once; PUT ETag prevents downloading the page's own write; booleans/numbers/JSON round-trip through `Storage.get` like a fresh read
- [ ] run node tests - must pass before task 7

### Task 7: Verify acceptance criteria
- [ ] anonymous (no marker): no `/user-data` request at all
- [ ] first login (browser redirect or TV device flow): server data applied with at most one reload; guest data uploaded only when the server has no document
- [ ] later starts: server values used without reload
- [ ] change while signed in: one GET + PUT after debounce, and keys changed on another device are not rolled back
- [ ] a change on another device shows up on a running page within about 60s (or on screen return) without a reload, also during playback
- [ ] logout or expired session: synced keys gone, device keys (`player`, `jackett_*`, `plugins`, …) untouched
- [ ] run full suite: `node --test docs/tests/*.test.cjs backend/pkg/storage/testdata/torrserver-one-link.test.cjs`, `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js`, and `make test race lint` in `backend/`
- [ ] confirm `git diff` does not touch `app.min.js`, `css/app.css`, `lang/*`, `assembly.json`

### Task 8: [Final] Update documentation
- [ ] `docs/settings-sync-backend-design.md`: §6/§7 describe boot apply, polling with ETag, live apply (also during playback) and "fresh server copy + dirty keys"; §8 notes the user-data precondition, `ETag`/`304` on GET and the ETag on PUT; §5.3.1 notes that each poll is one Keycloak call; §10 lists `account-sync.js` (no `account-sync.css`); §10.3 marks best-effort sections; §16 resolved (clear on logout); Status → Implemented
- [ ] §14 Plan 4 deviations: no feature flag; `file_view_*` not mapped; §6.3 read-back GET dropped; key-level last-wins instead of whole-document; CUB writers not coordinated (`favorite`/`file_view` unused while CUB sync is active); "Clear cache" and CUB backup restore are undone by the next boot GET; unsaved changes lost on 401/logout; sign-in/out notices disappear in the reload
- [ ] keep `docs/tests/ownership-docs.test.cjs` passing after doc edits
- [ ] `CLAUDE.md` Svtlv add-ons section: add `account-sync.js`, the seam order, the marker key, raw-string values and the save rule
- [ ] `backend/README.md`: mention the precondition if it lists route behavior
- [ ] move this plan to `docs/plans/completed/`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Deploy**: run `deploy-docker.yaml` (Production) from `svtlvtv`. No new secrets, config
or Compose changes. Check that `svtlv/account-sync.js` is served by `lampa-web`.

**Manual verification** (two devices, different player and Jackett settings):
- browser A signs in and adds a bookmark, a reaction, a language and the TorrServer URL and
  login; TV B signs in with the device flow, reloads once and shows the same values,
  while B keeps its own player and Jackett settings
- leave B playing a video; add a bookmark on A; B's progress saves must not remove it
  (check on A after a reload)
- with B idle on the bookmarks screen, add a bookmark on A: it appears on B within about
  60s without a reload; switching B's app away and back shows it immediately
- B plays a torrent; change the TorrServer URL on A: within about 60s B's settings show the
  new URL while its video keeps playing; after closing the player My Torrents shows the new
  server; both devices now share one server ID
- first login on a device with guest data and no server document uploads that data;
  with an existing server document the guest data is cleared instead
- logout on the TV clears bookmarks; browser SSO logout returns to a clean guest page;
  a second browser tab reloads when the first tab logs out
- stop `lampa-api`: Lampa boots within ~3s with local data and shows the sync Noty;
  restart the API and the next change saves
- old TV WebViews (both `lampa-app/LAMPA` engines): boot works and no ES5 errors appear in the console

**Rollback**: revert the Plan 4 commits and redeploy. Server documents stay in
PostgreSQL. Devices keep their last local values.
