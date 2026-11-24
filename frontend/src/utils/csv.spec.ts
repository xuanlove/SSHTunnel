import { describe, expect, it } from 'vitest'
import { csvSafe, sanitizeCSV } from './csv'

describe('csvSafe', () => {
  it('以公式字符 = + - @ 开头的值前置单引号中和', () => {
    expect(csvSafe('=cmd|\' /C calc\'!A0')).toBe("'=cmd|' /C calc'!A0")
    expect(csvSafe('+1+1')).toBe("'+1+1")
    expect(csvSafe('-1+1')).toBe("'-1+1")
    expect(csvSafe('@SUM(A1)')).toBe("'@SUM(A1)")
  })

  it('以 Tab / 回车开头的值同样中和', () => {
    expect(csvSafe('\tcmd')).toBe("'\tcmd")
    expect(csvSafe('\rinfo')).toBe("'\rinfo")
  })

  it('普通值原样返回', () => {
    expect(csvSafe('')).toBe('')
    expect(csvSafe('example.com:443')).toBe('example.com:443')
    expect(csvSafe('1.2.3.4:5678')).toBe('1.2.3.4:5678')
    expect(csvSafe('2026-11-24 10:00:00')).toBe('2026-11-24 10:00:00')
    expect(csvSafe('成功')).toBe('成功')
  })
})

describe('sanitizeCSV', () => {
  const HEADER = 'time,tunnel,protocol,listen_port,source,target,result,duration_ms,denied'

  it('表头行不动，数据行中网络可控列的危险单元格被中和（含引号时重新编码）', () => {
    const csv = `${HEADER}\n2026-11-24 10:00:00,t1,https,443,1.2.3.4:5678,=HYPERLINK("http://evil"),success,12,false\n`
    expect(sanitizeCSV(csv)).toBe(
      `${HEADER}\n2026-11-24 10:00:00,t1,https,443,1.2.3.4:5678,"'=HYPERLINK(""http://evil"")",success,12,false\n`,
    )
  })

  it('普通数据保持不变，结尾换行保持原样', () => {
    const csv = 'time,target\n2026-11-24 10:00:00,example.com:443\n'
    expect(sanitizeCSV(csv)).toBe(csv)
    expect(sanitizeCSV('time,target\n1,2')).toBe('time,target\n1,2')
  })

  it('双引号字段（含逗号/引号转义）内的公式同样被中和且编码正确', () => {
    const csv = 'time,target\n2026-11-24 10:00:00,"=cmd,he said ""hi"""\n'
    expect(sanitizeCSV(csv)).toBe('time,target\n2026-11-24 10:00:00,"\'=cmd,he said ""hi"""\n')
  })

  it('双引号字段跨行时保持字段内容完整并正确中和', () => {
    // 危险字符在单元格中间不被处理；跨行单元格以公式开头时中和且保留换行
    const csv = 'a,b\n"x\n=1","=2\ny"\n'
    expect(sanitizeCSV(csv)).toBe('a,b\n"x\n=1","\'=2\ny"\n')
  })

  it('空文本原样返回', () => {
    expect(sanitizeCSV('')).toBe('')
  })
})
