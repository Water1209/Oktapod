# Oktapod 部署与运维指南

> 版本：v1.0
> 更新日期：2026-07-28

## 1. 当前部署边界

Release 1A 默认采用单机 Go 服务、React 静态资源、SQLite 和操作系统 SecretStore。服务默认监听 `127.0.0.1:8787`；Linux 与 Team PostgreSQL 仍需按验收矩阵完成真实环境门禁后才能标记发布完成。

`Deploy/docker-compose.team.yml` 只用于后续 Team 单节点试点。运行前必须从 `Deploy/.env.example` 创建被 Git 忽略的 `Deploy/.env`，并配置独立随机的数据库口令、完整数据库 DSN 与 Team 管理 Token；仓库和镜像不提供共享默认凭据。

## 2. Provider 配置

Provider 非敏感元数据保存在数据库，凭据只保存在 Windows Credential Manager 或 Linux Secret Service。不得把 API Key 写入配置文件、命令参数、日志、截图或导出包。

兼容 CLI：

```powershell
.\Build\oktapod.exe model configure --endpoint https://provider.example/v1 --model model-id
.\Build\oktapod.exe model status
.\Build\oktapod.exe model reset
```

CLI 管理默认 Provider 实例。Web/API 可通过 `/api/v1/providers` 管理多个实例，`protocol_family` 可选择 `openai_compatible`、`anthropic` 或 `gemini`；凭据只在创建或更新时提交，服务不会回显旧值。Anthropic 默认 Endpoint 为 `https://api.anthropic.com/v1`，Gemini 默认 Endpoint 为 `https://generativelanguage.googleapis.com/v1beta`，自定义 Endpoint 仍受连接测试的超时、响应上限与脱敏约束。迁移 `000048_provider_registry` 会把旧的组织级单例配置回填为默认实例。

首次启动不会生成或写死可登录的管理员口令。操作者应在登录页注册第一个本地账户；任何示例、日志和部署文件都不得提供共享默认密码。初始化内部主体不具备固定登录凭据，不能绕过项目 Owner 隔离。

Provider 更新无需重启：下一次 Chat Turn 或新 Run 读取新配置；已经创建的 Run 继续使用冻结的 Provider、模型、Thinking 和 Catalog 版本。

## 3. 连接测试与故障处理

- `POST /api/v1/providers/{provider_id}/test` 使用受限超时并返回阶段、延迟、目录状态和脱敏错误。
- 模型目录过期时，API 可返回最近成功目录并标记 `stale`，同时后台刷新。
- `MODEL_CREDENTIAL_MISSING` 表示 SecretStore 中不存在对应凭据；应重新输入凭据，不能从数据库恢复明文。
- 删除 Provider 会同时删除其目录缓存和 SecretStore 凭据；默认实例删除后会选择剩余实例作为新默认。
- 原生 Anthropic/Gemini 的 Message、Tool、Thinking、Usage 和流式映射已纳入合同测试；Anthropic 使用 SSE，Gemini 使用 `streamGenerateContent?alt=sse`。发布前仍必须使用真实 Provider 验证可见增量、工具调用和最终 Usage。

## 4. Release 1B 数据与终端入口

- `/projects/{project_id}/terminal` 不向浏览器开放裸 Shell；命令会创建持久化 Task，并继续经过 ExecutionPolicy、审批、收据和审计。页面显示持久任务状态/结果摘要，允许请求取消，并可回到同一 Thread 继续分析；已经完成的命令无法被事后取消。
- `/import-export` 的浏览器预检会先扫描原始 JSON/JSONL 中的敏感键；发现凭据类字段时拒绝导入。服务端 `/api/v1/projects/{project_id}/transcript-import` 在单事务内创建私有 Thread、消息和 Session Entry，并支持幂等重试。
- 网页导出支持 JSON、JSONL 和 HTML，并对消息正文中的常见 Bearer/API Key/密码赋值模式脱敏；自有 JSONL 可按 Thread 重新导入。Pi v1/v2/v3 完整树兼容、`firstKeptEntryId`、Tool/Compaction 元数据和严格 round-trip 尚未通过发布门禁。
- 数据维护、备份和恢复仍是独立高风险操作，不得通过终端页或诊断页绕过现有运维流程。
- 项目级审批规则由迁移 `000049_project_approval_rules` 保存，默认有效 30 天。只有 Project Owner 可以查看或撤销；规则不会绕过静态拒绝。运维核查可使用 `GET /api/v1/projects/{project_id}/approval-rules`，撤销使用对应 `DELETE`，不得直接修改数据库延长 TTL。
- 自动审批必须单独配置 `OKTAPOD_REVIEWER_MODEL`。该模型只接收当前计划项、冻结参数、风险和策略理由，不获得工具；未配置、超时、解析失败和高风险结论都会自动转为人工 Human Action。不得将主 Agent 的自由文本当作 Reviewer 决策，也不得通过修改数据库跳过等待状态。
- `web_search` 默认依次访问 `https://html.duckduckgo.com/html/` 与 `https://www.bing.com/search?format=rss`，并继承 `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`。生产网络必须至少允许其中一个 HTTPS 端点；单端点不可用会自动回退，只有全部失败才记录 `WEB_SEARCH_UNAVAILABLE`。不得通过关闭 TLS 校验或放宽结果 URL 校验排障。
- 迁移 `000050_run_execution_plans`、`000051_human_actions` 和 `000052_approval_rule_signatures` 分别保存 Plan Revision/Item、统一人工动作以及 v2 结构化授权签名；`000053_task_run_status_reconciliation` 会以最新 Run 为真源修复旧版本遗留的 Task 终态漂移，避免已阻塞任务继续接收 Steering。升级前应先备份 SQLite；升级后检查旧待审批已按相同 ID 回填，旧宽泛授权只保留审计而不再命中。

## 5. 验证

### 5.1 Worker 并发

默认启动 4 个 Worker。需要降低本机资源占用时，可在启动前设置：

```powershell
$env:OKTAPOD_RUN_CONCURRENCY="2"
```

允许值为 `1` 到 `4`。该值降低当前服务进程的 Worker 数；SQLite 的数据库级 Claim 仍以最多 4 个不同活跃 Run 作为安全上限。不要通过启动额外进程绕过该边界。

注册、项目/任务创建、Transcript 导入和结果验收会在 `SQLITE_BUSY/BUSY_SNAPSHOT` 时使用短时有界退避并复用幂等键。持续出现锁错误不是正常状态，应通过诊断页检查活跃 Worker/Lease，而不是扩大无限重试。

### 5.2 只读诊断

登录后访问 `GET /api/v1/diagnostics` 可检查版本、数据库、Provider、Worker、审批等待数、活跃 lease 和 SSE 连接数。响应不包含 Provider 凭据、原始日志或配置 Secret。备份与恢复不提供在此只读 API 中，必须继续使用独立、受控的运维流程。

### 5.3 发布验证

代码或配置变化后至少执行：

```powershell
$env:GOCACHE="$env:TEMP\go-build-cache"
go test ./... -count=1
pnpm verify
```

发布验收还必须完成浏览器真实流式任务、审批、文件修改、测试与 Diff，并确认日志、API 响应和导出内容不包含凭据。
