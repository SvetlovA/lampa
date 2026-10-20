# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repository is

This is the **distribution** repo of Lampa (a media-catalog / player app for TVs, set-top boxes and phones) — a fork of `yumata/lampa`. It contains build output, not sources. The original sources live in a separate, **not-checked-out** repo: https://github.com/yumata/lampa-source.

Consequences that shape everything else:

- There is **no package.json or build system** for the frontend. Fork-only add-ons and the one-link TorrServer patch have Node runtime checks and an ES5 syntax gate; browser and TV verification remains manual. The fork-only Go service under `backend/` has its own tests and linter (see "lampa-api" below).
- `app.min.js` (~56k lines) is the whole application. **Despite the name it is not minified** — it is a readable Rollup IIFE bundle passed through Babel (ES5 output, 2-space indent, JSDoc comments in Russian preserved). Edit it directly.
- `css/app.css` is compiled + autoprefixed SCSS output. Edit it directly too; do not expect a `.scss` source here.
- Upstream commits (author `yumata`) touch `app.min.js` + `assembly.json`, sometimes `css/app.css` and `lang/*.js`. Follow the same pattern.

Local work happens on the `svtlvtv` branch; `main` mirrors upstream. Several worktrees share this repo: `C:/Users/21art/Projects/lampa` (main), `.../worktrees/lampa/Lampa-SvtlvTv` (svtlvtv) and `.../worktrees/lampa/lampa-backend` (backend work, branch `lampa-backend`). The deploy workflow runs only from `svtlvtv` (the default branch), so backend work must be merged there before a dispatch can deploy it.

## Running it

Everything is static files, so any web server pointed at the repo root works.

Docker (from README):

```
docker build --build-arg domain={domain} -t lampa .
docker run -p 8080:80 -d --restart unless-stopped -it --name lampa lampa
```

`{domain}` is required — the build substitutes it into `msx/start.json`. `--build-arg prefix=` defaults to `http://`.

Verification is manual: load the page in a browser and exercise the change. `window.Lampa` exposes every module in the console, so most checks can be done from devtools.

## Navigating app.min.js

Rollup flattened many ES modules into one scope, so **name collisions were resolved with `$N` suffixes**: `Utils$1`, `Subscribe$2`, `component$4`, `create$5`, `add$h`, `object$2`. Suffixes are assignment-order artifacts and shift when upstream rebuilds — never treat them as stable API. The stable names are the keys of the `window.Lampa` object.

Practical ways in:

- `grep -n "window.Lampa = {"` (~55837) — the full public API map, and the fastest way to translate a public name into its internal variable (e.g. `Reguest: Request`, `Manifest: object$2`, `InteractionLine: create$1`).
- Each module ends with its export object at 2-space indent: `grep -n "^  var Activity = {"`. Finding that object gives you the module's method list; the implementations sit immediately above it.
- Upstream JSDoc is in Russian — grepping Russian terms (`Создать компонент`, `Загрузка языка`) often finds the right spot faster than English.

