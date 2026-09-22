# Lampa

Lampa is a free media catalog and player interface designed for televisions,
set-top boxes, phones, and desktop devices. It uses public sources to display
information about films, television series, new releases, and popular titles.
The application does not operate its own media-distribution servers.

> [!IMPORTANT]
> This repository contains the ready-to-run Lampa distribution, not its original
> source project. The source code and build system are maintained separately in
> [yumata/lampa-source](https://github.com/yumata/lampa-source).

## Supported platforms

- LG webOS
- Samsung Tizen
- MSX
- Android
- macOS
- Windows

The distribution consists entirely of static files and can also be served by a
regular web server for use in a compatible browser.

## Repository contents

| Path | Purpose |
| --- | --- |
| `index.html` | Application entry point and script loader |
| `app.min.js` | Complete prebuilt application bundle |
| `css/app.css` | Compiled application styles |
| `lang/` | Runtime translation files |
| `msx/start.json` | MSX application descriptor |
| `vender/` | Browser libraries loaded by `index.html` |
| `Dockerfile` | Apache-based production image |
| `devops/docker-compose.yaml` | Local and server Compose configuration (`lampa-web`, `lampa-db`, `lampa-api`); builds both images locally |
| `devops/.env.example` | Template for the local `devops/.env` (gitignored) |
| `.github/workflows/tests.yaml` | Backend lint, tests and race plus a Compose config check on PRs and pushes to `svtlvtv` |
| `.github/workflows/deploy-docker.yaml` | Manual Svtlv deployment workflow |
| `backend/` | `lampa-api`, a fork-only Go service that stores user data in PostgreSQL; see [`backend/README.md`](backend/README.md) |
| `docs/` | Design documents and implementation plans for the backend |

Despite its name, `app.min.js` is readable compiled output. The web application
has no package manager, build command, test suite, or linter. Changes to the
application bundle or compiled CSS are made directly and verified in a browser.
The Go module under `backend/` is the exception: it has its own `Makefile`, tests
and linter, documented in [`backend/README.md`](backend/README.md).

## Run locally

### Static web server

Any static HTTP server can serve the repository root. For example, with Python:

```bash
python -m http.server 8080
```

Then open <http://localhost:8080>. Using an HTTP server is recommended instead of
opening `index.html` through a `file://` URL because the application loads scripts,
translations, and other resources dynamically.

### Docker

The Docker build requires the public domain or IP address that MSX will use. Pass
the host without a protocol and provide the protocol separately:

```bash
docker build \
  --build-arg domain=lampa.example.com \
  --build-arg prefix=https:// \
  -t lampa-web:local .

docker run --rm \
  --name lampa-web \
  -p 8092:80 \
  lampa-web:local
```

Open <http://localhost:8092>. During the image build, the `domain` and `prefix`
values replace the `{domain}` and `{PREFIX}` placeholders in `msx/start.json`.

### Docker Compose

`devops/docker-compose.yaml` defines three services: `lampa-web` (the Apache
image), `lampa-db` (PostgreSQL) and `lampa-api` (the Go persistence service from
`backend/`). It builds `lampa-web:dev` and `lampa-api:dev` locally and reads its
values from `devops/.env`, which is gitignored; `devops/.env.example` is the
template. Compose interpolates every service even when only one is started, so the
secrets must be set even for a web-only run; the placeholders from the template
are enough for that. The file also attaches `lampa-web` and `lampa-api` to the
external `svtlv_monitoring_external` network, which must exist:

```bash
cd devops
cp .env.example .env                               # adjust values, replace secrets
docker network create svtlv_monitoring_external    # once
docker compose up -d --build                       # or: up -d --build lampa-web
```

`.env.example` sets `COMPOSE_PROJECT_NAME=lampa`, so a local stack does not share
Svtlv's `devops` project. The API runs in the `Test` environment by default; see
"Configuration" and "Local run" in [`backend/README.md`](backend/README.md),
including the Development mode (a host `go run` against the published database).

The Compose services use these values:

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `LAMPA_DOMAIN` | Yes, to build `lampa-web` | None | MSX host without a protocol |
| `LAMPA_PREFIX` | No | `https://` | Protocol written to `msx/start.json` |
| `LAMPA_BIND_ADDRESS` | No | `0.0.0.0` | Published host interface for `lampa-web` |
| `LAMPA_PORT` | No | `8092` | Host port mapped to Apache port 80 |
| `LAMPA_ENVIRONMENT` | No | `Test` | `lampa-api` environment: `Development`, `Test` or `Production` |
| `LAMPA_API_PORT` | No | `5800` | Host port mapped to the API port 5800 |
| `LAMPA_DB_PORT` | No | `5434` | Loopback host port mapped to PostgreSQL 5432 |
| `LAMPA_DB_PASSWORD` | Yes | None | Password of the `lampa` database role (`openssl rand -hex 32`) |
| `LAMPA_API_DATA_KEY` | Yes | None | Base64 of 32 bytes that seal stored credentials (`openssl rand -base64 32`) |

The API health port 8081 is container-internal and never published. None of the
published ports collide with Svtlv's (`8080` is Keycloak, `5433` is Svtlv's
database).

