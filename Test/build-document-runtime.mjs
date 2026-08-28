import { createHash, createPrivateKey, createPublicKey, sign, verify } from 'node:crypto'
import { execFileSync, spawnSync } from 'node:child_process'
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { arch, platform } from 'node:os'
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const packageManifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'))
const version = String(process.env.OKTAPOD_DOCUMENT_RUNTIME_VERSION || packageManifest.version || '').trim()
const targetPlatform = process.env.OKTAPOD_DOCUMENT_RUNTIME_PLATFORM || (platform() === 'win32' ? 'windows' : platform())
const targetArch = process.env.OKTAPOD_DOCUMENT_RUNTIME_ARCH || ({ x64: 'amd64', arm64: 'arm64' })[arch()] || arch()
const target = `${targetPlatform}-${targetArch}`
const stage = requiredDirectory('OKTAPOD_DOCUMENT_RUNTIME_STAGE')
const signingKeyFile = requiredFile('OKTAPOD_DOCUMENT_RUNTIME_SIGNING_KEY_FILE')
const keyId = requiredText('OKTAPOD_DOCUMENT_RUNTIME_SIGNING_KEY_ID')
const pythonRelative = safeRelative(requiredText('OKTAPOD_DOCUMENT_RUNTIME_PYTHON'))
const libreOfficeRelative = safeRelative(requiredText('OKTAPOD_DOCUMENT_RUNTIME_LIBREOFFICE'))
const tesseractRelative = safeRelative(requiredText('OKTAPOD_DOCUMENT_RUNTIME_TESSERACT'))
const outputRoot = resolve(root, 'Build', 'runtime', 'document-runtime')
const buildRoot = resolve(root, 'Build')

if (!existsSync(join(stage, 'LICENSES')) || !statSync(join(stage, 'LICENSES')).isDirectory()) {
  throw new Error('OKTAPOD_DOCUMENT_RUNTIME_STAGE must contain the complete third-party LICENSES directory')
}

assertWithin(outputRoot, buildRoot)
rmSync(outputRoot, { recursive: true, force: true })
mkdirSync(outputRoot, { recursive: true })
cpSync(stage, outputRoot, { recursive: true, dereference: false, errorOnExist: false })
cpSync(join(root, 'Runtime', 'document-runtime', 'runtime.py'), join(outputRoot, 'runtime.py'))
cpSync(join(root, 'Runtime', 'document-runtime', 'requirements.lock'), join(outputRoot, 'requirements.lock'))
cpSync(join(root, 'Runtime', 'document-runtime', 'THIRD_PARTY_NOTICES.md'), join(outputRoot, 'THIRD_PARTY_NOTICES.md'))
removePythonCaches(outputRoot)

for (const required of [pythonRelative, 'runtime.py', libreOfficeRelative, tesseractRelative].filter(Boolean)) {
  const path = resolve(outputRoot, required)
  assertWithin(path, outputRoot)
  if (!existsSync(path) || !statSync(path).isFile()) throw new Error(`staged runtime file is missing: ${required}`)
}

const packageVersions = readFileSync(join(outputRoot, 'requirements.lock'), 'utf8')
  .split(/\r?\n/).map((line) => line.trim()).filter((line) => line && !line.startsWith('#'))
  .map((line) => { const [name, packageVersion] = line.split('==', 2); return { name, version: packageVersion || '' } })
const sbom = {
  spdxVersion: 'SPDX-2.3', dataLicense: 'CC0-1.0', SPDXID: 'SPDXRef-DOCUMENT',
  name: `Oktapod-document-runtime-${version}-${target}`,
  documentNamespace: `https://oktapod.local/sbom/document-runtime/${version}/${target}`,
  creationInfo: { created: new Date().toISOString(), creators: ['Tool: Oktapod build-document-runtime.mjs'] },
  packages: packageVersions.map((item, index) => ({ SPDXID: `SPDXRef-Package-${index + 1}`, name: item.name, versionInfo: item.version, downloadLocation: 'NOASSERTION', filesAnalyzed: false, licenseConcluded: 'NOASSERTION', licenseDeclared: 'NOASSERTION' })),
}
writeFileSync(join(outputRoot, 'sbom.spdx.json'), `${JSON.stringify(sbom, null, 2)}\n`, 'utf8')

