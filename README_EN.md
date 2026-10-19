<div align="center">

<h1><img src="./frontend/public/apple-touch-icon.svg" width="40" height="40" align="top" alt=""> Cli2api3</h1>

**One API for your Qoder, WorkBuddy and Trae accounts**

A multi-platform AI reverse proxy for personal use. Manage logins, models, usage and updates, then connect your preferred AI clients.

[![CI](https://github.com/cokeeven/Cli2api3/actions/workflows/ci.yml/badge.svg)](https://github.com/cokeeven/Cli2api3/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/cokeeven/Cli2api3)](https://github.com/cokeeven/Cli2api3/releases/latest)
[![License](https://img.shields.io/github/license/cokeeven/Cli2api3)](LICENSE)

[中文](README.md) · [Quick start](#quick-start) · [Deployment](deploy/README.md) · [Changelog](CHANGELOG.md) · [Issues](https://github.com/cokeeven/Cli2api3/issues)

<img src="./docs/assets/readme/hero-en.svg" width="100%" alt="Cli2api3 — one API for your Qoder, WorkBuddy and Trae accounts">

</div>

Cli2api3 connects upstream accounts you own and are authorized to use to **OpenAI / Anthropic-compatible APIs**, with chat, streaming and tool calls. Use a single endpoint for clients and a web console to inspect account readiness, choose models, create client keys and manage updates.

## Usage and project notice

Cli2api3 is an unofficial AI protocol adapter and reverse proxy for personal use, protocol compatibility research and development testing. Before using it, note the following:

1. **Bring your own accounts and permissions**

   Only connect accounts you own and are authorized to use for the intended purpose. Follow each platform's terms of service, quota limits and usage rules. This project does not provide accounts, subscriptions or model quota.

2. **Unofficial project**

   This project is not officially affiliated with or endorsed by the platforms it connects to. Platform names, trademarks and related services belong to their respective rights holders.

3. **Intended scope**

   The project focuses on personal client access and account management. It does not operate a public relay service or endorse third-party account sales, quota resale, paid access codes or hosted services.

4. **Compatibility and account risks**

   Compatible endpoints do not fully replicate official APIs. Changes to upstream protocols, models, quotas or risk controls may cause features to stop working, requests to be restricted or accounts to face limitations. The project does not guarantee continued availability or that accounts will remain unaffected.

5. **Credentials and data protection**

   The service listens on the local machine by default. Do not directly expose the management console, updater or service ports. Protect account credentials, client keys and database backups, and remove sensitive information before sharing issues or screenshots. Model requests are still sent to the selected upstream service.

6. **Open-source license and origins**

   The project retains the upstream MIT license and copyright notices. The code's open-source license does not grant additional rights to use any upstream accounts, models or services.

## Features

| Capability | What it provides |
|---|---|
| Unified APIs | Chat Completions, Responses, Anthropic Messages and model listings; available features depend on the adapter and upstream model |
| Account routing | Session affinity, concurrency limits, rate-limit cooldowns, account failover and per-client-key platform priorities |
| Caller instructions | Preserve caller `system` / `developer` instructions and tool definitions by default, adapting message roles to upstream protocols |
| Usage and guards | Inspect quota, usage and credit expiry; apply platform-supported token or credit guards while keeping actual usage separate from estimated liabilities |
| Account maintenance | Login, credential refresh, model catalogs, check-ins and WorkBuddy CN task records, with optional automatic maintenance |
| Managed updates | Download releases from this repository and confirm installation; drain requests and verify backups before upgrading, with program rollback keeping the current database by default |

## Platforms

| Platform | Adapter scope | Live verification |
|---|---|---|
| Qoder | CN and Global | Basic chat, streaming, tool calls and quota refresh verified with a CN account; Global awaits live-account acceptance |
| WorkBuddy | CN and Global | Basic chat, streaming, tool calls and quota refresh verified with a CN account; Global and some campaign branches await acceptance |
| Trae | CN | Basic chat, streaming, tool calls and quota refresh verified; interactive login and reauthentication after natural expiry still need acceptance |

Verification covers a limited set of CN accounts and models, not every model, account type or campaign. Experimental Devin, Command Code and other adapters remain in the repository; current maintenance focuses on the three platforms above.

## Quick start

You need Docker, Docker Compose and an upstream account you control. Use Docker Desktop on Windows / macOS; Windows requires Linux containers.

```bash
git clone https://github.com/cokeeven/Cli2api3.git
cd Cli2api3
docker compose -f deploy/docker-compose.yml pull
docker compose -f deploy/docker-compose.yml up -d
docker compose -f deploy/docker-compose.yml logs --tail=50
```

1. Save the **administrator key** printed in the first-start logs.
2. Open `http://127.0.0.1:3010`, sign in and add your accounts.
3. Create a **client key** under API keys, then choose and test a model under Access.

The default image is `ghcr.io/cokeeven/ali2api3:latest`. See the [deployment guide](deploy/README.md) to pin a version, build from source or install the host updater.

The project is now **Cli2api3**. The image repository keeps its existing `ali2api3` name so deployed updaters continue to find releases.

## Connect a client

Configure a client that supports the selected API protocol:

```text
Base URL: http://127.0.0.1:3010/v1
API Key:  <client key created in the console>
Model:    <model ID from Access or /v1/models>
```

| Protocol | Endpoint |
|---|---|
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |
| Model listing | `GET /v1/models` |

Client model lists return provider-qualified IDs and display names, such as `workbuddy/glm-5.3`, `trae/glm-5.3`, or `qoder/<model>` when present in the provider's catalog. Select a returned ID to pin that provider while retaining account rotation within it. Existing bare-ID calls remain available when the cross-provider model pool is enabled; disabling it requires qualified IDs. Refresh the client's model list after updating the gateway.

Clients execute tools and return their results. Compatibility does not guarantee full official API parity. Account pinning, session headers, platform prefixes and request examples are documented in the [deployment guide](deploy/README.md).

## Console and accounts

<p align="center">
  <img src="./docs/assets/readme/console-window-en.svg" width="100%" alt="Console illustration: manage accounts, inspect readiness and quota, and retrieve client connection details">
</p>

- **Accounts and models**: manage login, enablement, credential refresh, proxies and model catalogs; inspect readiness and quota.
- **Keys and routing**: administrator keys manage the console; client keys access `/v1/*`, with platform grants, priorities and concurrency settings.
- **Maintenance and logs**: opt into check-ins and automatic maintenance; inspect task steps and request records. Rewards depend on actual upstream eligibility and readback confirmation.

Cross-provider failover defaults off; explicit platform prefixes, permissions and region boundaries remain binding. Usage guards default off. Strict guards may reject requests without a trusted cost upper bound, and credit multipliers are not actual per-request costs. Console images are illustrations.

## Updates and data protection

Stable builds come from [this repository's Releases](https://github.com/cokeeven/Cli2api3/releases) and `ghcr.io/cokeeven/ali2api3`. With the host updater installed, **download first, then confirm installation** in System settings. Managed updates support Docker Compose and Windows amd64 native deployments after the one-time [native onboarding](deploy/README.md#windows-native-updates). Use a release that includes the full native application package and installer; an older standalone updater binary is insufficient.

- Wait for in-flight requests, create a consistent SQLite snapshot and verify its integrity. Draining or verification failure stops the upgrade.
- Keep application binaries separate from durable data. Program rollback preserves the current database by default; restoring an older database requires separate verification and preservation of newer data.
- Configure independent fallback sources for release checks, updater assets and container images, plus a proxy for update HTTP traffic. Fallback images are pulled by their published digest.

**Fallback sources require reachable addresses supplied by the operator; no third-party accelerator is enabled by default.** See [update network and mirrors](deploy/README.md#update-network-and-mirrors). Basic CN-account calls, upgrades, program rollback and data retention have been verified on a real Windows host; other host environments need their own acceptance. Keep independent backups of important data.

## How it works

<p align="center">
  <img src="./docs/assets/readme/architecture-en.svg" width="100%" alt="Cli2api3 architecture: clients reach a Go gateway, Qoder login and quota use isolated account workers, while chat and other platforms use in-process adapters">
</p>

The Go gateway handles authentication, routing, protocol adaptation and request logs; SQLite stores durable data. Qoder login, catalog and quota operations use isolated per-account Node workers, while chat uses the in-process Go COSY client. WorkBuddy and Trae use in-process Go adapters.

## Scope and limits

- Bring your own accounts and quota. Model, image and tool capabilities depend on upstream support. WorkBuddy / Trae currently do not support images; file inputs are rejected.
- Caller instructions are preserved by default, but hidden upstream instructions cannot be removed by a reverse proxy. Existing accounts retain their configured prompt policy.
- Login callbacks, natural credential expiry, Global accounts and some check-in campaigns still require scenario-specific acceptance. Completion of every platform activity is not guaranteed.
- Upstream changes can affect compatibility. Request logs do not store prompt or completion bodies by default.

## Documentation and contributing

- [Deployment and operations](deploy/README.md) · [Changelog](CHANGELOG.md) · [Development and releases](docs/DEVELOPMENT.md)
- [Architecture](docs/ARCHITECTURE_SUMMARY.md) · [Contributing](CONTRIBUTING.md) · [Security reports](SECURITY.md)
- [GitHub Issues](https://github.com/cokeeven/Cli2api3/issues) · [LINUX DO community](https://linux.do)

The service binds only to `127.0.0.1:3010` by default for local clients. Do not directly expose service ports or the management console. Protect account credentials, client keys and data backups. See [Security](SECURITY.md) for detailed guidance and vulnerability reporting.

## Origins and license

Developed from [caigee-cmd/cli2api](https://github.com/caigee-cmd/cli2api), retaining its [MIT license](LICENSE) and copyright notices. The Go module keeps the upstream import path for compatibility. Thanks to `workbuddy2api`, `CLIProxyAPI`, `sub2api` and other projects for ideas and inspiration. Follow each upstream platform's terms when using its accounts.
