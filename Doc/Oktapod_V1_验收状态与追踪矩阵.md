# Oktapod V1 验收状态与追踪矩阵

> 版本：v1.0
> 更新日期：2026-07-29
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

## 2. 当前发布判断

| 发布 | 状态 | 判断 |
|---|---|---|
| V1 Release Candidate | `NOT_STARTED` | 必须等待 W1-W6 全部验收门禁通过 |
| 历史 Release 1A/1B | `ARCHIVED` | 原 Release 1A/1B 范围已并入 V1 统一规划，不再单独追踪 |

## 2.1 V1 差距驱动批次总览

| 批次 | 目标 | 状态 | 关键交付 |
|---|---|---|---|
| B0 | 基线与验收真源 | `PASS` | 执行计划、总体架构、README、部署指南和本矩阵已统一范围；后续按代码事实持续同步 |
| B1 | 内核端口与运行时拆分 | `PARTIAL` | Repository 窄端口及 `agentloop`/`harness` 已落地并通过测试；Worker 与页面物理拆分仍未达到行数门禁 |
| B2 | 执行安全与工程门禁 | `PARTIAL` | `ExecutionAuthorization`、宿主 Git/Secret 隔离、多阶段非 root Docker、CI/OpenAPI/迁移门禁已落地；Windows 无 CGO，race 与 Docker 真实运行待 Linux CI |
| B3 | ToolSpec 与 Pi 语义 | `PARTIAL` | 统一 ToolSpec 元数据、简洁别名与旧名兼容已通过定向测试；命令增量事件和完整长输出 Artifact 待补 |
| B4 | MCP/Skill/搜索 | `PARTIAL` | MCP Schema Hash/配置版本/健康状态、Run Tool Snapshot、Skill 能力字段和 strict 缺失阻塞已落地；Envelope 与完整同能力选择待补 |
| B5 | Codex 用户闭环 | `PARTIAL` | Composer 已收敛并显示宿主安全边界；Run 开始/Git HEAD/Index Changes API/UI 已有集成证据；行/Hunk 与确定性撤销/提交待补 |
| B6 | 发布收口 | `PARTIAL` | 当前 `pnpm verify` 全绿，Playwright foundation 1/1、success 22/22；Docker smoke 脚本覆盖注册、会话与建项目；Linux、真实 Docker/Provider 和故障矩阵待执行 |

## 2.2 V1 正式范围决策

| 项目 | 状态 | 依据 |
|---|---|---|
| 团队项目主流程 | `N/A` | V1 仅交付注册用户的个人私有项目；保留代码边界，不计完成度 |
| PostgreSQL 发布门禁 | `N/A` | V1 仅 SQLite；PostgreSQL 适配保留但不阻塞发布 |
| 交互式 xterm | `N/A` | 命令在原 Thread 内以受治理 Task 执行，不恢复独立 Terminal |
| Pi v1/v2/v3 精确导入 | `N/A` | V1 只保证 Oktapod 自有格式 round-trip；Pi 精确兼容进入 V1.1 |
| 原生 Anthropic/Gemini 真实凭据 | `N/A` | V1 要求至少一个真实 Provider 主链路；原生协议保留合同测试 |
| 完整设置服务端化 | `N/A` | V1 允许非敏感显示偏好保存在浏览器；安全和运行配置必须服务端持久化 |

## 3. 基线门禁

| ID | 验收项 | 状态 | 当前证据 |
|---|---|---|---|
| BASE-001 | `pnpm install --frozen-lockfile` 可复现依赖 | `PASS` | 2026-07-26 初始基线执行成功；随后为根目录 E2E import 补充 Playwright 依赖并更新锁文件，重新安装成功 |
| BASE-002 | `pnpm verify` 全绿 | `PASS` | Go vet/test、ESLint、18 个 Vitest、TypeScript、Vite 和 Go build 通过 |
| BASE-003 | 默认 E2E foundation/success | `PASS` | foundation 1/1、success 22/22 通过，覆盖核心工作流、Provider、Tree、1B 路由和五个响应式视口 |
| BASE-004 | auth/turn/approval E2E | `PASS` | auth desktop/mobile 2/2、turn 1/1、approval 1/1 通过 |
| BASE-005 | Windows 与 Linux 双平台 | `PARTIAL` | Windows `pnpm verify`/全部浏览器套件通过；WSL Ubuntu Go 全量测试、Web 18/18 和生产构建通过。Linux 真实浏览器项目 E2E 尚未执行 |
| BASE-006 | 前端首包容量 | `PASS` | 主 JS 约 334KB；Terminal、Settings、Import/Export 已拆分为约 3.35KB、7.57KB、8.27KB 的懒加载路由块 |

