# Pi vs Oktapod：工具体系与安全/审批策略对比

对比基线：
- **Pi**：`E:\git\pi`，冻结基线 `b1c444d`。核心路径：`packages/coding-agent/src/core/tools/`（read/write/edit/bash/grep/find/ls）、`packages/coding-agent/src/core/session-manager.ts`、`packages/coding-agent/src/core/agent-session.ts`、`packages/coding-agent/src/core/trust-manager.ts`、`packages/coding-agent/src/core/project-trust.ts`、`packages/coding-agent/src/core/compaction/branch-summarization.ts`。
- **Oktapod**：`E:\git\Oktapod\Src`。核心路径：`Src/internal/adapters/workspace/local.go`、`Src/internal/runtime/worker.go`（`executeWorkspaceTool` / `executeSkillInvoke` / `executeMCPTool` / `executeWebSearchTool`）、`Src/internal/domain/core/tools.go`（`PlanTool` + 校验器）、`Src/internal/domain/core/permission.go`（`FullAccessActive`）、`Src/internal/adapters/db/sqlite/store.go`（审批落库 + `conversation_approval_rules` + `ApplyConversationSteering`）、`Src/db/migrations/sqlite/000044_session_entries.up.sql`、`Src/internal/adapters/model/openaicompat/client.go`（`workspaceTools()` 工具 schema 下发）。

---

## 一、工具清单

### 对比表

| Pi 工具 | 参数（typebox schema） | 输出处理 | 风险/审批声明 | Oktapod 对应物 | 判断 |
|---|---|---|---|---|---|
| `read` | `path, offset?, limit?` | 2000 行 / 50KB `truncateHead`；首行超字节时建议改用 `bash: sed -n ... | head -c ...`；offset/limit Continuation 提示带下一页 offset；图片走附件通道、不支持视觉的模型自动降级为文本提示 | 仅 schema description 隐含「只读」，无 approval 标记 | **无对应** | Pi 完备。Oktapod 缺独立的 read 工具，模型看不到文件内容只能靠 `web_search` 或 Skill 正文 |
| `write` | `path, content` | 不截断输入；返回 `Successfully wrote N bytes`；写前 mkdir -p；走 `withFileMutationQueue` 防并发 | 仅描述「Creates the file if it doesn't exist, overwrites if it does」 | **无 overwrite 限制** | `workspace_write_file(path, content)`：content ≤ 3000 字符（schema 强制）/ 1 MiB（domain 校验上限）；**只允许创建**，已存在则 `WORKSPACE_FILE_EXISTS`；sha256 相同则 replayed=true；写流程走临时文件 + hard link 原子替换 | Oktapod 更稳（原子 create-only + 内容哈希重放），但表达力受限：不能改写、不能分段写大文件；Pi 更通用 |
| `edit` | `path, edits[{oldText,newText}]` | edits 数组、要求 oldText 唯一不重叠；`prepareArguments` 兼容字符串化 JSON / 旧版 `oldText/newText` 直参；BOM 剥离、行尾 CRLF/LF 归一化；返回 unified patch + firstChangedLine；走 `withFileMutationQueue` | 描述「Make precise file edits with exact text replacement」 | **无对应** | Pi 完备。Oktapod 完全没有「局部精确编辑」原语；模型只能整文件重写，且当前还是 create-only，所以连「重写」都做不到 |
| `bash` | `command, timeout?` | stdout/stderr 流式累积（OutputAccumulator）；2000 行 / 50KB `truncateTail`（保尾部）；超长自动落 temp 文件并在尾部追加 `Full output: <path>`；timeout 通过 `killProcessTree` 整树终止；环境变量剥离 PI_* 再注入 | 描述「Execute a bash command ... Output is truncated to last 2000 lines or 50KB」 | **无审批字段**（信任模型在 harness 之外：项目级 trust + extension `user_bash` hook 拦截） | `workspace_run_command(program, args)`：禁 shell 解释器、禁 curl/wget/ssh/nc 等网络程序、禁路径形式 program；args ≤ 64 项；5 分钟超时；stdout/stderr 各 1 MiB `boundedBuffer` 截断；`Cancel` 整树终止；env 白名单（PATH/TEMP/LANG 等）+ 隔离 HOME/GOCACHE/GOPROXY=off | Oktapod 更严（参数化 exec 无 shell、网络工具黑名单、env 白名单 + 隔离构建缓存）；Pi 更通用（任意 shell 命令、有流式输出、有 full-output 落盘）。**安全姿态 Oktapod 显著更高，工程能力 Pi 显著更全** |
| `grep` | `pattern, path?, glob?, ignoreCase?, literal?, context?, limit?` | 走 `rg --json`；100 匹配默认上限；行 500 字符截断（`GREP_MAX_LINE_LENGTH`）；context 行从磁盘二次读取；`.gitignore` 默认尊重；命中/字节双维度截断告示 | 无 | **无对应** | Pi 完备。Oktapod 完全没有文件内容搜索能力 |
| `find` | `pattern, path?, limit?` | 走 `fd --glob`；默认 1000 条；`.gitignore` 尊重；路径含 `/` 时自动 `**/` 前缀与 `--full-path`；目录尾随 `/` 保留 | 无 | **无对应** | Pi 完备。Oktapod 完全没有文件查找能力 |
| `ls` | `path?, limit?` | 默认 500 条；目录带 `/` 后缀；不区分大小写排序；空目录返回 `(empty directory)` | 无 | **无对应** | Pi 完备。Oktapod 没有列目录能力 |
| —（无） | — | — | — | `web_search(query, max_results?, freshness?, allowed_domains?, blocked_domains?)` | Oktapod 独有。query 2-500 字符；max_results 1-10；freshness 枚举；域名白/黑名单互斥；走 `WebSearchExecutor` 接口由适配器实现 |
| —（无） | — | — | — | `skill_invoke(skill, args?)` | Oktapod 独有。skill 必须在 Run 冻结快照内；同名歧义返回 `SKILL_NAME_AMBIGUOUS`；`model_invocable=false` 拒执行；schema 强制 `additionalProperties:false` |
| —（无） | — | — | — | `mcp__<capability_id>__<tool>` | Oktapod 独有。命名空间三段强绑定 capability；参数 JSON > 1 MiB 直接拒；必须命中 Run 快照内 manifest 工具清单，否则 `MCP_TOOL_NOT_ALLOWED` |
| —（无） | — | — | — | **缺失**：read / edit / ls / find / grep / git | — |

