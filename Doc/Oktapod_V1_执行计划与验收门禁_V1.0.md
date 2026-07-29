# Oktapod V1 执行计划与验收门禁（AI Coding 执行总纲）

> **文档版本**：V1.0
> **文档类型**：AI Coding 执行指令 / 任务卡 / 验收门禁
> **编制日期**：2026-07-29
> **架构基线**：`Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md`
> **执行工具**：Claude Code / Cursor / 其他 AI Coding Agent
> **代码基线**：`E:\git\Oktapod\Src`（Go 后端）+ `E:\git\Oktapod\Src\web`（React/TS 前端）

---

## 0. 给 AI Coding Agent 的总指令

你正在参与 Oktapod V1 改造。这是一个**先拆内核、再长功能**的重构项目，不是新功能开发。你必须遵守：

1. **先读架构文档**：动手前必须读完 `Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md`。
2. **先拆端口，再写功能**：所有新代码必须挂在已拆分的窄端口上。禁止向 `application/repository.go` 或 `runtime/worker.go` 添加新方法。
3. **无行为变化重构**：每个任务卡的第一步都是纯重构，全量测试必须绿。重构完成前，禁止添加新功能。
4. **小步提交**：每个任务卡完成后立即提交，commit message 用英文，格式：`refactor: <scope> - <what>` 或 `feat: <scope> - <what>`。
5. **验证优先**：每完成一个子任务，必须运行对应验证命令（见任务卡"验证"列）。不验证不继续。
6. **Pi 语义直接采用**：实现 `read/edit/ls/find/grep/run_command` 时，直接参考 `E:\git\pi\packages\coding-agent\src\core\tools\` 的语义，不要重新设计截断、Continuation、错误恢复逻辑。
7. **不自动 push**：只有用户明确要求时才执行 `git push`。

---

## 1. 总体实施顺序（6 周）

```text
W1: 内核端口拆分（纯重构，无行为变化）
W2: 安全门禁与 CI 伴随端口落地 + Docker 修复
W3: 原生工具对齐 Pi（read/edit/write/ls/find/grep/git）
W4: Pure Agent Loop 与 Durable Harness 分离
W5: MCP 通用化 + Skill 能力扩展 + 删除内建搜索
W6: 体验闭环（Composer/实时输出/Diff/Inspector）+ 发布门禁
```

**关键约束**：W1 未完成前，禁止开始 W2-W6 的任何功能开发。

---

## 2. W1：内核端口拆分（纯重构）

### 目标

把 105 方法巨型 `Repository` 拆为 6 个领域接口；把散在 Worker 里的审批/路径/网络判断收敛为 `ExecutionPolicy` 端口；把身份模型从具体 `Scope` 改为 `PrincipalResolver` 端口；把文件系统写入改为 `ArtifactStore` 端口。**全程无行为变化。**

### 任务卡

| ID | 任务 | 具体动作 | 涉及文件 | 验证 | 预估 |
|---|---|---|---|---|---|
| W1-B1 | 拆 SessionRepository | 从 `application/repository.go` 提取 Session 相关方法为 `SessionRepository`；在 `stores/` 新建 `session_store.go`；调用方改为依赖窄接口 | `Src/internal/application/repository.go`、`Src/internal/stores/session_store.go`（新建） | `go build ./... && go test ./...` 绿 | 4h |
| W1-B2 | 拆 ProjectRepository | 同上，提取 Project 相关方法 | 同上 | 同上 | 3h |
| W1-B3 | 拆 TaskRepository | 同上，提取 Task/Run/Step/Checkpoint 相关方法 | 同上 | 同上 | 4h |
| W1-B4 | 拆 RunRepository | 同上，提取 RunEvent/Usage/Approval/SideEffect/Artifact 相关方法 | 同上 | 同上 | 4h |
| W1-B5 | 拆 ResourceRepository | 同上，提取 Capability/Memory/Agent/Team/MCP/Schedule 相关方法 | 同上 | 同上 | 3h |
| W1-B6 | 拆 UsageRepository | 同上，提取 UsageLedger/ModelCall 相关方法 | 同上 | 同上 | 2h |
| W1-B7 | 定义 PrincipalResolver 端口 | 新建 `internal/application/ports.go`：`PrincipalResolver.Resolve(ctx, RequestIdentity) (Principal, error)`；实现 `LocalPrincipalResolver` 返回固定 `local/local-user`；替换 `service.go` 中 `ScopeFor` 直传 | `Src/internal/application/ports.go`（新建）、`Src/internal/application/service.go` | `go build ./... && go test ./...` 绿 | 3h |
| W1-B8 | 定义 ExecutionPolicy 端口 | 新建 `internal/tools/policy/execution_policy.go`：`ExecutionPolicy.Evaluate(ctx, ExecutionRequest) (Decision, error)`；把 Worker 中审批/路径/网络判断迁移为实现 | `Src/internal/tools/policy/execution_policy.go`（新建）、`Src/internal/runtime/worker.go` | `go build ./... && go test ./...` 绿 | 6h |
| W1-B9 | 定义 ArtifactStore 端口 | 新建 `internal/stores/artifact_store.go`：`ArtifactStore.Put/Get/Delete`；实现本地 FS 版本；替换直接写文件系统的代码 | `Src/internal/stores/artifact_store.go`（新建）、`Src/internal/adapters/workspace/local.go` | `go build ./... && go test ./...` 绿 | 4h |
| W1-B10 | 定义 PureAgentLoop 接口 | 新建 `internal/agentloop/loop.go`：`AgentLoop.Next(ctx, LoopState) (LoopEvent, error)`；先做空实现/适配器，不迁移逻辑 | `Src/internal/agentloop/loop.go`（新建） | `go build ./...` 绿 | 2h |
| W1-B11 | 定义 DurableHarness 接口 | 新建 `internal/harness/harness.go`：`Harness.Start/Stop/Checkpoint/Recover`；先做空实现/适配器 | `Src/internal/harness/harness.go`（新建） | `go build ./...` 绿 | 2h |

### W1 验收门禁（必须全部通过才能进入 W2）

```bash
cd Src
go build ./...
go test ./...
go vet ./...
go test -race ./...
```

- [ ] 所有测试通过，无行为变化；
- [ ] `application/repository.go` 方法数 ≤ 20（只保留聚合）；
- [ ] `runtime/worker.go` 行数减少 ≥ 30%；
- [ ] 新增端口接口均有默认空实现或适配器；
- [ ] 无新增外部依赖。

---

## 3. W2：安全门禁与 CI 伴随落地 + Docker 修复

### 目标

在已拆分的端口上实现 ExecutionGrant、最小沙箱、Git 隔离；修复 Docker 前端构建；建立精简 CI 门禁。

### 任务卡

| ID | 任务 | 具体动作 | 涉及文件 | 验证 | 预估 |
|---|---|---|---|---|---|
| W2-S1 | 实现 ExecutionGrant | 新建 `internal/tools/grants/grant.go`：HMAC-SHA256 签名、签发、验证；`ExecutionPolicy` 在 `Ask` 决策时生成 Grant；Executor 执行前验证 Grant | `Src/internal/tools/grants/grant.go`（新建）、`Src/internal/tools/policy/execution_policy.go` | 单元测试：无 Grant 时 `Ask` 工具 100% 拒绝；签名篡改拒绝；过期拒绝 | 6h |
| W2-S2 | 实现最小 Project Sandbox | 新建 `internal/sandbox/sandbox.go`：命令执行只挂载 Workspace、隔离 HOME、禁 SSH/AWS/Git 凭据、非 root、进程树终止、资源限制 | `Src/internal/sandbox/sandbox.go`（新建）、`Src/internal/adapters/workspace/local.go` | 单元测试：命令无法读取宿主 `~/.ssh`；进程树终止生效 | 8h |
| W2-S3 | Git 隔离 | Git 命令设置 `GIT_CONFIG_NOSYSTEM=1`、`GIT_TERMINAL_PROMPT=0`、禁用 credential helper、隔离 HOME、空 hooksPath | `Src/internal/adapters/workspace/local.go` | 单元测试：Git 不读取宿主 `.gitconfig` | 3h |
| W2-S4 | Docker 多阶段构建 | 修改 `Src/Dockerfile`：Node/pnpm 前端构建 → Go 构建 → 最小运行镜像 | `Src/Dockerfile`、`Src/web/package.json` | `docker build -t oktapod:v1 .` 成功；`docker run` 可访问 Web UI | 4h |
| W2-S5 | 精简 CI 门禁 | 新建 `.github/workflows/ci.yml`（或项目 CI 配置）：`go test`/`go vet`/`go test -race`/`frontend lint`/`frontend test`/`frontend build`/`OpenAPI drift`/`SQLite migration check`/`Docker smoke` | `.github/workflows/ci.yml`（新建或修改） | CI 运行通过；故意引入错误时 CI 失败 | 4h |
| W2-S6 | External Content Envelope | 新建 `internal/contextplanner/envelope.go`：所有外部内容标记 `external_untrusted`，统一 Envelope 结构 | `Src/internal/contextplanner/envelope.go`（新建） | 单元测试：MCP ToolResult 被标记为 `external_untrusted` | 3h |

### W2 验收门禁

```bash
cd Src
go test ./internal/tools/grants/... -v
go test ./internal/sandbox/... -v
docker build -t oktapod:v1 .
# CI 全绿
```

- [ ] `Ask` 工具无 Grant 时 100% 拒绝；
- [ ] 命令执行不继承宿主凭据；
- [ ] Docker 镜像包含完整 Web UI；
- [ ] CI 8 项检查全部通过。

---

## 4. W3：原生工具对齐 Pi

### 目标

实现 `read/edit/write/ls/find/grep/git_status/git_diff/git_restore/git_commit/run_command/update_plan`，语义直接采用 Pi，用 Oktapod Receipt/Grant 包裹。

### 任务卡

| ID | 任务 | 具体动作 | Pi 参考文件 | 验证 | 预估 |
|---|---|---|---|---|---|
| W3-T1 | 实现 `read` | 2000 行/50KB `truncateHead`；offset/limit Continuation；首行超字节提示改用 `run_command`；图片附件通道 | `E:\git\pi\packages\coding-agent\src\core\tools\read.ts` | 单元测试：截断、Continuation、二进制/图片处理 | 6h |
| W3-T2 | 实现 `edit` | `edits[{oldText,newText}]`；唯一不重叠；BOM/CRLF 归一化；返回 unified patch；文件变更队列 | `E:\git\pi\packages\coding-agent\src\core\tools\edit.ts` | 单元测试：唯一匹配、重叠拒绝、patch 生成 | 8h |
| W3-T3 | 实现 `write` | 原子写入；create-only；sha256 重放检测；mkdir -p | `E:\git\pi\packages\coding-agent\src\core\tools\write.ts` + Oktapod 现有 `workspace_write_file` | 单元测试：原子性、重放、已存在拒绝 | 4h |
| W3-T4 | 实现 `ls` | 默认 500 条；目录带 `/`；不区分大小写排序 | `E:\git\pi\packages\coding-agent\src\core\tools\ls.ts` | 单元测试 | 2h |
| W3-T5 | 实现 `find` | `fd --glob`；默认 1000 条；`.gitignore` 尊重；路径含 `/` 时 `**/` 前缀 | `E:\git\pi\packages\coding-agent\src\core\tools\find.ts` | 单元测试 | 3h |
| W3-T6 | 实现 `grep` | `rg --json`；100 匹配上限；行 500 字符截断；`.gitignore` 尊重 | `E:\git\pi\packages\coding-agent\src\core\tools\grep.ts` | 单元测试 | 4h |
| W3-T7 | 实现 `run_command` | 流式累积；2000 行/50KB `truncateTail`；超长落 temp 文件；timeout 整树终止；env 剥离 `OKTAPOD_*` | `E:\git\pi\packages\coding-agent\src\core\tools\bash.ts` | 单元测试：流式、截断、终止 | 8h |
| W3-T8 | 实现 Git 工具组 | `git_status`/`git_diff`/`git_log`/`git_restore`/`git_commit`；全部走 Sandbox；`git_commit` 需 Grant | Oktapod 现有 Git 代码 + Pi 语义 | 单元测试：隔离、Grant 验证 | 6h |
| W3-T9 | 实现 `update_plan` | 仅复杂任务内部使用；简单任务不强制展示 | Oktapod 现有 Plan 代码 | 单元测试 | 2h |
| W3-T10 | 统一 ToolSpec | 新建 `internal/tools/registry/spec.go`：Name/Description/InputSchema/OutputSchema/Source/ServerID/Capabilities/ReadOnly/Destructive/OpenWorld/Idempotent/MaxOutput/Executor/Validator/Renderer | 新建 | 所有原生工具通过 ToolSpec 注册 | 4h |
| W3-T11 | 工具 Receipt 包裹 | 每个工具调用生成 Receipt（输入 Hash、输出 Hash、副作用、耗时）；写入 `ToolLedger` | `Src/internal/stores/tool_ledger.go`（新建） | 单元测试：Receipt 生成与持久化 | 4h |

### W3 验收门禁

```bash
cd Src
go test ./internal/tools/native/... -v
go test ./internal/tools/registry/... -v
```

- [ ] 所有原生工具语义与 Pi 一致；
- [ ] `write/edit/git_commit/run_command` 需 Grant；
- [ ] 每次调用生成 Receipt；
- [ ] 工具错误返回带 Continuation 提示（如 read 截断时返回 `[Showing lines X-Y of Z. Use offset=N to continue.]`）。

---

## 5. W4：Pure Agent Loop 与 Durable Harness 分离

### 目标

把 Worker 中的模型循环、租约、队列、提示词、Skill、MCP、审批、重试、Team 编排拆分为 Pure Agent Loop（无 DB 依赖）+ Durable Harness（负责持久化与恢复）。

### 任务卡

| ID | 任务 | 具体动作 | 涉及文件 | 验证 | 预估 |
|---|---|---|---|---|---|
| W4-L1 | 迁移模型循环到 PureAgentLoop | 把 Worker 中"调用模型 → 解析 ToolCall → 生成 ToolBatch → 接收 ToolResult → 下一轮"逻辑迁移到 `internal/agentloop/loop.go`；Loop 只依赖 `ModelClient` 和 `ToolExecutor` 接口 | `Src/internal/agentloop/loop.go`、`Src/internal/runtime/worker.go` | 单元测试：Loop 可独立运行，不依赖 DB | 10h |
| W4-L2 | 定义 LoopEvent | 实现 `ModelStarted/TextDelta/ThinkingDelta/ToolBatchProposed/ToolResultObserved/TurnCompleted/TurnFailed/ContextLimitReached` | `Src/internal/agentloop/events.go`（新建） | 单元测试：事件序列正确 | 3h |
| W4-L3 | 迁移持久化到 DurableHarness | 把 Run 生命周期、Lease、Checkpoint、ToolBatch 持久化、预算、恢复、Steering 迁移到 `internal/harness/harness.go` | `Src/internal/harness/harness.go`、`Src/internal/runtime/worker.go` | 单元测试：Harness 可独立持久化/恢复 | 10h |
| W4-L4 | Worker 改为适配器 | Worker 只负责从队列取任务、调用 Harness、调用 Loop、上报事件；不再包含业务逻辑 | `Src/internal/runtime/worker.go` | `go test ./...` 绿；Worker 行数减少 ≥ 50% | 6h |
| W4-L5 | 上下文压缩迁移 | 把上下文压缩逻辑迁移到 `internal/prompt/compressor.go`；Loop 触发 `ContextLimitReached` 时由 Harness 调用压缩 | `Src/internal/prompt/compressor.go`（新建） | 单元测试：压缩后上下文可恢复 | 4h |
| W4-L6 | Steering/Follow-up 迁移 | 把 Steering 事务化重规划迁移到 Harness | `Src/internal/harness/steering.go`（新建） | 集成测试：Steering 后 Run 可恢复 | 4h |

### W4 验收门禁

```bash
cd Src
go test ./internal/agentloop/... -v
go test ./internal/harness/... -v
go test -race ./...
```

- [ ] Pure Agent Loop 不导入 `database/sql`、`net/http`、`sqlite`；
- [ ] Harness 可独立恢复任意 Run；
- [ ] Worker 行数 ≤ 800；
- [ ] 任意 Worker kill 后无重复副作用。

---

## 6. W5：MCP 通用化 + Skill 能力扩展 + 删除内建搜索

### 目标

建立通用 MCP Registry/Discovery/Execution；删除内建 `web_search`；Skill 支持 `required-capabilities`/`preferred-tools`/`fallback-policy`。

### 任务卡

| ID | 任务 | 具体动作 | 涉及文件 | 验证 | 预估 |
|---|---|---|---|---|---|
| W5-M1 | MCP Registry | 新建 `internal/mcp/registry/registry.go`：管理 `mcp_servers` 表；支持 STDIO 和 Streamable HTTP | `Src/internal/mcp/registry/registry.go`（新建）、`Src/db/migrations/sqlite/`（新增迁移） | 单元测试：注册/启停/健康检查 | 6h |
| W5-M2 | MCP Discovery | 新建 `internal/mcp/discovery/discovery.go`：连接后执行 `initialize`/`tools/list`；保存 `mcp_tools`；Schema Hash 快照；Schema 变化进入重新审核 | `Src/internal/mcp/discovery/discovery.go`（新建） | 单元测试：Schema Hash 一致；变化触发审核 | 6h |
| W5-M3 | 通用 MCP Execution | 新建 `internal/mcp/execution/executor.go`：统一 `tools/call`；走 ExecutionPolicy/Grant；输出限制；External Content Envelope；Receipt | `Src/internal/mcp/execution/executor.go`（新建） | 单元测试：与原生工具共用 ToolBatch/审批/收据/恢复 | 8h |
| W5-M4 | 删除内建 web_search | 从 `domain/core/types.go` 删除 `ToolWebSearch`；从 Worker 删除 `executeWebSearchTool`；从 `openaicompat/client.go` 删除 `web_search` schema | `Src/internal/domain/core/types.go`、`Src/internal/runtime/worker.go`、`Src/internal/adapters/model/openaicompat/client.go` | `go build ./... && go test ./...` 绿；无 `web_search` 引用 | 3h |
| W5-M5 | 接入 Tavily MCP | 提供 Tavily MCP Server 配置示例；验证 `tavily_search`/`tavily_extract` 可通过通用 MCP 调用 | `examples/mcp/tavily.json`（新建） | 集成测试：Tavily 搜索返回结果并生成 Receipt | 4h |
| W5-M6 | Skill 能力扩展 | 扩展 Skill Manifest：`required-capabilities`/`preferred-tools`/`fallback-policy`；实现 `strict` 和 `same-capability` 两种策略 | `Src/internal/skills/manifest/`、`Src/internal/skills/resolver/` | 单元测试：严格约束不自动扩大；同能力替代可见 | 6h |
| W5-M7 | 最小 Capability Index | 新建 `internal/tools/capability/index.go`：支持 `web.search`/`web.extract`/`repository.search`/`repository.read`；标签来源仅接受内置目录/MCP Manifest/管理员确认 | `Src/internal/tools/capability/index.go`（新建） | 单元测试：模型建议不自动批准 | 4h |
| W5-M8 | Tool Context Planner | 新建 `internal/contextplanner/tool_planner.go`：按 Skill/任务过滤工具候选；系统过滤后向模型暴露 1~3 个候选 | `Src/internal/contextplanner/tool_planner.go`（新建） | 单元测试：候选过滤与排序正确 | 6h |

### W5 验收门禁

```bash
cd Src
go test ./internal/mcp/... -v
go test ./internal/skills/... -v
```

- [ ] 新增 MCP 工具无需编写 Provider 专属核心代码；
- [ ] Tavily 禁用后可启用另一个搜索 MCP；
- [ ] Skill 首选工具不可用时，`same-capability` 策略下可选择同能力工具；
- [ ] 严格 `allowed-tools` 不会被自动扩大；
- [ ] 所有 MCP 调用带 Server、Tool、Schema 和 Receipt。

---

## 7. W6：体验闭环 + 发布门禁

### 目标

单一 Composer；实时 stdout/stderr；Diff 查看/接受/撤销；Inspector 默认收起；E2E 与故障注入；V1 Release Candidate。

### 任务卡

| ID | 任务 | 具体动作 | 涉及文件 | 验证 | 预估 |
|---|---|---|---|---|---|
| W6-U1 | 单一 Composer | 隐藏 Auto/Ask/Task、Agent Team、Provider、模型、Thinking、执行器、场景、详细预算；只保留输入框/附件/安全姿态/发送/停止 | `Src/web/src/features/tasks/`、`Src/web/src/app/shell.tsx` | 前端测试：Composer 只显示 4 个元素 | 6h |
| W6-U2 | 实时工具输出 | 实现 `ToolExecutionEvent`：`started/stdout/stderr/progress/truncated/artifact_created/completed/failed/cancelled`；SSE 替代高频轮询；UI 终端式工具卡片 | `Src/internal/harness/events.go`、`Src/web/src/features/tasks/message-task-stream.tsx` | 集成测试：ToolCall 返回后 300ms 内产生 `started`；stdout/stderr 更新间隔 ≤ 250ms | 8h |
| W6-U3 | Diff 查看/接受/撤销 | 实现 Diff 视图；支持接受、撤销、继续修改；与 Git 工具组联动 | `Src/web/src/features/projects/project-page.tsx`、`Src/internal/harness/diff.go`（新建） | 集成测试：Diff 可接受/撤销 | 8h |
| W6-U4 | Inspector 默认收起 | 右侧 Inspector 包含 Run/Changes/Terminal/Tools/Context/Usage/Security/Trace；默认收起 | `Src/web/src/app/shell.tsx` | 前端测试 | 3h |
| W6-U5 | 审批卡 | 实现审批卡 UI；与 ExecutionGrant 联动 | `Src/web/src/features/tasks/` | 集成测试：审批后工具继续执行 | 4h |
| W6-U6 | 自适应计划 UI | 简单任务不展示计划；复杂任务展示计划；计划状态根据 Tool/Artifact 投影 | `Src/web/src/features/tasks/`、`Src/internal/harness/plan.go` | 集成测试 | 4h |
| W6-U7 | E2E 主链路 | 编写 E2E：打开项目 → 描述目标 → Agent 读/改/验 → 展示 Diff → 接受/撤销/提交 → 恢复 | `Src/tests/e2e/`（新建） | E2E 通过 | 8h |
| W6-U8 | 故障注入测试 | 模拟 Worker kill、MCP 失败、模型超时；验证恢复与 Receipt | `Src/tests/integration/` | 故障注入测试通过 | 6h |
| W6-U9 | 性能基线 | 测量：ToolCall → started 事件延迟；stdout/stderr 更新间隔；取消生效时间；Run 恢复时间 | `Src/tests/benchmark/`（新建） | 性能报告 | 4h |
| W6-U10 | V1 Release Candidate | 打 tag；更新 README；准备发布说明 | `README.md`、`CHANGELOG.md`（新建） | 发布检查清单全绿 | 4h |

### W6 验收门禁（V1 发布门禁）

```bash
cd Src
go test ./...
go test -race ./...
cd web && pnpm lint && pnpm test && pnpm build
cd ../.. && docker build -t oktapod:v1 .
# E2E 通过
# 故障注入通过
# 性能基线达标
```

- [ ] 用户可从自然语言目标完成代码修改和验证；
- [ ] 简单问题不产生额外分类调用；
- [ ] 简单任务不强制展示计划；
- [ ] 修改后可查看 Diff，可接受、撤销、继续修改；
- [ ] 会话和任务可恢复；
- [ ] ToolCall 返回后 300ms 内产生 `started` 事件；
- [ ] stdout/stderr 更新间隔 ≤ 250ms；
- [ ] 取消操作 1 秒内生效；
- [ ] 完整日志可追溯；
- [ ] 主分支 required checks 全部通过；
- [ ] Docker 镜像提供完整 Web UI；
- [ ] 核心文件 ≤ 800 行；
- [ ] 主页面组件 ≤ 300 行。

---

## 8. 关键代码片段与接口定义

### 8.1 Pure Agent Loop 接口

```go
// internal/agentloop/loop.go
package agentloop

