# Oktapod 部署与运维指南

> 版本：v1.0.3
> 更新日期：2026-08-13
> 发布路径：注册用户 + 个人私有项目 + SQLite

## 1. 部署结论

V1 默认是单机 Go 服务，内嵌 React 静态资源，数据存入 SQLite；Provider、MCP 多字段凭据和 OAuth Token 统一进入数据库加密 Vault。开发和原生发布运行默认监听 `127.0.0.1:8787`，不再依赖 Windows Credential Manager、Linux Secret Service、macOS Keychain 或桌面登录会话。

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

启动配置默认位于 `%APPDATA%\Oktapod\config.yaml`；可复制 `Deploy/config.yaml.example`。加载顺序固定为 `--config > OKTAPOD_CONFIG_FILE > 默认 config.yaml > 内置默认值`，未知 YAML 字段会阻止启动。`profile`、监听地址、DataRoot、Run 并发、数据库驱动和 Capability 信任键文件属于启动配置；Reviewer、模型价格、上下文和用户偏好属于数据库产品配置。

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

Provider 非敏感元数据保存在 `provider_instances`，不再维护 `model_provider_configs` 第二真源。Provider 凭据只允许进入数据库 Vault；`OKTAPOD_MODEL_API_KEY`、`OKTAPOD_MODEL_API_KEY_FILE`、`OKTAPOD_REVIEWER_MODEL` 和 `OKTAPOD_MODEL_PRICING_JSON` 不再参与产品运行。不得把 Key 写入 YAML、命令参数、日志、截图、普通导出或提交。

先初始化并确认 Vault，再配置 Provider。CLI 从隐藏 TTY、stdin 或文件读取恢复口令和 Provider 凭据，恢复口令没有命令行参数：

```powershell
.\Build\oktapod-v1.0.0-windows-amd64.exe model configure --endpoint https://provider.example/v1 --model model-id
.\Build\oktapod-v1.0.0-windows-amd64.exe model status
.\Build\oktapod-v1.0.0-windows-amd64.exe model reset
```

Web/API 支持多个 Provider 实例，协议族为 `openai_compatible`、`anthropic` 或 `gemini`。V1 发布证据使用真实 OpenAI-compatible Provider；原生 Anthropic/Gemini 保留合同测试，不要求真实凭据。

“自动审查”在“配置中心 → 治理与审批”中保存准确的 Provider ID + Model ID、启用状态、超时和策略版本；它与“模型服务”的连接配置保持职责分离。每次 Reviewer 调用冻结实际 Provider、Model 和策略版本；未配置、模型不在该 Provider 的启用目录、凭据不可用、超时或非法结构都会 fail-closed 并转人工审批。`GET /api/v1/diagnostics` 的 `auto_review.configured/available/reason` 是运维真源，`credential_issues` 会列出需重新录入的 Provider/MCP 名称、状态和修复路径，不返回 `credential_ref` 或 Secret。

## 5. MCP、Skill 与搜索

内建搜索是无需外部凭据的最低可用能力，不因可选 MCP 不健康而删除。资源中心支持当前用户全局或项目级 Skill、MCP 与 Agent 的校验、隔离、人工审核、启用、调用、停用和永久删除；活动 Run 引用时拒绝删除，历史快照与脱敏审计保留。MCP Schema、配置版本、凭据引用与健康状态会进入新 Run 快照。凭据原文不得进入 Manifest、API 返回、日志或普通导出；SQLite 只保存 AES-256-GCM 应用层密文。

Skill 菜单安装只接受单 Skill ZIP，不接受裸 `SKILL.md`、JSON 或 YAML；ZIP 在写入前校验唯一 `SKILL.md`、frontmatter、允许目录、文件数/体积、路径穿越、Unicode 冲突、符号链接、嵌套 Skill、依赖目录和隐藏载荷。MCP 不使用 ZIP：输入名称、`server.json`、规范化定义、URL、固定版本 npm/PyPI 包或本地命令，解析后只显示该传输所需字段，高级区可复核规范化 `MCPDefinition v1`。名称发现访问官方 Registry `https://registry.modelcontextprotocol.io`，生产环境需要允许对该域名的出站 HTTPS；Registry 只用于发现，歧义候选必须人工选择且安装前固定实际版本。模型不能静默安装或启用能力；在新 Thread 中用自然语言要求安装 MCP 时，Composer 会打开相同配置与确认面板。一个 MCP 的多项凭据以不透明加密集合写入数据库 Vault，运行时仅注入定义声明的环境变量或受控 Header。

