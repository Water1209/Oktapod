# Oktapod 第一阶段补全改造方案

> **文档版本**：V2.0（内核先行修订版）
> **文档类型**：改造方案 / 实施输入
> **编制日期**：2026-07-25
> **基线**：`Oktapod_第一阶段产品功能设计_V1.0.md`、`Oktapod_三阶段递进式架构设计_V1.0.md`、Pi 冻结基线 `earendil-works/pi@b1c444d`
> **对比依据**：`Doc/research/pi-vs-oktapod-agent-loop.md`、`pi-vs-oktapod-tools-security.md`、`pi-vs-oktapod-provider-model.md`

---

## 0. 修订说明（V1.0 → V2.0）

V1.0 是功能驱动（缺什么补什么）。架构复审后修正为**内核先行**：

1. **承接性优先**：第一阶段交付物必须是后续阶段的稳定地基。三阶段文档要求的 6 个不可变内核中，当前代码有 2 个未对齐——`application/repository.go` 是 105 方法巨型接口（违反"禁止 Storage 巨型接口"）、身份模型是具体 `Scope{OrgID, UserID}` 而非 `PrincipalResolver` 端口。现在不拆，第二阶段加功能时接口膨胀到不可拆，等于重写。
2. **前端不是补全是重构**：`project-page.tsx` 732 行塞 24 个组件（上帝组件）、`styles.css` 2865 行单文件无设计系统、路由仅 5 个。第一阶段的信息架构若按现状长，第二阶段每个页面都要重写。
3. **单用户简化而非删除**：团队能力（users/orgs/RLS/PostgreSQL）已实现，保留代码、运行时降级为 personal 固定单租户（`local/local-user`）。第二阶段是"打开开关"，不是重写。

**总原则不变**：不重写、保留已验收能力、效果对标 Pi 不劣于 Pi。新增原则：**先拆内核端口，再长功能；前端按信息架构重构，与后端并行。**

---

## 1. 改造总策略

| 策略 | 说明 |
|---|---|
| 内核先行 | 先拆 Repository/Principal/Policy/Artifact 端口（无行为变化），再在端口上长功能 |
| 前端重构 | 按功能设计 5.1/5.2 信息架构重建：设计系统 → 组件库 → 拆上帝组件 → 路由 → 四区工作台 |
| 保留自有优势 | steering 事务化重规划、持久化 step 循环、七层审批、Run 级模型快照、OS keyring |
| 直接采用 Pi | 上下文保真、压缩 EXACT 模板、并行工具、ThinkingLevelMap、目录缓存、read/edit/ls/find/grep/git 工具语义、Session Tree navigate/fork/branchSummary 语义 |
| 团队能力降级 | 保留代码，personal profile 下不暴露多用户入口，数据层固定单租户 |

---

## 2. 不可变内核对齐（三阶段承接判据）

| 三阶段内核要求 | 当前状态 | 改造动作 |
|---|---|---|
| 协议稳定（Message/Tool/Session Entry/Run Event） | RunEvent/Tool 稳定；Session Entry 已建迁移 000044 | 补齐 Session Entry 服务层 |
| Harness 生命周期/Turn Snapshot/保存点 | 已有（step+checkpoint） | ✅ 不动 |
| Project/Thread/Task/Run 领域关系 | 有，Thread=Conversation 未显式化 | 小改：Conversation 即 Thread，Session Entry 树承载分支 |
| **存储端口按领域拆分** | ❌ 105 方法巨型接口 | **拆 Session/Project/Task/Run/Resource/Usage 六接口** |
| **身份授权端口** | ❌ 具体 Scope 直传 | **PrincipalResolver + Authorizer 端口，Local 实现** |
| **执行策略端口** | ❌ 审批/路径/网络散在 worker | **ExecutionPolicy 端口统一** |
| Artifact 端口 | ❌ 直接写文件系统 | **ArtifactStore 端口，本地 FS 实现** |
| 作用域/全局 ID/事件序号 | 有 | ✅ 不动 |
| Pi 兼容测试 | 无 | 补 Golden Trace 契约测试 |

---

## 3. 分期实施计划（约 6 周，前后端并行）

### W1 — 后端端口拆分 / 前端设计系统

**后端（纯重构，无行为变化，全量测试必须全程绿）**

| 项 | 内容 | 验收 |
|---|---|---|
| B1.1 | `application/repository.go` 105 方法 → 按领域拆 6 接口：`SessionRepository`（conversation/message/session_entry/compaction/queue）、`ProjectRepository`（project/member/invitation/plan/asset）、`TaskRepository`（task/run/step/checkpoint/budget）、`RunRepository`（run_event/usage/approval/side_effect/artifact）、`ResourceRepository`（capability/memory/agent/team/mcp/schedule）、`UsageRepository`（usage_ledger/model_call）。Store 单实现聚合六接口，调用方按需引用 | `go build` + 全量测试绿；接口定义在调用方包内（11.1：接口由调用它的包拥有） |
| B1.2 | `PrincipalResolver` 端口：`Resolve(ctx, RequestIdentity) (Principal, error)`；`LocalPrincipalResolver` 返回固定 `local/local-user`。替换 service.go 里 `ScopeFor` 直传 Scope 的入口 | 行为不变 |
| B1.3 | `ExecutionPolicy` 端口：`Evaluate(ctx, PolicyInput) (PolicyDecision, error)`；收敛 worker 里散落的路径/命令/网络/审批判断 | 审批链路走统一端口 |
| B1.4 | `ArtifactStore` 端口：`Put/Open/Delete`；本地 FS 实现（现状平移），DB 只存元数据+Hash | 行为不变 |

