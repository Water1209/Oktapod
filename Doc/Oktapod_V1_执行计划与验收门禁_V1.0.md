# Oktapod V1 执行计划与验收门禁

> 版本：V1.8
> 更新日期：2026-09-04
> 架构基线：`Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md`
> 验收真源：`Oktapod_V1_验收状态与追踪矩阵.md`

## 0. 执行结论

V1 不按旧 W1-W6 从零重做。当前代码已经具备注册用户、个人私有项目、持久 Run、审批、Receipt、Workspace/Git、MCP、Skill、Provider、SSE 和 Codex 式工作台。后续执行采用**差距驱动**：

```text
冻结当前行为 → 拆窄端口和运行时 → 收紧执行安全
→ 统一工具/MCP/Skill 契约 → 补齐 Changes 用户闭环
→ 故障注入与跨平台发布验收
```

V1 唯一交付目标：

```text
用户描述开发目标
→ Agent 理解项目、计划、读改和验证
→ 展示 Diff，允许接受、撤销或提交
→ 审批、异常和恢复始终在原 Thread 可见
```

## 1. 不可破坏的现有契约

- HTTP 使用真实 Session Principal；禁止回退到固定 `local/local-user`。
- 个人项目仅 Owner 可见；所有查询和写入继续携带真实 Scope。
- `session_entries.entry_seq` 与 `messages.server_seq` 独立。
- 现有工具名、Run 快照、Receipt 和 SSE cursor 保持可恢复兼容。
- Steering 在已提交 Checkpoint 后生效；Retry 选择最新失败/阻塞前沿。
- 完全访问（内部值 `full_access`）自动执行冻结策略评估为 L0-L2 的动作；L3、硬拒绝和 Scope 约束不变。已启用的只读语义检索 MCP 缺少可选 Annotation 时按 L2 留存 Receipt，不再误弹高风险审批。
- 内建搜索在可替代能力完成真实验收前不得删除。
- 不恢复独立非交互 Terminal 导航；命令在 Thread 内受 Policy、审批和 Receipt 治理。

## 2. 执行批次

### 2.1 当前执行记录（2026-08-13）

| 批次 | 状态 | 已落地证据 | 仍未关闭 |
|---|---|---|---|
| B0 | `PASS` | 架构、计划、README、发布说明、部署指南、归档说明和追踪矩阵已统一为 V1.0.0 范围 | 后续变更仍须同步事实 |
| B1 | `PASS` | Repository 窄端口、`agentloop`、`harness` 与任务面板组件已提取并有测试 | Worker/页面仍可继续拆分，但行数只作维护指标 |
| B2 | `PARTIAL` | `ExecutionAuthorization`、Git/凭据隔离、版本注入、CI/OpenAPI/迁移门禁已落地 | 等待 Ubuntu/Windows 原生 CI 关闭 tag 门禁 |
| B3 | `PASS` | ToolSpec 元数据、简洁别名/旧名兼容、受限摘要和完整长输出 Artifact 已落地 | stdout/stderr 逐块直播进入 V1.1 |
| B4 | `PASS` | 通用来源解析、MCPDefinition v1、Skill/OAuth 已落地；内建 WebAccess 统一 `web_search`/`web_fetch`，具备精确私网 origin、Run 快照和强制人工授权 | 确定性同能力排名与跨 Provider 参数转换进入后续版本 |
| B5 | `PASS` | Composer 邻接治理、三基线 Changes、文件/Hunk 查看、接受、受治理撤销与选择提交已落地；中文未跟踪文件浏览器闭环通过 | 更细粒度行级交互可后续优化 |
| B6 | `PARTIAL` | Windows `pnpm verify`（Web 28/28）、Playwright foundation 初始化 1/1 + 重启 1/1、turn 1/1、success 26/26、真实 Provider、进程 kill 恢复、带版本工件和产品烟测均有证据 | Ubuntu race 与双平台原生产品烟测须在 tag 前由 CI 通过 |
| B8 | `PASS` | 000058～000063、DatabaseSecretStore、VaultKeyManager、严格 config.yaml、Web Access 配置/API/UI、Backup V2 和两阶段 Browser E2E 已落地 | DEK 轮换 UI、Team/PostgreSQL Vault 与外部 KMS 延期 |

### 2.2 场景、文件产物与审核体系收口（2026-08-11）

