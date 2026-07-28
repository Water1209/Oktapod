# Test — 黑盒测试

- `e2e/`：Playwright 端到端测试（启动真实二进制 + 浏览器，不引用 Go internal 包）。

白盒集成测试位于 `Src/tests/integration/`（Go `internal/` 可见性约束要求其在源码树内）。

OHCS（Oktapod Harness Compatibility Suite）同样位于
`Src/tests/integration/`，测试名统一使用 `TestOHCSGoldenTrace*`。Golden
Trace 只比较稳定的消息、事件、工具和持久化顺序，不固定时间戳、请求 ID
或模型自由文本。冻结的 Pi 行为基线是 `b1c444d9`。
