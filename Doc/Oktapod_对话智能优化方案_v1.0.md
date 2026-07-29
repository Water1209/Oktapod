# Oktapod 对话智能优化方案 v1.0

> 状态：规划草案 | 日期：2026-07-29 | 作者：wangdenuan

## 问题总结

同样的模型（glm-4.7-flash）在 pi、Claude Code 中表现聪明，在 Oktapod 中显得笨。典型表现：

1. 问"工作区在哪""skills 在哪个目录"-> 模型让用户自己查
2. 让创建测试文件，说"你自己定"-> 模型反复追问目录、文件名、内容
3. 扔一个压缩包让参照构建 skill -> 做不到
4. 给一个 MCP 信息让安装加载 -> 做不到
5. Skill 执行中遇到工具/脚本问题 -> 不会自己修复，直接报错给用户

**根因不是模型，是 Oktapod 的系统提示词、工具可用性和 Agent Loop 设计限制了模型的自主行动空间。**

## 与 pi / Claude Code 的关键差异

| 能力 | pi | Claude Code | Oktapod 当前 | 差距 |
|------|-----|-------------|-------------|------|
| chat 模式有工具 | ✅ 全部工具（bash/read/edit/write/grep/find/ls） | ✅ 全部工具 | ❌ 零工具，纯文本对话 | 模型无法自查任何信息 |
| 系统提示词含 cwd | ✅ `Current working directory: ...` | ✅ `CWD: ...` | ❌ 不传 | 模型不知道自己在哪 |
| 系统提示词含工具列表 | ✅ 在提示词中显式列出 | ✅ 在提示词中显式列出 | ❌ 仅通过 tool definitions 隐式传递 | 模型不知道自己能做什么 |
| 先自主再问用户 | ✅ 默认行动 | ✅ "Escalate only when genuinely stuck" | ❌ "缺少信息时调用 request_input" | 鼓励模型偷懒问用户 |
| 压缩包附件处理 | ✅ bash 工具直接 unzip | ✅ bash 工具直接 unzip | ❌ 仅 capability 安装走 ZIP，对话附件不能解压 | 用户扔的附件无法用 |
| MCP 动态安装 | ✅ `claude mcp add` | ✅ `claude mcp add` | ❌ 只能通过 UI 手动配置 | 对话中无法安装 |
| 工具失败自修复 | ✅ bash 报错 -> 模型读错误 -> 修命令重试 | ✅ "diagnose why before switching tactics" | ⚠️ 有 3 次 recovery 机制，但提示词引导不足 | 有基础设施但模型不知道该怎么做 |
| 环境信息 | ✅ uname、git 状态等 | ✅ 完整 env info | ❌ 仅 `project.Scene` | 模型对运行环境一无所知 |

## 现有 Agent Loop 恢复机制分析

Oktapod 已有的工具失败恢复基础设施（不是从零开始）：

- `toolRecoveryHint()`（worker.go:675）：失败后注入"分析错误、修订计划"的引导
- `queueToolFailureRecovery()`（store.go:6681）：自动排队恢复步骤，最多 3 次
- `isRecoverableToolExecutionFailure()`：判断错误码是否可恢复
- 恢复时把失败收据（错误码、原因）注入 continuation instruction

**问题不在基础设施，在提示词引导**：
1. 恢复提示词只说"分析错误、修订计划"，没说"你可以修改文件、修脚本、换工具"
2. 系统提示词没有"遇到工具失败先自己诊断修复"的引导
3. Skill 执行失败后没有明确告诉模型"你可以 read_file 看脚本内容、edit_file 修复、重试"

## 优化方案

分五个层次，按优先级排序：

### 第一层：让 chat 模式有只读工具（P0，立竿见影）

**目标**：用户问"工作区在哪""skills 在哪""项目状态"时，模型能自己查，不让用户代劳。

**改动点**：

1. `service.go` - `answerProjectTurnStream` / `answerProjectTurn`：给 chat 模式传入只读工具定义
2. `service.go` - `projectTurnAnswerPrompt`：系统提示词加入环境上下文
3. `worker.go` - 新增 chat-only 的工具执行路径（只读，不走 task/plan 流程）

**chat 模式开放的工具（只读，无副作用）**：

| 工具 | 用途 |
|------|------|
| `workspace_list_files` | 列出工作区目录结构 |
| `workspace_read_file` | 读取文件内容 |
| `workspace_find_files` | 按 glob 查找文件 |
| `workspace_grep` | 搜索文件内容 |
| `git_status` | 查看 Git 状态 |
| `git_log` | 查看提交历史 |

