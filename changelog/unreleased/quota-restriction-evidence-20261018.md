### English

- Persist account-level quota restriction evidence independently from cooldown timers, so a quota error without an upstream reset remains diagnosable without inventing a retry deadline or relabeling an existing auth cooldown.
- Preserve this bounded evidence through quota refreshes and SQLite restart, and show its code, source, applied/reported scope, observation times, and any confirmed upstream reset in route diagnostics.
- Keep legacy quota snapshots and missing reset values unknown; evidence excludes provider messages and request content.

### 中文

- 将账号额度限制证据与冷却计时分开持久化；上游未提供重置时间时仍可诊断，不伪造重试截止时间，也不改写已有认证冷却原因。
- 额度刷新与 SQLite 重启后保留这组有限证据，并在路由诊断中展示分类码、来源、实际/报告范围、观测时间及已确认的上游重置时间。
- 历史额度快照和缺失的重置时间继续保持未知；证据不包含上游错误正文或请求内容。
