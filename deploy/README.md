# Deploy with Docker Compose

## 1. Start

Docker Compose is the supported installation. Use Docker Engine + Compose on
Linux or Docker Desktop on macOS / Windows (Linux containers).
The `qoder-data` volume stores SQLite and account credentials. Source runs are
for development and do not support managed updates.

From the repository root, use:

macOS / Linux:

```bash
./scripts/start.sh
```

Windows PowerShell:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\start.ps1
```

Both launchers create `deploy/.env` if needed, pull the published image, fall
back to a local build when necessary, and wait for `/health`.

To run Compose directly, create `deploy/.env` first (an empty file is enough
for defaults; do not overwrite an existing file):

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
```

Add `--build` to build from the checked-out source. Only `127.0.0.1:3010` is
published.

Save the administrator key printed once in the first-start logs:

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml logs qoder-api-proxy
```

Open `http://127.0.0.1:3010`, sign in with that key, and add an account.
Create a separate client key in **API keys** for applications; it only grants
access to `/v1/*` and can be restricted by provider and region.

## 2. Add accounts

Use **Accounts** to add an account with its supported login method:

| Provider | Region | Login / import |
|----------|--------|----------------|
| Qoder | Global / CN | Browser OAuth, PAT, `qoder-native-v1` |
| WorkBuddy | Global / CN | Browser OAuth, `workbuddy-oauth-v1` |
| Trae Work | CN | Browser OAuth, `trae-oauth-v1` |
| Devin (experimental) | Global | Browser OAuth, `devin-session-v1` |

Basic chat, streaming, tool calls and quota refresh have been verified with CN
accounts for Qoder, WorkBuddy and Trae. Global accounts, interactive login,
natural credential expiry and some campaign branches still need acceptance.
Devin is experimental. Qoder login, catalog and quota use one isolated Node
worker per account; its chat path and other providers use Go in-process clients.
All durable credentials stay in SQLite.

WorkBuddy daily check-in and token keepalive are per-account opt-in and disabled
by default. Enable them only if you want those automatic account operations.

## 3. Connect a client

Use `http://127.0.0.1:3010/v1` on the host. For another container on the same
Docker network:

```text
base_url = http://qoder-api-proxy:3010/v1
api_key  = <client key created in the console>
```

Get a model ID from **Access** or `GET /v1/models`, then test a request:

```bash
export CLI2API_API_KEY='paste-your-client-key'

curl http://127.0.0.1:3010/v1/chat/completions \
  -H "Authorization: Bearer $CLI2API_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<model-id-from-v1-models>",
    "messages": [{"role": "user", "content": "Reply with OK only"}],
    "stream": false
  }'
```

PowerShell equivalent:

```powershell
$env:CLI2API_API_KEY = "paste-your-client-key"
$Headers = @{ Authorization = "Bearer $env:CLI2API_API_KEY" }
$Body = @{
  model = "<model-id-from-v1-models>"
  messages = @(@{ role = "user"; content = "Reply with OK only" })
  stream = $false
} | ConvertTo-Json -Depth 4
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:3010/v1/chat/completions" -Headers $Headers -ContentType "application/json" -Body $Body
```

Pin a request to a specific account with the `X-Qoder-Account: acc_...` header
(a historical header name that applies to every provider). Consecutive turns of
the same conversation prefer the same account from the first user message
(including image-only turns);
`X-CLI2API-Session` remains an optional override.
Account pinning is a routing preference, not an authorization boundary: a missing
account can fall back to the pool, and cooling pins may escape within the same
provider and region. Use client-key grants to enforce access limits.

Cross-provider routing for shared model IDs is enabled by default. Use a
provider-prefixed ID such as `qoder/<model-id>` to restrict the provider.
When cross-provider routing is disabled, a provider prefix is required.
Key grants, region constraints, model support, and account readiness still apply.

## 4. Configuration

Most settings are in the console: **System** for the global proxy, **Accounts**
for per-account proxy and concurrency, and **API keys** for client access.
Global and Qoder proxies accept HTTP(S) only; WorkBuddy / Trae / Devin account
proxies also accept SOCKS5. Use `direct` or `none` to bypass proxy inheritance.

The administrator key is generated once and stored in SQLite. Rotate it in the
console; environment variables cannot replace it.

<details>
<summary>Advanced environment variables and diagnostics</summary>

