const browserToolLabels: Record<string, string> = {
  browser_navigate: '打开网页',
  browser_snapshot: '读取页面结构',
  browser_find: '查找页面内容',
  browser_click: '点击页面元素',
  browser_type: '输入内容',
  browser_fill_form: '填写表单',
  browser_evaluate: '读取或执行页面脚本',
  browser_take_screenshot: '截取页面',
  browser_tabs: '管理浏览器标签页',
}

export function mcpToolDisplayLabel(toolName: string, declaredDisplayName = '') {
  const { provider, action } = mcpToolParts(toolName, declaredDisplayName)
  return `${provider} · ${action}`
}

export function mcpToolStepLabel(toolName: string, declaredDisplayName = '') {
  const { provider, action } = mcpToolParts(toolName, declaredDisplayName)
  return `${provider} ${action}`
}

function mcpToolParts(toolName: string, declaredDisplayName: string) {
  const technical = toolName.split('__').at(-1) || toolName
  const declaredProvider = declaredDisplayName.split('·')[0]?.trim()
  const hasSnapshotProvider = declaredProvider && declaredProvider !== toolName && !declaredProvider.startsWith('mcp__')
  return {
    provider: hasSnapshotProvider ? humanizeCapabilityName(declaredProvider) : technical.startsWith('browser_') ? '浏览器 MCP' : 'MCP',
    action: browserToolLabels[technical] || technical,
  }
}

function humanizeCapabilityName(value: string) {
  const trimmed = value.trim()
  if (/\bmcp\b/i.test(trimmed) && !trimmed.startsWith('@')) return trimmed
  let name = trimmed
  if (name.startsWith('@')) {
    const [scope, packageName] = name.slice(1).split('/', 2)
    name = packageName && packageName.toLowerCase() !== 'mcp' ? packageName.replace(/^server[-_]/i, '') : (scope || '')
  }
  name = name.replace(/[-_]+/g, ' ').replace(/\b\w/g, (letter) => letter.toUpperCase())
  return `${name || 'MCP'} MCP`
}
