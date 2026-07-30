# Oktapod

> **本地优先、可控、可审计、可恢复的智能开发 Agent**

Oktapod 是一个面向个人开发者和小型技术团队的本地优先 Coding Agent。它可以理解代码仓库、修改文件、运行验证、展示变更、请求必要审批，并在异常或重启后恢复任务。

---

## 快速开始

### 环境要求

- Node.js `>= 22.12.0`
- pnpm `10.28.2`
- Go `1.26.5`

### 构建与启动

```powershell
# 安装依赖
pnpm install

# 构建前端 + Go 二进制
pnpm build

# 启动服务（默认 127.0.0.1:8787，SQLite）
.\Build\oktapod.exe serve
```

浏览器访问 `http://127.0.0.1:8787/`，首次使用请在登录页注册本地账户。

### 开发模式

```powershell
# 终端 1：后端 API（8787）
go run ./Src/cmd/oktapod serve

# 终端 2：前端热更新（5173）
pnpm dev
```

---

## 文档地图

| 文档 | 说明 |
|------|------|
| **[V1 总体架构与产品优化方案（审定版）](Doc/Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md)** | **当前执行基线**：V1 定位、范围、架构原则、ADR |
| **[V1 执行计划与验收门禁](Doc/Oktapod_V1_执行计划与验收门禁_V1.0.md)** | **当前执行基线**：差距驱动批次、验收门禁、执行纪律 |
| [V1 验收状态与追踪矩阵](Doc/Oktapod_V1_验收状态与追踪矩阵.md) | 发布状态与功能完成度追踪 |
| [PRD v1.1（完整产品蓝图）](Doc/Oktapod_PRD_v1.1_轻量化架构优化版.md) | 历史产品蓝图，V1 范围以审定版为准 |
| [三阶段递进式架构设计](Doc/Oktapod_三阶段递进式架构设计_V1.0.md) | 长期演进路线图（Phase 1→2→3） |
| [部署与运维指南](Doc/Oktapod_部署与运维指南_v1.0.md) | 部署、Provider 配置、MCP 配置、运维命令 |

### 研究文档（`Doc/research/`）

| 文档 | 说明 |
|------|------|
| [pi-vs-oktapod-agent-loop.md](Doc/research/pi-vs-oktapod-agent-loop.md) | Agent Loop 与提示词语义对比 |
| [pi-vs-oktapod-tools-security.md](Doc/research/pi-vs-oktapod-tools-security.md) | 工具体系与安全策略对比 |
| [pi-vs-oktapod-provider-model.md](Doc/research/pi-vs-oktapod-provider-model.md) | Provider 与模型层对比 |

### 归档文档（`Doc/archive/`）

已被 V1 审定版取代或暂不进入 V1 主路径的文档，保留供参考。

---

## 验证

```powershell
# 常规全量验证（Go test/vet、前端 lint/test/build、SQLite 迁移、OpenAPI drift）
pnpm verify

# CI 全门禁（包含 race；本机需 CGO 与 C 编译器）
pnpm verify:ci

# E2E 测试
pnpm test:e2e
```

---

## 项目规范

详见 [AGENTS.md](AGENTS.md)。
