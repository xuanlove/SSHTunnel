import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { copyText } from './clipboard'

// navigator.clipboard 在 happy-dom 下可能不存在或只读，用 defineProperty 覆盖
function setClipboard(impl: { writeText: (t: string) => Promise<void> } | undefined) {
  Object.defineProperty(window.navigator, 'clipboard', {
    value: impl,
    configurable: true,
  })
}

// happy-dom 未实现 execCommand，直接在 document 上挂 mock
let execMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  execMock = vi.fn(() => true)
  ;(document as any).execCommand = execMock
})

afterEach(() => {
  vi.restoreAllMocks()
  setClipboard(undefined)
  delete (document as any).execCommand
})

describe('copyText', () => {
  it('Clipboard API 可用时直接写入并返回 true', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    setClipboard({ writeText })
    await expect(copyText('hello')).resolves.toBe(true)
    expect(writeText).toHaveBeenCalledWith('hello')
    expect(execMock).not.toHaveBeenCalled()
  })

  it('Clipboard API 写入失败时降级 execCommand 并返回 true', async () => {
    setClipboard({ writeText: vi.fn().mockRejectedValue(new Error('denied')) })
    await expect(copyText('token-1')).resolves.toBe(true)
    expect(execMock).toHaveBeenCalledWith('copy')
  })

  it('无 Clipboard API 时走 execCommand 降级', async () => {
    setClipboard(undefined)
    await expect(copyText('token-2')).resolves.toBe(true)
    expect(execMock).toHaveBeenCalledWith('copy')
  })

  it('execCommand 也失败（返回 false）时返回 false', async () => {
    setClipboard(undefined)
    execMock.mockReturnValue(false)
    await expect(copyText('token-3')).resolves.toBe(false)
  })
})