`auth.type=oauth` 使用官方 MCP Go SDK 的 Authorization Code + PKCE 流程。资源审核启用后点击“连接账号”，授权页在新窗口打开；回调地址按当前监听端口生成，例如默认 `http://127.0.0.1:8787/api/v1/mcp/oauth/callback`。Personal V1 要求浏览器与服务运行在同一台电脑；授权窗口被拦截时资源详情仍提供显式链接。取消只终止短期授权会话，不删除资源或项目文件。SDK 发现/注册得到的客户端配置、access token 和 refresh token 以 `oktapod.mcp-oauth/v1` 不透明信封进入 SecretStore，刷新后原位更新；HTTP 日志只记录 callback 路径，不记录含授权码的 query。手工 PAT/API token 使用 `bearer`，不要写入 Manifest 或自然语言消息。

Windows 上的 npm stdio MCP 会把 `npx.cmd`/`npx.ps1` 解析为 `node.exe` 直接启动 `npx-cli.js`，不经过命令行 shell。MCP 子进程的 HOME、APPDATA、npm 和 uv 缓存使用 `%TEMP%\oktapod-mcp-runtime`，防止社区服务器意外读取宿主配置；该目录是可重建运行缓存，服务停止后可由系统临时文件策略清理。

当用户在任务中明确点名 Tavily 等已连接 MCP 时，Run 只向模型暴露该 MCP、计划/输入/审批控制工具以及完成交付所需的工作区工具，不静默改走内建搜索。完整 Annotation 显式声明非只读、破坏性或非幂等，以及快照外未知工具时按 L3 逐次审批；已审核、启用并冻结到 Run 的语义检索工具即使缺少可选 Annotation，也按只读幂等 L2 保存 Receipt，在“完全访问”下不再重复确认。Tavily 是可选首选，不是 V1 核心依赖。

## 6. 数据库 Vault、备份与跨机器恢复

首次使用进入“设置 → 凭据与密钥库”，生成 32-byte Base64url 恢复口令并重新输入确认。确认前 Vault 为 `pending`，不得保存 Secret；恢复口令只在生成或旋转当次返回，不进入数据库、日志或页面存储。服务重启使用 `$DATA_ROOT/vault/bootstrap.key` 自动解锁；该文件是本机 KEK，不包含业务 Secret，目录和文件权限由跨平台适配器限制为当前服务用户。

```powershell
.\Build\oktapod-v1.0.0-windows-amd64.exe config doctor --json
.\Build\oktapod-v1.0.0-windows-amd64.exe vault status
.\Build\oktapod-v1.0.0-windows-amd64.exe vault initialize --recovery-output E:\secure\oktapod-recovery.txt
.\Build\oktapod-v1.0.0-windows-amd64.exe vault confirm --recovery-file E:\secure\oktapod-recovery.txt
.\Build\oktapod-v1.0.0-windows-amd64.exe vault recover --recovery-file E:\secure\oktapod-recovery.txt
.\Build\oktapod-v1.0.0-windows-amd64.exe vault rotate-recovery --recovery-output E:\secure\oktapod-recovery-new.txt
```

每个组织使用随机 256-bit DEK，每条 Secret 使用独立 Nonce 和包含 Schema、Organization、Secret Ref、Kind、Key Version 的 AAD。密文或 AAD 校验失败返回 `SECRET_CIPHERTEXT_INVALID`，不会以空值继续执行。Provider/MCP/OAuth 的状态统一为 `ready | locked | missing | corrupt`。

正式 Backup Manifest 为 V2：包含数据库密文、恢复包装、Vault 状态、Key Version 和 Secret 数量，但排除 `bootstrap.key`、恢复口令和 `.oktapod/runtime-cache`。因此新机器恢复后进入 `locked`，输入恢复口令会生成新的本地 KEK 并恢复自动启动。仅复制数据库也需要恢复口令；复制完整 DataRoot 会同时复制数据库和本地 KEK，便携但安全边界等同于整机数据泄露。项目逻辑导出不含 `secret_records`，Capability 的 `credential_ref` 会清空，导入后必须重新录入或重新连接。

如果本地 KEK 和恢复口令均丢失，Secret 永久不可恢复，但非敏感项目数据仍可访问。本版本不读取、迁移或删除旧操作系统凭据库；现有 Provider/MCP 会显示“凭据缺失”，由当前用户重新录入，OAuth MCP 重新授权。