**系统提示词改造**（`projectTurnAnswerPrompt`）：

```
You are the assistant inside a private Oktapod project conversation.
You help users by answering questions, exploring the workspace, and explaining code.
Use the available read-only tools to investigate before answering - do not ask the user
to look things up when you can check yourself.

Current working directory: {workspaceRoot}
Project: {projectName}
Scene: {project.Scene}
Model: {modelName}

Do not claim to have changed files or run commands. If execution or file modification
is needed, tell the user to send it as a task.
```

**关键设计**：chat 模式的工具调用走轻量路径，不创建 Task/Run/Plan，直接执行后把结果喂回模型继续回复。最多允许 3 轮工具调用，防止无限循环。

```
用户消息 -> 模型回复（含工具调用）-> 执行只读工具 -> 结果喂回模型 -> 模型继续回复 -> 流式输出
```

### 第二层：渐进式上下文注入（P1）

**目标**：模型在回答问题时有足够的背景信息，但不一次性灌满上下文。

**设计原则**：像 Claude Code 那样，系统提示词始终包含基础环境信息；详细内容按需加载。

#### 始终注入（系统提示词末尾，~200 tokens）

```
# Environment
Workspace root: {workspaceRoot}
Skills directory: {skillsRoot}
Project: {projectName} ({projectKind})
Git branch: {currentBranch}
Active tasks: {activeTaskCount}
```

#### 按需加载（模型主动查询）

| 信息 | 触发方式 | 实现 |
|------|---------|------|
| 工作区文件结构 | `workspace_list_files` 工具 | 已有，chat 模式开放 |
| Skills 列表 | 新增 `list_skills` 只读工具 | 扫描 skills 目录，返回名称+描述 |
| 项目任务状态 | 新增 `list_tasks` 只读工具 | 查询 DB，返回活跃任务摘要 |
| Provider 配置 | 新增 `list_providers` 只读工具 | 返回 provider 名称和状态（不含密钥） |
| 系统诊断 | 新增 `system_diagnostics` 只读工具 | 返回数据库大小、磁盘、运行时长 |

#### 系统提示词中的引导

```
# Available read-only tools for investigation
You have read-only tools to explore the environment. Use them proactively:
- workspace_list_files / workspace_read_file: explore project files
- list_skills: check available skills and their locations
- list_tasks: check current task execution status
- system_diagnostics: check system health

Do not ask the user for information you can look up yourself.
```

### 第三层：Task 模式自主决策引导（P1）

**目标**：用户说"创建测试文件，你自己定"时，模型自主决定文件名、目录、内容，不反复追问。

#### 系统提示词改造（`prompts()` 函数，`worker.go:1652`）

在 `ModeSingleAgent` 的系统提示词中加入：

```
自主决策规则：
- 对于常规操作（创建文件、选择目录、命名变量），当用户未指定细节时，自主选择合理的默认值并执行，
  不要因为细节未指定就暂停询问用户。
- 仅在以下情况调用 request_input：
  1. 缺少的信息会导致不可逆的破坏性操作（如删除、覆盖已有文件）
  2. 存在多个语义完全不同的合理理解，且选错代价很大
  3. 用户明确要求确认
- "创建测试文件""写个示例""随便生成一个"这类请求，模型应自主决定全部细节并直接执行。
```

#### `request_input` 工具描述改造

当前：
```
Pause the current plan at a checkpoint and ask the user for required structured input.
Use only when execution cannot safely continue without the answer.
```

改为：
```
Ask the user for input only when genuinely stuck after exhausting reasonable defaults.
Do NOT use this for: file names, directory paths, file content, test data,
configuration values - choose sensible defaults and proceed.
Use ONLY for: ambiguous intent that could lead to destructive or irreversible actions,
or when the user's request has multiple fundamentally different interpretations.
```

#### Agent Loop 行为约束

在 `worker.go` 的 continuation instruction 中加入：

```
如果上一轮调用了 request_input 且用户回复了补充信息，本轮必须基于该信息继续执行，
不得再次调用 request_input 追问同一问题的不同细节。
```

### 第四层：工具失败自修复（P1）

**目标**：Skill 执行或工具调用遇到脚本错误、路径不存在、依赖缺失等问题时，模型自己诊断、修复、重试，而非直接报错给用户。

