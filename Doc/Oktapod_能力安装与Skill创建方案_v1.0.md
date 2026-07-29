# Oktapod 能力安装与 Skill 创建方案 v1.0

> 状态：规划 | 日期：2026-07-29

## 核心设计决策

**不走"新工具"路线，走"内置 Skill + CLI 子命令"路线。**

参照 Claude Code / Codex / WorkBuddy 的统一模式：

| 产品 | 安装 Skill | 安装 MCP | 创建 Skill |
|------|-----------|---------|-----------|
| Claude Code | Bash `git clone` → 文件系统扫描 | `claude mcp add` CLI | Write 工具写 SKILL.md |
| Codex | 当任务执行，模型自主检索+确认 | 当任务执行 | 当任务执行 |
| WorkBuddy | plugin marketplace 安装 | plugin 配置 | skill-creator 技能+脚本 |

**共同点**：系统 API 能力不暴露给模型，而是封装成 CLI 子命令；Skill 指令文本引导模型调用 CLI 或 API。

## Oktapod 的实现方案

### 整体架构

```
用户说"帮我安装 MCP xxx"
    ↓
auto 分类 → task 路由（因为是"安装"动作）
    ↓
skill_catalog 中匹配到内置 skill "install-mcp"
    ↓
模型调用 skill_invoke("install-mcp", args="...")
    ↓
skill 指令加载到上下文，引导模型：
  1. 解析用户意图（name/transport/url）
  2. 调用 workspace_run_command 执行 CLI：
     oktapod capability install-mcp --name xxx --transport stdio --command "npx ..."
  3. CLI 内部调用 Service.CreateCapability + ConnectMCP
  4. 模型读取 CLI 输出确认安装结果
    ↓
安装完成，下一轮 MCP 工具自动可用
```

### 三个内置 Skill

#### 1. install-mcp（安装 MCP Server）

**Skill 指令**（SKILL.md 格式，存储在代码中作为内置 capability）：

```yaml
---
name: install-mcp
description: Install and connect an MCP server. Use when the user asks to install, add, or set up an MCP server, or provides an MCP server URL/command.
---

# Install MCP Server

When the user asks to install an MCP server, follow these steps:

## 1. Parse Intent
Extract from the user's message:
- **name**: A short identifier for the server (e.g. "sentry", "my-tools")
- **transport**: "stdio" (default), "http", or "sse"
- **command_or_url**: The command (e.g. "npx my-mcp-server") or URL (e.g. "https://mcp.example.com/mcp")
- **args**: Additional arguments (optional)
- **env**: Environment variables (optional)

If any required field is missing, ask the user. If the user gives a GitHub URL,
assume stdio transport and use `npx` with the package name.

## 2. Confirm with User
Present the parsed configuration and ask for confirmation before installing.

## 3. Execute Installation
Call workspace_run_command with:
  program: oktapod
  args: ["capability", "install-mcp", "--name", "<name>", "--transport", "<transport>",
        "--command", "<command_or_url>", "--project", "<project_id>"]

## 4. Verify
Read the CLI output. If installation succeeded, inform the user that MCP tools
will be available in the next turn. If it failed, diagnose the error and attempt
to fix it (e.g. wrong URL, missing dependency).
```

#### 2. install-skill（从 GitHub/URL 安装 Skill）

```yaml
---
name: install-skill
description: Install a Skill from a GitHub URL, ZIP archive, or SKILL.md content. Use when the user asks to install, add, or download a skill from a URL or repository.
---

# Install Skill

## From GitHub URL
1. Parse the URL to determine the repo and skill path
2. Call workspace_run_command:
   program: oktapod
   args: ["capability", "install-skill", "--source-url", "<github_url>", "--project", "<project_id>"]
3. The CLI downloads, validates, and registers the skill
4. Read output to confirm

## From ZIP attachment
1. The attachment is already in the workspace uploads/ directory
2. Call workspace_run_command:
   program: oktapod
   args: ["capability", "install-skill", "--source-path", "uploads/xxx.zip", "--project", "<project_id>"]

## From inline SKILL.md content
1. Use workspace_write_file to write the SKILL.md to a temp path
2. Call workspace_run_command:
   program: oktapod
   args: ["capability", "install-skill", "--source-path", "tmp/skill.md", "--project", "<project_id>"]

## After Installation
Inform the user the skill is available. The skill will appear in the skill
catalog for the next turn. If installation fails, read the error and attempt
to fix it.
```

#### 3. create-skill（创建新 Skill）

