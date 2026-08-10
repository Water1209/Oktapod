# Oktapod V1 验收状态与追踪矩阵

> 版本：v1.3
> 更新日期：2026-08-10
> 权威性：本文件是 V1 实现完成度与发布状态的唯一验收矩阵。设计文档覆盖某项能力，不代表该能力已经完成。
> 执行基线：`Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md`、`Oktapod_V1_执行计划与验收门禁_V1.0.md`

## 1. 状态定义

| 状态 | 含义 |
|---|---|
| `PASS` | 当前代码和可复核证据已证明该项满足验收条件 |
| `PARTIAL` | 已有实现或测试，但未覆盖全部明确要求 |
| `NOT_STARTED` | 尚未开始实现或没有可复核证据 |
| `BLOCKED` | 存在外部依赖，当前无法继续 |
| `N/A` | 经正式范围决策排除，必须记录依据 |
| `RC_READY` | V1 本地发布候选门禁通过；正式 tag 仍等待远端 CI |

## 2. 当前发布判断

| 发布 | 状态 | 判断 |
|---|---|---|
| V1.0.0 本地 Release Candidate | `RC_READY` | V1 个人版必选能力已关闭；本地工件和全量门禁通过后可提交 RC |
| V1.0.0 正式 tag | `BLOCKED` | 等待本提交进入 Ubuntu/Windows CI，完成双平台原生产品烟测与 Ubuntu race；不自动 push/tag |
| 历史 Release 1A/1B | `ARCHIVED` | 原 Release 1A/1B 范围已并入 V1 统一规划，不再单独追踪 |

## 2.1 V1 差距驱动批次总览

| 批次 | 目标 | 状态 | 关键交付 |
|---|---|---|---|
| B0 | 基线与验收真源 | `PASS` | 执行计划、总体架构、README、部署指南和本矩阵已统一范围；后续按代码事实持续同步 |
| B1 | 内核端口与运行时拆分 | `PASS` | Repository 窄端口、`agentloop`/`harness` 和独立 Inspector 已落地；剩余行数是维护指标，不影响行为闭环 |
| B2 | 执行安全与工程门禁 | `PARTIAL` | `ExecutionAuthorization`、宿主 Git/Secret 隔离、CI/OpenAPI/迁移门禁已落地；双平台原生构建与产品烟测、Ubuntu race 仍待 CI 证据 |
| B3 | ToolSpec 与 Pi 语义 | `PASS` | ToolSpec 元数据、简洁别名/旧名兼容、受限摘要和完整长输出 Artifact 已通过定向测试；逐块命令直播转 V1.1 |
| B4 | MCP/Skill/搜索 | `PASS` | 通用 MCP 来源解析、官方 SDK、本地/远端传输、多 Secret、产品级 OAuth 回调/取消/刷新、Skill ZIP 与内置 Skill Creator 已落地并有确定性测试 |
| B5 | Codex 用户闭环 | `PASS` | Composer 邻接治理；Changes 支持 Run/HEAD/Index、文件/Hunk、接受及受治理撤销/选择提交；中文未跟踪文件主链路已由浏览器验证 |
| B6 | 发布收口 | `PARTIAL` | Windows `pnpm verify`、Playwright foundation 1/1 + success 23/23、真实 Provider、进程 kill 恢复、带版本工件和产品烟测已有证据；Ubuntu race 与双平台原生 CI 仍是正式 tag 门禁 |

## 2.2 V1 正式范围决策

| 项目 | 状态 | 依据 |
|---|---|---|
| 团队项目主流程 | `N/A` | V1 仅交付注册用户的个人私有项目；保留代码边界，不计完成度 |
| PostgreSQL 发布门禁 | `N/A` | V1 仅 SQLite；PostgreSQL 适配保留但不阻塞发布 |
| 交互式 xterm | `N/A` | 命令在原 Thread 内以受治理 Task 执行，不恢复独立 Terminal |
| Pi v1/v2/v3 精确导入 | `N/A` | V1 只保证 Oktapod 自有格式 round-trip；Pi 精确兼容进入 V1.1 |
| 原生 Anthropic/Gemini 真实凭据 | `N/A` | V1 要求至少一个真实 Provider 主链路；原生协议保留合同测试 |
| 完整设置服务端化 | `N/A` | V1 允许非敏感显示偏好保存在浏览器；安全和运行配置必须服务端持久化 |
| Team/Agent Team/Schedule 主流程 | `N/A` | 代码保留兼容能力，但 V1 完成度只计算单 Agent 个人项目主链路，不删除已有入口 |
| 强沙箱与网络默认拒绝 | `N/A` | V1 明示 `governed_host` 非强沙箱；容器/OS 隔离进入 V1.1 |
| 命令逐块 stdout/stderr 直播 | `N/A` | V1 保证完成事件、截断摘要与完整 Artifact；逐块直播进入 V1.1 |
| Linux 浏览器全视口与完整故障矩阵 | `N/A` | V1 正式 tag 要求 Ubuntu race 与 Ubuntu/Windows clean/build/原生产品烟测；全视口浏览器和企业级故障矩阵进入加固 |

