// 剪贴板工具：优先 Clipboard API，HTTP 非安全上下文或权限受限时降级 execCommand

export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 继续走降级方案
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    try {
      ta.select()
      return document.execCommand('copy')
    } finally {
      ta.remove()
    }
  } catch {
    return false
  }
}
