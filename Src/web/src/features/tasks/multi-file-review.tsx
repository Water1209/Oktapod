import { useContext, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Download, FileText, Files, LoaderCircle, Maximize2, Minimize2, X } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { api, type Artifact, type Task } from '../../shared/api/client'
import { FileReviewContext, type FileReviewState } from './file-review-context'

// FileReviewProvider 由 ProjectPage/TaskPage 包裹：消息流和任务详情均可触发共享的右侧栏文件审阅，避免每条消息独立弹出模态。

export function FileReviewProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const [state, setState] = useState({ task: null as Task | null, artifacts: [] as Artifact[] })
  const value = useMemo<FileReviewState>(() => ({
    open: (artifacts, task) => setState({ task, artifacts }),
    close: () => setState({ task: null, artifacts: [] }),
  }), [])
  const accept = useMutation({
    mutationFn: () => api.acceptTaskOutcome(state.task!.id, `已审阅 ${state.artifacts.length} 个交付文件`),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['task', state.task!.id] }),
        queryClient.invalidateQueries({ queryKey: ['tasks', state.task!.project_id] }),
      ])
    },
  })
  return <FileReviewContext.Provider value={value}>
    {children}
    {state.task && state.artifacts.length > 0 ? <FileReviewPanel
      task={state.task}
      artifacts={state.artifacts}
      acceptPending={accept.isPending}
      acceptError={accept.error?.message ?? ''}
      onAccept={() => accept.mutate()}
      onClose={value.close}
    /> : null}
  </FileReviewContext.Provider>
}


// FileReviewPanel 是右侧栏文件审阅面板：左侧文件列表 + 右侧预览，支持滚动、
// 最大化与恢复。替代原 MultiFileReviewDialog 的模态形态。
export function FileReviewPanel({
  task,
  artifacts,
  onClose,
  onAccept,
  acceptPending,
  acceptError,
}: {
  task: Task
  artifacts: Artifact[]
  onClose: () => void
  onAccept: () => void
  acceptPending: boolean
  acceptError: string
}) {
  const [internalSelectedId, setSelectedId] = useState(artifacts[0]?.id ?? '')
  const [maximized, setMaximized] = useState(false)
  const selectedId = artifacts.some((a) => a.id === internalSelectedId) ? internalSelectedId : artifacts[0]?.id ?? ''
  const selected = artifacts.find((artifact) => artifact.id === selectedId) ?? artifacts[0]

  const content = useQuery({
    queryKey: ['artifact-preview', selected?.id],
    queryFn: () => api.fetchArtifactContent(selected!.id),
    enabled: Boolean(selected) && (selected ? !isVisualArtifact(selected) : false),
    staleTime: Infinity,
  })

  if (!selected) return null

  return (
    <aside className={`file-review-panel ${maximized ? 'maximized' : ''}`} aria-label={`审阅任务文件 · ${task.title}`}>
      <header className="file-review-panel-header">
        <div className="file-review-panel-title">
          <Files size={15} />
          <div>
            <strong>审阅任务文件</strong>
            <small>{artifacts.length} 个文件 · {task.title}</small>
          </div>
        </div>
        <div className="file-review-panel-actions">
          <button type="button" className="icon-button" aria-label={maximized ? '恢复' : '最大化'} title={maximized ? '恢复' : '最大化'} onClick={() => setMaximized((value) => !value)}>
            {maximized ? <Minimize2 size={15} /> : <Maximize2 size={15} />}
          </button>
          <button type="button" className="icon-button" aria-label="关闭审阅" title="关闭审阅" onClick={onClose}>
            <X size={16} />
          </button>
        </div>
      </header>
      <div className="file-review-panel-workspace">
        <nav className="file-review-panel-sidebar" aria-label="待审阅文件">
          <div className="file-review-panel-sidebar-title"><Files size={13} /><span>文件</span><small>{artifacts.length}</small></div>
          <div className="file-review-panel-file-list">
            {artifacts.map((artifact) => (
              <button
                type="button"
                className={artifact.id === selected.id ? 'active' : ''}
                key={artifact.id}
                title={artifact.name}
                onClick={() => setSelectedId(artifact.id)}
              >
                <FileText size={13} />
                <span><span className="file-review-file-name">{artifact.name}</span><small>{formatBytes(artifact.size_bytes)}</small></span>
              </button>
            ))}
          </div>
        </nav>
        <section className="file-review-panel-content" aria-label={`预览 ${selected.name}`}>
          <div className="file-review-panel-content-header">
            <span><FileText size={13} />{selected.name}</span>
            <a href={api.artifactPreviewUrl(selected.id)} download={selected.name}><Download size={13} />下载</a>
          </div>
          <div className="file-review-panel-document markdown-preview-body">
            {isVisualArtifact(selected)
              ? <img src={api.artifactPreviewUrl(selected.id)} alt={selected.name} />
              : content.isLoading
                ? <div className="markdown-preview-loading"><LoaderCircle className="spin" size={20} /><span>正在读取文件…</span></div>
                : content.error
                  ? <div className="markdown-preview-error">文件读取失败：{content.error.message}</div>
                  : isMarkdownArtifact(selected)
                    ? <ReactMarkdown remarkPlugins={[remarkGfm]}>{content.data ?? ''}</ReactMarkdown>
                    : <pre className="file-review-plain-text">{content.data ?? ''}</pre>}
          </div>
        </section>
      </div>
      <footer className="file-review-panel-footer">
        <span>{task.accepted ? <><Check size={13} />已审核此任务结果</> : '确认文件内容符合任务要求后完成审核。'}</span>
        <div>
          <button type="button" className="file-review-secondary" onClick={onClose}>稍后处理</button>
          <button type="button" className="file-review-primary" disabled={task.accepted || acceptPending} onClick={onAccept}>
            <Check size={13} />{task.accepted ? '已审核' : acceptPending ? '正在审核…' : '完成审核'}
          </button>
        </div>
        {acceptError ? <small className="file-review-error">审核失败：{acceptError}</small> : null}
      </footer>
    </aside>
  )
}

// MultiFileReviewDialog 保留为旧调用方的兼容壳：内部不再使用 portal，直接
// 通过共享的 FileReviewContext 触发右侧栏文件面板。
export function MultiFileReviewDialog({
  task,
  artifacts,
  open,
  onOpenChange,
}: {
  task: Task
  artifacts: Artifact[]
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const review = useContext(FileReviewContext)
  if (!open) return null
  if (!review) {
    return <FileReviewPanel task={task} artifacts={artifacts} onClose={() => onOpenChange(false)} onAccept={() => onOpenChange(false)} acceptPending={false} acceptError="" />
  }
  review.open(artifacts, task)
  return null
}

function isMarkdownArtifact(artifact: Artifact) {
  return artifact.media_type.includes('markdown') || artifact.name.toLowerCase().endsWith('.md')
}

function isVisualArtifact(artifact: Artifact) {
  return artifact.media_type.startsWith('image/')
}

function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`
}
