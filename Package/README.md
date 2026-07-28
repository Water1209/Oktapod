# Package — 依赖与打包

- 前端依赖：`node_modules/`（pnpm，根 workspace）
- 前端产物：`Src/web/dist/`（嵌入 Go 二进制）
- Go 依赖：`go.sum` 锁定，经 `go mod download`

发布打包流程见 `Deploy/README.md`。
