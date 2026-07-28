# Oktapod

Oktapod 是一套多用户 Agent OS。个人账户和团队成员使用同一套产品、同一个登录入口；项目决定协作边界，而不是部署出不同的软件。

# 说明
后端采用go语言，极简设计理念，不仅代码架构，UED设计也一样
技术栈选型、架构设计遵循对AI编程工具友好的，AI易于开发、验证、维护的原则，后续代码由AI生成

## 当前可运行基线

当前 MVP 使用 SQLite，进入网页后必须通过用户名、密码登录。用户可以注册多个本地账户并创建个人私有项目；项目列表、直接访问和后续任务操作均按当前 Session 隔离。团队项目与 PostgreSQL 适配器保留为后续演进资产，但不进入当前 Web 主流程或 MVP 完成度声明。

个人项目已提供独立四态计划看板、持久化会话、任务、项目动态、持久化执行、事件流、模型调用和受控 Workspace Tool 的纵向骨架。每个任务会原子保存 Conversation、用户 Message 和关联 PlanItem，执行终态按服务端序号追加 Assistant Message；移动 PlanItem 只改变项目计划，不会伪造 Task/Run 状态。项目动态来自权限过滤后的已提交 RunEvent，不用前端临时状态冒充。模型可以创建工作区文件；执行命令必须进入持久化审批中心，由用户确认冻结参数后才会运行。每次副作用均保存收据并经过确定性 Validator，结果 Artifact 只从已验证的工具结果生成。

模型调用采用 Run 级硬预算：Worker 在调用前原子预留调用次数和输出 Token 上界，租约恢复不会重复预留，完成后释放预留并按 Provider 实际 usage 结算；Run Usage 同时展示已用与预留额度。

模型网关内部使用 Provider-neutral `ModelMessage + ContentBlock` 协议，System Prompt 与历史消息分开传递；文本、图片、Thinking、ToolCall/ToolResult 和 `stop/tool_use/length/error/aborted` 终止原因均保留为类型化数据。唯一 `TranscriptAssembler` 从当前 Session Leaf 生成消息级历史；同一 Thread 创建新 Run 时会把“最近压缩摘要 + 保留消息”冻结进 Run，再在其后追加当前目标和 Run 内 ToolResult，因此“继续当前任务”不会只得到孤立的新目标。旧的单段 `UserPrompt/Content/ToolCalls` 只保留给尚未迁移的测试桩，不进入生产模型网关。Usage 语义版本为 v2：`InputTokens` 不重复包含缓存命中，`ContextTokens` 表示本次上下文窗口占用，Cache Read/Write 独立记录，Reasoning 是 Output 的子集且不再重复消耗预算。模型调用同时保存 requested/effective `ModelRef`、purpose、through entry、StopReason 和 usage 版本。Release 1B 已加入原生 Anthropic/Gemini Adapter 及 Message/Tool/Thinking/Usage 合同，并分别解析 Anthropic SSE 与 Gemini `streamGenerateContent` 的增量文本、工具、Thinking、StopReason 和最终 Usage；真实 Provider 可见流式任务仍是发布门禁。

Agent Loop 使用持久化 ToolBatch。策略预检、审批与快照串行完成；只读且未声明 `sequential` 的同批工具可被不同 Worker 并行领取，任一工具声明顺序执行时整批按源调用顺序推进。完成事件按实际完成时间发布，回灌模型的 ToolResult 始终按源调用顺序排列，整批只有一个 Worker 能领取 continuation。`finish_reason=length` 携带工具调用时所有调用均标记为未执行，并生成可见错误 ToolResult；单 Agent 任务即使尚未解析出完整工具调用，也会进入最多三次的自动续写与递增输出额度，不再把“回答被截断”误报为任务成功。超过内联上限的 ToolResult 写入内容寻址 Artifact，模型只接收可审计引用和短预览。

