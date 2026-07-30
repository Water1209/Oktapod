import { createServer } from 'node:http'


const server = createServer(async (request, response) => {
  response.setHeader('Content-Type', 'application/json')
  if (request.url === '/health') {
    response.end('{"status":"ok"}')
    return
  }
  if (request.headers.authorization !== 'Bearer fake-e2e-key') {
    response.writeHead(401)
    response.end('{"error":"unauthorized"}')
    return
  }
  if (request.url === '/v1/models') {
    response.end('{"data":[{"id":"model-e2e"}]}')
    return
  }
  if (request.url === '/v1/chat/completions' && request.method === 'POST') {
    const body = await readJSON(request)
    const userContent = Array.isArray(body.messages) ? body.messages.findLast((message) => message.role === 'user')?.content ?? '' : ''
    const hasToolResults = Array.isArray(body.messages) && body.messages.some((message) => message.role === 'tool')
    if (process.env.OKTAPOD_E2E_DEBUG === '1') process.stderr.write(`[mock-provider] chat request ${JSON.stringify({ stream: body.stream, userContent: userContent.slice(0, 80) })}\n`)
    const systemContent = Array.isArray(body.messages) ? body.messages.find((message) => message.role === 'system')?.content ?? '' : ''
    if (systemContent.includes("route a user's project message")) {
      const route = /创建|修改|运行|执行|文件|测试|task/i.test(userContent) ? 'task' : 'chat'
      response.end(JSON.stringify({
        id: `provider-e2e-classifier-${route}`,
        choices: [{ message: { role: 'assistant', content: JSON.stringify({ route }) } }],
        usage: { prompt_tokens: 12, completion_tokens: 3 },
      }))
      return
    }
    if (userContent.includes('受控工具审批验收')) {
      if (hasToolResults) {
        response.end(JSON.stringify({
          id: 'provider-e2e-tools-finished',
          choices: [{ message: { role: 'assistant', content: 'E2E 模型执行成功。' } }],
          usage: { prompt_tokens: 24, completion_tokens: 7 },
        }))
        return
      }
      response.end(JSON.stringify({
        id: 'provider-e2e-tools-1',
        choices: [{ message: {
          role: 'assistant',
          content: '已创建验收文件，并准备运行冻结的验证命令。',
          tool_calls: [
            { id: 'provider-e2e-write', type: 'function', function: { name: 'workspace_write_file', arguments: JSON.stringify({ path: 'e2e/approval.txt', content: 'approval persisted\n' }) } },
            { id: 'provider-e2e-command', type: 'function', function: { name: 'workspace_run_command', arguments: JSON.stringify({ program: 'node', args: ['--version'] }) } },
          ],
        } }],
        usage: { prompt_tokens: 30, completion_tokens: 14 },
      }))
      return
    }
    if (userContent.includes('提交以下已选文件')) {
      if (hasToolResults) {
        response.end(JSON.stringify({
          id: 'provider-e2e-changes-commit-finished',
          choices: [{ message: { role: 'assistant', content: '已按所选路径完成提交并复核工作区状态。' } }],
          usage: { prompt_tokens: 24, completion_tokens: 8 },
        }))
        return
      }
      const statusHash = userContent.match(/状态哈希为 ([^。\s]+)/)?.[1] ?? ''
      const message = userContent.match(/提交信息为：(.+?)。当前工作区/)?.[1] ?? 'Commit selected changes'
      const paths = userContent.split(/\r?\n/).filter((line) => line.startsWith('- ')).map((line) => line.slice(2).trim()).filter(Boolean)
      response.end(JSON.stringify({
        id: 'provider-e2e-changes-commit',
        choices: [{ message: {
          role: 'assistant',
          content: '核对冻结状态后，只提交用户在 Changes 中选择的文件。',
          tool_calls: [
            { id: 'provider-e2e-git-commit-paths', type: 'function', function: { name: 'git_commit_paths', arguments: JSON.stringify({ message, paths, expected_status_hash: statusHash }) } },
          ],
        } }],
        usage: { prompt_tokens: 30, completion_tokens: 14 },
      }))
      return
    }
    if (userContent.includes('返回 E2E 模型执行结果')) {
      if (hasToolResults) {
        response.end(JSON.stringify({
          id: 'provider-e2e-result-finished',
          choices: [{ message: { role: 'assistant', content: 'E2E 模型执行成功。' } }],
          usage: { prompt_tokens: 24, completion_tokens: 7 },
        }))
        return
      }
      response.end(JSON.stringify({
        id: 'provider-e2e-result-artifact',
        choices: [{ message: {
          role: 'assistant',
          content: '先写入执行结果文件，再总结。',
          tool_calls: [
            { id: 'provider-e2e-result-write', type: 'function', function: { name: 'workspace_write_file', arguments: JSON.stringify({ path: '执行结果.md', content: 'E2E 模型执行成功。\n' }) } },
            { id: 'provider-e2e-summary-write', type: 'function', function: { name: 'workspace_write_file', arguments: JSON.stringify({ path: '执行摘要.md', content: '# 执行摘要\n\n| 项目 | 结果 |\n| --- | --- |\n| 浏览器验收 | 通过 |\n' }) } },
          ],
        } }],
        usage: { prompt_tokens: 30, completion_tokens: 14 },
      }))
      return
    }
    if (body.stream === true) {
      response.setHeader('Content-Type', 'text/event-stream')
      response.write('data: {"id":"provider-e2e-stream","choices":[{"delta":{"content":"E2E 模型"}}]}\n\n')
      await delay(800)
      response.write('data: {"id":"provider-e2e-stream","choices":[{"delta":{"content":"执行成功。"}}],"usage":{"prompt_tokens":18,"completion_tokens":7}}\n\n')
      response.end('data: [DONE]\n\n')
      return
    }
    response.end(JSON.stringify({
      id: 'provider-e2e-1',
      choices: [{ message: { role: 'assistant', content: 'E2E 模型执行成功。' } }],
      usage: { prompt_tokens: 18, completion_tokens: 7 },
    }))
    return
  }
  response.writeHead(404)
  response.end('{"error":"not_found"}')
})

server.listen(19090, '127.0.0.1')

async function readJSON(request) {
  const chunks = []
  for await (const chunk of request) chunks.push(chunk)
  return JSON.parse(Buffer.concat(chunks).toString('utf8'))
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}
