# Oktapod 部署与运维指南

> 版本：v1.1.2
> 更新日期：2026-08-24
> 发布路径：注册用户 + 个人私有项目 + SQLite

## 1. 部署结论

V1 默认是单机 Go 服务，内嵌 React 静态资源，数据存入 SQLite；Provider、MCP 多字段凭据和 OAuth Token 统一进入数据库加密 Vault。开发和原生发布运行默认监听 `127.0.0.1:8787`，不再依赖 Windows Credential Manager、Linux Secret Service、macOS Keychain 或桌面登录会话。

当前仓库不提供 Docker 或 Compose 发布路径。未来团队与 PostgreSQL 兼容边界需要重新设计容器化方案，不能作为 V1 的可用部署方式。

## 2. 发布工件与目录布局

普通部署机器不需要 Node.js、pnpm 或 Go。发布方必须在目标操作系统上运行原生构建，并把下列文件作为同一可信发布集合交付；当前脚本不生成 MSI、DEB、RPM 或一体化安装器。

| 工件 | Windows | Linux | 用途 |
|---|---|---|---|
| 主程序 | `oktapod-v1.0.0-windows-amd64.exe` | `oktapod-v1.0.0-linux-amd64` | 内嵌 Web UI 的 Go 服务 |
| 主程序校验 | 同名 `.sha256` | 同名 `.sha256` | 安装前校验下载完整性 |
| 文档 Runtime | `document-runtime-v1.0.0-windows-amd64.zip` | `document-runtime-v1.0.0-linux-amd64.tar.gz` | 自包含 Python、LibreOffice、PDFium、Tesseract 与许可证 |
| Runtime 校验 | 同名 `.sha256` | 同名 `.sha256` | 安装前校验归档完整性 |
| Runtime 信任键 | `document-runtime-trusted-keys.json` | 同名 | 验证签名 Manifest；必须与发布工件通过同一可信渠道分发 |
| 配置样例 | `Deploy/config.yaml.example` | 同名 | 不含 Secret 的启动配置模板 |

安装后采用版本隔离布局，升级不得原地覆盖旧版本：

```text
Windows
C:\Program Files\Oktapod\releases\1.0.0\
  oktapod.exe
  runtime\document-runtime\runtime-manifest.json
C:\ProgramData\Oktapod\
  config.yaml
  trusted-keys.json
  data\

Linux
/opt/oktapod/releases/1.0.0/
  oktapod
  runtime/document-runtime/runtime-manifest.json
/etc/oktapod/
  config.yaml
  trusted-keys.json
/var/lib/oktapod/
```

主程序按顺序查找显式 `OKTAPOD_DOCUMENT_RUNTIME_ROOT`、DataRoot 的 `runtimes/document-runtime/current`、主程序同级的 `runtime/document-runtime`。正式部署推荐第三种版本内物理目录；Runtime 根和 Manifest 中的所有文件都不能是符号链接。配置加载顺序固定为 `--config > OKTAPOD_CONFIG_FILE > 默认 config.yaml > 内置默认值`，未知 YAML 字段会关闭式阻止启动。

### 2.1 发布方构建

构建环境为 Node.js `>=22.12.0`、pnpm `10.28.2`、Go `1.26.5`。Windows 工件必须在 Windows 构建，Linux 工件必须在 Linux 构建；构建脚本按当前主机确定平台与架构，不承诺交叉编译。

`OKTAPOD_DOCUMENT_RUNTIME_STAGE` 必须是目标平台的自包含目录，并包含完整 `LICENSES/`。Python、LibreOffice、Tesseract 环境变量均填写该目录内的相对路径；签名私钥只能来自仓库外的受保护文件。

```powershell
$env:OKTAPOD_DOCUMENT_RUNTIME_STAGE='D:\release-stage\windows-amd64'
$env:OKTAPOD_DOCUMENT_RUNTIME_PYTHON='python\python.exe'
$env:OKTAPOD_DOCUMENT_RUNTIME_LIBREOFFICE='libreoffice\program\soffice.exe'
$env:OKTAPOD_DOCUMENT_RUNTIME_TESSERACT='tesseract\tesseract.exe'
$env:OKTAPOD_DOCUMENT_RUNTIME_SIGNING_KEY_FILE='D:\release-secrets\document-runtime-ed25519.pem'
$env:OKTAPOD_DOCUMENT_RUNTIME_SIGNING_KEY_ID='document-runtime-2026-01'
pnpm install --frozen-lockfile
$env:GOCACHE="$env:TEMP\go-build-cache"
pnpm verify
pnpm release:build
pnpm release:smoke
```