**前端**

| 项 | 内容 | 验收 |
|---|---|---|
| F1.1 | 设计 token 层：颜色/间距/字号/圆角/阴影 CSS 变量（消灭硬编码 hex） | tokens.css |
| F1.2 | 基础组件库：Button/Card/Dialog/Panel/Tabs/Input/Badge，替换散件 | 组件目录 + Storybook 式示例页 |

### W2 — 后端 Harness 语义（Pi 直接采用项）/ 前端拆上帝组件

**后端**

| 项 | 内容 | 依据 |
|---|---|---|
| B2.1 | **上下文保真**：模型调用从"轨迹重组 JSON 塞 user prompt（96KB 截断）"改为消息级历史（user/assistant/toolResult 序列）；上下文构造切换到 session_entries 路径 | Pi 对比最大短板；与 Session Tree 合并 |
| B2.2 | **压缩升级**：Pi EXACT 六节模板（Goal/Constraints & Preferences/Progress(Done/In Progress/Blocked)/Key Decisions/Next Steps/Critical Context）+ 真实 usage 触发（替代字符启发式）+ 不切断 toolResult/turn 的 cut point + 增量更新 prompt + 确定性文件清单 | 压缩后摘要结构符合模板，可续跑任务 |
| B2.3 | **并行工具 + 截断作废**：无依赖工具并发执行；模型输出被 max_tokens 截断时该 turn 标记无效重试 | 契约测试 |

**前端**

| 项 | 内容 |
|---|---|
| F2.1 | 拆 `project-page.tsx` 24 组件 → `features/{conversation, management, settings, usage, capabilities, memories, agents, assets}` 独立文件 |
| F2.2 | 路由骨架按 5.1：`/`（首页）、`/projects/:id`（工作台）、`/projects/:id/tree`（Session Tree）、`/resources`、`/settings/providers`、`/diagnostics` |

### W3 — 后端 Thinking/模型层 / 前端四区工作台

**后端**

| 项 | 内容 |
|---|---|
| B3.1 | Thinking Level 全链路：枚举 off/minimal/low/medium/high/xhigh/max（与 Pi 一致）+ `ThinkingLevelMap`（每模型级别映射，null=不支持）+ 存 Run 快照 + Provider 不支持时透明降级提示 |
| B3.2 | ModelDescriptor 扩展：`MaxTokens`、`InputModalities[]`、`Cost{Input,Output,CacheRead,CacheWrite}`、ThinkingLevelMap |
| B3.3 | 目录缓存：`model_catalog_cache` 表存最近成功目录 + ETag，离线可选 |

**前端**

| 项 | 内容 |
|---|---|
| F3.1 | 四区工作台布局（5.2）：左导航（Threads/Tasks/Files/Changes/Resources/Session Tree）/ 中央 Thread 执行区 / 右检查器（Run/Context/Usage/Tools/Security/Changes/Trace）/ 底 Composer |
| F3.2 | Composer 邻接执行区：一个活动 Plan Revision，摘要不显示任务名；Human Action 位于输入框正上方；阻塞后原位显示终态与恢复入口，Thread 列表显示尾部状态；消息流只保留默认收起的历史工具证据与文件产物，不重复计划和任务详情 |

### W4 — 后端 Session Tree 服务层 / 前端 Session Tree 面板

**后端**（建在 W1 SessionRepository 端口上，语义对照 Pi 逐项对齐）

| 项 | 内容 |
|---|---|
| B4.1 | AppendEntry：新消息双写 messages + session_entries（seq 对齐 server_seq） |
| B4.2 | NavigateTo(entry)：Leaf 移动；用户消息节点回填 Composer，其他节点直接继续；新输入追加子 Entry 不改历史 |
| B4.3 | ForkFrom(entry)：从用户消息 Entry 建新 Session，parent_session_id 记录来源，复制祖先链 |
| B4.4 | CloneBranch：当前活动分支整条链复制到新 Session |
| B4.5 | BranchSummary：切分支时按共同祖先→离开 Leaf 路径生成摘要（复用 B2.2 模板），挂新分支根 |
| B4.6 | 忙保护：Run 活跃时禁 Navigate/Fork/Clone/Compact |

**前端**

| 项 | 内容 |
|---|---|
| F4.1 | Session Tree 面板：父子缩进树、当前 Leaf 高亮、点击选点继续、Fork/Clone/Label 菜单、分支摘要提示 |
| F4.2 | Label：Entry 加/删标签，Tree 过滤（默认/隐藏 Tool/仅用户/仅标签/全部） |