| 批次 | 状态 | 已落地范围 | 延期边界 |
|---|---|---|---|
| F1 | `PASS` | Work/Code 共用工具内核；新 Run 冻结 `scene`、`permission_mode`、Prompt Profile 和项目指令 Hash；历史 `scene_policy` 仍可恢复 | 不新增第三种场景 |
| F2 | `PASS` | `ArtifactCapture` 统一 Workspace 写入与脚本产物登记；100 MiB、路径/symlink、MIME、Hash 和 PDF 签名校验；PDF 无 Artifact 不得成功；旧 Run 遗漏文件可在明确选择当前任务后保存为任务输出 | 不自动扫描或猜测历史产物归属 |
| F3 | `PASS` | 文件页固定为“工作文件 / 项目资料 / 任务输出”，工作文件按常规文件浏览器展示工作区绝对路径、目录进入、面包屑、状态、大小和修改时间；Agent 多文件生成先规划路径，内部临时目录隐藏且不可 Capture；“保存到项目资料”仅表示项目内复用；项目库支持编辑/重命名与软删除，删除后保留工作区和历史；Changes 仍只是 Git 差异；Personal Diagnostics 显示存储根 | 旧工作区不自动搬移；Team 不返回宿主绝对路径；软删除项目的恢复 UI 未进入 V1 |
| F4 | `PASS` | Composer 多附件、上传进度/重试/移除、Run 附件快照、`asset_stage` 延迟物化与终态/24h 清理 | 更多 Office 格式深度验收进 V1.1 |
| F5 | `PASS` | Reviewer 仅处理 L2 `Ask`；`reviewer-decision-v1`、脱敏输入、fail-closed 和 Diagnostics 可用性；未配置时 UI 禁用自动审查 | L3 和静态拒绝永不交给 Reviewer 放行 |
| F6 | `PASS` | 每次用户发送先持久化可见消息，并冻结附件、Skill 与排队展示；后继任务绑定原消息 ID；同计划项审批聚合但逐项审计；预算扩展展示明确增量、收尾、不限额和终止语义 | 不把历史消息展示元数据反推回填到旧 Run |

### 2.3 统一配置与数据库密钥库（2026-08-13）

| 子批次 | 状态 | 已落地范围 | 验收边界 |
|---|---|---|---|
| V1 | `PASS` | `secret_vaults` / `secret_records`、每组织 DEK、每记录 Nonce/AAD、HKDF 恢复 KEK、本地 `bootstrap.key`、pending/confirm/recover/rotate 和重启自动解锁 | 不读取、不迁移、不删除旧系统 Keyring；现有凭据自然显示 missing |
| V2 | `PASS` | 严格 `config.yaml`、配置来源 API/doctor、Reviewer Provider+Model、版本化 Decimal 价格、上下文和用户偏好；删除 Reviewer/价格环境变量与 `model_provider_configs` 第二真源 | 数据库连接凭据仍是 Bootstrap Secret 例外；`full_access` 不跨设备持久化 |
| V3 | `PASS` | 七类职责设置页、Provider 单页一次保存、独立治理与费用页、Vault 初始化/恢复/旋转、Provider/MCP 重录、可定位到项目资源的诊断 `attention`、Backup Manifest V2、逻辑导出清 Secret/断开 Capability | Personal/SQLite 完整落地；PostgreSQL 只保留 Repository 边界 |

自动化覆盖正确/错误恢复口令、Nonce/AAD 篡改、密文互换、损坏、恢复口令旋转、Provider/MCP/OAuth CRUD、WAL/日志/导出明文扫描、无本地 KEK 的备份恢复，以及浏览器初始化、重新输入确认、Reviewer、MCP 重录和服务重启自动解锁。最终发布门禁仍以验收矩阵中的实际命令结果为准。

### B0：统一基线和验收真源

交付：

- 删除旧计划中的重复 W4/W5、错误目录和已完成能力的重复任务。
- 架构、执行计划、README、部署指南和验收矩阵使用同一 V1 范围。
- 将团队主流程、PostgreSQL 发布门禁、交互式 xterm、Pi 全版本精确导入、原生 Anthropic/Gemini 真实凭据门禁和完整设置服务端化标为 `N/A` 或 `DEFERRED`。

门禁：

- 文档链接、路径、状态和范围一致。
- 未验证能力不得标记 `PASS`。

### B1：内核端口与运行时拆分

