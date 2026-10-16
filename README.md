<div align="center">

<h1><img src="./frontend/public/apple-touch-icon.svg" width="40" height="40" align="top" alt=""> ali2api3</h1>

**把你自己的登录态，变成一个本机 OpenAI 兼容 API**

接入 **Qoder（国际版 / 国内版）**、**WorkBuddy（国际版 / 国内版）**、**Trae 国内版 Work**，以及实验性的 **Devin** 和 **Command Code**。

用 Docker 部署，在 Web 控制台管理账号，再通过兼容 API 接入客户端。

[![License](https://img.shields.io/github/license/cokeeven/ali2api3)](LICENSE)
[![LINUX DO](https://img.shields.io/badge/LINUX%20DO-community-ff6a00)](https://linux.do)

<sub>[English](README_EN.md) · [问题反馈](https://github.com/cokeeven/ali2api3/issues) · [LINUX DO](https://linux.do)</sub>

<img src="./docs/assets/readme/hero-zh.svg" width="100%" alt="CLI2API — 把你自己的登录态，变成一个本机运行的 OpenAI 兼容 API">

</div>

本仓库以 **ali2api3** 发布，基于 [caigee-cmd/cli2api](https://github.com/caigee-cmd/cli2api) 继续开发，保留上游 MIT 许可证及版权声明。新增功能包括按 Key 的平台优先级和并发限制、账号用量护栏与积分有效期分析、WorkBuddy 国内养号闭环，以及签到结果确认。Go module 名称仍沿用上游以保持包路径兼容。

此分支请从源码构建；下文上游镜像和托管更新的说明不代表已包含本仓库改动。公开仓库不包含账号数据、凭证或本机实例配置。

## 功能

- **兼容接口**：Chat Completions、Responses、Anthropic Messages 和模型列表，支持流式回复与函数工具调用。
- **多账号调度**：自动选号、会话粘性、并发限制、冷却与故障切换。
- **统一控制台**：管理账号、模型、客户端密钥、代理设置，查看额度和请求日志。
- **Docker 运维**：持久化账号数据；可选安装宿主机更新器，在控制台下载更新、确认升级和回滚。

Qoder 国内版、WorkBuddy、Trae 的适配代码已实现；WorkBuddy 国内版和 Trae 国内版已有部分真实账号验收，具体范围与待验收分支见下文；Devin 与 Command Code 为实验性接入，不承诺生产可用。托管更新的真实升级 / 回滚验收也尚未完成。

## 快速开始

需要 Docker 和一个你自己控制的上游账号。macOS / Windows 使用 Docker Desktop；Windows 需使用 Linux containers。

```bash
git clone https://github.com/cokeeven/ali2api3.git
cd ali2api3
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml logs --tail=50
```

1. 保存首次启动日志中打印的**管理员密钥**。
2. 打开 `http://127.0.0.1:3010`，用管理员密钥登录，在 **Accounts** 添加账号。
3. 在 **API keys** 创建客户端密钥，在 **Access** 选择模型并测试。

客户端密钥的编辑窗口可单独设置**平台调度**：优先级越高越先使用，平台并发上限按该密钥在此平台的所有模型合计。原有平台授权范围仍决定能用哪些平台；没有调度配置的新会话随机选择平台，平台内使用账号调度策略。已有会话优先复用账号，临时满额只借用同平台的其他账号。模型限额或明确的积分不足拒绝会尝试同平台同模型的下一个账号；跨平台故障切换默认关闭，显式的平台模型前缀始终限制平台。

账号菜单的**用量与护栏**可配置保留积分、每日 Token、每日积分和单模型每日 Token 限额，并查看临期积分包与逐日用量。所有护栏初始关闭。开启硬护栏后，无法证明请求成本上界的付费请求会被拒绝；积分倍率本身不作为可靠报价。弹窗同时标明所选平台实际支持哪些护栏：目前支持的平台都没有可信的单次请求积分成本；Token 上界只在平台不会超出客户端上限时存在（Trae 与 Codex 的 Responses 路径保持未知）。实际用量、估算预留和未知扣费分别显示，取消或中断不自动当作免费。人工对账只确认未知责任已被指定的新余额快照覆盖，不会生成虚假的请求积分成本。积分临期优先仅在已选平台的可用账号内生效，且不覆盖会话粘性。

Docker Compose 是支持的安装与托管更新路径；源码运行用于开发。详细步骤见 [部署说明](deploy/README.md)。

## 接入客户端

在支持 OpenAI 兼容 API 的客户端中填写：

```text
Base URL: http://127.0.0.1:3010/v1
API Key:  <在 API keys 页面创建的客户端密钥>
```

模型 ID 从 **Access** 页面或 `/v1/models` 获取。默认自动选择账号，同一对话优先复用原账号。固定账号、自定义会话标识及 curl / PowerShell 示例见 [部署说明](deploy/README.md)。

## 工作方式

<p align="center">
  <img src="./docs/assets/readme/architecture-zh.svg" width="100%" alt="CLI2API 架构：OpenAI 客户端经 Go 控制面路由到每账号独立运行时，再连接各 provider 上游">
</p>

Go 网关统一鉴权、调度和记录请求。Qoder 每个账号使用独立 Node 进程与 HOME；WorkBuddy、Trae、Devin、Command Code 使用 Go 进程内适配器，不为每次请求启动完整 CLI。

## 控制台

<p align="center">
  <img src="./docs/assets/readme/console-window-zh.svg" width="100%" alt="CLI2API 控制台 Accounts 页：每个账号显示登录方式、就绪状态与额度，右侧 Access 面板提供 Base URL 与快速验证">
</p>

账号、模型、接入和日志都在同一个 Web 控制台里管理：就绪状态和额度一目了然，Access 页可以直接复制 Base URL 并做一次快速验证。

在「账号」卡片直接操作 WorkBuddy 与 Qoder 国内版、国际版的签到、自动签到开关（默认关闭），并从「更多 → 签到记录」查看历史，不另设签到中心。在「系统 → 自动签到」配置各供应商默认时间，「编辑账号」可选择跟随默认或自定义；修改默认时间会立即影响未覆盖的账号。已有 WorkBuddy 账号保留原时间，可在编辑账号时切回跟随默认。活动关闭时只记录跳过，各平台和活动分支的真实验收范围分别记录。Trae 国内签到区分账号已签与设备受限；只有本次领取且上游回查确认，才报告新增奖励，重复点击不会复用旧成功结果。

WorkBuddy 国内个人账号卡片提供「立即养号」和「更多 → 养号记录」，逐项显示成功、已完成、跳过或失败。流程包含签到、每日活跃、Buddy 前置任务、成长任务接取与进度推进、猫猫旅行、昨日漏签补签、连登兑换、现有机会抽奖、礼包及补偿。进度、领奖、补签和兑换须读回上游状态确认；补签只使用已有补签卡，抽奖每轮最多五次，剩余机会留待有限补跑。需要真实桌面操作、公众号关注、捐赠及夜猫子跨天活动的任务明确跳过，不生成付费对话；本流程不支持国际版和企业账号。

养号结束仅显示简短结果提示，不再自动打开记录。可手动使用账号「更多 → 养号记录」，或在「日志 → 签到与养号」汇总查看所有平台的独立签到、养号与补跑，按账号、平台、操作、状态、触发方式和时间筛选，点击详情查看步骤。新记录会把养号中的签到关联到所属执行，避免重复展示；旧独立签到记录保留，无法确认的触发方式显示「未知」。查询日志不会触发上游任务。

「日志 → 签到与养号」中的「全平台自动签到与养号」是当前实例的自动任务总开关，默认开启以保留现有行为。关闭会暂停自动签到、养号及补跑，保留各账号原有选项及启用状态；重新开启后按原有设置恢复。手动签到、养号以及聊天、登录、令牌刷新不受影响。不同数据库实例的开关和日志各自独立。

总开关开启时，开启自动签到的已启用 WorkBuddy 国内账号，每晚服务器本地时间 22:00 后参与养号。运行记录和签到结果保存在同一 SQLite 数据库中；执行租约防止同账号并行运行，成功记录用于重启去重。失败按 5／10 分钟间隔补跑，每个自然日最多自动尝试三次，按任务跳过当日已确认完成的步骤；每账号独立限时 90 秒，最多并行四个账号，每轮最多上报 20 个成长事件，事件之间至少间隔一秒。手动执行不受自动签到开关限制。数据库彼此独立的实例不会共享记录或执行租约；复制同一账号无法隔离上游奖励或刷新令牌的副作用，真实验收必须串行。

2026-10-15 国内个人账号已通过查询接口、成长推进及领奖读回、结构化运行记录落库的真实验收。昨日补签、已解锁连登兑换、非零抽奖次数及旅行出发／到站领奖已有本地回归测试，仍待满足这些条件的真实账号验收；测试通过不代表所有活动分支均已实测或已部署。

## 使用限制

- 只连接你自己的账号，不提供账号、额度或官方 API 服务。
- 兼容接口不等于完整复刻官方 API。Messages / Responses 为无状态适配，不保存服务端会话，也不执行上游专属工具。
- 图片能力取决于上游和模型；WorkBuddy / Trae 不支持图片，文件输入会明确拒绝。具体范围见 [部署说明](deploy/README.md)。
- 跨上游同名模型调度受系统开关、账号权限和地域约束；不是任意账号之间都能切换。
- 上游协议变化可能影响兼容性；请求日志默认不保存提示词或回复正文。

## 文档

- [部署与运维：启动步骤、环境变量、接口、托管更新](deploy/README.md)
- [变更记录](CHANGELOG.md)

## 安全

默认只向本机开放 `127.0.0.1:3010`，不要直接暴露到公网。管理员密钥可操作控制台；客户端密钥仅用于 `/v1/*`。请妥善保管密钥、账号导出文件和数据库备份，发现问题按 [SECURITY.md](SECURITY.md) 私下报告。

## 社区与贡献

中文讨论见 [LINUX DO](https://linux.do)。缺陷和功能请求请走 GitHub [Issue](https://github.com/cokeeven/ali2api3/issues)；文档改进与 Pull Request 见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 致谢

感谢 `workbuddy2api`、`CLIProxyAPI`、`sub2api` 等开源项目提供的思路与启发。

## 许可证

[MIT](LICENSE)。使用上游账号时，请遵守对应平台的服务条款。
