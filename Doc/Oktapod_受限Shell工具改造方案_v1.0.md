# Oktapod 受限 Shell 工具改造方案 v1.0

> 状态：规划 | 日期：2026-07-29

## 问题

Oktapod 只有 `workspace_run_command`（program+args，无 shell），没有 Bash。导致模型无法执行 `git clone xxx && cd xxx && npm install`、`curl -sL url | sh`、`unzip x.zip -d /tmp/xxx` 等复合命令。这是 Oktapod 比 Claude Code / pi 笨的核心原因之一。

## 设计目标

给 Oktapod 加一个**受限 Shell 工具** `workspace_shell`，支持完整 shell 语法（管道、重定向、命令链、变量展开），但有安全约束：

- 工作区目录沙箱
- 命令黑名单（破坏性命令）
- 网络命令限制
- 超时控制
- 输出大小限制
- 审批机制（走已有 scene policy + permission mode）

## 与现有工具的关系

| 工具 | 定位 | 何时用 |
|------|------|--------|
| `workspace_run_command` | 精确执行单个程序，无 shell | 已有，保留。适合 `go build`、`npm test` |
| `workspace_shell` | 受限 shell，支持管道/重定向/命令链 | 新增。适合 `git clone && npm install`、`curl | sh`、`unzip` |

两个工具共存，模型根据需要选择。`workspace_shell` 的 scene policy 与 `workspace_run_command` 相同（scene=code 时可用，scene=work 时需 `request_permission`）。

## 安全约束设计（参照 Claude Code）

### 1. 工作区沙箱

```
- command.Dir = projectRoot（项目工作区）
- 环境变量白名单（复用 safeEnvironment）
- HOME 设为工作区内的 .oktapod/runtime-cache
- PATH 白名单（系统 PATH，但移除危险路径）
```

### 2. 命令黑名单

参照 Claude Code 的 `bashSecurity.ts` 和 Oktapod 已有的安全设计：

```go
var shellCommandBlacklist = map[string]bool{
    // 破坏性命令
    "rm":       true,  // 允许但限制路径（不能 rm -rf /、rm -rf ..）
    "rmdir":    true,
    "mkfs":     true,
    "dd":       true,
    "shred":    true,
    // 系统修改
    "chmod":    true,
    "chown":    true,
    "mount":    true,
    "umount":   true,
    // 网络攻击面
    "nc":       true,
    "ncat":     true,
    "telnet":   true,
    // 进程管理
    "kill":     true,
    "killall":  true,
    "pkill":    true,
    // 包管理（需审批）
    "apt":      true,
    "yum":      true,
    "brew":     true,
}
```

**注意**：黑名单不是安全边界，审批机制才是。黑名单只是防止模型意外执行破坏性命令。

### 3. 路径安全

- 解析命令中的路径参数，拒绝逃逸工作区的路径
- 禁止 `rm -rf /`、`rm -rf ~`、`rm -rf *` 等危险删除
- 禁止写入工作区外的路径
- 参照 Claude Code 的 `isDangerousRemovalPath()`

### 4. 危险模式检测

参照 Claude Code 的 `COMMAND_SUBSTITUTION_PATTERNS`：

```go
var dangerousPatterns = []string{
    "$(",   // 命令替换
    "${",   // 参数替换
    "<(",   // 进程替换
    ">(",   // 进程替换
}
```

**不是阻止这些模式**（shell 语法需要它们），而是标记为需要审批。

### 5. 超时和输出限制

```
- 超时：5 分钟（复用现有 workspace_run_command 的超时）
- stdout/stderr 各 1MB 上限（复用 boundedBuffer）
- 进程树终止（复用 terminateProcessTree）
```

### 6. Scene Policy

`workspace_shell` 与 `workspace_run_command` 共享 scene policy：
- `scene=code`：直接可用
- `scene=work`：需 `request_permission` 升级
- 审批走已有的 human action review 流程

## 实现计划

### 文件改动

| 文件 | 改动 |
|------|------|
| `Src/internal/domain/core/types.go` | 新增 `ToolWorkspaceShell` 常量 |
| `Src/internal/domain/core/tool_registry.go` | 新增 `workspace_shell` 工具定义 |
| `Src/internal/adapters/workspace/local.go` | 新增 `executeShell()` 方法 |
| `Src/internal/runtime/worker.go` | `applySceneToolPolicy` 加入 `workspace_shell` |
| `Src/internal/runtime/tools.go` | `isWorkspaceTool` 加入 `workspace_shell` |

### 工具定义