```bash
export OKTAPOD_DOCUMENT_RUNTIME_STAGE=/srv/release-stage/linux-amd64
export OKTAPOD_DOCUMENT_RUNTIME_PYTHON=python/bin/python3
export OKTAPOD_DOCUMENT_RUNTIME_LIBREOFFICE=libreoffice/program/soffice
export OKTAPOD_DOCUMENT_RUNTIME_TESSERACT=tesseract/tesseract
export OKTAPOD_DOCUMENT_RUNTIME_SIGNING_KEY_FILE=/run/secrets/document-runtime-ed25519.pem
export OKTAPOD_DOCUMENT_RUNTIME_SIGNING_KEY_ID=document-runtime-2026-01
pnpm install --frozen-lockfile
export GOCACHE="${TMPDIR:-/tmp}/go-build-cache"
pnpm verify
pnpm release:build
pnpm release:smoke
```

`release:build` 会检查版本、锁定依赖、LibreOffice、Tesseract `chi_sim + eng`、四格式 25 次 RPC、SBOM、许可证、逐文件 Hash 和 Ed25519 签名；`release:smoke` 使用临时端口，完成后会停止测试服务，不能代替真实部署健康检查。

### 2.2 本次 Runtime 资源包构建证据

2026-08-24 已在 Windows 主机和 WSL Ubuntu `linux-amd64` 环境分别构建两平台主程序和自包含 `document-runtime` 资源包。主程序均报告版本 `1.0.0`；构建器对包内 Python、LibreOffice、Tesseract、许可证、逐文件 Manifest 和 Ed25519 签名执行检查，并对 PPTX、DOCX、XLSX、PDF 完成 25 次 RPC smoke；Windows 与 Linux 均通过。Windows 主程序使用新归档完成完整 `release:smoke`；Linux 主程序在 WSL ext4 临时目录中解包 Runtime 后完成同等 `product-smoke`，避免把 `/mnt/e` 的 NTFS 逐文件 I/O 延迟误判为产品启动失败。

| 工件 | 大小 | SHA256 | 构建验证 |
|---|---:|---|---|
| `Build/oktapod-v1.0.0-windows-amd64.exe` | 31,812,096 bytes | `3d5e87591cc7c8620269f0c41dc05158667fb8773be819440c4d13a9dd025aed` | `version` = `1.0.0` |
| `Build/oktapod-v1.0.0-linux-amd64` | 31,154,338 bytes | `45407a2c0808378fc15d4068e1b182b58c0f0006972758ebcbafdd1a30a35de8` | `version` = `1.0.0` |
| `Build/document-runtime-v1.0.0-windows-amd64.zip` | 489,545,478 bytes | `5bd93247218ee79746d4ab3a3cf2aea18f6109c0247f604fac8ed503ba12798c` | 16,388 文件，25 RPC |
| `Build/document-runtime-v1.0.0-linux-amd64.tar.gz` | 284,075,490 bytes | `82e90cf26e4e1d90a87fce3c0d16a3a43c13f45d93c8bc8ddf4586eb084ed2c0` | 8,024 文件，25 RPC |
| `Build/document-runtime-trusted-keys.json` | 87 bytes | 随发布集合分发 | Key ID `oktapod-local-release-20260824` |

Windows Runtime 使用 Python 3.13、LibreOffice 26.2.4.2、Tesseract 5.4.0；Linux Runtime 使用 WSL Ubuntu 中的 Python 3.14.4、LibreOffice 26.2.5.2、Tesseract 5.5.0。构建器会剔除 staging 中的 `__pycache__`、`.pyc` 和 `.pyo`，本次两包归档也已检查不含这些运行时缓存。Linux 包内 LibreOffice 配置已改为基于 `ORIGIN` 的相对路径，避免真实主机必须安装到 `/usr/lib/libreoffice`；两包均包含 `LICENSES/`、SBOM、锁定依赖和签名 Manifest。上述是构建机和包级证据，不等同于已在目标生产发行版完成部署；真实部署仍必须执行本指南第 3、4、5 节的 Hash、`config doctor`、健康检查和资源中心验收。

本次 Key ID 对应的私钥仅存在于仓库外受保护位置。进入真实生产前必须完成离线备份、访问控制和组织密钥登记；若使用组织正式密钥，应重新运行两平台构建并重新分发对应的 `document-runtime-trusted-keys.json`，不能只替换公钥文件。

## 3. Windows 部署

以下命令在管理员 PowerShell 中执行。示例部署当前 `windows-amd64` 工件；ARM64 使用对应归档名。

