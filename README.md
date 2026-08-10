# Oktapod

> **V1.0.0 Release Candidate：本地优先、可控、可审计、可恢复的智能开发 Agent**

Oktapod V1 面向注册用户的个人私有项目。用户在一个 Thread 中描述目标，Agent 会先生成目标特定的语义执行计划，再读改文件、运行验证、展示 Diff 和请求审批；运行中可直接说“暂停当前任务”“取消当前任务”“继续当前任务”，也可说“继续当前任务，并……”把新约束合入后继 Run，原目标、显式 Skill、已确认收据和工作区改动不会丢失。任务只有在真实 Tool Receipt、变更后验证和最新交付 Artifact 满足目标与验收条件后才会完成；模型只说“已完成”不会改变任务状态，用户明确的搜索次数上限也由运行时执行而非只靠提示词。Thinking 选择冻结到 Run 并映射到 Provider，支持推理内容的工具轮续接、推理/参数截断恢复和受限输出扩容。资源中心或自然语言可触发同一套通用 MCP 来源解析、版本固定、校验、确认安装、审核、连接与调用流程，本地 stdio 和远端 Streamable HTTP 使用统一定义，多项凭据只进入 SecretStore；标准远端 OAuth 由官方 MCP Go SDK 完成浏览器授权、回调、取消和刷新令牌持久化，手工 token 使用 `bearer`。Skill 菜单安装仅接受经过结构与安全检查的单 Skill ZIP，内置 Skill Creator 通过对话生成草稿但不能绕过安装审批。只有 Provider 明确声明不支持工具时才会限制 Task 模式。完全访问会直接开放工作区命令并取消重复授权，但仍受路径、命令黑名单和 MCP 开放世界确认约束；其他模式缺少可授予权限时进入审批。确定性策略拒绝时自动换用允许方案，模型临时断线或工具批次已提交但续接未入队时仍可在原 Thread 恢复。团队项目与 PostgreSQL 保留兼容边界，不进入 V1 发布完成度。

## 快速开始

环境要求：Node.js `>=22.12.0`、pnpm `10.28.2`、Go `1.26.5`。

```powershell
pnpm install --frozen-lockfile
pnpm build
.\Build\oktapod.exe serve
```

浏览器访问 `http://127.0.0.1:8787/`，首次使用在登录页注册本地账户。默认服务仅监听本机并使用 SQLite；Provider Key 只通过 SecretStore 或环境变量注入。

主题在“设置 → 外观”切换，支持跟随系统、浅色和深色；选择会立即作用于整个工作区并在刷新后保留。

开发模式：

```powershell
# 终端 1
go run ./Src/cmd/oktapod serve
# 终端 2
pnpm dev
```

## 发布构建

```powershell
pnpm release:build
pnpm release:smoke
.\Build\oktapod-v1.0.0-windows-amd64.exe version
```

发布构建会生成带版本的本机二进制和 `.sha256` 校验文件；普通 `pnpm build` 仍是开发构建，运行时版本显示为 `dev`。

## 文档

| 文档 | 用途 |
|---|---|
| [V1 发布说明](Doc/Oktapod_V1_发布说明_V1.0.0.md) | 发布范围、已知限制、安装与回滚 |
| [V1 总体架构](Doc/Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md) | 当前架构事实与演进边界 |
| [V1 执行计划](Doc/Oktapod_V1_执行计划与验收门禁_V1.0.md) | 差距批次、门禁与剩余工作 |
| [V1 验收矩阵](Doc/Oktapod_V1_验收状态与追踪矩阵.md) | 发布完成度唯一真源 |
| [部署与运维](Doc/Oktapod_部署与运维指南_v1.0.md) | Windows/Linux 原生发布、Provider 与运维 |
| [PRD v1.1](Doc/Oktapod_PRD_v1.1_轻量化架构优化版.md) | 历史完整产品蓝图 |
| [三阶段架构](Doc/Oktapod_三阶段递进式架构设计_V1.0.md) | V1 之后的团队与集群演进 |

## 验证

```powershell
$env:GOCACHE="$env:TEMP\go-build-cache"
pnpm verify
pnpm test:e2e
```

Ubuntu CI 额外执行 `pnpm test:race`；Ubuntu 与 Windows CI 都执行原生发布构建和产品烟测。项目规则见 [AGENTS.md](AGENTS.md)。
