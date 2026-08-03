# Test — 发布与黑盒测试

- `e2e/`：Playwright 端到端测试（启动真实二进制 + 浏览器，不引用 Go internal 包）。
- `build-release.mjs`：按根 `package.json` 版本构建本机发布二进制并生成 SHA256。
- `release-smoke.mjs`：用独立端口与数据目录启动发布二进制，再运行产品烟测。
- `product-smoke.mjs`：验证健康、注册、鉴权、运行时版本与个人项目创建；协议和部署形态无关。

白盒集成测试位于 `Src/tests/integration/`（Go `internal/` 可见性约束要求其在源码树内）。

OHCS（Oktapod Harness Compatibility Suite）位于 `Src/tests/integration/`
及相邻的 application/runtime 契约测试中。Golden Trace 与语义测试只比较
稳定的消息、事件、工具和持久化顺序，不固定时间戳、请求 ID 或模型自由
文本。冻结的 Pi 行为基线是 `b1c444d9`。