```powershell
$Version = '1.0.0'
$Bundle = 'D:\Downloads\Oktapod'
$ReleaseRoot = "C:\Program Files\Oktapod\releases\$Version"
$ConfigRoot = 'C:\ProgramData\Oktapod'

function Assert-Sha256([string]$Path, [string]$ChecksumPath) {
  $Expected = ((Get-Content -Raw -LiteralPath $ChecksumPath).Trim() -split '\s+')[0].ToLowerInvariant()
  $Actual = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($Actual -ne $Expected) { throw "SHA256 mismatch: $Path" }
}

$Binary = Join-Path $Bundle 'oktapod-v1.0.0-windows-amd64.exe'
$RuntimeArchive = Join-Path $Bundle 'document-runtime-v1.0.0-windows-amd64.zip'
Assert-Sha256 $Binary "$Binary.sha256"
Assert-Sha256 $RuntimeArchive "$RuntimeArchive.sha256"

New-Item -ItemType Directory -Force $ReleaseRoot, "$ReleaseRoot\runtime", $ConfigRoot, "$ConfigRoot\data" | Out-Null
Copy-Item -LiteralPath $Binary -Destination "$ReleaseRoot\oktapod.exe"
Expand-Archive -LiteralPath $RuntimeArchive -DestinationPath "$ReleaseRoot\runtime" -Force
Copy-Item -LiteralPath "$Bundle\document-runtime-trusted-keys.json" -Destination "$ConfigRoot\trusted-keys.json"
Copy-Item -LiteralPath "$Bundle\config.yaml.example" -Destination "$ConfigRoot\config.yaml" -ErrorAction Stop
Test-Path -LiteralPath "$ReleaseRoot\runtime\document-runtime\runtime-manifest.json"
```

将 `C:\ProgramData\Oktapod\config.yaml` 调整为：

```yaml
profile: personal
listen_address: 127.0.0.1:8787
data_root: "C:/ProgramData/Oktapod/data"
run_concurrency: 4
database:
  driver: sqlite
model:
  first_token_timeout: 15s
  stream_idle_timeout: 60s
  turn_timeouts:
    auto: 120s
    off: 120s
    low: 180s
    medium: 300s
    high: 600s
capabilities:
  trusted_keys_file: "C:/ProgramData/Oktapod/trusted-keys.json"
web_access:
  private_fetch:
    enabled: false
    allowed_origins: []
```

`first_token_timeout` 从请求发出开始计算，只有正文、推理或工具调用的真实增量才算首 Token，HTTP 响应头、空 SSE 和 keep-alive 均不算；`stream_idle_timeout` 在首 Token 后约束连续无新 SSE 行的时间。`turn_timeouts` 是即使流持续活跃也不能突破的整轮硬上限。允许范围为首 Token/流空闲 5 秒～5 分钟、整轮 30 秒～30 分钟，并要求 `off <= low <= medium <= high`；非法值会阻止启动。修改后必须重启服务。

配置目录和 DataRoot 只应授权给运行 Oktapod 的 Windows 账户与系统管理员。随后以同一账户验证并启动：

```powershell
$Binary = 'C:\Program Files\Oktapod\releases\1.0.0\oktapod.exe'
$Config = 'C:\ProgramData\Oktapod\config.yaml'
& $Binary version
& $Binary --config $Config config doctor --json
& $Binary --config $Config serve
```

另开 PowerShell 验证：

```powershell
Invoke-RestMethod http://127.0.0.1:8787/api/v1/health
(Invoke-WebRequest http://127.0.0.1:8787/).StatusCode
Get-NetTCPConnection -LocalAddress 127.0.0.1 -LocalPort 8787 -State Listen
```

V1 主程序尚未实现原生 Windows Service Control Manager 接口。正式支持路径是同一用户前台运行；需要登录后自启动时可选用任务计划程序，但它不替代发布烟测，维护前仍要先暂停或完成活跃任务：

```powershell
$Action = New-ScheduledTaskAction -Execute $Binary -Argument "--config `"$Config`" serve" -WorkingDirectory (Split-Path $Binary)
$Trigger = New-ScheduledTaskTrigger -AtLogOn
$Settings = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName 'Oktapod' -Action $Action -Trigger $Trigger -Settings $Settings -Description 'Oktapod Personal' -Force
Start-ScheduledTask -TaskName 'Oktapod'
```

## 4. Linux 部署

示例使用 `linux-amd64`、专用不可登录用户和 systemd。发布工件必须在兼容的 Linux ABI 基线上构建并通过该平台 smoke；当前验收矩阵尚未把所有发行版标记为已验证。

```bash
set -euo pipefail
VERSION=1.0.0
BUNDLE=/tmp/oktapod-release
RELEASE_ROOT=/opt/oktapod/releases/$VERSION

