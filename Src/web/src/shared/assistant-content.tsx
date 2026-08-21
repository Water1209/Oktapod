import { useState, type ReactNode } from 'react'
import { ChevronRight } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import { splitAssistantContent } from './assistant-segments'
import { normalizeAssistantSourceLinks } from './assistant-source-links'

/** 折叠的推理过程块，默认收起，避免干扰阅读正文。 */
function ThinkingBlock({ text, streaming }: { text: string; streaming?: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <div className={`thinking-block${open ? ' open' : ''}${streaming ? ' streaming' : ''}`}>
      <button
        type="button"
        className="thinking-toggle"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <ChevronRight size={13} aria-hidden="true" />
        <span>推理过程</span>
        {!open ? <small>{streaming ? '思考中…' : '已折叠'}</small> : null}
      </button>
      {open ? <div className="thinking-content">{text}</div> : null}
    </div>
  )
}

/**
 * Assistant 消息正文：解析 <think> 折叠推理，正文走 Markdown 渲染。
 * user / 普通文本仍由调用方按原样 pre-wrap 输出，不经过这里。
 */
export function AssistantContent({ content, streaming }: { content: string; streaming?: boolean }) {
  const segments = splitAssistantContent(content)
  if (segments.length === 0) return null
  const body: ReactNode[] = segments.map((segment, index) =>
    segment.kind === 'think' ? (
      <ThinkingBlock key={`think-${index}`} text={segment.text} streaming={streaming && index === segments.length - 1} />
    ) : (
      <div key={`content-${index}`} className="assistant-markdown">
        <ReactMarkdown remarkPlugins={[remarkGfm]}>{normalizeAssistantSourceLinks(segment.text)}</ReactMarkdown>
      </div>
    ),
  )
  return <div className="assistant-content">{body}</div>
}