## 4. OHCS Golden Trace

冻结 Pi 基线：`earendil-works/pi@b1c444d9`。

| Fixture | 要验证的语义 | 状态 | 自动化证据 |
|---|---|---|---|
| `text_multi_turn` | Event Sequence、消息 Role、持久序号 | `PARTIAL` | `TestOHCSGoldenTraceTextTurn` 已覆盖单 Turn；多 Turn 待 W2 |
| `tool_call_and_result` | Tool Call/Result 配对和下一 Turn | `PASS` | `TranscriptAssembler` 与 Skill/Tool continuation 测试断言同 ID 配对 |
| `parallel_tool_batch` | 并行完成顺序与源结果顺序 | `PASS` | 并行领取、真实完成事件、源顺序回灌和单 continuation 测试通过 |
| `sequential_tool_override` | 任一 sequential 使整批串行 | `PASS` | `TestSequentialDeclarationForcesWholeToolBatchToSourceOrder` |
| `length_truncated_tool_calls` | length 截断时禁止执行并回灌错误 | `PASS` | 截断调用零执行、`tool.skipped`、错误 ToolResult 和恢复提示测试通过；未解析出完整 ToolCall 的单 Agent 截断也会自动递增额度重试，不再误报完成 |
| `steering_after_checkpoint` | checkpoint 后注入且不丢失 | `PARTIAL` | 已有集成测试；待纳入统一 Trace |
| `followup_after_idle` | Agent 原本结束后消费 | `PARTIAL` | 已有集成测试；待纳入统一 Trace |
| `abort_preserves_next_turn` | Abort 清队列但保留 Next Turn | `NOT_STARTED` | Next Turn 尚未完成 |
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
| R1A-005 | Durable ToolBatch | `PASS` | 000046 批次持久化；并行/串行、审批收敛、源顺序、唯一 continuation、lease 恢复语义通过；并行兄弟失败会收齐后仅创建一个恢复分支，不再由首个 Tool Error 提前终止 Run |
| R1A-006 | Session Tree V2 | `PASS` | 000047、独立 entry_seq、事务追加、Active Leaf 上下文、Navigate/Fork/Clone/Label/五节 Summary/CAS/Busy Guard/分页/循环检测/重启恢复及 OpenAPI 通过；PostgreSQL 已按 V1 范围决策排除 |
| R1A-007 | 多实例 Provider | `PASS` | 000048 单例回填；同协议多实例与同名模型隔离；Registry REST；SecretStore 补偿；脱敏测试；stale-while-revalidate；Run ModelRef/Thinking/Catalog 冻结及下一 Turn 生效全绿 |
| R1A-008 | 文件与 Git 工具 | `PASS` | Provider-neutral Registry；read/edit/list/find/grep/write；status/diff/log/branch/commit/restore；路径/symlink/二进制/分页/脱敏；edit 与 Git Hash 冲突；Artifact、Policy、审批和 Receipt 测试通过 |
| R1A-009 | 全局 Run 并发 | `PASS` | 默认 4 个唯一 lease owner；配置可降至 1–4；数据库限制不同活跃 Run；Durable ToolBatch 内并行保留；取消终态、SQLite Busy 退避和未知副作用保护通过 |
| R1A-010 | Codex 式工作台 | `PASS` | 四区 Shell、统一 Thread、桌面右侧 Inspector、Composer、Activity Spine、内联任务/审批及 1440/1280/1024/768/390 响应式 E2E 通过；项目侧栏已去除重复 Resources 和未完成 Terminal 入口 |
| R1A-011 | 可恢复 SSE | `PASS` | 共享连接管理器覆盖单 Run 单连接、after、退避、去重、迟到订阅重放、auth/logout、可见性和 Query 失效 |
| R1A-012 | 首页与核心路由 | `PASS` | 首页、Thread、Tree、Approvals、Resources、Provider、Diagnostics 均有真实路由和浏览器验收 |
| R1A-013 | Changes 与 Diff | `PARTIAL` | Run 创建时冻结 Git HEAD/状态，`GET /runs/{run_id}/changes` 和 Inspector 支持 Run 开始、Git HEAD、Index 三基线；已有真实 Git 集成测试。文件/行/Hunk 定位和选择提交 UI 尚未完整验收 |
| R1A-014 | 安全验收 | `PARTIAL` | 跨用户、无身份 401、路径、symlink、CAS、TTL、原始导入敏感键拒绝、Secret 和 Provider 错误脱敏测试通过；首次启动不再写入共享默认口令，真实 Provider（MiniMax）凭据仅经 SecretStore 注入且未落盘；Linux 日志审计待验证 |
| R1A-015 | 恢复验收 | `PARTIAL` | 模型、工具、审批、Compaction、Tree、SQLite Busy 与 SSE 恢复自动化通过；完整进程级故障注入矩阵尚未全部执行 |
| R1A-016 | 语义计划与 Human Action | `PARTIAL` | 000050～000052、Plan Revision/Item、模型 `update_plan` 真实语义清单/状态更新、首轮执行证据门禁、运行中稳定计划/结构改写投影/无进度自恢复、成功搜索收敛综合、Steering supersede、恢复耗尽转人工、统一 Human Action API/SSE、Ask/Auto-review/Full Access、MCP 强制人工和 Composer 邻接 UI 已实现；真实 Reviewer Provider、浏览器 SSE 断线恢复及移动端完整 E2E 尚待执行 |