### 关键证据

**Pi 工具集合（`packages/coding-agent/src/core/tools/index.ts`）**：

```ts
export type ToolName = "read" | "bash" | "edit" | "write" | "grep" | "find" | "ls";
export function createCodingToolDefinitions(cwd) { return [read, bash, edit, write]; }
export function createReadOnlyToolDefinitions(cwd) { return [read, grep, find, ls]; }
```

Pi 把 7 个工具分两个集合：编码（read/bash/edit/write）+ 只读探索（read/grep/find/ls）。所有工具的 `Operations` 接口都可替换（`ReadOperations`/`EditOperations`/`BashOperations`/...），支持 SSH 远端委托。

**Oktapod 工具注册（`Src/internal/domain/core/types.go:45-48`）**：

```go
ToolWorkspaceWriteFile  = "workspace_write_file"
ToolWorkspaceRunCommand = "workspace_run_command"
ToolWebSearch           = "web_search"
ToolSkillInvoke         = "skill_invoke"
```

加上 `mcp__<cap>__<tool>` 命名空间共 5 类。`PlanTool`（`domain/core/tools.go:22`）只对这 5 类有分支；其余任何名字直接 L3 / `PolicyDeny / "tool is not registered"`。

**Oktapod 缺失工具的后果**：模型在单 agent 模式下没有任何「读文件内容」「看目录结构」「搜代码」「精确改文件」的手段，只能写新文件、跑命令、调 web、调 MCP。这是一个面向「交付物生成」的 Agent，不是一个面向「代码协作」的 Agent。

---

## 二、工具 schema 质量

### 对比表

