# GitopsCompose

GitopsCompose is a GitOps continuous delivery tool for single-node Docker Compose deployments.

It polls a Git repository (including **Azure DevOps over SSH**), detects which
deployment directories changed, and runs `docker compose up` for those
deployments only.

Run `gitops-compose --help` for flags and environment variables.

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

1. On start and on every poll interval, `git fetch` is run against the configured remote.
2. If the remote branch is ahead of local HEAD, the set of changed file paths is computed.
3. Each changed path is mapped to the nearest ancestor directory that contains a `compose.yaml` or `docker-compose.yml` file.
4. Only the affected deployments are reconciled — a change to `beta/payments/.env` will not cause `beta/frontend` to be restarted. Nested files such as `beta/payments/config/app.conf` still map to `beta/payments`.
5. `git pull` is run, then `docker compose up` is called for each affected deployment.
6. If a deployment fails after the pull, it is retried on the next poll even if there are no new Git commits.

> GitopsCompose exits early when the local repository is dirty. When reconciliation begins, errors are tracked per deployment but all deployments continue to be processed.

---

## Repository layout

The Git repository contains one or more independent Compose deployments organised in subdirectories. Any directory structure is supported; a "deployment" is simply a directory that contains a `compose.yaml` or `docker-compose.yml` file.

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

The CD pipeline builds and pushes a Docker image, then commits an image-tag update to the appropriate `.env`:

```diff
-PAYMENTS_IMAGE=registry.example.com/payments:1.42.7
+PAYMENTS_IMAGE=registry.example.com/payments:1.42.8
```

GitopsCompose detects the changed file (`beta/payments/.env`), maps it to the `beta/payments` deployment, and runs:

```bash
cd /opt/deployments/beta/payments
docker compose -f compose.yaml up -d
```

Docker Compose then determines which containers need to be recreated.

### Compose file names

Both `compose.yaml` and `docker-compose.yml` are supported. When both exist in the same directory, `compose.yaml` is preferred.

---

## Setup

Setup has two phases:

- **Phase A** — Azure DevOps account configuration (done once, from any machine)
- **Phase B** — Host installation (done on the server that will run the agent)

---

### Phase A — Azure DevOps

#### A1 — Generate an SSH key pair

Do **not** set a passphrase — the agent runs unattended.

**Azure DevOps Server (on-premises)** only accepts RSA keys:

```bash
ssh-keygen -t rsa -b 4096 -C "gitops-compose" -f ./id_rsa -N ""
```

**Azure DevOps Services (cloud)** also supports Ed25519:

```bash
ssh-keygen -t ed25519 -C "gitops-compose" -f ./id_ed25519 -N ""
```

The rest of this guide uses `id_rsa` / `id_rsa.pub`. Substitute `id_ed25519` / `id_ed25519.pub` if you are on the cloud service.

The two generated files:

```text
id_rsa      ← private key  (copy to the host in phase B)
id_rsa.pub  ← public key   (add to Azure DevOps below)
```

#### A2 — Add the public key to Azure DevOps

1. Open **User settings → SSH public keys**:
   `https://dev.azure.com/<ORG>/_usersSettings/keys`
2. Click **New Key**, paste the contents of `id_ed25519.pub`, and save.

#### A3 — Note the SSH clone URL

In Azure DevOps: open the repository → **Clone** → **SSH**. The URL looks like:

```text
git@ssh.dev.azure.com:v3/ORG/PROJECT/REPOSITORY
```

---

### Phase B — Host installation

All commands below run as **root** on the target host unless stated otherwise.

#### B1 — Install the binary

```bash
# linux/amd64
curl -fsSL https://github.com/zwsq/gitops-compose/releases/latest/download/gitops-compose-linux-amd64 \
  -o /usr/local/bin/gitops-compose
chmod +x /usr/local/bin/gitops-compose
```

```bash
# linux/arm64
curl -fsSL https://github.com/zwsq/gitops-compose/releases/latest/download/gitops-compose-linux-arm64 \
  -o /usr/local/bin/gitops-compose
chmod +x /usr/local/bin/gitops-compose
```

#### B2 — Create a dedicated system user

```bash
useradd --system --no-create-home --shell /usr/sbin/nologin gitops
usermod -aG docker gitops   # grant Docker socket access
```

#### B3 — Place the SSH keys

`/opt` is root-owned — that is normal. The agent only needs ownership of its own subdirectories.

```bash
mkdir -p /opt/gitops/ssh

# Copy the files generated in phase A
cp id_rsa     /opt/gitops/ssh/id_rsa
cp id_rsa.pub /opt/gitops/ssh/id_rsa.pub

# Fetch the host key and verify the fingerprint against:
# https://learn.microsoft.com/en-us/azure/devops/repos/git/use-ssh-keys-to-authenticate
ssh-keyscan -p 22 ssh.dev.azure.com > /opt/gitops/ssh/known_hosts

# Restrict permissions — ssh refuses keys that are group/world readable
chmod 700 /opt/gitops/ssh
chmod 600 /opt/gitops/ssh/id_rsa
chmod 644 /opt/gitops/ssh/id_rsa.pub /opt/gitops/ssh/known_hosts
chown -R gitops:gitops /opt/gitops
```

#### B4 — Clone the deployment repository

The agent expects the repository to already exist at `REPOSITORY_PATH`. Clone it once, then transfer ownership to the `gitops` user — the agent runs `git pull` here and needs write access to the working tree.

