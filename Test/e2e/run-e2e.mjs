import { spawn } from 'node:child_process'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'

const mode = process.argv[2]
if (!['foundation', 'success', 'approval', 'turn', 'auth'].includes(mode)) {
  throw new Error('usage: node e2e/run-e2e.mjs <foundation|success|approval|turn|auth>')
}

const repositoryRoot = resolve(import.meta.dirname, '../..')
const webRoot = resolve(repositoryRoot, 'Src/web')
const webRequire = createRequire(resolve(webRoot, 'package.json'))
const apiBinary = resolve(repositoryRoot, 'Build', process.platform === 'win32' ? 'oktapod-e2e.exe' : 'oktapod-e2e')
const viteCLI = resolve(dirname(webRequire.resolve('vite')), '../../bin/vite.js')
const playwrightCLI = webRequire.resolve('@playwright/test/cli')
const success = ['success', 'approval', 'turn'].includes(mode)
const apiPort = mode === 'auth' ? 18789 : success ? 18788 : 18787
const webPort = mode === 'auth' ? 15175 : success ? 15174 : 15173
const dataRoot = resolve(tmpdir(), 'oktapod-e2e', `${mode}-${process.pid}-${Date.now().toString(36)}`)
const children = []
let activeTest
let cleaning = false
let modelConfigured = false
const apiEnvironment = {
  ...process.env,
  OKTAPOD_DATA_ROOT: dataRoot,
  OKTAPOD_LISTEN_ADDRESS: `127.0.0.1:${apiPort}`,
}

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.once(signal, async () => {
    await cleanup()
    process.exit(130)
  })
}

try {
  if (success) {
    children.push(start(process.execPath, [resolve(repositoryRoot, 'Test/e2e', 'mock-provider.mjs')], webRoot, process.env, process.env.OKTAPOD_E2E_DEBUG === '1' ? 'inherit' : 'ignore'))
    await waitForURL('http://127.0.0.1:19090/health')
    await run(apiBinary, ['model', 'configure', '--endpoint', 'http://127.0.0.1:19090/v1', '--model', 'model-e2e'], repositoryRoot, apiEnvironment, 'fake-e2e-key\n')
    modelConfigured = true
  }
  children.push(start(apiBinary, ['serve'], repositoryRoot, apiEnvironment, process.env.OKTAPOD_E2E_DEBUG === '1' ? 'inherit' : 'ignore'))
  await waitForURL(`http://127.0.0.1:${apiPort}/api/v1/health`)
  children.push(start(process.execPath, [viteCLI, '--host', '127.0.0.1', '--port', String(webPort)], webRoot, {
    ...process.env,
    OKTAPOD_API_PROXY: `http://127.0.0.1:${apiPort}`,
    OKTAPOD_WEB_PORT: String(webPort),
  }))
  await waitForURL(`http://127.0.0.1:${webPort}`)

  const args = ['test']
  if (mode === 'success' || mode === 'approval') args.push('--config', 'playwright.success.config.ts')
  if (mode === 'turn') args.push('--config', 'playwright.turn.config.ts')
  if (mode === 'auth') args.push('--config', 'playwright.auth.config.ts')
  if (mode === 'approval') args.push('--grep', 'L2 命令')
  args.push(...process.argv.slice(3))
  activeTest = start(process.execPath, [playwrightCLI, ...args], webRoot, process.env, 'inherit')
  const code = await waitForExit(activeTest)
  process.exitCode = code ?? 1
} finally {
  await cleanup()
  if (modelConfigured) {
    await run(apiBinary, ['model', 'reset'], repositoryRoot, apiEnvironment)
  }
}

function start(command, args, cwd, env, stdio = 'ignore') {
  return spawn(command, args, { cwd, env, stdio, shell: false, windowsHide: true })
}

async function run(command, args, cwd, env, input = '') {
  const child = spawn(command, args, { cwd, env, stdio: ['pipe', 'ignore', 'pipe'], shell: false, windowsHide: true })
  let errorOutput = ''
  child.stderr.setEncoding('utf8')
  child.stderr.on('data', (chunk) => { errorOutput += chunk })
  child.stdin.end(input)
  const code = await waitForExit(child)
  if (code !== 0) throw new Error(`${command} ${args.join(' ')} failed: ${errorOutput.trim()}`)
}

async function cleanup() {
  if (cleaning) return
  cleaning = true
  if (activeTest) await stop(activeTest)
  for (const child of children.reverse()) await stop(child)
}

async function waitForURL(url) {
  const deadline = Date.now() + 120_000
  let lastError
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url)
      if (response.ok) return
      lastError = new Error(`${url} returned ${response.status}`)
    } catch (error) {
      lastError = error
    }
    await delay(200)
  }
  throw new Error(`service did not become ready: ${url}`, { cause: lastError })
}

function waitForExit(child) {
  if (child.exitCode !== null) return Promise.resolve(child.exitCode)
  return new Promise((resolveExit, reject) => {
    child.once('error', reject)
    child.once('exit', resolveExit)
  })
}

async function stop(child) {
  if (child.exitCode !== null || child.signalCode !== null) return
  child.kill()
  const exited = await Promise.race([waitForExit(child).then(() => true), delay(2_000).then(() => false)])
  if (!exited && child.exitCode === null) {
    child.kill('SIGKILL')
    await Promise.race([waitForExit(child), delay(2_000)])
  }
}

function delay(milliseconds) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds))
}