cd "$BUNDLE"
sha256sum -c oktapod-v1.0.0-linux-amd64.sha256
sha256sum -c document-runtime-v1.0.0-linux-amd64.tar.gz.sha256

id -u oktapod >/dev/null 2>&1 || sudo useradd --system --home-dir /var/lib/oktapod --shell /usr/sbin/nologin oktapod
sudo install -d -m 0755 "$RELEASE_ROOT/runtime" /etc/oktapod
sudo install -d -o oktapod -g oktapod -m 0700 /var/lib/oktapod
sudo install -m 0755 oktapod-v1.0.0-linux-amd64 "$RELEASE_ROOT/oktapod"
sudo tar -xzf document-runtime-v1.0.0-linux-amd64.tar.gz -C "$RELEASE_ROOT/runtime"
sudo install -m 0644 document-runtime-trusted-keys.json /etc/oktapod/trusted-keys.json
sudo install -m 0644 config.yaml.example /etc/oktapod/config.yaml
sudo chown -R root:root "$RELEASE_ROOT"
test -f "$RELEASE_ROOT/runtime/document-runtime/runtime-manifest.json"
```

将 `/etc/oktapod/config.yaml` 调整为：

```yaml
profile: personal
listen_address: 127.0.0.1:8787
data_root: /var/lib/oktapod
run_concurrency: 4
database:
  driver: sqlite
capabilities:
  trusted_keys_file: /etc/oktapod/trusted-keys.json
web_access:
  private_fetch:
    enabled: false
    allowed_origins: []
```

先验证配置，再创建 `/etc/systemd/system/oktapod.service`：

```bash
sudo -u oktapod "$RELEASE_ROOT/oktapod" --config /etc/oktapod/config.yaml config doctor --json
```

```ini
[Unit]
Description=Oktapod Personal Agent OS
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=oktapod
Group=oktapod
WorkingDirectory=/opt/oktapod/releases/1.0.0
ExecStartPre=/opt/oktapod/releases/1.0.0/oktapod --config /etc/oktapod/config.yaml config doctor --json
ExecStart=/opt/oktapod/releases/1.0.0/oktapod --config /etc/oktapod/config.yaml serve
Restart=on-failure
RestartSec=3s
KillSignal=SIGTERM
TimeoutStopSec=20s
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/oktapod

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now oktapod
sudo systemctl status oktapod --no-pager
curl --fail --silent --show-error http://127.0.0.1:8787/api/v1/health
curl --fail --silent --show-error http://127.0.0.1:8787/ >/dev/null
sudo journalctl -u oktapod -n 100 --no-pager
```

Personal Profile 只允许回环地址，不应改为 `0.0.0.0`。远程 Linux 主机通过 SSH 隧道访问：

```bash
ssh -L 8787:127.0.0.1:8787 user@linux-host
```

随后在本机浏览器打开 `http://127.0.0.1:8787/`。

## 5. 首次启动验收

Windows 与 Linux 首次启动都不会创建共享默认账户或密码。必须在登录页注册本地账户，在“设置 → 凭据与密钥库”生成并确认一次性恢复口令，再将恢复口令转移到离线受保护介质。Vault 变为 `active` 后，通过 Web 配置 Provider/MCP 凭据；Secret 不得写入 YAML、systemd unit、任务计划参数或日志。

部署验收至少覆盖：

1. `<binary> version` 与预期版本一致，不是 `dev`。
2. `config doctor --json` 成功，`GET /api/v1/health` 返回 `sqlite/personal/ok`，首页返回 200。
3. 可以注册、登录、创建个人项目；重启后仍能登录并读取项目。
4. 资源中心的 `document-runtime`、`presentations`、`pdf`、`documents`、`spreadsheets` 都显示“可用”，Runtime 详情列出六个固定 `document_*` 工具。
5. 上传 PPTX/PDF/DOCX/XLSX 后不生成临时解析脚本，能够完成结构读取、分页、渲染和 Artifact 导出。

## 6. 升级、回滚与卸载

升级采用旁路安装，不能覆盖当前版本：

1. 等待活跃任务到达检查点，运行当前版本 `preflight`，解决活跃 Run 和待审批项。
2. 停止当前服务，再用当前版本和当前配置执行 `backup -output <全新空目录>`；同时离线保存恢复口令、配置和当前 `trusted-keys.json`。
3. 将新二进制和新 Runtime 安装到新的版本目录，校验 Hash；先运行新版本 `config doctor`。
4. Windows 更新任务计划或人工启动路径；Linux 修改 systemd 的 `WorkingDirectory/ExecStartPre/ExecStart` 后执行 `daemon-reload`。
5. 启动后核对版本、健康、Vault、资源中心、项目读取和文档 Runtime，再接受新任务。

