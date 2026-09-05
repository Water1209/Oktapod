export type FilePreviewKind = 'markdown' | 'html' | 'pdf' | 'office' | 'code' | 'image' | 'unsupported'

export type PreviewableFile = {
  name: string
  mediaType?: string
}

const markdownExtensions = new Set(['md', 'mdown', 'markdown'])
const htmlExtensions = new Set(['htm', 'html'])
const officeExtensions = new Set(['docx', 'xlsx', 'pptx'])
const codeExtensions = new Set([
  'bat', 'c', 'cc', 'cfg', 'conf', 'cpp', 'cs', 'css', 'csv', 'dockerfile', 'env', 'go', 'h', 'hpp',
  'ini', 'java', 'js', 'jsx', 'json', 'kt', 'kts', 'log', 'lua', 'm', 'makefile', 'php', 'pl', 'properties', 'ps1',
  'py', 'rb', 'rs', 'scss', 'sh', 'sql', 'swift', 'toml', 'ts', 'tsx', 'txt', 'vue', 'xml', 'yaml', 'yml', 'zsh',
])

function extensionOf(name: string) {
  const base = name.trim().toLowerCase().split('/').pop() || ''
  if (base === 'dockerfile' || base === 'makefile') return base
  const separator = base.lastIndexOf('.')
  return separator >= 0 ? base.slice(separator + 1) : ''
}

export function filePreviewKind(name: string, mediaType = ''): FilePreviewKind {
  const extension = extensionOf(name)
  const media = mediaType.toLowerCase().split(';')[0]?.trim() || ''
  if (markdownExtensions.has(extension) || media === 'text/markdown') return 'markdown'
  if (htmlExtensions.has(extension) || media === 'text/html') return 'html'
  if (extension === 'pdf' || media === 'application/pdf') return 'pdf'
  if (officeExtensions.has(extension) || [
    'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
    'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  ].includes(media)) return 'office'
  if (media.startsWith('image/')) return 'image'
  if (codeExtensions.has(extension) || media.startsWith('text/') || media === 'application/json' || media.endsWith('+json') || media.endsWith('+xml')) return 'code'
  return 'unsupported'
}

export function canPreviewFile(target: PreviewableFile) {
  return filePreviewKind(target.name, target.mediaType) !== 'unsupported'
}
