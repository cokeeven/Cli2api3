### English

- Report local concurrency saturation separately from upstream rate limits, add a bounded FIFO wait queue for eligible WorkBuddy/Trae requests, and show queue outcome and wait details in request diagnostics.
- Settle and release a retry's prior account slot and usage reservation before it joins the fair queue, so earlier waiters can use that capacity.

### 中文

- 将本机并发容量不足与上游频率限制分开；为符合条件的 WorkBuddy/Trae 请求增加有界 FIFO 等待队列，并在请求诊断中展示排队结果和等待信息。
- 重试重新进入公平等待队列前先结算并释放上次失败尝试的账号槽位和用量预留，避免占槽阻塞较早的等待请求。
