import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, Download, ExternalLink, Eye, FileCode2, FileSpreadsheet, FileText, LoaderCircle, Presentation, TriangleAlert } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { api } from '../../shared/api/client'
import { filePreviewKind, type FilePreviewKind } from './file-preview-kind'
import { buildSandboxedHTML } from './html-preview'

export type FilePreviewTarget = {
  projectId: string
  sourceKind: 'workspace' | 'asset' | 'artifact'
  reference: string
  name: string
  mediaType?: string
  sizeBytes: number
  previewUrl: string
  downloadUrl?: string
}


export function FilePreview({ onBack, target }: { onBack: () => void; target: FilePreviewTarget }) {
  const kind = filePreviewKind(target.name, target.mediaType)
  const [textMode, setTextMode] = useState<'preview' | 'source'>('preview')
  const hasTextMode = kind === 'markdown' || kind === 'html'
  const text = useQuery({
    queryKey: ['file-preview-text', target.sourceKind, target.reference],
    queryFn: () => api.fetchPreviewText(target.previewUrl),
    enabled: kind === 'markdown' || kind === 'html' || kind === 'code',
    staleTime: 60_000,
    retry: false,
  })
  const documentPreview = useQuery({
    queryKey: ['file-preview-document', target.projectId, target.sourceKind, target.reference],
    queryFn: () => api.createFilePreview(target.projectId, { source_kind: target.sourceKind, reference: target.reference }),
    enabled: kind === 'office',
    staleTime: 10 * 60_000,
    retry: false,
  })

  useEffect(() => {
    const close = (event: KeyboardEvent) => { if (event.key === 'Escape') onBack() }
    document.addEventListener('keydown', close)
    return () => document.removeEventListener('keydown', close)
  }, [onBack])

  const externalUrl = target.previewUrl
  const error = text.error || documentPreview.error
  const sandboxedHTML = useMemo(() => kind === 'html' && text.data !== undefined ? buildSandboxedHTML(text.data) : '', [kind, text.data])
  return <section className="inspector-file-preview" aria-label={`预览 ${target.name}`}>
    <header>
      <button className="file-preview-back" type="button" onClick={onBack}><ArrowLeft size={15} />返回文件</button>
      <div className="file-preview-heading"><PreviewIcon kind={kind} /><span><strong title={target.name}>{target.name}</strong><small>{previewKindLabel(kind)} · {formatBytes(target.sizeBytes)}</small></span></div>
      <div className="file-preview-actions">
        {hasTextMode ? <div className="file-preview-mode-switch" role="group" aria-label="查看方式">
          <button className={textMode === 'preview' ? 'active' : ''} type="button" aria-pressed={textMode === 'preview'} onClick={() => setTextMode('preview')}><Eye size={14} />预览</button>
          <button className={textMode === 'source' ? 'active' : ''} type="button" aria-pressed={textMode === 'source'} onClick={() => setTextMode('source')}><FileCode2 size={14} />{kind === 'html' ? '源码' : '原文'}</button>
        </div> : null}
        <a href={externalUrl} target="_blank" rel="noreferrer" title="在新页面打开"><ExternalLink size={15} /><span>新页面</span></a>
        {target.downloadUrl ? <a href={target.downloadUrl} title="下载原文件"><Download size={15} /><span>下载</span></a> : null}
      </div>
    </header>
    <div className={`file-preview-body ${kind}`}>
      {kind === 'markdown' && textMode === 'preview' && text.data !== undefined ? <article className="file-preview-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{ a: ({ children, ...props }) => <a {...props} target="_blank" rel="noreferrer">{children}</a> }}>{text.data}</ReactMarkdown></article> : null}
      {kind === 'markdown' && textMode === 'source' && text.data !== undefined ? <CodePreview content={text.data} label="Markdown 原文" /> : null}
      {kind === 'html' && textMode === 'preview' && text.data !== undefined ? <iframe sandbox="" referrerPolicy="no-referrer" srcDoc={sandboxedHTML} title={`HTML 预览：${target.name}`} /> : null}
      {kind === 'html' && textMode === 'source' && text.data !== undefined ? <CodePreview content={text.data} label="HTML 源码" /> : null}
      {kind === 'code' && text.data !== undefined ? <CodePreview content={text.data} /> : null}
      {kind === 'pdf' ? <iframe src={target.previewUrl} title={`PDF 预览：${target.name}`} /> : null}
      {kind === 'image' ? <div className="file-preview-image"><img src={target.previewUrl} alt={target.name} /></div> : null}
      {kind === 'office' && documentPreview.data ? <div className="file-preview-pages">{documentPreview.data.pages.map((page, index) => <figure key={page}><img src={page} alt={`${target.name} 第 ${index + 1} 页`} loading={index < 2 ? 'eager' : 'lazy'} /><figcaption>{index + 1} / {documentPreview.data.page_count}</figcaption></figure>)}</div> : null}
      {(text.isLoading || documentPreview.isLoading) ? <div className="file-preview-state"><LoaderCircle className="spin" size={22} /><strong>{kind === 'office' ? '正在由本机文档运行时生成预览…' : '正在读取文件…'}</strong><span>原文件保持不变。</span></div> : null}
      {error ? <div className="file-preview-state error" role="alert"><TriangleAlert size={22} /><strong>暂时无法在面板内预览</strong><span>{error instanceof Error ? error.message : '预览失败'}</span><a href={externalUrl} target="_blank" rel="noreferrer">按原方式在新页面打开</a></div> : null}
      {kind === 'unsupported' ? <div className="file-preview-state"><TriangleAlert size={22} /><strong>这个格式尚未支持内嵌预览</strong><a href={externalUrl} target="_blank" rel="noreferrer">在新页面打开</a></div> : null}
    </div>
    {documentPreview.data?.warnings.length ? <footer><TriangleAlert size={13} /><span>{documentPreview.data.warnings.join('；')}</span></footer> : null}
  </section>
}

function CodePreview({ content, label = '源码内容' }: { content: string; label?: string }) {
  return <div className="file-preview-code" role="region" aria-label={label}>{content.split('\n').map((line, index) => <div key={index}><span aria-hidden="true">{index + 1}</span><code>{line || '\u00a0'}</code></div>)}</div>
}

function PreviewIcon({ kind }: { kind: FilePreviewKind }) {
  if (kind === 'office') return <FileSpreadsheet size={18} />
  if (kind === 'pdf' || kind === 'markdown') return <FileText size={18} />
  if (kind === 'html' || kind === 'code') return <FileCode2 size={18} />
  if (kind === 'image') return <Presentation size={18} />
  return <FileText size={18} />
}

function previewKindLabel(kind: FilePreviewKind) {
  const labels: Record<FilePreviewKind, string> = { markdown: 'Markdown', html: 'HTML', pdf: 'PDF', office: '本机文档预览', code: '源码', image: '图片', unsupported: '文件' }
  return labels[kind]
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / 1024 / 1024).toFixed(1)} MB`
}
