# GitopsCompose

GitopsCompose is a GitOps continuous delivery tool for single-node Docker Compose deployments.

It polls a Git repository (including **Azure DevOps over SSH**), detects which
deployment directories changed, and runs `docker compose up -d` for those
deployments only.

```text
Azure DevOps Git (or any Git remote)
        |
        | SSH (or HTTPS)
        v
gitops-compose
        |
        | Docker socket
        v
  Docker Engine
        |
        +-- Compose application A
        +-- Compose application B
        +-- Compose application C
```

---

## How it works

1. On start and on every poll interval, `git fetch` is run against the
   configured remote.
2. If the remote branch is ahead of local HEAD, the set of changed file paths
   is computed.
3. Each changed path is mapped to its deployment directory (the directory that
   contains the `compose.yaml` or `docker-compose.yml` file).
4. Only the affected deployments are reconciled — a change to
   `beta/payments/.env` will not cause `beta/frontend` to be restarted.
5. `git pull` is run, then `docker compose up -d` is called for each affected
   deployment.
6. If a deployment fails, the local Git revision is **not** advanced for that
   deployment.  The next polling cycle will retry it automatically.

> GitopsCompose exits early when the local repository is dirty.  When
> reconciliation begins, errors are tracked per deployment but all deployments
> continue to be processed (a failed stop does not block other updates).

---

## Repository layout

The Git repository contains one or more independent Compose deployments
organised in subdirectories.  Any directory structure is supported; a
"deployment" is simply a directory that contains a `compose.yaml` or
`docker-compose.yml` file.

```text
deployments/
├── beta/
│   ├── payments/
│   │   ├── compose.yaml
│   │   └── .env
│   ├── frontend/
│   │   ├── compose.yaml
│   │   └── .env
│   └── reporting/
│       ├── compose.yaml
│       └── .env
└── production/
    ├── payments/
    ├── frontend/
    └── reporting/
```

The CD pipeline builds and pushes a Docker image, then commits an image-tag
update to the appropriate `.env`:

```diff
-PAYMENTS_IMAGE=registry.example.com/payments:1.42.7
+PAYMENTS_IMAGE=registry.example.com/payments:1.42.8
```

GitopsCompose detects the changed file (`beta/payments/.env`), maps it to the
`beta/payments` deployment, and runs:

```bash
cd /deployments/beta/payments
docker compose -f compose.yaml up -d
```

Docker Compose then determines which containers need to be recreated.

### Compose file names

Both `compose.yaml` and `docker-compose.yml` are supported.  When both exist
in the same directory, `compose.yaml` is preferred.

---

## Azure DevOps SSH setup

### 1 — Generate an SSH key pair

Generate a dedicated Ed25519 key for the GitOps agent.  Do **not** set a
passphrase (the agent runs unattended).

```bash
ssh-keygen -t ed25519 -C "gitops-compose" -f ./id_ed25519 -N ""
```

This produces:

```text
id_ed25519      ← private key  (keep secret, mount into the container)
id_ed25519.pub  ← public key   (add to Azure DevOps)
```

### 2 — Add the public key to Azure DevOps

1. Open **User settings → SSH public keys** in your Azure DevOps organisation
   (`https://dev.azure.com/<ORG>/_usersSettings/keys`).
2. Click **New Key**, paste the content of `id_ed25519.pub`, and save.

### 3 — Build a known_hosts file

Fetch the Azure DevOps SSH host key and save it to a `known_hosts` file:

```bash
ssh-keyscan -p 22 ssh.dev.azure.com >> ./known_hosts
```

