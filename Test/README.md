# Test — 发布与黑盒测试

- `e2e/`：Playwright 端到端测试（启动真实二进制 + 浏览器，不引用 Go internal 包）。
- `build-release.mjs`：按根 `package.json` 版本构建本机发布二进制并生成 SHA256。
- `release-smoke.mjs`：用独立端口与数据目录启动发布二进制，再运行产品烟测。
- `product-smoke.mjs`：验证健康、注册、鉴权、运行时版本与个人项目创建；协议和部署形态无关。
- `runtime-performance-baseline.mjs`：只在带 `.oktapod-perf-isolated` 标记的隔离数据目录对应服务上运行 12 个 Runtime 固定场景；显式指定 1～30 次迭代，输出逐 Run JSONL 与 p50/p95 摘要，不读取或写入用户现有 AppData 数据库。

白盒集成测试位于 `Src/tests/integration/`（Go `internal/` 可见性约束要求其在源码树内）。

OHCS（Oktapod Harness Compatibility Suite）位于 `Src/tests/integration/`
及相邻的 application/runtime 契约测试中。Golden Trace 与语义测试只比较
稳定的消息、事件、工具和持久化顺序，不固定时间戳、请求 ID 或模型自由
文本。冻结的 Pi 行为基线是 `b1c444d9`。
