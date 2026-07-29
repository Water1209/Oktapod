# Oktapod V1 总体架构与产品优化方案（总架构师审定版）

> **文档版本**：V2.0
> **文档类型**：架构决策 / 实施输入 / AI Coding 执行总纲
> **编制日期**：2026-07-29
> **基线**：GPT-5.6《Oktapod V1 总体架构与产品优化方案 V1.0》、`Doc/research/pi-vs-oktapod-tools-security.md`、`Doc/research/pi-vs-oktapod-agent-loop.md`、`Oktapod_第一阶段补全改造方案_V1.0.md`（V2.0 内核先行修订版）
> **配套执行文件**：`Oktapod_V1_执行计划与验收门禁_V1.0.md`（任务卡、验收门禁、实施顺序）
> **输入代码基线**：`Src/`（Go）+ `Src/web/`（React/TS），当前 git 分支 `main`，最近提交 `d1f5c93 feat: establish Oktapod MVP agent runtime`

---

## 0. 审定结论（先说结论）

GPT-5.6 方案**方向正确、收敛果断、范围控制得当**，可作为 V1 战略基线。但直接按其执行存在**三类结构性风险**，必须在动工前修正：

1. **顺序风险**：把"Pure Agent Loop / Durable Harness 拆分"放在阶段 B，把"Docker/CI/Grant"放在阶段 A。这意味着在 Worker 仍是"模型循环 + 租约 + 队列 + Prompt + Skill + MCP + 审批 + 重试 + Team 编排"的巨型耦合体时，先补安全门禁和 CI。结果是：门禁建在沙地上，阶段 B 一拆，门禁和测试大面积返工。**正确顺序是"内核端口先行，安全门禁与 CI 伴随端口落地，功能最后长"**——与《第一阶段补全改造方案 V2.0》一致。

2. **过度工程风险**：V1 引入完整 ToolSpec / Capability Ontology / ExecutionGrant 签名体系 / 沙箱网络策略 / 多阶段 CI 门禁，范围仍大于"一个可发布的 Coding Agent"。V1 必须再砍一刀：**只保留能支撑"读-改-验-Diff-恢复"主链路的最小安全与扩展能力**，其余标记为 V1.1+。

3. **落地风险**：方案未区分"必须新写"与"直接采用 Pi 已验证语义"。Oktapod 当前缺失 `read/edit/ls/find/grep` 等基础工具（见 `pi-vs-oktapod-tools-security.md` 第 15-25 行），这些工具的语义、截断策略、Continuation 提示、错误恢复引导，Pi 已冻结验证。**不应重新设计，应直接移植 Pi 语义，再用 Oktapod 的 Receipt/Grant 体系包裹**。

**修正后的 V1 总原则**：

> **内核端口先行 → 安全门禁与 CI 伴随端口 → 原生工具对齐 Pi → MCP/Skill 通用化 → 沙箱与体验闭环。**
> 每一步无行为变化、全量测试绿、可独立验收。

---

## 1. V1 范围再收敛（在 GPT-5.6 基础上再砍 30%）

### 1.1 V1 唯一交付目标

用户只说一句开发目标，Agent 能：

```text
读项目 → 改代码 → 跑验证 → 展示 Diff → 接受/撤销/提交 → 异常后可恢复
```

其他一切能力（Team、Schedule、Memory、Advisor、能力市场、多 Agent、浏览器自动化、复杂计划 UI）**代码可保留，入口全部隐藏，不投入新工时**。

### 1.2 V1 必须交付（终版清单）

