# Oktapod 会话并发与任务恢复设计 V1.0

## 2026-09-10 策略拒绝人工恢复

预算审批补充：“本次不限额”继续使用 `approved_exception` 状态，`max_model_calls=0`、`max_tokens=0` 表示两项均不限额，与 `UpdateRunBudget`、数据库 CHECK 和 API 枚举保持一致。此前写入未定义的 `unlimited_exception` 导致事务回滚并返回 HTTP 500；修复后无需迁移，原待处理审批可重新提交。审批成功只影响当前 Run，不修改项目默认预算或工具审批策略。

人工 `retry` 对确定性拒绝创建新一轮有限模型修正，清除新步骤继承的自动重试计数，保留历史源步骤、冻结权限、当前计划与完成项。逐一读取被拒批次的调用并传递具体约束；不将 deny 改为 allow，也不原样重执行被拒参数。前端从最近工具批次事件展示原因，提供“恢复任务”，请求失败保持可见。无关 Skill 不再因描述泛词及中英混合 n-gram 自动加载；明确名称/触发词仍支持自动加载和工具约束。详见 [3D测试复盘](Oktapod_3D测试执行复盘与恢复修复.md)。

日期：2026-09-09；状态：原受阻任务与四格式读取通过，新增 PDF 修复、真实交付与最终 pnpm verify 已通过。完成度以验收状态与追踪矩阵为准。

## 结论与目标

项目是资料、权限和工作区的组织边界，Thread（当前模型中的 Conversation）是独立执行与消息顺序边界。同项目不同会话允许并行；同会话同一时间只推进一个任务，后续消息排队或引导当前任务。实例 Worker 并发上限仍然有效。

修复文档 Skill 的 V2 计划工具遗漏，并支持从已有持久化模型记录恢复计划协议错误。保留当前受阻任务和附件用于验证，不靠删除数据绕开问题。

## 已确认事实与根因

1. 当前样本 Task/Run 均为 blocked，错误为 TRANSITION_RUN_PLAN_REQUIRED；最后模型步骤为 succeeded，持久化了执行前后 checkpoint，但没有业务工具收据或可执行的后继步骤。
2. filterToolDefinitionsBySkill 只保留旧 update_plan 和交互工具，过滤掉 transition_v2 所需 replace_plan/advance_plan。业务工具前置计划校验因此永远无法满足，自动修复耗尽。
3. retryPlanControlFailure 只接受旧协议错误，还要求已有计划版本；首次计划尚未生成的 V2 失败无法恢复。界面沿用模型进度摘要，误导为等待审批；实际并没有待批准的工具动作。
4. projectStartAllowed、两个队列领取入口以及 idx_queue_project_claim 都按项目排他，blocked Run 会使同项目其他会话永久排队。前端队列卡还从整个项目选择阻塞任务。
5. 实际恢复后发现旧附件完成检查提示仍要求 asset_stage 与脚本解析，与文档 Skill 的严格工具范围冲突；模型只处理首个 PPTX 后仍缺少其余附件证据。恢复提示改为逐一核对全部冻结附件、优先 document_open/document_read，并保留全部附件的完成门禁。

## 2026-09-10 重试上下文与导出恢复补充

真实受阻 Run 的导出文件名未包含 `.docx`，被确定性参数校验拒绝（`TOOL_POLICY_DENIED`）。该响应的 `advance_plan` 已持久化推进到最后一项，重试却直接复制原步骤的 `active_plan` 和剩余项计数，持续给模型旧的第 3 项状态，最终触发 `RUN_PLAN_ALREADY_AT_FINAL_ITEM` 并耗尽自动修正次数。本问题区别于此前 Skill 过滤 V2 计划工具：这次工具可见，错误来自重试快照与持久化计划不同步。

