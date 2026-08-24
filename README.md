# Oktapod

> **V1.0.0 Release Candidate：本地优先、可控、可审计、可恢复的智能开发 Agent**
Oktapod V1 面向注册用户的个人私有项目。Work/Code 只决定工作流、Prompt 和默认界面，两种场景都使用同一套 Workspace、脚本、Git、Skill、MCP 和 Artifact 执行内核，审批只由权限模式与 ExecutionPolicy 决定。用户在一个 Thread 中描述目标，Auto 会直接进入一次可恢复 Agent Turn，不再先消耗模型做路由分类；Agent 可以直接回答、直接调用工具，或在同一业务响应中提交语义计划与首批工具，多阶段推进也不产生独立计划模型往返。执行计划只保存面向结果的语义步骤，逐次工具调用、审批和 Receipt 只进入执行记录。运行中可直接说“暂停当前任务”“取消当前任务”“继续当前任务”，也可说“继续当前任务，并……”把新约束合入后继 Run，原目标、显式 Skill、已确认收据和工作区改动不会丢失。只要本次发送选择了附件，Auto 就会进入 Task 并冻结附件快照；PPTX/PDF/DOCX/XLSX 由可卸载预置 Skill 和签名 `document-runtime` 通过固定 `document_*` 工具处理，其他二进制继续使用 `asset_stage`。Runtime 缺失时资源明确显示 degraded，不静默生成解析脚本。选择的 Memory 也会把实际 Prompt 载荷冻结到新 Run，后续编辑或删除不改变正在执行的上下文。未成功读取所选二进制附件的任务不能完成。任务只有在真实 Tool Receipt、变更后验证和最新交付 Artifact 满足目标与验收条件后才会完成；模型只说“已完成”不会改变任务状态。运行中取消会终止受控命令或 MCP 调用；已经开始但无法确认最终副作用的 Receipt 会进入 unknown 并禁止自动重试。文件页将可变“工作文件”、可复用“项目资料”和不可变“任务输出”分开；工作文件再按项目内容、输入与下载、过程文件、临时辅助和待登记输出归类，Agent 多文件任务必须先规划路径，临时文件进入当前 Run 的内部目录，交付仍以 Artifact 为唯一真源。新项目默认任务保险丝为 80 次模型调用与 1,000,000 Token。资源中心统一管理预置、自定义和系统资源，列表与详情展示能力说明、真实工具或工作流、适用场景和治理边界；预置资源可停用、删除和手动恢复，用户删除状态不会因重启或升级被覆盖。Provider、MCP 多项凭据和 OAuth Token 统一进入 SQLite 数据库加密 Vault，操作系统凭据库和模型环境变量不再参与产品状态。删除 Provider 时，可变的项目、会话偏好、Agent 和治理配置会事务性切换到当前默认实例；历史 Run 的冻结 ModelRef 与审计不改写，运行时遇到已删除实例也会明确记录默认回退。自动审查固定到数据库中准确的 Provider + Model + 策略版本，不可用时关闭式转为人工审批。团队项目与 PostgreSQL 只保留兼容边界，不进入 V1 完成度。

任务流摘要以持久化 `Task.status` 为唯一状态真源，暂停、等待、失败、取消和完成不会因事件流停止而混淆。Changes 面板明确区分任务开始、Git HEAD 与暂存区三种基线，按文件展示增删统计、行号和折叠差异；内部 `.oktapod` 路径在后端排除，明确的运行缓存在前端单独折叠且不进入全选、撤销和提交，所有变更操作仍只能通过原 Thread 的受治理工具链。工作文件中的嵌套缓存、依赖、测试覆盖率和虚拟环境目录统一进入默认收起的“临时辅助”，不删除旧文件。Task 与 Ask 统一消费 Provider 流式响应；Task 的安全正文通过无持久 cursor 的 `run_live` 临时显示，最终仍由 SQLite 消息与事件替换。模型传输从请求发出开始限制首个真实正文/推理/工具 Token，空 SSE/keep-alive 不算首 Token；首 Token 后另有流空闲截止。新工具型 Run 的 `Auto` 采用 action-first 有效策略，显式 Off/Low/Medium/High 仍按用户选择执行；整轮硬上限默认分别为 120/120/180/300/600 秒。首 Token、流空闲和各级整轮时间均由 `config.yaml` 的 `model` 段配置并在启动时校验。`Worker` 只协调 Durable Claim/Lease/Checkpoint/Finish、Steering 与 Follow-up；固定 `StepDispatcher`、`ModelInvoker` 和五类 `ToolPreparer` 分别承担步骤路由、单次模型采样与收据前准备，SQLite 仍是唯一恢复真源。网络阶段、首 Token、流空闲、整轮超时和流中断会写入脱敏诊断；网络类故障只从检查点自动重试 1 次，语义响应修复仍保留 3 次。项目运行态优先通过可恢复 SSE 更新；项目空闲时不持续轮询任务、动态和消息列表，只有 live task 或用户打开动态面板时才启用有限轮询，会话列表则在发送、创建、重命名和删除后通过缓存失效更新。全局收件箱为空时退避到 15 秒，有待处理项时保持 5 秒刷新。

