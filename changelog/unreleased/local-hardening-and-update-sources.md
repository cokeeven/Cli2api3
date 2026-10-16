### English

- Preserve caller system/developer instructions by default for new accounts, including WorkBuddy; distinguish the explicit caller-instruction compatibility switch from platform-native prompts. Existing account settings remain unchanged.
- Stop treating an unverified account as ready: in-process accounts start out of routing until a probe verifies the credential, and a login, import, or re-enable now re-probes immediately so readiness and the model catalog return as soon as the account is valid.
- Bound model-catalog refreshes process-wide: concurrent callers share one concurrency limit, each account is fetched at most once at a time, and a failed fetch backs off instead of being retried on every request.
- State which usage guards a platform can actually enforce, and explain an unknown-cost refusal: no supported platform reports a trusted per-request credit cost (a price multiplier is not a bound), so credit guards refuse a paid request instead of pricing it at zero.
- Point every update source at this repository (`cokeeven/ali2api3`, `ghcr.io/cokeeven/ali2api3`) and report version state accurately: a development build, a failed check, and a repository with no published release no longer display as "up to date".
- Protect data across upgrades with verified snapshots and schema metadata; version rollback keeps the current database. Reject historical database restore without a selected verified snapshot; preserve WAL/journal files during explicit host failure recovery and retain recovery sets outside routine pruning.
- Drain requests on both update paths and stop the update if draining times out; serialize admission with drain observation.
- Update `undici` to 6.29.1 in the Qoder worker, clearing the reported advisories while keeping the proxy and worker behaviour unchanged.
- Resolve canceled login waits; prevent stale account probes from overriding a restarted account, and re-probe after browser login completes.
- Assemble Qoder non-streaming tool-call argument fragments, map Trae caller developer instructions to its system role, and emit the OpenAI tool-call finish reason for completed Trae tool calls.
- Update the frontend's `source-map-js` dependency to 1.2.2 to address its reported indexed-source-map advisory.

### 中文

- 新账号默认保留调用方的 system/developer 指令，包括 WorkBuddy；明确区分“丢弃调用方指令”兼容选项与平台原生提示词，已有账号保留原设置。
- 未验证的账号不再被视为可用：进程内账号在探测通过前不参与调度；登录、导入或重新启用后会立即重新探测，账号可用时状态与模型目录同步恢复。
- 模型目录刷新改为进程级并发上限：并发调用共享同一限制，同一账号同时最多刷新一次，失败后进入退避，不再随请求反复重试。
- 明确标示各平台实际可执行的护栏，并解释“成本未知”的拒绝：目前没有平台提供可信的单次请求积分成本（价格倍率不是成本上限），因此积分护栏会拒绝付费请求，而不是按 0 计费。
- 所有更新来源统一指向本仓库（`cokeeven/ali2api3`、`ghcr.io/cokeeven/ali2api3`），并准确区分版本状态：开发版、检查失败和仓库尚无发布都不再显示为“已是最新版本”。
- 升级前校验快照及数据库结构记录，版本回退保留当前数据库；未选择并验证历史快照时拒绝旧库恢复。宿主显式失败恢复会先保留主库及 WAL/journal 文件，恢复副本不参与日常备份清理。
- 两条升级路径都等待在途请求结束，超时则停止更新；请求进入与排空检查使用同一同步机制。
- 将 Qoder worker 的 `undici` 升级到 6.29.1，消除已报告的安全告警，代理与 worker 行为保持不变。
- 取消登录时结束等待；防止旧探测结果覆盖重启后的账号状态，并在浏览器登录完成后重新探测。
- 拼接 Qoder 非流式工具调用的分段参数，将 Trae 调用方 developer 指令转换为其 system 角色，并为 Trae 已完成的工具调用返回 OpenAI 工具调用结束标记。
- 将前端 `source-map-js` 依赖更新到 1.2.2，修复已报告的索引 source map 安全问题。