### W5 — 后端 Provider Web 配置 / 前端 Provider 设置页 + 模型选择器

**后端**

| 项 | 内容 |
|---|---|
| B5.1 | API：`GET /api/v1/providers`（列表+状态）、`PUT /api/v1/providers/{kind}`（endpoint/default_model/api_key→keyring）、`DELETE`、`POST .../test`（连接测试：列模型+最小请求，返回延迟/错误） |
| B5.2 | 协议族 Adapter：OpenAI-Compatible + Anthropic + Gemini 三族（OAuth/订阅列 P2） |

**前端**

| 项 | 内容 |
|---|---|
| F5.1 | Provider 设置页：Provider 卡片（状态点/endpoint/默认模型/测试连接/编辑凭据）；凭据永不明文回显 |
| F5.2 | 模型选择器：下拉带搜索 + 能力徽章（🧠/👁/🔧/窗口大小）+ Thinking Level 选择器 |

### W6 — 后端工具组 / 前端首页+诊断+收尾验收

**后端**

| 项 | 内容 |
|---|---|
| B6.1 | read/edit/ls/find/grep 五工具（Pi 语义：四层文本 description/promptSnippet/promptGuidelines/输出即 continuation 指令；统一三层长度限制）；edit 承担结构化修改，write 保留 create-only |
| B6.2 | git 工具：status/diff/log/branch/commit（commit 必填信息不静默提交；push/pull 高风险审批 SHOULD 后置） |
| B6.3 | 并发 Run 上限：worker claim 信号量，默认 4 可配 |

**前端**

| 项 | 内容 |
|---|---|
| F6.1 | Changes 面板 + Monaco Diff viewer（三基线切换：Run 开始/Git HEAD/工作区；Hunk 级恢复 SHOULD） |
| F6.2 | 首页：最近项目/Thread/运行中 Run/待审批/成本摘要聚合 |
| F6.3 | 诊断页：健康/日志/备份入口 |
| F6.4 | **全量验收**：Go 测试 + web lint/tsc/vitest/build + Playwright E2E + 真实浏览器对标 Pi 工作流 |

---

## 4. P1（W6 后，约 2 周）

| 项 | 内容 |
|---|---|
| 项目 Trust 三级 | `project_trust` 表 + 资源加载前置校验 + 首次打开 Trust 对话框（挂在 B1.3 ExecutionPolicy 端口上） |
| Next Turn 队列 | queue.kind 加 `next_turn`，下一次用户请求前注入 |
| 终端 | xterm.js + 受控 Shell 会话 |
| 图片输入 | 附件上传 + 预览 + Provider vision 校验 |
| @ 引用 | 文件/符号/Diff/Artifact 模糊搜索注入 |

## 5. P2（约 2 周）

| 项 | 内容 |
|---|---|
| Packages | Local/Git 资源包 + Manifest + 固定 Ref |
| Extension 进程 | stdio JSON-RPC 子进程注册 Tool/Command/Hook（复用 MCP 框架） |
| Pi JSONL 导入导出 | v1/v2/v3 + Compaction 兼容 + HTML 报告 |
| 首次启动向导 | 环境检测 + Provider 配置引导 |
| OAuth/订阅 Provider | ChatGPT Plus/Claude Pro 等（Pi auth/resolve 语义） |

---

## 6. 保留自有（不削弱）

| 能力 | 理由 |
|---|---|
| steering 事务化重规划（取消未完步骤+原子 replan+checkpoint 后生效） | Pi 没有，独有强语义 |
| 持久化 step 循环（durable/可恢复/可审计） | Pi 内存循环崩溃即丢 |
| 七层审批（L0-L3/ApprovalScope/参数 sha256 冻结/会话规则/24h TTL/手动 Full Access/幂等回放） | 比 Pi 单层 Trust 细得多 |
| Run 级模型快照（创建时冻结） | 比 Pi 会话级切换更可审计，符合 F-MDL-012 |
| OS keyring 凭据 | 比 Pi 可插拔 CredentialStore 更安全；仅抽象接口便于测试 |
| 静态拒绝 shell 解释器与网络程序 | 独有 |
| 团队能力代码 | 保留降级，第二阶段打开开关 |

---

## 7. 验收标准

1. **承接性**：W1 后 Repository 拆为 6 个领域接口，Principal/Policy/Artifact 端口就位；后续功能不新增跨域调用。
2. **对标 Pi**：同一多轮 coding 任务，上下文细节完整度、压缩摘要结构、工具清单、Thinking 选择、Diff 展示不弱于 Pi 冻结基线。
3. **前端**：project-page 拆解完成，路由覆盖 5.1 信息架构全部一级页面，设计 token 覆盖全部颜色/间距。
4. **恢复**：重启后 Session Tree、队列、Run 轨迹完整恢复。
5. **全量**：Go 测试 + web lint/tsc/vitest/build + Playwright E2E + 真实浏览器验收全绿。
