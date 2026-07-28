# Pi vs Oktapod — Provider 与模型层对比

> 基线：pi@b1c444d（packages/ai、packages/coding-agent/src/core/model-*）vs Oktapod 当前 Src/。
> 结论先行：**模型层全面落后于 Pi，按"直接用 Pi 语义"改造。**

## 对比表

| 维度 | Pi 基线 | Oktapod 当前 | 判断 |
|---|---|---|---|
| 模型能力字段 | `Model{id, name, api, provider, baseUrl, reasoning, thinkingLevelMap, input:[text,image], cost, contextWindow, maxTokens, compat}` | `ModelDescriptor{ID, ToolCalling, Vision, StructuredOutput, Reasoning, ContextWindow}` | **Pi 更好**。Pi 有 ThinkingLevelMap（每模型思考级别映射表，null=不支持）、成本费率、maxTokens、输入模态（text/image）、协议 compat 覆写。Oktapod 缺成本、Thinking 映射、maxTokens、图片模态标记。 |
| Thinking Level | `off/minimal/low/medium/high/xhigh/max`，`ThinkingLevelMap` 把级别映射到 Provider 特定值（如 Anthropic budget_tokens），不支持时透明降级 | 无。模型请求固定参数，无 Thinking 概念 | **Pi 更好，直接采用**。功能设计 F-MDL-013/014 要求完全一致。 |
| Provider 接入方式 | OAuth（ChatGPT Plus/Claude Pro/Copilot 等订阅）+ API Key + 环境变量，`auth/resolve.ts` 完整凭据生命周期（刷新、过期、撤销） | 仅 API Key → OS keyring。无 OAuth、无订阅 | **Pi 更好**。第一阶段可先只补 OpenAI-Compatible + API Key 的 Web 配置（对标 Pi 的 custom provider 路径），OAuth 列入 P2。 |
| 连接测试 | `refreshModels` 拉目录 + 最小请求，错误分类（ModelsError） | 无 Web 端点；CLI `model configure` 无测试 | **Pi 更好，Oktapod 全缺**。 |
| 模型目录缓存 | `models-store.ts` + ETag 刷新，离线可继续选择 | 无缓存，每次实时拉取 | **Pi 更好，直接采用**（F-MDL-008/009 一致）。 |
| 凭据存储 | `CredentialStore` 接口 + InMemory 实现，可插拔 | zalando/go-keyring（OS Credential Manager / Keychain） | **Oktapod 偏好更安全**（OS 级加密），但应抽象为接口（Pi 思路）便于测试与无头环境。 |
| 模型选择器 | Composer 切换 + Thinking Level 选择 + 能力展示 | 项目级 default/fallback 下拉 + Composer 文本下拉（无 thinking、无能力徽章） | **Pi 更好**。 |
| 默认模型解析 | model-resolver.ts（726 行）：Provider→模型→Thinking→Fallback 链，含 compat 自动检测 | 项目 default_model → fallback_model 两级，简单 | **Pi 更好**，但 Oktapod 的 Run 快照冻结模型配置这一点 Pi 没有（Pi 是会话级切换，Oktapod 是 Run 级不可变快照）——**Oktapod 此项更符合功能设计 F-MDL-012（切换只影响下一 Turn）**。 |

## 改造决策

1. **ModelDescriptor 扩展**（直接采用 Pi 字段语义）：加 `MaxTokens`、`InputModalities[]`、`Cost{Input,Output,CacheRead,CacheWrite}`、`ThinkingLevelMap map[string]*string`。
2. **Thinking Level 全链路**（直接采用）：`ThinkingLevel` 枚举与 Pi 一致；存入 Run 快照；Provider 不支持时按 ThinkingLevelMap 降级并在 UI 提示。
3. **Provider Web 配置**（直接采用 Pi custom-provider 路径）：`PUT /api/v1/providers/{kind}` + `POST .../test`，凭据入 keyring；先做 OpenAI-Compatible + Anthropic + Gemini 三个协议族。
4. **目录缓存**（直接采用）：`model_catalog_cache` 表存最近成功目录 + fetched_at，离线可读。
5. **保留 Oktapod 优势**：Run 级模型快照不变（Pi 的会话级切换导致同一 Session 前后模型不一致，Oktapod 的快照语义更可审计）；keyring 存储不换，但抽象 `CredentialStore` 接口。

## 关键证据

- Pi Model 类型：`packages/ai/src/types.ts:749-776`
- Pi ThinkingLevelMap：`packages/ai/src/types.ts:79-81`
- Pi 目录缓存：`packages/ai/src/models-store.ts`、`models.ts RefreshModelsContext`
- Oktapod 快照冻结：`Src/internal/application/service.go` CreateTask 的 snapshot（model_override/project_default_model）
- Oktapod 凭据：`Src/internal/adapters/secretstore/keyring.go`
