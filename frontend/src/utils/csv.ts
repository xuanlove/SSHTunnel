// CSV 导出防护：Excel/WPS 会把以 = + - @ 等开头的单元格当作公式执行，
// 网络可控字段（如 target/source）可能被注入恶意公式，需前置单引号中和。

export function csvSafe(v: string): string {
  return /^[=+\-@\t\r]/.test(v) ? `'${v}` : v
}

// 与后端一致的单元格编码：含逗号/引号/换行时用双引号包裹并转义内部引号
function csvCell(v: string): string {
  if (/[",\n\r]/.test(v)) return `"${v.replace(/"/g, '""')}"`
  return v
}

// 解析 CSV 文本为二维数组（支持双引号字段跨行、"" 转义）
function parseCSVRows(csv: string): string[][] {
  const rows: string[][] = []
  let row: string[] = []
  let cell = ''
  let inQuotes = false
  for (let i = 0; i < csv.length; i++) {
    const ch = csv[i]
    if (inQuotes) {
      if (ch === '"') {
        if (csv[i + 1] === '"') {
          cell += '"'
          i++
        } else {
          inQuotes = false
        }
      } else {
        cell += ch
      }
    } else if (ch === '"') {
      // 仅字段起始处的引号作为包裹定界符；字段中间出现的引号按字面量保留
      if (cell === '') inQuotes = true
      else cell += ch
    } else if (ch === ',') {
      row.push(cell)
      cell = ''
    } else if (ch === '\n') {
      row.push(cell)
      rows.push(row)
      row = []
      cell = ''
    } else if (ch !== '\r') {
      cell += ch
    }
  }
  if (cell !== '' || row.length > 0) {
    row.push(cell)
    rows.push(row)
  }
  return rows
}

// 对整段 CSV 文本逐单元格做公式注入防护（表头行不动，结尾换行保持原样）
export function sanitizeCSV(csv: string): string {
  if (!csv) return csv
  const trailingNL = csv.endsWith('\n')
  const rows = parseCSVRows(csv)
  const out = rows
    .map((row, idx) => (idx === 0 ? row : row.map(csvSafe)).map(csvCell).join(','))
    .join('\n')
  return trailingNL ? `${out}\n` : out
}
