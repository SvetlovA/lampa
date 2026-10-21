# Restore two TorrServer URLs with a local selector

## Overview

Restore Lampa's two TorrServer URL inputs, link selector and runtime behavior
exactly as implemented before the one-link removal, preserving upstream
compatibility. Store both URLs and
the existing shared authentication settings in the account's database document.
Each client chooses its active link locally: switching links never changes the
account document or another client's selection. The default selection is URL 1.

The user's intended configuration is a tailnet address in URL 1 and a direct
home LAN IP address in URL 2, both reaching the same physical TorrServer.
The user accepts treating these addresses as separate server identities for
this implementation: each has its own My Torrents ownership list. Saving under
one address does not automatically claim the torrent under the other. Shared
identity or alias mapping is outside this plan.
Sharing owned torrents across both links is deferred in
[`docs/backlog/share-torrents-between-torrserver-links.md`](../backlog/share-torrents-between-torrserver-links.md).

## Context (from discovery)

- This is a distribution repository: the readable ES5 `app.min.js` bundle is the
  implementation, with no frontend source checkout or build system.
- The one-link patch removed `torrserver_url_two` and `torrserver_use_link` from
  settings and runtime paths. The pre-patch bundle at `ed967ae^:app.min.js`
  provides the original two-link UI and behavior. Restore relevant fragments,
  rather than reverting whole commits containing unrelated work.
- `svtlv/storage-scope.json` controls sync and encryption. Currently only
  `torrserver_url`, `torrserver_auth`, `torrserver_login` and
  `torrserver_password` are portable connection fields.
- `svtlv/account-sync.js` handles these fields generically. The backend already
  accepts arbitrary schema-version-2 JSON values and seals keys selected by the
  client. Neither a database migration nor a backend setting-name list is needed.
- `svtlv/torrserver-ownership.js` currently derives membership identity directly
  from URL 1 and refreshes only when that URL changes.
- `backend/pkg/storage/crypto_test.go` and
  `backend/pkg/storage/testdata/torrserver-one-link.test.cjs` enforce the old
  one-link behavior. Documentation guards also assume URL 2 cannot sync.
- The current implementation drops the old second link without migration; the
  historical migration described in the completed Plan 3 is not live code.

## Development Approach

- **Testing approach: Regular** — update code, then add or update behavioral tests.
- Complete each numbered task and pass its checks before starting the next.
- Keep changes focused and ES5-compatible. Add-on changes use public Lampa APIs.
- Restoring the built-in two-link configuration requires a narrow bundle edit,
  as explicitly requested. Account sync and ownership integration stay in `svtlv/`.
- Restore original two-link fragments without redesigning their behavior. Keep
  upstream names, markup, translations, selector registration, defaults, events,
  request routing and per-path fallback semantics. Adapt bundle-local identifiers
  only as needed to match the current Rollup scope.
- Update this plan if implementation reveals a material change in scope.
- Preserve existing shared-timecode removal, membership isolation, parser search
  modes and unrelated Jackett/Prowlarr settings.

## Solution Overview

Restore the original two-link implementation from `ed967ae^:app.min.js`, including
its existing behavior in each consumer. Add URL 2 to the existing
portable connection allowlist and classify the selector as device-local. Use
`Lampa.Torserver.ip()` as the public authority for the effective endpoint when
deriving membership identity, so list, playback and membership use the same server.

The credentials remain one shared username/password pair for both URLs, matching
the original settings. Login and password retain their existing encryption policy.

## Technical Details

### Storage contract

| Key | Scope | Database behavior |
| --- | --- | --- |
| `torrserver_url` | `user_connection` | Sync URL 1 |
| `torrserver_url_two` | `user_connection` | Sync URL 2 |
| `torrserver_auth` | `user_connection` | Sync shared authentication toggle |
| `torrserver_login` | `user_connection` | Sync and seal shared username |
| `torrserver_password` | `user_connection` | Sync and seal shared password |
| `torrserver_use_link` | `device` | Never export or import; local `one` / `two` |

- Register `torrserver_use_link` with default `one`. Preserve an explicit stored
  client selection across reloads and account sync. Preserve upstream behavior
  for missing/invalid stored values without adding new normalization or writes;
  applying remote settings must not reset a valid selection.
- Restore the original `Torserver.ip()` fallback: use the selected URL when present,
  otherwise the other nonempty URL. Both empty means unconfigured. Defaulting to
  `one` does not prevent fallback when only URL 2 is configured.
- Do not swap, copy or rewrite URL fields when the selector changes.
- Restore each runtime path exactly as before removal: torrent operations,
  streaming and speed tests use the original `Torserver.ip()` / `url()` behavior;
  parser/search and authentication use the selected URL field directly, as in
  upstream. Restore the original status-check selection, both URL change
  listeners, selector listener and speed-test insertion after the second input.
  Preserve the original differences when the selected field is empty; do not
  introduce a shared resolver or change fallback behavior in upstream consumers.
