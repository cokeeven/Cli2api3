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
| `api_mirrors` | GitHub-compatible API base URLs. A base is followed by `/repos/cokeeven/Cli2api3/releases?...`; it must support pagination. |
| `download_mirrors` | Download proxy prefixes. A prefix is followed by `/https://github.com/cokeeven/Cli2api3/releases/download/<version>/<asset>`. |
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


## Windows native updates

### Windows Server 2022 首次接入

此后端只支持 **Windows amd64**；原有 Docker updater 与镜像包名保持兼容。必须使用包含 `cli2api-app_windows_amd64.zip` 的支持版正式 Release。仓库中已增加构建和 CI 流程；本地开发包或尚未运行的工作流不能算作已发布版本。

1. 核对当前网关计划任务、身份、启动参数、环境文件、工作目录与持久目录。保留 `ali2api3-Server` 的任务名，图形控制台可继续启停它。不要更改独立的 `ali2api3-Caddy`。
2. 从同一正式 tag 下载六项更新器资产中的 `cli2api-updater_windows_amd64.exe`、`cli2api-updater_checksums.txt`，以及应用 zip 和 `cli2api-app_checksums.txt`。安装脚本来自该 tag 的 `deploy/install-native-updater.ps1`。核对下载来源及 SHA-256；同一来源的校验和属于完整性检查，不是独立数字签名。
3. 以 `deploy/native-launch.example.json` 为格式，将**原有**启动参数、环境文件与工作目录写到新的接入 JSON。不要把示例环境覆盖到现有未知配置上。环境变量从原文件读取，再应用 JSON 的明确值；原凭据和 Key 留在现有数据目录。
4. 在管理员 PowerShell 中执行以下模板。这里的版本必须替换成实际已发布的支持版；下面路径只是现有布局的接入示例，不是硬编码。

```powershell
$version = Read-Host '已发布且包含 Windows 应用包的版本，如 v0.x.y'
./install-native-updater.ps1 `
  -InstallRoot 'C:\ali2api3' `
  -ProgramDirectory 'C:\ali2api3\program' `
  -DataDirectory 'C:\ali2api3\data' `
  -LaunchConfig 'C:\ali2api3\config\native-launch.json' `
  -TaskName 'ali2api3-Server' `
  -UpdaterTaskName 'ali2api3-NativeUpdater' `
  -UpdaterDirectory 'C:\ali2api3\updater-native' `
  -Port 3011 -HealthURL 'http://127.0.0.1:3010/health' `
  -UpdaterBinary '.\cli2api-updater_windows_amd64.exe' `
  -UpdaterChecksums '.\cli2api-updater_checksums.txt' `
  -BootstrapPackage '.\cli2api-app_windows_amd64.zip' `
  -PackageChecksums '.\cli2api-app_checksums.txt' `
  -Version $version