数据库迁移只前进。回滚时禁止让旧程序直接打开已被新版本迁移的数据根；使用升级前备份恢复到一个全新的空 DataRoot，并让旧版本配置指向该目录：

```text
old binary + old trusted keys + rollback config
                         ↓
       restore pre-upgrade backup into empty DataRoot
                         ↓
             start old version and verify
```

卸载前先执行最终备份并确认恢复口令可用。停止服务后可以删除版本目录；配置、DataRoot 和备份含项目数据或加密 Secret，不得与程序文件一起默认删除。只有用户明确确认不再需要恢复时才删除这些数据。

## 7. Web Access 与受控私网读取

`web_search` 只向模型暴露单个 `query`，使用内建 DDG → Bing RSS 零凭据链路；`web_fetch` 只接受模型传入一个 URL。代理、Header、Cookie、凭据、超时、内容上限和网络策略都由服务端固定。公网 Fetch 仅允许 HTTP/HTTPS 的 80/443；域名解析结果只要包含私网、回环、链路本地、组播、未指定、文档/基准测试网段或其他非公网地址，整次请求即拒绝。

私网读取默认关闭。需要读取内网站点时，在配置中心“运行与上下文 → Web Access 网络边界”编辑，或直接修改 YAML 后重启：

```yaml
web_access:
  private_fetch:
    enabled: true
    allowed_origins:
      - id: intranet-wiki
        origin: http://192.168.10.20:8080
      - id: local-preview
        origin: http://127.0.0.1:3000
      - id: ipv6-service
        origin: https://[fd00::20]:8443
```

每项只能是唯一 ID 和“HTTP/HTTPS + 字面量 IPv4/IPv6 + 显式端口”的精确 origin；最多 64 项。域名、路径、query、fragment、userinfo、CIDR、通配符、端口范围、公网地址、链路本地/组播/未指定/广播、云元数据地址和 Oktapod 自身监听 origin 会阻止启动。开启但列表为空也会阻止启动。普通成员的配置 API 只返回启用状态和规则数量，完整 origin 只向 Owner/Admin 展示。

新 Run 只冻结 origin Hash 和策略 Hash，不保存明文白名单。执行时取“Run 冻结策略 ∩ 当前启动策略”：新增规则不会让旧 Run 访问新 origin；关闭、删除或修改 origin 并重启会立即收回对应访问权。任意配置策略 Hash 变化都会使已有 `run_origin` 授权失效；仍同时存在于冻结与当前列表的 origin 可重新人工批准，不需要重建 Run。每个 Run 首次访问一个私网 origin 必须人工批准；同 Run 同 scheme + IP + port 的后续路径复用授权，不跨端口、协议或 Run，最长 24 小时且 Run 终态后失效。“完全访问”和 AI Reviewer 都不能替代这次人工决定。

Fetch 固定 GET-only、无系统代理/认证/Cookie/Referer，DNS 校验和 Dial 绑定同一验证 IP；最多 5 次同源重定向，每跳重验。跨 origin 不跟随，只把目标地址返回给模型发起新的受治理调用。响应在解压后限制 2 MiB，转换输入 256 KiB，模型正文 24 KiB；支持 HTML、text、JSON、XML，PDF 与二进制拒绝。HTML 转 Markdown 会移除脚本节点并把相对链接解析为绝对链接；页面内容统一标记 `external_untrusted`，只能作为数据和引用来源。

普通 `pnpm build` 是开发构建，运行时版本为 `dev`；不能作为正式部署工件。

## 8. 原生产品烟测

发布烟测验证健康、注册、Session/CSRF、`/diagnostics` 版本及个人项目创建：

```powershell
$env:OKTAPOD_SMOKE_URL="http://127.0.0.1:8787/api/v1"
$env:OKTAPOD_EXPECT_VERSION="1.0.0"
node Test/product-smoke.mjs
```

```bash
export OKTAPOD_SMOKE_URL=http://127.0.0.1:8787/api/v1
export OKTAPOD_EXPECT_VERSION=1.0.0
node Test/product-smoke.mjs
```

发布二进制不包含 Provider Key、数据库密码或登录凭据。

## 9. Provider

Provider 非敏感元数据保存在 `provider_instances`，不再维护 `model_provider_configs` 第二真源。Provider 凭据只允许进入数据库 Vault；`OKTAPOD_MODEL_API_KEY`、`OKTAPOD_MODEL_API_KEY_FILE`、`OKTAPOD_REVIEWER_MODEL` 和 `OKTAPOD_MODEL_PRICING_JSON` 不再参与产品运行。不得把 Key 写入 YAML、命令参数、日志、截图、普通导出或提交。

