import { useQuery } from '@tanstack/react-query'
import * as Dialog from '@radix-ui/react-dialog'
import { LoaderCircle, Download } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { api, type Artifact } from './api/client'

export function MarkdownPreviewDialog({
  artifact,
  open,
  onOpenChange,
}: {
  artifact: Artifact | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const content = useQuery({
    queryKey: ['artifact-preview', artifact?.id],
    queryFn: () => api.fetchArtifactContent(artifact!.id),
    enabled: open && Boolean(artifact),
    staleTime: Infinity,
  })

  if (!artifact) return null

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content className="dialog-content markdown-preview-dialog">
          <div className="dialog-heading">
            <div>
              <Dialog.Title>{artifact.name}</Dialog.Title>
              <Dialog.Description>
                {artifact.kind === 'evidence' ? '研究材料' : '交付文件'} · {artifact.media_type} · {formatBytes(artifact.size_bytes)}
              </Dialog.Description>
            </div>
            <div className="markdown-preview-actions">
              <a
                className="icon-button"
                href={api.artifactPreviewUrl(artifact.id)}
                download={artifact.name}
                title="下载原文件"
              >
                <Download size={16} />
              </a>
              <Dialog.Close className="icon-button" aria-label="关闭">×</Dialog.Close>
            </div>
          </div>
          <div className="markdown-preview-body">
            {content.isLoading ? (
              <div className="markdown-preview-loading">
                <LoaderCircle className="spin" size={20} />
                <span>正在读取文件…</span>
              </div>
            ) : content.error ? (
              <div className="markdown-preview-error">文件读取失败：{content.error.message}</div>
            ) : (
              <ReactMarkdown remarkPlugins={[remarkGfm]}>{content.data ?? ''}</ReactMarkdown>
            )}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}

function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`
}