## 6. Release 1B 验收矩阵

| ID | 能力 | 状态 | 完成条件 |
|---|---|---|---|
| R1B-001 | 受控命令执行 | `PASS` | 命令在 Thread 内通过 Policy/审批/Receipt 持久 Task 执行，支持状态、结果、取消和上下文回填；交互式 xterm 已按 V1 范围决策排除 |
| R1B-002 | Oktapod 导入导出 | `PASS` | 浏览器预览、原始敏感字段 fail-closed、正文模式脱敏、JSON/JSONL/HTML、Oktapod JSONL Thread round-trip、事务化重建/幂等和 E2E 通过；Pi 精确兼容已转 V1.1 |
| R1B-003 | Extensions/Packages | `PARTIAL` | 资源中心区分 Built-in Tools、Packages、MCP、Skills 与 Agents，支持全局/项目作用域以及 SKILL.md/ZIP/JSON/YAML 两阶段校验/安装、风险隔离和人工启用；同版本同 Hash 幂等、同版本异内容冲突、新版本并存已有集成测试。2026-07-28 浏览器已完成 researchpilot 项目级 ZIP 安装、审核、显式调用、联网检索与报告产物闭环；完整包兼容矩阵仍待补 |
| R1B-004 | Provider 协议与真实主链路 | `PASS` | 原生 Anthropic/Gemini 增量合同通过；OpenAI 兼容通道已经真实 MiniMax 端到端验证可见流式、工具、文件修改和回读哈希；更多原生真实凭据已按 V1 范围决策排除 |
| R1B-005 | 高级 Git | `PARTIAL` | `git_conflicts`、按 Hunk 恢复和选择路径提交具备状态 Hash、Policy、审批与 Receipt 测试；直接 UI 操作和受控远程操作评估未完成 |
| R1B-006 | V1 设置 | `PASS` | Context、Retry、安全、Shell、主题、快捷键和数据维护路由/来源展示及 E2E 已完成；非敏感显示偏好允许浏览器持久化，完整服务端化进入 V1.1 |
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
| 性能 | Thread 分页、局部事件更新、Monaco/xterm 懒加载、无 390px 横向溢出 |

## 8. 发布门禁

只有同时满足以下条件才可将第一阶段标记为完成：

