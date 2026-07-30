# Oktapod 部署与运维指南

> 版本：v1.0.0
> 更新日期：2026-07-30
> 发布路径：注册用户 + 个人私有项目 + SQLite

## 1. 部署结论

V1 默认是单机 Go 服务，内嵌 React 静态资源，数据存入 SQLite，Provider 凭据进入操作系统 SecretStore。开发运行默认监听 `127.0.0.1:8787`；Docker 镜像监听容器内 `0.0.0.0:8787`。

`Deploy/docker-compose.team.yml` 与 PostgreSQL 仅保留后续 Team 试点边界，不属于 V1 发布或完成度。

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

## 3. Docker

```powershell
docker build --build-arg OKTAPOD_VERSION=1.0.0 -f Deploy/Dockerfile -t oktapod:1.0.0 .
docker run --rm --name oktapod-v1 -p 8787:8787 -v oktapod-data:/data oktapod:1.0.0
```

镜像使用 Node/pnpm 构建 Web、Go 构建二进制，并以 Debian 非 root 用户运行。健康检查是 `/api/v1/health`；发布烟测还会验证注册、Session/CSRF、`/diagnostics` 版本及个人项目创建：

```powershell
$env:OKTAPOD_SMOKE_URL="http://127.0.0.1:8787/api/v1"
$env:OKTAPOD_EXPECT_VERSION="1.0.0"
node Test/docker-smoke.mjs
```

镜像本身不包含 Provider Key、数据库密码或登录凭据。

## 4. Provider

Provider 非敏感元数据保存在数据库，凭据只允许存入 Windows Credential Manager、Linux Secret Service 或由 `OKTAPOD_MODEL_API_KEY` 等环境变量注入。不得把 Key 写入配置文件、命令参数、日志、截图、导出包或提交。

默认 Provider CLI：

```powershell
.\Build\oktapod-v1.0.0-windows-amd64.exe model configure --endpoint https://provider.example/v1 --model model-id
.\Build\oktapod-v1.0.0-windows-amd64.exe model status
.\Build\oktapod-v1.0.0-windows-amd64.exe model reset
```

Web/API 支持多个 Provider 实例，协议族为 `openai_compatible`、`anthropic` 或 `gemini`。V1 发布证据使用真实 OpenAI-compatible Provider；原生 Anthropic/Gemini 保留合同测试，不要求真实凭据。

## 5. MCP、Skill 与搜索

内建搜索是无需外部凭据的最低可用能力，不因可选 MCP 不健康而删除。资源中心支持全局/项目级 Skill、Package 与 MCP 的校验、隔离、人工审核、启用和调用；MCP Schema、配置版本与健康状态会进入新 Run 快照。

上传 ZIP/SKILL.md/JSON/YAML 后仍需人工审核。模型不能静默安装或启用能力。Tavily 等 MCP 可作为已审核首选，但不是 V1 核心依赖。

## 6. 执行安全边界

默认执行姿态为 `governed_host`：

- Native 文件工具限定项目 Workspace，并阻止路径逃逸与 symlink 穿越；
- 子进程使用隔离 HOME，清理 SSH/AWS/Git/Provider 凭据环境；
- Git 禁用宿主配置、credential helper、prompt 与 hooks；
- 命令有超时、取消与进程树终止；
- 写文件、命令和 Git 修改受 Policy、状态哈希、审批、Receipt 与验证治理。

这不是容器或 OS 级强沙箱。宿主进程仍可能访问宿主可读资源，因此 UI 明示“宿主受控 · 非强沙箱”。网络默认拒绝、文件挂载白名单、CPU/内存硬隔离属于后续强沙箱。

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

Linux CI 额外执行 `pnpm verify:ci`（含 race）和 Docker 产品烟测。本机没有 Docker daemon 或 CGO C 编译器时，只能形成 RC，不能把未运行门禁标记为本机已通过。

- `MODEL_CREDENTIAL_MISSING`：SecretStore 中不存在相应凭据。
- `WEB_SEARCH_UNAVAILABLE`：检查主机网络与内建搜索，再检查可选 MCP 健康。
- `SQLITE_BUSY`：检查活跃 Worker/Lease；应用会有限重试，不要改成无限重试。
- 版本不符：运行 `<binary> version`，不要发布显示为 `dev` 的开发构建。

历史部署方案保存在 `Doc/archive/Oktapod_部署与运维指南_v1.0.md`。
