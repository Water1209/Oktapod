# Oktapod V1 执行计划与验收门禁

> 版本：V1.2
> 更新日期：2026-07-30
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
- 完全访问（内部值 `full_access`）仍受硬拒绝、Scope 和 MCP 人工确认约束。
- 内建搜索在可替代能力完成真实验收前不得删除。
- 不恢复独立非交互 Terminal 导航；命令在 Thread 内受 Policy、审批和 Receipt 治理。

## 2. 执行批次

### 2.1 当前执行记录（2026-07-30）

| 批次 | 状态 | 已落地证据 | 仍未关闭 |
|---|---|---|---|
| B0 | `PASS` | 架构、计划、README、发布说明、部署指南、归档说明和追踪矩阵已统一为 V1.0.0 范围 | 后续变更仍须同步事实 |
| B1 | `PASS` | Repository 窄端口、`agentloop`、`harness` 与 Inspector 组件已提取并有测试 | Worker/页面仍可继续拆分，但行数只作维护指标 |
| B2 | `PARTIAL` | `ExecutionAuthorization`、Git/凭据隔离、版本注入、CI/OpenAPI/迁移门禁已落地 | 等待 Ubuntu/Windows 原生 CI 关闭 tag 门禁 |
| B3 | `PASS` | ToolSpec 元数据、简洁别名/旧名兼容、受限摘要和完整长输出 Artifact 已落地 | stdout/stderr 逐块直播进入 V1.1 |
| B4 | `PASS` | MCP Schema/配置/健康版本、Run Tool Snapshot、Skill 约束、strict 阻塞和 `external_untrusted` Envelope 已落地 | 确定性同能力排名与跨 Provider 参数转换进入 V1.1 |
| B5 | `PASS` | Composer 邻接治理、三基线 Changes、文件/Hunk 查看、接受、受治理撤销与选择提交已落地；中文未跟踪文件浏览器闭环通过 | 更细粒度行级交互可后续优化 |
| B6 | `PARTIAL` | Windows `pnpm verify`、Playwright foundation 1/1 + success 23/23、真实 Provider、进程 kill 恢复、带版本工件和产品烟测均有证据 | Ubuntu race 与双平台原生产品烟测须在 tag 前由 CI 通过 |

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
- 将 `project-page.tsx` 中的 Inspector、Changes 等稳定业务块提取为组件；约 300 行仅作维护参考，不作为用户闭环发布门禁。

门禁：

```powershell
$env:GOCACHE="$env:TEMP\go-build-cache"
go test ./Src/internal/application/... ./Src/internal/runtime/... ./Src/tests/integration/...
go test -race ./Src/internal/runtime/... ./Src/tests/integration/...
pnpm verify
```

- Golden Trace 证明重构前后事件、消息、Receipt 和恢复语义一致。
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
- 内建搜索进入统一 ToolSpec，作为无需外部凭据的可用能力；Tavily MCP 是可选首选，不是核心依赖。
- MCP、搜索、网页和仓库指令统一包裹为 `external_untrusted` Envelope。

门禁：

- STDIO 与 Streamable HTTP 合同通过。
- Schema 变化触发重审；安装幂等、同版本冲突、鉴权失败和限流可见。
- 外部内容不能改变权限、系统提示或安全策略。

### B5：Codex 式用户闭环

交付：

- Composer 保留输入、附件、安全姿态、发送/停止；审批、输入和阻塞动作紧邻 Composer。
- Inspector 默认收起，包含 Run、Changes、Terminal、Tools、Context、Usage、Security 和 Trace。
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
- `ToolSpec`：Native、MCP 和内建搜索共用。

### 数据

- 扩展 `mcp_connections` 的 Schema Hash、配置版本和健康状态。
- 新增 Run Tool Snapshot，冻结工具身份、来源、Schema、能力和 Policy Version。
- Skill 能力约束保存在已审核 Manifest，V1 不重复建表。

### OpenAPI

- 增加 Run Changes 只读查询。
- 为 Tool Execution Event 定义稳定结构。
- Changes 的写操作复用受治理 Task/Human Action 流程。

## 4. 执行纪律

- 每批先定向测试，再执行 `pnpm verify`；并发/恢复变更追加 `go test -race ./...`。
- 只暂存本批文件，保留工作区其他修改。
- Commit message 使用英文；不自动 push。
- 每批代码完成后同步 README、架构、部署指南和验收矩阵。
- 用户体验和已有行为优先于目录形式、代码行数和架构纯度。
