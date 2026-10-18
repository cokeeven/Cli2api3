---
id: cli2api-development
title: Development
scope: [build, validate, release]
status: canonical
read-when: 本地构建 / 跑测试 / 发布维护者版本时
summary: 本地构建与校验命令，以及维护者发布流程（workflow_dispatch、unreleased fragments、归档 PR）。
related: [CONTRIBUTING.md, docs/ARCHITECTURE_SUMMARY.md]
last-updated: 2026-09-19
---

# Development

Repository rules, layering constraints, and validation checklists live in
[CONTRIBUTING.md](../CONTRIBUTING.md). This page covers the local build loop
and the maintainer release workflow.

## Requirements

Go `1.25.6+`, Node.js `22+`, npm, and Docker for container development.

## Validate

```bash
# Go API
go test ./internal/app -run 'TestImportConstraints|TestDutyBoundaries' -count=1
go test ./...
go test -race ./...
go vet ./...

# Qoder child runtime
(cd worker && npm ci && npm audit && npm test)

# Console (npm test runs the pure-logic tests the frontend job also runs in CI)
(cd frontend && npm ci && npm test && npm run lint && npm run build)
git diff --check
```

`go test -race` needs a C compiler (cgo); on a host without one, record that the
race run was skipped rather than claiming it passed.

The console's pure-logic tests live in `frontend/tests/*.test.mjs`, import the
TypeScript sources directly, and run through `node --test
--experimental-strip-types`. Add a case there when a change fixes a wizard or
policy behaviour that would otherwise only be covered by hand.

WorkBuddy/Trae chat limits, timeout phases, usage semantics, optional source
diagnostics and maintenance controls are described in
[CHAT_DIAGNOSTICS.md](CHAT_DIAGNOSTICS.md). Local fixture tests and authorized
real-account evidence must be reported separately.

The Qoder worker pins `undici` in `worker/package.json`. Keep `npm audit` clean:
the pin is exact, so a security fix means a new exact version in the same major
plus a worker test run.

After frontend changes, run `npm run sync` to update the static assets embedded by Go:

```bash
cd frontend
npm run sync
```

`vite build` writes `frontend/dist`. The Go binary embeds `internal/webui/static`. Shipping a console change without `sync` leaves the previous hashed bundle in the image. Commit the new `index-*.js` / `index-*.css` and `index.html` in the same change; Git will show the old hashed files as deletions.

Build and start the container from source:

```bash
cd deploy
docker compose up -d --build
```

## Maintainer release

Do not create or push version tags by hand. The tag, GitHub Release, updater assets, and GHCR image aliases all come from the serialized Release workflow on `main`.

### Before you run it

1. `main` is the commit you want to ship. The workflow waits for CI on that exact SHA.
2. `changelog/unreleased/` has bilingual fragment files for every user-facing change on `main` since the last published tag. The workflow concatenates those files into the GitHub Release and the console System page. `validate` allows an empty unreleased directory; `extract-for-release` fails if it is empty and the new version heading does not already exist.
3. Do not archive fragments or edit `CHANGELOG.md` yourself before the run. The workflow reads `changelog/unreleased/` first; archive only after the tag is public, through the pull request the workflow opens.
4. Do not ship a change that edits the SQL bytes of an already-applied SQLite migration. Existing databases panic on boot with `checksum mismatch`, and the host updater rolls back. Append a new numbered entry in `internal/store/migrations.go`; the public architecture map is in `docs/ARCHITECTURE_SUMMARY.md`.

```bash
gh workflow run release.yml --ref main
```

You can also use **Actions → Release → Run workflow**.

When maintaining through the GitHub Git API without a local authenticated CLI,
an explicitly authorized commit on `main` whose message starts with
`release: publish` requests the same pipeline with a patch bump. Ordinary
pushes skip publishing. The pipeline still waits for CI on that exact commit,
then creates and verifies the draft, updater assets, and images before making
the release public. Use `workflow_dispatch` for a minor or major bump.

The workflow calculates the next patch from the latest published stable release, creates an invisible draft, builds six checksum-verified updater binaries plus `cli2api-updater_checksums.txt`, publishes `linux/amd64` + `linux/arm64` images, uploads `cli2api-image-digest.json` with that exact multi-platform digest, then makes the GitHub Release latest and moves `latest` / series aliases. Console update checks ignore drafts, so a failed pre-publication run stays invisible.

### After it publishes

The `changelog` job opens a `docs: archive changelog for v0.x.y` pull request. It copies the published GitHub Release body under `## 0.x.y - YYYY-MM-DD` and deletes only the `changelog/unreleased/` files that existed at the tagged commit. Fragments merged after the tag stay unpublished.

Merge that PR. Do not re-run `release.yml` to archive notes — a second run would mint the next patch. Treat a missing archive PR as the thing to fix, not a red `publish` / `aliases` job.

If required checks do not start on the bot PR (the default Actions token cannot retrigger workflows), push an empty commit to that branch from a local checkout.

