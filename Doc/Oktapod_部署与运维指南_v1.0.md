# Oktapod 部署与运维指南

> 版本：v1.0.1
> 更新日期：2026-08-11
> 发布路径：注册用户 + 个人私有项目 + SQLite

## 1. 部署结论

V1 默认是单机 Go 服务，内嵌 React 静态资源，数据存入 SQLite，Provider 凭据进入操作系统 SecretStore。开发和原生发布运行默认监听 `127.0.0.1:8787`。

当前仓库不提供 Docker 或 Compose 发布路径。未来团队与 PostgreSQL 兼容边界需要重新设计容器化方案，不能作为 V1 的可用部署方式。

## 2. Windows 本机发布

环境：Node.js `>=22.12.0`、pnpm `10.28.2`、Go `1.26.5`。

```powershell
pnpm install --frozen-lockfile
$env:GOCACHE="$env:TEMP\go-build-cache"
pnpm verify
pnpm release:build
pnpm release:smoke
.\Build\oktapod-v1.0.0-windows-amd64.exe version
.\Build\oktapod-v1.0.0-windows-amd64.exe serve
```

浏览器访问 `http://127.0.0.1:8787/`。首次启动不会创建共享默认账户或密码，必须在登录页完成注册。新建个人项目会自动初始化 `main` 分支和空 Git 基线，确保首次文件修改就能进入 Changes、撤销和选择提交闭环。

`pnpm release:build` 从根 `package.json` 读取版本，生成带版本二进制及同名 `.sha256`；普通 `pnpm build` 是开发构建，运行时版本为 `dev`。

## 3. 原生产品烟测

发布烟测验证健康、注册、Session/CSRF、`/diagnostics` 版本及个人项目创建：

```powershell
$env:OKTAPOD_SMOKE_URL="http://127.0.0.1:8787/api/v1"
$env:OKTAPOD_EXPECT_VERSION="1.0.0"
node Test/product-smoke.mjs
```

发布二进制不包含 Provider Key、数据库密码或登录凭据。

## 4. Provider

Provider 非敏感元数据保存在数据库，凭据只允许存入 Windows Credential Manager、Linux Secret Service 或由 `OKTAPOD_MODEL_API_KEY` 等环境变量注入。不得把 Key 写入配置文件、命令参数、日志、截图、导出包或提交。

默认 Provider CLI：

```powershell
.\Build\oktapod-v1.0.0-windows-amd64.exe model configure --endpoint https://provider.example/v1 --model model-id
.\Build\oktapod-v1.0.0-windows-amd64.exe model status
.\Build\oktapod-v1.0.0-windows-amd64.exe model reset
```

Web/API 支持多个 Provider 实例，协议族为 `openai_compatible`、`anthropic` 或 `gemini`。V1 发布证据使用真实 OpenAI-compatible Provider；原生 Anthropic/Gemini 保留合同测试，不要求真实凭据。

“自动审查”需通过 `OKTAPOD_REVIEWER_MODEL` 指定当前 Provider Catalog 中真实可用的独立 Reviewer 模型。`GET /api/v1/diagnostics` 的 `auto_review.configured/available/reason` 是运维真源；未配置、模型不在 Catalog、超时或非法结构都会 fail-closed 并转人工审批，不得将“选项可见”当作 Reviewer 已生效。

## 5. MCP、Skill 与搜索

内建搜索是无需外部凭据的最低可用能力，不因可选 MCP 不健康而删除。资源中心支持当前用户全局或项目级 Skill、MCP 与 Agent 的校验、隔离、人工审核、启用、调用、停用和永久删除；活动 Run 引用时拒绝删除，历史快照与脱敏审计保留。MCP Schema、配置版本、凭据引用与健康状态会进入新 Run 快照，凭据原文不进入 SQLite、Manifest、API 返回、日志或导出。

Skill 菜单安装只接受单 Skill ZIP，不接受裸 `SKILL.md`、JSON 或 YAML；ZIP 在写入前校验唯一 `SKILL.md`、frontmatter、允许目录、文件数/体积、路径穿越、Unicode 冲突、符号链接、嵌套 Skill、依赖目录和隐藏载荷。MCP 不使用 ZIP：输入名称、`server.json`、规范化定义、URL、固定版本 npm/PyPI 包或本地命令，解析后只显示该传输所需字段，高级区可复核规范化 `MCPDefinition v1`。名称发现访问官方 Registry `https://registry.modelcontextprotocol.io`，生产环境需要允许对该域名的出站 HTTPS；Registry 只用于发现，歧义候选必须人工选择且安装前固定实际版本。模型不能静默安装或启用能力；在新 Thread 中用自然语言要求安装 MCP 时，Composer 会打开相同配置与确认面板。一个 MCP 的多项凭据以不透明加密集合写入系统 SecretStore，运行时仅注入定义声明的环境变量或受控 Header。

