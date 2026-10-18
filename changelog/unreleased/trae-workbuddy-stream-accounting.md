### English

- Preserve Trae stream termination reasons and report incomplete streams as failures instead of successful completions.
- Map caller output limits and supported reasoning settings to WorkBuddy and Trae requests; reject explicit unsupported reasoning levels instead of silently lowering them, and expose sourced cache usage in OpenAI responses.
- Keep request message-shape history and add redacted stream diagnostics for finish reasons, cancellation stage, and phase timings.
- Add separate WorkBuddy/Trae chat deadlines, preserve known non-stream usage on failure without replay, and discard obsolete catalog tasks after account lifecycle changes.
- Extend the existing maintenance panel with credential/quota history, independent schedules, manual actions and stale quota indications. Optional process-scoped source fingerprints are disabled by default and only trust explicitly configured proxies.
- Release skipped quota work before it can block manual refreshes. Successful maintenance follows the current configured period; failed runs retain their persisted retry backoff.
- Let WorkBuddy/Trae failover reach eligible accounts beyond four within the configured account cap and total deadline; invalidate failed authentication affinity safely and retain candidate exclusions, upstream failures and final scheduling reasons in request details.

### 中文

- 保留 Trae 流的结束原因；未完整结束的流按失败记录，避免显示为成功完成。
- 将调用方的输出上限和受支持的推理设置映射到 WorkBuddy、Trae 请求；明确拒绝不受支持的推理强度，避免静默降低，并在 OpenAI 响应中提供有来源依据的缓存用量。
- 保留请求消息形状记录，并增加脱敏流诊断，展示结束原因、取消阶段和阶段耗时。
- 增加 WorkBuddy/Trae 分层聊天超时；非流式失败保留已知用量并阻止已产生输出后的重放；账号生命周期变化后丢弃失效目录任务。
- 在已有维护面板补充凭据/额度历史、独立排程、手动入口和额度陈旧提示。来源指纹默认关闭，仅在当前进程内关联，且只信任显式配置的代理。
- 跳过的额度任务不再阻塞后续手动刷新；成功维护按当前配置周期调度，失败任务仍保留持久化重试退避。
- WorkBuddy/Trae 在配置账号上限和总时限内可继续尝试第 5 个及之后的合格账号；安全解除认证失败账号的粘性，并在请求详情保留候选排除原因、上游失败与最终调度停止原因。