先初始化并确认 Vault，再配置 Provider。CLI 从隐藏 TTY、stdin 或文件读取恢复口令和 Provider 凭据，恢复口令没有命令行参数：

```powershell
.\Build\oktapod-v1.0.0-windows-amd64.exe model configure --endpoint https://provider.example/v1 --model model-id
.\Build\oktapod-v1.0.0-windows-amd64.exe model status
.\Build\oktapod-v1.0.0-windows-amd64.exe model reset
```

Web/API 支持多个 Provider 实例，协议族为 `openai_compatible`、`anthropic` 或 `gemini`。V1 发布证据使用真实 OpenAI-compatible Provider；原生 Anthropic/Gemini 保留合同测试，不要求真实凭据。

“自动审查”在“配置中心 → 治理与审批”中保存准确的 Provider ID + Model ID、启用状态、超时和策略版本；它与“模型服务”的连接配置保持职责分离。每次 Reviewer 调用冻结实际 Provider、Model 和策略版本；未配置、模型不在该 Provider 的启用目录、凭据不可用、超时或非法结构都会 fail-closed 并转人工审批。`GET /api/v1/diagnostics` 的 `auto_review.configured/available/reason` 是运维真源，`credential_issues` 会列出需重新录入的 Provider/MCP 名称、状态和修复路径，不返回 `credential_ref` 或 Secret。

## 10. MCP、Skill 与 Web Access

内建 `web_search` 与 `web_fetch` 是无需外部凭据的最低可用 Web Access 能力，不因可选 MCP 不健康而删除。资源中心支持当前用户全局或项目级 Skill、MCP 与 Agent 的校验、隔离、人工审核、启用、调用、停用和永久删除；活动 Run 引用时拒绝删除，历史快照与脱敏审计保留。MCP Schema、配置版本、凭据引用与健康状态会进入新 Run 快照。凭据原文不得进入 Manifest、API 返回、日志或普通导出；SQLite 只保存 AES-256-GCM 应用层密文。

Skill 菜单安装只接受单 Skill ZIP，不接受裸 `SKILL.md`、JSON 或 YAML；ZIP 在写入前校验唯一 `SKILL.md`、frontmatter、允许目录、文件数/体积、路径穿越、Unicode 冲突、符号链接、嵌套 Skill、依赖目录和隐藏载荷。MCP 不使用 ZIP：输入名称、`server.json`、规范化定义、URL、固定版本 npm/PyPI 包或本地命令，解析后只显示该传输所需字段，高级区可复核规范化 `MCPDefinition v1`。名称发现访问官方 Registry `https://registry.modelcontextprotocol.io`，生产环境需要允许对该域名的出站 HTTPS；Registry 只用于发现，歧义候选必须人工选择且安装前固定实际版本。模型不能静默安装或启用能力；在新 Thread 中用自然语言要求安装 MCP 时，Composer 会打开相同配置与确认面板。一个 MCP 的多项凭据以不透明加密集合写入数据库 Vault，运行时仅注入定义声明的环境变量或受控 Header。

### 10.1 Document Runtime

新用户和现有用户首次升级会安装 `document-runtime` 与 `presentations/pdf/documents/spreadsheets` 五个全局预置资源。用户停用状态会保留；删除后写入 tombstone，重启和升级都不会恢复。Runtime 或依赖缺失时资源中心显示 `degraded` 并提供显式安装/修复；模型不能安装、更新或删除 Runtime，也不能回退为临时解析脚本。删除共享 Runtime 前必须先删除所有声明 `document.runtime` 的 Skill，活动 Run 引用时仍会拒绝。

开发环境只有显式设置 `OKTAPOD_DOCUMENT_RUNTIME_DEV=1` 才会从 `Runtime/document-runtime/runtime.py` 启动，并通过 `OKTAPOD_DOCUMENT_RUNTIME_PYTHON` 指向开发 Python；需要使用宿主渲染器时，可显式设置 `OKTAPOD_DOCUMENT_RUNTIME_LIBREOFFICE` 和 `OKTAPOD_DOCUMENT_RUNTIME_TESSERACT`。这两个开发路径只接受绝对路径、普通文件且拒绝符号链接，不会放宽生产信任边界。生产环境只加载 DataRoot 的受信版本或二进制旁 `runtime/document-runtime` 签名包；Manifest 必须匹配平台、协议、Ed25519 信任 Key 和逐文件 SHA256。生产启动使用 Python `-I -B`；`-I` 会忽略 `PYTHONDONTWRITEBYTECODE`，因此显式使用 `-B`，避免 Python 字节码缓存改写已签名 Runtime 包。Runtime 不监听端口，使用独立 HOME/TEMP、空代理和受控文件句柄；缓存位于 DataRoot 的 `document-runtime/cache`，默认 1 GiB / 30 天 LRU，不进入 Backup/项目导出。