| Variable | Default | Purpose |
|----------|---------|---------|
| `QODER_DATA_DIR` | `/data` | SQLite database and durable account credentials |
| `QODER_RUNTIME_DIR` | `/run/cli2api` | Ephemeral per-account runtime homes for providers that use child processes |
| `QODER_MAX_RETRY_ACCOUNTS` | `4` | Maximum accounts attempted for one request (1-64) |
| `QODER_SSE_DIAGNOSTIC_MODELS` | empty | Comma-separated Qoder model IDs for redacted SSE diagnostics in Runtime Logs; `*` enables all |
| `QODER_WORKER_BASE_PORT` | `32100` | Internal child-runtime port range |
| `QODER_PROXY_URL` | empty | Initial global outbound proxy: `http(s)://`, `direct`, or `none`; saved console settings take precedence |
| `QODERCLI_JS` | image default | Pinned Qoder Global CLI bundle |
| `QODERCNCLI_JS` | image default | Pinned Qoder CN CLI bundle |
| `UPDATE_GITHUB_TOKEN` | empty | Optional GitHub token for release checks |
| `UPDATE_AGENT_URL` | empty | Docker Desktop host updater URL, written by the installer |
| `UPDATE_AGENT_TOKEN` | empty | Docker Desktop updater token, written by the installer |
| `CLI2API_UPDATER_SOCKET_DIR` | platform-specific | Host directory mounted read-only for the Linux updater socket |

These are process settings; Compose passes only variables declared in its
`environment` section. For variables not listed there, add them through a local
`deploy/docker-compose.override.yml` and include that file with `-f` when running
Compose directly. An entry in `deploy/.env` alone is not enough.

For Qoder stream diagnostics, set `QODER_SSE_DIAGNOSTIC_MODELS` to a model ID
from `/v1/models` and recreate the container. Diagnostics record event metadata,
not prompts, responses, tool arguments, or credentials.

</details>