Check or stop the local services with:

```bash
docker compose ps
docker compose logs --tail 100 lampa-web lampa-api
docker compose down
```

## Install in MSX

For a manual installation:

1. Host the repository contents on your own HTTPS web server.
2. Replace `{PREFIX}` and `{domain}` in `msx/start.json` with the public address.
3. Confirm that the descriptor is available at
   `https://lampa.example.com/msx/start.json`.
4. Add that descriptor URL in MSX and launch Lampa.

For example, the relevant values in the published descriptor should resolve to:

```json
{
  "parameter": "content:https://lampa.example.com/msx/start.json",
  "action": "link:https://lampa.example.com"
}
```

The Docker image performs this substitution automatically from its `domain` and
`prefix` build arguments.

## Manual deployment to the Svtlv server

The **Build and Deploy Lampa** GitHub Actions workflow runs only when an operator
starts it manually. Pushing or merging a commit does not deploy the application.
It must be started from the default branch (`svtlvtv`) and fails otherwise,
because the deploy pulls the `latest` images that only the default branch
publishes.

Deployments are serialized so that two workflow runs cannot update the server at
the same time.

The workflow has a single input:

| Input | Default | Description |
| --- | --- | --- |
| `environment` | `Production` | `lampa-api` environment written to the server `.env` as `LAMPA_ENVIRONMENT`: `Development`, `Test` or `Production`. Development is for a local `go run` only and cannot reach the database from a container |

The workflow performs the following operations:

1. Validates the branch and all required repository secrets, including the format
   of `LAMPA_DB_PASSWORD` and `LAMPA_API_DATA_KEY`.
2. Builds `ghcr.io/<owner>/lampa-web` and `ghcr.io/<owner>/lampa-api` and publishes
   them to GitHub Container Registry (GHCR) with the branch, `<branch>-<sha>` and
   `latest` tags.
3. Rewrites the Compose file for release: swaps `lampa-web:dev` / `lampa-api:dev`
   for the `latest` GHCR images and strips the `build:` blocks with `sed`, then
   fails if a build key or a `:dev` image survives or a section went missing.
4. Connects the GitHub-hosted runner to the server's tailnet and configures SSH.
5. Copies the Compose file and a generated `.env` to `DEPLOY_DIR`.
6. Logs the server in to GHCR, checks the Compose configuration, pulls the images,
   stops the running containers and starts `lampa-db`, `lampa-api` and `lampa-web`
   with `docker-compose`.
7. Waits up to 3 minutes for the `svtlvtv_lampa_api` healthcheck, prints its logs
   and fails the run when it does not become healthy, then prunes old images.

