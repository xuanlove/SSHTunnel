import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import HopEditor from './HopEditor.vue'

function mountEditor() {
  return mount(HopEditor, {
    props: { modelValue: [] },
    global: { plugins: [ElementPlus] },
  })
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('HopEditor', () => {
  it('点击「+ 添加跳板」追加一跳并发出 update', async () => {
    const w = mountEditor()
    const addBtn = w.findAll('button').find(b => b.text().includes('添加跳板'))
    expect(addBtn).toBeTruthy()
    await addBtn!.trigger('click')
    const emitted = w.emitted('update:modelValue')
    expect(emitted).toHaveLength(1)
    expect(emitted![0][0]).toEqual([{ user: '', host: '', port: 22, auth_type: 'password' }])
  })

  it('提供最小权限账户命令示例，覆盖建号/公钥/转发限制', async () => {
    const w = mountEditor()
    expect(w.text()).toContain('如何创建最小权限 SSH 账户')
    const code = w.find('.hint-code').text()
    expect(code).toContain('useradd --system')
    expect(code).toContain("$(command -v nologin)")
    expect(code).toContain('authorized_keys')
    expect(code).toContain('Match User sshtunnel')
    expect(code).toContain('AllowTcpForwarding yes')
    expect(code).toContain('PermitTTY no')
  })

  it('点击「复制命令」将示例写入剪贴板', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const w = mountEditor()
    await w.find('[data-testid="ssh-account-copy"]').trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledTimes(1)
    const copied = writeText.mock.calls[0][0] as string
    expect(copied).toContain('sudo useradd --system')
    expect(copied).toContain('Match User sshtunnel')
  })
})