| 维度 | Pi | Oktapod | 判断 |
|---|---|---|---|
| 描述精度 | 每个工具有 4 层文本：`description`（schema 字段，随参数下发）+ `promptSnippet`（system prompt 一行简介）+ `promptGuidelines`（system prompt 条目，可多条）+ 错误时的 continuation 提示（如 read 截断时返回 `[Showing lines X-Y of Z. Use offset=N to continue.]`） | 单层：`Description` 一段中文/英文混合字符串 + schema 自身（`minLength/maxLength/enum`） | Pi 更好。四层文本让模型在「是否需要这个工具」「怎么用」「参数怎么填」「出错如何继续」四个阶段都有引导；Oktapod 一层文本既要做营销也要做约束，精度必然稀释 |
| 参数约束 | typebox 类型 + 自然语言描述，几乎不用 `minLength/maxLength`；限制通过 description 文本说明（如 edit 要求 oldText 唯一不重叠） | JSON Schema 强约束（`minLength:1, maxLength:200, additionalProperties:false, enum:[...]`）+ domain 层 `validate*` 二次校验（包括 `filepath.IsAbs`、`filepath.VolumeName`、shell 黑名单、网络程序黑名单、NUL 字符检测） | Oktapod 更严。schema 层硬约束让模型在「生成阶段」就被约束，domain 层校验兜底；Pi 完全靠 description + 运行时校验，模型可以生成超界参数，由工具内部再报错 |
| 错误处理引导 | 截断时返回结构化提示带下一页 offset / `Full output: <path>` / `Use limit=N for more, or refine pattern`；首行超字节时建议改用 `bash: sed -n '${line}p' ${path} | head -c ${DEFAULT_MAX_BYTES}` | 错误码 + 中文 ResultSummary（如 `SKILL_NOT_SNAPSHOTTED` / `MCP_TOOL_NOT_ALLOWED` / `WORKSPACE_FILE_EXISTS`），Output 里塞 `reason` / `retryable`；但**没有给模型的 continuation 提示**——模型只看到「失败」，不知道下一步该怎么调整 | Pi 更好。Pi 的工具输出本身就是给模型看的下一回合指令；Oktapod 的输出是给用户看的审计记录，模型拿到错误后缺乏「如何继续」的引导 |
| Schema 兼容性 | typebox 库，工具内部有 `prepareArguments` 兼容字符串化 edits / 旧版单 edit 参数 | 手写 map[string]any；`parseToolArguments` 在 openaicompat 客户端做了一次 JSON 修复（`repaired` 计数），不兼容旧参数形态 | Pi 更鲁棒。`prepareArguments` 显式列出兼容路径（「Some models (Opus 4.6, GLM-5.1) send edits as a JSON string」），并保留旧版 `oldText/newText` 直参 |
| 上下文工程 | system prompt 有「Available tools」清单（一行 snippet 一行），跟随 `selectedTools` 动态裁剪 | system prompt 用中文自然语言提示「必须先单独调用 skill_invoke」「长文交付必须主动分段」，工具列表由 `workspaceTools()` 在 OpenAI 兼容客户端硬编码 3 项 + MCP 工具按需注入 | Pi 更结构化。Pi 的工具清单是代码生成的、可裁剪；Oktapod 把工具规则塞进自然语言段落，与真实下发的 `payload.Tools` 可能脱节（如 `applySceneToolPolicy` 过滤掉 `workspace_run_command` 时，system prompt 仍然提到「需要审批」） |

### 关键证据

**Pi `edit` 工具的完整描述层（`packages/coding-agent/src/core/tools/edit.ts:295-305`）**：

```ts
description: "Edit a single file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes.",
promptSnippet: "Make precise file edits with exact text replacement, including multiple disjoint edits in one call",
promptGuidelines: [
  "Use edit for precise changes (edits[].oldText must match exactly)",
  "When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls",
  "Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.",
  "Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
],
```

description（schema 内嵌）+ snippet（system prompt 一行）+ guidelines（system prompt 多行）三层各管一件事，覆盖「是什么 / 何时用 / 怎么用对」。

**Oktapod `workspace_write_file` 单层描述（`Src/internal/adapters/model/openaicompat/client.go:769-780`）**：

```go
Name:        core.ToolWorkspaceWriteFile,
Description: "Create a new UTF-8 text file inside the current project workspace. Paths must be project-relative. Existing files are never overwritten. Keep content within 3000 characters; split longer documents into clearly named section files.",
Parameters: map[string]any{
    "type": "object", "additionalProperties": false,
    "required": []string{"path", "content"},
    "properties": map[string]any{
        "path":    map[string]any{"type": "string", "minLength": 1, "maxLength": 500},
        "content": map[string]any{"type": "string", "maxLength": 3000},
    },
},
```

