import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { AlertTriangle, Download, FileJson, ShieldCheck, Upload } from 'lucide-react'
import { api, type ConversationMessage, type Project } from '../../shared/api/client'

type PortableConversation = {
  id?: string
  title?: string
  messages: Array<Pick<ConversationMessage, 'role' | 'content' | 'created_at'>>
}

type PortableArchive = {
  format: 'oktapod-browser-export-v1' | 'pi-jsonl-preview-v1'
  exported_at: string
  project: Pick<Project, 'name' | 'description' | 'scene'>
  conversations: PortableConversation[]
  redaction: { credentials_included: false; secret_fields_removed: string[] }
}

export function ImportExportPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects })
  const [projectId, setProjectId] = useState('')
  const selectedProject = projects.data?.items.find((item) => item.id === (projectId || projects.data?.items[0]?.id))
  const [archive, setArchive] = useState<PortableArchive>()
  const [sourceFile, setSourceFile] = useState<File>()
  const [parseError, setParseError] = useState('')
  const [exportFormat, setExportFormat] = useState<'json' | 'jsonl' | 'html'>('json')

  const exportProject = useMutation({
    mutationFn: async (project: Project) => {
      const conversations = await api.conversations(project.id)
      const portable: PortableConversation[] = []
      for (const conversation of conversations.items) {
        let after = 0
        const messages: PortableConversation['messages'] = []
        for (;;) {
          const page = await api.conversationMessages(conversation.id, after)
          messages.push(...page.items.map(({ role, content, created_at }) => ({ role, content: redactSensitiveText(content), created_at })))
          if (!page.next_cursor || page.next_cursor === after) break
          after = page.next_cursor
        }
        portable.push({ id: conversation.id, title: conversation.title, messages })
      }
      return {
        format: 'oktapod-browser-export-v1',
        exported_at: new Date().toISOString(),
        project: { name: project.name, description: project.description, scene: project.scene },
        conversations: portable,
        redaction: { credentials_included: false, secret_fields_removed: ['provider credentials', 'auth sessions', 'secret store references', 'sensitive message patterns'] },
      } satisfies PortableArchive
    },
    onSuccess: (value) => downloadArchive(value, exportFormat),
  })

  const importArchive = useMutation({
    mutationFn: async (value: PortableArchive) => {
      const created = await api.createProject({
        name: `${value.project.name || 'Imported project'} · Import`,
        description: `${value.project.description || ''}\nImported from ${value.format}; source transcript is preserved as a project asset pending Session Tree reconstruction.`.trim(),
        kind: 'personal',
        scene: value.project.scene === 'code' ? 'code' : 'work',
      })
      await api.importTranscript(created.id, {
        source_format: value.format,
        conversations: value.conversations.map((conversation) => ({ title: conversation.title || 'Imported Thread', messages: conversation.messages })),
      })
      const body = new File([JSON.stringify(value, null, 2)], sourceFile?.name || 'portable-transcript.json', { type: 'application/json' })
      await api.uploadAsset(created.id, body, 'private')
      return created
    },
    onSuccess: async (created) => {
      await queryClient.invalidateQueries({ queryKey: ['projects'] })
      await navigate({ to: '/projects/$projectId', params: { projectId: created.id }, search: { inspector: 'files' } })
    },
  })
  const counts = useMemo(() => archive ? {
    conversations: archive.conversations.length,
    messages: archive.conversations.reduce((total, conversation) => total + conversation.messages.length, 0),
  } : undefined, [archive])

  const parse = async (file: File) => {
    setSourceFile(file)
    setParseError('')
    try {
      const text = await file.text()
      const raw = file.name.toLowerCase().endsWith('.jsonl')
        ? text.split(/\r?\n/).filter(Boolean).map((line) => JSON.parse(line) as unknown)
        : JSON.parse(text) as unknown
      const sensitive = findSensitiveFields(raw)
      if (sensitive.length) throw new Error(`检测到敏感字段：${sensitive.slice(0, 5).join('、')}。请先脱敏后再导入。`)
      const parsed = parsePortableArchive(text, file.name)
      setArchive(parsed)
    } catch (error) {
      setArchive(undefined)
      setParseError(error instanceof Error ? error.message : '无法解析导入文件')
    }
  }

  return <main className="migration-page">
    <header className="management-heading"><div><span className="eyebrow">Data / Portable archive</span><h1>导入与导出</h1><p>先预览、再写入；Provider 凭据、登录会话和 SecretStore 引用永不进入浏览器导出包。</p></div></header>
    <div className="migration-grid">
      <section>
        <header><Download size={19}/><div><h2>导出 Oktapod 项目</h2><p>生成可检查 JSON，包含会话消息但不含 Secret。</p></div></header>
        <label className="form-field"><span>项目</span><select value={selectedProject?.id || ''} onChange={(event) => setProjectId(event.target.value)}>{projects.data?.items.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label>
        <label className="form-field"><span>格式</span><select aria-label="导出格式" value={exportFormat} onChange={(event) => setExportFormat(event.target.value as 'json' | 'jsonl' | 'html')}><option value="json">JSON · 可重新导入</option><option value="jsonl">JSONL · 流式交换</option><option value="html">HTML · 可读报告</option></select></label>
        <button className="primary-button" disabled={!selectedProject || exportProject.isPending} onClick={() => selectedProject && exportProject.mutate(selectedProject)}>{exportProject.isPending ? '正在整理…' : '下载脱敏导出包'}</button>
        <div className="migration-note"><ShieldCheck size={15}/>完整 CLI 项目归档仍可迁移 Blob、Workspace 与数据库关系；网页导出用于可读会话交换。</div>
      </section>
      <section>
        <header><Upload size={19}/><div><h2>导入 Pi / Oktapod</h2><p>支持 Oktapod JSON 和 Pi JSONL 预览；检测疑似凭据后 fail-closed。</p></div></header>
        <label className="import-drop"><FileJson size={24}/><span><strong>选择 JSON / JSONL</strong><small>先在浏览器本地解析，不会立即写入</small></span><input type="file" accept=".json,.jsonl,application/json" onChange={(event) => { const file = event.target.files?.[0]; if (file) void parse(file) }}/></label>
        {parseError ? <div className="form-error"><AlertTriangle size={15}/>{parseError}</div> : null}
        {archive && counts ? <div className="import-preview"><strong>{archive.project.name}</strong><dl><div><dt>格式</dt><dd>{archive.format}</dd></div><div><dt>会话</dt><dd>{counts.conversations}</dd></div><div><dt>消息</dt><dd>{counts.messages}</dd></div><div><dt>凭据</dt><dd>未发现</dd></div></dl><p>导入会重建个人私有会话与会话树节点，并将原始记录同时保存为项目资料以便复核；不会调用模型伪造回复。</p><button className="primary-button" disabled={importArchive.isPending} onClick={() => importArchive.mutate(archive)}>{importArchive.isPending ? '正在导入…' : '导入为个人项目'}</button>{importArchive.error ? <div className="form-error"><AlertTriangle size={15}/>{importArchive.error.message}</div> : null}</div> : null}
      </section>
    </div>
  </main>
}

