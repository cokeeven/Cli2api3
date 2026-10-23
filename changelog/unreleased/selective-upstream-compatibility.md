### English

- Qoder streaming now stops reading at the upstream `[DONE]` marker, releases the converted stream when the client closes it, uses one completion ID per request, retains assistant reasoning history, and bounds the wait for the first meaningful event with the configured chat timeout.
- Codex preserves an explicit `parallel_tool_calls` boolean and sends `reasoning.context=all_turns` for Responses Lite models without discarding other reasoning fields.
- WorkBuddy keeps `max` when the selected model catalog advertises it and continues to reject unsupported caller-selected levels.
- The Overview adds Today, 30-day, and custom request-stat windows plus a pausable 30-second refresh that stops while the page is hidden.

### 中文

- Qoder 流式读取会在上游 `[DONE]` 标记处停止；客户端关闭转换流时会释放上游；每个请求使用独立 completion ID；保留 assistant 历史 reasoning；首个有效事件等待受现有聊天超时配置约束。
- Codex 保留调用方显式设置的 `parallel_tool_calls` 布尔值；Responses Lite 模型发送 `reasoning.context=all_turns`，并保留其他 reasoning 字段。
- WorkBuddy 仅在所选模型目录声明支持时保留 `max`，未支持的调用方档位仍会被拒绝。
- 概览页新增“今天”、30天和自定义请求统计时间范围，并支持暂停的30秒自动刷新；页面隐藏时停止该定时器。