- Derive `server_id` from the effective raw URL using the existing normalization
  and SHA-256 algorithm. Existing URL-1 memberships therefore retain their IDs;
  aliases with different normalized hosts/paths/ports remain separate.
- Keep request callbacks bound to their captured account and effective server.
  Switching to a different server must invalidate pending ownership results.

### Sync and compatibility

- Both URLs follow existing document-level first-login rules: local guest values
  upload when the account has no document; an existing account document replaces
  guest data, including clearing absent synced fields. Later explicit local edits
  are dirty and save normally. Do not introduce a separate migration or merge rule.
- Switching links alone must produce no user-data PUT. Imports, polling,
  conditional-save retries and account replacement must preserve the local selector.
- Logout clears synced connection values under existing ownership rules while
  leaving the device-local selector local. Fresh clients still default to `one`.
- Existing schema-version-2 documents lacking URL 2 remain valid. Test both this
  case and documents containing both URLs, including explicit secondary clears.
- Old sync clients can omit URL 2 from subsequent whole-document writes because
  their allowlist excludes it. Operational verification must include refreshing
  clients onto the new version; mixed-version preservation is outside this change.

## Testing Strategy

- Exercise actual bundle functions in the existing Node VM harness, updating its
  one-link expectations to two-link behavior. Keep its current filename to avoid
  unnecessary CI and command changes.
- Cover both selections, default/invalid selection, selected-empty behavior in
  each upstream path, both-empty behavior, explicit schemes and authentication
  against the selected field. Use the historical implementation as the behavioral
  reference, including cases where parser/auth behavior differs from `ip()` fallback.
- Extend ownership tests for selected-server isolation and delayed responses.
- Extend sync tests with two clients sharing one server document but different
  local selections, including remote edits, secondary clears and logout.
- Update Go bundle/scope guards and documentation guard negative fixtures.
- Run the full frontend suite, pinned ES5 syntax checks and targeted Go guards.
  Browser and TV remote-control verification remains manual after implementation.

## Progress Tracking

- Mark completed items `[x]` immediately and keep this plan aligned with changes.
- Add newly discovered tasks with a ➕ prefix and blockers with a ⚠️ prefix.
- Move the plan to `docs/plans/completed/` only after implementation is complete.

## Auto Review

- `planning:make` plan-review verdict: **APPROVE**. No critical or important issues.
- [plan-review] Task 5 minor finding addressed: explicitly update `CLAUDE.md` so
  future maintenance restores both links and retains our sync/ownership policy.
- The reviewer verified historical per-path URL behavior, public `Torserver.ip()`
  availability, generic sync scope filtering and test coverage across all five tasks.
- Tasks 1 and 2 must both be complete before deployment: core routing and
  ownership must agree on the effective server in the delivered application.
- This review inspected the plan and code; application tests run during implementation.

## Implementation Steps

### Task 1: Restore two-link configuration and classify its storage keys

**Files:**
- Modify: `app.min.js`
- Modify: `assembly.json`
- Modify: `svtlv/storage-scope.json`
- Modify: `backend/pkg/storage/crypto_test.go`
- Modify: `backend/pkg/storage/testdata/torrserver-one-link.test.cjs`
- Modify: `docs/settings-sync-backend-design.md`
- Modify: `docs/tests/ownership-docs.test.cjs`

- [x] Restore both URL inputs and the selector from the original template and Params registration, preserving names, labels, markup, ordering, events and default `one` exactly.
- [x] Restore the original two-link fragments across torrent, parser/search, authentication, speed-test and status paths, including their original per-path fallback semantics and change listeners; adapt only shifted bundle-local identifiers.
- [x] Add `torrserver_url_two` to `user_connection` and `torrserver_use_link` to `device`; update policy notes while retaining shared credential encryption.
- [x] Rename/adapt the Go single-URL guard to require two inputs and the default selector; retain timecode-removal, unrelated-parser and version-stamp assertions. Update scope expectations and the design scope table/negative documentation fixture together so guards remain valid.
- [x] Update the application version and matching assembly metadata using the current stamp convention; leave CSS version unchanged unless CSS changes are actually required.
- [x] Update bundle runtime tests for both links, defaults, fallback, empty values, scheme handling, auth, search changes, speed tests and status checks, matching historical upstream behavior rather than imposing new consistency rules.
- [x] Compare each restored fragment with `ed967ae^:app.min.js`; add regression coverage for selected-empty cases proving that original `ip()` fallback and direct selected-field consumers remain unchanged.
- [x] Run the bundle runtime tests, documentation guards, current frontend tests, targeted Go guards and ES5 checks; all must pass before Task 2.

### Task 2: Bind ownership to the effective selected server

**Files:**
- Modify: `svtlv/torrserver-ownership.js`
- Modify: `docs/tests/torrserver-ownership.test.cjs`
- Modify: `docs/tests/account-state.test.cjs` (public API fixture if needed)

