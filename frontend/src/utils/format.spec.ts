import { describe, expect, it } from 'vitest'
import {
  formatBytes,
  formatDuration,
  formatElapsed,
  formatTime,
  rangeToFromISO,
} from './format'

describe('formatBytes', () => {
  it('空值、零与负数返回 0 B', () => {
    expect(formatBytes()).toBe('0 B')
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(-5)).toBe('0 B')
  })

  it('小于 1KB 不换算、不带小数', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1023)).toBe('1023 B')
  })

  it('KB 档位换算与 0/1 位小数规则', () => {
    expect(formatBytes(1024)).toBe('1.0 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    // 100 及以上保留 0 位小数
    expect(formatBytes(102400)).toBe('100 KB')
  })

  it('MB/GB/TB 逐级换算', () => {
    expect(formatBytes(1048576)).toBe('1.0 MB')
    expect(formatBytes(1073741824)).toBe('1.0 GB')
    expect(formatBytes(1099511627776)).toBe('1.0 TB')
  })

  it('超过 TB 上限时停留在 TB 档位', () => {
    expect(formatBytes(2 * 1024 ** 4)).toBe('2.0 TB')
    expect(formatBytes(2048 * 1024 ** 4)).toBe('2048 TB')
  })
})

describe('formatDuration', () => {
  it('空值、零与负数按 0 秒处理', () => {
    expect(formatDuration()).toBe('0秒')
    expect(formatDuration(0)).toBe('0秒')
    expect(formatDuration(-10)).toBe('0秒')
  })

  it('分钟以内只显示秒', () => {
    expect(formatDuration(45)).toBe('45秒')
  })

  it('分钟档位显示 分+秒', () => {
    expect(formatDuration(90)).toBe('1分30秒')
  })

  it('小时档位显示 时+分（丢弃秒）', () => {
    expect(formatDuration(3600)).toBe('1小时0分')
    expect(formatDuration(3661)).toBe('1小时1分')
  })

  it('天档位显示 天+小时', () => {
    expect(formatDuration(86400)).toBe('1天0小时')
    expect(formatDuration(90061)).toBe('1天1小时')
  })
})

describe('formatTime', () => {
  it('空输入返回占位符', () => {
    expect(formatTime()).toBe('-')
    expect(formatTime('')).toBe('-')
  })

  it('Date 对象格式化为 yyyy-MM-dd HH:mm:ss（本地构造，避免时区差异）', () => {
    expect(formatTime(new Date(2026, 0, 21, 10, 30, 5))).toBe('2026-01-21 10:30:05')
    // 月/日/时/分/秒 均两位补齐
    expect(formatTime(new Date(2026, 10, 1, 9, 5, 3))).toBe('2026-11-01 09:05:03')
  })

  it('无法解析的字符串原样返回', () => {
    expect(formatTime('not-a-date')).toBe('not-a-date')
  })
})

describe('formatElapsed', () => {
  const START = '2026-01-01T00:00:00Z'
  const base = Date.parse(START)

  it('空或非法起始时间返回占位符', () => {
    expect(formatElapsed()).toBe('-')
    expect(formatElapsed('bad-time')).toBe('-')
  })

  it('不足 1 小时显示 mm:ss', () => {
    expect(formatElapsed(START, base + 5000)).toBe('0:05')
    expect(formatElapsed(START, base + 60000)).toBe('1:00')
  })

  it('超过 1 小时显示 h:mm:ss', () => {
    expect(formatElapsed(START, base + 3661000)).toBe('1:01:01')
    expect(formatElapsed(START, base + 7200000)).toBe('2:00:00')
  })

  it('起始时间晚于当前时间（时钟回拨）按 0 秒处理', () => {
    expect(formatElapsed(START, base - 60000)).toBe('0:00')
  })
})

describe('rangeToFromISO', () => {
  const NOW = Date.parse('2026-11-21T12:00:00Z')

  it('各档位换算为 RFC3339 起点', () => {
    expect(rangeToFromISO('1h', NOW)).toBe('2026-11-21T11:00:00.000Z')
    expect(rangeToFromISO('6h', NOW)).toBe('2026-11-21T06:00:00.000Z')
    expect(rangeToFromISO('24h', NOW)).toBe('2026-11-20T12:00:00.000Z')
    expect(rangeToFromISO('7d', NOW)).toBe('2026-11-14T12:00:00.000Z')
  })

  it('空或未知范围返回空串（表示不限时间）', () => {
    expect(rangeToFromISO('', NOW)).toBe('')
    expect(rangeToFromISO('99h', NOW)).toBe('')
  })

  it('原型链键不得被当作有效档位（不得抛异常）', () => {
    expect(rangeToFromISO('constructor', NOW)).toBe('')
    expect(rangeToFromISO('toString', NOW)).toBe('')
    expect(rangeToFromISO('__proto__', NOW)).toBe('')
  })
})
