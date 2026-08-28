import { spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(fileURLToPath(new URL('..', import.meta.url)))
const python = String(process.env.OKTAPOD_DOCUMENT_RUNTIME_TEST_PYTHON || '').trim()
if (!python) throw new Error('OKTAPOD_DOCUMENT_RUNTIME_TEST_PYTHON must point to the self-contained Runtime Python executable')
const runtime = resolve(String(process.env.OKTAPOD_DOCUMENT_RUNTIME_TEST_ENTRYPOINT || join(root, 'Runtime', 'document-runtime', 'runtime.py')))
const workRoot = mkdtempSync(join(tmpdir(), 'oktapod-document-runtime-smoke-'))
const requests = []
let id = 0
const call = (method, params) => requests.push({ jsonrpc: '2.0', id: ++id, method, params })
call('health', {})
for (const format of ['pptx', 'docx', 'xlsx', 'pdf']) {
  const revision1 = `sessions/smoke/${format}/revision-1.${format}`
  const revision2 = `sessions/smoke/${format}/revision-2.${format}`
  const specification = {
    pptx: { slides: [{ title: 'Runtime smoke', body: 'before' }] },
    docx: { title: 'Runtime smoke', paragraphs: [{ text: 'before' }] },
    xlsx: { sheets: [{ name: 'Smoke', rows: [['before', 1]] }] },
    pdf: { pages: [{ text: 'Runtime smoke before' }] },
  }[format]
  const operations = {
    pptx: [{ op: 'replace_text', find: 'before', replace: 'after' }],
    docx: [{ op: 'replace_text', find: 'before', replace: 'after' }],
    xlsx: [{ op: 'set_cell', sheet: 'Smoke', cell: 'A1', value: 'after' }],
    pdf: [{ op: 'rotate_pages', pages: [1], degrees: 90 }],
  }[format]
  call('create', { format, output_handle: revision1, specification })
  call('open', { input_handle: revision1, format, mode: 'full' })
  call('apply', { input_handle: revision1, output_handle: revision2, format, operations })
  call('open', { input_handle: revision2, format, mode: 'full' })
  call('render', { input_handle: revision2, output_dir_handle: `renders/${format}`, format, selector: '' })
  call('export', { input_handle: revision2, output_handle: `exports/smoke.${format}`, source_format: format, format })
}

try {
  const result = spawnSync(python, ['-I', '-B', runtime], {
    cwd: root, encoding: 'utf8', maxBuffer: 32 * 1024 * 1024,
    input: `${requests.map((request) => JSON.stringify(request)).join('\n')}\n`,
    env: {
      OKTAPOD_DOCUMENT_ROOT: workRoot,
      OKTAPOD_LIBREOFFICE: String(process.env.OKTAPOD_DOCUMENT_RUNTIME_TEST_LIBREOFFICE || ''),
      OKTAPOD_TESSERACT: String(process.env.OKTAPOD_DOCUMENT_RUNTIME_TEST_TESSERACT || ''),
      PYTHONNOUSERSITE: '1', PYTHONDONTWRITEBYTECODE: '1', PYTHONUTF8: '1',
      HOME: workRoot, USERPROFILE: workRoot, TEMP: workRoot, TMP: workRoot,
      HTTP_PROXY: '', HTTPS_PROXY: '', ALL_PROXY: '', NO_PROXY: '*',
      PATH: process.env.PATH || '', SYSTEMROOT: process.env.SYSTEMROOT || '', WINDIR: process.env.WINDIR || '',
    },
  })
  if (result.status !== 0) throw new Error(`runtime process failed (${result.status}): ${result.stderr}`)
  const responses = result.stdout.trim().split(/\r?\n/).filter(Boolean).map((line) => JSON.parse(line))
  if (responses.length !== requests.length) throw new Error(`runtime returned ${responses.length} responses for ${requests.length} requests`)
  for (const response of responses) {
    if (response.error) throw new Error(`runtime call ${response.id} failed: ${response.error.code}: ${response.error.message}`)
  }
  const health = responses[0].result
  if (!health.available || health.protocol !== 'oktapod.document/v1') throw new Error(`runtime health is degraded: ${JSON.stringify(health)}`)
  for (const format of ['pptx', 'docx', 'xlsx', 'pdf']) {
    const output = join(workRoot, 'exports', `smoke.${format}`)
    if (!existsSync(output) || !statSync(output).isFile() || statSync(output).size === 0) throw new Error(`missing exported ${format} fixture`)
  }
  process.stdout.write(`Document Runtime smoke passed: ${responses.length} RPC calls across PPTX/DOCX/XLSX/PDF\n`)
} finally {
  const resolved = resolve(workRoot)
  const temp = resolve(tmpdir())
  if (resolved.startsWith(`${temp}${sep}`)) rmSync(resolved, { recursive: true, force: true })
}