```

脚本先校验包并准备完整组件，再对原库通过 SQLite `VACUUM INTO` 生成包含 WAL 已提交数据的一致快照和 SHA/schema/integrity 清单；失败不停止网关。首次明确调用 `-BootstrapPackage` 是一次性引导安装，保留原 exe/组件及任务 XML，再切换到支持版。它不会把 live `qoder.db` 当普通文件复制、脱敏或文本转换。原有 data、runtime、认证 HOME、config、logs、console、Caddy 均不属于更新包替换范围。

接入后，原网关任务的身份、触发器和设置继续保留，动作改为稳定 seed exe 的 `--run-native-gateway`。独立 SYSTEM 更新器任务运行 `--supervise-native`；子更新器从经过校验的 hash 目录启动，Windows 文件锁不会阻止自身更新。无需保留 AI 会话、RDP 或控制台窗口。只监听回环端口，Token 首次随机生成并限制 NTFS 权限，重复接入复用已有 Token，端口归属不符则拒绝，不抢占其他服务。

若 Windows 阻止运行首次安装脚本，使用管理员窗口并按组织批准的签名/执行策略运行本仓库可信脚本；不要加 `-ExecutionPolicy Bypass` 或绕过组策略。运行期启动和监督均为 exe，不需要放宽 PowerShell 脚本策略。没有管理员权限的 `-IsolatedProcessMode` 仅用于临时验收，**不提供重启自启，也不能代替服务器正式接入**。

### 后续网页更新与恢复

在“系统设置 → 版本更新”检查并下载，等待校验完成后由用户确认安装。准备阶段网关继续运行；确认后排空最多 30 秒，超时中止。宿主再次验证备份、暂存文件、版本/架构与组件清单，只交换 `program/qoder-api-proxy.exe`、`program/components`、`program/native-manifest.json`。旧文件存放于 `program/.cli2api-update/rollback/<job>`，不会先删除唯一可用版本。

启动验证要求健康返回目标版本/commit/实例 ID，且 PID 的真实 exe 路径属于本安装；单独健康 200 不算成功。失败默认仅恢复程序/组件，保留最新数据库，包括失败前新版本写入的记录。旧程序不兼容新库时显示恢复失败，**不自动还原旧库**。历史库恢复仍需独立离线方案，网页没有新增覆盖旧库按钮。

宿主状态 `status.json` 与 `transaction.json` 持久化于更新器目录，网关重启或页面刷新后继续显示同一任务。中断时 supervisor 用可用旧宿主核对日志并恢复，禁止盲目重做替换。`recovery_failed` 会阻止新安装；先停止该安装的网关和独立更新器，保留数据库及 WAL/SHM、backups、整个 `.cli2api-update`、状态日志与原任务 XML。检查兼容性并恢复可用程序或前向修复；不要删除事务日志来强行解锁或把旧快照覆盖到有新写入的库。

首次引导的旧版程序可能尚无实例身份接口。如果引导中断，保留上述文件，恢复 `original-gateway-task.xml` 中原动作，并从记录的 `rollback/<bootstrap-job>` 恢复旧程序/受控组件；只在进程停止后处理程序文件，数据库不回退。无法核实旧程序健康时，应报告恢复待核实而不是宣称自动恢复成功。

备用网络配置为 `updater-native/network.json`，复用 GitHub 元数据、附件镜像、HTTP(S)/SOCKS 代理与逐源超时。原生后端忽略 Docker 镜像仓库列表，绝不把镜像当 Windows 应用包。应用附件选择仅 Windows amd64 固定名称；旧 Release 若没有原生包，下载失败并保留当前服务。

### Native onboarding and recovery (English)

Use a published supporting release and the full Windows amd64 application archive, not just updater.exe. Supply the existing task name, data/program/updater paths, loopback health URL, free agent port and a reviewed launch JSON retaining the original environment file, arguments and working directory. Run the parameterized example above in an elevated PowerShell session. The installer verifies the archive, snapshots the existing SQLite database consistently, preserves the original task XML/program, and bootstraps the first supported gateway. It does not touch Caddy or move credentials into the application package.

The gateway retains its original scheduled-task identity/settings and a stable executable launcher action. An independent startup task supervises the authenticated loopback agent. Hash-addressed host executables avoid replacing a running Windows exe; no runtime script-policy bypass is needed. A repeated installation retains the token and existing listener. A foreign port owner/task action is rejected. Process-mode installation is an isolated test option and does not promise reboot persistence.

Later releases use the existing console check → download → explicit confirm → drain → verified snapshot → replace → verify flow. The runtime archive includes Node, worker, pinned CLI packages, updater and the gateway with embedded UI; mutable data/config/HOME are excluded. The backend validates archive paths, file hashes, PE architecture, version/commit and instance/PID ownership. Default rollback preserves the newest database; schema incompatibility or failed program recovery is reported without restoring an older database. Keep journals and rollback copies for operator recovery; never erase a journal or replace a live database to manufacture success.

首次引导旧版尚未提供原生维护接口，应安排维护窗口，先停止客户端新调用并等待在途请求结束，再执行一次性 Bootstrap。后续支持版的网页更新由协调器强制排空，超时中止。当前正式接入针对已有 Windows 计划任务；不把任意 SCM/NSSM 服务包装器视为已支持的接入方式。

For the first bootstrap, an older gateway may lack the native maintenance API: pause incoming client traffic and let existing requests finish in a maintenance window. Subsequent web upgrades enforce request draining and abort on timeout. Production onboarding currently targets an existing Windows Scheduled Task; arbitrary SCM/NSSM service wrappers are not supported.