发布构建新增严格输入：`OKTAPOD_DOCUMENT_RUNTIME_STAGE` 指向包含自包含平台依赖和完整 `LICENSES/` 的目录；`OKTAPOD_DOCUMENT_RUNTIME_PYTHON`、`OKTAPOD_DOCUMENT_RUNTIME_LIBREOFFICE`、`OKTAPOD_DOCUMENT_RUNTIME_TESSERACT` 都是该目录内相对文件路径；`OKTAPOD_DOCUMENT_RUNTIME_SIGNING_KEY_FILE` 指向受保护 Ed25519 PKCS8 PEM，`OKTAPOD_DOCUMENT_RUNTIME_SIGNING_KEY_ID` 是部署信任 Key ID。`pnpm release:build` 缺少任一输入即失败；归档前还会用包内 Python 检查锁定依赖、LibreOffice、Tesseract `chi_sim + eng` 和四格式 25 次 RPC。构建只输出不含私钥的 `Build/document-runtime-trusted-keys.json`，`release:smoke` 将它写入隔离 DataRoot 并要求资源目录中的 Runtime 为 `available`，避免发布“主程序正常但文档能力暗中退化”的工件。

`auth.type=oauth` 使用官方 MCP Go SDK 的 Authorization Code + PKCE 流程。资源审核启用后点击“连接账号”，授权页在新窗口打开；回调地址按当前监听端口生成，例如默认 `http://127.0.0.1:8787/api/v1/mcp/oauth/callback`。Personal V1 要求浏览器与服务运行在同一台电脑；授权窗口被拦截时资源详情仍提供显式链接。取消只终止短期授权会话，不删除资源或项目文件。SDK 发现/注册得到的客户端配置、access token 和 refresh token 以 `oktapod.mcp-oauth/v1` 不透明信封进入 SecretStore，刷新后原位更新；HTTP 日志只记录 callback 路径，不记录含授权码的 query。手工 PAT/API token 使用 `bearer`，不要写入 Manifest 或自然语言消息。

Windows 上的 npm stdio MCP 会把 `npx.cmd`/`npx.ps1` 解析为 `node.exe` 直接启动 `npx-cli.js`，不经过命令行 shell。MCP 子进程的 HOME、APPDATA、npm 和 uv 缓存使用 `%TEMP%\oktapod-mcp-runtime`，防止社区服务器意外读取宿主配置；该目录是可重建运行缓存，服务停止后可由系统临时文件策略清理。

当用户在任务中明确点名 Tavily 等已连接 MCP 时，Run 同时移除内建 `web_search` 与 `web_fetch`，只向模型暴露该 MCP、计划/输入/审批控制工具以及完成交付所需的工作区工具，不静默混用外部数据源。完整 Annotation 显式声明非只读、破坏性或非幂等，以及快照外未知工具时按 L3 逐次审批；已审核、启用并冻结到 Run 的语义检索工具即使缺少可选 Annotation，也按只读幂等 L2 保存 Receipt，在“完全访问”下不再重复确认。Tavily 是可选首选，不是 V1 核心依赖。

## 11. 数据库 Vault、备份与跨机器恢复

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

## 12. 执行安全边界

默认执行姿态为 `governed_host`：

- Native 文件工具限定项目 Workspace，并阻止路径逃逸与 symlink 穿越；
- 子进程使用隔离 HOME，清理 SSH/AWS/Git/Provider 凭据环境；
- Git 禁用宿主配置、credential helper、prompt 与 hooks；
- 命令有超时、取消与进程树终止；
- 写文件、命令和 Git 修改受 Policy、状态哈希、审批、Receipt 与验证治理。

这不是容器或 OS 级强沙箱。宿主进程仍可能访问宿主可读资源，因此 UI 明示“宿主受控 · 非强沙箱”。通用进程的网络默认拒绝、文件挂载白名单、CPU/内存硬隔离仍属于后续强沙箱；内建 `web_fetch` 已单独实施 DNS/Dial/重定向/内容边界，不代表任意宿主进程获得同等级网络隔离。

项目文件在项目右侧“任务面板”查看：“工作区”是可变文件，“项目文件”是上传或明确保存、可供同项目后续任务复用的 Asset，“任务输出”是不可变 Artifact；“变更”仅显示 Git 差异。“保存到项目文件”不会向项目外发布。Personal Profile 的 Diagnostics 会显示数据根和 Workspace 根的宿主绝对路径，Team Profile 不暴露该路径。脚本生成的文件先在“工作区”可见；只有经 `artifact_capture` 校验登记后才会进入“任务输出”并可作为任务交付证据。

