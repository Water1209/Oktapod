# Oktapod 部署

- `Dockerfile`：V1 个人模式镜像；多阶段构建 Web 与 Go，非 root 运行，默认版本 `1.0.0`。
- `docker-compose.team.yml`：PostgreSQL + Oktapod team 模式试验边界，不属于 V1 发布路径。
- `postgres/`：数据库初始化脚本目录（RLS、扩展等）。
- `.env.example`：环境变量样例。

V1 镜像构建与启动：

```powershell
docker build --build-arg OKTAPOD_VERSION=1.0.0 -f Deploy/Dockerfile -t oktapod:1.0.0 .
docker run --rm -p 8787:8787 -v oktapod-data:/data oktapod:1.0.0
```

Team Compose 仅用于后续单节点试点。使用前将 `.env.example` 复制为 `Deploy/.env`，为所有数据库与管理凭据填入独立随机值；仓库不提供共享默认凭据。

详见 `Doc/Oktapod_部署与运维指南_v1.0.md`。