## 7. 执行安全边界

默认执行姿态为 `governed_host`：

- Native 文件工具限定项目 Workspace，并阻止路径逃逸与 symlink 穿越；
- 子进程使用隔离 HOME，清理 SSH/AWS/Git/Provider 凭据环境；
- Git 禁用宿主配置、credential helper、prompt 与 hooks；
- 命令有超时、取消与进程树终止；
- 写文件、命令和 Git 修改受 Policy、状态哈希、审批、Receipt 与验证治理。

这不是容器或 OS 级强沙箱。宿主进程仍可能访问宿主可读资源，因此 UI 明示“宿主受控 · 非强沙箱”。网络默认拒绝、文件挂载白名单、CPU/内存硬隔离属于后续强沙箱。

项目文件在项目右侧“任务面板”查看：“工作区”是可变文件，“项目文件”是上传或明确保存、可供同项目后续任务复用的 Asset，“任务输出”是不可变 Artifact；“变更”仅显示 Git 差异。“保存到项目文件”不会向项目外发布。Personal Profile 的 Diagnostics 会显示数据根和 Workspace 根的宿主绝对路径，Team Profile 不暴露该路径。脚本生成的文件先在“工作区”可见；只有经 `artifact_capture` 校验登记后才会进入“任务输出”并可作为任务交付证据。

新项目默认将每个任务限制为 80 次模型调用、1,000,000 Token；单次模型输出先使用 8,192 Token，仅在截断恢复时提升到 16,384/32,768。该值来自 30,000 汉字深度报告的真实极限回归（67 次调用、约 840k 输入输出 Token）并保留约 20% 调用余量。项目“资源配额”可显式调整或设为不限额；已有 Run 使用创建时冻结的值，不被配置修改追溯覆盖。Run 额度不足时只创建一个可解释的预算决策，显示当前用量和建议新增额度；用户可选择按建议扩展、用现有结果收尾、本 Run 不限额或停止任务。停止后保留已有文件和 Receipt，并进入终态，不会反复申请预算。

## 8. 运维

```powershell
.\Build\oktapod-v1.0.0-windows-amd64.exe preflight
.\Build\oktapod-v1.0.0-windows-amd64.exe backup -output E:\backups\oktapod-20260730
.\Build\oktapod-v1.0.0-windows-amd64.exe restore -input E:\backups\oktapod-20260730
.\Build\oktapod-v1.0.0-windows-amd64.exe cleanup
```

停止前等待正在执行的受治理任务到达检查点；升级前先执行正式 Backup 并确认恢复口令可用。恢复到新机器后先运行 `vault status`，再从隐藏 TTY、stdin 或受保护文件完成 `vault recover`。回滚时停止新版本、恢复备份，再启动旧版本二进制。数据库迁移只前进，不承诺直接降级复用已迁移数据库。

## 9. 发布门禁与故障排查

```powershell
$env:GOCACHE="$env:TEMP\go-build-cache"
pnpm verify
pnpm test:e2e
```

Ubuntu CI 额外执行 `pnpm test:race`；Ubuntu 与 Windows CI 都执行 clean install、`pnpm verify`、原生发布构建和产品烟测。本机不能把未运行的跨平台门禁标记为已通过。

- `SECRET_VAULT_UNINITIALIZED` / `SECRET_VAULT_PENDING`：完成初始化和恢复口令确认。
- `SECRET_VAULT_LOCKED`：数据库来自其他机器或缺少本地 KEK，使用恢复口令重新绑定本机。
- `SECRET_CIPHERTEXT_INVALID`：密文、Nonce、AAD 或包装信息损坏；禁止空值回退，使用正式备份恢复或重新录入。
- `MODEL_CREDENTIAL_MISSING`：数据库 Vault 中不存在相应凭据，重新录入 Provider Key。
- `WEB_SEARCH_UNAVAILABLE`：检查主机网络与内建搜索，再检查可选 MCP 健康。
- `SQLITE_BUSY`：检查活跃 Worker/Lease；应用会有限重试，不要改成无限重试。
- 预算申请反复出现：先确认运行的是 2026-08-13 之后的构建；新版本不会以“每次只增加 1 次模型调用”的方式循环申请，失败或停止的终态任务也不会继续占用“待我处理”徽标。
- 版本不符：运行 `<binary> version`，不要发布显示为 `dev` 的开发构建。

历史部署方案保存在 `Doc/archive/Oktapod_部署与运维指南_v1.0.md`。
