const THINK_PATTERN = /<think>([\s\S]*?)<\/think>/g

export type AssistantSegment =
  | { kind: 'think'; text: string }
  | { kind: 'content'; text: string }

/**
 * 把 assistant 原文拆成 thinking 段与正文段。
 * MiniMax 等推理模型会把思考过程包在 <think>…</think> 里随 content 一起返回，
 * 直接渲染会把内部推理噪声暴露给用户，这里将其折叠为可展开的“推理过程”。
 */
export function splitAssistantContent(raw: string): AssistantSegment[] {
  const segments: AssistantSegment[] = []
  let last = 0
  for (const match of raw.matchAll(THINK_PATTERN)) {
    const index = match.index ?? 0
    if (index > last) segments.push({ kind: 'content', text: raw.slice(last, index) })
    segments.push({ kind: 'think', text: (match[1] ?? '').trim() })
    last = index + match[0].length
  }
  if (last < raw.length) segments.push({ kind: 'content', text: raw.slice(last) })
  return segments.filter((segment) => segment.kind === 'think' || segment.text.trim() !== '')
}

/** 生成纯文本摘要：剥离 think 段与 markdown 标记，用于 Thread 列表等预览位。 */
export function assistantExcerpt(raw: string, max = 120): string {
  const plain = splitAssistantContent(raw)
    .filter((segment) => segment.kind === 'content')
    .map((segment) => segment.text)
    .join(' ')
    .replace(/[#*`>-]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
  return plain.length > max ? `${plain.slice(0, max)}…` : plain
}
