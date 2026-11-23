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
  delete (navigator as any).clipboard
  delete (document as any).execCommand
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
    expect(code).toContain('PubkeyAuthentication yes')
    expect(code).toContain('PasswordAuthentication no')
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

  it('用户名默认跟随第 1 跳用户名，手动编辑后不再覆盖', async () => {
    const hop = (user: string) => [{ user, host: '10.0.0.1', port: 22, auth_type: 'password' }]
    const w = mount(HopEditor, {
      props: { modelValue: hop('alice') },
      global: { plugins: [ElementPlus] },
    })
    const userInput = () => w.find('input[data-testid="ssh-account-user"]').element as HTMLInputElement
    expect(userInput().value).toBe('alice')

    // 未手动编辑：随第 1 跳变化
    await w.setProps({ modelValue: hop('bob') })
    expect(userInput().value).toBe('bob')

    // 手动编辑后：不再跟随
    await w.find('input[data-testid="ssh-account-user"]').setValue('carol')
    await w.setProps({ modelValue: hop('dave') })
    expect(userInput().value).toBe('carol')
  })

  it('公钥未填写时生成占位配置并给出警告标记', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const w = mountEditor()
    await w.find('[data-testid="ssh-account-generate"]').trigger('click')
    await flushPromises()
    expect(w.find('.hint-code').text()).toContain('ssh-ed25519 AAAA... your-key')
    expect(w.find('.hint-warn').exists()).toBe(true)
    expect(document.body.textContent).toContain('公钥为占位符')
  })

  it('编辑输入后清除「已生成」标记', async () => {
    const w = mountEditor()
    await w.find('input[data-testid="ssh-account-pubkey"]').setValue('ssh-ed25519 AAAAC3Nz x')
    await w.find('[data-testid="ssh-account-generate"]').trigger('click')
    await flushPromises()
    expect(w.find('.hint-generated').exists()).toBe(true)

    await w.find('input[data-testid="ssh-account-user"]').setValue('someone')
    await flushPromises()
    expect(w.find('.hint-generated').exists()).toBe(false)
  })

  it('Clipboard API 不可用时降级 execCommand 复制', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    const exec = vi.fn().mockReturnValue(true)
    ;(document as any).execCommand = exec
    const w = mountEditor()
    await w.find('[data-testid="ssh-account-copy"]').trigger('click')
    await flushPromises()
    expect(exec).toHaveBeenCalledWith('copy')
    expect(document.body.textContent).toContain('账户创建命令已复制')
  })

  it('一键生成配置按用户名与公钥替换占位符并自动复制', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const w = mountEditor()

    await w.find('input[data-testid="ssh-account-user"]').setValue('tunnelx')
    await w.find('input[data-testid="ssh-account-pubkey"]').setValue('ssh-ed25519 AAAAC3Nz test@host')
    await w.find('[data-testid="ssh-account-generate"]').trigger('click')
    await flushPromises()

    const code = w.find('.hint-code').text()
    expect(code).toContain('sudo useradd --system --user-group --create-home --shell "$(command -v nologin)" tunnelx')
    expect(code).toContain('/home/tunnelx/.ssh')
    expect(code).toContain("echo 'ssh-ed25519 AAAAC3Nz test@host'")
    expect(code).toContain('Match User tunnelx')
    expect(code).not.toContain('sshtunnel')
    expect(writeText).toHaveBeenCalledWith(expect.stringContaining('Match User tunnelx'))
    expect(w.text()).toContain('已按当前输入生成')
  })

  it('切换密码认证后生成密码模式配置并复制', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const w = mountEditor()
    await w.find('input[data-testid="ssh-account-user"]').setValue('tunnelpw')
    await w.find('input[value="password"]').setValue()

    // 公钥输入隐藏，预览立即切换为密码模式
    expect(w.find('input[data-testid="ssh-account-pubkey"]').exists()).toBe(false)
    expect(w.find('.hint-code').text()).toContain('sudo passwd tunnelpw')

    await w.find('[data-testid="ssh-account-generate"]').trigger('click')
    await flushPromises()

    const code = w.find('.hint-code').text()
    expect(code).toContain('sudo passwd tunnelpw')
    expect(code).toContain('PasswordAuthentication yes')
    expect(code).toContain('PubkeyAuthentication no')
    expect(code).not.toContain('authorized_keys')
    expect(code).not.toContain('公钥')
    expect(writeText).toHaveBeenCalledWith(expect.stringContaining('sudo passwd tunnelpw'))
    // 密码模式不产生「公钥占位符」警告
    expect(w.find('.hint-warn').exists()).toBe(false)
  })

  it('非法用户名不生成配置并给出提示', async () => {
    const w = mountEditor()
    await w.find('input[data-testid="ssh-account-user"]').setValue('Bad User')
    await w.find('[data-testid="ssh-account-generate"]').trigger('click')
    await flushPromises()
    // 仍为默认示例
    expect(w.find('.hint-code').text()).toContain('Match User sshtunnel')
    expect(document.body.textContent).toContain('用户名只能包含')
  })

  it('非法公钥不生成配置并给出提示', async () => {
    const w = mountEditor()
    await w.find('input[data-testid="ssh-account-pubkey"]').setValue('not-a-key')
    await w.find('[data-testid="ssh-account-generate"]').trigger('click')
    await flushPromises()
    expect(w.find('.hint-code').text()).toContain('ssh-ed25519 AAAA... your-key')
    expect(document.body.textContent).toContain('公钥格式不正确')
  })
})