**现有机制**（已有，需增强）：
- `toolRecoveryHint()`：失败后注入引导，最多 3 次恢复
- `queueToolFailureRecovery()`：自动排队恢复步骤
- 恢复时把失败收据注入 continuation instruction

**需要改造的部分**：

#### 4a. 增强恢复提示词（`toolRecoveryHint()`，worker.go:675）

当前：
```
工具失败恢复 1/3：xxx 执行失败（原因）。请分析错误、修订计划并选择更合适的参数或替代工具；
不要重复已经成功的步骤，也不要原样重试同一个失败动作。
```

改为：
```
工具失败恢复 1/3：xxx 执行失败（原因）。你可以：
1. 用 workspace_read_file 读取相关脚本/配置文件，定位错误根因
2. 用 workspace_edit_file 修复脚本中的问题（语法错误、路径错误、依赖缺失）
3. 用 workspace_write_file 创建缺失的依赖文件或配置
4. 用 workspace_run_command 安装缺失依赖或运行验证
5. 换用不同参数或替代工具重新尝试
不要原样重试同一个失败动作；先诊断根因再修复。
```

#### 4b. 系统提示词加入自修复引导

在 `prompts()` 的 `ModeSingleAgent` 系统提示词中加入：

```
工具失败处理规则：
- 工具执行返回错误时，先读取错误信息，分析根因
- 如果是脚本/配置文件问题，用 workspace_read_file 查看内容，用 workspace_edit_file 修复后重试
- 如果是依赖缺失，用 workspace_run_command 安装后重试
- 如果是路径错误，检查工作区结构，修正路径后重试
- 不要因为一次失败就放弃或直接报告用户；先尝试诊断和修复
- 3 次恢复尝试后仍失败，再向用户报告具体错误和已尝试的修复
```

#### 4c. Skill 执行失败的特殊处理

Skill 本质是一组指令文本，执行时可能引用不存在的文件、脚本或工具。当 skill_invoke 或 skill 中的工具调用失败时：

```
Skill 执行失败恢复：
- Skill 指令可能引用了不存在或需修改的脚本/文件
- 用 workspace_read_file 读取 Skill 指令中引用的文件，确认是否存在
- 如果文件不存在但有类似文件，用 workspace_find_files 查找正确路径
- 如果脚本有语法或逻辑错误，用 workspace_edit_file 修复
- 修复后重新执行失败的工具调用
```

#### 4d. 恢复次数从 3 提升到 5

当前 3 次恢复对复杂修复场景（读文件->定位->修文件->重试->再修）不够。提升到 5 次，但保持单次恢复的 token 预算不变。

### 第五层：附件处理与动态能力安装（P2）

#### 5a. 对话附件解压与读取

**目标**：用户在对话中扔一个 ZIP 压缩包，模型能解压、读取内容、参照构建。

**方案**：给 chat 和 task 模式都开放 `workspace_extract_archive` 工具。

```go
// 新增工具：workspace_extract_archive
// 参数：archive_path（工作区内已有的 zip 路径）, dest_dir（解压目标目录）
// 安全约束：
//   - 仅接受 .zip 格式
//   - 解压到工作区内指定目录，不可逃逸
//   - 复用 capability_bundle.go 的安全校验逻辑（路径穿越、炸弹检测）
```

**对话流程**：
1. 用户上传 ZIP 附件 -> 存入工作区 `uploads/` 目录
2. 用户说"参照这个压缩包帮我构建 skill"
3. 模型调用 `workspace_extract_archive` 解压到临时目录
4. 模型调用 `workspace_read_file` / `workspace_list_files` 读取内容
5. 模型基于内容执行用户目标（创建文件、生成代码等）

#### 5b. 对话中安装 MCP

**目标**：用户在对话中给一个 MCP server 信息，模型能安装、连接、加载工具。

**方案**：新增 `install_mcp` 工具（task 模式，需审批）。

```go
// 新增工具：install_mcp
// 参数：name, transport (stdio|http|sse), command_or_url, args[], env{}
// 安全约束：
//   - 标记为高风险，需用户审批
//   - 安装后自动连接并加载工具定义
//   - 工具列表注入下一轮模型调用的 tool_definitions
```

**对话流程**：
1. 用户说"帮我安装这个 MCP: npx my-mcp-server"
2. 模型调用 `install_mcp`（触发审批）
3. 用户审批后，系统安装并连接 MCP
4. MCP 的工具自动注入模型的工具列表
5. 模型可以调用新 MCP 工具继续执行
6. 如果 MCP 连接失败，模型自诊断（参照第四层）

