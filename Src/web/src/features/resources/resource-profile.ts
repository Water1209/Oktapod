import type { Capability } from '../../shared/api/client'

export type ResourceItem = Capability & { catalog_install_state?: 'available'; description?: string }
export type ResourceAbility = { title: string; detail: string; technicalName?: string }
export type ResourceProfile = {
  displayName?: string
  summary: string
  abilityHeading: string
  abilities: ResourceAbility[]
  useCases: string[]
  usage: string[]
}

export const systemResourceProfiles: Record<string, ResourceProfile> = {
  'Skill Creator': {
    summary: '通过对话澄清目标、触发条件和输入输出，生成可校验、可安装的单 Skill 包。',
    abilityHeading: '主要能力',
    abilities: [
      { title: '需求澄清', detail: '把一句能力想法收敛为明确触发条件和交付契约。' },
      { title: '规范生成', detail: '生成 SKILL.md 与必要的 scripts、references、assets。' },
      { title: '安全安装', detail: '交付 ZIP 后继续走资源中心的完整校验与审批链。' },
    ],
    useCases: ['创建团队工作流 Skill', '把重复操作沉淀成能力', '修复不兼容的 Skill 包'],
    usage: ['在对话中说“创建一个用于……的 Skill”，补充期望输入、输出和触发条件。'],
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
    abilityHeading: '主要能力',
    abilities: [
      { title, detail: summary },
      { title: '受控工作区', detail: '只在当前项目的授权边界内运行，并保留可审计结果。' },
      { title: '确定性结果', detail: '输入、状态和错误使用统一工具协议返回，不把执行记录伪装成计划。' },
    ],
    useCases,
    usage: [`在任务中直接描述目标；Agent 会在需要“${title}”时调用该工具。`],
  }
}

const presetResourceProfiles: Record<string, ResourceProfile> = {
  'document-runtime': {
    displayName: '办公文档 Runtime',
    summary: '受信、离线、共享的办公文档运行时，为固定 document_* 工具提供解析、修订、渲染和导出能力。',
    abilityHeading: '主要能力',
    abilities: [
      { title: '解析与读取', detail: '把 PPTX、PDF、DOCX、XLSX 转为统一结构并分页读取。' },
      { title: '受控修订', detail: '用修订号保护 document_apply，冲突时拒绝覆盖。' },
      { title: '渲染与交付', detail: '先完成布局诊断，再导出不可变 Task Artifact。' },
    ],
    useCases: ['为办公文档 Skill 提供共享底座', '离线解析用户附件', '统一渲染和导出验收'],
    usage: ['这是 documents、presentations、spreadsheets 和 pdf 的共享底座，无需在对话中单独点名。'],
  },
  presentations: documentSkillProfile('PPTX', '演示文稿', ['读取现有幻灯片', '创建或受控编辑 PPTX', '渲染检查后导出交付']),
  pdf: documentSkillProfile('PDF', '文档', ['读取或 OCR 扫描件', '创建和结构化编辑 PDF', '逐页渲染校验后导出']),
  documents: documentSkillProfile('DOCX', 'Word 文档', ['读取现有 DOCX', '创建或受控编辑报告', '渲染检查后导出']),
  spreadsheets: documentSkillProfile('XLSX', '电子表格', ['读取工作簿结构与数据', '创建或受控编辑 XLSX', '渲染检查后导出']),
}

function documentSkillProfile(format: string, label: string, useCases: string[]): ResourceProfile {
  return {
    summary: `通过固定 document_* 工具读取、创建、受控编辑、渲染和导出 ${format} ${label}。`,
    abilityHeading: '文档能力',
    abilities: [
      { title: `${format} 读取`, detail: '打开冻结的附件、Artifact 或工作区文件，并分页读取结构。' },
      { title: '结构化编辑', detail: '通过修订号保护的操作修改内容，不执行文档宏或临时脚本。' },
      { title: '视觉验收', detail: '最后一次编辑后必须渲染检查，交付必须注册 Artifact。' },
    ],
    useCases,
    usage: [`在对话中上传或指定 ${format} 文件，并直接说明“读取、创建、修改或导出”的目标。`],
  }
}