## 3. 基线门禁

| ID | 验收项 | 状态 | 当前证据 |
|---|---|---|---|
| BASE-001 | `pnpm install --frozen-lockfile` 可复现依赖 | `PASS` | 2026-07-26 初始基线执行成功；随后为根目录 E2E import 补充 Playwright 依赖并更新锁文件，重新安装成功 |
| BASE-002 | `pnpm verify` 全绿 | `PASS` | 2026-07-30 最终执行通过：Go vet/test、ESLint、20 个 Vitest、TypeScript、Vite、Go build、SQLite migration 和 OpenAPI drift 全绿 |
| BASE-003 | 默认 E2E foundation/success | `PASS` | 2026-07-30 最终执行 foundation 1/1、success 23/23 通过，覆盖核心工作流、受治理 Changes、Provider、Tree、1B 路由和五个响应式视口 |
| BASE-004 | auth/turn/approval E2E | `PASS` | auth desktop/mobile 2/2、turn 1/1、approval 1/1 通过 |
| BASE-005 | Windows 与 Linux 构建边界 | `PASS` | Windows `pnpm verify`/浏览器套件通过；WSL Ubuntu Go 全量、Web 测试和生产构建已有证据。Linux 浏览器全视口已转后续加固 |
| BASE-006 | 前端首包容量 | `PASS` | 主 JS 约 334KB；Terminal、Settings、Import/Export 已拆分为约 3.35KB、7.57KB、8.27KB 的懒加载路由块 |

## 4. OHCS Golden Trace

冻结 Pi 基线：`earendil-works/pi@b1c444d9`。

| Fixture | 要验证的语义 | 状态 | 自动化证据 |
|---|---|---|---|
| `text_multi_turn` | Event Sequence、消息 Role、持久序号 | `PASS` | `TestOHCSGoldenTraceTextTurn`、`TestTranscriptAssemblerCarriesConversationIntoNewRun` 与 HTTP follow-up 序号测试共同覆盖 |
| `tool_call_and_result` | Tool Call/Result 配对和下一 Turn | `PASS` | `TranscriptAssembler` 与 Skill/Tool continuation 测试断言同 ID 配对 |
| `parallel_tool_batch` | 并行完成顺序与源结果顺序 | `PASS` | 并行领取、真实完成事件、源顺序回灌和单 continuation 测试通过 |
| `sequential_tool_override` | 任一 sequential 使整批串行 | `PASS` | `TestSequentialDeclarationForcesWholeToolBatchToSourceOrder` |
| `length_truncated_tool_calls` | length 截断时禁止执行并回灌错误 | `PASS` | 截断调用零执行、`tool.skipped`、错误 ToolResult 和恢复提示测试通过；未解析出完整 ToolCall 的单 Agent 截断也会自动递增额度重试，不再误报完成 |
| `evidence_backed_completion` | 无 ToolCall 不等于目标完成 | `PASS` | 联网/MCP 检索、工作区写入/编辑、受控 Git 变更、变更后验证、交付 Artifact 与显式字数均由持久 Receipt/Artifact 门禁；自然语言续作可复用来源 Run 的最新写入/编辑 Artifact，并要求本 Run 新变更后的再次验证；文本完成声明、缺文件、缺验证和短报告反例测试通过 |
| `skill_context_continuation` | 显式 Skill 跨所有续轮保留 | `PASS` | 长 Skill 的末尾 QA/交付门禁在语义计划后仍存在；计划、工具、长度截断和完成修复统一继承 `invoked_skill_ids` 与参数 |
| `steering_after_checkpoint` | checkpoint 后注入且不丢失 | `PASS` | `TestConversationSteeringReplansAfterAtomicStep` |
| `followup_after_idle` | Agent 原本结束后消费 | `PASS` | 顺序消费、失败回队、空闲恢复和进程重开集成测试 |
| `abort_preserves_next_turn` | Abort 清 Steering 但保留 Next Turn | `PASS` | `TestConversationAbortDropsSteeringAndPreservesNextTurn` |
| `compaction_initial` | 首次结构化摘要和保留尾部 | `PASS` | Pi 六节模板、三次重试、真实 usage/回退估算和文件清单测试通过 |
| `compaction_incremental` | previous summary 增量更新 | `PASS` | previous-summary 增量模板和 checkpoint 事务测试通过 |
| `compaction_split_turn` | 超大 Turn 前缀摘要 | `PASS` | V2 split message/prefix 字段、路径加载裁剪和迁移测试通过 |
| `tree_navigate_branch_fork` | Leaf、Branch、Fork 和上下文路径 | `PASS` | Navigate/Branch/Fork/Clone/Label/五节 Summary/CAS/Busy Guard 测试通过 |
| `provider_switch_next_turn` | 当前 Run 冻结、下一 Turn 生效 | `PASS` | `TestProviderChangeAppliesToNextRunWithoutMutatingExistingSnapshot` 断言旧 Run 保持 model-a，新 Run 使用 model-b，并冻结 Provider、Thinking 与 Catalog 版本 |
| `restart_recovery` | Queue、Run、Receipt、Leaf 恢复 | `PASS` | Run/Queue/Receipt 既有恢复测试 + Active Leaf/Path 重启恢复测试 |

