# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

## What this repository is

This is the **distribution** repo of Lampa (a media-catalog / player app for TVs, set-top boxes and phones) — a fork of `yumata/lampa`. It contains build output, not sources. The original sources live in a separate, **not-checked-out** repo: https://github.com/yumata/lampa-source.

Consequences that shape everything else:

- There is **no package.json, no build system, no tests, no linter** for the frontend. Nothing to install, nothing to compile. The one exception is the fork-only Go service under `backend/` (see "lampa-api" below).
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

A fork-only Go module (`go 1.26`, vendored) that stores one user-data document per user in PostgreSQL; design in `docs/settings-sync-backend-design.md`, usage in `backend/README.md`. It follows the Ralphex checkout's style (lowercase comments, `pkg/<responsibility>`, moq into `mocks/`, one `_test.go` per source file, stdlib `log` with `[INFO]`/`[WARN]` prefixes).

- Run `make test` / `make race` / `make lint` / `make fmt` **inside WSL Ubuntu** from `backend/` — the Windows Go has no cgo, so `-race` fails there.
- DB tests use testcontainers (`postgres:18.6`) and need Docker; they skip without it unless `LAMPA_API_REQUIRE_DOCKER=1` (CI sets it).
- The root `Dockerfile` does `COPY . htdocs/`, so **any new top-level directory must be added to the root `.dockerignore`** or it ships inside the public `lampa-web` image (`backend`, `docs`, `.codex` and `.ralphex` already are).
- Config is embedded `backend/pkg/config/defaults/appsettings.json` + `appsettings.<Environment>.json` (Svtlv layering, strict decoding), environment from `LAMPA_ENVIRONMENT` (default `Test`; `Development` = host `go run` against the DB on `127.0.0.1:5434`, not reachable from a container). Secrets are `{LAMPA_DB_PASSWORD}` / `{LAMPA_API_DATA_KEY}` placeholders, resolved only in `Database.Password` and `DataKey` (a new secret field needs its own `resolve` call in `config.Load`); there are no other env overrides. Ports: API `5800` (published on `LAMPA_BIND_ADDRESS`, like the web port), health `8081` (container-internal, never published), DB loopback `5434` — none may collide with Svtlv's published ports.
- `.github/workflows/tests.yaml` runs lint, `make test`, `make race` and a compose `config` check against `devops/.env.example` on every PR and push to `svtlvtv`; keep `.env.example` in step with the compose file.
- `.github/workflows/deploy-docker.yaml` is manual-only, runs only from the default branch, and has a single `environment` input (Development / Test / Production, default Production) written to the server `.env`. It does not run tests. There are no image-tag or API on/off inputs: roll back by reverting and redeploying. It waits up to 3 minutes for `svtlvtv_lampa_api` to be healthy.
- `devops/docker-compose.yaml` builds `lampa-web:dev` / `lampa-api:dev` locally (copy `devops/.env.example` to the gitignored `devops/.env`; there is no local override file) and needs the external `svtlv_monitoring_external` network. The deploy swaps the `:dev` images for `ghcr.io/<owner>/lampa-{web,api}:latest` and deletes `build:`…`dockerfile:` with `sed`, so **keep the header-comment layout rules**: `image:` before `build:`, only `context:`/`args:` between `build:` and `dockerfile:`, and no other line (comments included) containing `build:`. A post-strip guard fails the deploy otherwise.
- `storage.SensitiveSettings` (sealed with `LAMPA_API_DATA_KEY` into `encrypted_connections`) mirrors the secret inputs of the Lampa settings UI: TorrServer login/password, Jackett and Prowlarr keys. `TestSensitiveSettings_matchUI` parses `app.min.js`, so **an upstream pull that adds any settings input fails CI until it is classified** — sealed in `SensitiveSettings` or plain in the test's `plainUIInputs`. `LAMPA_API_DATA_KEY` must be backed up outside GitHub (losing it makes stored credentials unreadable).
- User-data routes answer `401` until a real `Authenticator` replaces `api.DenyAll` (Plan 2); that is expected, not a bug.
- Frontend files (`app.min.js`, `css/app.css`, `lang/*`, `index.html`) are never touched by backend work.

## Conventions and gotchas

- **ES5 only.** The bundle is Babel output targeting old TV browsers (webOS, Tizen, Orsay/Maple on Safari 5.1). No arrow functions, `let`/`const`, template literals, native `Promise` (a polyfill is bundled), or modern DOM APIs in `app.min.js`. Match the surrounding compiled style — `_typeof`, `_createClass`, `.concat()` instead of template strings.
- **jQuery is the DOM layer** (`$` is global from `vender/jquery`). Events are jQuery events; the remote-control ones are custom: `hover:enter`, `hover:focus`, `hover:long`.
- **Version bumps.** `Manifest.app_version` / `css_version` (in `object$2`) are authoritative; `app_digital` / `css_digital` are derived getters. `assembly.json` duplicates all four plus a build timestamp and hash — it is a build stamp for external installers and is *not* read by `app.min.js`. `index.html` separately hardcodes `css/app.css?v=4.56`. Upstream regenerates `assembly.json` on every commit.
- **Pulling upstream will conflict** with any local edit to `app.min.js`, since upstream ships a full rebuild of that file. Keep local changes small, well-marked, and easy to reapply; expect to redo rather than merge them.
- Commit messages upstream are short English imperatives ("Fixed volume", "Added category"); a few are Russian. Match that.