- 普通续接、人工恢复及模型重试共用当前计划快照生成逻辑；在同一事务内读取最新计划，保留已有历史、Skill 与拒绝原因。已成功的计划推进不回滚、不重复执行，重试只修正被拒动作。
- 最后一项的提示明确禁止再次 `advance_plan`，要求继续未完成业务工具并通过交付门禁。Skill 加载后若无计划，明确要求 `replace_plan` 与首批业务工具同一响应提交。
- `document_export.name` 的工具描述和参数 Schema 明确要求带与格式匹配的扩展名，不自动放宽非法文件名校验。Office 长文在截断前即提示按章节 `document_apply` 到同一工作副本；每轮使用最新 revision，完成后统一渲染与导出。本次不提高全局 Token 上限。
- `document_apply` 的确定性协议拒绝不属于待审批动作；模型恢复提示会保留拒绝原因并指向固定操作 Schema。DOCX 段落统一使用 `set_text/update_paragraph`，表格统一使用带有界 `rows` 的 `add_table/insert_table`，人工恢复开启新的有限修复轮次，不原样重放被拒动作。
- 时间线区分补全/修正计划、修正参数、输出上限、执行约束与模型服务重试；展开保留触发时间、错误码、原因和当前执行结果，计数标注“本轮第 N 次，最多 M 次”。最后一项受阻时明确沿用已有计划恢复，不提示重建所有已完成工作。

回归覆盖：计划推进后参数拒绝，重试读取最新最后一项并成功完成；旧步骤快照不被改写；Skill/历史/拒绝原因保留；Skill 首次计划提示与工具权限一致；不同重试原因展示及最后一项提示不冲突。

## Codex 参考及适用边界