## 5. Release 1A 验收矩阵

| ID | 能力 | 状态 | 完成条件 |
|---|---|---|---|
| R1A-001 | 注册用户与个人私有项目 | `PASS` | 登录、Session、Owner-only 项目和跨用户隔离测试通过 |
| R1A-002 | 调用方端口和身份/授权/策略 | `PASS` | Worker 16 方法端口；HTTP Principal 与无身份 401；Project/Task 显式 Authorizer；逐消息 Scope 过滤；BlobStore；规划/执行统一 ExecutionPolicy；全量 Go 与双用户隔离回归通过 |
| R1A-003 | Provider-neutral 消息 | `PASS` | OpenAI-compatible 文本、Tool、Image、Thinking、StopReason、取消、流式工具分片和 Usage v2 Contract 全绿；原生 Anthropic/Gemini 按固定范围归 1B |
| R1A-004 | 上下文与 Compaction | `PASS` | Active Leaf typed transcript、真实 usage、初次/增量六节模板、文件清单、ToolResult Artifact、split-turn 和失败不推进全绿；同 Thread 新 Run 继承压缩感知消息上下文并保留当前目标顺序 |
| R1A-005 | Durable ToolBatch | `PASS` | 000046 批次持久化；并行/串行、审批收敛、源顺序、唯一 continuation、lease 恢复语义通过；并行兄弟失败会收齐后仅创建一个恢复分支；批次收据已提交但 continuation 尚未入队的崩溃窗口会自动补建模型续接，不再留下无可执行 Step 的 `running` Run |
| R1A-006 | Session Tree V2 | `PASS` | 000047、独立 entry_seq、事务追加、Active Leaf 上下文、Navigate/Fork/Clone/Label/五节 Summary/CAS/Busy Guard/分页/循环检测/重启恢复及 OpenAPI 通过；PostgreSQL 已按 V1 范围决策排除 |
| R1A-007 | 多实例 Provider | `PASS` | 000048 单例回填；同协议多实例与同名模型隔离；Registry REST；SecretStore 补偿；脱敏测试；stale-while-revalidate；Run ModelRef/Thinking/Catalog 冻结及下一 Turn 生效全绿 |
| R1A-008 | 文件与 Git 工具 | `PASS` | Provider-neutral Registry；read/edit/list/find/grep/write；status/diff/log/branch/commit/restore；新项目初始化空 Git 基线；未跟踪/中文路径、symlink、二进制、分页、脱敏、CAS、Artifact、Policy、审批和 Receipt 测试通过 |
| R1A-009 | 全局 Run 并发 | `PASS` | 默认 4 个唯一 lease owner；配置可降至 1–4；数据库限制不同活跃 Run；Durable ToolBatch 内并行保留；取消终态、SQLite Busy 退避和未知副作用保护通过 |
| R1A-010 | Codex 式工作台 | `PASS` | 四区 Shell、统一 Thread、桌面右侧 Inspector、Composer、Activity Spine、内联任务/审批及 1440/1280/1024/768/390 响应式 E2E 通过；Thread 操作收敛到“编辑名称/删除”下拉，执行记录的搜索/展开位于卡片顶部，任务流显示实际 Agent；运行中 Enter 引导当前任务、Alt+Enter 排队下一轮，明确自然语言可暂停/取消/恢复，停止后可安全删除 Thread |
| R1A-011 | 可恢复 SSE | `PASS` | 共享连接管理器覆盖单 Run 单连接、after、退避、去重、迟到订阅重放、auth/logout、可见性和 Query 失效 |
| R1A-012 | 首页与核心路由 | `PASS` | 首页、Thread、Tree、Approvals、Resources、Provider、Diagnostics 均有真实路由和浏览器验收 |
| R1A-013 | Changes 与 Diff | `PASS` | 冻结 Git HEAD/状态；三基线 API；Inspector 文件/Hunk 查看、接受、受治理撤销和选择提交；中文未跟踪文件的展示、选择、审批和提交 Playwright 闭环通过 |
| R1A-014 | 安全验收 | `PASS` | 跨用户、无身份 401、路径/symlink/CAS/TTL、敏感导入拒绝、Secret/Provider 脱敏、无共享默认口令及真实 Provider SecretStore 证据通过 |
| R1A-015 | 恢复验收 | `PASS` | 模型、工具、审批、Compaction、Tree、SQLite Busy、SSE、未知 Receipt 与真实子进程 kill 后 Lease 恢复自动化通过；`TestClaimNextStepRecoversCompletedBatchWithoutRunnableStep` 覆盖全部 Step 已终态但 Task/Run 仍运行的批次续接恢复 |
| R1A-016 | 语义计划与 Human Action | `PASS` | 持久计划、回退修复、Steering/Next Turn、统一 Human Action、请我审批/自动审查/完全访问和 Composer 邻接 UI 已实现；完全访问自动放行冻结风险 L0-L2，MCP L3 仍强制人工；暂停/取消/恢复由 Application 层确定性指令处理，裸“继续”不会劫持普通对话，取消后同 Thread 继任 Run 保留 Workspace 改动 |

