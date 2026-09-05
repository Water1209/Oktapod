import { describe, expect, it } from 'vitest'
import { buildSandboxedHTML } from './html-preview'

describe('buildSandboxedHTML', () => {
	it('injects an isolated policy that permits inline app scripts', () => {
		const rendered = buildSandboxedHTML('<html><head><script>window.bad = true</script></head><body>Preview</body></html>')

		expect(rendered).toContain("default-src 'none'")
		expect(rendered).toContain("script-src 'unsafe-inline'")
		expect(rendered).toContain("form-action 'none'")
    expect(rendered.indexOf('Content-Security-Policy')).toBeLessThan(rendered.indexOf('<script>'))
  })

  it('wraps an HTML fragment in a complete preview document', () => {
    const rendered = buildSandboxedHTML('<main>Preview</main>')

    expect(rendered).toMatch(/^<!doctype html>/)
    expect(rendered).toContain('<body><main>Preview</main></body>')
  })
})
