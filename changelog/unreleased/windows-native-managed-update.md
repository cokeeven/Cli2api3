### English

- Add a Windows amd64 native update backend using the existing console prepare/confirm flow. Verify full application packages, drain requests, validate SQLite snapshots, replace only managed program/runtime files, and check version, commit and instance ownership.
- Preserve the newest database on failed startup or interrupted-install recovery. Persist update identity and recovery journals; report recovery failure instead of restoring an older database automatically.
- Provide a parameterized first-onboarding PowerShell installer, immutable executable launch/supervision, Windows runtime packages and isolated process/task CI rehearsals. Docker images, legacy updater assets and protocols remain compatible.

### 中文

- 增加 Windows amd64 原生更新后端，复用网页下载准备和确认安装流程，校验完整应用包、排空请求、验证 SQLite 备份，只替换受控程序与依赖，并核对版本、提交和实例归属。
- 启动失败或安装中断时保留最新数据库，持久化任务标识与恢复日志；恢复失败明确报告，不自动覆盖为旧数据库。
- 提供参数化首次接入 PowerShell 脚本、独立 exe 启动和托管、Windows 运行包及隔离进程/计划任务 CI 演练。保留 Docker 镜像、既有更新器资产和协议兼容。