Anchors worth knowing (line numbers drift on every upstream pull — re-grep, don't trust these):

| Module | Locate with |
|---|---|
| `Manifest` (domains, versions, mirrors) | `grep -n "var object\$2 = {"` (~2072) |
| `Template` | `grep -n "var Template = {"` (~3129) |
| `Component` registry + `create`/`add`/`get` | `grep -n "var Component = {"` (~45117) |
| base `Component` class | `grep -n "var Component\$1 = "` (~43617) |
| `Activity` / `ActivitySlide` | `grep -n "var Activity = {"` (~46059) |
| `Controller` | `grep -n "var Controller = {"` (~46444) |
| `Platform` detection | `grep -n "function init\$D"` (~32488) |
| inline `ru` / `en` dictionaries | `grep -n "^  var ru = {"` / `"^  var en = {"` (~48666 / ~49941) |
| `window.lampa_settings` defaults | `grep -n "torrents_use = true"` (~55620) |
| boot sequence (`initClass` / `prepareApp` / `loadApp`) | end of file (~55835–56336) |

## Architecture

### Boot

`index.html` is a bare loader: it pulls `vender/*` (jQuery, navigator, keypad, scrollbar, notify) as globals, then `app.min.js?v=<15-minute bucket>`. If `AndroidJS.getLampaURL()` returns a URL, it instead loads the app from that remote host — the Android shell can point at an updated build without reinstalling. The end of `app.min.js` runs `initClass()` (populates `window.Lampa`) → `prepareApp()` (Platform, DeviceInput, Params, Controller, Keypad, Layer init) → `loadApp()` → `loadLang()` → plugin load → `showApp()`.

### Activity / Component / Controller — the core triangle

This is a TV app: there is no router in the web sense and no framework. Screens are a stack.

- **Component** — a screen class. `Component.add(name, class)` registers one; `Activity.push({component: 'name', ...})` instantiates and pushes it. Unknown names silently fall back to `nocomponent`. Contract: `create()`, `render()`, plus optional `start()`, `pause()`, `stop()`, `resize()`, `beforeRefresh()`, `destroy()`. Most built-in screens are built by `Utils.createInstance(Full|Line|...)` plus `comp.use({onCreate, onNext, onInstance, ...})` hooks rather than by subclassing.
- **Activity** — the screen stack (`push`, `back`, `replace`, `refresh`, `active`). Emits `Lampa.Listener.send('activity', {type: 'init'|'create'|...})` at each stage; that event is the main extension point for plugins.
- **Controller** — focus / remote-key routing. `Controller.add(name, {up, down, left, right, enter, back, toggle})` defines a focus context; `Controller.toggle(name)` activates it. A `MutationObserver` auto-binds any `.selector` element that enters the DOM. There is no click/tap-first model — **every interactive element must be `.selector` and respond to `hover:enter`**, otherwise it is unreachable by remote.

### Templates and i18n

`Template.add(name, html)` / `Template.get(name, vars)`. `get()` runs the HTML through `Lang.translate` first, then substitutes `{var}` placeholders and `{@other_template}` includes, and returns a jQuery object.

`Lang.translate('key')` looks up a flat dictionary; inside template strings the form is `#{key}`. `{site}` and `{mirror}` are always substituted from `Manifest`.

**Adding a translation key means editing two places.** `ru` and `en` are compiled *into* `app.min.js` (only those two — see `loadLang`, which skips the fetch for them), while every other locale is fetched at runtime from `lang/<code>.js`. Those files look like `export default {...}` but are loaded as text and `eval`'d after rewriting `export default` → `translate =`, so they are not real ES modules — don't add imports or anything else executable to them. `lang/ru.js` and `lang/en.js` are kept in sync with the inlined copies by upstream's build; if you add a key, update both the inline object and the matching `lang/*.js`.

### Platform abstraction

`Platform.get()` returns one of `webos`, `tizen`, `android`, `philips`, `apple_tv`, `apple`, `nw`, `electron`, `netcast`, `orsay`, `browser`, or `''`. Gate device-specific code with `Platform.is('android')` / `Platform.is(['tizen','webos'])`, never on user-agent sniffing at the call site. The detected value also lands on `<body>` as `platform--<name>`, which is how `css/app.css` does per-device styling. `webos/webOSTV.js` and the `Orsay` / `Tizen` modules wrap the vendor SDKs.

### Feature flags

`window.lampa_settings` (defaults applied via `Arrays.extend` near the end of the file) is the app-wide kill-switch table: `socket_use`, `account_use`, `plugins_use`, `torrents_use`, `iptv`, `white_use`, `read_only`, and a `disable_features` sub-object (dmca, lgbt, ai, trailers, remote_configuration, …). Several are forced off when `iptv` is set, and `torrents_use` is auto-disabled for store builds (RuStore / `lampa_client_yasha` user agents) to pass moderation. Build variants are produced by pre-setting `window.lampa_settings` before `app.min.js` loads — respect that and read flags at call time rather than caching them.

### Plugins

Third-party plugins are remote scripts loaded at boot (`Plugins.load`) from CUB account data plus `Storage.get('plugins')`, filtered through a blacklist. They extend the app purely through `window.Lampa` — `Component.add`, `Template.add`, `Lampa.Listener.follow('activity', ...)`, `SettingsApi.addComponent/addParam`. Keeping those APIs backward-compatible matters more than internal tidiness. See SECURITY.md: plugins are untrusted by design.

### Backend

The upstream app has no server of its own. It talks to CUB (`Manifest.cub_site` → `cub.best`, or `cub.black` when `window.vpn_region == 'ru'`) for accounts / sync / plugins, to TMDB for catalog data, and to user-configured TorrServer / Jackett instances for torrents. `Manifest.cub_mirrors` / `old_mirrors` drive mirror failover — when a domain changes, that list plus `Manifest` are what to edit.

### lampa-api (`backend/`)

A fork-only Go module (`go 1.26`, vendored) that signs users in with the Svtlv Keycloak realm (stateless sealed session cookies, `pkg/auth`) and stores one user-data document per user in PostgreSQL; design in `docs/settings-sync-backend-design.md`, usage in `backend/README.md`. It follows the Ralphex checkout's style (lowercase comments, `pkg/<responsibility>`, moq into `mocks/`, one `_test.go` per source file, stdlib `log` with `[INFO]`/`[WARN]` prefixes).

- Run `make test` / `make race` / `make lint` / `make fmt` **inside WSL Ubuntu** from `backend/` — the Windows Go has no cgo, so `-race` fails there.
- DB tests use testcontainers (`postgres:18.6`) and need Docker; they skip without it unless `LAMPA_API_REQUIRE_DOCKER=1` (CI sets it).
- The root `Dockerfile` does `COPY . htdocs/`, so **any new top-level directory must be added to the root `.dockerignore`** or it ships inside the public `lampa-web` image (`backend`, `docs`, `.codex` and `.ralphex` already are). `svtlv/` is the deliberate exception: it is the frontend add-on directory and must ship, so it is **not** in `.dockerignore`.
- Config is embedded `backend/pkg/config/defaults/appsettings.json` + `appsettings.<Environment>.json` (Svtlv layering, strict decoding), environment from `LAMPA_ENVIRONMENT` (default `Test`; `Development` = host `go run` against the DB on `127.0.0.1:5434`, not reachable from a container). `{ENV_VAR}` placeholders are resolved only in `Database.Password`, `DataKey`, and `Authentication.Keycloak.ClientSecret` (a new placeholder field needs its own entry in `config.Load`'s resolve list); there are no other env overrides. `Authentication.Keycloak.Authority` is a non-secret setting selected by environment; browser-facing origin comes from request Host and Apache-set `X-Lampa-Proto`. Ports: API `5800` (container-internal, reached only through the `/api/v1/` proxy), health `8081` (container-internal, never published or proxied), DB loopback `5434` — none may collide with Svtlv's published ports.
- `.github/workflows/tests.yaml` runs lint, `make test`, `make race` and a compose `config` check against `devops/.env.example` on every PR and push to `svtlvtv`; keep `.env.example` in step with the compose file.
- `.github/workflows/deploy-docker.yaml` is manual-only, runs only from the default branch, and has a single `environment` input (Development / Test / Production, default Production) written to the server `.env`; Development is rejected in `prepare`, before anything is built or stopped. It does not run tests. There are no image-tag or API on/off inputs: roll back by reverting and redeploying. It requires the `LAMPA_KEYCLOAK_CLIENT_SECRET` secret, reads the public URL from the selected appsettings files, and waits up to 3 minutes for `svtlvtv_lampa_api` to be healthy.
- `devops/docker-compose.yaml` builds `lampa-web:dev` / `lampa-api:dev` locally (copy `devops/.env.example` to the gitignored `devops/.env`, then run with `docker compose -p lampa`; there is no local override file) and needs the external `svtlv_monitoring_external` network. The deploy swaps the `:dev` images for `ghcr.io/<owner>/lampa-{web,api}:latest` and deletes `build:`…`dockerfile:` with `sed`, so **keep the header-comment layout rules**: `image:` before `build:`, only `context:`/`args:` between `build:` and `dockerfile:`, and no other line (comments included) containing `build:`. A post-strip guard fails the deploy otherwise.
- User-data schema version 2 accepts arbitrary JSON values with top-level keys matching `^[A-Za-z0-9_.:-]{1,128}$` (4 MiB body, depth 32). The optional client `encrypt` list selects up to 64 keys; the backend seals those values in place as `{"$sealed":"…"}` with `LAMPA_API_DATA_KEY`, bound to the user ID. Unreadable sealed values are omitted with a warning; v1 rows read as absent. Migration `00003` drops `encrypted_connections` and adds `etag`. GET returns a quoted ETag (SHA-256 of canonical plaintext data) or 304 on matching `If-None-Match`; PUT returns 204 + ETag and uses `If-Match` for an atomic conditional update (stale/missing row → 412), otherwise upserts. `X-Lampa-Expected-User` guards GET/PUT/DELETE before storage access (mismatch → 409). There is no backend secret-key list or UI-input drift test. `TestStorageScope_matchBundle` still fails on unclassified upstream storage keys, invalid scope data or stale rules; it skips only a backend-only checkout without the bundle. Back up `LAMPA_API_DATA_KEY` outside GitHub.
- The `lampa-web` image proxies `/api/v1/` (and nothing else) to `lampa-api:5800` with Apache `mod_proxy`, configured inline in the root `Dockerfile` (`disablereuse=On`, so an API restart with a new IP does not leave stale 502s). The frontend therefore calls the API same-origin; there is no CORS.
- Auth errors: `401` only when the session is missing, expired or revoked in Keycloak; any other `Authenticator` failure is `503 session_unavailable`. Keycloak outages fail open (sessions keep working, new logins fail). Every non-GET/HEAD/OPTIONS request needs `X-Lampa-Csrf: 1` and, if it has an `Origin`, exactly the request's browser-facing origin. `api.DenyAll` survives only for `pkg/api` tests.
- Frontend files (`app.min.js`, `css/app.css`, `lang/*`, `index.html`) are never touched by backend work.

### Svtlv add-ons (`svtlv/`)

Fork-only frontend integrations live in `svtlv/` as plain ES5 files. `account.js` + `account.css` provide the Account settings entry, header avatar (keeping CUB reachable), Keycloak sign-in/sign-out and session readiness. `torrserver-ownership.js` wraps the public `Lampa.Torserver` API for account and device-local guest My Torrents membership; removal never deletes a TorrServer record. `account-sync.js` applies account data before boot, polls for remote changes and saves local edits; it has no stylesheet. `storage-scope.json` is its runtime sync and encryption policy. Design: `docs/settings-sync-backend-design.md` §10.

- **Never edit `app.min.js`, `css/app.css` or `lang/*` for them.** An add-on uses only the public `window.Lampa` API, like a third-party plugin, so upstream pulls do not conflict with it.
- The explicit core-change exception is the small, marked (`// svtlv:`) TorrServer patch in `app.min.js` and its matching `assembly.json` stamp. It drops the second URL input/selector without migrating it (a device that used the second link falls back to `torrserver_url`), uses only the sole URL at runtime, and removes TrackTimecode: the settings toggle, `Torserver.viewed`/`viewedSet` and every shared `/viewed` read/write, so playback progress lives only in Lampa's local `file_view*`. It also rewords `settings_server_base_descr` ("Save to database") without naming TorrServer, inline and in every `lang/*.js`. Reapply this patch after upstream bundle updates; ownership and sync logic remain outside the bundle.
- The loader seam is the marked `<!-- svtlv:begin -->` … `<!-- svtlv:end -->` block at the end of `index.html`'s `<body>`. It appends `account.css`, loads `account.js` without waiting, then `torrserver-ownership.js` → `account-sync.js` → `svtlv_sync_boot(svtlvStart)` → `startLampa()`. A missing/throwing sync script starts Lampa through the fallback without the `.no-network` overlay; the pre-boot hook waits at most 3 seconds with a marker and completes immediately without one. Preserve the seam and marked `startLampa`/readyState loader adjustments on upstream updates; ownership must wrap Lampa before the first My Torrents render.
- CI (`tests.yaml`) parses `svtlv/*.js` with pinned `acorn --ecma5`, which checks syntax only: post-ES5 APIs (`fetch`, `Object.assign`, …) still have to be avoided by hand. CI also runs `node --test docs/tests/*.test.cjs backend/pkg/storage/testdata/torrserver-one-link.test.cjs` for runtime and documentation checks. The Go one-link guard checks bundle structure and metadata, not runtime behavior. Browser and TV verification remains manual.
- Ownership requests send `X-Lampa-Expected-User` as a precondition against the cookie-authenticated identity. A mismatch answers `409 account_changed` and triggers a session recheck without replaying the operation. Identity, URL and guest-storage changes destroy inactive My Torrents components through `Activity.all()` so Back recreates their filtered lists.
- `storage-scope.json` uses exact-key precedence and longest trailing-asterisk prefix matching. `user` and `user_connection` must sync; `user_optional` is best-effort (largest encoded optional values are dropped first to fit 4 MiB). Unknown keys remain local. To sync a key, add it to `user`/`user_optional` in `storage-scope.json`; to encrypt it, also add it to `encrypt`. No JS or Go change is required. The portable connection fields are `torrserver_url`, `torrserver_auth`, `torrserver_login` and `torrserver_password`; the shipped encrypt list chooses login/password. `file_view` is optional, `file_view_*` is external; plugins, menus and player settings never sync.
- `localStorage['svtlv_sync_user']` identifies the owner of local synced values; `svtlv_sync_base` holds per-key `length:FNV-1a` fingerprints outside Lampa.Storage. A dirty key differs from its last synced raw string, even after reload or a `nolisten` edit. Before boot, sync writes raw strings; live apply uses `Lampa.Storage.set` and refreshes Favorite/Timeline caches. First login keeps guest data only if no server document exists; otherwise it clears guest values before apply. Marker/base are written before a required reload. Logout/confirmed expiry clears synced keys with `''` (hiding IndexedDB reserve), drops marker/base and reloads, except during browser SSO navigation. Device keys remain local.
- Sync observes `Storage.listener` changes to schedule saves (2 seconds for must-sync, 5 minutes for optional-only), flushes on hide, and polls every 60 seconds while visible and on screen return. A save polls, overlays dirty keys and sends `If-Match`, retrying a 412 once. Each authenticated GET/PUT revalidates with Keycloak, including 304 responses. Network failure keeps edits dirty; 401/409 stops timers and sends the Account `expired` event.
- Add-ons must leave Lampa boot working and keep CSS scoped to their own classes. Account UI hides on non-HTTP(S) origins or an unreachable API; an active ownership wrapper fails closed for unresolved identity, account lookup errors or corrupt/unavailable guest storage. Guests use only `localStorage['svtlv_torrserver_guest_hashes']`, with no account/guest transfer or sync. Missing/disabled ownership scripts restore upstream shared behavior, so the filtered view is not an access-control boundary. Strings are an inline `{en, ru}` object, not `lang/*` keys.

## Conventions and gotchas

- **ES5 only.** The bundle is Babel output targeting old TV browsers (webOS, Tizen, Orsay/Maple on Safari 5.1). No arrow functions, `let`/`const`, template literals, native `Promise` (a polyfill is bundled), or modern DOM APIs in `app.min.js`. Match the surrounding compiled style — `_typeof`, `_createClass`, `.concat()` instead of template strings.
- **jQuery is the DOM layer** (`$` is global from `vender/jquery`). Events are jQuery events; the remote-control ones are custom: `hover:enter`, `hover:focus`, `hover:long`.
- **Version bumps.** `Manifest.app_version` / `css_version` (in `object$2`) are authoritative; `app_digital` / `css_digital` are derived getters. `assembly.json` duplicates all four plus a build timestamp and hash — it is a build stamp for external installers and is *not* read by `app.min.js`. `index.html` separately hardcodes `css/app.css?v=4.56`. Upstream regenerates `assembly.json` on every commit.
- **Pulling upstream will conflict** with any local edit to `app.min.js`, since upstream ships a full rebuild of that file. Keep local changes small, well-marked, and easy to reapply; expect to redo rather than merge them.
- Commit messages upstream are short English imperatives ("Fixed volume", "Added category"); a few are Russian. Match that.
