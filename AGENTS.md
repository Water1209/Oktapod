# Oktapod 项目规范

## 项目目标

Oktapod 是一套面向个人与企业团队的多用户 Agent OS，不按个人版和团队版拆成两套软件。用户登录同一产品后可创建个人私有项目或团队项目；个人项目仅创建者可见。当前 V1.0.0 只发布个人私有项目与 SQLite 主路径，团队项目与 PostgreSQL 作为后续兼容边界，不进入 V1 主流程和完成度声明。

## 目录约定

- `Doc/`：保存正式需求、调研和架构文档。正式文件使用清晰的中文名称；同一主题若有完整版与 MVP 版，文件名必须显式区分。
- `Doc/assets/`：保存正式文档引用的可迁移图示；文件名使用 `fig-序号-英文主题`，仅保留被交付文档引用的源图，不放渲染页和临时预览。
- `Doc/research/`：保存可复核的研究 brief、证据映射、来源日志和草稿。文件名使用小写英文与短横线；这些是研究支撑材料，不是最终交付物。
- 临时渲染图、LibreOffice 配置、缓存和一次性脚本不得留在仓库；统一放在任务临时目录，交付前清理。

### 代码目录

- `Src/api/`：OpenAPI、事件 Schema、Capability manifest 等跨端契约；生成代码不得手工修改。
- `Src/cmd/oktapod/`：唯一产品二进制入口；子命令只负责参数解析和依赖装配。
- `Src/internal/domain/`：领域模型与不变量，不依赖 HTTP、SQL、Provider SDK 或操作系统实现。
- `Src/internal/application/`：命令、查询、事务边界和用例编排；跨领域写操作从这里进入。
- `Src/internal/runtime/`：Worker、调度、恢复、上下文构建和确定性验收。
- `Src/internal/adapters/`：数据库、Blob、Workspace、模型、MCP、Secret、HTTP/SSE 与 Runner 适配器。
- `Src/internal/bootstrap/`：Personal/Team Profile 装配与生命周期，不放业务规则。
- `Src/db/`：按 SQLite/PostgreSQL 分开的迁移与查询；双方言共享契约测试，不用 ORM 隐藏差异。
- `Src/web/`：React/TypeScript 前端；按业务 `features` 组织，生成 API 客户端放 `Src/web/src/shared/api/generated/`，仅 UI 浏览器闭环放 `Test/e2e/`。
- `Src/tests/integration/`：跨模块契约、集成、故障、安全的白盒集成测试；`Test/e2e/`：Playwright 端到端测试。
- `Deploy/`：原生发布配置示例和运维入口；示例不得包含真实 Secret。当前 V1 不提供 Docker 或 Compose 发布路径。
- `Runtime/`：保存随发布包交付、但不嵌入 Go 主程序的受信运行时源码、锁定清单与打包入口；平台构建产物、缓存、临时用户目录和第三方工具二进制不得提交，统一输出到被忽略的 `Build/runtime/`。

新增代码目录必须符合上述边界；如果确需新一级目录，先在这里说明用途、命名和清理规则。构建产物统一进入被忽略的 `Build/`、`dist/`、`bin/`、`coverage/` 或系统临时目录，不在源码目录生成临时文件。`sqlc` 生成代码固定写入 `Src/internal/adapters/db/sqlite/sqlc/`，仓库根目录的 `internal/` 视为旧配置产生的本机残留，不得提交。

## 文档要求

- 正式文档默认以 Markdown（`.md`）作为主交付与单一内容源，便于 AI 读取、版本比较和协作；仅在用户明确要求时额外生成 DOCX 或 PDF。
- 先给结论，再展开范围、用户影响、取舍和验收标准。
- 产品需求必须区分事实、产品决策、假设与待验证项；竞品能力优先引用官方文档或源码。
- PRD 至少覆盖目标与非目标、角色、范围、流程、功能需求、权限安全、数据与审计、非功能需求、指标、里程碑、风险、验收和追踪矩阵。
- MVP 必须能独立验收，避免把完整产品蓝图简单删减成愿望清单。

## 技术与安全边界

- 后端以 Go 预研，前后端分离，优先采用成熟开源组件并记录采用理由与替代方案。
- Windows 与 Linux 均为一等平台；密钥、Token、密码不得进入仓库或文档示例。
- 涉及命令、文件、网络、模型调用和外部工具时，默认采用最小权限、可审批、可审计、可恢复设计。
- Provider 地址可以进入非敏感配置；Provider、MCP 与 OAuth 凭据只允许进入数据库加密 SecretStore，不得出现在环境变量、源码、测试夹具、日志、截图或文档示例中。Team/PostgreSQL 的数据库连接凭据属于数据库打开前的 Bootstrap Secret 例外，由部署系统注入。

