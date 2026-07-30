# Oktapod

> **V1.0.0 Release Candidate：本地优先、可控、可审计、可恢复的智能开发 Agent**

Oktapod V1 面向注册用户的个人私有项目。用户在一个 Thread 中描述目标，Agent 可计划、读改文件、运行验证、展示 Diff、请求审批，并支持接受、撤销、选择提交和异常恢复。团队项目与 PostgreSQL 保留兼容边界，不进入 V1 发布完成度。

## 快速开始

环境要求：Node.js `>=22.12.0`、pnpm `10.28.2`、Go `1.26.5`。

```powershell
pnpm install --frozen-lockfile
pnpm build
.\Build\oktapod.exe serve
```

浏览器访问 `http://127.0.0.1:8787/`，首次使用在登录页注册本地账户。默认服务仅监听本机并使用 SQLite；Provider Key 只通过 SecretStore 或环境变量注入。

开发模式：

```powershell
# 终端 1
go run ./Src/cmd/oktapod serve
# 终端 2
pnpm dev
```

## 发布构建

```powershell
pnpm release:build
pnpm release:smoke
.\Build\oktapod-v1.0.0-windows-amd64.exe version
```

发布构建会生成带版本的本机二进制和 `.sha256` 校验文件；普通 `pnpm build` 仍是开发构建，运行时版本显示为 `dev`。

## 文档

| 文档 | 用途 |
|---|---|
| [V1 发布说明](Doc/Oktapod_V1_发布说明_V1.0.0.md) | 发布范围、已知限制、安装与回滚 |
| [V1 总体架构](Doc/Oktapod_V1_总体架构与产品优化方案_审定版_V2.0.md) | 当前架构事实与演进边界 |
| [V1 执行计划](Doc/Oktapod_V1_执行计划与验收门禁_V1.0.md) | 差距批次、门禁与剩余工作 |
| [V1 验收矩阵](Doc/Oktapod_V1_验收状态与追踪矩阵.md) | 发布完成度唯一真源 |
| [部署与运维](Doc/Oktapod_部署与运维指南_v1.0.md) | 本机、Docker、Provider 与运维 |
| [PRD v1.1](Doc/Oktapod_PRD_v1.1_轻量化架构优化版.md) | 历史完整产品蓝图 |
| [三阶段架构](Doc/Oktapod_三阶段递进式架构设计_V1.0.md) | V1 之后的团队与集群演进 |

## 验证

```powershell
$env:GOCACHE="$env:TEMP\go-build-cache"
pnpm verify
pnpm test:e2e
```

Linux CI 额外执行 `pnpm verify:ci`（含 race）与 Docker 产品烟测。项目规则见 [AGENTS.md](AGENTS.md)。