## 6. Release 1B 验收矩阵

| ID | 能力 | 状态 | 完成条件 |
|---|---|---|---|
| R1B-001 | 受控命令执行 | `PASS` | 命令在 Thread 内通过 Policy/审批/Receipt 持久 Task 执行，支持状态、结果、取消和上下文回填；交互式 xterm 已按 V1 范围决策排除 |
| R1B-002 | Oktapod 导入导出 | `PASS` | 浏览器预览、原始敏感字段 fail-closed、正文模式脱敏、JSON/JSONL/HTML、Oktapod JSONL Thread round-trip、事务化重建/幂等和 E2E 通过；Pi 精确兼容已转 V1.1 |
| R1B-003 | Extensions/Packages | `PASS` | 通用 `MCPDefinition v1`、官方 Go SDK、本地 stdio/远端 Streamable HTTP、Registry/`server.json`/URL/固定版包/命令解析、资源中心/自然语言双入口、多 Secret、稳定可用状态和 Skill/MCP/Agent 启停删除已实现；标准远端默认按 OAuth challenge 自动发现，Registry 声明的 Secret Header 则生成安全绑定。Authorization Code/PKCE、浏览器回调、取消、动态客户端注册与刷新令牌 SecretStore 回写均有确定性测试。Tavily 发现 5 个工具并真实搜索，Filesystem 完成受限目录写读与越界拒绝；OAuth fixture 覆盖首次授权、取消和过期令牌刷新，故障 fixture 覆盖超时、非法 Schema、超量输出、进程缺失、SSRF/重定向与活动 Run 删除阻止。GitHub/Notion 无外部账号时仍完整验证发现、配置、审批和失败恢复，真实数据调用标记为可选外部烟测。单 Skill ZIP 严格校验覆盖路径穿越、Unicode 冲突、符号链接、嵌套 Skill、隐藏载荷和压缩炸弹。 |
| R1B-004 | Provider 协议与真实主链路 | `PASS` | 原生 Anthropic/Gemini 增量合同通过；OpenAI 兼容通道已经真实 MiniMax 端到端验证可见流式、工具、文件修改和回读哈希；更多原生真实凭据已按 V1 范围决策排除 |
| R1B-005 | 高级 Git | `PASS` | `git_conflicts`、按 Hunk 恢复、选择路径提交具备状态 Hash、Policy、审批与 Receipt；Changes UI 通过原 Thread 创建受治理动作。远程 Git 不进入 V1 |
| R1B-006 | V1 设置 | `PASS` | Context、Retry、安全、Shell、主题、快捷键和数据维护路由/来源展示及 E2E 已完成；主题在首次绘制前恢复，设置入口直达外观，Light/Dark/System 共用语义 token，工作台、Markdown 表格、任务卡、资源、审批、表单和弹窗完成跨页对比度回归；非敏感显示偏好允许浏览器持久化，完整服务端化进入 V1.1 |
| R1B-007 | 项目级审批规则 | `PASS` | 000049；API 强制二次确认；Tool + 确定性窄键；30 天 TTL；Owner 查看/撤销；撤销后恢复审批；Policy 静态拒绝不被绕过；Go 与 Playwright 验收通过 |

## 7. 前端验收

| 范围 | 必须覆盖 |
|---|---|
| 视口 | 1440、1280、1024、768、390 |
| 主题 | Light、Dark、System |
| 键盘 | 发送、换行、Steering、Follow-up、Abort、命令面板、焦点循环 |
| SSE | 断线、重连、去重、页面隐藏、登录失效 |
| 状态 | 空、加载、失败、审批、重试、中断、恢复 |
| 控制台 | 无 error、未处理 Promise rejection 和重复 EventSource |
| 性能 | Thread 分页、局部事件更新、重路由懒加载、无 390px 横向溢出 |