交付：

- 将 Application Repository 拆成 Identity、Project、Conversation、Run、Resource、Approval/Receipt、Usage 等窄端口；SQLite Store 可实现多个端口。
- 保留现有 PrincipalResolver、Authorizer 和 BlobStore，不建立平行实现。
- 提取 `agentloop`：模型调用、ToolCall 解析、ToolBatch 提议、ToolResult 观察和终止判断。
- 提取 `harness`：Run、Lease、Checkpoint、审批、Receipt、预算、Steering、Follow-up 和恢复。
- Worker 逐步收敛为队列调度和装配；复杂度、职责数量和变更耦合是主指标，约 800 行仅作拆分参考。
- 任务成功终态由 Domain 推导的完成要求与 Harness 持久证据共同决定；模型无 ToolCall 仅结束当前 Turn。单一完整报告按一个主 Artifact 核验真实字符数、URL 与占位符，不允许用版本或分片合计冒充；不可变 Artifact 历史仍保留审计，UI 仅显示每个交付路径的最新版本。显式 Skill 的 ID、名称或 `name@version`、参数和完整交付门禁跨计划、工具、截断与完成修复续轮继承；带新约束的自然语言续作还冻结来源 Run 证据链。
- Thinking 选择必须真实传到 Provider；交错思考的 `reasoning_content` 与 ToolCall 一起续接。推理/参数截断进入 8192→16384→32768 Token 的有界动作恢复；搜索次数上限和“禁止再搜索”跨继任 Run 继承，预算耗尽后搜索语义族不再暴露，漏网提案仍由 Harness 拒绝。新项目默认任务保险丝为 80 次模型调用与 1,000,000 Token，项目可显式改为不限额。
- 将 `project-page.tsx` 中的 Inspector、Changes 等稳定业务块提取为组件；约 300 行仅作维护参考，不作为用户闭环发布门禁。

门禁：

```powershell
$env:GOCACHE="$env:TEMP\go-build-cache"
go test ./Src/internal/application/... ./Src/internal/runtime/... ./Src/tests/integration/...
go test -race ./Src/internal/runtime/... ./Src/tests/integration/...
pnpm verify
```

- Golden Trace 证明重构前后事件、消息、Receipt 和恢复语义一致。
- 报告、代码修改、文件创建、外部检索等任务只靠文本宣称完成时必须继续；真实变更、变更后验证、最新 Artifact 和显式字数门禁均有反例测试。
- HTTP 无 Principal 返回 401；跨用户项目访问仍被拒绝。

### B2：执行授权、宿主受控执行与双平台 CI

交付：

- 用持久审批、冻结参数、`input_hash`、Scope、Risk 和 Policy Version 形成 `ExecutionAuthorization`。
- Executor 执行前重新评估 Policy 并核对授权；参数、Scope、Policy 或工具身份变化时 fail-closed。
- 不新增 HMAC ExecutionGrant 表；当前同进程/同数据库模型下 HMAC 不增加有效隔离。
- `governed_host` 隔离临时 HOME、清理凭据环境变量、禁 Git credential/helper/hooks、限制超时并终止进程树。
- UI 明示“宿主受控执行，非强沙箱”；容器/OS 强隔离留待 V1.1。
- CI 在 Ubuntu 与 Windows 统一执行 Go vet/test、Web lint/test/build、OpenAPI drift、SQLite migration、原生发布构建和产品烟测；Ubuntu 额外执行 race。

门禁：

- 审批篡改、过期、输入变化和 Policy 变化均拒绝执行。
- 子进程不继承 SSH/AWS/Git/Provider Secret；Git 不读取宿主配置。
- 原生产品首次注册、登录、建项目和 Web 主链路可用。

### B3：统一 ToolSpec 与 Pi 语义

交付：

- 单一 `ToolSpec` 包含稳定身份、调用名、Schema、来源、风险、读写属性、幂等性、输出限制、Executor、Validator 和 Renderer。
- 复用现有 Workspace/Git 实现，补齐 Pi 的截断、Continuation、BOM/CRLF、唯一编辑、二进制、`.gitignore` 和进程树终止语义。
- 新 Run 可暴露 `read/edit/write/ls/find/grep/run_command` 简洁别名；历史 `workspace_*` 名称继续映射到同一实现。
- `run_command` 产生可恢复的 started/completed/failed/cancelled、截断摘要和 Artifact；逐块 stdout/stderr/progress 直播进入 V1.1。
- 长输出保存为 Artifact；事件和模型上下文只接收受限摘要。
- `workspace_write_file` 与 `workspace_edit_file` 都生成最新可验证交付 Artifact；适配器内部传递的完整编辑后内容不得进入 Receipt、事件或模型上下文。

