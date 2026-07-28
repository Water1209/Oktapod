# Oktapod 部署

- `Dockerfile`：多阶段构建，产出 team 模式二进制。
- `docker-compose.team.yml`：PostgreSQL + Oktapod team 模式一键启动。
- `postgres/`：数据库初始化脚本目录（RLS、扩展等）。
- `.env.example`：环境变量样例。

Team Compose 仅用于后续单节点试点。使用前将 `.env.example` 复制为 `Deploy/.env`，为 `OKTAPOD_POSTGRES_PASSWORD`、`OKTAPOD_DATABASE_URL` 和 `OKTAPOD_TEAM_ADMIN_TOKEN` 填入独立随机值；仓库不提供共享默认凭据。

详见 `Doc/Oktapod_部署与运维指南_v1.0.md`。
