# Src — 源码

| 目录 | 说明 |
|---|---|
| `cmd/oktapod` | 主程序入口（serve / preflight / backup / restore / export / import / cleanup / model） |
| `internal/` | 内部实现：adapters（db/http/model/mcp/websearch/workspace/secretstore）、application（领域服务）、bootstrap（配置）、domain（核心类型）、runtime（worker/scheduler） |
| `api/` | OpenAPI 契约与 oapi-codegen 配置 |
| `db/` | sqlc 查询与 SQLite 迁移 |
| `web/` | 前端（React + Vite，embed 进二进制） |
| `tests/integration/` | 白盒集成测试（引用 internal，必须在 Src 树内） |

Go module 根在仓库根（`github.com/oktapod/oktapod`），包路径为 `github.com/oktapod/oktapod/Src/<dir>`。