export function resourceProfile(item: ResourceItem, toolsJSON = ''): ResourceProfile {
  const fixed = systemResourceProfiles[item.name] ?? (item.preset_key ? presetResourceProfiles[item.preset_key] : undefined)
  if (fixed) return fixed

  const manifest = parseResourceManifest(item.manifest_json)
  const discoveredTools = parseMCPTools(toolsJSON)
  const declaredSummary = conciseText(item.description || stringValue(manifest.description) || '', 220)
  const summary = declaredSummary || inferredResourceSummary(item, discoveredTools)
  const declaredCapabilities = uniqueStrings(stringList(manifest.capabilities))
  let abilities: ResourceAbility[] = []
  if (item.kind === 'mcp') {
    abilities = discoveredTools.slice(0, 12).map((tool) => ({
      title: tool.title || humanizeToolName(tool.name),
      technicalName: tool.name,
      detail: conciseText(tool.description || '由 MCP Server 在连接握手时声明的可调用工具。', 180),
    }))
  } else if (item.kind === 'skill') {
    abilities = skillInstructionAbilities(stringValue(manifest.instructions))
  } else if (declaredCapabilities.length) {
    abilities = declaredCapabilities.slice(0, 8).map((capability) => ({ title: humanizeToolName(capability), technicalName: capability, detail: summary }))
  }
  if (!abilities.length) abilities = fallbackAbilities(item, summary)

  const triggers = uniqueStrings([...stringList(manifest.triggers), ...stringList(manifest.keywords)])
  const useCases = triggers.length ? triggers.slice(0, 8) : inferredUseCases(item, abilities, discoveredTools, declaredSummary)
  return {
    displayName: humanizeResourceName(item.name, item.kind),
    summary,
    abilityHeading: item.kind === 'mcp' && discoveredTools.length ? '可调用工具' : '主要能力',
    abilities,
    useCases: useCases.length ? useCases : fallbackUseCases(item),
    usage: fallbackUsage(item),
  }
}

