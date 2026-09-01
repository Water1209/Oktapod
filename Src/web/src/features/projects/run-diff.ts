export interface RunDiffHunk {
  header: string
  lines: string[]
}

export interface RunDiffFile {
  path: string
  header: string[]
  hunks: RunDiffHunk[]
}

function diffPath(line: string) {
  const raw = line.slice(4).trim()
  if (!raw || raw === '/dev/null') return ''
  const unquoted = raw.startsWith('"') && raw.endsWith('"') ? raw.slice(1, -1) : raw
  return unquoted.replace(/^[ab]\//, '')
}

function diffGitPath(line: string) {
  const marker = line.lastIndexOf(' b/')
  if (marker >= 0) return diffPath(`+++ ${line.slice(marker + 1)}`)
  const quotedMarker = line.lastIndexOf('" "b/')
  if (quotedMarker < 0 || !line.endsWith('"')) return ''
  return line.slice(quotedMarker + 5, -1)
}

export function isTemporaryDiffPath(path: string) {
  const clean = path.trim().replaceAll('\\', '/').toLowerCase()
  const parts = clean.split('/').filter(Boolean)
  const root = parts[0] ?? ''
  const base = parts.at(-1) ?? ''
  if (clean === '.oktapod' || clean.startsWith('.oktapod/')) return true
  if (['pip', 'tmp', 'temp', '.tmp', '.temp'].includes(root)) return true
  if (parts.some((part) => ['node_modules', '__pycache__', '.pytest_cache', '.mypy_cache', '.ruff_cache', '.venv', 'venv', '.tox', '.nox'].includes(part))) return true
  return ['.extract_', '.generate_', '.render_'].some((prefix) => base.startsWith(prefix))
    || base.endsWith('.tmp')
    || base.endsWith('.log')
}

export function parseUnifiedDiff(diff: string): RunDiffFile[] {
  const files: RunDiffFile[] = []
  let current: RunDiffFile | undefined
  let hunk: RunDiffHunk | undefined

  const ensureFile = () => {
    if (!current) {
      current = { path: 'workspace.diff', header: [], hunks: [] }
      files.push(current)
    }
    return current
  }

  for (const line of diff.split(/\r?\n/)) {
    if (line.startsWith('diff --git ')) {
      current = { path: diffGitPath(line), header: [line], hunks: [] }
      files.push(current)
      hunk = undefined
      continue
    }
    const file = ensureFile()
    if (line.startsWith('+++ ')) {
      file.path = diffPath(line) || file.path
    } else if (line.startsWith('--- ') && !file.path) {
      file.path = diffPath(line)
    }
    if (line.startsWith('@@')) {
      hunk = { header: line, lines: [] }
      file.hunks.push(hunk)
    } else if (hunk) {
      hunk.lines.push(line)
    } else {
      file.header.push(line)
    }
  }

  return files
    .filter((file) => file.header.some(Boolean) || file.hunks.length > 0)
    .map((file, index) => ({ ...file, path: file.path || `change-${index + 1}` }))
}
