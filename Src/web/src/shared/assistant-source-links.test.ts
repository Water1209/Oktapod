import { describe, expect, it } from 'vitest'
import { normalizeAssistantSourceLinks } from './assistant-source-links'

describe('assistant source links', () => {
  it('links source titles to their first URL and preserves additional URLs', () => {
    const result = normalizeAssistantSourceLinks('主要来源：中国天气网南京今日预报（weather.com.cn/weather1d/101190101.shtml）；南京博物院官网及预约指南（njmuseum.com、m.nj.bendibao.com/tour/152187.shtm）')

    expect(result).toContain('[中国天气网南京今日预报](https://weather.com.cn/weather1d/101190101.shtml)')
    expect(result).toContain('[南京博物院官网及预约指南](https://njmuseum.com)')
    expect(result).toContain('[m.nj.bendibao.com/tour/152187.shtm](https://m.nj.bendibao.com/tour/152187.shtm)')
    expect(normalizeAssistantSourceLinks('**主要来源**：天气（weather.com.cn）')).toContain('[天气](https://weather.com.cn)')
  })

  it('does not rewrite ordinary prose or an existing Markdown source link', () => {
    expect(normalizeAssistantSourceLinks('普通文本 weather.com.cn/a')).toBe('普通文本 weather.com.cn/a')
    expect(normalizeAssistantSourceLinks('主要来源：[天气](https://weather.com.cn)')).toBe('主要来源：[天气](https://weather.com.cn)')
  })
})