| 域 | 交付项 | 说明 |
|---|---|---|
| **Agent 核心** | 单 Agent 主循环；ToolBatch 并行；Steering；上下文压缩；Run 持久化与恢复 | 从 Worker 拆出 Pure Agent Loop + Durable Harness |
| **原生工具** | `read` / `edit` / `write` / `ls` / `find` / `grep` / `git_status` / `git_diff` / `git_restore` / `git_commit` / `run_command` / `update_plan` | **语义直接采用 Pi**；用 Oktapod Receipt/Grant 包裹 |
| **MCP** | Registry（STDIO + Streamable HTTP）；`initialize`/`tools/list`/`tools/call`；Schema Hash 快照；健康状态 | 搜索等全部走 MCP，删除内建 `web_search` |
| **Skill** | 严格 `allowed-tools` + 新增 `required-capabilities` / `preferred-tools` / `fallback-policy`（仅 `strict` / `same-capability`） | 见第 6 章 |
| **安全** | ExecutionGrant（HMAC 签名，非零信任）；项目级命令沙箱（最小：只挂载 Workspace、隔离 HOME、禁宿主凭据、非 root、进程树终止）；Git 隔离；External Content Envelope | **V1 不做网络默认拒绝的完整沙箱**，仅做"命令执行不继承宿主凭据 + 工作区隔离" |
| **体验** | 单一 Composer；实时 stdout/stderr；审批卡；Diff 查看/接受/撤销；Inspector 默认收起 | 高级配置全部移入项目设置 |
| **工程** | Docker 多阶段构建（含前端）；`go test` / `go vet` / `go test -race` / 前端 lint+test+build / Docker smoke / OpenAPI drift / SQLite migration check | **V1 不做 SBOM、依赖扫描、完整 E2E、PostgreSQL migration、Playwright 全链路**，这些进 V1.1 |

### 1.3 V1 明确不交付（比 GPT-5.6 更严格）

- 网络级零信任沙箱（默认拒绝网络、受控授权）；
- 完整 Tool Ontology 与跨 Provider 参数转换；
- MCP 自动安装 / 自动升级；
- 能力市场；
- 多 Agent / Team 编排 UI；
- 长期后台自治任务；
- 浏览器完整自动化；
- 每次工具替代都询问用户；
- SBOM / 依赖扫描 / 完整安全测试矩阵；
- PostgreSQL 迁移门禁（V1 仅 SQLite，代码保留 PG 适配）。

> **判断依据**：V1 是"个人开发者/小团队本地优先 Coding Agent"。网络零信任、企业级 SBOM、多 Agent 协作不是首版付费/留存理由。先让"读-改-验-恢复"在本地跑稳，再谈企业能力。

---

## 2. 架构原则修正（三条新增，两条强化）

### 2.1 新增：内核端口先行，功能后长

所有功能（工具、MCP、Skill、Grant、沙箱）必须挂在**已拆分的窄端口**上，不允许继续向 `application/repository.go` 105 方法巨型接口或 `runtime/worker.go` 添方法。

V1 动工第一周必须先完成：

- `Repository` 按领域拆 6 接口（Session / Project / Task / Run / Resource / Usage）；
- `PrincipalResolver` + `Authorizer` 端口；
- `ExecutionPolicy` 端口（审批、路径、网络、预算统一入口）；
- `ArtifactStore` 端口；
- `PureAgentLoop` 接口与 `DurableHarness` 接口分离。

**无行为变化，全量测试绿，方可进入下一阶段。**

### 2.2 新增：Pi 语义直接采用，Oktapod 只加安全与持久化

凡 Pi 已冻结验证的语义（read 截断策略、edit 唯一匹配约束、bash 流式累积与整树终止、grep/find/ls 参数与 `.gitignore` 尊重、Continuation 提示格式），**直接移植，不重新设计**。Oktapod 的增量价值只在：

- 每次工具调用生成 Receipt（输入 Hash、输出 Hash、副作用、耗时）；
- 需要审批的工具走 ExecutionGrant；
- 状态持久化到 SQLite，支持崩溃恢复。

### 2.3 新增：安全门禁与 CI 伴随端口落地，不滞后

不是"先拆完再补安全"，而是**每拆一个端口，同步落地该端口的安全校验与 CI 检查**。例如拆 `ExecutionPolicy` 端口时，同步实现 ExecutionGrant 签发/验证，并把"无 Grant 拒绝执行"加入 `go test` 单元测试。

### 2.4 强化：极简核心，能力外置（GPT-5.6 正确，保留）

核心只负责 Agent Loop、状态、工具协议、安全、恢复、审计。搜索、网页抓取、GitHub、数据库、浏览器等全部走 MCP。删除内建 `web_search`，不建设 `WebSearchAdapter` / `SearchProviderRouter` / `CanonicalWebSearch`。

### 2.5 强化：系统限定边界，模型发挥智能（GPT-5.6 正确，保留）