## 验证与清理

- `.docx` 必须完成结构检查并渲染为逐页图片做视觉复核。
- 来源链接、编号、需求 ID、表格标题和交叉引用在交付前统一检查。
- 删除空白表、占位符、失败草稿、临时文件和渲染中间产物；保留最终文档与必要的研究证据。

## 关键模块设计可参照
记忆、skills、系统提示词、Agent、hook、agent loop、工具、插件、agent team、交互方式等设计思路，可参照：
### 主要参照： pi源码：E:\git\pi
### 其他参照：
- claude code源码：E:\code\claude-code-main
- openclaw源码：E:\git\openclaw
- hermes源码：E:\git\hermes-agent
- grok-build源码：E:\git\grok-build
- goclaw源码：E:\git\goclaw
- cliproxyapi源码：E:\git\CLIProxyAPI
- workbuddy:D:\Program Files\WorkBuddy\resources
### 界面交互参照：codex的风格和交互模式

---

## 文档同步规范（强制执行）

> **重要约定**：每次代码变更完成后，必须同步更新相关文档，确保文档与代码状态始终一致。

### 文档更新范围

| 变更类型 | 需要更新的文档 |
|---------|---------------|
| 功能新增/修改 | README.md（功能描述）+ 对应设计文档 |
| 部署/启动变更 | `Doc/Oktapod_部署与运维指南_v1.0.md` |
| API 接口变更 | `Src/api/openapi.yaml` + 相关文档 |
| 配置项新增/修改 | README.md + 部署指南 |
| UI 交互变更 | 相关产品设计文档 |
| 修复重要 Bug | 验收文档状态更新 |

### 文档检查清单（提交前必须确认）

- [ ] 变更内容已在 README 对应章节说明
- [ ] 如涉及启动/部署，`Doc/Oktapod_部署与运维指南_v1.0.md` 已更新
- [ ] 新增配置项已在文档中列出说明
- [ ] 命令行参数变化已同步到文档示例
- [ ] 验收状态矩阵已对应更新

### 当前文档版本追踪

| 文档 | 版本 | 最后更新 | 说明 |
|------|------|---------|------|
| README.md | 最新 | 每次变更同步 | 项目门面，60 行内 |
| `Doc/Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md` | V2.13 | 2026-09-04 | **当前执行基线**：V1 定位、范围、架构、ADR |
| `Doc/Oktapod_V1_执行计划与验收门禁_V1.0.md` | V1.8 | 2026-09-04 | **当前执行基线**：差距驱动批次、验收门禁 |
| `Doc/Oktapod_V1.1_Agent_Runtime_优化执行计划.md` | V1.9 | 2026-08-24 | V1.1 Runtime、语义计划、Web Access、文档 Runtime、资源/Provider 生命周期与 Task 流式模型传输实施证据 |
| `Doc/Oktapod_预置文档能力与_Artifact_Runtime_实现说明_V1.0.md` | V1.0 | 2026-08-22 | 预置资源、固定文档协议、签名 Runtime、格式矩阵与未关闭门禁 |
| `Doc/Oktapod_部署与运维指南_v1.0.md` | V1.1.1 | 2026-08-22 | Windows/Linux 原生部署、签名 Runtime、升级回滚与运维 |
| `Doc/Oktapod_V1_验收状态与追踪矩阵.md` | V1.23 | 2026-09-04 | 发布状态与功能完成度唯一权威 |
| `Doc/Oktapod_V1_发布说明_V1.0.0.md` | V1.0.0 RC | 2026-08-22 | 发布范围、限制、安装和回滚 |
| `Doc/Oktapod_PRD_v1.1_轻量化架构优化版.md` | V1.1 | 历史产品蓝图 | 完整产品愿景，V1 范围以审定版为准 |
| `Doc/Oktapod_三阶段递进式架构设计_V1.0.md` | V1.0 | 架构基线 | 长期演进路线图（Phase 1→2→3） |

> 归档文档位于 `Doc/archive/`，已被 V1 审定版取代或暂不进入 V1 主路径。

### 记忆点

下次进入项目时，优先检查：
1. 当前服务运行状态（端口、PID、健康检查）
2. 最近一次变更的功能点
3. 对应文档是否已同步
4. 模型配置是否生效