- [x] Resolve the raw effective endpoint through public `Lampa.Torserver.ip()` before applying existing server-ID normalization; preserve graceful behavior when the public API is unavailable.
- [x] Refresh the relevant My Torrents activity on either URL or local selector changes, keeping existing account-generation and effective-server checks.
- [x] Update public API fixtures to reflect actual endpoint selection and fallback.
- [x] Test signed-in and guest membership separation while switching between different servers, then switching back; confirm existing primary-server IDs and rows remain usable.
- [x] Test delayed list/add/playback responses after switching servers, fallback endpoint identity, unconfigured behavior and unchanged non-destructive removal.
- [x] Run ownership/account-state runtime tests and the ES5 gate; all must pass before Task 3.

Verification: 163 ownership/account-state tests and all 427 frontend tests passed.
Pinned Acorn ES5 parsing passed for each add-on and `app.min.js`; `git diff --check` passed.

### Task 3: Verify both URLs sync independently of local selection

**Files:**
- Modify: `docs/tests/account-sync.test.cjs`
- Modify: `svtlv/account-sync.js` (only if behavioral tests expose a gap in generic policy handling)

- [x] Add scope assertions showing both URLs are must-sync and the selector is local; retain login/password sealing assertions.
- [x] Test a first upload and another client's import of both URLs and credentials, with each client keeping its own selection and a fresh client defaulting to URL 1.
- [x] Test that selector-only changes neither dirty exported data nor issue a PUT; remote documents containing a selector must not overwrite the local value.
- [x] Test live polling, explicit URL edits, URL-2 clears/tombstones and stale-save retries with different client selections.
- [x] Test existing documents without URL 2, account replacement, logout and must-sync storage failures using current isolation and recovery rules.
- [x] Keep generic sync code unchanged if these behaviors already follow from policy; if a change is needed, cover the discovered failure and recovery.
- [x] Run all account-sync tests and the full frontend suite; all must pass before Task 4.

Verification: all 158 account-sync tests and all 444 frontend tests passed.
Pinned Acorn ES5 parsing passed for every add-on and `app.min.js`; `git diff --check` passed.
The existing generic sync implementation satisfies the two-link policy without changes.

### Task 4: Verify acceptance criteria

**Files:**
- Review: files changed in Tasks 1–3
- Update: relevant test files only if a concrete gap is found

- [ ] Verify both URLs and shared credentials are stored by account sync while selection remains local, persists across reloads and defaults to `one` when absent.
- [ ] Verify restored core fragments match the original two-link implementation, including its per-path selected-empty behavior; verify add-on membership identity follows the effective `Torserver.ip()` endpoint used by torrent operations.
- [ ] Run `node --test docs/tests/*.test.cjs backend/pkg/storage/testdata/torrserver-one-link.test.cjs` from the repository root.
- [ ] Run `npx --yes acorn@8.14.0 --ecma5 --silent svtlv/*.js app.min.js` and `git diff --check`.
- [ ] From `backend/`, run `go test ./pkg/storage -run 'TestTorrserver|TestStorageScope|TestSensitiveSettings'` and `make lint` if Go guard code changed. Use the available supported Go environment; record any unavailable checks explicitly.
- [ ] Add a regression test for any new failure discovered during acceptance review and rerun the affected checks before proceeding.

### Task 5: Update documentation and complete the plan

**Files:**
- Modify: `backend/README.md`
- Modify: `docs/settings-sync-backend-design.md`
- Modify: `CLAUDE.md`
- Modify: `docs/tests/ownership-docs.test.cjs` (if additional assertions need updating)
- Move: this plan to `docs/plans/completed/` after completion

- [ ] Replace current one-link descriptions with both synced URLs, shared credentials, the local selector/default, original upstream per-path URL behavior and effective-server membership behavior. Document the original selected-empty differences without presenting a new routing policy.
- [ ] Document compatibility with older one-URL documents and the need to refresh older clients before relying on durable URL-2 sync.
- [ ] Update `CLAUDE.md` to remove instructions to reapply the one-link patch, list both portable URL fields and the device-local selector, and describe the revised bundle guard while retaining the separate shared-timecode removal. Update other active guidance and commands if needed; leave completed historical plans as records of their original work.
- [ ] Run documentation guards and the final frontend suite, record verification results and remaining manual checks, then move this completed plan.

## Post-Completion

**Manual verification:**
- In a browser and on a TV remote, edit both URL inputs, select each link, reload,
  and exercise status checks, authentication, search, speed test and playback.
- Sign in on two clients. Confirm both receive URL edits and credentials while
  selecting different links. Inspect network activity to confirm a switch alone
  sends no user-data PUT.
- Confirm My Torrents follows the selected effective server and restores the
  original membership list when switching back.
- Refresh older clients after deployment before relying on URL-2 persistence.

**External systems:**
- Deploy through the normal workflow when separately requested. No PostgreSQL
  schema or Keycloak configuration change is expected.
