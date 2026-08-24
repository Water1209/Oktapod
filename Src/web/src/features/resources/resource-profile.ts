import type { Capability } from '../../shared/api/client'

export type ResourceItem = Capability & { catalog_install_state?: 'available'; description?: string }
type ResourceAbility = { title: string; detail: string }
type ResourceProfile = { summary: string; abilities: ResourceAbility[]; useCases: string[] }

export const systemResourceProfiles: Record<string, ResourceProfile> = {
  'Skill Creator': {
    summary: '通过对话澄清目标、触发条件和输入输出，生成可校验、可安装的单 Skill 包。',
    abilities: [
      { title: '需求澄清', detail: '把一句能力想法收敛为明确触发条件和交付契约。' },
      { title: '规范生成', detail: '生成 SKILL.md 与必要的 scripts、references、assets。' },
      { title: '安全安装', detail: '交付 ZIP 后继续走资源中心的完整校验与审批链。' },
    ],
    useCases: ['创建团队工作流 Skill', '把重复操作沉淀成能力', '修复不兼容的 Skill 包'],
  },
  read_file: toolProfile('读取项目文件', '按行分页读取文本、源码和配置，不修改工作区。', ['检查源码实现', '读取配置与日志', '为修改建立前置证据']),
  list_files: toolProfile('浏览项目结构', '列出目录内容并支持受控递归与分页。', ['了解代码布局', '定位文档与资产', '核对生成文件']),
  find_files: toolProfile('按名称定位文件', '使用有边界的 glob 模式搜索项目相对路径。', ['查找同类文件', '定位入口与测试', '筛选特定扩展名']),
  grep: toolProfile('搜索文件内容', '在 UTF-8 工作区文件中分页搜索文字，并对敏感值脱敏。', ['追踪符号引用', '定位配置项', '核对用户可见文案']),
  edit_file: toolProfile('精确修改已有文件', '基于原文和 preimage hash 原子替换，冲突时不会覆盖外部改动。', ['小范围代码修复', '同步配置文案', '安全更新已有文件']),
  write_file: toolProfile('创建新文件', '在项目工作区创建新文件；已有文件必须走精确编辑。', ['新增源码或测试', '生成项目文档', '创建配置模板']),
  run_command: toolProfile('运行受控命令', '在项目工作区执行单个程序；高风险操作继续受策略和审批控制。', ['运行测试与构建', '执行格式化工具', '调用项目脚本']),
  git_status: toolProfile('检查 Git 状态', '只读返回工作区状态与可用于后续写操作的前置哈希。', ['识别未提交改动', '确认变更边界', '准备安全提交']),
  git_diff: toolProfile('查看代码差异', '按工作区、暂存区或固定提交基线读取有边界的 diff。', ['代码审查', '核对本轮改动', '生成变更摘要']),
  git_log: toolProfile('读取提交历史', '分页读取最近提交，用于理解演进和定位变更来源。', ['追踪设计演进', '定位回归来源', '准备发布说明']),
}

function toolProfile(title: string, summary: string, useCases: string[]): ResourceProfile {
  return {
    summary,
    abilities: [
      { title, detail: summary },
      { title: '受控工作区', detail: '只在当前项目的授权边界内运行，并保留可审计结果。' },
      { title: '确定性结果', detail: '输入、状态和错误使用统一工具协议返回，不把执行记录伪装成计划。' },
    ],
    useCases,
  }
}

