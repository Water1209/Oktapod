# Oktapod 发布配置

当前 V1 只支持 Windows/Linux 原生发布二进制；不提供 Docker 或 Compose 发布路径。

- `.env.example`：后续兼容边界的环境变量说明，不能包含真实凭据。
- `config.yaml.example`：Personal/SQLite 的无 Secret 配置模板；正式部署必须设置明确的绝对 DataRoot 和 Runtime 信任键路径。
- 发布集合必须同时包含主程序、签名 `document-runtime` 归档、双方 `.sha256`、`document-runtime-trusted-keys.json` 和配置样例；只复制主程序会使文档资源进入 `degraded`。
- Windows 推荐版本目录位于 `C:\Program Files\Oktapod\releases\<version>`，配置和数据位于 `C:\ProgramData\Oktapod`；Linux 推荐 `/opt/oktapod/releases/<version>`、`/etc/oktapod` 和 `/var/lib/oktapod`。
- 原生构建、Hash 校验、Windows 启动、Linux systemd、烟测、升级和空 DataRoot 回滚见[部署与运维指南](../Doc/Oktapod_部署与运维指南_v1.0.md)。

未来若重新设计容器化，会作为新的运行时和安全方案单独评审，不能把旧命令视为当前支持。