Session Tree V2 使用独立 `entry_seq`，Message 仍由 `ref_id` 指向唯一正文。Message、Entry、Leaf 和消息序号在同一事务提交；Navigate 使用 expected-leaf CAS，活动 Run 存在时数据库拒绝 Navigate/Fork/Clone/Label/Branch Summary。Fork 与 Clone 重建选中祖先路径而不修改历史，Label 会追加 `system/label` 审计 Entry，Branch Summary 使用独立五节模板。上下文、Compaction 和后续 Turn 只沿当前 Active Leaf 读取，因此切换分支不会混入已放弃分支；相关接口位于 `/api/v1/conversations/{conversation_id}/entries|navigate|fork|clone|branch-summary`。

默认只监听本机回环地址。模型 Provider 已升级为组织级多实例 Registry：`provider_id` 表示实例身份，`protocol_family` 只表示共享协议实现。Endpoint、默认模型和 SecretRef 以数据库为唯一配置源，API Key 保存在当前操作系统账户的凭据库中，不读取模型环境变量或密钥文件；尚未配置时使用 `https://vip-sg.freemodel.dev/v1` 作为非敏感 Endpoint 回退值。登录使用 HttpOnly Session Cookie，写请求同时校验 CSRF Token；密码采用 PBKDF2-SHA256 保存，连续失败会触发临时锁定。当前本地账户体系不等同于 OIDC/企业 SSO。

HTTP 身份统一解析为 `Principal` 后再派生资源 Scope；产品 Handler 缺少有效 Session/Bearer 时返回 401，不回退到启动时默认用户。`LocalPrincipalResolver` 只保留给 CLI/初始化等明确离线入口。Worker 仅依赖自己实际使用的 16 个持久化方法；文件字节通过内容寻址 `BlobStore` 保存，SQLite/PostgreSQL Repository 继续负责元数据、引用、权限和审计。工具规划与执行前复核共用同一确定性 `ExecutionPolicy`，审批只能把 `Ask` 收窄为临时允许，不能绕过静态拒绝。

任务可选择已启用的版本化 Agent 或项目 TeamDefinition，运行时把 Agent/Team 模型、角色和版本固化到 Run 快照；这里的 Agent Team 是单个项目内的执行编排，不代表多人团队项目。当前命令执行采用“无 Shell + 结构化参数 + 隔离环境变量”的最小实现，还不等同于完整网络沙箱。模型需要当前资料时可调用内建 `web_search`：它只访问固定的 HTTPS DuckDuckGo HTML 搜索端点，查询、域名过滤、时间范围和结果数量均有上限；搜索结果会先写入工具收据，再由独立模型步骤综合，最终答案必须引用实际 URL。

内建工具由 Provider-neutral Tool Registry 统一声明，模型 Adapter 只映射 Schema，不再各自拥有工具业务规则。Release 1A 提供 `read/list/find/grep/edit/write`、`git status/diff/log/branch/commit/restore` 和结构化命令；Release 1B 增加 `git_conflicts`、按 Hunk 恢复和选择路径提交。文件访问统一执行工作区 canonical path、symlink、二进制、字节/行数、分页和 Secret 脱敏检查，大输出转为 Artifact。`edit` 使用 preimage SHA-256、临时文件和原子替换防止覆盖外部修改；Git 变更在执行前校验工作区状态 Hash，并经过 Policy、审批和副作用收据。`push/pull` 未开放。

服务默认启动 4 个具有唯一 lease owner 的 Worker，并将数据库级同时活跃 Run 限制为 4；`OKTAPOD_RUN_CONCURRENCY=1..4` 可降低当前进程并发。取消后 Worker 使用独立短超时上下文持久化终态，SQLite Busy 使用指数退避，未知副作用仍由既有收据恢复规则阻断重复执行。`GET /api/v1/diagnostics` 只返回版本、数据库、Provider、Worker 和活动 SSE 连接等脱敏状态；原始进程日志不经该接口暴露，备份与恢复继续使用独立的高权限运维流程。