门禁：

- 文件与 Git 写操作继续经过 Policy、ExecutionAuthorization、CAS/Status Hash、Receipt 和验证。
- 历史 Run、旧工具名和旧 Receipt 可继续恢复。

### B4：MCP、Skill、搜索和外部内容

交付：

- 复用 `capabilities`、`mcp_connections`、审核和安装任务。
- MCP 增加 Schema Hash、配置版本、健康状态和 Run Tool Snapshot；变更只影响下一 Run。
- Skill Manifest 支持 `required-capabilities`、`preferred-tools`、`fallback-policy`。
- `strict` 在首选不可用时阻塞；`same-capability` 只能从 Run 冻结的已审核、启用、健康工具中向模型暴露候选。确定性候选排名和参数转换进入 V1.1。
- 对话式安装必须完成解析、隔离校验、权限差异、人工审核、启用、调用和 Receipt，模型不能静默安装。
- 名称发现走官方 MCP Registry；安装前固定实际版本并展示来源，歧义候选必须人工选择。`server.json`、URL、固定版 npm/PyPI 包和本地命令进入同一草稿用例。
- 多 Secret 只写入 SecretStore 的不透明加密集合，运行时按定义注入声明的环境变量或受控 Header；不得进入消息、事件、日志、数据库字段、导出和截图。
- 内置 Skill Creator 只负责通过对话生成单 Skill ZIP 草稿，不直接写资源记录、不绕过 ZIP 校验和审批。
- Skill ZIP 的浏览器检查只称“客户端预检”；服务端完整扫描失败时不安装，显示可行动的格式原因，并提供“用 Skill Creator 修复副本”对话入口。
- 内建搜索进入统一 ToolSpec，作为无需外部凭据的可用能力；Tavily MCP 是可选首选，不是核心依赖。
- MCP、搜索、网页和仓库指令统一包裹为 `external_untrusted` Envelope。
- MCP Annotation 是风险证据而不是可用性前置条件：显式非只读、破坏性或非幂等始终 L3；已审核、启用且冻结到 Run 的语义检索在 Annotation 不完整时为只读幂等 L2，仅在完全访问下免逐次确认。

门禁：

- STDIO 与 Streamable HTTP 合同通过。
- Schema 变化触发重审；安装幂等、同版本冲突、鉴权失败和限流可见。
- Bearer、多 Secret 和缺凭据恢复通过；`auth.type=oauth` 使用官方 SDK 的标准授权发现、动态客户端注册、PKCE、浏览器回调、取消和加密刷新令牌持久化。手工 access token 明确使用 `bearer`，不会与 OAuth 生命周期混用。
- OAuth 回调只保留短期内存会话并绑定当前 Capability/登录 Scope；API 不单独返回 state、授权码或 Token。Personal V1 使用同机 loopback 回调，外部账号真实数据调用在缺少用户 Secret 时标记为可选烟测，但发现、配置、审批、凭据请求和失败恢复不得跳过。
- Windows stdio 通过 `node.exe` 直接启动 `npx-cli.js`，不依赖 shell 包装脚本；HOME、APPDATA 与 npm/uv 缓存收敛到系统临时运行目录。真实 Filesystem MCP 必须完成授权目录写读和越界拒绝。
- 外部 MCP 调用必须有可配置硬截止；默认 10 分钟，超时产生脱敏、可恢复 Receipt，不允许无限占用 Worker。
- 真实 Playwright MCP 必须以固定版本打开本地 Oktapod 并返回可访问性内容；真实 Git MCP 必须对临时仓库返回 status 和 log。第三方包未限定主版本依赖时不由 Oktapod 猜测兼容版本；应将完整命令与依赖约束作为待审核配置固定。
- 外部内容不能改变权限、系统提示或安全策略。

### B5：Codex 式用户闭环

交付：

