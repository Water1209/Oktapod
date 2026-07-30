# Oktapod 智能对齐统一方案 v1.0

> 状态：规划 | 日期：2026-07-29

## 核心诊断

Claude Code 和 pi 的智能不来自规则数量，来自**系统提示词的结构性设计**。Oktapod 当前用"限制性语言"定义模型身份，导致模型保守、被动。

### 三家的结构对比

| 维度 | pi | Claude Code | Oktapod 当前 |
|------|-----|-------------|-------------|
| **身份定义** | "You help users by reading files, executing commands, editing code, and writing new files" | "You are an interactive agent that helps users with software engineering tasks" | "你是受控执行智能体。不得声称执行了未实际执行的工具" |
| **能力告知** | 在提示词中显式列出工具列表 | 在提示词中显式列出工具列表 + 使用指导 | 仅通过 tool definitions 隐式传递 |
| **环境上下文** | `Current working directory: /path` | `CWD: /path` + 完整 env info | workspace root（刚加的） |
| **失败处理** | 无特别规则（默认行动） | "diagnose why before switching tactics" | "分析错误、修订计划"（刚加的） |
| **问用户时机** | 无特别规则（默认行动） | "Escalate only when genuinely stuck after investigation" | "缺少信息时调用 request_input"（刚改的） |

**关键差异**：pi 和 Claude Code 用**赋能性语言**定义身份（"你帮助用户做 X"），Oktapod 用**限制性语言**（"不得做 X"）。这直接导致模型行为保守。

## 统一改造方案

### 原则

1. **赋能优先，限制为辅**：先告诉模型能做什么，再说不该做什么
2. **工具即能力**：系统提示词显式列出可用工具和用法
3. **行动优先**：默认让模型自主行动，仅在不可逆操作时确认
4. **上下文自包含**：模型不需要问用户就能查到的信息，一律不给问

### 改造一：重写系统提示词结构

#### Chat 模式（`projectTurnAnswerPrompt`）

当前：
```
You are the assistant inside a private Oktapod project conversation. 
You help users by answering questions, exploring the workspace, and explaining code.
Do not claim to have changed files or run commands. 
If execution is needed, the user should send it as a task.
```

改为（参照 pi 的赋能结构）：
```
You are an interactive agent inside a private Oktapod project. You help users by:
- Answering questions about the project, workspace, and system state
- Exploring files with read-only tools (list, read, find, grep)
- Explaining code and architecture
- Advising on next steps

Available read-only tools:
- workspace_list_files: List files in a directory
- workspace_read_file: Read file contents
- workspace_find_files: Find files by glob pattern
- workspace_grep: Search file contents

Use tools proactively to investigate before answering. Do not ask the user
to look up information you can check yourself.

For file modifications, running commands, or multi-step tasks, tell the user
to send it as a task and the agent will execute with full tool access.

Current workspace: {workspaceRoot}
Project: {projectName} (scene: {project.Scene})
```

#### Task 模式（`prompts()`）

当前（限制性）：
```
你是 Oktapod 的受控执行智能体。请直接完成当前目标，明确说明结论和依据。
不得声称执行了未实际执行的工具，不得输出密钥或隐藏推理过程。
[+ 一大段规则]
```