#### 5c. 对话中安装 Skill

**目标**：用户在对话中扔一个 SKILL.md 或 ZIP 包，模型能安装为 capability。

**方案**：新增 `install_skill` 工具（task 模式，需审批）。

```go
// 新增工具：install_skill
// 参数：source_path（工作区内文件路径）或 archive_path（ZIP 路径）
// 安全约束：
//   - 复用 capability_bundle.go 的校验逻辑
//   - 标记为中等风险，需用户审批
//   - 安装后 skill 元数据注入下一轮的 skill_catalog
```

## 实施路线

### 阶段一（P0，预计 2-3 天）

- [ ] chat 模式开放只读工具（`workspace_list_files`, `workspace_read_file`, `workspace_find_files`, `workspace_grep`）
- [ ] chat 模式新增轻量工具执行路径（不创建 Task/Run，最多 3 轮）
- [ ] 系统提示词加入 cwd、project 信息
- [ ] 验证：问"工作区在哪""列出文件"能直接回答

### 阶段二（P1，预计 3-4 天）

- [ ] 系统提示词加入环境上下文（workspace root、skills path、active tasks）
- [ ] 新增 `list_skills`、`list_tasks`、`system_diagnostics` 只读工具
- [ ] Task 模式系统提示词加入自主决策引导
- [ ] `request_input` 工具描述和 Agent Loop 约束改造
- [ ] 验证：说"创建测试文件，你自己定"能直接执行

### 阶段三（P1，预计 2-3 天）

- [ ] 增强 `toolRecoveryHint()` 提示词，明确告诉模型可以 read/edit/write 修复
- [ ] 系统提示词加入工具失败自修复引导
- [ ] Skill 执行失败的特殊恢复提示词
- [ ] 恢复次数从 3 提升到 5
- [ ] 验证：skill 执行中脚本报错，模型能自己读取、修复、重试

### 阶段四（P2，预计 4-5 天）

- [ ] 新增 `workspace_extract_archive` 工具
- [ ] 对话附件上传存入工作区 `uploads/`
- [ ] 新增 `install_mcp` 工具（含审批流程）
- [ ] 新增 `install_skill` 工具（含审批流程）
- [ ] 验证：扔 ZIP 附件能解压读取；给 MCP 信息能安装加载

## 验收标准

| 场景 | 预期行为 | 对标 |
|------|---------|------|
| "工作区在哪个路径" | 模型直接回答路径，不让用户自查 | pi ✅ CC ✅ |
| "skills 安装在哪个目录" | 模型调用 list_skills 或直接回答 | pi ✅ CC ✅ |
| "当前有什么任务在跑" | 模型调用 list_tasks 并汇总 | N/A |
| "创建一个测试文件，你自己定" | 模型自主选择路径/文件名/内容并执行 | pi ✅ CC ✅ |
| Skill 执行中脚本报错 | 模型读取脚本、定位错误、修复后重试 | pi ✅ CC ✅ |
| 工具因依赖缺失失败 | 模型安装依赖后重试 | pi ✅ CC ✅ |
| 扔 ZIP 附件 + "参照构建 skill" | 模型解压、读取、参照执行 | pi ✅ CC ✅ |
| "帮我安装这个 MCP" | 模型调用 install_mcp，审批后加载 | CC ✅ |

## 关键文件索引

| 改动位置 | 文件 | 说明 |
|---------|------|------|
| chat 系统提示词 | `Src/internal/application/service.go:1053` | `projectTurnAnswerPrompt()` |
| chat 模型调用 | `Src/internal/application/service.go:1015` | `answerProjectTurnStream()` |
| task 系统提示词 | `Src/internal/runtime/worker.go:1652` | `prompts()` |
| task continuation | `Src/internal/runtime/worker.go:354` | continuation instruction |
| 工具恢复提示词 | `Src/internal/runtime/worker.go:675` | `toolRecoveryHint()` |
| request_input 定义 | `Src/internal/domain/core/tool_registry.go:38` | ToolSpec |
| 工具定义 | `Src/internal/domain/core/tool_registry.go:13` | `BuiltinToolSpecs()` |
| 工具执行 | `Src/internal/adapters/workspace/local.go` | `Local.Execute()` |
| Agent Loop 主循环 | `Src/internal/runtime/worker.go:95` | `Worker.Run()` |
| 恢复排队 | `Src/internal/adapters/db/sqlite/store.go:6681` | `queueToolFailureRecovery()` |