注册、项目/任务创建、Transcript 导入和结果验收等幂等写操作也对 SQLite `BUSY/BUSY_SNAPSHOT` 使用有界指数退避；重试复用同一幂等键，避免并行 Worker 写入时向用户暴露偶发锁错误或重复创建。

Team PostgreSQL 的 `backup`/`restore`、项目 `export`/`import` 和 `cleanup` CLI 已走 PostgreSQL Repository。备份使用参数化 `pg_dump`，数据库内容写入 custom dump，Workspace/Blob 继续生成 SHA-256 manifest；启用 FORCE RLS 时建议额外配置仅用于维护的 `OKTAPOD_DATABASE_BACKUP_URL`，其数据库角色需要 `BYPASSRLS` 且不得用于应用请求。恢复使用 `OKTAPOD_DATABASE_URL` 对应的数据库 Owner，确保表和 RLS 对象归应用角色所有；未配置维护 DSN 时备份/预检回退到应用 DSN，若角色没有权限会 fail-closed 并给出错误。

如需费用估算，可注入非敏感的 `OKTAPOD_MODEL_PRICING_JSON`（按模型提供 `input_per_1k` / `output_per_1k`），系统会把费用写入 UsageLedger；未配置时明确显示 `unpriced`。

Provider 管理 API 为 `/api/v1/providers`、`/api/v1/providers/{provider_id}`、`/test` 和 `/models`。Registry 支持多个 OpenAI-compatible 实例，Release 1B 也可选择原生 Anthropic 或 Gemini 协议族；模型目录按 Provider 隔离并使用 stale-while-revalidate，刷新失败时保留最近成功目录并标记 `stale`。凭据字段只接收新值，任何响应都只返回“已配置/未配置”。项目可保存默认模型与 fallback，任务级 Provider/模型/Thinking 覆盖会冻结到 Run；配置变化只影响下一 Turn。Provider 明确声明模型不支持 Tool Calling 时，请求会在发送前阻断并尝试兼容 fallback；标签缺失时保持兼容。缺少凭证会返回 `503/MODEL_CREDENTIAL_MISSING`，Provider 错误和响应体会脱敏。

Web 已采用 Codex 式会话优先工作台：中央 Thread 统一展示消息、Tool、重试、文件变化和结果；Composer 上方只呈现当前 Session 的一个活动语义计划及 Human Action，宽度与输入框一致。创建 Run 时仅显示“正在制定执行计划”，模型随后必须通过内部 `update_plan` 控制工具发布与本次目标对应的完整语义清单；计划项包含 `pending/in_progress/completed`，禁止使用固定“三步模板”或把 Tool Call 冒充任务。计划发布后的首轮必须先产生工具证据，系统不暴露 `update_plan`，避免“发布计划—继续—再次发布计划”的空转；后续状态更新按稳定位置绑定原计划项，模型改写标题或增删步骤不会生成新 Revision，而会保留原清单并投影完成度。无变化更新会被审计并自动切回不暴露 `update_plan` 的执行轮，完成项回退仍拒绝。模型完成语义项后以完整清单更新状态，只有 Steering 与权限变化才生成新 Revision。摘要只显示“完成数/总数 + 当前计划项”，展开后读取持久化 Plan Revision；任务阻塞后同一位置改为明确的终态和恢复入口，Thread 侧栏同步显示运行、审批、输入或阻塞状态。消息中的历史任务区不再重复展示语义计划或“查看任务详情”，只保留默认收起的执行证据和文件产物。审批、补充输入、权限升级和未知副作用都在 Composer 邻接区等待处理，右侧 Inspector 查看 Run、Context、Usage、Tools、Security、Changes 和 Trace。Inspector 在桌面宽度固定占右栏，不再落到 Composer 下方。`/projects/:id/tree` 提供 Session Tree，`/settings/providers`、`/resources`、`/approvals`、`/diagnostics` 提供管理入口；项目侧栏不重复放置全局资源入口，现有非交互式 `/projects/:id/terminal` 入口暂时隐藏，命令仍由对话中的结构化工具、Policy、审批与 Receipt 完成，待真正的交互式终端窗口设计后再开放。导入入口先在浏览器进行敏感字段扫描，再通过 `POST /api/v1/projects/{project_id}/transcript-import` 事务化重建私有 Thread、消息和 Session Entry；网页导出支持正文敏感模式脱敏后的 JSON、JSONL 和 HTML，自有 JSONL 可按 Thread round-trip。当前支持 Oktapod JSON/JSONL 与通用 Pi JSONL 角色消息；Pi v1/v2/v3 完整树、`firstKeptEntryId` 和 Tool/Compaction 全量 round-trip 仍未完成。

