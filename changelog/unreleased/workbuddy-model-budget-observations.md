### English

- Show bounded WorkBuddy account/model budget observations with separate progress bars for complete reset-window usage and the 24-hour comparison, plus first-limit samples and weaker peer references; unknown upstream budgets remain unknown and do not affect routing.
- Keep the selected 24-hour comparison reference fixed when observed use exceeds it, label the overrun, and retain the sample window and estimate time. For WorkBuddy chat availability failures with no provider scope, isolate only the local cooldown to that account/model; explicit account failures such as expired sessions remain account scoped.
- Offer an explicit local per-account, per-model daily token guard from repeated complete evidence while preserving any existing model limit.

### 中文

- 展示有界的 WorkBuddy 账号/模型预算观测，并以独立进度条显示完整重置窗口用量和24小时比较，另行展示首次限额样本及较弱的同类参考；实际上游预算未知时继续显示未知，且不影响路由。
- 当实际观测超过所选的24小时比较参考时，保留原参考值并明确标记超出，同时展示样本区间和估算时间。WorkBuddy 聊天阶段未提供范围的可用性错误，仅在本地按账号/模型隔离冷却；明确的账号级错误（如会话失效）仍作用于账号。
- 仅在操作者明确保存时，才可依据重复完整样本设置本地每账号/模型日 Token 护栏；已有模型限额会保留。
