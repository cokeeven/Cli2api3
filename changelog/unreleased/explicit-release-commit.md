### English

- Allow an explicit `release: publish` commit on main to request the existing verified release pipeline when maintaining through the GitHub API; ordinary commits do not publish releases.
- Allow the Windows updater installer to target an existing deployment's Compose file, environment file, container, and installation directory.

### 中文

- 支持通过 GitHub API 维护时，以 main 上明确的 `release: publish` 提交请求现有校验发布流程；普通提交不会发布版本。
- Windows 更新器安装脚本支持指定已有部署的 Compose、环境文件、容器及安装目录。
