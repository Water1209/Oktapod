const sourceUrlPattern = /(?:(?:https?:\/\/)|(?:www\.)?)[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?\.[a-z]{2,}(?:\/[\w./?%#=&:+~@-]*)?/gi

function hrefForSource(value: string) {
  const trimmed = value.trim()
  return /^https?:\/\//i.test(trimmed) ? trimmed : `https://${trimmed}`
}

function linkSourcePart(part: string) {
  const leading = part.match(/^\s*/)?.[0] ?? ''
  const trailing = part.match(/\s*$/)?.[0] ?? ''
  const value = part.trim()
  if (!value || /\[[^\]]+\]\(https?:\/\//i.test(value)) return part

  const wrapped = value.match(/^(.+?)[（(]([^()]+)[）)]$/)
  if (wrapped) {
    const title = (wrapped[1] ?? '').trim()
    const urls = (wrapped[2] ?? '').match(sourceUrlPattern) ?? []
    const firstUrl = urls[0]
    if (firstUrl) {
      const linkedTitle = title ? `[${title}](${hrefForSource(firstUrl)})` : `[${firstUrl}](${hrefForSource(firstUrl)})`
      const additional = urls.slice(1).filter((url): url is string => Boolean(url)).map((url) => `[${url}](${hrefForSource(url)})`).join('、')
      return `${leading}${linkedTitle}${additional ? `（${additional}）` : ''}${trailing}`
    }
  }

  return part.replace(sourceUrlPattern, (url) => `[${url}](${hrefForSource(url)})`)
}

/**
 * Makes the compact "主要来源" fallback emitted by some providers usable.
 * Provider-authored Markdown links remain untouched; only source paragraphs
 * are normalized, so ordinary prose and code blocks are not rewritten.
 */
export function normalizeAssistantSourceLinks(markdown: string) {
  return markdown.split('\n').map((line) => {
    const match = line.match(/^(\s*(?:[-*]\s+)?(?:\*\*)?主要来源(?:\*\*)?\s*[:：])(\s*)(.*)$/)
    if (!match || /\[[^\]]+\]\(https?:\/\//i.test(line)) return line
    const parts = (match[3] ?? '').split(/([；;])/)
    return `${match[1] ?? ''}${match[2] ?? ''}${parts.map((part) => /^[；;]$/.test(part) ? part : linkSourcePart(part)).join('')}`
  }).join('\n')
}