function downloadArchive(value: PortableArchive, format: 'json' | 'jsonl' | 'html') {
  const content = format === 'json'
    ? JSON.stringify(value, null, 2)
    : format === 'jsonl'
      ? archiveJSONL(value)
      : archiveHTML(value)
  const mediaType = format === 'html' ? 'text/html' : format === 'jsonl' ? 'application/x-ndjson' : 'application/json'
  const blob = new Blob([content], { type: `${mediaType};charset=utf-8` })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `${safeName(value.project.name)}-oktapod-export.${format}`
  anchor.click()
  URL.revokeObjectURL(url)
}

function parsePortableArchive(text: string, filename: string): PortableArchive {
  if (filename.toLowerCase().endsWith('.jsonl')) {
    const records = text.split(/\r?\n/).filter(Boolean).map((line) => JSON.parse(line) as Record<string, unknown>)
    const ownHeader = records.find((record) => record.type === 'oktapod_archive')
    if (ownHeader) {
      const project = (ownHeader.project && typeof ownHeader.project === 'object' ? ownHeader.project : {}) as Record<string, unknown>
      const conversationRecords = records.filter((record) => record.type === 'oktapod_conversation')
      const conversations = conversationRecords.map((record) => ({
        id: String(record.conversation_id || ''),
        title: String(record.title || 'Imported Thread'),
        messages: records.filter((item) => item.type === 'oktapod_message' && item.conversation_id === record.conversation_id).map((item) => ({
          role: normalizeRole(item.role),
          content: String(item.content || ''),
          created_at: String(item.created_at || new Date().toISOString()),
        })),
      }))
      return {
        format: 'oktapod-browser-export-v1',
        exported_at: String(ownHeader.exported_at || new Date().toISOString()),
        project: { name: String(project.name || filename.replace(/\.jsonl$/i, '')), description: String(project.description || ''), scene: project.scene === 'code' ? 'code' : 'work' },
        conversations,
        redaction: { credentials_included: false, secret_fields_removed: ['portable export policy'] },
      }
    }
    const messages = records.flatMap((record) => normalizePiRecord(record))
    return {
      format: 'pi-jsonl-preview-v1',
      exported_at: new Date().toISOString(),
      project: { name: filename.replace(/\.jsonl$/i, ''), description: 'Imported Pi transcript', scene: 'work' },
      conversations: [{ title: 'Pi imported session', messages }],
      redaction: { credentials_included: false, secret_fields_removed: [] },
    }
  }
  const parsed = JSON.parse(text) as Partial<PortableArchive>
  if (!parsed.project || !Array.isArray(parsed.conversations)) throw new Error('不是受支持的 Oktapod 导出包')
  return {
    format: parsed.format === 'pi-jsonl-preview-v1' ? parsed.format : 'oktapod-browser-export-v1',
    exported_at: typeof parsed.exported_at === 'string' ? parsed.exported_at : new Date().toISOString(),
    project: { name: String(parsed.project.name || 'Imported project'), description: String(parsed.project.description || ''), scene: parsed.project.scene === 'code' ? 'code' : 'work' },
    conversations: parsed.conversations.map((conversation) => ({ title: String(conversation.title || ''), messages: Array.isArray(conversation.messages) ? conversation.messages.map((message) => ({ role: normalizeRole(message.role), content: String(message.content || ''), created_at: String(message.created_at || new Date().toISOString()) })) : [] })),
    redaction: { credentials_included: false, secret_fields_removed: Array.isArray(parsed.redaction?.secret_fields_removed) ? parsed.redaction.secret_fields_removed.map(String) : [] },
  }
}

