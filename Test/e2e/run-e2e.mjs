import { spawn } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, writeFileSync } from 'node:fs'
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
mkdirSync(dataRoot, { recursive: true })
const configPath = resolve(dataRoot, 'config.yaml')
const browserStatePath = resolve(dataRoot, 'browser-state.json')
writeFileSync(configPath, `profile: personal\nlisten_address: 127.0.0.1:${apiPort}\ndata_root: ${JSON.stringify(dataRoot)}\nrun_concurrency: 4\ndatabase:\n  driver: sqlite\ncapabilities:\n  trusted_keys_file: trusted-keys.json\n${success ? `web_access:\n  private_fetch:\n    enabled: true\n    allowed_origins:\n      - id: e2e-private-docs\n        origin: http://127.0.0.1:19090\n` : ''}`)
const children = []
let activeTest
let cleaning = false
let modelConfigured = false
let apiProcess
const apiEnvironment = {
  ...process.env,
  OKTAPOD_CONFIG_FILE: configPath,
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
    const recoveryPath = resolve(dataRoot, 'recovery-code.txt')
    await run(apiBinary, ['vault', 'initialize', '--recovery-output', recoveryPath], repositoryRoot, apiEnvironment)
    await run(apiBinary, ['vault', 'confirm', '--recovery-file', recoveryPath], repositoryRoot, apiEnvironment)
    await run(apiBinary, ['model', 'configure', '--endpoint', 'http://127.0.0.1:19090/v1', '--model', 'model-e2e'], repositoryRoot, apiEnvironment, 'fake-e2e-key\n')
    modelConfigured = true
  }
  apiProcess = start(apiBinary, ['serve'], repositoryRoot, apiEnvironment, process.env.OKTAPOD_E2E_DEBUG === '1' ? 'inherit' : 'ignore')
  children.push(apiProcess)
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
  const playwrightEnvironment = {
    ...process.env,
    OKTAPOD_E2E_STATE_PATH: browserStatePath,
    OKTAPOD_E2E_DATA_ROOT: dataRoot,
  }
  activeTest = start(process.execPath, [playwrightCLI, ...args], webRoot, playwrightEnvironment, 'inherit')
  let code = await waitForExit(activeTest)
  if (mode === 'foundation' && code === 0) {
    await stop(apiProcess)
    apiProcess = start(apiBinary, ['serve'], repositoryRoot, apiEnvironment, process.env.OKTAPOD_E2E_DEBUG === '1' ? 'inherit' : 'ignore')
    children.push(apiProcess)
    await waitForURL(`http://127.0.0.1:${apiPort}/api/v1/health`)
    activeTest = start(process.execPath, [playwrightCLI, 'test', '--grep', 'Vault 在服务重启后'], webRoot, { ...playwrightEnvironment, OKTAPOD_E2E_RESTART_CHECK: '1' }, 'inherit')
    code = await waitForExit(activeTest)
  }
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