export function resourceDisplayName(item: ResourceItem) {
  return humanizeResourceName(item.name, item.kind)
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

function parseMCPTools(raw: string): Array<{ name: string; title: string; description: string }> {
  try {
    const parsed = JSON.parse(raw || '[]') as unknown
    const values = Array.isArray(parsed) ? parsed : parsed && typeof parsed === 'object' && Array.isArray((parsed as { tools?: unknown[] }).tools) ? (parsed as { tools: unknown[] }).tools : []
    return values.flatMap((value) => {
      if (!value || typeof value !== 'object') return []
      const tool = value as Record<string, unknown>
      const annotations = tool.annotations && typeof tool.annotations === 'object' ? tool.annotations as Record<string, unknown> : {}
      const name = stringValue(tool.name)
      return name ? [{ name, title: stringValue(annotations.title), description: stringValue(tool.description) }] : []
    })
  } catch {
    return []
  }
}

function inferredResourceSummary(item: ResourceItem, tools: Array<{ name: string }>) {
  const toolNames = tools.map((tool) => tool.name)
  if (item.kind === 'mcp' && toolNames.some((name) => name.startsWith('browser_'))) {
    return '通过在线发现的 browser_* 工具控制真实浏览器，完成导航、内容读取、页面交互与测试诊断。'
  }
  if (item.kind === 'mcp') return '把外部服务提供的实际工具接入当前项目，供 Agent 在任务中按需调用。'
  if (item.kind === 'skill') return '提供一套面向具体目标的专业工作方法，帮助 Agent 稳定完成相应任务。'
  if (item.kind === 'agent') return '以固定角色、模型策略和输出要求处理项目任务。'
  return '为 Oktapod 提供固定的运行时能力。'
}

function skillInstructionAbilities(instructions: string): ResourceAbility[] {
  if (!instructions) return []
  const matches = [...instructions.matchAll(/^##\s+(.+)$/gm)]
  return matches.slice(0, 6).flatMap((match, index) => {
    const start = (match.index ?? 0) + match[0].length
    const end = matches[index + 1]?.index ?? instructions.length
    const section = instructions.slice(start, end).trim()
    const detail = section.split(/\n\s*\n/).map(stripMarkdown).find(Boolean) || ''
    return detail ? [{ title: conciseText((match[1] ?? '').trim(), 80), detail: conciseText(detail, 220) }] : []
  })
}

function stripMarkdown(value: string) {
  return value.replace(/^#+\s*/gm, '').replace(/[*_`>]/g, '').replace(/\s+/g, ' ').trim()
}

function humanizeResourceName(value: string, kind: ResourceItem['kind']) {
  const trimmed = value.trim()
  if (kind === 'mcp' && /^@[^/]+\/[^/]+$/.test(trimmed)) {
    const [scope = '', pkg = ''] = trimmed.slice(1).split('/', 2)
    if (pkg.toLowerCase() === 'mcp') return `${humanizeWords(scope)} MCP`
    return `${humanizeWords(scope)} · ${humanizeWords(pkg)}`
  }
  return humanizeWords(trimmed)
}

function humanizeWords(value: string) {
  return value.split(/[-_]+/).filter(Boolean).map((part) => part.toLowerCase() === 'mcp' ? 'MCP' : part[0]?.toUpperCase() + part.slice(1)).join(' ')
}

function fallbackAbilities(item: ResourceItem, summary: string): ResourceAbility[] {
  if (item.kind === 'mcp') return [
    { title: '外部工具集', detail: '连接后会在这里列出服务端实际声明的工具和用途。' },
    { title: '任务调用', detail: '在对话中点名该 MCP 或直接描述它能完成的目标。' },
  ]
  if (item.kind === 'skill') return [
    { title: '专业工作流', detail: summary },
    { title: '稳定交付', detail: '按 Skill 自身说明执行关键步骤、检查点和交付要求。' },
  ]
  if (item.kind === 'agent') return [
    { title: '角色专长', detail: summary },
    { title: '一致输出', detail: '按该 Agent 的职责、模型策略和输出约定处理任务。' },
  ]
  return [
    { title: '运行时能力', detail: summary },
    { title: '可验证状态', detail: '依赖不可用时明确显示原因，不把降级状态伪装成可用。' },
  ]
}

function fallbackUseCases(item: ResourceItem) {
  if (item.kind === 'mcp') return ['调用该服务提供的实际工具', '把外部数据或操作接入任务', '在审批后执行开放世界操作']
  if (item.kind === 'skill') return ['完成该 Skill 描述的专业任务', '复用稳定的领域工作方法', '按既定检查点交付结果']
  if (item.kind === 'agent') return ['复用固定角色处理任务', '保持项目内一致的执行风格', '在 Agent Team 中承担明确职责']
  return ['支撑内建 Agent 工作流', '提供可审计的项目操作', '作为上层 Skill 的运行依赖']
}

function inferredUseCases(
  item: ResourceItem,
  abilities: ResourceAbility[],
  tools: Array<{ name: string }>,
  declaredSummary: string,
) {
  if (item.kind === 'mcp') {
    const names = tools.map((tool) => tool.name.toLowerCase())
    const useCases: string[] = []
    if (names.some((name) => /(?:^|_)(?:navigate|snapshot|find|content|read)(?:_|$)/.test(name))) {
      useCases.push('访问目标页面并读取结构、文本、链接或当前状态')
    }
    if (names.some((name) => /(?:^|_)(?:click|type|fill|press|select|drag|upload)(?:_|$)/.test(name))) {
      useCases.push('自动执行点击、输入、表单、键盘或文件交互')
    }
    if (names.some((name) => /(?:^|_)(?:screenshot|console|network|trace|test)(?:_|$)/.test(name))) {
      useCases.push('收集截图、控制台或网络证据并诊断页面问题')
    }
    if (names.some((name) => /(?:^|_)(?:search|extract|crawl|map)(?:_|$)/.test(name))) {
      useCases.push('检索、提取或遍历外部信息并返回可引用结果')
    }
    return uniqueStrings(useCases).slice(0, 4).length ? uniqueStrings(useCases).slice(0, 4) : fallbackUseCases(item)
  }
  if (item.kind === 'skill') {
    const useCases = [
      declaredSummary ? `使用该 Skill 完成：${conciseText(declaredSummary, 180)}` : '',
      ...abilities.slice(0, 2).map((ability) => `按“${ability.title}”方法处理并检查结果`),
    ]
    return uniqueStrings(useCases).slice(0, 3).length ? uniqueStrings(useCases).slice(0, 3) : fallbackUseCases(item)
  }
  return fallbackUseCases(item)
}

function fallbackUsage(item: ResourceItem) {
  if (item.kind === 'mcp') return [`在对话中说“使用 ${item.name} 完成……”，或直接描述与其工具能力匹配的任务。`]
  if (item.kind === 'skill') return [`在对话中说“使用 ${item.name} 完成……”，也可以直接描述与该 Skill 相符的目标。`]
  if (item.kind === 'agent') return [`创建任务时选择 ${item.name}，并明确目标、输入与验收要求。`]
  return ['直接描述任务目标；Agent 会在需要时调用该运行时能力。']
}

function humanizeToolName(value: string) {
  const aliases: Record<string, string> = {
    browser_navigate: '打开网页', browser_snapshot: '读取页面结构', browser_find: '查找页面内容', browser_click: '点击元素',
    browser_type: '输入文本', browser_fill_form: '填写表单', browser_evaluate: '读取或执行页面脚本', browser_take_screenshot: '页面截图',
    browser_console_messages: '控制台消息', browser_network_requests: '网络请求', browser_tabs: '标签页管理', browser_run_code_unsafe: '执行浏览器脚本（高风险）',
  }
  return aliases[value] || value.replace(/^browser_/, '').split(/[_-]+/).filter(Boolean).map((part) => part[0]?.toUpperCase() + part.slice(1)).join(' ')
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
