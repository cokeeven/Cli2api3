### English

- Add atomic live chat admission for shared account limits and per-key platform limits, held through stream completion, cancellation, and cleanup. Configure platform priorities separately from existing key grants; new sessions without preferences choose platforms uniformly, and cross-provider failover defaults off.
- Preserve session affinity during temporary capacity overflow and migrate it after confirmed quota failures. Retry only permitted accounts serving the same canonical model, without replaying an already emitted response.
- Add opt-in account credit/token guards with transactional reservations, per-attempt settlement, conservative unknown-charge recovery, fresh-snapshot reconciliation, and credit-package expiry analysis. WorkBuddy resource queries paginate and distinguish deduction expiry from billing-cycle boundaries.

### 中文

- 新增聊天实时并发原子预留，同时约束所有密钥共享的账号上限和单个密钥的平台上限，流式结束、取消及清理时释放。平台优先级与现有授权围栏分开配置；未配置偏好的新会话按平台均匀随机，跨供应商故障切换默认关闭。
- 临时并发满额借用账号时保留原会话粘性，明确到量后成功切号才迁移绑定。仅重试授权范围内提供相同标准模型的账号，已输出的响应不重放。
- 新增默认关闭的账号积分与 Token 护栏，事务预留、逐次尝试结算、未知扣费保守恢复、新鲜余额快照人工对账及积分包临期分析。WorkBuddy 积分查询支持分页，区分抵扣截止与计费周期边界。