schema 有 `maxLength:3000` 硬约束，domain 层 `validateWriteArguments` 又校验 1 MiB 上限（与 3000 字符的 prompt 约束不一致——见 `worker.go:278` 的 system prompt 提示「不得超过 3000 个字符」）。这种「schema 写死 3000、domain 允许 1 MiB、prompt 又要求 3000」的三层不一致，模型很容易困惑。

**Pi read 截断时的 actionable continuation（`read.ts:295-310`）**：

```ts
if (truncation.truncatedBy === "lines") {
  outputText += `\n\n[Showing lines ${startLineDisplay}-${endLineDisplay} of ${totalFileLines}. Use offset=${nextOffset} to continue.]`;
} else {
  outputText += `\n\n[Showing lines ${startLineDisplay}-${endLineDisplay} of ${totalFileLines} (${formatSize(DEFAULT_MAX_BYTES)} limit). Use offset=${nextOffset} to continue.]`;
}
// 首行超字节:
outputText = `[Line ${startLineDisplay} is ${firstLineSize}, exceeds ${formatSize(DEFAULT_MAX_BYTES)} limit. Use bash: sed -n '${startLineDisplay}p' ${path} | head -c ${DEFAULT_MAX_BYTES}]`;
```

工具输出直接教模型下一步怎么调用自己，是闭环的。Oktapod 的工具输出（`local.go:148` 等）是 `{"path": ..., "content_hash": ..., "size_bytes": ..., "replayed": false}`，是给人看的收据，不是给模型的指令。

---

## 三、安全模型

### 对比表

