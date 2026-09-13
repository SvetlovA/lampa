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
| `devops/docker-compose.yaml` | Local and server Compose configuration |
| `.github/workflows/deploy-docker.yaml` | Manual Svtlv deployment workflow |

Despite its name, `app.min.js` is readable compiled output. There is no package
manager, build command, test suite, or linter in this repository. Changes to the
application bundle or compiled CSS are made directly and verified in a browser.

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

From PowerShell:

```powershell
$env:LAMPA_DOMAIN = 'lampa.example.com'
$env:LAMPA_PREFIX = 'https://'
docker compose -f devops/docker-compose.yaml up -d --build
```

From Bash:

```bash
LAMPA_DOMAIN=lampa.example.com \
LAMPA_PREFIX=https:// \
docker compose -f devops/docker-compose.yaml up -d --build
```

The Compose service uses these values:

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `LAMPA_DOMAIN` | Yes | None | MSX host without a protocol |
| `LAMPA_PREFIX` | No | `https://` | Protocol written to `msx/start.json` |
| `LAMPA_IMAGE` | No | `lampa-web:local` | Image name used by Compose |
| `LAMPA_BIND_ADDRESS` | No | `0.0.0.0` | Published host interface |
| `LAMPA_PORT` | No | `8092` | Host port mapped to Apache port 80 |

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

1. Checks out the `svtlvtv` branch and validates all required repository secrets.
2. Builds the Apache image and publishes it to GitHub Container Registry (GHCR).
3. Tags the image with the deployed commit's full SHA and the moving `svtlvtv` tag.
4. Connects the GitHub-hosted runner to the server's tailnet.
5. Configures SSH using the deployment key.
6. Copies the Compose file and generated environment file to `DEPLOY_DIR`.
7. Logs the server in to GHCR and pulls the immutable SHA-tagged image.
8. Pulls and recreates the Lampa container with `docker-compose`.

Container health is tracked by the healthcheck in `devops/docker-compose.yaml`.
The workflow does not wait for that healthcheck after starting the container.

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
tag and the resolved Compose configuration; SSH, Tailscale, and registry
credentials are not written into the application image.

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

### Roll back

Every deployment publishes an immutable `sha-<commit>` image tag. To restore an
earlier build, edit `LAMPA_IMAGE` in `$DEPLOY_DIR/.env` to the previous SHA tag
and recreate the service. For example:

```bash
DEPLOY_DIR=/opt/svtlvtv/lampa-web
cd "$DEPLOY_DIR"
docker-compose pull lampa-web
docker-compose up -d --no-build lampa-web
docker-compose ps lampa-web
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
