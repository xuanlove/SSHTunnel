// 通用展示格式化：字节数、运行时长、本地时间与已持续时长

// 字节数：B 不显示小数，其余按数值大小保留 0/1 位小数
export function formatBytes(n?: number): string {
  const v = n || 0
  if (v <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let val = v
  let i = 0
  while (val >= 1024 && i < units.length - 1) {
    val /= 1024
    i++
  }
  const digits = i === 0 ? 0 : (val >= 100 ? 0 : 1)
  return `${val.toFixed(digits)} ${units[i]}`
}

// 运行时长：2天3小时 / 3小时12分 / 12分30秒
export function formatDuration(seconds?: number): string {
  const s = Math.max(0, Math.floor(seconds || 0))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (d > 0) return `${d}天${h}小时`
  if (h > 0) return `${h}小时${m}分`
  if (m > 0) return `${m}分${sec}秒`
  return `${sec}秒`
}

// 本地时间 yyyy-MM-dd HH:mm:ss
export function formatTime(input?: string | Date): string {
  if (!input) return '-'
  const d = input instanceof Date ? input : new Date(input)
  if (Number.isNaN(d.getTime())) return String(input)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
    `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// 已持续时长：mm:ss 或 h:mm:ss（用于活动连接）
export function formatElapsed(startedAt?: string, nowMs = Date.now()): string {
  if (!startedAt) return '-'
  const start = new Date(startedAt).getTime()
  if (Number.isNaN(start)) return '-'
  const s = Math.max(0, Math.floor((nowMs - start) / 1000))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  const p = (n: number) => String(n).padStart(2, '0')
  return h > 0 ? `${h}:${p(m)}:${p(sec)}` : `${m}:${p(sec)}`
}