import "context"

type LoopState struct {
    Messages []ModelMessage
    Tools    []ToolSpec
    // ... 其他运行态
}

type LoopEvent interface{ isLoopEvent() }

type ModelStarted struct{ ModelID string }
type TextDelta struct{ Delta string }
type ThinkingDelta struct{ Delta string }
type ToolBatchProposed struct{ Calls []ToolCall }
type ToolResultObserved struct{ Results []ToolResult }
type TurnCompleted struct{ FinalMessage ModelMessage }
type TurnFailed struct{ Err error }
type ContextLimitReached struct{ UsedTokens int }

type AgentLoop interface {
    Next(ctx context.Context, state LoopState) (LoopEvent, error)
}
```

### 8.2 ExecutionPolicy 接口

```go
// internal/tools/policy/execution_policy.go
package policy

import "context"

type ExecutionRequest struct {
    ToolIdentity string
    Input        map[string]any
    Scope        Scope
    RunID        string
    StepID       string
}

type Decision struct {
    Action       Action // Allow | Ask | Deny
    Risk         RiskLevel
    Grant        *ExecutionGrant // 仅 Action=Ask 时可能生成
    Reason       string
    BudgetImpact BudgetImpact
}

type ExecutionPolicy interface {
    Evaluate(ctx context.Context, req ExecutionRequest) (Decision, error)
}
```

### 8.3 ExecutionGrant

```go
// internal/tools/grants/grant.go
package grants