1. 本矩阵所有 1A、1B 必选项为 `PASS`；
2. 所有 OHCS Fixture 为 `PASS`；
3. `pnpm verify` 和全部 Playwright 套件通过；
4. Windows 与 Linux 各完成一次真实项目 E2E；
5. 使用真实 Provider 完成流式回复、Tool、审批、文件修改、测试和 Diff；
6. 安全、迁移和 Durable Recovery 故障注入通过；
7. README、OpenAPI、设计、部署与验收文档与代码一致；
8. 未完成或未验证项不得通过修改措辞降级为“已完成”。

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
| 2026-07-28 | 语义计划与 Human Action | 000050～000052；Plan Revision/Item、Ask/Auto-review/Full Access、统一人工动作、MCP Annotation 风险、v2 精确签名与 Composer 邻接 UI；Go 集成、Web 18/18、foundation 1/1、success 22/22 通过，真实 Reviewer Provider 故障注入仍待补充 |
| 2026-07-26 | 原生 Provider 增量流式 | Anthropic SSE 与 Gemini streamGenerateContent 解析增量 Text/Thinking/Tool/StopReason/Usage，Adapter 合同测试通过；真实凭据验收待执行 |
| 2026-07-26 | Release 1B 终端与导出补全 | 终端状态/结果/取消/同 Thread 上下文回填；脱敏 JSON/JSONL/HTML 与自有 JSONL Thread round-trip；success 22/22 通过 |
| 2026-07-26 | SQLite 并发写恢复 | 注册、项目/任务创建、Transcript 导入和结果验收对 BUSY/BUSY_SNAPSHOT 有界退避并复用幂等键；并行 success 22/22 通过 |
| 2026-07-26 | Linux 门禁 | WSL Ubuntu `go test ./...` 全绿；独立 Linux 依赖目录下 Web 5 文件/18 测试和 production build 通过；临时目录已清理 |
| 2026-07-28 | 真实 Provider 端到端（MiniMax） | 经 CLI `model configure` 注入 MiniMax-M3（凭据仅入系统凭据库）；注册→建项目→真实任务闭环：可见流式、`workspace_write_file`/`workspace_read_file`/`workspace_find_files` 工具调用、文件真实落盘且 SHA256 与模型回执一致（`565339bc…`）；Activity Spine 步骤可视；`pnpm verify`、Web 18/18、success E2E 22/22 通过 |
| 2026-07-28 | 前端美化第一批 | Assistant 正文接 Markdown（GFM）渲染、`<think>` 折叠为可展开“推理过程”、Thread 列表摘要剥离 think/markdown 噪声、执行计划折叠卡片化、用量数字层次化、Composer 聚焦高亮及 dark 适配；`pnpm verify` 与 success E2E 22/22 零回归 |
| 2026-07-28 | 阻塞状态与跨 Run 上下文修复 | 000053 对齐 Task/Run 终态，活动任务判断改用 Run 真源；新 Run 冻结同 Thread 压缩感知消息；移除消息内重复计划/详情，Composer 与 Thread 侧栏显示阻塞状态；定向 Go、Web lint/test/build 通过 |
| 2026-07-29 | Codex 式状态真源与 WebAccess 修复 | 初次计划禁止提前完成；联网/文档交付增加收据完成门禁；Thread 状态改为尾部图标、执行流显示 Skill、Human Action 邻接 Composer、支持受控删除 Thread；DuckDuckGo 失败自动回退 Bing RSS。真实浏览器以 `/researchpilot` 完成 Palantir 官方资料检索：初始计划为 0/3、3 次 `web_search` 均成功、最终返回 3 个官方链接；风险命令已出现 Composer 邻接审批卡并可批准/拒绝；验证用阻塞 Thread 在窄屏菜单中删除成功。适配器、定向集成、Web lint/test/build 与浏览器控制台检查均通过 |
| 2026-07-29 | Full Access 与计划自恢复修正 | Composer 权限文案恢复为“请我审批 / 自动审查 / Full Access”；Full Access 不再自动过期，由用户手动切换且仍受硬拒绝和 MCP 人工确认约束；运行中 `update_plan` 回退已完成项时保留真实计划并自动要求模型修复，连续达到模型重试上限后才阻塞交给用户；旧版任务级计划异常即使没有失败 Step，也能从持久计划与对话历史重建恢复检查点 |
| 2026-07-29 | Web Search 并发稳定性 | Agent 仍可并行规划工具，但共享的公共搜索适配器按请求串行访问 DuckDuckGo/Bing，避免同一批并发请求触发挑战页并被误判为“无可用结果”；并发契约测试和真实外网查询连续验证通过 |
| 2026-07-29 | 原 Thread 恢复、计划续版与紧凑消息流 | 浏览器在原 `调研token云平台形成md文档` Thread 内发送“重新规划任务，继续”：Auto Composer 原地恢复同一 Task/Run 与 `researchpilot`，确认消息不再走聊天模型；重试选择最新可执行前沿，冻结 ToolCall/Receipt 与批次续写状态一致。修复无活动计划时 Revision 从 1 重建导致的唯一键冲突，并保持 Step 心跳到完成事务提交。真实 Run 获得成功 Web Search 收据、计划完成 4/4、写入 3 个含 10 条 URL 且无占位符的 Markdown 文件，Task 最终 `succeeded`；Assistant 源空白和 comfortable density 间距已压缩，浏览器控制台无错误。定向 Go 测试与 Web build 通过 |
| 2026-07-28 | Skill/Harness 全流程复验 | 浏览器真实导入 `researchpilot`：同版本同 Hash 幂等返回原 Skill；`/researchpilot` 产生 `skill.invoked/skill.loaded`，目标特定 5 项计划实时刷新，搜索失败进入 `tool.recovery_queued` 后继续并最终完成 5/5；对话流式输出 Physical AI 中文简报和 16 个 Markdown 来源链接。同步修复计划空转、运行中结构改写、无进度自恢复、空域名参数归一化和成功搜索收敛门禁 |
| 2026-07-28 | 仓库与初始化安全收口 | 构建/文档路径统一为 `Build/` 与 `Doc/`；`sqlc` 输出回归 `Src/internal/`；浏览器自动化、旧生成目录和本机构建产物纳入忽略规则；移除源码与文档中的共享默认登录口令，首次使用改为页面注册 |
