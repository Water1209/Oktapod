# Src — 源码

| 目录 | 说明 |
|---|---|
| `cmd/oktapod` | 主程序入口（serve / version / preflight / backup / restore / export / import / cleanup / model / context） |
| `internal/agentloop` | Agent Loop 的稳定状态、事件与终止判断 |
| `internal/harness` | Durable Harness 的授权、检查点与恢复契约 |
| `internal/` | 其余内部实现：adapters、application、bootstrap、domain、runtime |
| `api/` | OpenAPI 契约与 oapi-codegen 配置 |
| `db/` | sqlc 查询与 SQLite/PostgreSQL 迁移 |
| `web/` | 前端（React + Vite，embed 进二进制） |
| `tests/integration/` | 白盒集成测试（引用 internal，必须在 Src 树内） |

Go module 根在仓库根（`github.com/oktapod/oktapod`），包路径为 `github.com/oktapod/oktapod/Src/<dir>`。