function normalizePiRecord(record: Record<string, unknown>): PortableConversation['messages'] {
  const candidate = (record.message && typeof record.message === 'object' ? record.message : record) as Record<string, unknown>
  if (!['user', 'assistant', 'tool'].includes(String(candidate.role || ''))) return []
  const content = typeof candidate.content === 'string' ? candidate.content : JSON.stringify(candidate.content ?? '')
  return [{ role: normalizeRole(candidate.role), content, created_at: String(candidate.timestamp || record.timestamp || new Date().toISOString()) }]
}

function normalizeRole(value: unknown): ConversationMessage['role'] {
  return value === 'assistant' ? 'assistant' : value === 'tool' ? 'tool' : 'user'
}

function findSensitiveFields(value: unknown, path = ''): string[] {
  if (!value || typeof value !== 'object') return []
  const hits: string[] = []
  for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
    const next = path ? `${path}.${key}` : key
    const redactionMetadata = path === 'redaction' && ['credentials_included', 'secret_fields_removed'].includes(key)
    if (!redactionMetadata && /(api[_-]?key|password|access[_-]?token|refresh[_-]?token|credential|secret)/i.test(key) && child) hits.push(next)
    if (typeof child === 'object') hits.push(...findSensitiveFields(child, next))
  }
  return hits
}

function safeName(value: string) {
  return [...value.trim().replace(/[<>:"/\\|?*]/g, '-')].filter((character) => character.charCodeAt(0) >= 32).join('').slice(0, 80) || 'project'
}

function redactSensitiveText(value: string) {
  return value
    .replace(/\bBearer\s+[A-Za-z0-9._~+/=-]{8,}/gi, 'Bearer [REDACTED]')
    .replace(/\b(sk|rk|pk)-[A-Za-z0-9_-]{12,}\b/g, '$1-[REDACTED]')
    .replace(/\b(api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret)\s*[:=]\s*["']?[^\s"',;]{6,}/gi, '$1=[REDACTED]')
}

function archiveJSONL(value: PortableArchive) {
  const records: Record<string, unknown>[] = [{
    type: 'oktapod_archive',
    version: 1,
    exported_at: value.exported_at,
    project: value.project,
    redaction: value.redaction,
  }]
  for (const conversation of value.conversations) {
    const conversationId = conversation.id || crypto.randomUUID()
    records.push({ type: 'oktapod_conversation', conversation_id: conversationId, title: conversation.title || 'Thread' })
    for (const message of conversation.messages) {
      records.push({ type: 'oktapod_message', conversation_id: conversationId, ...message })
    }
  }
  return records.map((record) => JSON.stringify(record)).join('\n') + '\n'
}

function archiveHTML(value: PortableArchive) {
  const threads = value.conversations.map((conversation) => `<section><h2>${escapeHTML(conversation.title || 'Thread')}</h2>${conversation.messages.map((message) => `<article class="${escapeHTML(message.role)}"><header>${escapeHTML(message.role)} · ${escapeHTML(message.created_at)}</header><pre>${escapeHTML(message.content)}</pre></article>`).join('')}</section>`).join('')
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>${escapeHTML(value.project.name)}</title><style>body{max-width:900px;margin:40px auto;padding:0 20px;font:15px/1.6 system-ui;color:#172033;background:#f6f7f9}h1,h2{color:#071454}section{margin:30px 0}article{margin:12px 0;padding:14px;border:1px solid #d9dee8;border-radius:8px;background:#fff}header{font-size:12px;color:#667085}pre{white-space:pre-wrap;overflow-wrap:anywhere;font:13px/1.6 ui-monospace,monospace}.assistant{border-left:3px solid #2455e6}.tool{border-left:3px solid #18c6d8}</style></head><body><h1>${escapeHTML(value.project.name)}</h1><p>${escapeHTML(value.project.description ?? '')}</p><small>Exported ${escapeHTML(value.exported_at)} · credentials excluded</small>${threads}</body></html>`
}

function escapeHTML(value: string) {
  return value.replace(/[&<>"']/g, (character) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' } as Record<string, string>)[character] ?? character)
}