The workflow does not wait for the `lampa-web` healthcheck. It does not run the
backend tests either; the **Tests** workflow (`tests.yaml`) gates PRs and pushes to
`svtlvtv`, so merge only green changes before deploying.

The Compose layout the `sed` strip depends on is described in the header comment of
`devops/docker-compose.yaml`: each built service lists `image:` before its `build:`
block, only `context:` and `args:` sit between `build:` and `dockerfile:`, and no
other line, comments included, contains `build:`.

### Server prerequisites

The target server must have:

- Docker Engine;
- the `docker-compose` executable;
- an SSH user that can create `DEPLOY_DIR` and run Docker commands;
- Tailscale connectivity from the GitHub Actions runner to `SERVER_HOST`;
- the configured `LAMPA_PORT` available to bind, or a reverse proxy prepared to
  use that port.

Local Compose and automated deployments default to port `8092` because port
`8080` is already used by Keycloak on the Svtlv server. The optional
`LAMPA_PORT` secret overrides that default. The API is published on `5800`.

### Required GitHub Actions secrets

Configure these under **Repository settings → Secrets and variables → Actions →
Secrets**:

| Secret | Description |
| --- | --- |
| `DEPLOY_DIR` | Absolute deployment path, such as `/opt/svtlvtv/lampa-web` |
| `LAMPA_DOMAIN` | Public MSX host without a protocol |
| `TAILSCALE_AUTHKEY` | Auth key for runner tailnet access |
| `SERVER_HOST` | Server's Tailscale hostname or IP |
| `SSH_USER` | User that performs the remote deployment |
| `SSH_PRIVATE_KEY` | Private SSH key authorized for `SSH_USER` on the server |
| `GHCR_PAT` | GitHub personal access token with `read:packages` permission |
| `LAMPA_DB_PASSWORD` | 64 lowercase hex characters (`openssl rand -hex 32`) |
| `LAMPA_API_DATA_KEY` | Base64 of exactly 32 bytes (`openssl rand -base64 32`) |

**Back up `LAMPA_API_DATA_KEY` outside GitHub.** It seals every stored TorrServer,
Jackett and Prowlarr credential; losing or changing it makes them unreadable, and key
rotation is not implemented. `LAMPA_DB_PASSWORD` applies only when the
`lampa_db_data` volume is first initialized; see
[`backend/README.md`](backend/README.md) for rotating it.

`GITHUB_TOKEN` is supplied automatically by GitHub Actions and is used to check
out the repository and push the images produced by the workflow.

### Optional GitHub Actions secret

| Secret | Default | Description |
| --- | --- | --- |
| `LAMPA_PORT` | `8092` | Server port mapped to the container's port 80 |

### Optional GitHub Actions variables

Configure these under **Repository settings → Secrets and variables → Actions →
Variables** when the defaults are not suitable:

| Variable | Default | Description |
| --- | --- | --- |
| `LAMPA_PREFIX` | `https://` | Protocol written to the MSX descriptor |
| `LAMPA_BIND_ADDRESS` | `0.0.0.0` | Published server interface |

### Run a deployment

The workflow file must exist on the repository's default branch before GitHub shows
the **Run workflow** button. Once it is available:

1. Open the repository on GitHub.
2. Select **Actions → Build and Deploy Lampa**.
3. Select **Run workflow**.
4. Start the run and monitor every step until the health check passes.

No deployment occurs until an operator completes these steps.

The workflow creates the directory configured by `DEPLOY_DIR` automatically.
With `DEPLOY_DIR=/opt/svtlvtv/lampa-web`, a successful run produces:

```text
/opt/svtlvtv/lampa-web/
├── .env
└── docker-compose.yaml
```

The generated `.env` file is set to mode `600`. It records `LAMPA_ENVIRONMENT`,
the published ports and bind address, the database password and the data key;
SSH, Tailscale, and registry credentials are not written into it or into the
application images. It sets no `COMPOSE_PROJECT_NAME`, so the project name stays
the `DEPLOY_DIR` basename and container and volume names are stable across
deployments.