import "time"

type ExecutionGrant struct {
    GrantID       string
    RunID         string
    StepID        string
    ToolCallID    string
    ToolIdentity  string
    InputHash     string
    Scope         Scope
    Risk          RiskLevel
    ApprovalRule  string
    ExpiresAt     time.Time
    PolicyVersion string
    Signature     string // HMAC-SHA256
}

func Sign(grant *ExecutionGrant, secret []byte) string { /* ... */ }
func Verify(grant *ExecutionGrant, secret []byte) bool { /* ... */ }
```

### 8.4 Durable Harness 接口

```go
// internal/harness/harness.go
package harness

import "context"

type Harness interface {
    StartRun(ctx context.Context, req StartRunRequest) (Run, error)
    AbortRun(ctx context.Context, runID string) error
    Checkpoint(ctx context.Context, runID string) error
    Recover(ctx context.Context, runID string) (Run, error)
    RecordEvent(ctx context.Context, runID string, event LoopEvent) error
    // ... Steering/Follow-up/Budget
}
```

### 8.5 ToolSpec

```go
// internal/tools/registry/spec.go
package registry

type ToolSpec struct {
    Name         string
    Description  string
    InputSchema  JSONSchema
    OutputSchema JSONSchema
    Source       Source // native | mcp
    ServerID     string // 仅 mcp
    Capabilities []string
    ReadOnly     bool
    Destructive  bool
    OpenWorld    bool
    Idempotent   bool
    MaxOutput    int
    Executor     Executor
    Validator    Validator
    Renderer     Renderer
}
```

---

## 9. 风险与回退策略

| 风险 | 概率 | 影响 | 回退策略 |
|---|---|---|---|
| W1 拆分引入行为变化 | 中 | 高 | 每步小提交；测试不绿立即回退；拆分期间禁止并行开发其他功能 |
| Pi 语义移植遗漏边界条件 | 中 | 中 | 逐行对比 Pi 源码；为每个工具编写与 Pi 输出对比的 Golden Test |
| Pure Agent Loop 拆分后性能下降 | 低 | 中 | 保留 Worker 作为适配器；性能基线不达标时优化 Loop/Harness 通信 |
| MCP 通用化后 Tavily 集成复杂 | 低 | 低 | 先支持 STDIO；Streamable HTTP 可延后到 V1.1 |
| W6 E2E 不稳定 | 中 | 中 | E2E 失败不阻塞主链路；先发布 RC，E2E 稳定后再正式 Release |

---

## 10. 给 AI Coding Agent 的执行检查清单

每完成一个任务卡，必须自查：

- [ ] 已阅读并理解架构文档对应章节；
- [ ] 代码挂在已拆分的窄端口上，未向巨型接口添加方法；
- [ ] 无行为变化重构阶段，全量测试绿；
- [ ] 新功能有对应单元测试；
- [ ] 涉及工具执行时，已验证 ExecutionGrant 与 Receipt；
- [ ] 涉及外部内容时，已使用 External Content Envelope；
- [ ] commit message 为英文，格式正确；
- [ ] 未执行 `git push`。

---

**文档结束。下一步：按 W1 任务卡开始执行。**
