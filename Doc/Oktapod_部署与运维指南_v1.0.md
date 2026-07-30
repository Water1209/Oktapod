# Oktapod 部署与运维指南

> 版本：v2.0（按 V1 架构修订）
> 更新日期：2026-07-29
> 基线：`Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md`

## 1. 当前部署边界

V1 默认采用单机 Go 服务、React 静态资源、SQLite 和操作系统 SecretStore。服务默认监听 `127.0.0.1:8787`。

`Deploy/docker-compose.team.yml` 只用于后续 Team 单节点试点，不进入 V1 主流程。

## 2. Provider 配置

Provider 非敏感元数据保存在数据库，凭据只保存在 Windows Credential Manager 或 Linux Secret Service。不得把 API Key 写入配置文件、命令参数、日志、截图或导出包。

兼容 CLI：

```powershell
.\Build\oktapod.exe model configure --endpoint https://provider.example/v1 --model model-id
.\Build\oktapod.exe model status
.\Build\oktapod.exe model reset
```

CLI 管理默认 Provider 实例。Web/API 可通过 `/api/v1/providers` 管理多个实例，`protocol_family` 可选择 `openai_compatible`、`anthropic` 或 `gemini`。

首次启动不会生成或写死可登录的管理员口令。操作者应在登录页注册第一个本地账户。

## 3. MCP 配置

V1 将内建搜索、MCP 搜索和网页提取统一注册为 ToolSpec。内建搜索提供无需外部凭据的最低可用能力；Tavily 等 MCP 可在完成审核后成为首选。

### 3.1 安装 MCP Server

资源中心支持上传或声明 MCP 配置，完成校验、风险审核和显式启用后才允许连接。对话式安装属于 V1 差距任务；在追踪矩阵标记 `PASS` 前，不应把以下交互描述为已完成能力：

```text
帮我安装 MCP: npx tavily-mcp-server
```

目标流程会触发安装 Skill，但仍必须展示来源、权限差异和人工审核，模型不能静默安装或启用。

### 3.2 手动配置（高级）

当前使用 Web 资源中心手动配置 MCP Server；CLI 与可迁移示例在对应任务通过验收后再进入正式说明。

### 3.3 搜索能力

安装并审核启用 Tavily MCP 后，Agent 可获得其声明的搜索和提取工具。禁用 Tavily 不影响内建搜索；也可审核启用另一个同能力 MCP，无需修改 Agent Core。

## 4. 验证

### 4.1 发布验证

代码或配置变化后至少执行：

```powershell
$env:GOCACHE="$env:TEMP\go-build-cache"
go test ./... -count=1
go vet ./...
go test -race ./...
pnpm verify
```

### 4.2 Docker 构建

V1 必须采用多阶段构建：

```dockerfile
# Node/pnpm 前端构建 → Go 构建 → 最小运行镜像
```

构建命令：

```powershell
docker build -f Deploy/Dockerfile -t oktapod:v1 .
docker run -p 8787:8787 oktapod:v1
```

## 5. 安全边界

V1 默认执行模式为 `governed_host`：

- Native 文件工具限制在当前 Workspace；
- 子进程使用独立 HOME，不继承宿主 SSH/AWS/Git/Provider 凭据；
- Git 禁用宿主配置、credential helper、prompt 和 hooks；
- 进程树终止；
- 资源限制（CPU/内存/超时）。

宿主 Shell 仍可能通过绝对路径读取宿主文件，因此 UI 必须标注“宿主受控执行，非强沙箱”。非 root、挂载隔离和网络默认拒绝只在容器/OS 强沙箱中声明。

## 6. 运维命令

```powershell
.\Build\oktapod.exe preflight
.\Build\oktapod.exe backup -output E:\backups\oktapod-20260729
.\Build\oktapod.exe restore -input E:\backups\oktapod-20260729
.\Build\oktapod.exe cleanup
```

## 7. 故障排查

- `MODEL_CREDENTIAL_MISSING`：SecretStore 中不存在对应凭据，重新输入凭据。
- `WEB_SEARCH_UNAVAILABLE`：内建搜索适配器当前不可用；先检查服务网络与搜索配置，再检查可选 MCP 健康状态。禁用 Tavily 不应移除内建搜索能力。
- SQLite Busy：检查活跃 Worker/Lease，不要扩大无限重试。

---

> 本文档随 V1 架构演进持续更新。历史版本见 `Doc/archive/Oktapod_部署与运维指南_v1.0.md`。