const presetResourceProfiles: Record<string, ResourceProfile> = {
  'document-runtime': {
    summary: '受信、离线、共享的办公文档运行时，为固定 document_* 工具提供解析、修订、渲染和导出能力。',
    abilities: [
      { title: '解析与读取', detail: '把 PPTX、PDF、DOCX、XLSX 转为统一结构并分页读取。' },
      { title: '受控修订', detail: '用修订号保护 document_apply，冲突时拒绝覆盖。' },
      { title: '渲染与交付', detail: '先完成布局诊断，再导出不可变 Task Artifact。' },
    ],
    useCases: ['为办公文档 Skill 提供共享底座', '离线解析用户附件', '统一渲染和导出验收'],
  },
  presentations: documentSkillProfile('PPTX', '演示文稿', ['读取现有幻灯片', '创建或受控编辑 PPTX', '渲染检查后导出交付']),
  pdf: documentSkillProfile('PDF', '文档', ['读取或 OCR 扫描件', '创建和结构化编辑 PDF', '逐页渲染校验后导出']),
  documents: documentSkillProfile('DOCX', 'Word 文档', ['读取现有 DOCX', '创建或受控编辑报告', '渲染检查后导出']),
  spreadsheets: documentSkillProfile('XLSX', '电子表格', ['读取工作簿结构与数据', '创建或受控编辑 XLSX', '渲染检查后导出']),
}

function documentSkillProfile(format: string, label: string, useCases: string[]): ResourceProfile {
  return {
    summary: `通过固定 document_* 工具读取、创建、受控编辑、渲染和导出 ${format} ${label}。`,
    abilities: [
      { title: `${format} 读取`, detail: '打开冻结的附件、Artifact 或工作区文件，并分页读取结构。' },
      { title: '结构化编辑', detail: '通过修订号保护的操作修改内容，不执行文档宏或临时脚本。' },
      { title: '视觉验收', detail: '最后一次编辑后必须渲染检查，交付必须注册 Artifact。' },
    ],
    useCases,
  }
}

export function resourceProfile(item: ResourceItem, toolsJSON = ''): ResourceProfile {
  const fixed = systemResourceProfiles[item.name] ?? (item.preset_key ? presetResourceProfiles[item.preset_key] : undefined)
  if (fixed) return fixed
  const manifest = parseResourceManifest(item.manifest_json)
  const summary = conciseText(item.description || stringValue(manifest.description) || fallbackResourceSummary(item), 220)
  const discoveredTools = parseMCPTools(toolsJSON)
  const allowedTools = uniqueStrings([
    ...stringList(manifest.allowed_tools),
    ...stringList(manifest.tools),
    ...stringList(manifest.capabilities),
  ])
  let abilities: ResourceAbility[] = discoveredTools.slice(0, 12).map((tool) => ({
    title: tool.name,
    detail: conciseText(tool.description || '由 MCP Server 在连接握手时声明的可调用工具。', 180),
  }))
  if (!abilities.length && allowedTools.length) {
    abilities = allowedTools.slice(0, 10).map((tool) => ({ title: tool, detail: abilityDetail(tool) }))
  }
  if (!abilities.length) abilities = fallbackAbilities(item)
  const triggers = uniqueStrings([...stringList(manifest.triggers), ...stringList(manifest.keywords)])
  const useCases = triggers.length ? triggers.slice(0, 8).map((trigger) => `对话提到“${trigger}”时按需调用`) : fallbackUseCases(item)
  return { summary, abilities, useCases }
}

export function resourceKindText(kind: Capability['kind']) {
  if (kind === 'mcp') return 'MCP 工具服务'
  if (kind === 'skill') return 'Skill 工作流'
  if (kind === 'agent') return '项目 Agent'
  return '运行时插件'
}