| 维度 | Pi | Oktapod | 判断 |
|---|---|---|---|
| 路径限制 | 工具内 `resolveToCwd`：所有路径相对 cwd resolve，但**没有阻止 escape**——`resolveToCwd("../etc/passwd", cwd)` 会返回 `/etc/passwd`，工具照常读。`resolveReadPathAsync` 还会额外尝试 macOS NFD/curly-quote 变体。安全姿态依赖「用户已在受信任目录启动 pi」 | `validateWriteArguments` 拒绝绝对路径 / 卷名 / 前导斜杠；`secureParent` 逐段 walk，发现 symlink 立即拒绝；`projectRoot` 强制 UUID 形式 project_id 并 `filepath.Rel` 二次验证不越出 root；workspace root 必须真实目录且非 symlink | **Oktapod 显著更细**。Pi 是「信任启动目录」模型；Oktapod 是「零信任 + symlink 防护 + UUID 强约束 + 逐段 walk」模型 |
| 命令预检 | 无静态预检。`bash` 接受任意 command 字符串，靠 `killProcessTree` 做超时兜底；extension 可挂 `user_bash` hook 拦截/重写命令（`extensions/runner.ts:946`） | 静态黑名单两层：①`validateCommandArguments` 拒绝 shell 解释器（cmd/powershell/bash/sh/zsh/fish/wsl）+ 网络程序（curl/wget/ssh/scp/sftp/ftp/nc/ncat/netcat/telnet）；②参数化 exec 不走 shell，无注入面；③`sceneAllowsWorkspaceCommand` 在 Run 快照层再过滤一次（scene_policy.workspace_command=false 时整类拒绝） | **Oktapod 显著更细**。Pi 完全靠「调用方信任模型输出」；Oktapod 是「白名单可执行文件 + 参数化 + 场景策略 + Run 快照冻结」四道闸 |
| 风险分级 | 无显式分级。工具 description 不声明风险；审批完全外包给 extension（`tool_call` hook 可拦）或用户（trust prompt 只针对「项目目录是否可信」，不是单次工具） | 4 级：`L0`（skill_invoke / web_search）→ `L1`（workspace_write_file）→ `L2`（workspace_run_command / MCP）→ `L3`（未注册工具默认）。每级对应不同 PolicyDecision | **Oktapod 显著更细**。Pi 没有风险语义；Oktapod 把风险等级显式写进 `tool_calls.risk_level` 列并随事件流式下发（`tool.planned` 事件 payload 含 `risk_level`） |
| 审批范围 | 项目目录级。`ProjectTrustStore` 用 JSON 文件存 `{path: true|false|null}`；`hasTrustRequiringProjectResources` 检测 `.pi/settings.json` / `.pi/extensions` / `.agents/skills` 等是否需要授权；选项：「Trust / Trust parent folder / Trust (this session only) / Do not trust」 | 工具调用级 + 会话级。`ApprovalScopeOnce`（一次性）vs `ApprovalScopeConversation`（会话内同类工具自动通过）；会话规则以 `rule_key` 索引——`workspace_run_command` 的 key 是 `program:<lowercased-program>`，MCP 是 `mcp:<full-tool-name>`；规则带 `expires_at`（继承源 approval 的 24h TTL） | **Oktapod 显著更细**。Pi 是「这个目录我信任你跑任何代码」；Oktapod 是「这次 `git status` 我批准，下次同一个 program 不再问」 |
| 审批参数冻结 | 无概念。信任后任意命令 | 强制。`approval_requests.frozen_params_json` + `input_hash`（`sha256(tool_name + "\n" + JSON(args))`）；审批时 Worker 重新跑 `PlanTool`，比对 `planned.InputHash != input.InputHash` 则拒执行（`TOOL_POLICY_DENIED`）；规则命中时也只放过同 program 的调用，参数仍是新冻结 | **Oktapod 显著更细**。这是 Pi 完全没有的能力——Pi 的 trust 是「路径黑名单」，Oktapod 是「参数哈希 + 工具名 + 决定」三元组 |
| Full Access | 无概念 | 用户在 Composer 中手动开启并持续到手动切换。`FullAccessActive(snapshot, now)` 在 `permission_mode=full_access` 时返回 true；只抑制 `workspace_run_command` 的 PolicyAsk，**不**绕过静态黑名单（shell/network 仍拒） | **Oktapod 独有**。给用户一个明确的手动控制开关，但不会因此放开硬安全约束 |
| 审批 TTL | 无（trust 永久） | `DefaultApprovalTTL = 24 * time.Hour`；`expires_at` 到期未决定自动置 rejected，「Expiry is a deterministic rejection, never an automatic execution」（types.go:61-64 注释） | **Oktapod 独有**。防止「昨天批的命令今天被回放」 |
| 副作用收据 | 无统一抽象。`bash` 有 `killProcessTree` 但无幂等性概念 | `BeginToolExecution` + `Idempotent` 标记 + `SIDE_EFFECT_STATUS_UNKNOWN`：上一个非幂等执行没有最终收据时，自动重试被停止；`workspace_write_file` 通过 `content_hash` 比对实现 `replayed=true` 幂等返回 | **Oktapod 显著更细**。Pi 没有「这个工具调用执行过吗」的语义；Oktapod 每个 ToolCall 都有 sha256 input hash + 显式 idempotent flag + 写入临时文件 + hard link 的原子语义 |

### 关键证据

**Oktapod 审批参数冻结（`Src/internal/adapters/db/sqlite/store.go:5597-5606`）**：

```sql
INSERT INTO approval_requests (id, ..., tool_name, risk_level, status, reason, frozen_params_json, input_hash, ..., expires_at)
VALUES (?, ..., 'pending', ?, ?, ?, ?, ?, ?)
```

`frozen_params_json` 在审批请求创建时落盘；Worker 执行时（`worker.go:1073-1075`）：

```go
planned := core.PlanTool(call)
if planned.Decision == core.PolicyDeny || planned.InputHash == "" || planned.InputHash != input.InputHash {
    return core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "TOOL_POLICY_DENIED", ...}
}
```

审批时刻的 hash 与执行时刻的 hash 必须一致——任何中间人/重放篡改都会被拒。

**Pi 项目信任的存储与命中（`trust-manager.ts:184-206`）**：

```ts
export function hasTrustRequiringProjectResources(cwd: string): boolean {
  // 检查 cwd/.pi/{settings.json, extensions, skills, prompts, themes, SYSTEM.md, APPEND_SYSTEM.md}
  // 沿父目录向上找 .agents/skills
}
```