## 5. Endpoints and limits

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/health` | Health probe; no API key required |
| `GET` | `/v1/models` | Model catalog |
| `POST` | `/v1/chat/completions` | OpenAI-compatible chat |
| `POST` | `/v1/messages` | Anthropic-compatible messages |
| `POST` | `/v1/responses` | OpenAI Responses-compatible API |
| `GET/POST/PATCH/DELETE` | `/api/*` | Console management API |
| `POST` | `/api/accounts/{id}/daily-tasks` | Run WorkBuddy daily maintenance; inspect `status` and `run.report.steps` for partial failures |
| `GET` | `/api/accounts/{id}/daily-tasks?limit=20` | Persisted daily-task history (up to 50 runs) |

Console `/api/*` requires the administrator key. `/v1/*` accepts that key or an
enabled client key. `/health`, static frontend resources, and `/v1/*` CORS
preflight `OPTIONS` do not require authentication.

Messages / Responses are stateless compatibility APIs, not full official API
implementations. They do not store server-side conversations or execute
upstream-specific tools. Qoder supports images where the model allows them;
experimental Devin accepts base64 data-URL images. WorkBuddy / Trae do not
support images. File inputs are rejected.

<a id="update-network-and-mirrors"></a>

## Update network and mirrors / 更新网络与备用源

The application and host updater read the same `network.json`, mounted read-only
at `/run/cli2api-updater/network.json` in the application. Installers create it
from [update-network.example.json](update-network.example.json) only when absent:

| Host | Configuration location |
|---|---|
| Windows | `<InstallDir>\socket-placeholder\network.json` |
| Linux | `/var/lib/cli2api-updater/shared/network.json` |
| macOS | `~/Library/Application Support/cli2api-updater/shared/network.json` |

Edit the existing file without deleting its directory; changes apply on the next
forced version check or download, without restarting. Upgrade older installers
once so both processes see the shared directory. Custom deployments can use
`UPDATE_NETWORK_CONFIG` for the application and `--network-config` for the host.
Keep a credential-bearing proxy configuration private; the console displays
only source hostnames, counts, and whether a proxy is configured.

配置文件与账号数据库独立。三个备用源列表默认为空，**没有地址就不会自动获得国内加速**。
填写实际可用且可信任的地址后，主源失败或超时会依次尝试备用线路：

| Field | Format and behavior |
|---|---|
| `api_mirrors` | GitHub-compatible API base URLs. A base is followed by `/repos/cokeeven/ali2api3/releases?...`; it must support pagination. |
| `download_mirrors` | Download proxy prefixes. A prefix is followed by `/https://github.com/cokeeven/ali2api3/releases/download/<version>/<asset>`. |
| `image_mirrors` | Complete mirrored repositories, e.g. `registry.example/owner/ali2api3`, without scheme, tag or digest. The registry must retain the **same multi-platform manifest digest** as GHCR. Docker Hub `registry-mirrors` alone does not accelerate GHCR. |
| `proxy_url` | HTTP(S) or SOCKS5/SOCKS5H proxy for update metadata and attachments. Direct/environment networking is tried first, then this proxy. Both application and host must reach this address; their loopback addresses are different. |
| `request_timeout_seconds` | Per HTTP metadata request, default 8, range 1–30. Version checks are bounded to 90 seconds overall. |
| `download_timeout_seconds` | Per asset source/route, default 120, range 1–300. |
| `pull_timeout_seconds` | Per Docker pull, default 180, range 1–300. |

Each list accepts at most three entries. HTTPS is required for HTTP mirrors,
except loopback test servers. GitHub tokens are never sent to mirror hosts or
cross-origin redirects. Sources are operator-selected trust roots: checksums
protect content consistency but are not independent signatures if both metadata
and content come from the same mirror.

镜像备用拉取先读取该版本的 `cli2api-image-digest.json`，检查版本和仓库，再按
`镜像仓库@sha256:...` 拉取，成功后才标记为正式镜像。未发布摘要的旧版本只能走原源；
不会降级成只按标签信任备用仓库。更新器二进制保留 SHA-256 校验，失败停止安装。
所有下载失败均不重建当前服务，不覆盖持久数据。

`proxy_url` 不会修改 Docker Desktop/Engine 的守护进程代理；程序镜像可通过上述
`image_mirrors` 切源，或在 Docker 自身配置代理。系统设置显示已配置数量和实际使用来源；
“尚未配置备用源”表示当前只有原源可用，并不代表已保证国内网络连通。

## 6. Managed updates (optional)

Start `qoder-api-proxy` once before installing the optional host updater.

| Host | Container platform | Updater asset |
|------|--------------------|---------------|
| Linux x86-64 | `linux/amd64` | `cli2api-updater_linux_amd64` |
| Linux ARM64 | `linux/arm64` | `cli2api-updater_linux_arm64` |
| macOS Intel | Docker Desktop `linux/amd64` | `cli2api-updater_darwin_amd64` |
| macOS Apple Silicon | Docker Desktop `linux/arm64` | `cli2api-updater_darwin_arm64` |
| Windows x86-64 | Docker Desktop Linux containers | `cli2api-updater_windows_amd64.exe` |
| Windows ARM64 | Docker Desktop Linux containers | `cli2api-updater_windows_arm64.exe` |

Installers prefer checksum-verified release binaries and fall back to the
running container's binary or a local Go build when needed.

macOS + Docker Desktop:

```bash
./deploy/install-updater.sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --force-recreate qoder-api-proxy
```

Linux + systemd:

```bash
sudo ./deploy/install-updater.sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --force-recreate qoder-api-proxy
```

Windows + Docker Desktop in Linux-container mode, from the logged-in Docker user's PowerShell:

```powershell
powershell -ExecutionPolicy Bypass -File .\deploy\install-updater.ps1
docker compose --env-file deploy\.env -f deploy\docker-compose.yml up -d --force-recreate qoder-api-proxy
```

The application container never receives the Docker socket. Linux uses a private
Unix Socket. macOS runs a per-user LaunchAgent and Windows runs a current-user
Scheduled Task (or a current-user Startup entry when task registration is denied);
both Docker Desktop platforms use an authenticated updater bound
to `127.0.0.1` and reached through `host.docker.internal`.

In **System**, download the latest stable update, then confirm installation.
Confirmation waits for in-flight requests (up to 30 seconds), creates a SQLite
snapshot, and replaces the container. If draining times out, the update stops
before backup and replacement. Development builds without a semantic
version cannot use managed updates.

Updates jump directly to the latest stable release. The console shows skipped
versions and offers rollback to one of the three previous stable releases.
Every snapshot is published with an integrity manifest that records its SHA-256
and the schema migrations it contains; an upgrade stops before touching the
container when that record is missing or does not match. The five most recent
snapshots are retained in `/data/backups`.

If an upgrade fails its health check or updater replacement, the updater restores
the previous image. **Version rollback keeps the current database**. Append-only
migration files do not guarantee that an older program understands the newer
schema; health checks must confirm compatibility. The console rejects
`restore_database: true` because selecting a version does not identify a verified
historical snapshot. Historical data recovery requires a separate reviewed
procedure. The host's explicit failure-recovery path preserves the database and
any WAL/journal files before restoring its supplied snapshot, aborts if that copy
fails, and excludes preserved recovery sets from routine backup pruning.
The application and its data live in separate places:
`/data` is a named volume, and no upgrade path deletes or re-initialises it.

The flow is covered by isolated tests for backup integrity, migration failure,
failed start, rollback after new data, and database/config restore. Managed
upgrades, program rollback and data retention have also been verified on a real
Windows host with Docker Desktop. This does not cover every host configuration;
keep an independent database backup before upgrading.

Keep `deploy/.env` private: Docker Desktop mode stores an updater token there.
Do not remove `qoder-data` or run `docker compose down -v` unless you intend to
delete the accounts and credentials.