function parseResourceManifest(raw: string): Record<string, unknown> {
  try {
    const value = JSON.parse(raw || '{}')
    return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function parseMCPTools(raw: string): Array<{ name: string; description: string }> {
  try {
    const parsed = JSON.parse(raw || '[]') as unknown
    const values = Array.isArray(parsed) ? parsed : parsed && typeof parsed === 'object' && Array.isArray((parsed as { tools?: unknown[] }).tools) ? (parsed as { tools: unknown[] }).tools : []
    return values.flatMap((value) => {
      if (!value || typeof value !== 'object') return []
      const tool = value as Record<string, unknown>
      const name = stringValue(tool.name)
      return name ? [{ name, description: stringValue(tool.description) }] : []
    })
  } catch {
    return []
  }
}

function fallbackResourceSummary(item: ResourceItem) {
  if (item.kind === 'mcp') return '连接外部 MCP Server，在统一授权、凭据和 Receipt 边界内向对话提供工具。'
  if (item.kind === 'skill') return '向模型提供可按触发条件加载的专业工作流，并限制它可调用的工具范围。'
  if (item.kind === 'agent') return '保存一个项目内 Agent 的指令、模型策略与执行配置，供任务按需调用。'
  return '为 Oktapod 提供固定的运行时能力，并继续经过统一策略与审计链。'
}

function fallbackAbilities(item: ResourceItem): ResourceAbility[] {
  if (item.kind === 'mcp') return [
    { title: '协议连接', detail: '使用 stdio 或 Streamable HTTP 与 MCP Server 建立受控连接。' },
    { title: '工具发现', detail: '连接成功后读取服务端工具清单和输入 Schema。' },
    { title: '隔离凭据', detail: '凭据只保存在数据库 SecretStore，不进入 manifest 或对话。' },
  ]
  if (item.kind === 'skill') return [
    { title: '触发识别', detail: '根据名称、描述和触发词判断何时加载。' },
    { title: '工作流指令', detail: '把专业步骤注入当前 Agent Turn，不创建第二套状态事实。' },
    { title: '工具收敛', detail: '只暴露 Skill 声明且项目允许的工具交集。' },
  ]
  if (item.kind === 'agent') return [
    { title: '角色指令', detail: '固定 Agent 的职责、边界和输出方式。' },
    { title: '模型策略', detail: '继承项目默认模型或使用 Agent 明确指定的模型。' },
    { title: '项目隔离', detail: '只在所属项目和用户可见范围内被调用。' },
  ]
  return [
    { title: '固定协议', detail: '以版本化协议向 Runtime 暴露能力。' },
    { title: '统一治理', detail: '调用仍经过策略、Authorization、Receipt 和结果归一化。' },
    { title: '故障显式化', detail: '依赖不可用时保持 degraded，不静默降级。' },
  ]
}

function fallbackUseCases(item: ResourceItem) {
  if (item.kind === 'mcp') return ['调用外部服务提供的专业工具', '把远端或本地 Server 接入对话', '在审批后执行开放世界操作']
  if (item.kind === 'skill') return ['执行重复的专业工作流', '按触发词加载领域知识', '限制任务只使用必要工具']
  if (item.kind === 'agent') return ['复用固定角色处理任务', '为项目保留一致的执行风格', '在 Agent Team 中承担明确职责']
  return ['支撑内建 Agent 工作流', '提供可审计的项目操作', '作为上层 Skill 的运行依赖']
}

function abilityDetail(name: string) {
  if (name.startsWith('document_')) return '固定文档协议工具；继续经过修订号、渲染与 Artifact 交付约束。'
  if (name.includes('web_')) return '受网络策略约束的 Web 能力；外部内容始终视为不可信数据。'
  if (name.includes('workspace') || ['read_file', 'list_files', 'find_files', 'grep', 'edit_file', 'write_file', 'run_command'].includes(name)) return '在项目工作区与当前授权范围内调用。'
  return '由资源 manifest 声明，并由 Runtime 在调用前重新校验。'
}

function stringList(value: unknown) {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string' && Boolean(item.trim())).map((item) => item.trim()) : []
}

function stringValue(value: unknown) {
  return typeof value === 'string' ? value.trim() : ''
}

function uniqueStrings(values: string[]) {
  return [...new Set(values.filter(Boolean))]
}

function conciseText(value: string, max: number) {
  const text = value.replace(/\s+/g, ' ').trim()
  return text.length > max ? `${text.slice(0, max - 1).trimEnd()}…` : text
}
