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

## Deployment (binary + systemd)

The recommended way to run gitops-compose is as a plain binary under systemd.
No container overhead, no socket-in-socket complexity.

### 1 — Install the binary

Download the latest release binary for your architecture and place it on the
host:

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

### 2 — Create a dedicated user

```bash
useradd --system --no-create-home --shell /usr/sbin/nologin gitops
usermod -aG docker gitops   # grant Docker socket access
```

### 3 — Lay out the directories

`/opt` is root-owned, which is fine — the agent only needs ownership of its
own subdirectories, not of `/opt` itself.

```bash
# Create directories as root (normal for /opt)
mkdir -p /opt/gitops/ssh /opt/deployments

# Copy your SSH key pair (generated earlier)
cp id_ed25519  /opt/gitops/ssh/id_ed25519
cp known_hosts /opt/gitops/ssh/known_hosts

# The key must be readable only by the gitops user
chmod 700 /opt/gitops/ssh
chmod 600 /opt/gitops/ssh/id_ed25519
chmod 644 /opt/gitops/ssh/known_hosts
chown -R gitops:gitops /opt/gitops

# Clone as root using the key we just placed, then hand ownership to gitops.
# The agent runs git pull on /opt/deployments, so it must own the tree.
GIT_SSH_COMMAND="ssh -i /opt/gitops/ssh/id_ed25519 -o IdentitiesOnly=yes \
  -o UserKnownHostsFile=/opt/gitops/ssh/known_hosts" \
  git clone git@ssh.dev.azure.com:v3/ORG/PROJECT/REPOSITORY /opt/deployments
chown -R gitops:gitops /opt/deployments
```

### 4 — Create the environment file

```bash
cat > /etc/gitops-compose.env <<'EOF'
REPOSITORY_PATH=/opt/deployments
REPOSITORY_BRANCH=main
SSH_KEY_PATH=/opt/gitops/ssh/id_ed25519
SSH_KNOWN_HOSTS_PATH=/opt/gitops/ssh/known_hosts
CHECK_INTERVAL_IN_SECONDS=30
DOCKER_REGISTRIES=[{"url":"registry.example.com","username":"robot","password":"secret"}]
LOG_FORMAT=json
EOF
chmod 600 /etc/gitops-compose.env
chown gitops:gitops /etc/gitops-compose.env
```

### 5 — Create the systemd unit

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
# /opt/deployments must be writable — git pull writes to the working tree
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