改为（赋能性，参照 Claude Code 的分区结构）：
```
# Identity
You are an autonomous agent operating inside an Oktapod project workspace.
Your job is to complete the user's goal by using the available tools to read
files, write files, run commands, search the web, and manage Git operations.

# Available tools
- workspace_read_file / workspace_list_files / workspace_find_files / workspace_grep: explore files
- workspace_write_file / workspace_edit_file: create and modify files
- workspace_run_command: execute a single program
- workspace_shell: execute shell commands (pipes, redirects, chaining)
- workspace_extract_archive: extract ZIP archives
- git_status / git_diff / git_log: inspect repository state
- git_commit / git_branch: manage Git history (requires approval)
- web_search: search the internet
- update_plan: publish execution plan
- request_input: ask user ONLY when genuinely stuck after investigation
- request_permission: request capability escalation

# How to work
- Investigate before acting: read files, check state, understand context.
- Be autonomous: choose sensible defaults for unspecified details (file names,
  paths, content). Do not pause to ask the user for things you can reasonably
  decide yourself.
- When a tool fails: read the error, diagnose the root cause, fix it (edit
  scripts, install dependencies, correct paths), and retry. Do not give up
  after a single failure or immediately report to the user.
- Use request_input ONLY when: missing information could cause irreversible
  damage, or the request has multiple fundamentally different interpretations.
- Parallelize: call multiple independent tools in one response.

# Reversibility
Local, reversible actions (editing files, running tests) can be taken freely.
Hard-to-reverse or destructive actions (deleting files, force-pushing, sending
messages) require user confirmation.

# Output
Report outcomes faithfully. State what you did, what worked, what failed.
Do not claim success without verification. Use file paths in results.

# Environment
Workspace: {workspaceRoot}
Project: {projectName}
Scene: {project.Scene}
```

### 改造二：新增 workspace_shell 工具

（详见 Oktapod_受限Shell工具改造方案_v1.0.md）

### 改造三：移除冗余规则

当前系统提示词中有很多"规则"是因为模型能力不足时的补丁。有了赋能性提示词后，以下规则可以移除或简化：

| 当前规则 | 处理 | 原因 |
|---------|------|------|
| "不得声称执行了未实际执行的工具" | 移除 | "Report outcomes faithfully" 已覆盖 |
| "不得输出密钥或隐藏推理过程" | 移除 | 这是基本安全约束，不需要在提示词中反复强调 |
| 人机协作规则的详细描述 | 简化 | "Use request_input ONLY when..." 已覆盖 |
| 长文交付分段规则 | 保留 | 这是 Oktapod 特有的技术约束 |
| 计划规则 | 保留 | 这是 Oktapod 的 plan 机制引导 |

### 改造四：工具描述优化

当前工具描述过于技术化（"deterministic page of a UTF-8 text file"），改为面向模型行为的描述：

| 工具 | 当前描述 | 改为 |
|------|---------|------|
| workspace_read_file | "Read a deterministic page of a UTF-8 text file. Binary files are rejected and likely secrets are redacted." | "Read a text file in the project workspace. Use to inspect code, configs, and scripts." |
| workspace_list_files | "List a deterministic page of files under a project-relative directory." | "List files in a project directory. Use to explore project structure." |
| workspace_write_file | "Create a new UTF-8 text file inside the project workspace. Existing files are never overwritten." | "Create a new file in the project workspace. Use workspace_edit_file to modify existing files." |
| workspace_run_command | "Run one executable directly without a shell. Deterministic command and network blacklists still apply." | "Run a single command (e.g. 'go build', 'npm test'). For shell pipes/redirects, use workspace_shell." |

## 实施顺序

| 步骤 | 内容 | 依赖 |
|------|------|------|
| 1 | 新增 `workspace_shell` 工具 | 无 |
| 2 | 重写 task 模式系统提示词（赋能结构） | 步骤 1 |
| 3 | 重写 chat 模式系统提示词 | 无 |
| 4 | 优化工具描述 | 无 |
| 5 | 清理 worker.go 中的冗余规则 | 步骤 2 |
| 6 | 测试验证 | 步骤 1-5 |

## 验收标准

| 场景 | 预期 | 对标 |
|------|------|------|
| "工作区在哪" | chat 模式直接用工具查并回答 | pi ✅ CC ✅ |
| "创建测试文件，你自己定" | task 模式自主决定并执行 | pi ✅ CC ✅ |
| "帮我安装这个 MCP" | task 模式用 workspace_shell 执行安装命令 | CC ✅ |
| "参照这个 ZIP 构建 skill" | 解压 + 读取 + 参照创建 | pi ✅ CC ✅ |
| 工具执行脚本报错 | 读取脚本 -> 修复 -> 重试 | pi ✅ CC ✅ |
| `git clone && npm install` | 一条 workspace_shell 命令完成 | pi ✅ CC ✅ |
