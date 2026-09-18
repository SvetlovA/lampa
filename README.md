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
| `devops/docker-compose.yaml` | Local and server Compose configuration (`lampa-web`, `lampa-db`, `lampa-api`) |
| `devops/docker-compose.local.yaml` | Local-only override that builds `lampa-api` from `backend/` |
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
`backend/`). Compose interpolates every service even when only one is started, so
the database and API variables below must be set as well; the values are unused
when only `lampa-web` is started. The file also attaches `lampa-web` and
`lampa-api` to the external `svtlv_monitoring_external` network, which must exist:

```bash
docker network create svtlv_monitoring_external   # once
```

To run only the web application, from PowerShell:

```powershell
$env:LAMPA_DOMAIN = 'lampa.example.com'
$env:LAMPA_PREFIX = 'https://'
$env:LAMPA_API_IMAGE = 'unused'
$env:LAMPA_DB_PASSWORD = 'unused'
$env:LAMPA_API_DATA_KEY = 'unused'
docker compose -f devops/docker-compose.yaml up -d --build lampa-web
```

From Bash:

```bash
LAMPA_DOMAIN=lampa.example.com \
LAMPA_PREFIX=https:// \
LAMPA_API_IMAGE=unused LAMPA_DB_PASSWORD=unused LAMPA_API_DATA_KEY=unused \
docker compose -f devops/docker-compose.yaml up -d --build lampa-web
```

To run the API and its database locally, see "Local run" in
[`backend/README.md`](backend/README.md).

The Compose services use these values:

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `LAMPA_DOMAIN` | Yes | None | MSX host without a protocol |
| `LAMPA_PREFIX` | No | `https://` | Protocol written to `msx/start.json` |
| `LAMPA_IMAGE` | No | `lampa-web:local` | Image name used by Compose |
| `LAMPA_BIND_ADDRESS` | No | `0.0.0.0` | Published host interface |
| `LAMPA_PORT` | No | `8092` | Host port mapped to Apache port 80 |
| `LAMPA_API_IMAGE` | Yes | None | `lampa-api` image; `lampa-api:local` with the local override |
| `LAMPA_DB_PASSWORD` | Yes | None | Password of the `lampa` database role (`openssl rand -hex 32`) |
| `LAMPA_API_DATA_KEY` | Yes | None | Base64 of 32 bytes that seal stored credentials (`openssl rand -base64 32`) |

Check or stop the local service with:

