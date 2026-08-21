# Oktapod
> **V1.0.0 Release Candidate：本地优先、可控、可审计、可恢复的智能开发 Agent**
Oktapod V1 面向注册用户的个人私有项目。Work/Code 只决定工作流、Prompt 和默认界面，两种场景都使用同一套 Workspace、脚本、Git、Skill、MCP 和 Artifact 执行内核，审批只由权限模式与 ExecutionPolicy 决定。用户在一个 Thread 中描述目标，Agent 会先生成目标特定的语义执行计划，再读改文件、运行验证、展示 Diff 和请求审批；运行中可直接说“暂停当前任务”“取消当前任务”“继续当前任务”，也可说“继续当前任务，并……”把新约束合入后继 Run，原目标、显式 Skill、已确认收据和工作区改动不会丢失。只要本次发送选择了附件，Auto 就会进入 Task、冻结附件快照并通过 `asset_stage` 读取二进制正文；选择的 Memory 也会把实际 Prompt 载荷冻结到新 Run，后续编辑或删除不改变正在执行的上下文。未成功读取所选二进制附件的任务不能完成。任务只有在真实 Tool Receipt、变更后验证和最新交付 Artifact 满足目标与验收条件后才会完成；模型只说“已完成”不会改变任务状态。运行中取消会终止受控命令或 MCP 调用；已经开始但无法确认最终副作用的 Receipt 会进入 unknown 并禁止自动重试。文件页将可变“工作文件”、可复用“项目资料”和不可变“任务输出”分开；工作文件再按项目内容、输入与下载、过程文件、临时辅助和待登记输出归类，Agent 多文件任务必须先规划路径，临时文件进入当前 Run 的内部目录，交付仍以 Artifact 为唯一真源。新项目默认任务保险丝为 80 次模型调用与 1,000,000 Token。资源中心或自然语言可触发同一套 MCP 来源解析、版本固定、校验、安装、审核、连接与调用流程；资源诊断和列表使用同一登录 Scope，项目资源与系统能力不交叉组合筛选。Provider、MCP 多项凭据和 OAuth Token 统一进入 SQLite 数据库加密 Vault，操作系统凭据库和模型环境变量不再参与产品状态。自动审查固定到数据库中准确的 Provider + Model + 策略版本，不可用时关闭式转为人工审批。团队项目与 PostgreSQL 只保留兼容边界，不进入 V1 完成度。

任务流摘要以持久化 `Task.status` 为唯一状态真源，暂停、等待、失败、取消和完成不会因事件流停止而混淆。Changes 面板明确区分任务开始、Git HEAD 与暂存区三种基线，按文件展示增删统计、行号和折叠差异，撤销与提交仍只能通过原 Thread 的受治理工具链。工作文件中的嵌套缓存、依赖、测试覆盖率和虚拟环境目录统一进入默认收起的“临时辅助”，不删除旧文件。Task 与 Ask 统一消费 Provider 流式响应，不再由 Task 等待整包结果；模型传输分别限制连接、TLS、首包和流式静默阶段，首包或连续流式静默超过 60 秒即分类失败。单次 Task 模型 Turn 即使仍收到 reasoning 或心跳也最多等待 120 秒，避免“连接活着但没有可执行输出”的长时间假进展。DNS、连接、TLS、首包超时、整轮超时和流中断会写入脱敏诊断；网络类故障只从检查点自动重试 1 次，语义响应修复仍保留 3 次。

## 快速开始

环境要求：Node.js `>=22.12.0`、pnpm `10.28.2`、Go `1.26.5`。

```powershell
pnpm install --frozen-lockfile
pnpm build
New-Item -ItemType Directory -Force "$env:APPDATA\Oktapod" | Out-Null
Copy-Item Deploy\config.yaml.example "$env:APPDATA\Oktapod\config.yaml"
.\Build\oktapod.exe serve
```

浏览器访问 `http://127.0.0.1:8787/`，首次使用在登录页注册本地账户，然后在“设置 → 凭据与密钥库”生成、保存并确认一次性恢复口令。Vault 变为 active 后重新录入 Provider/MCP 凭据；Secret 只以 AES-256-GCM 应用层密文进入 SQLite。

配置中心按职责分为“模型服务、治理与审批、运行与上下文、费用与用量、凭据与密钥库、数据与备份、外观与偏好”。“运行与上下文”提供默认关闭的受控私网 `web_fetch`：只接受精确 IP + 端口白名单，新 Run 冻结策略 Hash，每 Run 首次访问一个 origin 必须人工批准；公网 Fetch 保持 80/443 与 DNS/Dial 防 SSRF 边界。Provider 的连接、API Key 与启用模型一次保存；Reviewer 不再混入模型连接页。诊断会列出具体缺失凭据及修复入口。启动配置加载顺序为 `--config > OKTAPOD_CONFIG_FILE > %APPDATA%/Oktapod/config.yaml > 内置默认值`；可用 `oktapod config doctor --json` 和 `oktapod vault status` 诊断。

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
| [V1.1 Agent Runtime 优化](Doc/Oktapod_V1.1_Agent_Runtime_优化执行计划.md) | 主 Agent Assembly、统一 ToolRuntime、兼容边界与合并门禁 |
| [部署与运维](Doc/Oktapod_部署与运维指南_v1.0.md) | Windows/Linux 原生发布、Provider 与运维 |

## 验证

```powershell
$env:GOCACHE="$env:TEMP\go-build-cache"
pnpm verify
pnpm test:e2e
```
Ubuntu CI 额外执行 `pnpm test:race`；Ubuntu 与 Windows CI 都执行原生发布构建和产品烟测。项目规则见 [AGENTS.md](AGENTS.md)。
