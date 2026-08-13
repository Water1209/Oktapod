# Oktapod V1.0.0 发布说明

> 状态：Release Candidate
> 候选日期：2026-08-13
> 发布范围：个人私有项目 / SQLite / 单 Agent 主链路

## 1. 发布判断

V1.0.0 已具备形成 Release Candidate 的产品闭环。代码行数和目录纯度不作为发布门禁；门禁只判断用户主链路、安全不变量、恢复语义和可复核工件。

正式 tag 与 push 前仍必须由 Ubuntu CI 通过 race，并由 Ubuntu 与 Windows CI 通过原生产品烟测。当前仓库不会自动 tag 或 push。

## 2. 用户可用能力

- 注册、登录和 Owner-only 个人私有项目；
- 在单一 Thread 内完成目标理解、持久计划、文件读改、命令验证和流式结果；
- 用户发送内容先持久化为可见消息；附件、显式 Skill、Steering 与下一轮队列状态不会因模型错误或快速连发而丢失；
- Composer 邻接审批、输入、阻塞恢复、Steering、Next Turn 与 Abort；同计划项动作可批量审批但保留逐项冻结参数、Hash 和审计；
- 预算不足时可按明确增量扩展、使用现有结果收尾、将本 Run 设为不限额或停止并保留证据，不再按单次调用循环申请；
- Work/Code 共用 Workspace、命令、Git、Skill、MCP 和 Artifact 执行内核，场景不改变审批风险；
- “工作区 / 项目文件 / 任务输出”分层展示可变文件、可复用输入和不可变交付；附件可直接关联已有项目文件；PDF 交付必须通过签名、MIME、Hash 与 Artifact 完成门禁；
- 首页工作台、项目库、项目切换器、账号菜单、中文设置与可最大化任务面板形成一致导航；
- Provider API 目录与用户启用模型分离；未配置独立 Reviewer 时自动审查明确禁用并关闭式转人工；
- Changes 的 Run 开始 / Git HEAD / Git Index 三基线，按文件和 Hunk 查看；
- 接受成果、撤销所选文件、选择文件提交；撤销和提交会创建受治理任务，不由浏览器直接改写 Git；
- Policy、ExecutionAuthorization、状态哈希、Receipt、Checkpoint 与重启恢复；
- 内建 Workspace/Git/搜索工具，旧工具名与简洁别名兼容；
- 全局/项目级 Skill、Package、MCP 的校验、审核、启用、调用和 Run 快照；
- OpenAI-compatible 真实 Provider 主链路；Anthropic/Gemini 保留协议合同。

## 3. 安全说明

默认是 `governed_host`：隔离 HOME 与凭据环境、限制工作区路径、禁用宿主 Git 凭据与 hooks、设置超时并终止进程树。它不是容器或 OS 级强沙箱；需要处理不可信仓库时，应在额外隔离的主机或容器中运行。

首次启动没有共享默认账号、密码或 Provider Key。凭据只能通过 SecretStore 或环境变量注入。

## 4. 已知限制与延期

以下项目不阻塞 V1.0.0 个人版发布：

- Team 项目主流程、PostgreSQL 发布门禁和多节点运行；
- 容器/OS 强沙箱、网络默认拒绝、CPU/内存硬隔离；
- 交互式 xterm；命令仅以受治理任务运行；
- Pi 各历史格式精确导入；
- 原生 Anthropic/Gemini 真实凭据门禁；
- Skill `same-capability` 的确定性候选排名与跨 Provider 参数转换；
- `run_command` 的逐块 stdout/stderr 直播；完成事件和完整长输出 Artifact 已可恢复；
- Linux 真实浏览器矩阵与完整企业级故障注入，进入后续加固。

## 5. 构建与校验

```powershell
pnpm install --frozen-lockfile
$env:GOCACHE="$env:TEMP\go-build-cache"
pnpm verify
pnpm test:e2e
pnpm release:build
pnpm release:smoke
.\Build\oktapod-v1.0.0-windows-amd64.exe version
```

发布构建生成：

```text
Build/oktapod-v1.0.0-<os>-<arch>[.exe]
Build/oktapod-v1.0.0-<os>-<arch>[.exe].sha256
```

本次 Windows RC 工件：

```text
Build/oktapod-v1.0.0-windows-amd64.exe
SHA256 ccb7a03e3eb5520da0e97f6226f0d2a68fc506e0901fc23ef206f6ff6df1315f
```

2026-08-13 最终本地门禁：`pnpm verify` 通过（Web 26/26），Playwright foundation 1/1、turn 1/1、success 24/24 通过；发布构建与独立临时数据目录产品烟测完成健康、注册、Session/CSRF、版本诊断和个人项目创建。

最终门禁证据与未运行项以《Oktapod V1 验收状态与追踪矩阵》为唯一真源。

## 6. 升级与回滚

升级前运行 `backup` 并保留旧二进制。升级后先检查 `<binary> version`、健康接口、登录与个人项目读取，再接入 Provider。若需要回滚，停止新版本，恢复升级前备份，并启动旧二进制；不要让旧程序直接复用已由新版本迁移的数据库。