```bash
docker compose -f devops/docker-compose.yaml ps
docker compose -f devops/docker-compose.yaml logs --tail 100 lampa-web
docker compose -f devops/docker-compose.yaml down
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
The workflow always checks out the latest `svtlvtv` branch before building, even
if the workflow was started from another branch in the Actions interface.

Deployments are serialized so that two workflow runs cannot update the server at
the same time.

The workflow performs the following operations:

1. Checks out the `svtlvtv` branch and validates the inputs and all required
   repository secrets.
2. Unless `api_image_tag` is set or `api_enabled` is `false`, runs the backend lint,
   test and race checks (they need Docker, and the database tests fail instead of
   skipping).
3. Builds the Apache image and, unless `api_image_tag` is set or `api_enabled` is
   `false`, the `lampa-api` image, and publishes them to GitHub Container Registry
   (GHCR).
4. Tags each image with the deployed commit's full SHA. The moving `svtlvtv` tag is
   moved only when `deploy` is `true`.
5. Connects the GitHub-hosted runner to the server's tailnet.
6. Configures SSH using the deployment key.
7. Copies the Compose file and generated environment file to `DEPLOY_DIR`.
8. Logs the server in to GHCR and pulls the immutable SHA-tagged images.
9. Recreates `lampa-db`, `lampa-api` and `lampa-web` with `docker-compose`, or with
   `api_enabled: false` removes `lampa-api` and `lampa-db` (the `lampa_db_data`
   volume is kept) and recreates only `lampa-web`.

With `api_enabled: true` the workflow waits up to 3 minutes for the
`svtlvtv_lampa_api` healthcheck and fails the run when it does not become healthy.
It does not wait for the `lampa-web` healthcheck.

The workflow inputs are:

| Input | Default | Description |
| --- | --- | --- |
| `deploy` | `true` | `false` = run checks and build images only; nothing is deployed and the `svtlvtv` tags do not move |
| `web_image_tag` | empty | Deploy this existing `sha-<40 hex>` web image instead of building one |
| `api_image_tag` | empty | Deploy this existing `sha-<40 hex>` API image instead of building one; skips the backend checks |
| `api_enabled` | `true` | `false` = skip backend checks and the API build, remove `lampa-api` and `lampa-db`, deploy `lampa-web` only |

The deploy job runs only after backend checks, image builds and the configuration
checks succeeded. Run the workflow from a state where the backend changes are
already merged into `svtlvtv`, because that is the branch it checks out.

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
`LAMPA_PORT` secret overrides that default.

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
| `LAMPA_DB_PASSWORD` | Required when deploying with `api_enabled`: 64 lowercase hex characters (`openssl rand -hex 32`) |
| `LAMPA_API_DATA_KEY` | Required when deploying with `api_enabled`: base64 of exactly 32 bytes (`openssl rand -base64 32`) |

**Back up `LAMPA_API_DATA_KEY` outside GitHub.** It seals every stored TorrServer
and Jackett credential; losing or changing it makes them unreadable, and key
rotation is not implemented. `LAMPA_DB_PASSWORD` applies only when the
`lampa_db_data` volume is first initialized; see
[`backend/README.md`](backend/README.md) for rotating it.

`GITHUB_TOKEN` is supplied automatically by GitHub Actions and is used to check
out the repository and push the image produced by the workflow.

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

The generated `.env` file is set to mode `600`. It records the immutable image
tags, the database password, the data key and the resolved Compose configuration;
SSH, Tailscale, and registry credentials are not written into the application
image. With `api_enabled: false` the database password and data key are written as
the placeholder `api-disabled` unless the secrets are set.

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

With `api_enabled: true`, also check the API and its database. The API and health
ports are not published on the host, so query them from inside the container:

```bash
docker inspect --format '{{.State.Health.Status}}' svtlvtv_lampa_api svtlvtv_lampa_db
docker exec svtlvtv_lampa_api curl -s http://localhost:8081/health
docker exec svtlvtv_lampa_api curl -s -w ' %{http_code}\n' http://localhost:8080/api/v1/user-data
```

The last request answers `401` until authentication is added in a later plan.

### Roll back

Every deployment publishes immutable `sha-<commit>` image tags for both images. The
simplest rollback is to start the workflow with `web_image_tag` and/or
`api_image_tag` set to the previous SHA tags; the images are verified to exist
before anything is deployed. To disable the API without touching the web
application, start the workflow with `api_enabled: false`.

To restore an earlier build by hand, edit `LAMPA_IMAGE` (and `LAMPA_API_IMAGE`) in
`$DEPLOY_DIR/.env` to the previous SHA tag and recreate the services. For example:

```bash
DEPLOY_DIR=/opt/svtlvtv/lampa-web
cd "$DEPLOY_DIR"
docker-compose pull lampa-web lampa-api
docker-compose up -d --no-build lampa-db lampa-api lampa-web
docker-compose ps
```

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
  wrong `LAMPA_DB_PASSWORD` for an already initialized database volume or a
  malformed `LAMPA_API_DATA_KEY`. To bring the site back while investigating, start
  the workflow with `api_enabled: false`.
- **Backend checks fail** — run `make lint`, `make test` and `make race` in
  `backend/` as described in [`backend/README.md`](backend/README.md). A failed
  check blocks the deployment; start the workflow with `api_image_tag` set to an
  existing image or `api_enabled: false` to deploy without them.

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