## 8. 发布门禁

本地 Release Candidate 必须同时满足：

1. 本矩阵全部 V1 个人版必选 1A/1B 和 OHCS 语义为 `PASS`；
2. `pnpm verify`、Playwright 主链路、带版本发布构建和独立临时数据目录产品烟测通过；
3. 真实 Provider 已证明流式、Tool、文件修改、验证和结果可见；
4. 安全、迁移、Receipt 与 Durable Recovery 的 V1 必选故障证据通过；
5. README、OpenAPI、架构、计划、部署、发布说明与代码一致；
6. 未完成项必须明确列入 V1.1/团队边界，不得伪装为已完成。

正式 tag 还必须由 Ubuntu CI 通过 race，且 Ubuntu/Windows CI 通过原生产品烟测。Tag、push 和远端发布均不自动执行。

## 9. 证据日志

| 日期 | 变更 | 证据 |
|---|---|---|
| 2026-07-26 | 建立 W0 基线 | `pnpm install --frozen-lockfile` 和 `pnpm verify` 通过 |
| 2026-07-26 | 建立 OHCS Runner 骨架 | 新增 `TestOHCSGoldenTraceTextTurn`，锁定事件与消息持久顺序 |
| 2026-07-26 | 修复 Thread 内联执行流上下文 | `ProjectPage` 改为在 `FileReviewProvider` 内消费上下文；production build 与 foundation E2E 通过 |
| 2026-07-26 | 采样既有浏览器基线 | foundation 1/1；success 4/16；turn 0/1；approval 0/1；auth 因未启动服务无有效产品结论 |
| 2026-07-26 | W1 端口与安全边界第一批 | Worker 16 方法端口；HTTP Principal/401；Local BlobStore；统一 ExecutionPolicy；相关 Go 单元与 integration 全绿 |
| 2026-07-26 | W1 完成门禁 | Project/Task 显式 RepositoryAuthorizer 接入；逐消息可见性保留查询级过滤；`pnpm verify` 全绿 |
| 2026-07-26 | W2 typed model protocol | 新增 ModelMessage/ContentBlock/ModelRef/StopReason；000045 持久化 purpose、through entry、requested/effective model、context/cache/usage version；Provider 与迁移合同通过 |
| 2026-07-26 | W3 typed Harness 与 Compaction | 唯一 TranscriptAssembler；Pi 初次/增量六节摘要；实际 usage + 尾部估算；split-turn；length 截断零执行；大 ToolResult Artifact |
| 2026-07-26 | W3 Durable ToolBatch | 000046；parallel/sequential；审批拒绝/过期整批收敛；源顺序 ToolResult；单 continuation claim |
| 2026-07-26 | W4 Session Tree V2 | 000047 修复迁移；Message/Entry/Leaf 事务原语；Active Leaf 上下文；Navigate/Fork/Clone/Label/五节 Summary；CAS/Busy Guard/分页/循环/重启恢复；OpenAPI |
| 2026-07-26 | W5 Provider Registry | 000048 将旧单例回填为默认实例；多 OpenAI-compatible 实例、目录 SWR、凭据补偿、REST、Provider-aware Chat/Task、Run 模型快照和错误脱敏；`go test ./... -count=1` 全绿 |
| 2026-07-26 | W6 Tool Registry 与 Workspace/Git | Adapter 不再硬编码 Schema；文件分页/脱敏/CAS/原子替换；Git 状态 Hash、审批与 Receipt；安全及冲突测试全绿 |
| 2026-07-26 | W6 全局并发与诊断 | 4 Worker 唯一 lease owner、数据库活跃 Run 上限、取消终态与 Busy 退避；`/diagnostics` 提供脱敏数据库/Provider/Worker/SSE 状态并隔离高风险维护 |
| 2026-07-26 | Release 1A Codex 工作台与浏览器收口 | 四区 Shell、统一执行流、任务状态条、Inspector、Session Tree、Provider/Resources/Diagnostics；foundation 1/1、success 22/22、auth 2/2、turn 1/1、approval 1/1 |
| 2026-07-26 | Release 1B 第一批 | 受控终端、设置、导入导出路由；Transcript 事务导入；原生 Anthropic/Gemini 合同；Hunk Restore、选择提交与冲突辅助；`pnpm verify` 全绿 |
| 2026-07-26 | Release 1B 项目级审批规则 | 000049、二次确认、30 天窄规则、Owner 管理与撤销；定向集成测试、`pnpm verify`、approval 1/1、foundation 1/1、success 22/22 通过 |
| 2026-07-28 | 语义计划与 Human Action | 000050～000052；Plan Revision/Item、请我审批/自动审查/完全访问、统一人工动作、MCP Annotation 风险、v2 精确签名与 Composer 邻接 UI；Go 集成、Web 18/18、foundation 1/1、success 22/22 通过，真实 Reviewer Provider 故障注入仍待补充 |
| 2026-07-26 | 原生 Provider 增量流式 | Anthropic SSE 与 Gemini streamGenerateContent 解析增量 Text/Thinking/Tool/StopReason/Usage，Adapter 合同测试通过；真实凭据验收待执行 |
| 2026-07-26 | Release 1B 终端与导出补全 | 终端状态/结果/取消/同 Thread 上下文回填；脱敏 JSON/JSONL/HTML 与自有 JSONL Thread round-trip；success 22/22 通过 |
| 2026-07-26 | SQLite 并发写恢复 | 注册、项目/任务创建、Transcript 导入和结果验收对 BUSY/BUSY_SNAPSHOT 有界退避并复用幂等键；并行 success 22/22 通过 |
| 2026-07-26 | Linux 门禁 | WSL Ubuntu `go test ./...` 全绿；独立 Linux 依赖目录下 Web 5 文件/18 测试和 production build 通过；临时目录已清理 |
| 2026-07-28 | 真实 Provider 端到端（MiniMax） | 经 CLI `model configure` 注入 MiniMax-M3（凭据仅入系统凭据库）；注册→建项目→真实任务闭环：可见流式、`workspace_write_file`/`workspace_read_file`/`workspace_find_files` 工具调用、文件真实落盘且 SHA256 与模型回执一致（`565339bc…`）；Activity Spine 步骤可视；`pnpm verify`、Web 18/18、success E2E 22/22 通过 |
| 2026-07-28 | 前端美化第一批 | Assistant 正文接 Markdown（GFM）渲染、`<think>` 折叠为可展开“推理过程”、Thread 列表摘要剥离 think/markdown 噪声、执行计划折叠卡片化、用量数字层次化、Composer 聚焦高亮及 dark 适配；`pnpm verify` 与 success E2E 22/22 零回归 |
| 2026-07-28 | 阻塞状态与跨 Run 上下文修复 | 000053 对齐 Task/Run 终态，活动任务判断改用 Run 真源；新 Run 冻结同 Thread 压缩感知消息；移除消息内重复计划/详情，Composer 与 Thread 侧栏显示阻塞状态；定向 Go、Web lint/test/build 通过 |
| 2026-07-29 | Codex 式状态真源与 WebAccess 修复 | 初次计划禁止提前完成；联网/文档交付增加收据完成门禁；Thread 状态改为尾部图标、执行流显示 Skill、Human Action 邻接 Composer、支持受控删除 Thread；DuckDuckGo 失败自动回退 Bing RSS。真实浏览器以 `/researchpilot` 完成 Palantir 官方资料检索：初始计划为 0/3、3 次 `web_search` 均成功、最终返回 3 个官方链接；风险命令已出现 Composer 邻接审批卡并可批准/拒绝；验证用阻塞 Thread 在窄屏菜单中删除成功。适配器、定向集成、Web lint/test/build 与浏览器控制台检查均通过 |
| 2026-07-29 | 完全访问与计划自恢复修正 | Composer 权限文案为“请我审批 / 自动审查 / 完全访问”；完全访问不再自动过期，由用户手动切换，静态硬拒绝和当时的 MCP 人工确认边界保留（2026-08-10 后细化为 L0-L2 自动执行、L3 确认）；运行中 `update_plan` 回退已完成项时保留真实计划并自动要求模型修复，连续达到模型重试上限后才阻塞交给用户；旧版任务级计划异常即使没有失败 Step，也能从持久计划与对话历史重建恢复检查点 |
| 2026-07-29 | Web Search 并发稳定性 | Agent 仍可并行规划工具，但共享的公共搜索适配器按请求串行访问 DuckDuckGo/Bing，避免同一批并发请求触发挑战页并被误判为“无可用结果”；并发契约测试和真实外网查询连续验证通过 |
| 2026-07-29 | 原 Thread 恢复、计划续版与紧凑消息流 | 浏览器在原 `调研token云平台形成md文档` Thread 内发送“重新规划任务，继续”：Auto Composer 原地恢复同一 Task/Run 与 `researchpilot`，确认消息不再走聊天模型；重试选择最新可执行前沿，冻结 ToolCall/Receipt 与批次续写状态一致。修复无活动计划时 Revision 从 1 重建导致的唯一键冲突，并保持 Step 心跳到完成事务提交。真实 Run 获得成功 Web Search 收据、计划完成 4/4、写入 3 个含 10 条 URL 且无占位符的 Markdown 文件，Task 最终 `succeeded`；Assistant 源空白和 comfortable density 间距已压缩，浏览器控制台无错误。定向 Go 测试与 Web build 通过 |
| 2026-07-28 | Skill/Harness 全流程复验 | 浏览器真实导入 `researchpilot`：同版本同 Hash 幂等返回原 Skill；`/researchpilot` 产生 `skill.invoked/skill.loaded`，目标特定 5 项计划实时刷新，搜索失败进入 `tool.recovery_queued` 后继续并最终完成 5/5；对话流式输出 Physical AI 中文简报和 16 个 Markdown 来源链接。同步修复计划空转、运行中结构改写、无进度自恢复、空域名参数归一化和成功搜索收敛门禁 |
| 2026-07-28 | 仓库与初始化安全收口 | 构建/文档路径统一为 `Build/` 与 `Doc/`；`sqlc` 输出回归 `Src/internal/`；浏览器自动化、旧生成目录和本机构建产物纳入忽略规则；移除源码与文档中的共享默认登录口令，首次使用改为页面注册 |
| 2026-07-30 | V1.0.0 RC 发布收口 | 新项目自动建立空 Git 基线；Changes 支持中文未跟踪文件并由原 Thread 审批提交；Abort 保留 Next Turn；`pnpm verify`、Web 20/20、Playwright foundation 1/1 + success 23/23、带版本 Windows 工件与独立临时数据目录产品烟测通过；工件 SHA256 `749c8e705bf0b5829a4ac8bbfff49a90c5e09d16b7168352e08b09eacb322d31` |
| 2026-07-30 | Thread 状态与失败恢复修复 | 初始计划占位在同一修订内替换为模型语义步骤；模型绕过 `update_plan` 直接执行时占位项原地物化为本次真实目标，无效/占位/过早完成/工具混用计划自动重试；可授予权限进入审批，确定性拒绝及其混合工具批次转模型修正，旧拒绝步骤保留审计但不再污染最终状态；重试优先恢复策略拒绝检查点；成功任务同步全部计划步骤为完成。Auto 分类断线改为先持久化 Task，空 Thread “继续”拒绝无上下文猜测；Thread 名称编辑/删除下拉、任务流 Agent 标识、顶部执行记录搜索/展开已实现。`pnpm verify` 全绿，正式 `127.0.0.1:8787` HTML 创建/验证/打开流程已通过 |
| 2026-08-03 | Tavily MCP 双入口与安全凭据闭环 | 资源中心与新 Thread 自然语言安装复用同一安装草稿、校验、审核和连接流程；凭据仅入 SecretStore，Manifest/SQLite/API/导出不含原文；连接发现 `tavily_search`、`tavily_extract`、`tavily_crawl`、`tavily_map`、`tavily_research` 5 个工具。修复工作场景动态 MCP 被白名单过滤、Auto 安装误入只读 Chat、显式 Tavily 被内建搜索/工作区工具绕过及 MCP 无效持久授权按钮；真实浏览器 Run 产生 6 个成功 Tavily 搜索/提取 Receipt 并完成来源综合，定向 Go 与 Web lint/test/build 通过 |
| 2026-08-03 | 通用资源中心与 MCP/Skill 生命周期 | 新增规范化 `MCPDefinition v1` 与固定版官方 MCP Go SDK；本地/远端 MCP 共用 SecretStore、审核、健康与删除流程；资源中心改为已安装优先的两栏布局和真实“安装 → 验证 → 可用”路径，移动端为列表到全屏详情；菜单 Skill 安装收敛为单 ZIP 严格校验，内置只读 Skill Creator 从对话入口生成兼容副本；OpenAPI 新增 ResourceAvailability、SecretBinding、MCPDefinitionV1 和 ResourceInstallDraft 公共结构。 |
| 2026-08-04 | 全局主题一致性与可读性收口 | 合并旧工作台与 Codex Shell 两套颜色语义，主题在 React 首次绘制前恢复；设置入口改为直达外观，Provider 页新增“主题与外观”入口；修复深色 Markdown 表格近黑正文、链接/成功状态对比度、白底任务胶囊，以及工作台、资源、审批、Composer、弹窗和表单的主题混用。真实浏览器在首页、设置、Provider、资源、审批和历史 Thread 完成深色跨页扫描，无横向溢出且主要文字不再低于 4.5:1。 |
| 2026-08-04 | 无可执行 Step 的活动任务恢复与停止入口 | 修复工具批次收据已提交但模型 continuation 未入队时 Task/Run 永久保持 `running`、计划停在 `0/N` 且 Thread 无法删除的问题：Worker 空闲领取会原子补建续接 Step 并记录 `tool.batch_recovered`；Composer 在活动任务且输入为空时显示明确“停止任务”键，取消后同步任务与计划查询并移除 Composer 上方的终态任务胶囊，历史仍保留在执行记录和侧栏。真实遗留 Thread 已确认全部 Step 终态而 Task/Run/Plan 仍活跃，作为本次回归样本。 |
| 2026-08-04 | 通用 MCP 来源与自然语言任务控制 | 删除 Tavily 前端预设和正则，名称发现改用官方 MCP Registry，支持 `server.json`、规范化 JSON、URL、固定版 npm/PyPI 包和本地命令并在歧义时要求选源；自然语言安装草稿由父级工作区持有，路由创建新 Thread 时配置面板不会丢失。新增多 Secret 集合、内置只读 Skill Creator、确定性暂停/取消/恢复指令和 Steering/Next Turn 排队交互。真实浏览器验证 1440×900、1280×800、1024×768、390×844 明暗主题无横向溢出，移动端为列表到全屏详情；产品级 OAuth 仍按 `PARTIAL` 跟踪。 |
| 2026-08-10 | MCP OAuth 与故障矩阵关闭 | 远端 Registry/URL 在无 Secret Header 声明时使用标准 OAuth challenge 自动发现，声明 Header 时生成 Bearer/Custom Header 绑定；官方 Go SDK 完成 DCR、PKCE、浏览器回调、取消与刷新令牌 SecretStore 回写。资源中心使用单一“连接账号”动作和自动状态轮询，Skill ZIP 明确区分客户端预检与服务端完整校验，失败可转 Skill Creator 对话修复副本。Windows 直接以 `node.exe` 启动 npx CLI 并隔离运行缓存；官方 Filesystem MCP 真实写读与越界拒绝通过，微软 Playwright MCP `0.0.78` 以显式 Edge 路径打开本地产品并返回可访问性内容，Git MCP `2026.7.10` 对临时仓库完成 status/log。Git 包未给 Python MCP SDK 声明主版本上限，裸解析会与 v2 失配；验收以可审查的 `mcp<2` 命令约束固定，产品不静默猜测第三方依赖。OAuth、超时、非法工具 Schema、超量输出、stdio 进程缺失、完整停用→重启用→重连和活动 Run 删除阻止均有确定性测试；真实浏览器在 1440×900、1280×800、1024×768、390×844 明暗主题完成资源列表、连接修复、移动详情和 ZIP 错误回归，Success E2E 23/23 通过。 |
| 2026-08-10 | 证据驱动完成协议与 Skill 续轮 | 复盘真实 Physical AI 任务确认：MCP 研究成功后 Provider 工具参数截断，重试只说“开始写文件”，旧运行时即误判成功；同时显式 `researchpilot` 只在首轮加载且 6000 字正文裁掉末尾 QA/交付门禁。完成协议现统一要求联网/MCP Receipt、真实工作区变更、变更后验证、最新 Artifact 与显式字数，失败进入受控修复；显式长 Skill 跨计划、工具、截断与完成修复续轮继承。真实重跑进一步修复显式 MCP 误删工作区工具、完全访问仍重复申请命令能力、Thinking 只展示不下传、GLM 交错推理未续接、推理到上限空转、搜索次数/禁用约束不跨 Run 且可用 `map` 绕过、MCP 长调用无截止，以及终态“继续当前任务，并……”丢失原目标/Skill/证据的问题；预算耗尽会同时收缩 Tool Schema 与执行门禁，默认模型输出已在同日极限回归后校准为 8192，恢复 16384/32768，MCP 截止 10 分钟。相关反例、适配器、续作与完成门禁测试通过。 |
| 2026-08-10 | 单一完整报告与预算实跑校准 | 从网页新建独立 Thread，以自然语言调用 `researchpilot@0.1.0` 和已连接 Tavily MCP；3 次互补搜索均为 L2/allow、无审批并产生成功 Receipt。Run 最终完成 11/11，单一 `physical_ai_complete_report_2026.md` 独立核验 30,021 个汉字、77 个 URL、15/15 必需章节、无 TODO/TBD/待补充/续写锚点；总用量 67 次模型调用、778,648 输入 + 61,272 输出 Token，证明旧 32/200k 和候选 64 次上限不足，默认最终校准为 80 次/1,000,000 Token，单次输出 8192→16384→32768。完成门禁改为单一主 Artifact 而非分片求和，Skill `name@version` 可解析，完全访问仅自动放行 L0-L2，50 个不可变文件版本在 UI 收敛为 1 个最新交付文件。 |
| 2026-08-10 | 并发调度短暂锁恢复 | 完整门禁复现两个 SQLite 调度实例同时领取同一 Schedule 时，胜出实例可能在创建任务阶段遭遇短暂 `SQLITE_BUSY`，造成 Fire 已记录但任务未创建。Scheduler 现基于既有 `schedule-fire-{id}` 幂等键执行有界退避重试；单元反例与双实例并发用例连续 50 轮通过，不扩大租约或绕过失败审计。 |