权限模式为 `ask`、`auto_review` 和 `full_access`。`ask` 将策略 Ask 转为人工 Human Action；`auto_review` 创建无工具权限的独立 Reviewer Step，只有严格解析出的低中风险结论可批准，未配置 `OKTAPOD_REVIEWER_MODEL`、调用失败、输出非法或高风险时一律转人工；`full_access` 是 30 分钟 Run 快照，只减少命令类逐次审批，不能绕过沙箱、场景能力、硬拒绝或破坏性/开放世界 MCP。审批支持“拒绝、允许本次、本会话同类动作”和经站内二次确认的“本项目允许 30 天”。v2 规则使用工具、规范化完整参数、风险等级和目标范围的结构化签名；旧宽泛规则仅留审计，不再自动匹配。破坏性 Git 与 MCP 不创建长期授权。Owner 可在工作台 Security 检查器查看有效期并随时撤销，规则创建、命中和撤销均保留审计状态。迁移 `000050`～`000052` 保存语义计划、Human Action 和 v2 规则；旧 `/approvals` 接口继续兼容，新客户端使用 `/human-actions`。

能力清单可通过非敏感配置 `OKTAPOD_CAPABILITY_TRUSTED_KEYS_JSON` 注入“key id -> base64 Ed25519 公钥”映射。带 `_oktapod_signature` 的 manifest 会先做签名校验：可信 key 签名通过后继续执行权限/MCP 审核规则，未知 key 会以 `untrusted-signature` 隔离，签名格式错误或内容被篡改则拒绝导入；未签名 manifest 保留向后兼容路径。生产环境仍需将该配置接入可信发布根、轮换和强制签名门禁，当前 MVP 不把可选校验宣称为完整供应链治理。

项目配置抽屉支持 Owner/Approver 通过 `PATCH /api/v1/projects/{project_id}` 更新默认模型和 fallback；变更写入审计，已运行任务继续使用创建时固化的 Run 快照。

## Windows 本地手动运行

默认只监听 `127.0.0.1:8787` 并使用 SQLite。数据保存在 `%APPDATA%\Oktapod`，停止或重新构建程序不会删除已有数据。

### 环境要求

- Node.js `>= 22.12.0`
- pnpm `10.28.2`
- Go `1.26.5`

可在 PowerShell 中检查当前版本：

```powershell
node --version
pnpm --version
go version
```

### 首次运行或代码更新后

```powershell
cd E:\git\Oktapod

# 首次运行、依赖变化或 pnpm-lock.yaml 更新后执行
pnpm install

# 构建 React 前端和内嵌前端资源的 Go 可执行文件
pnpm build
```

如果代码和依赖均未变化，可以跳过安装与构建，直接启动已有的 `Build\oktapod.exe`。

### 首个登录账户

首次启动不创建可登录的固定口令账户。请在登录页切换到“注册”，创建第一个本地账户；密码只以带随机盐的 PBKDF2-SHA256 哈希保存。初始化使用的内部主体不能通过固定凭据登录，也不能绕过项目 Owner 隔离。

### 配置模型

模型配置是可选的。不设置时仍可打开界面和使用不依赖模型的功能，但模型调用会返回 `MODEL_CREDENTIAL_MISSING`。CLI 继续管理默认实例以保持兼容；Web Provider 页面使用多实例 REST API，API Key 仅在创建或显式更新时提交且永不回显。