系统决定模型可见/可调用的工具集合；模型决定当前是否需要工具、用哪个。模型不得自行安装 MCP、放宽权限、改变风险等级、绕过 Skill 约束、启用未审核工具、将未知工具归类为低风险、静默切换数据 Provider。

---

## 3. V1 总体架构（修正后分层）

```text
┌─────────────────────────────────────────────┐
│           Web / CLI / IDE (UI)              │
│  单一 Composer / 实时输出 / 审批 / Diff      │
└─────────────────────┬───────────────────────┘
                      │
┌─────────────────────▼───────────────────────┐
│         Interaction Controller              │
│  会话展示 / 流式事件 / Steering / 取消       │
└─────────────────────┬───────────────────────┘
                      │
┌─────────────────────▼───────────────────────┐
│          Application Commands               │
│  SubmitTurn / AbortRun / Approve /          │
│  AcceptChanges / RestoreChanges / Commit    │
└─────────────────────┬───────────────────────┘
                      │
┌─────────────────────▼───────────────────────┐
│            Durable Harness                  │
│  Run / Lease / Checkpoint / Budget /        │
│  Receipt / Recovery / ToolBatch 持久化       │
└─────────────────────┬───────────────────────┘
                      │
┌─────────────────────▼───────────────────────┐
│            Pure Agent Loop                  │
│  Model → ToolBatch → Observation → Model    │
│  事件: ModelStarted/TextDelta/ToolBatchProposed │
└─────────────┬─────────────────┬─────────────┘
              │                 │
┌─────────────▼─────────┐ ┌─────▼─────────────┐
│ Prompt & Context      │ │ Tool Context      │
│ Planner               │ │ Planner           │
│ (Bundle / Hash /      │ │ (Skill /          │
│  压缩策略)             │ │  Capability 过滤) │
└─────────────┬─────────┘ └─────┬─────────────┘
              │                 │
              └────────┬────────┘
                       │
┌──────────────────────▼──────────────────────┐
│         Unified Tool Registry               │
│  Native Tools + MCP Tools + ToolSpec        │
└──────────────────────┬──────────────────────┘
                       │
┌──────────────────────▼──────────────────────┐
│   ExecutionPolicy → ExecutionGrant → Broker │
└──────────────┬─────────────────┬────────────┘
               │                 │
       ┌───────▼──────┐  ┌───────▼──────────┐
       │ Native Tool  │  │ Generic MCP      │
       │ Executors    │  │ Client (tools/call)│
       └───────┬──────┘  └───────┬──────────┘
               │                 │
┌──────────────▼─────────────────▼─────────────┐
│              Project Sandbox                │
│  Filesystem / Process / Resources           │
│  (V1: 隔离 HOME + 凭据 + 工作区 + 进程树)    │
└──────────────────────────────────────────────┘
```

**关键修正**：`ExecutionPolicy` 不再是散在 Worker 里的判断，而是统一端口；`Prompt & Context Planner` 与 `Tool Context Planner` 分离，前者管 Prompt Bundle 与压缩，后者管工具候选过滤与排序。

---

## 4. 核心模块设计（V1 终版）

### 4.1 Pure Agent Loop

职责仅限：

```text
接收 ModelMessage → 调用模型 → 解析 ToolCall → 生成 ToolBatch
→ 接收 ToolResult → 生成下一轮消息 → 判断继续/结束
```

接口：

```go
type AgentLoop interface {
    Next(ctx context.Context, state LoopState) (LoopEvent, error)
}
```

事件：

```text
ModelStarted / TextDelta / ThinkingDelta /
ToolBatchProposed / ToolResultObserved /
TurnCompleted / TurnFailed / ContextLimitReached
```

**不依赖**：DB、HTTP、用户 Session、SQLite、MCP Transport、具体工具实现、审批存储、UI。

### 4.2 Durable Harness

职责：Run 生命周期、Lease、Save Point、Checkpoint、ToolBatch 持久化、预算、恢复、Steering、Follow-up、Exactly-once Receipt、Worker 重启恢复。

**关键约束**：Pure Agent Loop 只产生事件，Harness 决定如何持久化。Loop 不知道 SQLite 存在。

### 4.3 ExecutionPolicy 端口（V1 新增，替代散点审批）