只在「这个目录有可执行资源」时弹 trust prompt。一旦 trust 通过，后续 `bash` / `write` / `edit` 都不再拦截。

**Oktapod 会话级规则的窄化（`store.go:5624-5644`）**：

```go
// conversationApprovalRuleKey deliberately excludes command arguments.
// A session rule is only reusable for the same executable (or MCP tool), while
// every concrete invocation remains frozen and auditable in its own ToolCall.
func conversationApprovalRuleKey(toolName, argumentsJSON string) (string, bool) {
  if toolName == core.ToolWorkspaceRunCommand {
    // ... 只取 program 字段做 key，不带 args
    return "program:" + program, true
  }
  if strings.HasPrefix(toolName, "mcp__") && ... {
    return "mcp:" + toolName, true
  }
  return "", false
}
```

注释明确「规则只对 program 复用，不对 args 复用」——批准 `git status` 不等于批准 `git push`，因为 args 不同。但批准 `git status` 后再调 `git status foo bar` 也会命中规则（args 不参与 key）。这是设计上有意的窄化。

**Pi 完全没有等价物**——搜索 `Approve`、`approval` 在 `coding-agent/src` 下只命中 CLI 参数解析（`args.ts`）和 `package-manager-cli.ts`（与工具安全无关）。Pi 把「工具调用要不要审批」这件事完全外包给 extension 系统，core 不内建。

---

## 四、Session Tree

### 对比表