`auth.type=oauth` 使用官方 MCP Go SDK 的 Authorization Code + PKCE 流程。资源审核启用后点击“连接账号”，授权页在新窗口打开；回调地址按当前监听端口生成，例如默认 `http://127.0.0.1:8787/api/v1/mcp/oauth/callback`。Personal V1 要求浏览器与服务运行在同一台电脑；授权窗口被拦截时资源详情仍提供显式链接。取消只终止短期授权会话，不删除资源或项目文件。SDK 发现/注册得到的客户端配置、access token 和 refresh token 以 `oktapod.mcp-oauth/v1` 不透明信封进入 SecretStore，刷新后原位更新；HTTP 日志只记录 callback 路径，不记录含授权码的 query。手工 PAT/API token 使用 `bearer`，不要写入 Manifest 或自然语言消息。

Windows 上的 npm stdio MCP 会把 `npx.cmd`/`npx.ps1` 解析为 `node.exe` 直接启动 `npx-cli.js`，不经过命令行 shell。MCP 子进程的 HOME、APPDATA、npm 和 uv 缓存使用 `%TEMP%\oktapod-mcp-runtime`，防止社区服务器意外读取宿主配置；该目录是可重建运行缓存，服务停止后可由系统临时文件策略清理。

当用户在任务中明确点名 Tavily 等已连接 MCP 时，Run 只向模型暴露该 MCP、计划/输入/审批控制工具以及完成交付所需的工作区工具，不静默改走内建搜索。完整 Annotation 显式声明非只读、破坏性或非幂等，以及快照外未知工具时按 L3 逐次审批；已审核、启用并冻结到 Run 的语义检索工具即使缺少可选 Annotation，也按只读幂等 L2 保存 Receipt，在“完全访问”下不再重复确认。Tavily 是可选首选，不是 V1 核心依赖。

## 6. 执行安全边界

默认执行姿态为 `governed_host`：

- Native 文件工具限定项目 Workspace，并阻止路径逃逸与 symlink 穿越；
- 子进程使用隔离 HOME，清理 SSH/AWS/Git/Provider 凭据环境；
- Git 禁用宿主配置、credential helper、prompt 与 hooks；
- 命令有超时、取消与进程树终止；
- 写文件、命令和 Git 修改受 Policy、状态哈希、审批、Receipt 与验证治理。

这不是容器或 OS 级强沙箱。宿主进程仍可能访问宿主可读资源，因此 UI 明示“宿主受控 · 非强沙箱”。网络默认拒绝、文件挂载白名单、CPU/内存硬隔离属于后续强沙箱。

项目文件在工作台右侧 `Files` 查看：`Workspace` 是可变文件，`Project Files` 是上传/发布的 Asset，`Task Outputs` 是不可变 Artifact；`Changes` 仅显示 Git 差异。Personal Profile 的 Diagnostics 会显示数据根和 Workspace 根的宿主绝对路径，Team Profile 不暴露该路径。脚本生成的文件先在 Workspace 可见；只有经 `artifact_capture` 校验登记后才会进入 Task Outputs 并可作为任务交付证据。

新项目默认将每个任务限制为 80 次模型调用、1,000,000 Token；单次模型输出先使用 8,192 Token，仅在截断恢复时提升到 16,384/32,768。该值来自 30,000 汉字深度报告的真实极限回归（67 次调用、约 840k 输入输出 Token）并保留约 20% 调用余量。项目“资源配额”可显式调整或设为不限额；已有 Run 使用创建时冻结的值，不被配置修改追溯覆盖。

## 7. 运维

```powershell
.\Build\oktapod-v1.0.0-windows-amd64.exe preflight
.\Build\oktapod-v1.0.0-windows-amd64.exe backup -output E:\backups\oktapod-20260730
.\Build\oktapod-v1.0.0-windows-amd64.exe restore -input E:\backups\oktapod-20260730
.\Build\oktapod-v1.0.0-windows-amd64.exe cleanup
```

停止前等待正在执行的受治理任务到达检查点；升级前先备份数据目录。回滚时停止新版本、恢复备份，再启动旧版本二进制。数据库迁移只前进，不承诺直接降级复用已迁移数据库。

## 8. 发布门禁与故障排查

```powershell
$env:GOCACHE="$env:TEMP\go-build-cache"
pnpm verify
pnpm test:e2e
```

Ubuntu CI 额外执行 `pnpm test:race`；Ubuntu 与 Windows CI 都执行 clean install、`pnpm verify`、原生发布构建和产品烟测。本机不能把未运行的跨平台门禁标记为已通过。

- `MODEL_CREDENTIAL_MISSING`：SecretStore 中不存在相应凭据。
- `WEB_SEARCH_UNAVAILABLE`：检查主机网络与内建搜索，再检查可选 MCP 健康。
- `SQLITE_BUSY`：检查活跃 Worker/Lease；应用会有限重试，不要改成无限重试。
- 版本不符：运行 `<binary> version`，不要发布显示为 `dev` 的开发构建。

历史部署方案保存在 `Doc/archive/Oktapod_部署与运维指南_v1.0.md`。