```go
type ExecutionPolicy interface {
    Evaluate(ctx context.Context, req ExecutionRequest) (Decision, error)
    // Decision: Allow | Ask | Deny
    // 包含: 风险等级、所需 Grant 类型、审批规则、预算影响
}
```

所有工具执行前必须经过 `ExecutionPolicy.Evaluate`。`Ask` 工具必须持有有效 `ExecutionGrant` 才能执行，否则 fail-closed。

### 4.4 ExecutionGrant（V1 简化版）

V1 采用 **HMAC-SHA256 签名**（非零信任 PKI），字段：

```text
grant_id / run_id / step_id / tool_call_id /
tool_identity / input_hash / scope / risk /
approval_rule / expires_at / policy_version / signature
```

Executor 验证：存在性、签名、Input Hash、Tool 一致、未过期、作用域匹配。**V1 不做跨进程零信任验证**，只做本进程内不可伪造授权证明。

### 4.5 Project Sandbox（V1 最小可行）

V1 不做完整容器/网络沙箱，只做：

- 命令执行只挂载当前 Workspace；
- 临时目录独立；
- 默认禁止读取宿主 `HOME`（除显式白名单）；
- 默认禁止继承 SSH / AWS / Git 凭据；
- 非 root 运行；
- 进程树终止；
- 资源限制（CPU / 内存 / 超时）。

宿主直接执行仅作为开发模式，UI 明确标注："宿主受控执行，非沙箱"。

**V1.1 再补**：网络默认拒绝、受控网络授权、完整容器沙箱。

---

## 5. 统一工具体系（V1 终版）

### 5.1 原生工具（直接采用 Pi 语义）

```text
read / edit / write / ls / find / grep /
git_status / git_diff / git_restore / git_commit /
run_command / update_plan
```

**移植要求**：

- `read`：2000 行 / 50KB `truncateHead`；offset/limit Continuation 提示；首行超字节建议改用 `run_command: sed -n ... | head -c ...`；图片走附件通道。
- `edit`：`edits[{oldText,newText}]`；oldText 唯一不重叠；BOM 剥离、CRLF/LF 归一化；返回 unified patch + firstChangedLine；走文件变更队列防并发。
- `run_command`：stdout/stderr 流式累积；2000 行 / 50KB `truncateTail`；超长自动落 temp 文件并返回 `Full output: <path>`；timeout 整树终止；env 剥离 `OKTAPOD_*` 再注入。
- `grep`：走 `rg --json`；100 匹配上限；行 500 字符截断；`.gitignore` 尊重。
- `find`：走 `fd --glob`；默认 1000 条；`.gitignore` 尊重。
- `ls`：默认 500 条；目录带 `/` 后缀。

**Oktapod 增量**：每次调用生成 Receipt；`write`/`edit`/`git_commit`/`run_command` 需 ExecutionGrant。

### 5.2 MCP 工具

以下全部走 MCP：互联网搜索、网页提取、GitHub 远程数据、Jira/Confluence、数据库、浏览器、企业内部系统、知识库、第三方研究工具。

V1 验收插件：**Tavily MCP**（`tavily_search` / `tavily_extract`）。Tavily 只是可选 MCP Server，不进入核心依赖。

### 5.3 单一 ToolSpec（V1 简化版）

V1 不建设完整 Tool Ontology，ToolSpec 仅包含：

```text
Name / Description / InputSchema / OutputSchema /
Source: native | mcp / ServerID / Capabilities /
ReadOnly / Destructive / OpenWorld / Idempotent /
MaxOutput / Executor / Validator / Renderer
```

`RiskClassifier` / `Sequential` / 复杂策略进 V1.1。

---

## 6. Skill 与智能工具替代（V1 终版）

### 6.1 保留严格工具语义

```yaml
allowed-tools:
  - mcp:tavily/tavily_search
```

含义不变：只能调用该工具，不能自动替换。

### 6.2 新增能力需求（V1 仅支持两种策略）

```yaml
required-capabilities:
  - web.search

preferred-tools:
  - mcp:tavily/tavily_search

fallback-policy: same-capability   # 或 strict
```

**V1 只支持**：

- `strict`：首选不可用 → 阻塞 → 提示用户配置；
- `same-capability`：首选不可用 → 系统过滤同能力候选（权限/风险/健康/快照/Schema 已审核）→ 向模型暴露 1~3 个候选 → 模型选择。