| 维度 | Pi | Oktapod（000044 迁移） | 判断 |
|---|---|---|---|
| 数据形态 | JSONL 文件，每行一个 entry。`SessionHeader` 单独一行（`type:"session", version, id, timestamp, cwd, parentSession`），其余 entry 都有 `id, parentId, timestamp, type` | 关系表 `session_entries`。`id, organization_id, project_id, conversation_id, parent_entry_id, seq, kind, ref_id, label, summary, namespace, payload_json, created_at`，外加 `UNIQUE(conversation_id, seq)` | 设计取向不同。Pi 是「单文件 append-only，跨 cwd 用文件系统目录隔离」；Oktapod 是「多租户关系表，行级外键 + 级联删除」。各自适配部署形态 |
| Entry 类型 | `message / thinking_level_change / model_change / compaction / branch_summary / custom / custom_message / label / session_info`（9 种） | `message / config_change / compaction / custom / branch_summary`（5 种，`CHECK` 约束） | **Pi 更细**。Pi 把「思考级别变化」「模型切换」「会话改名」「用户标签」都建为独立 entry 类型，参与树遍历；Oktapod 用 `config_change` 一类合并 `thinking_level_change + model_change`，并缺 `label`（用户书签）与 `session_info`（会话改名）这两类 |
| Leaf 概念 | `SessionManager.leafId` 内存字段，表示「当前追加位置」；不持久化。重启后从「fileEntries 最后一条」重建（`_buildIndex` 里 `this.leafId = entry.id` 循环覆盖） | `conversations.active_leaf_entry_id` 持久化列，FK 到 `session_entries.id`；回填时指向「每会话最后一条 message」 | **Oktapod 更显式**。把 leaf 落到 schema 层意味着可以跨进程恢复、可以做并发控制；Pi 的 leaf 是内存指针，重启即重置（但因为 entry 是线性追加，重置后等价于 leaf=last）。**但 Oktapod 当前还没有任何代码读写这个列**——列已建，逻辑未实装 |
| Navigate（树内跳转） | `navigateTree(targetId, options)`：收集 `entriesToSummarize`（oldLeaf→commonAncestor），可选 LLM 摘要 → `branchWithSummary` 在目标位置追加 `branch_summary` entry → `leafId = newLeafId`；用户消息类目标则把内容塞回编辑器（`editorText`）支持「重编辑」 | **未实现**。schema 已具备（`parent_entry_id` + `active_leaf_entry_id`），但没有任何 Go 代码读 `session_entries` 或更新 `active_leaf_entry_id` | **Pi 完备，Oktapod 仅数据层就绪**。Pi 的 navigate 是一个闭环（包含可选 LLM 摘要、取消、扩展 hook）；Oktapod 只有表 |
| Fork（跨 session 克隆） | `SessionManager.forkFrom(sourcePath, targetCwd)`：把源 session 的所有非 header entry 拷贝到新文件，新 header 的 `parentSession = sourcePath`；**entry id 不变**（共享 id 空间），跨 cwd 的 fork 用 `parentSessionPath` 追溯 | `conversations.parent_session_id` + `forked_from_entry_id` 两列已建，**未实现**。从列设计看，Oktapod 想记录「我是从哪个 session 的哪个 entry fork 来的」，比 Pi 多一个 `forked_from_entry_id` | **设计意图 Oktapod 更精确，实现都未完成**。Pi 已实装但只记录父文件路径；Oktapod 设计了双列（父 session + fork 点）但代码未写 |
| Branch Summary | `BranchSummaryEntry{type:"branch_summary", fromId, summary, details?, usage?, fromHook?}`；navigate 时由 `generateBranchSummary` LLM 生成；`collectEntriesForBranchSummary` 从 oldLeaf 回溯到 commonAncestor，跨越 compaction 边界；进入 LLM 上下文通过 `sessionEntryToContextMessages` 转成 `createBranchSummaryMessage` | `session_entries.kind = 'branch_summary'` + `summary` 字段，**未实现生成逻辑**。Oktapod 已有 `conversation_compactions` 表（独立做压缩），但 branch_summary 的「离开分支时摘要」概念没有入口 | **Pi 完备，Oktapod 未实装** |
| Compaction | `CompactionEntry{type:"compaction", summary, firstKeptEntryId, tokensBefore, ...}`；`buildContextEntries` 找到路径上最新 compaction，跳过早于 `firstKeptEntryId` 的 entries，从那里开始重放 | `session_entries.kind = 'compaction'` + `ref_id` 指向 `conversation_compactions.id`（独立表） | **设计等价**。Pi 把 summary 文本内嵌在 entry 里；Oktapod 把 entry 当指针，正文在另一表。Oktapod 这种方式更适合大 summary 的查询优化，但**目前没看到代码把 session_entries 与 conversation_compactions 关联使用** |
| Label | `LabelEntry{type:"label", targetId, label}`：独立的 entry 类型，也作为 `parentId` 参与树（branch 时跳过 label 重新链接）；`labelsById` 内存索引 | `session_entries.label` 列（字符串），同一 entry 一个标签 | **Pi 更灵活**。Pi 的 label 是事件（可以打、可以改、可以删，有 `labelTimestamp`），且可以挂在任何 entry 上；Oktapod 的 label 是列（一个 entry 只能一个标签，无历史） |
| Custom entries | `CustomEntry{type:"custom", customType, data}`（**不**进 LLM context，供 extension 持久化）+ `CustomMessageEntry{type:"custom_message", customType, content, display, details}`（**进** LLM context，作为 user message 注入） | `session_entries.kind = 'custom'` + `namespace` + `payload_json` | **Pi 更细**。Pi 区分「持久化但不注入」与「注入到 LLM」两种 custom；Oktapod 只有一种，且没看到「是否进 context」的标记 |
| 版本迁移 | `migrateV1ToV2` / `migrateV2ToV3`：v1 没有 id/parentId（线性），v2 加 id 树；v2→v3 把 `hookMessage` role 改名 `custom`；启动时自动迁移文件 | 回填：`INSERT INTO session_entries ... FROM messages` 按 `server_seq` 排序生成线性 message 链，`parent_entry_id` 指向前一条 | **Pi 更精细**。Pi 有显式 schema 版本号 + 迁移函数链；Oktapod 是一次性 SQL 回填，对未来 schema 演进没有 version 列 |
| 上下文构造 | `buildSessionContext`：从 leaf 走到根，处理 compaction 跳过，处理 `branch_summary` 转 user message，处理 `custom_message` 转 user message；最终输出 `{messages, thinkingLevel, model}` | **未实现**。`messages` 表仍然是线性 history，`collectContinuationHistory` 仍按 `server_seq` 拉所有消息 | **Pi 完备，Oktapod 未实装**。Oktapod 的 LLM 上下文构造仍在用旧的 messages 表，没有走 session_entries |

### 关键证据

**Pi navigate 完整流程（`agent-session.ts:2891-3051`）**：

