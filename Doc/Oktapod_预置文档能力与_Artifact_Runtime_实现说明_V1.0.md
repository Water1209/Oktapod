# Oktapod 预置文档能力与 Artifact Runtime 实现说明

> 版本：V1.1
> 更新日期：2026-09-05
> 状态：核心实现完成，跨平台签名包与性能发布门禁待 CI/标准夹具证明

## 1. 结论与用户影响

Oktapod 已采用“可卸载预置 Skill + 共享签名 Document Runtime”两层架构。`presentations`、`pdf`、`documents`、`spreadsheets` 与自定义 Skill 使用同一资源生命周期；实际文件处理只能经过六个静态 `document_*` 工具和既有 `ToolRuntime → ExecutionPolicy → Receipt → Run Snapshot → ArtifactCapture`，Skill ZIP 不能携带或替换 Runtime 可执行代码。

用户删除预置资源后，重启、登录、资源刷新和版本升级都不会静默恢复；资源目录提供显式恢复。Runtime 缺失或不健康时，相关 Skill 显示 `degraded`，附件任务关闭式失败并给出修复入口，不回退到临时解析脚本。输入文件保持不可变，编辑产生 Run 绑定修订，默认导出新的不可变 Artifact。

## 2. 已实现架构

固定数据流：

`冻结 Asset / Artifact / Workspace → document_open → 解析缓存 → Run 修订 → document_render → document_export → ArtifactCapture`

- Capability 增加 `origin`、`preset_key`、`dependencies`、`managed_version`、`update_state`；`preset_resource_states` 保存安装/删除记忆。
- `GET /resource-catalog` 与 `POST /resource-catalog/{preset_key}:install` 提供预置目录和显式恢复；现有 Capability 状态与删除接口继续统一管理预置和自定义资源。
- `document-runtime` 提供 `document.runtime`；四个预置 Skill 只包含触发词、格式、固定工作流、严格 fallback 和六个工具白名单。
- `document_open/read/render` 固定为 L0；`document_create/apply/export` 固定为 L1。所有写操作按输入 Hash 和修订号幂等，协议递归拒绝 `code`、`command`、`script`、`macro`、`network` 等可执行载荷。
- `document_sessions` 与 `document_revisions` 保存 Run、Runtime 版本、修订 Hash、操作 Hash、工作句柄和最终渲染证据；CAS 防止重复或乱序编辑。
- 解析缓存键包含 Organization、源内容 Hash、Runtime 版本和提取配置；渲染缓存键包含修订 Hash、渲染器和选项。缓存仅位于 DataRoot 派生目录，执行 1 GiB / 30 天 LRU，每次命中仍重新授权源对象。
- Task 流展示“结构读取、分页读取、修订、渲染校验、输出”，渲染预览通过 Principal + Run + Document 授权的只读路由提供，版式警告和缓存命中可见。
- 文件面板复用同一 Runtime，为 Workspace、Project Asset 和 Artifact 创建 20 分钟短期预览能力；每次创建重新核对项目 Scope、源 Hash 和当前工作文件未变化，页面路由继续核对 Organization + User。DOCX/XLSX/PPTX 输出分页 PNG，浏览器不接触 Runtime 句柄或宿主绝对路径。
- `document_export` 只接受当前修订且最后一次渲染成功的文档；OOXML 再校验 ZIP、Content Types、必需部件、路径、加密、宏和压缩炸弹，PDF 再校验签名、页数和可渲染性，最终复用 ArtifactCapture。同一任务修改多个文档时，每个文档都必须分别匹配当前修订、渲染 Hash 和 Artifact，不能由单一导出替代。

## 3. Runtime 与发布边界

Runtime 是 stdio newline JSON-RPC 子进程，不监听端口；Go 只传 DataRoot 内受控相对句柄。子进程使用最小环境、隔离 HOME/TEMP、清空代理并禁用 Python socket API。Office 渲染使用隔离用户目录的 LibreOffice Headless，PDF 页面使用 PDFium；扫描 PDF 只在 Tesseract 与 `chi_sim + eng` 实际可用时 OCR，结果标注来源和置信度。Windows 使用同步等待的 `soffice.com`（Manifest 仍可声明 `soffice.exe`，宿主只在同目录 `.com` 也受签名覆盖时切换），每次转换创建独立临时 Profile；文件预览缓存目录使用短 Hash，避免 LibreOffice 深层 Profile 越过传统 `MAX_PATH`。

