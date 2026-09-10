# Oktapod V1.0.0 发布说明

> 状态：Release Candidate
> 候选日期：2026-08-13
> 文档更新：2026-09-10
> 发布范围：个人私有项目 / SQLite / 单 Agent 主链路

## 1. 发布判断

V1.0.0 已具备形成 Release Candidate 的产品闭环。代码行数和目录纯度不作为发布门禁；门禁只判断用户主链路、安全不变量、恢复语义和可复核工件。

正式 tag 与 push 前仍必须由 Ubuntu CI 通过 race，并由 Ubuntu 与 Windows CI 通过原生产品烟测。当前仓库不会自动 tag 或 push。

当前源码已补充实例维护者权限、会话级并发排队、文档预览与计划重试恢复。Windows 本地验证通过；当前修复尚未重新生成双平台签名发布集合，旧安装包记录不代表当前提交。完整结论以[验收矩阵](Oktapod_V1_验收状态与追踪矩阵.md)为准。

## 2. 用户可用能力

- 注册、登录和仅创建者可见的个人私有项目；首次注册账户为实例维护者，统一管理公共模型与 Vault，普通成员只管理自己的项目与偏好；
- 同项目不同会话可并发，同会话消息持久排队；退出或会话失效时清理当前认证范围的工作区缓存；
- Office 预览使用独立短路径转换目录；模型修正参数时读取最新持久化计划，时间线展示分类重试原因；
- 在单一 Thread 内完成目标理解、持久计划、文件读改、命令验证和流式结果；
- 用户发送内容先持久化为可见消息；附件、显式 Skill、Steering 与下一轮队列状态不会因模型错误或快速连发而丢失；
- Composer 邻接审批、输入、阻塞恢复、Steering、Next Turn 与 Abort；同计划项动作可批量审批但保留逐项冻结参数、Hash 和审计；
- 预算不足时可按明确增量扩展、使用现有结果收尾、将本 Run 设为不限额或停止并保留证据，不再按单次调用循环申请；
- Work/Code 共用 Workspace、命令、Git、Skill、MCP 和 Artifact 执行内核，场景不改变审批风险；
- “工作区 / 项目文件 / 任务输出”分层展示可变文件、可复用输入和不可变交付；附件可直接关联已有项目文件；PDF 交付必须通过签名、MIME、Hash 与 Artifact 完成门禁；
- V1.1 预览已接入可卸载 `presentations/pdf/documents/spreadsheets` 与共享签名 `document-runtime`：支持固定 `document_*` 协议、修订/渲染/Artifact 主链路；跨平台签名包、性能 P95、复杂编辑和完整恶意夹具未关闭，因此不计入 V1.0.0 完整办公套件声明；
- 首页工作台、项目库、项目切换器、账号菜单、中文设置与可最大化任务面板形成一致导航；
- Provider API 目录与用户启用模型分离；Reviewer 保存准确的 Provider + Model + 策略版本，未配置时自动审查明确禁用并关闭式转人工；
- 配置中心按模型服务、治理审批、运行上下文、费用用量、凭据密钥库、数据备份和外观偏好拆分；Provider 连接、密钥、模型一次保存，Reviewer 作为独立安全边界配置；
- Provider、MCP 多字段凭据和 OAuth Token 使用每组织 DEK、每记录独立 Nonce 和 AAD 的 AES-256-GCM 数据库 Vault；重启自动解锁，正式 Backup 可用恢复口令跨机器恢复；
- Changes 的 Run 开始 / Git HEAD / Git Index 三基线，按文件和 Hunk 查看；内部 `.oktapod` 路径关闭式排除，明确的运行缓存单独折叠且不进入变更操作；
- 接受成果、撤销所选文件、选择文件提交；撤销和提交会创建受治理任务，不由浏览器直接改写 Git；
- Policy、ExecutionAuthorization、状态哈希、Receipt、Checkpoint 与重启恢复；
- 内建 Workspace/Git/搜索工具，旧工具名与简洁别名兼容；
- 全局/项目级 Skill、Package、MCP 的校验、审核、启用、调用和 Run 快照；
- OpenAI-compatible 真实 Provider 主链路；Anthropic/Gemini 保留协议合同。