```ts
async navigateTree(targetId, options) {
  const oldLeafId = this.sessionManager.getLeafId();
  const { entries: entriesToSummarize, commonAncestorId } =
    collectEntriesForBranchSummary(this.sessionManager, oldLeafId, targetId);
  // 1. 触发 session_before_tree extension hook，可取消 / 提供自定义摘要
  // 2. 默认走 generateBranchSummary LLM 摘要
  // 3. 根据目标 entry 类型决定 newLeafId：
  //    - user message / custom_message: newLeafId = targetEntry.parentId（重编辑）
  //    - 其他: newLeafId = targetId
  // 4. 有摘要 -> sessionManager.branchWithSummary(newLeafId, summaryText, ...)
  //    无摘要且目标是根 -> sessionManager.resetLeaf()
  //    无摘要且非根 -> sessionManager.branch(newLeafId)
}
```

**Oktapod 000044 迁移的全部 schema 能力**：

```sql
CREATE TABLE session_entries (
    id, organization_id, project_id, conversation_id,
    parent_entry_id,           -- 树
    seq, kind, ref_id,         -- 顺序 + 类型 + 外部引用
    label, summary, namespace, payload_json, created_at
);
ALTER TABLE conversations ADD COLUMN active_leaf_entry_id;
ALTER TABLE conversations ADD COLUMN parent_session_id;
ALTER TABLE conversations ADD COLUMN forked_from_entry_id;
```

数据层具备：树（parent_entry_id）+ 当前位置（active_leaf_entry_id）+ Fork 链（parent_session_id + forked_from_entry_id）+ 5 种 entry 类型。

**Oktapod 当前完全没实装的语义点**（需要在后续 migration + Go 代码补齐）：

1. **Leaf 移动**：没有 `UPDATE conversations SET active_leaf_entry_id = ?` 的代码路径；appendMessage 时也没有把 active_leaf 推进到新 entry。
2. **Navigate**：没有「在树内跳转到指定 entry，把 leaf 移过去，新消息从新 leaf 继续」的入口。
3. **Branch Summary 生成**：没有 LLM 摘要管道；`summary` 列建了但没写入。
4. **Fork**：没有「读源 conversation 的 entries → 拷贝到新 conversation → 设置 parent_session_id + forked_from_entry_id」的服务函数。
5. **Entry 类型细分**：`config_change` 是合并类型，没有区分 `thinking_level_change` / `model_change`；缺 `label`（独立事件型）、`session_info`（重命名）、`custom_message`（注入到 LLM 的 custom）三种。
6. **上下文构造**：`collectContinuationHistory` 仍按 `server_seq` 拉 `messages` 表，没走 `session_entries` 树；因此当前 navigate 即使实装了，LLM 也看不到「沿当前 leaf 到根的路径」这个语义。
7. **Label 历史**：`label` 列只能存当前值；Pi 用独立 `LabelEntry` 保留打标签时间戳与历史。
8. **Custom 进 context 标记**：缺 `display` / `inject_into_context` 标志位。
9. **Entry 版本**：缺 `version` 列支持未来 entry schema 演进；Pi 有 `CURRENT_SESSION_VERSION = 3` + 迁移函数。

---

## 总结

| 维度 | 谁更优 | 一句话 |
|---|---|---|
| 工具清单 | **Pi 显著更全** | Oktapod 缺 read/edit/ls/find/grep/git，没有「读代码 / 改代码」原语，不是 coding agent |
| 工具 schema 质量 | **Pi 显著更好** | Pi 的「description + snippet + guidelines + 输出即下一回合指令」四层结构是工具工程的标杆；Oktapod 单层描述 + 审计导向输出 |
| 安全模型 | **Oktapod 显著更细** | Pi 是「目录级信任」；Oktapod 是「L0-L3 风险分级 + ApprovalScope + 参数哈希冻结 + 会话级规则 + 24h TTL + Full Access 时限」六层结构 |
| Session Tree | **Pi 显著更完备** | Pi 已经实装 Navigate/Branch Summary/Fork 全链路；Oktapod 只完成 000044 数据层，缺 Leaf 移动 / Navigate / Branch Summary / Fork / 上下文构造 / 6 种 entry 类型语义 |