### Verify a deployment

On the server:

```bash
DEPLOY_DIR=/opt/svtlvtv/lampa-web
LAMPA_PORT=8092
cd "$DEPLOY_DIR"
docker-compose ps lampa-web
docker inspect --format '{{.State.Health.Status}}' svtlvtv_lampa_web
docker-compose logs --tail 100 lampa-web
curl --fail "http://127.0.0.1:${LAMPA_PORT}/"
```

If `LAMPA_PORT` or `LAMPA_BIND_ADDRESS` was changed, adjust the `curl` address
accordingly.

Also check the API and its database. The health port is not published on the
host, so query it from inside the container:

```bash
docker inspect --format '{{.State.Health.Status}}' svtlvtv_lampa_api svtlvtv_lampa_db
docker exec svtlvtv_lampa_api curl -s http://localhost:8081/health
curl -s -w ' %{http_code}\n' http://127.0.0.1:5800/api/v1/user-data
docker-compose logs lampa-api | grep -m1 Environment
```

The user-data request answers `401` until authentication is added in a later
plan. The startup log names the environment selected by the workflow input.

### Roll back

The workflow has no rollback inputs. To roll back, revert the offending commit on
`svtlvtv` and start the workflow again; it rebuilds and deploys `latest`. The
`lampa_db_data` volume survives redeployments.

### Troubleshooting

- **Missing required repository secret** — add the named secret and start the
  workflow again.
- **Tailscale ping fails** — confirm the auth key is valid and `SERVER_HOST` is
  reachable from the same tailnet.
- **SSH authentication fails** — verify `SSH_USER`, the private key, and the matching
  public key in the server user's `authorized_keys` file.
- **GHCR returns `denied` or `unauthorized`** — verify that `GHCR_PAT` belongs
  to a user with package access and includes `read:packages`.
- **Port is already allocated** — set `LAMPA_PORT` to an unused port or stop the
  conflicting service.
- **Container is unhealthy** — inspect
  `docker-compose logs --tail 100 lampa-web` and confirm that Apache can serve
  `/` inside the container.
- **`svtlvtv_lampa_api` is not healthy after 3 minutes** — inspect
  `docker-compose logs --tail 100 lampa-api lampa-db`; the usual causes are a
  wrong `LAMPA_DB_PASSWORD` for an already initialized database volume, a
  malformed `LAMPA_API_DATA_KEY`, or `environment: Development`, which points the
  API at `localhost` inside its container.
- **Deploy must run from the default branch** — start the workflow from
  `svtlvtv`; merge the change there first.
- **Stripped docker-compose check fails** — a Compose edit broke the layout rules
  in the header comment of `devops/docker-compose.yaml`; fix the layout.
- **Tests fail** — run `make lint`, `make test` and `make race` in `backend/` as
  described in [`backend/README.md`](backend/README.md).

## Working with this distribution repository

The upstream project publishes generated files into this repository. In particular:

- edit `app.min.js` directly for application changes;
- edit `css/app.css` directly for style changes;
- update both the inline `ru`/`en` dictionaries in `app.min.js` and the matching
  `lang/ru.js` or `lang/en.js` file when adding translations;
- keep changes small because upstream updates frequently replace the complete bundle;
- verify changes by serving the repository and exercising them in the target browser
  or television environment.

The stable plugin API is exposed through `window.Lampa`. Internal variable
suffixes inside the compiled bundle are build artifacts and should not be treated
as public API.

## Security

Lampa supports third-party plugins. Plugins execute code inside the application
and must be treated as untrusted unless you have reviewed their source. Install
plugins only from sources you trust and review the repository's
[security policy](SECURITY.md) before deploying the application for other users.

Never commit SSH keys, Tailscale auth keys, GitHub tokens, or generated deployment
environment files to this repository.

## License

This distribution is licensed under the [GNU General Public License version 2](LICENSE).