```yaml
---
name: create-skill
description: Create a new skill from a description. Use when the user asks to create, build, or make a new skill.
---

# Create Skill

## 1. Capture Intent
Ask the user:
- What should this skill do?
- When should it trigger?
- Expected output format?

## 2. Write SKILL.md
Use workspace_write_file to create a draft SKILL.md with:
- YAML frontmatter (name, description)
- Markdown instructions
- Reference scripts if needed

## 3. Register
Call workspace_run_command:
  program: oktapod
  args: ["capability", "install-skill", "--source-path", "<path>", "--project", "<project_id>"]

## 4. Iterate
Offer to test and refine the skill based on the user's feedback.
```

### 新增 CLI 子命令

在 `Src/cmd/oktapod/main.go` 中新增 `capability` 子命令：

```
oktapod capability install-mcp --name <name> --transport <stdio|http|sse> --command <cmd> [--project <id>]
oktapod capability install-skill --source-url <url> | --source-path <path> [--project <id>] [--name <name>]
```

**实现逻辑**：
- `install-mcp`：调用 `Service.CreateCapability`（kind=mcp）+ `Service.ConnectMCP`
- `install-skill`：
  - `--source-url`：HTTP 下载 ZIP 或 SKILL.md → 调用 `Service.CreateCapability`（kind=skill）
  - `--source-path`：读取本地文件 → 调用 `Service.CreateCapability`（kind=skill）
- 输出 JSON 结果（success/failure + capability id + name）

### 内置 Skill 注入机制

在 `Service` 的 task 创建流程中（`configSnapshot` 构建时），自动注入三个内置 skill：

```go
// 在 service.go 的 configSnapshot 构建逻辑中
func builtinSkillCapabilities() []map[string]any {
    return []map[string]any{
        {"id": "builtin:install-mcp", "name": "install-mcp", "version": "1.0.0",
         "content_hash": "builtin", "manifest_json": installMCPSkillManifest},
        {"id": "builtin:install-skill", "name": "install-skill", "version": "1.0.0",
         "content_hash": "builtin", "manifest_json": installSkillSkillManifest},
        {"id": "builtin:create-skill", "name": "create-skill", "version": "1.0.0",
         "content_hash": "builtin", "manifest_json": createSkillSkillManifest},
    }
}
```

这些内置 skill 始终出现在 `skill_catalog` 中，模型可以像调用其他 skill 一样 `skill_invoke` 它们。

### Skill 自带脚本的支持

参照 skill-creator 的 `scripts/` 目录模式，Oktapod 的 skill ZIP 包可以包含 `scripts/` 目录。Skill 指令中引用脚本时，模型通过 `workspace_run_command` 执行：

```
SKILL.md 中写：
"运行 scripts/validate.py 来验证输出"
模型读取到这段指令后，调用：
  workspace_run_command("python", ["scripts/validate.py", ...])
```

Oktapod 已有的 `workspace_run_command` 工具（task 模式）支持这个，不需要额外改动。

## 实施步骤

### 步骤 1：新增 CLI 子命令（1 天）

- `Src/cmd/oktapod/capability_command.go`：新增 `capability install-mcp` 和 `capability install-skill` 子命令
- 复用 `Service.CreateCapability` 和 `Service.ConnectMCP`
- `--source-url` 支持 HTTP 下载 ZIP/SKILL.md
- 输出 JSON 结果

### 步骤 2：注入内置 Skill（0.5 天）

- `Src/internal/application/builtin_skills.go`：定义三个内置 skill 的 SKILL.md 内容
- `Src/internal/application/service.go`：在 configSnapshot 构建时注入内置 skill
- 确保 `skillContextForPrompt` 能正确匹配和加载内置 skill

### 步骤 3：验证（0.5 天）

- 对话中说"帮我安装 MCP: npx my-server" → 模型调用 install-mcp skill → 执行 CLI → 安装成功
- 对话中给 GitHub URL 安装 skill → 模型调用 install-skill skill → CLI 下载安装
- 对话中说"创建一个技能" → 模型调用 create-skill skill → 写 SKILL.md → 注册

## 关键文件

| 文件 | 说明 |
|------|------|
| `Src/cmd/oktapod/capability_command.go` | 新增 CLI 子命令 |
| `Src/cmd/oktapod/main.go` | 注册 capability 子命令 |
| `Src/internal/application/builtin_skills.go` | 内置 skill 定义 |
| `Src/internal/application/service.go` | configSnapshot 注入内置 skill |