## 3. 安全说明

默认是 `governed_host`：隔离 HOME 与凭据环境、限制工作区路径、禁用宿主 Git 凭据与 hooks、设置超时并终止进程树。它不是容器或 OS 级强沙箱；需要处理不可信仓库时，应在额外隔离的主机或容器中运行。

首次启动没有共享默认账号、密码或 Provider Key。Provider/MCP/OAuth 凭据只能写入数据库 Vault，SQLite 中始终是应用层密文；操作系统 Keyring 和模型环境变量不再参与产品状态。`bootstrap.key` 只包装组织 DEK，正式 Backup 不包含它或恢复口令。

## 4. 已知限制与延期

以下项目不阻塞 V1.0.0 个人版发布：

- Team 项目主流程、PostgreSQL 发布门禁和多节点运行；
- 容器/OS 强沙箱、网络默认拒绝、CPU/内存硬隔离；
- 交互式 xterm；命令仅以受治理任务运行；
- Pi 各历史格式精确导入；
- 原生 Anthropic/Gemini 真实凭据门禁；
- Skill `same-capability` 的确定性候选排名与跨 Provider 参数转换；
- `run_command` 的逐块 stdout/stderr 直播；完成事件和完整长输出 Artifact 已可恢复；
- Linux 真实浏览器矩阵与完整企业级故障注入，进入后续加固。
- DEK 轮换 UI、团队/PostgreSQL Vault 发布路径和外部 KMS；当前 Schema 保留 `key_version`，但不进入 V1 完成度。

## 5. 构建与校验

先按[部署指南第 2.1 节](Oktapod_部署与运维指南_v1.0.md#21-发布方构建)准备目标平台自包含依赖、完整许可证与仓库外 Ed25519 签名输入，设置所列环境变量后执行以下命令。缺少发布输入时不得将部分输出作为正式安装包。

```powershell
pnpm install --frozen-lockfile
$env:GOCACHE="$env:TEMP\go-build-cache"
pnpm verify
pnpm test:e2e
pnpm release:build
pnpm release:smoke
.\Build\oktapod-v1.0.0-windows-amd64.exe version
```

发布构建生成：

```text
Build/oktapod-v1.0.0-<os>-<arch>[.exe]
Build/oktapod-v1.0.0-<os>-<arch>[.exe].sha256
Build/document-runtime-v1.0.0-<os>-<arch>.zip|tar.gz
Build/document-runtime-v1.0.0-<os>-<arch>.zip|tar.gz.sha256
Build/document-runtime-trusted-keys.json
```

以下是 2026-08-13、引入签名 Document Runtime 硬门禁之前的历史 Windows 基线工件，不代表当前工作树已形成可部署发布集合：

```text
Build/oktapod-v1.0.0-windows-amd64.exe
SHA256 4eed8ba5fb8f3918d07ecc07c735e568e957b3f33c13435fd6278f50257d7865
```

2026-08-13 当时的本地门禁：`pnpm verify` 通过（Web 28/28），Playwright foundation 初始化 1/1 + 服务重启自动解锁 1/1、turn 1/1、success 26/26 通过；当时发布构建与独立临时数据目录产品烟测完成健康、注册、Session/CSRF、版本诊断和个人项目创建。当前签名 Runtime 发布门禁以验收矩阵为准。

最终门禁证据与未运行项以《Oktapod V1 验收状态与追踪矩阵》为唯一真源。

## 6. 升级与回滚

升级前运行正式 Backup V2、保存恢复口令、配置、信任键和完整旧版本目录。Windows/Linux 都采用版本目录旁路安装；升级后先检查 `<binary> version`、`config doctor`、`vault status`、诊断页、五个文档预置资源和个人项目读取。若需要回滚，停止新版本，用升级前备份恢复到新的空 DataRoot，再以旧二进制、旧信任键和回滚配置启动；不要让旧程序直接复用已由新版本迁移的数据库。此版本不读取、迁移或删除 Windows Credential Manager、Linux Secret Service 或 macOS Keychain 中的旧 Secret；Provider/MCP 凭据缺失时需要重新录入，OAuth MCP 需要重新授权。
