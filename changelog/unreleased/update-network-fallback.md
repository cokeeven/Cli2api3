### English

- Add configurable fallback routes for release checks, updater assets and container images, with bounded timeouts and an optional HTTP/SOCKS proxy for update HTTP traffic. Mirrors are operator-supplied; none are silently enabled.
- Pin fallback image pulls to the multi-platform digest published with each release, retain updater SHA-256 verification, and never forward GitHub credentials to alternate origins.
- Show configured backup counts and the sources actually used in System settings. Windows, Linux and macOS installers share network configuration with the console; failed downloads do not replace the running service or its data.

### 中文

- 版本检查、更新器附件和程序镜像支持配置备用源、有限超时，以及更新 HTTP 请求的 HTTP/SOCKS 代理；备用地址由部署者提供，不默认启用第三方公共源。
- 备用镜像按随版本发布的多架构摘要拉取，保留更新器 SHA-256 校验，不向备用站点转发 GitHub 凭据。
- 系统设置显示备用源数量和实际使用来源；Windows、Linux、macOS 安装器共享网络配置，下载失败不替换当前服务或持久数据。