- Composer 保留输入、附件、安全姿态、已启用模型选择、发送/停止；审批、输入和阻塞动作紧邻 Composer。模型选择必须同时携带 Provider，服务端拒绝未启用模型绕过。
- 任务面板默认收起、可最大化，文件与 Changes 只从任务面板进入；工作台与项目库为不同路由，会话树从 Thread 菜单进入并保留历史深链。
- Changes 支持 Run 开始、Git HEAD、当前工作区三基线，以及文件/行/Hunk 定位、撤销、选择提交和冲突反馈。
- Diff 修改动作创建受治理的确定性 Tool Task，不由 UI 直接写工作区。
- 计划 UI 以持久化计划为真源；模型回退已完成项时自动修复，耗尽后再请求用户。
- 保留 Markdown、Thinking 折叠、Skill/Tool/审批可见性、Steering、Follow-up、Abort 和移动端能力。

门禁：

- 用户可在单一 Thread 完成“目标→读改→测试→Diff→撤销/提交→恢复”。
- 1440、1280、1024、768、390 视口无横向溢出或功能丢失。

### B6：发布收口

交付：

- OHCS 语义证据覆盖多轮文本、Tool 配对、并行/串行、Steering、Follow-up、Abort、Compaction、Tree、Provider 切换和恢复；证据可由统一 Golden Trace 与相邻契约测试共同组成。
- V1 必选故障覆盖 Worker kill、审批过期、SQLite Busy、Receipt 写失败保护和 SSE 重连；完整模型/MCP/网络故障矩阵进入发布后加固。
- Windows 完成真实浏览器主链路；Ubuntu 与 Windows CI 完成 clean install、build 和原生产品烟测，Ubuntu 额外完成 race。Linux 真实浏览器全视口矩阵进入后续加固。
- 使用 SecretStore 注入的真实 Provider 验证流式、文件读改、命令测试、审批、Diff、撤销/提交和恢复。
- 原生产品烟测覆盖真实产品流程，不只检查端口。

门禁：

```powershell
$env:GOCACHE="$env:TEMP\go-build-cache"
pnpm install --frozen-lockfile
pnpm verify
go test -race ./...
pnpm --dir Src/web test:e2e:foundation
pnpm --dir Src/web test:e2e:success
pnpm --dir Src/web test:e2e:auth
pnpm --dir Src/web test:e2e:turn
pnpm --dir Src/web test:e2e:approval
```

- 权威矩阵中全部 V1 必选项为 `PASS`；明确列入 V1.1/团队边界的项目不阻塞个人版。
- 不自动 tag、不 push；没有证据的项目保持 `PARTIAL/NOT_STARTED/N/A`。

## 3. 接口与数据决策

### 内部接口

- Repository Ports：按调用者和事务边界拆分，不按表机械拆分。
- `AgentLoop.Next(ctx, LoopState) (LoopEvent, error)`：不依赖 DB、HTTP、具体 Provider 或具体工具。
- `DurableHarness`：消费 LoopEvent 并负责持久化、授权、执行与恢复。
- `ExecutionAuthorization`：引用审批/规则、输入 Hash、Scope、Risk 和 Policy Version。
- `ToolSpec`：Native、MCP 和内建搜索共用；`PromptHint/Guidelines` 从实际下发工具动态组装。
- `ArtifactCapture`：Workspace 只解析受控路径，BlobStore 只管不可变内容，Repository 只管事务化元数据/收据/验证。
- `ProjectFileQueries`：Workspace、Asset 和 Artifact 各有查询契约，Changes 不进入文件存储。

### 数据

- 扩展 `mcp_connections` 的 Schema Hash、配置版本和健康状态。
- 新增 Run Tool Snapshot，冻结工具身份、来源、Schema、能力和 Policy Version。
- Skill 能力约束保存在已审核 Manifest，V1 不重复建表。

### OpenAPI

- 增加 Run Changes 只读查询。
- 增加 Workspace 列表/内容、Project Artifact 查询与 Diagnostics 存储/自动审查状态。
- 为 Tool Execution Event 定义稳定结构。
- Changes 的写操作复用受治理 Task/Human Action 流程。

## 4. 执行纪律

- 每批先定向测试，再执行 `pnpm verify`；并发/恢复变更追加 `go test -race ./...`。
- 只暂存本批文件，保留工作区其他修改。
- Commit message 使用英文；不自动 push。
- 每批代码完成后同步 README、架构、部署指南和验收矩阵。
- 用户体验和已有行为优先于目录形式、代码行数和架构纯度。