**V1 不支持**：每次询问用户、跨能力替代、自动跨 Provider 参数转换。

### 6.3 V1 最小能力分类

```text
web.search / web.extract / repository.search / repository.read
```

其余能力标签进 V1.1。能力标签来源仅接受：Oktapod 内置可信目录、MCP Manifest 明确声明、管理员人工确认。模型可建议，不能自动批准。

---

## 7. 工具失败与智能恢复（V1 终版）

### 7.1 不进行后台静默替换

错误：`tavily_search` 失败 → 系统偷偷调另一家。
正确：`tavily_search` 返回失败 Receipt → Agent 收到失败观察 → Tool Context Planner 补充同能力候选 → 下一轮模型选择另一个工具。

### 7.2 错误分类与处理

| 类型 | 判定 | V1 处理 |
|---|---|---|
| Transport Failure | 连接失败/进程退出/超时/5xx | 允许同能力候选 |
| Authentication Failure | 401/403/凭据缺失/OAuth 失效 | 移出候选，Inspector 显示配置错误 |
| Rate Limit | 429/额度耗尽 | 允许同能力候选，必须显示 Provider 限流 |
| Empty Result | 结果为空 | 模型决定：改写查询/放宽条件/换工具/说明无结果 |
| Invalid Input | 参数校验失败 | 返回模型重新构造，不允许系统跨 Schema 静默转换 |

---

## 8. 数据与代码模块拆分（V1 终版）

### 8.1 Store 拆分（第一周必须完成）

```text
IdentityStore / ProjectStore / ConversationStore /
RunStore / ToolLedger / ApprovalStore /
CapabilityStore / MCPStore / UsageStore / ArtifactStore
```

跨领域事务通过 `UnitOfWork` / `Transaction Script` 显式组合。当前 `application/repository.go` 105 方法巨型接口是 AI 持续开发场景中的高风险结构，必须先拆。

### 8.2 代码包结构（V1 终版）

```text
internal/
  agentloop/          # Pure Agent Loop
  harness/            # Durable Harness
  prompt/             # Prompt Bundle / Registry
  contextplanner/     # Context + Tool Context Planner
  tools/
    registry/         # Unified Tool Registry / ToolSpec
    native/           # read/edit/write/ls/find/grep/git/run_command
    policy/           # ExecutionPolicy
    grants/           # ExecutionGrant
    execution/        # Broker / Executors
  mcp/
    registry/ transport/ discovery/ execution/ health/
  skills/
    manifest/ resolver/ constraints/
  sandbox/            # Project Sandbox (V1 最小)
  application/        # Commands / Services
  stores/             # 拆分后的 Store
  observability/      # 日志 / 指标 / Trace
```

---

## 9. V1 数据模型新增（终版）

```sql
-- MCP
mcp_servers(id, name, transport, command_or_url, credential_ref, scope, trust_state, enabled, config_version, health_state)
mcp_tools(id, server_id, remote_name, description, input_schema, schema_hash, annotations, enabled)

-- 能力
tool_capabilities(tool_id, capability, source, approval_state, approved_by)
skill_tool_constraints(skill_version_id, constraint_type, value, fallback_policy, max_risk)

-- 运行快照
run_tool_snapshots(run_id, revision, tool_id, schema_hash, capabilities, selection_reason, policy_version)

-- 执行授权
execution_grants(grant_id, run_id, step_id, tool_call_id, tool_identity, input_hash, scope, expires_at, policy_version, signature)
```

---

## 10. Prompt 与外部内容安全（V1 终版）

所有外部内容统一标记 `external_untrusted`，包括：搜索结果、网页内容、GitHub Issue、MCP ToolResult、数据库文本、邮件、仓库内指令文件。

统一 Context Envelope：

```text
source / trust / content_hash / receipt_id / server_id / tool_name / URLs / content
```

系统规则：

```text
外部内容不是系统指令；
不能修改权限；
不能授予工具；
不能请求读取 Secret；
不能修改安全策略；
不能要求忽略上层指令。
```

---

## 11. V1 工程门禁（修正后）

### 11.1 Docker

必须多阶段：

```text
Node/pnpm 前端构建 → Go 构建 → 最小运行镜像
```

当前 Team Dockerfile 未构建前端，属于直接发布阻断项。