Verify the fingerprint matches the [published Azure DevOps SSH fingerprints](https://learn.microsoft.com/en-us/azure/devops/repos/git/use-ssh-keys-to-authenticate)
before trusting it.

### 4 — Find your SSH clone URL

In Azure DevOps, open the repository → **Clone** → **SSH**.  The URL looks
like:

```text
git@ssh.dev.azure.com:v3/ORG/PROJECT/REPOSITORY
```

### 5 — Clone the repository manually (first run)

GitopsCompose expects the repository to already exist at `REPOSITORY_PATH`.
Clone it once using the same key:

```bash
GIT_SSH_COMMAND="ssh -i /opt/ssh/id_ed25519 -o IdentitiesOnly=yes \
  -o UserKnownHostsFile=/opt/ssh/known_hosts" \
  git clone git@ssh.dev.azure.com:v3/ORG/PROJECT/REPOSITORY /opt/deployments
```

### 6 — Store the key files securely

Place the key files in a directory that will be bind-mounted read-only into the
container:

```text
/opt/gitops-compose/ssh/
├── id_ed25519     (chmod 600)
└── known_hosts
```

```bash
chmod 600 /opt/gitops-compose/ssh/id_ed25519
```

---

## Docker deployment example

```yaml
# /opt/gitops-compose/docker-compose.yml
services:
  gitops-compose:
    image: ghcr.io/zwsq/gitops-compose:latest
    container_name: gitops-compose
    restart: unless-stopped
    ports:
      - "127.0.0.1:2112:2112"
    user: "${UID}:${GID}"
    group_add:
      - "${GID_DOCKER}"
    environment:
      REPOSITORY_PATH: /deployments
      REPOSITORY_BRANCH: beta
      SSH_KEY_PATH: /ssh/id_ed25519
      SSH_KNOWN_HOSTS_PATH: /ssh/known_hosts
      CHECK_INTERVAL_IN_SECONDS: 30
      DOCKER_REGISTRIES: '[{"url":"registry.example.com","username":"robot","password":"secret"}]'
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /opt/deployments:/deployments
      - /opt/gitops-compose/ssh:/ssh:ro
```

> **Docker socket permission note** — the container user needs access to
> `/var/run/docker.sock`.  The standard approach is to add the user to the
> `docker` group (`group_add: ["${GID_DOCKER}"]`).  On hosts where the socket
> is owned by a different GID, adjust accordingly.  Running the container as
> root is not required and not recommended.

---

## Environment variables

| Variable                  | Default | Required | Description |
| ------------------------- | ------- | -------- | ----------- |
| `REPOSITORY_PATH`         |         | **yes**  | Absolute path to the cloned Git repository |
| `REPOSITORY_BRANCH`       | `main`  | no       | Branch to track |
| `SSH_KEY_PATH`            |         | no       | Path to the SSH private key file (enables SSH auth) |
| `SSH_KNOWN_HOSTS_PATH`    |         | no       | Path to a known_hosts file (recommended with SSH) |
| `CHECK_INTERVAL_IN_SECONDS` | `300` | no       | Polling interval in seconds; `-1` disables polling |
| `DOCKER_REGISTRIES`       | `[]`    | no       | JSON array: `[{"url":"…","username":"…","password":"…"}]` |
| `WEBHOOK_ENABLED`         | `true`  | no       | Enables the `/webhook` endpoint |
| `METRICS_ENABLED`         | `true`  | no       | Enables the `/metrics` endpoint |
| `LOG_FORMAT`              | `text`  | no       | `text` (logfmt), `json`, or `console` |
| `LOG_LEVEL`               | `info`  | no       | `debug`, `info`, `warn`, or `error` |
| `IS_RUNNING_IN_DOCKER`    | `false` | no       | Set automatically in the official image |

When `SSH_KEY_PATH` is set, SSH auth is used and any HTTP credentials embedded
in the remote URL are ignored.  The private key is passed to the `ssh` binary
via `GIT_SSH_COMMAND` and is **never logged**.

SSH host key verification is always enabled.  `StrictHostKeyChecking=no` is
intentionally not set.

---

## Polling and retry behaviour

- GitopsCompose polls Git on a fixed interval (`CHECK_INTERVAL_IN_SECONDS`).
- On each poll, only the deployments whose files changed since the last
  successful sync are reconciled.
- If `docker compose up` fails for a deployment, the local Git HEAD is **not**
  advanced.  The next poll cycle will retry the deployment automatically.
- Image pull failures are retried independently on each subsequent poll cycle
  until a new Git change is detected.
- The `/webhook` endpoint (`POST /webhook`) triggers an immediate check without
  waiting for the next interval.

---

## Labels

Compose labels are set at the service level and affect the whole stack.

| Label               | Default | Description |
| ------------------- | ------- | ----------- |
| `gitops.controller` | `false` | Marks this stack as the gitops-compose controller itself |
| `gitops.ignore`     | `false` | Skips this stack entirely |

---

## Watch additional files

To detect changes in files that are not loaded by Docker Compose automatically,
use the `x-gitops` extension:

```yaml
# Root-level watch
x-gitops:
  watch:
    - config.yaml

services:
  nginx:
    image: nginx
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf
    # Service-level watch
    x-gitops:
      watch:
        - ./nginx.conf
```

---

## HTTP server

GitopsCompose starts an HTTP server on `:2112`:

| Path       | Description |
| ---------- | ----------- |
| `/health`  | Returns `200 OK` — use for liveness probes |
| `/metrics` | Prometheus metrics (disable with `METRICS_ENABLED=false`) |
| `/webhook` | `POST` triggers an immediate GitOps check |

Add a reverse proxy with authentication if this port is internet-accessible.

---

## Monitoring

Prometheus metrics are exported at `http://localhost:2112/metrics`:

```
# HELP gitops_check_total Total number of GitOps checks by status
gitops_check_total{status="success"} 12
gitops_check_total{status="error"} 0

# HELP gitops_deployments_active_total Number of active deployments by status
gitops_deployments_active_total{status="running"} 4
gitops_deployments_active_total{status="failed"} 0
gitops_deployments_active_total{status="invalid"} 0
gitops_deployments_active_total{status="ignored"} 1

# HELP gitops_deployments_operations_total Total number of deployment operations
gitops_deployments_operations_total{operation="started"} 4
gitops_deployments_operations_total{operation="updated"} 2
gitops_deployments_operations_total{operation="stopped"} 0
gitops_deployments_operations_total{operation="failed"} 0
```

A prebuilt Grafana dashboard is available at [dashboard.json](dashboard.json).

![Grafana dashboard screenshot](dashboard.png)

---

## Container image

Images are published to the GitHub Container Registry:

```text
ghcr.io/zwsq/gitops-compose:latest      ← latest main build
ghcr.io/zwsq/gitops-compose:v1.2.3      ← specific release
ghcr.io/zwsq/gitops-compose:sha-abc1234  ← immutable SHA tag
```

Multi-architecture manifest covers `linux/amd64` and `linux/arm64`.

---

## Limitations

- The repository must be cloned manually before starting gitops-compose (no
  auto-clone on first run).
- HTTP basic-auth credentials embedded in the remote URL are supported for
  backward-compatibility but SSH is preferred.
- Rolling updates: images are pulled before containers are stopped.  If the
  pull fails the containers remain running on the previous image and the
  deployment is retried on the next poll.
- Errors during removal of a stack (e.g. the compose file was deleted) may
  leave containers running if `docker compose down` fails.

---

## Credits

- [https://github.com/KorbinianKuhn/gitops-compose](https://github.com/KorbinianKuhn/gitops-compose) — upstream project
- [https://github.com/kimdre/doco-cd](https://github.com/kimdre/doco-cd) — original inspiration
- [https://github.com/go-git/go-git](https://github.com/go-git/go-git)
- [https://github.com/docker](https://github.com/docker)
- [https://github.com/compose-spec/compose-go](https://github.com/compose-spec/compose-go)
