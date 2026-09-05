const sandboxPolicy = [
  "default-src 'none'",
  "img-src data: blob:",
  "media-src data: blob:",
  "style-src 'unsafe-inline'",
  "font-src data:",
  "object-src 'none'",
  "frame-src 'none'",
  "form-action 'none'",
  "base-uri 'none'",
].join('; ')

const policyMeta = `<meta http-equiv="Content-Security-Policy" content="${sandboxPolicy}">`

export function buildSandboxedHTML(content: string) {
  if (/<head(?:\s[^>]*)?>/i.test(content)) {
    return content.replace(/<head(?:\s[^>]*)?>/i, (head) => `${head}${policyMeta}`)
  }
  if (/<html(?:\s[^>]*)?>/i.test(content)) {
    return content.replace(/<html(?:\s[^>]*)?>/i, (html) => `${html}<head>${policyMeta}</head>`)
  }
  return `<!doctype html><html><head>${policyMeta}</head><body>${content}</body></html>`
}