const files = Object.fromEntries(walkFiles(outputRoot).filter((item) => item !== 'runtime-manifest.json').map((item) => [item, sha256(join(outputRoot, item))]))
for (const required of [pythonRelative, 'runtime.py']) {
  if (!files[required]) throw new Error(`required runtime file is not covered by the signed hash list: ${required}`)
}
const payload = {
  schema_version: 'oktapod.document-runtime/v1', version, protocol: 'oktapod.document/v1', platform: target,
  python: pythonRelative, entrypoint: 'runtime.py',
  ...(libreOfficeRelative ? { libreoffice: libreOfficeRelative } : {}),
  ...(tesseractRelative ? { tesseract: tesseractRelative } : {}),
  files,
}
const payloadBytes = Buffer.from(JSON.stringify(payload), 'utf8')
const privateKey = createPrivateKey(readFileSync(signingKeyFile))
const publicKey = createPublicKey(privateKey)
const signature = sign(null, payloadBytes, privateKey)
if (!verify(null, payloadBytes, publicKey, signature)) throw new Error('runtime manifest signature self-check failed')
const envelope = { signed_payload: payloadBytes.toString('base64'), signature: { algorithm: 'ed25519', key_id: keyId, value: signature.toString('base64') } }
writeFileSync(join(outputRoot, 'runtime-manifest.json'), `${JSON.stringify(envelope, null, 2)}\n`, 'utf8')
const publicJWK = publicKey.export({ format: 'jwk' })
if (!publicJWK.x) throw new Error('Ed25519 public key export is invalid')
const trustedKeysPath = join(buildRoot, 'document-runtime-trusted-keys.json')
writeFileSync(trustedKeysPath, `${JSON.stringify({ [keyId]: Buffer.from(publicJWK.x, 'base64url').toString('base64') }, null, 2)}\n`, 'utf8')

verifyPackagedRuntime()

const archiveName = targetPlatform === 'windows' ? `document-runtime-v${version}-${target}.zip` : `document-runtime-v${version}-${target}.tar.gz`
const archivePath = join(buildRoot, archiveName)
rmSync(archivePath, { force: true })
const tarArgs = targetPlatform === 'windows'
  ? ['-a', '-c', '-f', archivePath, '-C', dirname(outputRoot), basename(outputRoot)]
  : ['-czf', archivePath, '-C', dirname(outputRoot), basename(outputRoot)]
execFileSync('tar', tarArgs, { stdio: 'inherit' })
const archiveDigest = sha256(archivePath)
writeFileSync(`${archivePath}.sha256`, `${archiveDigest.slice('sha256:'.length)}  ${archiveName}\n`, 'utf8')

process.stdout.write(`Document Runtime package: ${archivePath}\n`)
process.stdout.write(`Platform: ${target}\n`)
process.stdout.write(`Files: ${Object.keys(files).length}\n`)
process.stdout.write(`SHA256: ${archiveDigest}\n`)
process.stdout.write(`Trusted public key: ${trustedKeysPath}\n`)

function requiredText(name) {
  const value = String(process.env[name] || '').trim()
  if (!value) throw new Error(`${name} is required for a signed Document Runtime release`)
  return value
}

function requiredDirectory(name) {
  const value = resolve(requiredText(name))
  if (!existsSync(value) || !statSync(value).isDirectory()) throw new Error(`${name} must point to a staged self-contained runtime directory`)
  return value
}

function requiredFile(name) {
  const value = resolve(requiredText(name))
  if (!existsSync(value) || !statSync(value).isFile()) throw new Error(`${name} must point to an Ed25519 PKCS8 PEM file`)
  return value
}

function safeRelative(value) {
  const normalized = String(value).trim().replaceAll('\\', '/')
  if (!normalized || isAbsolute(normalized) || normalized === '..' || normalized.startsWith('../') || normalized.includes('/../') || /^[A-Za-z]:/.test(normalized)) throw new Error(`unsafe staged runtime path: ${value}`)
  return normalized
}

function assertWithin(path, parent) {
  const rel = relative(parent, path)
  if (!rel || rel === '..' || rel.startsWith(`..${sep}`) || isAbsolute(rel)) throw new Error(`refusing build output outside ${parent}`)
}