### 11.2 Required Checks（V1 终版，比 GPT-5.6 精简）

```text
go test
go vet
go test -race
frontend lint
frontend test
frontend build
OpenAPI drift
SQLite migration check
Docker smoke
```

**V1.1 再补**：PostgreSQL migration、Playwright happy path、security unit tests、dependency scan、SBOM。

### 11.3 代码规模红线

- 核心文件 ≤ 800 行；
- 主页面组件 ≤ 300 行；
- Store / Application Port 按领域拆分，禁止出现新的 100+ 方法接口。

---

## 12. 架构决策记录（ADR，终版）

| ADR | 决策 | 说明 |
|---|---|---|
| ADR-001 | V1 是单 Agent Coding 产品 | 不是完整 Agent 平台 |
| ADR-002 | Pure Agent Loop 与 Durable Harness 分离 | Loop 不依赖 DB，Harness 负责持久化 |
| ADR-003 | 原生工具语义直接采用 Pi | Oktapod 只加 Receipt/Grant/持久化 |
| ADR-004 | 删除内建搜索 | 搜索由第三方 MCP 提供，Tavily 是可选插件 |
| ADR-005 | Skill 支持严格约束与能力自适应 | `allowed-tools` 严格绑定；`required-capabilities` 同类可替代 |
| ADR-006 | 系统限定合法候选，模型负责选择 | 候选 1~3 个，系统过滤，模型决策 |
| ADR-007 | 工具替代过程可见 | 不静默切换 Provider |
| ADR-008 | 副作用执行必须持 ExecutionGrant | Ask 工具无 Grant 100% 拒绝 |
| ADR-009 | 单一 Composer | 高级配置进入项目设置和 Inspector |
| ADR-010 | Docker/CI/恢复测试是发布前置 | V1 精简门禁，V1.1 补完整矩阵 |
| ADR-011 | **内核端口先行** | 先拆 Repository/Principal/Policy/Artifact，再长功能 |
| ADR-012 | **V1 最小沙箱** | 隔离 HOME/凭据/工作区/进程树，网络沙箱进 V1.1 |

---

## 13. 与 GPT-5.6 方案的关键差异总结

| 维度 | GPT-5.6 V1.0 | 本审定版 V2.0 | 理由 |
|---|---|---|---|
| 实施顺序 | 阶段 A 先补 Docker/CI/Grant，阶段 B 再拆 Loop/Harness | **先拆内核端口，安全/CI 伴随端口落地，功能最后长** | 避免在耦合 Worker 上建门禁，减少返工 |
| 原生工具 | 未明确是否重新设计 | **直接采用 Pi 语义**，Oktapod 只加安全与持久化 | Pi 已冻结验证，重新设计浪费且风险高 |
| 沙箱范围 | 项目级沙箱 + 网络默认拒绝 | **V1 最小沙箱**（隔离 HOME/凭据/工作区/进程树），网络沙箱进 V1.1 | V1 是本地优先产品，网络零信任非首版必需 |
| CI 门禁 | 13 项全量 | **8 项精简**，SBOM/依赖扫描/完整 E2E/PG migration 进 V1.1 | 先保证主链路稳定，再补企业级门禁 |
| ToolSpec | 完整 Ontology | **V1 简化版**，复杂分类进 V1.1 | 避免过度工程 |
| 执行授权 | 零信任签名 | **HMAC 简化版**，跨进程零信任进 V1.1 | 满足不可伪造即可，不过度设计 |

---

## 14. V1 最终目标（不变，强化验收）

Oktapod V1 不追求功能数量，追求：

```text
极简：用户只描述目标
智能：模型根据当前合法工具自主行动
可控：系统限定工具、权限、风险、作用域
可靠：Run、Checkpoint、Receipt 支持恢复和防重
可扩展：Skill 和 MCP 增加能力，不修改 Agent Core
```

**V1 成功的标志**：用户可以只说一句开发目标，Agent 能安全地理解项目、选择合法工具、完成修改、运行验证、展示证据，并在任何异常后可靠恢复。

---

**下一步**：阅读配套执行文件 `Oktapod_V1_执行计划与验收门禁_V1.0.md`，获取按周划分的任务卡、验收门禁与 AI Coding 执行指令。
