# Envconfig — 环境配置说明

构建期配置文件位于仓库根（工具链约定，不可移动）：

- `go.mod` / `go.sum`：Go module
- `sqlc.yaml`：sqlc 代码生成（迁移与查询指向 `Src/db/`）
- `package.json` / `pnpm-workspace.yaml` / `pnpm-lock.yaml`：前端 workspace（包指向 `Src/web`）
- `.gitignore`

运行时环境变量样例见 `Deploy/.env.example`，完整说明见 `Doc/Oktapod_部署与运维指南_v1.0.md`。
