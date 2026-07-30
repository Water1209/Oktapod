# Package — 依赖与打包

- 前端依赖：`node_modules/`（pnpm，根 workspace）
- 前端产物：`Src/web/dist/`（嵌入 Go 二进制）
- Go 依赖：`go.sum` 锁定，经 `go mod download`
- 开发构建：`pnpm build`，产出 `Build/oktapod[.exe]`，版本为 `dev`
- 发布构建：`pnpm release:build`，产出 `Build/oktapod-v<version>-<os>-<arch>[.exe]` 与 `.sha256`

发布打包流程见 `Deploy/README.md`。
