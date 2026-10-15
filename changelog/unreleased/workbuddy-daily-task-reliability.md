### English

- Report WorkBuddy daily-task failures and skipped rewards accurately, including per-task batch acceptance results; preserve confirmed steps when retrying.
- Persist task history and check-in results atomically, with per-account execution leases, restart deduplication, bounded retries and independent account timeouts.
- Move daily-task actions into the account cards and add bilingual run history with step results, replacing the injected floating button.
- Extend cooldowns on the first repeated failure, including on Windows clocks with coarse time resolution.
- Correct CN growth endpoint contracts and add verified task progress, Buddy prerequisites, missed-day makeup, streak redemption and bounded lottery settlement; exclude Global and enterprise accounts before refresh.
- Show the current console port on the login page instead of the default port.
- Enable manual WorkBuddy maintenance from runtime login readiness instead of the legacy auth type; explain disabled actions without changing account data.

### 中文

- 准确展示 WorkBuddy 养号任务失败与奖励跳过状态，逐项核对批量接取结果，补跑时保留已确认完成的步骤。
- 原子保存养号历史和签到结果，加入账号执行租约、重启去重、有限重试与独立账号超时。
- 将养号操作整合到账号卡片，新增双语运行历史及步骤结果，替代注入式悬浮按钮。
- 首次重复失败即延长冷却时间，并兼容 Windows 时钟精度下的退避校验。
- 修正国内成长接口契约，接入须读回确认的任务推进、Buddy 前置、昨日补签、连登兑换和有限抽奖结算，在刷新前排除国际版和企业账号。
- 登录页显示当前控制台端口，不再固定显示默认端口。
- 按运行时登录就绪状态启用 WorkBuddy 手动养号，不再依赖旧认证类型，并说明按钮禁用原因，不修改账号数据。
