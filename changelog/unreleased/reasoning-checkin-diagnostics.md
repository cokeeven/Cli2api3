### English

- Match WorkBuddy and Trae reasoning effort against the selected account's model catalog, retain caller intent, and show the requested and resolved levels in request history. Unsupported or unknown reasoning settings remain explicit errors.
- Distinguish check-in outcomes with bounded, redacted status evidence. A Trae 9074 rejection does not imply chat authentication failure or trigger an automatic same-day retry; status rule amounts are not reported as confirmed rewards.
- Add safe local tool-history validation diagnostics without recording messages, tool arguments, or call identifiers.

### 中文

- 根据实际账号的 WorkBuddy、Trae 模型目录匹配思考档位，在请求记录中展示请求与实际档位；无法确认的模型能力或未知设置明确报错。
- 区分签到结果，记录有限且脱敏的阶段、状态和业务码；Trae 9074 不代表聊天认证失败，也不会触发当天自动补跑；状态接口中的规则金额不会被当作已到账奖励。
- 为本地工具历史校验补充安全诊断，不记录消息正文、工具参数或调用标识。
