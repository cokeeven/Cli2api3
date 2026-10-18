### English

- Add a protected, local model-usage view for WorkBuddy and Trae with bounded filters, request/attempt usage coverage, current route candidates, and linked request details.
- Add a manually selected, single-account short probe with a small explicit request budget, cancellation, concurrency limits, and validation of the observed account, visible output, upstream finish reason, and actual usage.
- Show WorkBuddy budget observations only from distinct 6004 events with explicit reset anchors and complete usage windows; keep Trae single-model balance unknown without a verified source.
- Store only an allowlisted public API path for new request logs; keep historical paths unknown, and require a known matching path and upstream-reported plan for cross-account references.
- Add disclosed three-UTC-day 6/3/1 estimates, per-account-balanced references, sample ranges and freshness; exclude explicitly non-billable refusals from usage gaps while retaining canceled attempts with actual usage.
- Explain classified upstream limits separately from caller API-key rejection, distinguish upstream resets from local retry times, and expose redacted technical details on demand.
- Record upstream finish reason and source on request logs for output-length diagnostics. Historical rows remain unknown.
- Give each WorkBuddy budget row a stable identity that includes its full account/model/plan/request-path grouping dimensions.
- Disclose narrowly-scoped Trae output-budget evidence in model entries and request diagnostics; keep untested models, missing usage, and incomparable token bases explicit without changing upstream requests or finish reasons.

### 中文

- 新增受保护的本地 WorkBuddy/Trae 模型用量页，提供有界筛选、请求与尝试用量覆盖、当前路由候选及关联请求详情。
- 新增管理员手动选择账号的短探测，采用明确的小请求预算，支持取消与并发限制，并校验实际账号、可见正文、上游结束原因和实际 usage。
- WorkBuddy 预算观测仅依据去重后的明确 6004、上游重置锚点及完整用量窗口；Trae 单模型余额没有已验证来源时保持未知。
- 新请求日志只记录白名单公开接口路径，历史路径保持未知；跨账号参考要求套餐快照和接口路径均已知且一致。
- 增加明确披露的近3个UTC统计日6/3/1加权、按账号均衡参考、样本范围与新鲜度；非计费拒绝不再误算为用量缺失，取消尝试中已确认的实际usage仍计入。
- 分开说明已分类的上游限制与调用方 API Key 拒绝，区分上游重置时间和本地重试时间，并按需展开脱敏技术详情。
- 请求日志记录上游结束原因及其来源，便于诊断输出长度终止；历史记录仍保持未知。
- WorkBuddy 预算行的稳定标识包含账号、地区、模型、套餐和请求路径全部分组维度，避免不同路径的样本行互相覆盖。
- 在模型目录和请求日志中披露有范围限制的 Trae 输出预算证据；未验证模型、缺失用量和不可比较口径保持明确未知，不修改上游请求或结束原因。
