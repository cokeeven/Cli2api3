### English

- Limit recovery from an expired per-account, per-model cooldown to one probe, preserving pending model probes across restarts and clearing only the successful model.
- Recognize narrowly evidenced WorkBuddy model-service failures and preserve account authentication scope, reported reset evidence and bounded local backoff.
- Add explicitly configured, disabled-by-default same-provider and same-region model alternatives. Require the selected account's catalog to establish request capabilities, stop after one alternative and record the actual model and attempt route.

### 中文

- 每个账号的模型冷却到期后仅放行一次探测；重启后保留待探测状态，成功只清除对应模型的限制。
- 根据明确证据识别 WorkBuddy 模型服务错误，区分账号认证范围，并保留真实重置证据和有界本地退避。
- 新增默认关闭、需显式配置的同平台同地区替代模型规则。仅在所选账号目录明确支持请求能力时最多尝试一次，并记录实际模型和尝试路径。