`pnpm release:build` 会调用 `Test/build-document-runtime.mjs`。发布环境必须通过受保护文件提供 Ed25519 PKCS8 私钥，并暂存带 Python、LibreOffice、Tesseract `chi_sim + eng`、锁定依赖和完整 `LICENSES/` 的自包含平台 Runtime；脚本生成 SPDX 2.3 SBOM、第三方声明、逐文件 SHA256、签名 Manifest、平台归档和归档校验和。归档前必须用包内依赖通过健康探测和四格式 25 次 RPC；`release:smoke` 还会验证服务确实信任并加载该包。生产加载器校验信任 Key、算法、平台、协议、必需文件 Hash、所有路径组件和符号链接；开发源码 Runtime 只有显式 `OKTAPOD_DOCUMENT_RUNTIME_DEV=1` 才可发现，宿主 LibreOffice/Tesseract 也只能通过开发专用环境变量传入绝对、普通、非符号链接文件。

## 4. 当前格式能力

| 格式 | 已验证主链路 | 明确边界 |
|---|---|---|
| PPTX | 文字/备注/表格/图表元数据读取；创建；文字替换、文本设置、增删幻灯片；LibreOffice/PDFium 渲染；PPTX/PDF 导出 | 动画、宏、OLE 只读保护；复杂图表/母版编辑尚未声明完成 |
| DOCX | 段落/run、表格、章节、页眉页脚读取；创建；替换、段落、页眉页脚编辑；DOCX/PDF 导出 | 修订记录、内容控件、宏、嵌入对象不做可写承诺 |
| XLSX | 工作表、范围、公式、表格/图表元数据读取；创建；单元格/公式、工作表、文本替换；XLSX/PDF 导出 | 不执行宏、不刷新外部连接；复杂图表编辑尚未声明完成 |
| PDF | 原生文本、布局、表格、表单、低密度 OCR；创建；旋转、排序、裁剪、表单填写；页面渲染和 PDF 导出 | 不承诺 Word 式正文回流；合并/拆分/批注/遮盖完整矩阵尚未声明完成 |

旧二进制 Office、加密文档和宏格式返回结构化不支持错误，不修改原件。

## 5. 验证证据与未关闭门禁

当前本机证据：

- Go 定向单元/集成覆盖固定工具风险、可执行载荷拒绝、签名篡改、预置安装/停用/删除记忆/恢复、依赖阻止删除、团队 Scope 和原有完整集成回归。
- `pnpm test:document-runtime` 在本机真实 Python + LibreOffice 上完成 25 次 JSON-RPC，覆盖四格式创建、打开、编辑、再次打开、渲染和导出。
- Web lint 与生产构建通过，资源目录与任务文档卡已完成类型检查。
- 本机真实浏览器验证五个预置资源均为“可用”，Runtime 详情展示六个固定工具，级联删除提示准确列出四个依赖且取消不产生变更；代码场景的任务输出、Artifact 下载/预览和 Changes 均正常，浏览器控制台 `error/warn=0`。
- 浏览器验证同时发现并关闭两个集成缺口：开发 Runtime 可显式接入受限宿主渲染器；Changes 后端排除 `.oktapod`，前端将 82 个历史 pip/解析残留折叠为“运行缓存与临时文件”，不进入成果全选、撤销或提交。

仍不得宣称“完整办公套件发布可用”的门禁：

1. Windows/Linux 各自以发布用自包含依赖和真实发布 Key 构建、签名、离线安装、卸载、升级回滚并完成 release smoke。
2. 标准 20 MiB / 100 页或幻灯片夹具证明首次结构读取 P95 ≤ 5 秒、缓存命中 P95 ≤ 300 ms、分页 P95 ≤ 200 ms，且相对旧通用脚本路径至少 5 倍。
3. 补齐 OOXML ZIP 炸弹、XML 实体、宏/外部链接、损坏 PDF、Runtime 崩溃/超时、跨用户缓存、导出中断和 ArtifactCapture 失败的完整恶意夹具矩阵。
4. 完成 PPTX/DOCX/XLSX/PDF 计划中尚未实现的复杂结构编辑矩阵，再将验收状态从 `PARTIAL` 改为 `PASS`。
