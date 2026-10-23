### English

- Classify WorkBuddy authentication failures from structured evidence and show sanitized phase, source, and observed HTTP status in request attempt details; the additive schema fields remain unknown for older attempts.
- Explain missing Trae local credentials and malformed tool history without exposing credential values or repairing incomplete tool arguments.
- Keep an existing WorkBuddy or Trae upstream authentication failure until an explicit login or credential refresh records recovery; checking only local credential completeness no longer marks the account ready.
- Preserve the total timeout phase on worker attempts, distinguish it from caller cancellation, and release the attempt slot once. Expired account cooldowns no longer hide current readiness; unknown cooldown deadlines remain unknown.
- Explain that historical `started` and `streaming` request rows lack a recorded terminal result but do not prove current execution; use the account runtime count for live in-flight requests.
- Keep credential refresh generations separate from the account modification timestamp. A successful WorkBuddy or Trae token exchange clears an earlier auth marker only when its credential payload and generation still match; this does not prove the chat endpoint is healthy.

### 中文

- 根据结构化证据分类 WorkBuddy 鉴权失败，并在请求尝试详情显示脱敏后的阶段、来源和实际观察到的 HTTP 状态；新增字段为旧尝试保留未知值。
- 明确说明 Trae 本地凭据缺失及工具历史格式错误，不暴露凭据，也不自动修补不完整的工具参数。
- WorkBuddy 或 Trae 已有上游鉴权失败时，本地凭据完整性检查不会再把账号标为就绪；只有显式登录或凭据刷新记录恢复后才重新就绪。
- 工作进程 attempt 保留总超时阶段并与调用方取消区分，且只释放一次并发槽。过期账号冷却不再遮住当前就绪状态；冷却截止时间未知时仍保持未知。
- 说明历史 `started` 和 `streaming` 请求仅表示日志未记录终态，不能证明当前仍在执行；实时并发应查看账号运行时请求数。
- 凭据刷新代次独立于账号修改时间。WorkBuddy 或 Trae 的令牌交换成功后，只有凭据内容和代次仍匹配才会清除先前鉴权标记；这不代表聊天接口已验证可用。