If this checkout cannot switch to `main` because another worktree already has it, merge with the GitHub API instead of `git checkout main`:

```bash
gh api -X PUT repos/cokeeven/Cli2api3/pulls/<n>/merge -f merge_method=merge
```

Expected artifacts for a published tag:

| Kind | Names |
|------|--------|
| GitHub Release assets (10) | six `cli2api-updater_{os}_{arch}` binaries, `cli2api-updater_checksums.txt`, `cli2api-image-digest.json`, `cli2api-app_windows_amd64.zip`, and `cli2api-app_checksums.txt` |
| GHCR (`ghcr.io/cokeeven/ali2api3`) | `v0.x.y`, `0.x.y`, series (`0.2`), `latest`, all the same multi-arch digest |

If a **pre-publication** job fails (`prepare` / `assets` / `native-assets` / `draft` / `image` / `promote`), use **Re-run failed jobs** on the same run. The draft remains unpublished. Do not create the tag locally while that draft exists.


## Windows native runtime development / Windows 原生更新开发

`internal/updater.NativeExecutor` is selected by `cmd/updater --backend windows-native --native-config <absolute JSON>`. The default remains Docker. Gateway `UPDATE_BACKEND=windows-native` refuses a Docker/legacy agent; protocol 2 retains old Docker request compatibility. The native backend currently supports Windows amd64 only. Keep the historical GHCR package and updater asset names.

The runtime package is built on Windows, embeds the synced console in the gateway exe, and includes Node 22.23.3 (pinned upstream archive SHA-256), worker plus npm lockfile dependencies, qodercli/qoderclicn 1.1.32, and the matching updater. The Windows-only npm lockfile pins a patched sharp 0.35.5 override without changing either CLI version; packaging loads the real Windows addon and checks a one-pixel PNG operation. `native-manifest.json` records backend, OS, architecture, version, commit, protocol, component versions and every file's size/SHA-256. This is integrity metadata, not an independent signature. The CLI runtime has its own lockfile in `scripts/windows-runtime`; do not silently change CLI compatibility hooks to clear a dependency audit. Runtime npm lifecycle scripts are explicitly disabled: the CLI postinstall changes per-user PATH/dispatcher state, which the gateway does not need; the real Windows prebuilt addon and bundle hooks are checked directly.

```powershell
$env:APP_VERSION='v9.0.2' # isolated test identity, never a manually published tag
$env:APP_COMMIT='2222222222222222222222222222222222222222'
./scripts/build-windows-app.ps1 -OutputDirectory "$env:TEMP/native-app-assets"
$env:CLI2API_NATIVE_REHEARSAL='1'
$env:CLI2API_NATIVE_APP_PACKAGE="$env:TEMP/native-app-assets/cli2api-app_windows_amd64.zip"
go test ./internal/updater -run TestNative -count=1 -v -timeout=15m
./scripts/test-native-installer.ps1 -ApplicationDirectory "$env:TEMP/native-app-assets" -WorkDirectory "$env:TEMP/native-onboarding"
# An elevated runner is required for real scheduled-task tests:
$env:CLI2API_NATIVE_TASK_REHEARSAL='1'
go test ./internal/updater -run TestNative -count=1 -v -timeout=15m
./scripts/test-native-installer.ps1 -ApplicationDirectory "$env:TEMP/native-app-assets" -WorkDirectory "$env:TEMP/native-onboarding-tasks" -RequireTasks
```

真实进程用例包含正常替换、失败启动、失败前新写入保留、真实文件占用、损坏备份、错误架构、重复宿主与恢复日志。完整应用包用例运行真实 `cmd/server` 并从受鉴权网页 API 确认安装，核对新进程版本/commit、账号/凭据/管理员密钥/调用 Key/配置/历史及网关重启后的同一任务结果。计划任务模式和首次安装需要提升权限；不得把 process 模式通过写成计划任务已通过。CI 的 `windows-native` 作业构建运行包并执行提升权限专项；本地没有 gcc 时记录 Windows race 未执行，Linux CI 的完整 race 仍须通过。

First onboarding uses a stable, hash-addressed seed exe for `--run-native-gateway` and `--supervise-native`. A successful update exits only its child agent; the independent supervisor starts the package's verified updater from another immutable hash directory. Runtime operation does not require PowerShell script execution-policy changes. The installer preserves the gateway task principal/settings and changes only its reviewed action; unrelated tasks are outside the update backend.

事务日志先于破坏性步骤落盘，组件旧副本位于 `program/.cli2api-update/rollback/<job>`。中断恢复只核对日志并恢复程序，不盲目重放安装或恢复旧库。恢复失败保留日志、组件副本与最新数据库，阻止下一次安装，需按部署文档处理。已发布 migration SQL 不得修改。运行包不得包含 data、认证 HOME 或生产配置。

The Release workflow still builds six host updater binaries and Docker multi-architecture images. A separate Windows job produces the application zip/checksums before the draft can publish; the final release now has ten assets including the existing image digest manifest. Workflow edits must be validated locally but are not proof an online release ran.