新项目默认将每个任务限制为 80 次模型调用、1,000,000 Token；单次模型输出先使用 8,192 Token，仅在截断恢复时提升到 16,384/32,768。该值来自 30,000 汉字深度报告的真实极限回归（67 次调用、约 840k 输入输出 Token）并保留约 20% 调用余量。项目“资源配额”可显式调整或设为不限额；已有 Run 使用创建时冻结的值，不被配置修改追溯覆盖。Run 额度不足时只创建一个可解释的预算决策，显示当前用量和建议新增额度；用户可选择按建议扩展、用现有结果收尾、本 Run 不限额或停止任务。停止后保留已有文件和 Receipt，并进入终态，不会反复申请预算。

## 13. 运维

```powershell
$Binary = 'C:\Program Files\Oktapod\releases\1.0.0\oktapod.exe'
$Config = 'C:\ProgramData\Oktapod\config.yaml'
& $Binary --config $Config preflight
& $Binary --config $Config backup -output E:\backups\oktapod-20260822
& $Binary --config C:\ProgramData\Oktapod\rollback.yaml restore -input E:\backups\oktapod-20260822
& $Binary --config $Config cleanup
```

```bash
BINARY=/opt/oktapod/releases/1.0.0/oktapod
CONFIG=/etc/oktapod/config.yaml
sudo -u oktapod "$BINARY" --config "$CONFIG" preflight
sudo systemctl stop oktapod
sudo -u oktapod "$BINARY" --config "$CONFIG" backup -output /srv/backups/oktapod-20260822
# restore 使用指向全新空 DataRoot 的 rollback.yaml
sudo -u oktapod "$BINARY" --config /etc/oktapod/rollback.yaml restore -input /srv/backups/oktapod-20260822
sudo systemctl start oktapod
```

停止前等待正在执行的受治理任务到达检查点；升级前先执行正式 Backup 并确认恢复口令可用。`restore` 拒绝覆盖已有数据库，因此恢复或回滚配置必须指向新的空 DataRoot。恢复到新机器后先运行 `vault status`，再从隐藏 TTY、stdin 或受保护文件完成 `vault recover`。数据库迁移只前进，不承诺直接降级复用已迁移数据库。

## 14. 发布门禁与故障排查

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
- `WEB_FETCH_BLOCKED_URL`：目标不是公网 80/443，或私网 origin 未同时命中 Run 快照和当前启动白名单；检查设置并重启，新建 Run 后再试。
- `WEB_FETCH_REDIRECT_BLOCKED`：页面跳到其他 origin；确认返回的目标地址后由模型发起新的受治理 Fetch，私网目标需单独配置和审批。
- `WEB_FETCH_TOO_LARGE` / `WEB_FETCH_UNSUPPORTED_CONTENT_TYPE`：页面解压后超过 2 MiB，或返回 PDF/二进制；首批不会放宽模型参数绕过固定边界。
- `SQLITE_BUSY`：检查活跃 Worker/Lease；应用会有限重试，不要改成无限重试。
- `document runtime package is not installed`：确认归档解压为主程序同级的 `runtime/document-runtime`，而不是多套一层目录。
- `document runtime signature is not trusted`：确认配置指向与当前发布集合一起分发的 `trusted-keys.json`；不要通过关闭验证或使用开发模式绕过。
- 本地开发启动后所有预置文档 Skill 显示"需要修复"：通常是 `capabilities.trusted_keys_file` 未配置导致签名校验失败；把 `Build/document-runtime-trusted-keys.json` 复制到数据配置目录并在 `config.yaml` 的 `capabilities.trusted_keys_file` 中填写完整路径，重启后资源中心应全部转为"可用"。
- Linux 服务启动但自定义 npm/uv MCP 不可用：核心 Document Runtime 是自包含的，但自定义 MCP 仍依赖 systemd 服务用户可见的固定可执行路径；在资源定义中使用受审核绝对路径，或为 unit 配置最小 PATH 后重启。
- 预算申请反复出现：先确认运行的是 2026-08-13 之后的构建；新版本不会以“每次只增加 1 次模型调用”的方式循环申请，失败或停止的终态任务也不会继续占用“待我处理”徽标。
- 版本不符：运行 `<binary> version`，不要发布显示为 `dev` 的开发构建。

历史部署方案保存在 `Doc/archive/Oktapod_部署与运维指南_v1.0.md`。
