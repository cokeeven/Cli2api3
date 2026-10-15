---
id: cli2api-architecture-summary
title: CLI2API Architecture Summary
scope: [backend, runtime, providers, routing, console]
status: canonical
read-when: 需要快速理解后端分层、请求路径、运行时和 provider 扩展边界时
summary: 面向贡献者和 AI 的精简架构摘要；详细协议、里程碑和本地运维资料不在本文维护。
related: [AGENTS.md, CONTRIBUTING.md, docs/DESIGN.md, docs/REFACTORING.md, docs/NATIVE_PROTOCOL_REQUESTS.md]
last-updated: 2026-09-24
---

# CLI2API 架构摘要

本文是给贡献者和 AI 使用的快速入口，不替代代码、测试和本地详细资料。
行为以当前代码和测试为准；包边界以 `AGENTS.md` 与 `docs/REFACTORING.md` 为准。

## 产品边界

CLI2API 是一个面向个人部署的 Go + Node 网关：

- 对外提供 OpenAI Chat Completions、Anthropic Messages、OpenAI Responses 和 Models 兼容接口。
- 管理多个上游账号，并按 provider、region、model、pin、会话粘性和冷却状态选号。
- Qoder 登录、目录和配额仍使用每账号隔离的 Node child process；Qoder 聊天在 Go 进程内签名直连网关。WorkBuddy、Trae CN Work 和 Devin 使用 Go 进程内 adapter。
- 控制台负责账号、密钥、设置、目录、日志、登录、导入和更新操作。
- 这是个人网关，不实现计费、Redis 槽位、多租户商业网关或公开暴露的管理端口。

## 运行时分层

```text
cmd/server
  └── internal/app       进程组装、依赖注入
        ├── internal/server   路由、CORS、OPTIONS、maintenance、webui
        ├── internal/gateway  公有 /v1 协议 HTTP 与 SSE
        ├── internal/console  操作员 /api/* HTTP
        ├── internal/control  控制面编排与目录展示缓存
        ├── internal/runtime  账号生命周期、Qoder worker、刷新与维护循环
        ├── internal/executor 选号、请求准备、分类、冷却与 failover
        ├── internal/store    SQLite、迁移、持久化请求记录
        ├── internal/providers provider 注册、协议客户端、adapter
        └── internal/update   托管更新协调器
```

`internal/api` 仅是测试兼容门面，生产启动路径不使用它，也不能在其中新增业务。

## 请求路径

1. `server` 将公有 `/v1/*` 请求交给 `gateway`，将控制台 `/api/*` 请求交给 `console`。
2. `gateway` 校验并解析 OpenAI、Anthropic 或 Responses 输入，转换为公共请求模型。
3. `executor` 根据显式账号 pin、会话粘性和账号池选择 runtime，并执行请求准备。
4. provider adapter 或 Qoder worker 负责上游协议、认证、HTTP/SSE 或 Connect-RPC。
5. `executor` 负责错误分类、冷却、重试和 failover；`gateway` 只负责公共协议响应和流式输出。
6. 请求元数据写入日志；不要默认保存 prompt、completion、密钥或凭证内容。

公共协议和 provider 上游协议必须分离。不要在 `gateway` 中拼 provider payload，
也不要让 provider 包接收 `http.ResponseWriter` 或决定 executor 的冷却策略。

## 包职责

| 包 | 负责 | 不负责 |
|---|---|---|
| `accounts` | 账号实体、凭证契约、纯账号规则 | Pool、Manager、HTTP handler |
| `store` | SQLite repository、不可变 migration、持久化 | 路由策略、密钥业务生成 |
| `control` | 账号/密钥/设置/登录/导入/目录编排 | 公有协议转换、provider payload |
| `runtime` | 账号生命周期、进程表、刷新、维护循环 | 公共协议和选号策略 |
| `executor` | Pool、选号、prepare、分类、冷却、failover | SQLite、HTTP handler、具体 provider policy |
| `gateway` | `/v1/*` 校验、转换、SSE 和公共错误 | store、runtime Manager、具体 provider |
| `console` | `/api/*` 解码、授权、错误映射 | store、runtime Manager、具体 provider |
| `server` | 路由和 web UI | 业务策略、store、runtime Manager |
| `app` | 组装依赖和启动关闭 | 领域业务规则 |

## Provider 边界

- Qoder Global 和 Qoder CN 是同一个 `provider=qoder`，通过 `region` 区分，不创建新的 provider family。
- Qoder 登录仍使用独立 HOME 和独立 child process，不共享 WASM context；聊天不加载 CLI bundle。不得为每个请求启动完整 CLI agent。
- Qoder 的 CLI / worker 兼容性版本固定在 `worker/src/compat.mjs`，不兼容时应明确失败。
- Qoder 聊天改为在 Go 进程内使用 COSY 客户端签名直连网关（[`internal/providers/qoder/cosy.go`](../internal/providers/qoder/cosy.go)）。COSY 协议版本固定在 [`COSYVersion = "1.1.32"`](cosy.go:23)，支持中国区（`gateway.qoder.com.cn`）和全球区（`api1.qoder.sh`）双端点。
- WorkBuddy、Trae CN Work、Devin 使用进程内 adapter，不复制 Qoder worker 生命周期。
- provider 负责上游事实映射；executor 负责是否切号、冷却多久和是否 failover。
- Provider 能力、模型目录和 reasoning level 必须以实际 catalog 声明为准，不凭空增加模型能力。