先构建程序，再执行：

```powershell
.\Build\oktapod.exe model configure --endpoint https://api.example.com/v1 --model example-model
.\Build\oktapod.exe model status
# 如需彻底移除配置和系统凭据
.\Build\oktapod.exe model reset
```

`configure` 会交互式读取 API Key，输入不回显，也不会进入命令历史。Endpoint、默认模型和 SecretRef 写入 SQLite/PostgreSQL；API Key 写入 Windows Credential Manager 或 Linux Secret Service。旧单例配置在迁移 `000048_provider_registry` 中回填为默认 Provider 实例。服务会在下一次模型调用时读取更新后的数据库配置，无需重启；已经创建的 Run 继续使用冻结的 requested/effective ModelRef、Thinking 和目录版本。

### 配置会话上下文压缩

会话不会删除旧消息。模型上下文默认在最新有效 assistant `ContextTokens + OutputTokens` 加尾部估算达到 100,000 Token 时自动生成耐久摘要；Provider 未返回有效 usage 时才使用保守文本估算。摘要严格使用 Pi 对齐的六节初次/增量模板，保留最近约 20,000 Token、确定性的已读/已修改文件清单，并支持超大 Turn 前缀摘要；摘要失败最多重试三次且不会推进 checkpoint。下一轮使用“当前 Leaf 上的最近摘要 + 摘要后的消息”。阈值和预算保存在数据库中，修改后立即生效：

```powershell
.\Build\oktapod.exe context status
.\Build\oktapod.exe context configure --auto=true --trigger-tokens 100000 --retain-tokens 20000 --summary-tokens 4096
```

在已有会话输入 `/compact` 可立即压缩；也可附加本次摘要要求，例如 `/compact 保留所有文件路径和未完成事项`。手动或自动压缩都保留原始消息和模型调用审计记录。

### 启动与停止

在仓库根目录执行：

```powershell
cd E:\git\Oktapod
.\Build\oktapod.exe serve
```

看到 `Oktapod started` 后，浏览器访问 `http://127.0.0.1:8787/`。运行期间保持该 PowerShell 窗口打开；需要停止时，在窗口中按 `Ctrl+C`。

检查服务是否正在监听：

```powershell
Get-NetTCPConnection -LocalPort 8787 -State Listen
```

如果端口已被旧的 Oktapod 进程占用，可先停止旧进程：

```powershell
Get-Process oktapod -ErrorAction SilentlyContinue | Stop-Process
```

### 前后端开发模式

需要前端热更新时，使用两个 PowerShell 窗口。第一个窗口启动 Go API：

```powershell
cd E:\git\Oktapod
go run ./Src/cmd/oktapod serve
```

第二个窗口启动 Vite：

```powershell
cd E:\git\Oktapod
pnpm dev
```

开发模式访问 `http://127.0.0.1:5173/`，Vite 会把 `/api` 代理到 `http://127.0.0.1:8787`。模型配置仍使用上述 CLI，并由 Go API 从数据库和系统凭据库读取。

Linux 构建使用相同的 `pnpm install` 和 `pnpm build` 命令，产物为 `Build/oktapod`，通过 `./Build/oktapod serve` 启动。

## 运维命令

目标目录必须由操作者显式指定：

```powershell
.\Build\oktapod.exe preflight
.\Build\oktapod.exe backup -output E:\backups\oktapod-20260717
.\Build\oktapod.exe restore -input E:\backups\oktapod-20260717
.\Build\oktapod.exe cleanup

# Personal -> Team 管理员迁移（导出目录必须为空；导入目标为当前 Profile）
.\Build\oktapod.exe export -project <project-id> -output E:\migrations\project-20260717
.\Build\oktapod.exe import -input E:\migrations\project-20260717
```

