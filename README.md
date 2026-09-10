# Oktapod

本地优先的 Agent 工作台，让任务执行、文件交付与操作审计留在自己的环境中。

**状态：V1.0.0 RC** · Windows / Linux · Go + React · SQLite · [AGPL-3.0](LICENSE)

[快速开始](#快速开始) · [安装与部署](Doc/Oktapod_部署与运维指南_v1.0.md) · [架构设计](Doc/Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md) · [验收状态](Doc/Oktapod_V1_验收状态与追踪矩阵.md)

## 功能

- **个人私有项目**：账户登录、项目隔离、实例维护者统一配置模型服务；同项目多会话并发，同会话消息持久排队。
- **可恢复任务**：语义计划、暂停与恢复、审批、预算和执行收据；重试展示具体原因，并读取最新计划状态。
- **代码与工作文件**：Workspace、脚本、Git 变更和文件预览；项目移除保留工作目录与历史。
- **文档交付**：通过预置 Skill 与签名 Document Runtime 读取、生成和渲染 PDF、DOCX、XLSX、PPTX；输出登记为可验证 Artifact。
- **可扩展能力**：Skill、MCP、Agent、Memory 与多 Provider；凭据统一存入数据库加密 Vault。
- **集成工作台**：对话旁查看文件、变更、用量、项目与追踪；HTML 单文件应用在隔离沙箱中支持交互。

V1 发布范围为个人私有项目与 SQLite；团队项目、PostgreSQL 和 OS 级强沙箱不属于当前发布范围。签名安装包与跨平台部署验收状态以[验收矩阵](Doc/Oktapod_V1_验收状态与追踪矩阵.md)为准。

## 快速开始

源码开发需要 **Node.js ≥22.12.0、pnpm 10.28.2、Go 1.26.5**。在仓库根目录执行：

```sh
pnpm install --frozen-lockfile
pnpm build
```

Windows 执行 `.\Build\oktapod.exe serve`；Linux 执行 `./Build/oktapod serve`。浏览器打开 <http://127.0.0.1:8787/>，首次注册账户成为实例维护者，在“设置 → 凭据与密钥库”初始化 Vault 后配置模型服务。

普通构建不包含文档依赖，缺少 Runtime 时文档能力会显示降级；完整安装请按[部署指南](Doc/Oktapod_部署与运维指南_v1.0.md)配置签名 Runtime。需要前端热更新时，分别运行 `go run ./Src/cmd/oktapod serve` 与 `pnpm dev`。

## 安装包制作与部署

发布方在目标 Windows/Linux 主机准备自包含 Python、LibreOffice、PDFium、Tesseract、许可证和仓库外签名私钥，再执行 `pnpm release:build` 与 `pnpm release:smoke`。完整输入、暂存目录和交付清单见[发布方构建](Doc/Oktapod_部署与运维指南_v1.0.md#21-发布方构建)。

部署方校验 SHA256，将主程序、签名 Runtime、信任公钥与配置样例安装到独立版本目录，初始化账户与 Vault，并验证 JSON 健康及文档能力。部署机器无需 Node.js、pnpm、Go 或单独安装 LibreOffice；Linux 仍需满足目标发行版的系统库与字体要求。当前交付原生文件集合，不提供 MSI、DEB、RPM 或 Docker 安装方式。

## 开发与贡献

提交前执行 `pnpm verify`；UI 流程变更执行 `pnpm test:e2e`，Ubuntu 并发检查执行 `pnpm test:race`。Windows 可将 `GOCACHE` 设置为 `$env:TEMP\go-build-cache`。贡献请说明问题、变更范围与验证结果，并遵守 [AGENTS.md](AGENTS.md) 的目录、契约与文档同步约定。

## 文档

| 文档 | 内容 |
|---|---|
| [部署与运维](Doc/Oktapod_部署与运维指南_v1.0.md) | 安装包制作、Windows/Linux 安装、升级、备份与回滚 |
| [总体架构](Doc/Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md) / [执行计划](Doc/Oktapod_V1_执行计划与验收门禁_V1.0.md) | 产品边界、架构决策与实施门禁 |
| [验收矩阵](Doc/Oktapod_V1_验收状态与追踪矩阵.md) / [发布说明](Doc/Oktapod_V1_发布说明_V1.0.0.md) | 实现状态、验证证据与已知限制 |
| [文档 Runtime](Doc/Oktapod_预置文档能力与_Artifact_Runtime_实现说明_V1.0.md) | 预置能力、签名协议与文件交付 |
| [会话与恢复](Doc/Oktapod_会话并发与任务恢复设计_V1.0.md) | 并发、队列、重试与恢复语义 |

## 许可证

项目采用 [GNU AGPL v3](LICENSE)。随文档 Runtime 分发的第三方组件保留各自许可证，详见 [第三方声明](Runtime/document-runtime/THIRD_PARTY_NOTICES.md) 及发布包内 `LICENSES/`。