## 持久化和安全边界

- SQLite migration 一旦发布，SQL 字节不可修改；新增 schema 必须追加新的编号 migration。
- 控制台密钥、客户端密钥和上游凭证是不同的认证边界，不能混用。
- API 返回中不得暴露原始 token、auth blob、密钥明文、host 路径或凭证 payload。
- 管理接口必须认证；默认只绑定本机，不要直接暴露 host 端口到公网。
- 不提交 `.env`、auth blob、token、raw capture、主机 IP、私有部署 runbook。

## WorkBuddy 日常维护

- `providers.DailyTasksReport` 与可选 `DailyTasksRunner` 承载结构化步骤事实；runtime 不导入 WorkBuddy 包，原有字符串摘要接口保留兼容。
- `accounts.DailyTaskStore` 定义持久化契约，SQLite migration 023 追加运行表与账号执行租约。运行结果、账号签到状态和签到历史在同一事务提交。
- runtime 负责同账号互斥、90 秒执行期限、最多四账号并发，以及每晚 22:00 后的持久化去重和有限补跑。进程退出先取消并等待维护任务，再由 app 关闭数据库。
- 控制台的 `POST /api/accounts/{id}/daily-tasks` 返回运行状态和步骤报告，HTTP 成功表示取得运行报告，不表示所有步骤成功；同路径 GET 读取持久化历史。两者均使用管理员鉴权。
- 前端操作位于 React 账号卡片，历史弹窗显示失败与跳过原因；通过正常静态同步构建交付，不向产物额外注入脚本。
- 国内个人账号流程在刷新令牌前判断地区和企业资格；先完成活跃、Buddy 前置及日常奖励，再推进其余成长任务。逐任务记录 `growth:<task_code>`，仅复用读回确认的已领奖步骤，不用历史成长汇总阻断后续任务。每轮最多 20 个事件和五次现有机会抽奖，真实桌面、公众号、捐赠及夜猫活动不伪报完成。
- `automation_maintenance_enabled` 是当前实例的自动签到、自动养号及补跑总闸，缺省开启以保留既有行为；设置不可读或损坏时停止自动任务。关闭时只取消自动维护上下文，不修改账号启用状态、各账号自动选项，不影响手动动作、聊天、登录或令牌刷新。
- 日志页的 `GET /api/logs/maintenance` 统一读取所有平台的签到和养号记录。migration 024 追加签到触发来源及养号父记录关联；新养号内的签到只作为子步骤展示，旧记录保持未知来源且不按时间猜测归属。control 对历史消息作安全投影，console 只解析筛选及返回结果。
- 手动养号结束仅显示结果提示，不自动打开历史弹窗；详情保留在账号菜单与日志页供手动查看。

## 聊天路由与用量护栏

- 账号与 Key 的平台调度合同在 `accounts`；既有 provider/region grants 仍是权限边界。Key 的平台优先级和平台并发限制按 provider + region 生效，不按同名模型另建权限规则。
- executor Pool 在一次锁内选择并预留共享账号及 Key 平台名额，健康探测值不覆盖请求持有的计数。流式响应由 gateway 传回终态与 usage，executor 幂等结算并释放；HTTP 200 响应头不代表一次聊天执行成功。
- 会话粘性先于新会话平台优先级；临时容量借号不永久改绑，明确到量切号后的成功结果通过版本比较迁移绑定。未配置平台优先级的新会话按平台随机，平台内保留账号调度策略。跨平台故障切换默认关闭，显式 provider 前缀和模型身份约束始终生效。
- `accounts.UsageBudgetStore` 将事务预留与逐尝试结算注入 executor；executor 不导入 SQLite。store 保存账号独立护栏、预留责任、积分余额快照历史和扩展请求尝试日志。未知扣费保留责任，重启不恢复聊天活动计数；人工快照对账不伪造实际请求费用。
- 四项账号护栏默认关闭。严格 Token / 积分限制需要可信上界，未知成本不当作零；积分倍率和余额变化不直接推导单次请求费用。临期偏好不越过平台选择和会话粘性，计费周期边界不视为已确认的积分作废时间。

## 原生协议请求设计

三个公共入口与上游协议的对应关系、双路径分派及 Responses 原生请求的演进方案，见 [原生协议请求架构与渐进改造方案](NATIVE_PROTOCOL_REQUESTS.md)。
当前 Codex 已实现可选原生 Responses 能力；Chat 和 Messages 仍经 `ChatRequest` 兼容路径。服务端对话存储等后续设计不属于已实现能力，具体范围以该文档和当前代码为准。

## 修改前检查

- 先读 `AGENTS.md`，再按任务需要读 `DESIGN.md`、`CONTRIBUTING.md` 或本摘要。
- 修改路由、冷却、粘性或错误分类时，以当前代码和测试为准，并检查 `executor` 边界。
- 修改 provider 时，不要绕过 adapter/runtime 契约，也不要把协议策略塞进公共 handler。
- 修改控制台 UI 时遵守 `docs/DESIGN.md`，并运行 `cd frontend && npm run sync`。
- 修改行为后运行受影响包测试，再运行 `go test ./...`；检查 `git diff --check`。

详细 provider 协议、真实账号验收、部署主机信息和抓包记录属于本地资料，
不作为本公共摘要的实现来源。