语义与完成合同补充：计划一旦绑定，普通续轮只能推进当前目标，只有 Steering、权限修订、恢复等显式重规划阶段才能替换计划；完成项与规范化重复项不得再次进入新版本。用户明确写出“不联网/离线/禁止搜索”时，搜索预算冻结为零，联网工具从当前目录移除，完成门禁也不会反向要求新的 Web Receipt。对 `.oktapod/` 内部工作文件的受控写入跳过自动 Artifact 登记而保持成功，公开候选文件仍必须严格 Capture。

MCP 在界面中使用资源名与工具短名，内部 UUID 只用于冻结唯一身份和审计。L3 工具可批准当前参数一次，或仅在当前 Task 内复用同一个完整工具身份；不会扩权到整个 MCP、会话或项目，每次调用仍产生独立 Authorization 与 Receipt。同一语义计划项累计 12 个终态 MCP 调用后会停止继续调用工具，基于已有证据收尾并明确缺口。资源能力说明分别来自 Skill Manifest/instructions 与 MCP 在线工具发现；预置文档资源的“安装或修复”只有在当前进程确实加载健康 Runtime 后才会成功。

## 快速开始

以下是源码开发方式，需要 Node.js `>=22.12.0`、pnpm `10.28.2`、Go `1.26.5`；正式 Windows/Linux 部署机器不需要这些构建工具，但必须同时安装版本二进制、签名 Document Runtime 和信任键，详见[部署与运维](Doc/Oktapod_部署与运维指南_v1.0.md)。

```powershell
pnpm install --frozen-lockfile
pnpm build
New-Item -ItemType Directory -Force "$env:APPDATA\Oktapod" | Out-Null
Copy-Item Deploy\config.yaml.example "$env:APPDATA\Oktapod\config.yaml"
.\Build\oktapod.exe serve
```

浏览器访问 `http://127.0.0.1:8787/`，首次使用在登录页注册本地账户，然后在“设置 → 凭据与密钥库”生成、保存并确认一次性恢复口令。Vault 变为 active 后重新录入 Provider/MCP 凭据；Secret 只以 AES-256-GCM 应用层密文进入 SQLite。

配置中心按职责分为“模型服务、治理与审批、运行与上下文、费用与用量、凭据与密钥库、数据与备份、外观与偏好”。“运行与上下文”提供默认关闭的受控私网 `web_fetch`：只接受精确 IP + 端口白名单，新 Run 冻结策略 Hash，每 Run 首次访问一个 origin 必须人工批准；公网 Fetch 保持 80/443 与 DNS/Dial 防 SSRF 边界。Provider 的连接、API Key 与启用模型一次保存；Reviewer 不再混入模型连接页。诊断会列出具体缺失凭据及修复入口。启动配置加载顺序为 `--config > OKTAPOD_CONFIG_FILE > %APPDATA%/Oktapod/config.yaml > 内置默认值`；可用 `oktapod config doctor --json` 和 `oktapod vault status` 诊断。开发模式命令如下：

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

发布构建除带版本二进制外，还强制要求已暂存的自包含 Python/LibreOffice/PDFium/Tesseract 运行时、完整许可证目录和 Ed25519 私钥文件，生成签名 `document-runtime`、SPDX SBOM 与逐文件/归档 SHA256；普通 `pnpm build` 仍允许 Runtime 缺失并明确降级。

## 文档

| 文档 | 用途 |
|---|---|
| [V1 发布说明](Doc/Oktapod_V1_发布说明_V1.0.0.md) | 发布范围、已知限制、安装与回滚 |
| [V1 总体架构](Doc/Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md) | 当前架构事实与演进边界 |
| [V1 执行计划](Doc/Oktapod_V1_执行计划与验收门禁_V1.0.md) | 差距批次、门禁与剩余工作 |
| [V1 验收矩阵](Doc/Oktapod_V1_验收状态与追踪矩阵.md) | 发布完成度唯一真源 |
| [V1.1 Agent Runtime 优化](Doc/Oktapod_V1.1_Agent_Runtime_优化执行计划.md) | 主 Agent Assembly、统一 ToolRuntime、预置文档 Runtime、兼容边界与合并门禁 |
| [Agent Runtime 性能 V2](Doc/Oktapod_Agent_Runtime_性能与LLM调用优化执行方案_V2.0.md) | Auto 单 Turn、Plan Transition、Live Streaming、Context Projection、Barrier 与性能门禁 |
| [部署与运维](Doc/Oktapod_部署与运维指南_v1.0.md) | Windows 版本目录/任务计划、Linux systemd、签名 Runtime、升级与空 DataRoot 回滚 |

## 验证

```powershell
$env:GOCACHE="$env:TEMP\go-build-cache"
pnpm verify
pnpm test:e2e
```
Ubuntu CI 额外执行 `pnpm test:race`；Ubuntu 与 Windows CI 都执行原生发布构建和产品烟测。项目规则见 [AGENTS.md](AGENTS.md)。