- [官方 App Server 文档](https://learn.chatgpt.com/docs/app-server)：turn/start 指定 threadId，turn/steer 指定当前 expectedTurnId，执行与事件按 Thread/Turn 定位。此为公开协议证据，不推断未公开桌面端数据库实现。
- [官方 Worktrees 文档](https://learn.chatgpt.com/docs/environments/git-worktrees)：支持同项目独立会话并行，Git worktree 用于分离可写文件。Oktapod 本批不引入完整 worktree/Handoff 产品流程；并发控制与共享文件冲突必须分别说明。

### 本地 Codex 源码核对

- `E:/git/codex/codex-rs/core/src/thread_manager.rs`：ThreadManagerState 使用 ThreadId 到 CodexThread 的映射。
- `E:/git/codex/codex-rs/core/src/session/session.rs`：每个 Session 持有独立 active_turn Mutex 和 InputQueue。
- `E:/git/codex/codex-rs/core/src/session/input_queue.rs`：输入关联当前 turn_context.sub_id；队列归属会话，不以项目为全局队列。
- `E:/git/codex/codex-rs/core/src/turn_diff_tracker.rs`：根据实际提交的 apply_patch 记录追踪 Turn diff，不重读整个共享目录推断归属。

采用会话级调度和工具级冲突保护；不照搬 Rust 对象模型。当前共享 Git 差异明确标注非任务独占，未来若需要完整隔离修改与合并，再设计 worktree 生命周期。

## 详细设计

### 会话准入与队列

- 准入检查接收 conversation_id，活动 Task、队列 claim、未结束执行租约及未知副作用只阻止所属会话继续；跨会话共享资源的额外保护须保留在工具执行边界。
- 新会话没有历史活动任务，可直接准入；同会话重复请求仍由幂等键和短事务保护。
- 同步领取与后台领取使用一致的会话范围。同步指定 queue item 时，不得被其他会话较早消息阻挡；同会话保持 FIFO。
- 新迁移将 claimed follow_up 唯一索引由项目改为会话，保留 claim_version 防旧 Worker 回写。旧迁移不改写。
- 项目级队列接口可保留兼容命名，但响应的 blocking_task_id 必须属于请求会话；前端使用当前会话过滤阻塞状态，文案改为会话消息排队。

### 文档 Skill 与手动恢复

- Skill 限制业务工具，不删除系统计划控制工具；V1/V2 适用性仍由 ToolCatalog 协议筛选决定，禁止扩大业务工具授权。
- V2 计划错误使用明确可恢复错误集合。没有失败步骤但有已成功模型步骤时，以持久化续接历史生成新的模型步骤；首次计划未生成允许空计划绑定，重新开放 replace_plan。
- 手动恢复保留 Run 配置、附件、已加载 Skill 和已确认收据，新增审计事件，按一次新的有限修复周期执行；不重复已完成外部副作用。
- 存在 unknown/started 副作用、仍存活租约或不可修复错误时仍拒绝危险重放。
- UI 对计划协议受阻显示原因和恢复操作；审批按钮仅对应真实待处理审批。不可恢复错误不能统一显示可恢复承诺。

### 共享工作区与执行资源

- 模型采样、文档 Run 工作副本、独立 Artifact 可并行；一个会话审批期间不占用另一个会话的执行权。
- 签名文档 Runtime 当前以单进程顺序处理 RPC。首次启动健康检查通过后缓存不可变的版本、协议和渲染器身份，后续工具预检不再因另一个文档操作占用 RPC 锁而把超时误报为版本缺失；实际操作失败仍由工具收据阻塞并保留检查点。
- 实施时审核 Workspace 命令、Git baseline/变更归属、文件写入和工具调度的冲突边界。不得把其他会话修改误归属为当前任务成果；必要保护限定到具体共享资源或短暂操作，不恢复项目级整轮锁。
- 实例 Worker 并发上限仍限制资源消耗。数据库事务不跨模型/工具执行持有。

### 真实文档恢复补充

- 工具批次手动重试恢复全部未完成兄弟步骤，并同步批次续接状态；历史只恢复单个步骤的部分重试由恢复器收敛。等待审批状态不被后完成的兄弟工具覆盖。
- Office Open XML MIME 保持二进制，不因名称含 xml 而进入内联文本截断；全部冻结附件必须逐一读取。
- PDF 创建支持 title/subtitle/sections/body/bullets/footer 与 pages[].text，使用中文字体并自动折行分页；空规格明确失败。Go 创建校验拒绝非空规格生成无正文 PDF，创建缓存版本更新以避开历史空白会话。
- document_open/read 纳入变更后的验证收据，避免文档回读已完成却被要求执行无关 Shell。页面渲染仍需人工视觉复核，不能把页数当作内容正确性的证明。

## 验收矩阵

| 场景 | 预期 | 当前状态 |
|---|---|---|
| 文档 Skill + transition_v2 | 模型可见计划控制工具且业务工具范围不扩大 | 定向回归与真实计划生成通过 |
| 首次计划失败后恢复 | 同一 Task/Run 新建有效续接步骤，保留附件 | 同 Run 恢复测试通过；真实 Run 已读取和渲染 PPTX |
| 同项目不同会话 | 两个 Run 可同时推进，一方审批不阻塞另一方 | 集成、race、浏览器回归通过 |
| 同会话重复发送 | FIFO/引导语义正确，幂等重放无重复 Task | 集成回归通过 |
| 并发领取与 Worker 重启 | 会话 claim 唯一，旧版本回写失败 | 迁移和准入回归通过 |
| 未知副作用 | 不盲目重复执行，其他独立会话可推进 | 集成与 race 回归通过 |
| 完全访问/请我审批 | 实际工具收据与审批前后执行证据齐全 | 两项浏览器回归通过（模拟 Provider） |
| PPTX/PDF/DOCX/XLSX | 产品内真实读取和输出，不用本地 smoke 代替 | 原四附件 Task succeeded；DOCX/XLSX/PDF 输出成功，直接 PDF 创建、单页视觉复核、导出回读与任务 succeeded 通过 |

## 交付与验证顺序

先修工具目录和恢复不变量，再改会话准入、迁移与 UI；完成针对性回归及项目门禁后重建本地服务。通过产品接口恢复当前样本并观察真实结果，随后用两个会话验证并发及权限模式。正式状态只记录已取得证据的项目。
