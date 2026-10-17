### English

- Return provider-qualified model IDs and display names from `/v1/models`, so clients can distinguish and select models such as `workbuddy/glm-5.3` and `trae/glm-5.3`. Keep provider permissions, upstream aliases and internal model settings intact; existing bare-ID calls remain governed by the cross-provider pool setting.

### 中文

- `/v1/models` 返回带平台前缀的模型 ID 和显示名，让客户端区分并选择 `workbuddy/glm-5.3`、`trae/glm-5.3` 等模型。保留平台权限、上游别名和内部模型设置；已有无前缀调用仍遵守跨平台模型池开关。