Team 单节点试点后续可使用 `Deploy/docker-compose.team.yml`。先将 `Deploy/.env.example` 复制为被 Git 忽略的 `Deploy/.env`，配置独立随机的 PostgreSQL 口令、完整 `OKTAPOD_DATABASE_URL` 和 `OKTAPOD_TEAM_ADMIN_TOKEN`，再执行 `docker compose --env-file Deploy/.env -f Deploy/docker-compose.team.yml up -d --build`。Docker 镜像构建与运行暂不纳入当前 MVP 验收门；该拓扑尚未装配容器可用的系统凭据库，因此不承诺模型调用，生产环境仍需反向代理 TLS、外部 SecretStore、备份/PITR 和部署演练。

需要人工批准的任务会显示为“等待审批”，也会出现在全局“待我处理”入口。审批页面会展示风险等级、理由、冻结工具参数和输入哈希；批准或拒绝均支持幂等重放，刷新页面或重启服务不会丢失待办。

计划任务支持 `misfire_policy=skip|run_now|ask` 和 `overlap_policy=forbid|queue|replace`，策略会随 Schedule 持久化并在任务监控中展示；多个服务进程通过持久化 lease 避免重复触发。MCP 能力包默认隔离，只有启用后显式连接才会做受限握手和工具发现，并拒绝非回环私网目标；工具 Schema 会在请求前按目标做确定性裁剪并记录节省量。Skill 采用 Run 快照的元数据优先与正文按需加载策略，支持直接导入带 YAML frontmatter 的 `SKILL.md`；`name`、`description`、`instructions`、`triggers`、`allowed-tools`、`model-invocable` 会被归一化为受控 manifest，中文触发词也会参与匹配，正文按需加载并记录 ContextPlan。模型为 `web_search` 可选域名数组产生的空字符串会在参数冻结前安全删除，非空非法域名仍然硬拒绝；累计六次成功搜索且已有至少三个候选结果，或达到十二次硬上限时，Harness 强制进入无工具综合步骤，防止成功搜索也无限扩搜。记忆可在用户明确操作后生成带来源哈希的 quarantined Skill 草稿，必须人工启用。命令执行采用进程组/进程树终止，避免超时后遗留子进程。记忆支持标题/内容关键词检索，并在写入前拒绝明显的 API key、Bearer、私钥和密码材料。任务页可提交预算例外并明确批准；终态任务可请求 Advisor 预览，预览不调用模型、不消耗 Token，用户显式批准后才进行模型复核并写入 UsageLedger。

Bootstrap 和前端“运行能力”面板会显示当前 OS、沙箱标签、文件/命令/网络/MCP 边界；网络标签 `search-only-fixed-provider` 表示只有上述受控搜索出口，不等同于任意网络访问，`restricted-native` 也不宣称容器级隔离。MCP HTTP 每次连接会重新校验解析地址、禁用代理并拒绝重定向。

## 互联网搜索与 Skill 使用

在单 Agent 任务中直接提出“查询最新资料”“查官方文档”等目标即可触发 `web_search`。搜索是只读 L0 工具，不弹出命令审批；每次调用最多返回 10 条结果、响应体最多 2 MiB，并在域名过滤后再写入收据。Tool Error 是 Agent Loop 的观察结果，不再直接终止整个 Run：并行 Tool Batch 会等待全部兄弟调用形成收据，再由唯一恢复所有者要求模型换参数、换工具或修订方案；恢复上限耗尽但已有搜索证据时自动进入综合，没有可用证据时创建 `input_request` 请用户选择已有证据交付、补充资源或停止。若搜索成功，Worker 会自动排队一个不暴露工具的综合步骤，将受控结果交给模型，答案中的“来源”必须使用搜索返回的真实 URL。若 Run 的模型调用预算只有 1 次，搜索后综合会因预算不足而被阻断，应将 `max_model_calls` 调到至少 2。