function walkFiles(directory, prefix = '') {
  const result = []
  for (const entry of readdirSync(join(directory, prefix), { withFileTypes: true })) {
    const relativePath = prefix ? `${prefix}/${entry.name}` : entry.name
    const absolute = join(directory, relativePath)
    if (entry.isSymbolicLink()) throw new Error(`runtime stage contains a symbolic link: ${relativePath}`)
    if (entry.isDirectory()) result.push(...walkFiles(directory, relativePath))
    else if (entry.isFile()) result.push(relativePath)
    else throw new Error(`runtime stage contains an unsupported entry: ${relativePath}`)
  }
  return result.sort()
}

function removePythonCaches(directory) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const absolute = join(directory, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === '__pycache__') {
        rmSync(absolute, { recursive: true, force: true })
        continue
      }
      removePythonCaches(absolute)
      continue
    }
    if (entry.isFile() && (entry.name.endsWith('.pyc') || entry.name.endsWith('.pyo'))) {
      rmSync(absolute, { force: true })
    }
  }
}

function sha256(path) { return `sha256:${createHash('sha256').update(readFileSync(path)).digest('hex')}` }

function verifyPackagedRuntime() {
  const python = resolve(outputRoot, pythonRelative)
  const libreOffice = resolve(outputRoot, libreOfficeRelative)
  const tesseract = resolve(outputRoot, tesseractRelative)
  const healthRoot = mkdtempSync(join(buildRoot, 'document-runtime-health-'))
  assertWithin(healthRoot, buildRoot)
  try {
    const dependencyCheck = spawnSync(python, ['-I', '-B', '-c', 'import defusedxml; from PIL import Image'], {
      cwd: outputRoot, encoding: 'utf8', timeout: 30_000,
      env: runtimeEnvironment(healthRoot, python, libreOffice, tesseract),
    })
    if (dependencyCheck.status !== 0) throw new Error(`packaged Runtime dependencies are incomplete: ${dependencyCheck.stderr || dependencyCheck.error || dependencyCheck.status}`)
    const request = `${JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'health', params: {} })}\n`
    const checked = spawnSync(python, ['-I', '-B', join(outputRoot, 'runtime.py')], {
      cwd: outputRoot, encoding: 'utf8', input: request, timeout: 60_000,
      env: runtimeEnvironment(healthRoot, python, libreOffice, tesseract),
    })
    if (checked.status !== 0) throw new Error(`packaged Runtime health process failed: ${checked.stderr || checked.error || checked.status}`)
    const response = JSON.parse(String(checked.stdout || '').trim())
    const health = response.result || {}
    if (!health.available || health.protocol !== 'oktapod.document/v1' || !health.libreoffice) throw new Error(`packaged Runtime health is degraded: ${JSON.stringify(health)}`)
    for (const language of ['chi_sim', 'eng']) {
      if (!health.ocr_languages?.includes(language)) throw new Error(`packaged Runtime is missing required OCR language: ${language}`)
    }
    execFileSync(process.execPath, [join(root, 'Test', 'document-runtime-smoke.mjs')], {
      cwd: root, stdio: 'inherit',
      env: {
        ...process.env,
        OKTAPOD_DOCUMENT_RUNTIME_TEST_PYTHON: python,
        OKTAPOD_DOCUMENT_RUNTIME_TEST_ENTRYPOINT: join(outputRoot, 'runtime.py'),
        OKTAPOD_DOCUMENT_RUNTIME_TEST_LIBREOFFICE: libreOffice,
        OKTAPOD_DOCUMENT_RUNTIME_TEST_TESSERACT: tesseract,
      },
    })
  } finally {
    rmSync(healthRoot, { recursive: true, force: true })
  }
}

function runtimeEnvironment(workRoot, python, libreOffice, tesseract) {
  return {
    OKTAPOD_DOCUMENT_ROOT: workRoot,
    OKTAPOD_LIBREOFFICE: libreOffice,
    OKTAPOD_TESSERACT: tesseract,
    PYTHONNOUSERSITE: '1', PYTHONDONTWRITEBYTECODE: '1', PYTHONUTF8: '1',
    HOME: workRoot, USERPROFILE: workRoot, TEMP: workRoot, TMP: workRoot,
    HTTP_PROXY: '', HTTPS_PROXY: '', ALL_PROXY: '', NO_PROXY: '*',
    PATH: [dirname(python), dirname(libreOffice), dirname(tesseract)].join(process.platform === 'win32' ? ';' : ':'),
    SYSTEMROOT: process.env.SYSTEMROOT || '', WINDIR: process.env.WINDIR || '',
  }
}
