# Oktapod 发布配置

当前 V1 只支持 Windows/Linux 原生发布二进制；不提供 Docker 或 Compose 发布路径。

- `.env.example`：后续兼容边界的环境变量说明，不能包含真实凭据。
- `config.yaml.example`：Personal/SQLite 的无 Secret 配置模板；正式部署必须设置明确的绝对 DataRoot 和 Runtime 信任键路径。
- 发布集合必须同时包含主程序、签名 `document-runtime` 归档、双方 `.sha256`、`document-runtime-trusted-keys.json` 和配置样例；只复制主程序会使文档资源进入 `degraded`。
- 当前已构建的 Runtime 资源包位于仓库 `Build/`：`document-runtime-v1.0.0-windows-amd64.zip`（SHA256 `5bd93247218ee79746d4ab3a3cf2aea18f6109c0247f604fac8ed503ba12798c`）和 `document-runtime-v1.0.0-linux-amd64.tar.gz`（SHA256 `82e90cf26e4e1d90a87fce3c0d16a3a43c13f45d93c8bc8ddf4586eb084ed2c0`）；两者共用 `document-runtime-trusted-keys.json` 中的 Key ID `oktapod-local-release-20260824`。
- 当前已补齐两平台主程序：Windows `oktapod-v1.0.0-windows-amd64.exe`（SHA256 `3d5e87591cc7c8620269f0c41dc05158667fb8773be819440c4d13a9dd025aed`），Linux `oktapod-v1.0.0-linux-amd64`（SHA256 `45407a2c0808378fc15d4068e1b182b58c0f0006972758ebcbafdd1a30a35de8`）；两者均报告版本 `1.0.0`。
- 以上 Runtime 包已通过包内依赖检查和四格式 25 次 RPC smoke；交付真实生产前应将本次本机生成的签名私钥安全备份，并按组织发布密钥流程替换或确认该 Key ID，不得把私钥复制到仓库或部署包。
- Windows 推荐版本目录位于 `C:\Program Files\Oktapod\releases\<version>`，配置和数据位于 `C:\ProgramData\Oktapod`；Linux 推荐 `/opt/oktapod/releases/<version>`、`/etc/oktapod` 和 `/var/lib/oktapod`。
- 原生构建、Hash 校验、Windows 启动、Linux systemd、烟测、升级和空 DataRoot 回滚见[部署与运维指南](../Doc/Oktapod_部署与运维指南_v1.0.md)。

未来若重新设计容器化，会作为新的运行时和安全方案单独评审，不能把旧命令视为当前支持。