“能力中心”支持 ZIP、JSON manifest 或标准化 `SKILL.md`。安装流程分为“校验资源”和“确认安装”：浏览器先检查类型、大小、范围与现有名称/版本，服务端在确认时继续执行 manifest、路径穿越、符号链接、压缩炸弹、文件数量、签名、权限与可执行内容校验。同一作用域下同名同版本、同内容 Hash 的导入幂等返回现有记录，不创建副本；同名同版本但内容不同会拒绝覆盖；新版本与旧版本并存，由新 Run 冻结明确版本。安装位置分为全局和当前项目：默认安装到全局，同一组织下的其他项目可发现并按需加载；项目能力只在当前项目可见。对话输入框的附件按钮也可直接安装 ZIP，默认全局，并复用相同的内容哈希、路径校验、风险隔离和审批规则。最小 `SKILL.md` 示例：

```markdown
---
name: release-check
description: 发布前检查并引用最新版本信息
triggers:
  - 发布检查
  - release check
allowed-tools: web_search
---
# Release check

先查询官方来源，再按验收标准列出事实、风险和来源链接。
```

导入后先经过 manifest 预检；带命令、hook、网络或其他扩权声明的能力进入隔离态，必须由 Owner/Approver 审查。Skill 正文不会全量注入每次请求：运行时先将名称、版本、描述和触发词作为元数据，再按中英文触发匹配结果加载正文；执行日志里的 `ContextPlan.skills` 可查看本次加载数量和节省的 Token。匹配到带 `allowed-tools` 的 Skill 时，本次模型请求只展示其声明工具与精确匹配的 MCP 工具；该收窄规则仍不会绕过 `PlanTool`、审批或 MCP 冻结快照。

显式调用有两种入口：在任务输入框键入 `/` 打开 Skill 面板并用方向键选择，或点击能力卡上的“使用”自动填入命令。命令名会把 Skill 名称转为小写并将空白替换为 `-`；例如 `/release-check 验证生产发布状态` 会把 `验证生产发布状态` 作为本次参数传入。显式调用会强制使用单 Agent 任务，在创建 Run 时冻结 Skill 版本和内容哈希，不能同时选择 Agent Team。

普通任务中，模型也可以在目标与 Skill 的 `description` 或 `triggers` 匹配时主动调用：它会先通过只读 `skill_invoke` 请求加载 Skill，再由系统恢复同一个任务继续执行。该次模型响应只能调用 `skill_invoke`，不能混用其他工具；恢复阶段不会再暴露 `skill_invoke`，避免 Skill 递归调用。主动调用默认开启；如需只允许用户显式触发，可在 JSON manifest 中设置 `"model_invocable": false`，标准化 `SKILL.md` 则使用 `model-invocable: false`。

只有当前项目可见的项目 Skill、同一组织可见的全局 Skill，以及已启用且进入本次 Run 冻结快照的版本才能被调用；未知名称、重名、已禁用或未快照的 Skill 都会拒绝执行。Skill 正文与调用参数从恢复阶段开始生效，其 `allowed_tools` 只会收窄可用工具，不会绕过审批、预算、MCP 冻结快照或命令安全边界。任务页依次展示 `skill.invoked`、`skill.loaded` 和 `model.skill_resume_queued`，工具收据与 `ContextPlan` 中保留调用来源、参数存在性和加载后的工具范围。


---

## 部署与运维

完整的开发环境启动、生产环境部署、常见问题排查指南，请参阅：

📄 **[部署与运维指南](Doc/Oktapod_部署与运维指南_v1.0.md)**

包含：
- ✅ 开发环境前后端分离启动
- ✅ 单二进制生产部署
- ✅ Linux Systemd 服务配置
- ✅ Nginx 反向代理配置
- ✅ 环境变量完整说明
- ✅ 常见问题排查清单
- ✅ 生产环境检查清单

快速启动：

```powershell
# 开发模式
go run ./Src/cmd/oktapod serve    # 终端 1: 后端 8787
pnpm dev                       # 终端 2: 前端 5173

# 生产模式
pnpm build
.\Build\oktapod.exe serve
```


完整校验：

```powershell
pnpm verify
pnpm test:e2e
```

更完整的范围、架构与验收状态以 `Doc/` 下的 Markdown 文档为准；当前版本仍在按 MVP 追踪矩阵持续实现。
