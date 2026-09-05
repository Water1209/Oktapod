import { describe, expect, it } from 'vitest'
import { filePreviewKind } from './file-preview-kind'

describe('filePreviewKind', () => {
  it.each([
    ['方案.MD', '', 'markdown'],
    ['报告.html', '', 'html'],
    ['页面.htm', 'text/plain', 'html'],
    ['报告.pdf', 'application/octet-stream', 'pdf'],
    ['说明.docx', '', 'office'],
    ['数据.xlsx', '', 'office'],
    ['演示.pptx', '', 'office'],
    ['worker.go', 'text/plain', 'code'],
    ['Dockerfile', '', 'code'],
    ['diagram.svg', 'image/svg+xml', 'image'],
    ['archive.zip', 'application/zip', 'unsupported'],
    ['legacy.doc', 'application/msword', 'unsupported'],
  ])('routes %s to %s', (name, mediaType, expected) => {
    expect(filePreviewKind(name, mediaType)).toBe(expected)
  })
})
