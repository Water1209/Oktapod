# Package — 依赖与打包

- 前端依赖：`node_modules/`（pnpm，根 workspace）
- 前端产物：`Src/web/dist/`（嵌入 Go 二进制）
- Go 依赖：`go.sum` 锁定，经 `go mod download`
- 开发构建：`pnpm build`，产出 `Build/oktapod[.exe]`，版本为 `dev`
- 发布构建：`pnpm release:build`，在当前原生平台产出：
  - `Build/oktapod-v<version>-<os>-<arch>[.exe]` 与 `.sha256`
  - `Build/document-runtime-v<version>-<os>-<arch>.zip|tar.gz` 与 `.sha256`
  - `Build/document-runtime-trusted-keys.json`
- `release:build` 必须提供目标平台自包含 Runtime stage、完整 `LICENSES/` 和仓库外 Ed25519 私钥；缺少输入会关闭式失败。
- 正式安装把 Runtime 解压到主程序同级 `runtime/document-runtime`，把信任键复制到配置引用的位置；普通 `pnpm build` 仅供开发，不是完整发布包。

发布打包与 Windows/Linux 安装流程见[发布配置](../Deploy/README.md)和[部署与运维指南](../Doc/Oktapod_部署与运维指南_v1.0.md)。