```go
{
    Name: ToolWorkspaceShell,
    Description: "Execute a shell command in the project workspace. Supports pipes, redirects, command chaining, and variable expansion. Use for complex operations like 'git clone && npm install', 'curl | sh', 'unzip x.zip -d dir'. Destructive commands require approval.",
    Sequential: true,
    Parameters: objectSchema([]string{"command"}, map[string]any{
        "command": map[string]any{"type": "string", "minLength": 1, "maxLength": 10000},
        "timeout": map[string]any{"type": "integer", "minimum": 1, "maximum": 300},
    }),
}
```

### executeShell 实现

```go
func (l *Local) executeShell(ctx context.Context, projectRoot string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
    command := call.Arguments["command"].(string)

    // 1. 危险路径检测
    if err := validateShellCommand(command); err != nil {
        return core.ToolExecution{Status: core.StatusFailed, ErrorCode: "SHELL_COMMAND_DENIED", ...}
    }

    // 2. 构建命令
    shellCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
    defer cancel()
    cmd := exec.CommandContext(shellCtx, "bash", "-c", command)
    // Windows: exec.CommandContext(shellCtx, "cmd", "/c", command)
    configureCommand(cmd)
    cmd.Cancel = func() error { return terminateProcessTree(cmd) }
    cmd.WaitDelay = 2 * time.Second
    cmd.Dir = projectRoot

    // 3. 安全环境
    env, err := safeEnvironment(projectRoot)
    if err != nil { ... }
    cmd.Env = env

    // 4. 执行
    stdout := &boundedBuffer{limit: maxCommandOutput}
    stderr := &boundedBuffer{limit: maxCommandOutput}
    cmd.Stdout = stdout
    cmd.Stderr = stderr
    err = cmd.Run()

    // 5. 返回结果
    return core.ToolExecution{
        Output: map[string]any{
            "command": command,
            "stdout": stdout.String(),
            "stderr": stderr.String(),
            "exit_code": cmd.ProcessState.ExitCode(),
        },
    }
}
```

### validateShellCommand

```go
func validateShellCommand(command string) error {
    // 1. 检查危险删除
    if isDangerousRemoval(command) {
        return fmt.Errorf("destructive rm command detected")
    }
    // 2. 检查路径逃逸
    if containsPathEscape(command) {
        return fmt.Errorf("command attempts to escape workspace")
    }
    // 3. 检查黑名单命令（仅标记，不阻止 -- 审批机制处理）
    // 黑名单命令在 scene policy 层面处理
    return nil
}

func isDangerousRemoval(command string) bool {
    lower := strings.ToLower(command)
    patterns := []string{
        "rm -rf /", "rm -rf ~", "rm -rf *", "rm -rf ..",
        "rm -rf $HOME", "rm -rf $PWD",
        "mkfs", "dd if=", "> /dev/sd",
    }
    for _, p := range patterns {
        if strings.Contains(lower, p) {
            return true
        }
    }
    return false
}
```

### Chat 模式开放

`workspace_shell` 仅在 task 模式可用。Chat 模式保持只读工具，不开放 shell。

### 系统提示词更新

在 `prompts()` 的 `ModeSingleAgent` 中加入：

```
需要执行复合 shell 命令（管道、重定向、命令链）时，使用 workspace_shell 工具。
例如：git clone xxx && npm install、curl -sL url | sh、unzip x.zip -d dir。
单条简单命令仍可用 workspace_run_command。破坏性命令会触发审批。
```

## 验收标准

| 场景 | 预期行为 |
|------|---------|
| `git clone https://github.com/xxx/skill.git skills/my-skill && cd skills/my-skill && npm install` | 一条命令完成 |
| `curl -sL https://raw.githubusercontent.com/xxx/main/SKILL.md -o skills/my-skill/SKILL.md` | 下载文件 |
| `unzip uploads/skill.zip -d skills/my-skill` | 解压到指定目录 |
| `rm -rf /` | 被拒绝，返回错误 |
| `cat file | grep "pattern" | wc -l` | 管道正常工作 |
| scene=work 时执行 shell | 触发 request_permission |

## 后续演进

`workspace_shell` 实现后，之前的"内置 Skill + CLI 子命令"方案可以大幅简化：
- 安装 MCP：模型直接 `workspace_shell` 执行 `oktapod capability install-mcp ...`
- 安装 Skill：模型直接 `workspace_shell` 执行 `git clone` 到 skills 目录
- 不需要专门的内置 Skill 引导，模型自己知道怎么做（和 Claude Code 一样）
