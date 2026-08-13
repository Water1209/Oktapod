# Oktapod

> **V1.0.0 Release Candidate：本地优先、可控、可审计、可恢复的智能开发 Agent**

Oktapod V1 面向注册用户的个人私有项目。Work/Code 只决定工作流、Prompt 和默认界面，两种场景都使用同一套 Workspace、脚本、Git、Skill、MCP 和 Artifact 执行内核，审批只由权限模式与 ExecutionPolicy 决定。用户在一个 Thread 中描述目标，Agent 会先生成目标特定的语义执行计划，再读改文件、运行验证、展示 Diff 和请求审批；运行中可直接说“暂停当前任务”“取消当前任务”“继续当前任务”，也可说“继续当前任务，并……”把新约束合入后继 Run，原目标、显式 Skill、已确认收据和工作区改动不会丢失。只要本次发送选择了附件，Auto 就会进入 Task、冻结附件快照并通过 `asset_stage` 读取二进制正文；未成功读取所选二进制附件的任务不能完成。任务只有在真实 Tool Receipt、变更后验证和最新交付 Artifact 满足目标与验收条件后才会完成；模型只说“已完成”不会改变任务状态，用户要求 PDF 时必须有经 `%PDF-` 签名、MIME、Hash 和大小校验的新 Artifact，已生成但未登记的文件不能冒充交付完成。文件页将可变“工作区”、上传或保存复用的“项目文件”和不可变“任务输出”分开；历史任务遗漏登记的文件只在用户明确选择任务后“保存为本次任务输出”，不自动猜测归属；“保存到项目文件”只供同项目后续任务复用，不向项目外发布。Changes 仅表示 Git 派生差异。新项目默认任务保险丝为 80 次模型调用与 1,000,000 Token，单次输出按 8,192→16,384→32,768 Token 有界恢复，项目可改为不限额。Thinking 选择冻结到 Run 并映射到 Provider，支持推理内容的工具轮续接、推理/参数截断恢复和受限输出扩容。资源中心或自然语言可触发同一套通用 MCP 来源解析、版本固定、校验、确认安装、审核、连接与调用流程，本地 stdio 和远端 Streamable HTTP 使用统一定义，多项凭据只进入 SecretStore；标准远端 OAuth 由官方 MCP Go SDK 完成浏览器授权、回调、取消和刷新令牌持久化，手工 token 使用 `bearer`。Skill 菜单安装仅接受经过结构与安全检查的单 Skill ZIP，内置 Skill Creator 通过对话生成草稿但不能绕过安装审批。只有 Provider 明确声明不支持工具时才会限制 Task 模式。完全访问会自动执行确定性风险为 L0-L2 的动作，包括已启用、已冻结的只读 MCP 检索；L3、硬拒绝、路径与命令黑名单仍不放行。自动审查只处理 Policy 判定为 `ask` 的 L2 动作；未配置可用 Reviewer 时界面禁用该模式并回退到人工审批。确定性策略拒绝时自动换用允许方案，模型临时断线或工具批次已提交但续接未入队时仍可在原 Thread 恢复。团队项目与 PostgreSQL 保留兼容边界，不进入 V1 发布完成度。

## 快速开始

环境要求：Node.js `>=22.12.0`、pnpm `10.28.2`、Go `1.26.5`。

```powershell
pnpm install --frozen-lockfile
pnpm build
.\Build\oktapod.exe serve
```

浏览器访问 `http://127.0.0.1:8787/`，首次使用在登录页注册本地账户。默认服务仅监听本机并使用 SQLite；Provider Key 只通过 SecretStore 或环境变量注入。

首页是任务工作台，“项目”固定进入项目库；项目标题可直接展开切换项目，项目文件与 Git 变更只在右侧任务面板查看，会话分支从对话“…”菜单进入。设置只保留真正生效的“模型服务、外观”：Provider 的 API 目录与用户启用列表分离，只有已启用模型能被项目、会话和 Agent 选择，连接配置可用“测试连接”即时验证；主题支持跟随系统、浅色和深色并即时生效。头像只打开账号菜单，退出需要在菜单内再次明确操作。

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