```bash
GIT_SSH_COMMAND="ssh -i /opt/gitops/ssh/id_rsa -o IdentitiesOnly=yes \
  -o UserKnownHostsFile=/opt/gitops/ssh/known_hosts" \
  git clone git@ssh.dev.azure.com:v3/ORG/PROJECT/REPOSITORY /opt/deployments

chown -R gitops:gitops /opt/deployments
```

#### B5 — Create the environment file

```bash
cat > /etc/gitops-compose.env <<'EOF'
REPOSITORY_PATH=/opt/deployments
REPOSITORY_BRANCH=main
SSH_KEY_PATH=/opt/gitops/ssh/id_rsa
SSH_KNOWN_HOSTS_PATH=/opt/gitops/ssh/known_hosts
CHECK_INTERVAL_IN_SECONDS=30
DOCKER_REGISTRIES=[{"url":"registry.example.com","username":"robot","password":"secret"}]
LOG_FORMAT=json
EOF

chmod 600 /etc/gitops-compose.env     # contains registry credentials
chown gitops:gitops /etc/gitops-compose.env
```

#### B6 — Create the systemd unit

```ini
# /etc/systemd/system/gitops-compose.service
[Unit]
Description=GitOps Compose agent
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
Type=simple
User=gitops
Group=gitops
EnvironmentFile=/etc/gitops-compose.env
ExecStart=/usr/local/bin/gitops-compose
Restart=on-failure
RestartSec=10s
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
# git pull writes to the working tree, so this path must be writable
ReadWritePaths=/opt/deployments

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now gitops-compose
journalctl -u gitops-compose -f
```

---

## Environment variables

| Variable                    | Default | Required | Description |
| --------------------------- | ------- | -------- | ----------- |
| `REPOSITORY_PATH`           |         | **yes**  | Absolute path to the cloned Git repository |
| `REPOSITORY_BRANCH`         | `main`  | no       | Branch to track |
| `DEPLOYMENTS_PATH`          |         | no       | Subdirectory to watch recursively; files outside this path are ignored |
| `SSH_KEY_PATH`              |         | no       | Path to the SSH private key (enables SSH auth) |
| `SSH_KNOWN_HOSTS_PATH`      |         | no       | Path to a known_hosts file |
| `CHECK_INTERVAL_IN_SECONDS` | `300`   | no       | Polling interval in seconds; `-1` disables polling |
| `DOCKER_REGISTRIES`         | `[]`    | no       | JSON array: `[{"url":"…","username":"…","password":"…"}]` |
| `WEBHOOK_ENABLED`           | `true`  | no       | Enables the `/webhook` endpoint |
| `METRICS_ENABLED`           | `true`  | no       | Enables the `/metrics` endpoint |
| `LOG_FORMAT`                | `text`  | no       | `text` (logfmt), `json`, or `console` |
| `LOG_LEVEL`                 | `info`  | no       | `debug`, `info`, `warn`, or `error` |

When `SSH_KEY_PATH` is set, SSH auth is used and any HTTP credentials embedded in the remote URL are ignored. The private key is passed to the `ssh` binary via `GIT_SSH_COMMAND` and is **never logged**.

SSH host key verification is always enabled. `StrictHostKeyChecking=no` is intentionally not set.

---

## Polling and retry behaviour

- GitopsCompose polls Git on a fixed interval (`CHECK_INTERVAL_IN_SECONDS`).
- On each poll, only the deployments whose files changed since the last successful sync are reconciled.
- When `DEPLOYMENTS_PATH` is set, only that subtree is watched. Commits that only touch other directories still fast-forward the clone, but they do not reconcile any stack.
- Git is pulled before compose is applied so the working tree matches remote. If apply fails, that deployment is retried on the next poll even if there are no new Git commits.
- Image pull failures are retried on each subsequent poll until the pull succeeds or a new Git change is applied.
- The `/webhook` endpoint (`POST /webhook`) triggers an immediate check without waiting for the next interval.

---

## Command line

```bash
gitops-compose --help
gitops-compose --version
```

Missing or invalid configuration prints the error and the same usage text, then exits with status 1 (no panic). Unknown flags exit with status 2.

---

## Labels

Compose labels are set at the service level and affect the whole stack.

| Label               | Default | Description |
| ------------------- | ------- | ----------- |
| `gitops.controller` | `false` | Marks this stack as the gitops-compose controller itself |
| `gitops.ignore`     | `false` | Skips this stack entirely |

---

## Watch additional files

To detect changes in files that are not loaded by Docker Compose automatically, use the `x-gitops` extension:

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

## Limitations

- The repository must be cloned manually before starting gitops-compose (no auto-clone on first run).
- HTTP basic-auth credentials embedded in the remote URL are supported for backward-compatibility but SSH is preferred.
- Rolling updates: images are pulled before `docker compose up`. If the pull fails, containers remain running on the previous image and the deployment is retried on the next poll.
- Errors during removal of a stack (e.g. the compose file was deleted) may leave containers running if `docker compose down` fails.

---

## Credits

- [https://github.com/KorbinianKuhn/gitops-compose](https://github.com/KorbinianKuhn/gitops-compose) — upstream project
- [https://github.com/kimdre/doco-cd](https://github.com/kimdre/doco-cd) — original inspiration
- [https://github.com/go-git/go-git](https://github.com/go-git/go-git)
- [https://github.com/docker](https://github.com/docker)
- [https://github.com/compose-spec/compose-go](https://github.com/compose-spec/compose-go)
