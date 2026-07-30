import { readFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'

const generatedPath = 'Src/internal/adapters/httpapi/generated/api.gen.go'
const result = spawnSync(
  'go',
  [
    'run',
    'github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1',
    '-config',
    'Src/api/oapi-codegen.yaml',
    'Src/api/openapi.yaml',
  ],
  { encoding: 'utf8', shell: false },
)

if (result.status !== 0) {
  process.stderr.write(result.stderr || 'OpenAPI generation failed\n')
  process.exit(result.status || 1)
}

const normalize = (value) => value.replaceAll('\r\n', '\n')
const committed = normalize(readFileSync(generatedPath, 'utf8'))
const generated = normalize(result.stdout)
if (committed !== generated) {
  process.stderr.write(`OpenAPI generated client drifted: regenerate ${generatedPath}\n`)
  process.exit(1)
}

process.stdout.write('OpenAPI generated client is current\n')
