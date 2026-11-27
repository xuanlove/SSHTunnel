import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// api/index.ts 的 isWeb 在模块加载时求值，且 authToken 会从 localStorage 初始化。
// 因此每个用例需清理环境后 resetModules + 动态导入，才能分别覆盖 web/desktop 分支。
async function loadApi() {
  vi.resetModules()
  return await import('@/api')
}

type Body = Record<string, any>

// 构造仅包含 status/json 的最小 Response 形状（httpGet 等封装只访问这两个成员）
function jsonRes(body: Body, status = 200) {
  return { status, json: async () => body }
}

// 挂载全局 fetch mock（body 序列化已由调用方完成，此处仅响应）
function stubFetch(impl: (input: any, init?: RequestInit) => any) {
  const fn = vi.fn(impl)
  vi.stubGlobal('fetch', fn)
  return fn
}

beforeEach(() => {
  delete (window as any).go
  localStorage.clear()
  location.hash = ''
})

afterEach(() => {
  vi.unstubAllGlobals()
  delete (window as any).go
  delete (window as any).runtime
  localStorage.clear()
})

describe('api · web 模式（无 window.go，走 HTTP）', () => {
  it('getNodeStatus 携带 history_limit 并解析 data', async () => {
    const api = await loadApi()
    const fetchMock = stubFetch(() => jsonRes({ code: 0, data: { tunnel_total: 3 } }))
    const st = await api.getNodeStatus(50)
    expect(st).toEqual({ tunnel_total: 3 })
    expect(fetchMock.mock.calls[0][0]).toBe('/api/node/status?history_limit=50')
  })

  it('业务错误（code != 0）抛出后端 message', async () => {
    const api = await loadApi()
    stubFetch(() => jsonRes({ code: 1, message: '端口被占用' }))
    await expect(api.getNodeStatus()).rejects.toThrow('端口被占用')
  })

  it('IP 证书设置通过 /api/settings/ipcert 读写，签发走 POST', async () => {
    const api = await loadApi()
    const fetchMock = stubFetch((input: any, init?: RequestInit) => {
      if (init?.method === 'PUT') return jsonRes({ code: 0, data: { settings: { enabled: true }, status: {} } })
      if (init?.method === 'POST') return jsonRes({ code: 0, data: { issuing: true } })
      return jsonRes({ code: 0, data: { settings: { enabled: false }, status: {} } })
    })

    await api.getIPCertSettings()
    expect(fetchMock.mock.calls[0][0]).toBe('/api/settings/ipcert')

    await api.saveIPCertSettings({ enabled: true })
    expect(fetchMock.mock.calls[1][0]).toBe('/api/settings/ipcert')
    expect(fetchMock.mock.calls[1][1]!.method).toBe('PUT')

    await api.issueIPCertNow()
    expect(fetchMock.mock.calls[2][0]).toBe('/api/settings/ipcert/issue')
    expect(fetchMock.mock.calls[2][1]!.method).toBe('POST')
  })

  it('登录成功返回 token，setToken 后写入 localStorage 与后续请求头', async () => {
    const api = await loadApi()
    const fetchMock = stubFetch((_input, init) => {
      const url = String(_input)
      if (url === '/api/login') return jsonRes({ code: 0, data: { token: 'tk-1' } })
      expect((init?.headers as Record<string, string>).Authorization).toBe('Bearer tk-1')
      return jsonRes({ code: 0, data: [] })
    })
    // login 只返回 token，持久化由调用方 setToken 完成（与 Login.vue 行为一致）
    expect(await api.login('admin', 'pass')).toBe('tk-1')
    expect(localStorage.getItem('sshtunnel_token')).toBeNull()
    api.setToken('tk-1')
    expect(localStorage.getItem('sshtunnel_token')).toBe('tk-1')
    await api.listConfigs()
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('登录失败抛出 message', async () => {
    const api = await loadApi()
    stubFetch(() => jsonRes({ code: 1, message: '用户名或密码错误' }))
    await expect(api.login('admin', 'bad')).rejects.toThrow('用户名或密码错误')
  })

  it('searchAccessHistory 组装查询参数', async () => {
    const api = await loadApi()
    const fetchMock = stubFetch(() => jsonRes({ code: 0, data: [] }))
    await api.searchAccessHistory({ tunnel_id: 't1', from: 'F', to: 'T', result: 'denied', limit: 100, offset: 10 })
    const url = String(fetchMock.mock.calls[0][0])
    expect(url).toBe('/api/history/search?tunnel_id=t1&from=F&to=T&result=denied&limit=100&offset=10')
  })

  it('searchAccessHistory 省略可选参数，limit 默认 500', async () => {
    const api = await loadApi()
    const fetchMock = stubFetch(() => jsonRes({ code: 0, data: [] }))
    await api.searchAccessHistory({})
    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/history/search?limit=500')
  })

  it('exportAccessHistoryCSV 强制 limit=50000', async () => {
    const api = await loadApi()
    const fetchMock = stubFetch(() => jsonRes({ code: 0, data: 'a,b\n1,2' }))
    const csv = await api.exportAccessHistoryCSV({ tunnel_id: 't1' })
    expect(csv).toBe('a,b\n1,2')
    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/history/export?tunnel_id=t1&limit=50000')
  })

  it('401 时清除 token、跳转登录页并抛错', async () => {
    const api = await loadApi()
    api.setToken('stale')
    stubFetch(() => jsonRes({}, 401))
    await expect(api.listConfigs()).rejects.toThrow('未授权')
    expect(api.getToken()).toBeNull()
    expect(localStorage.getItem('sshtunnel_token')).toBeNull()
    expect(location.hash).toBe('#/login')
  })

  it('迟到的 401 不得清除刚登录获得的新 token', async () => {
    const api = await loadApi()
    // 发出请求时未带 token；fetch 挂起，模拟期间用户完成登录
    let resolveFetch!: (v: any) => void
    stubFetch(() => new Promise(r => { resolveFetch = r }))
    const pending = api.listConfigs().catch(e => e)
    await vi.waitFor(() => expect(resolveFetch).toBeTruthy())
    api.setToken('fresh-token')
    resolveFetch(jsonRes({}, 401))
    const err = await pending
    expect((err as Error).message).toBe('未授权')
    // 新 token 保留、不跳转
    expect(api.getToken()).toBe('fresh-token')
    expect(location.hash).toBe('')
  })

  it('resetAuthRedirect 后可再次触发 401 跳转', async () => {
    const api = await loadApi()
    stubFetch(() => jsonRes({}, 401))
    await api.getAlertRules().catch(() => {})
    expect(location.hash).toBe('#/login')
    // 幂等：未重置前不再重复处理
    location.hash = '#/status'
    await api.getAlertRules().catch(() => {})
    expect(location.hash).toBe('#/status')
    // 重置后恢复跳转
    api.resetAuthRedirect()
    await api.getAlertRules().catch(() => {})
    expect(location.hash).toBe('#/login')
  })

  it('checkAuthStatus 请求公开鉴权状态端点', async () => {
    const api = await loadApi()
    const fetchMock = stubFetch(() => jsonRes({ code: 0, data: { auth_enabled: true, tls_enabled: false } }))
    expect(await api.checkAuthStatus()).toEqual({ auth_enabled: true, tls_enabled: false })
    expect(fetchMock.mock.calls[0][0]).toBe('/api/auth/status')
  })

  it('saveConfig 新建走 POST、编辑走 PUT', async () => {
    const api = await loadApi()
    const fetchMock = stubFetch(() => jsonRes({ code: 0, data: { id: 't1' } }))
    await api.saveConfig({ id: '', name: 'demo' } as any)
    await api.saveConfig({ id: 't1', name: 'demo' } as any)
    const [createPath, createInit] = fetchMock.mock.calls[0]
    expect(createPath).toBe('/api/configs')
    expect(createInit!.method).toBe('POST')
    expect(JSON.parse(String(createInit!.body))).toEqual({ id: '', name: 'demo' })
    const [updatePath, updateInit] = fetchMock.mock.calls[1]
    expect(updatePath).toBe('/api/configs/t1')
    expect(updateInit!.method).toBe('PUT')
  })

  it('httpPost 遇到 401 同样清除 token 并跳登录', async () => {
    const api = await loadApi()
    api.setToken('stale')
    stubFetch(() => jsonRes({}, 401))
    await expect(api.startTunnel('t1')).rejects.toThrow('未授权')
    expect(api.getToken()).toBeNull()
    expect(location.hash).toBe('#/login')
  })

  it('httpDelete 成功调用删除端点', async () => {
    const api = await loadApi()
    const fetchMock = stubFetch(() => jsonRes({ code: 0, data: null }))
    await api.deleteConfig('t1')
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/configs/t1')
    expect(init!.method).toBe('DELETE')
  })

  it('updateWebAuthSettings 开启后自动保存返回的 token', async () => {
    const api = await loadApi()
    stubFetch(() => jsonRes({ code: 0, data: { enabled: true, username: 'u', token: 'tk-9' } }))
    const st = await api.updateWebAuthSettings({ enabled: true, username: 'u', password: 'p' })
    expect(st).toEqual({ enabled: true, username: 'u' })
    expect(api.getToken()).toBe('tk-9')
    expect(localStorage.getItem('sshtunnel_token')).toBe('tk-9')
  })

  it('updateWebAuthSettings 关闭后清除 token', async () => {
    const api = await loadApi()
    api.setToken('tk-old')
    stubFetch(() => jsonRes({ code: 0, data: { enabled: false, username: '' } }))
    await api.updateWebAuthSettings({ enabled: false, username: '', password: '' })
    expect(api.getToken()).toBeNull()
    expect(localStorage.getItem('sshtunnel_token')).toBeNull()
  })

  it('geoIPAvailable 请求失败时降级为 false', async () => {
    const api = await loadApi()
    stubFetch(() => { throw new Error('network down') })
    expect(await api.geoIPAvailable()).toBe(false)
  })

  it('getWebAuthSettings 在 web 模式走 HTTP', async () => {
    const api = await loadApi()
    stubFetch(() => jsonRes({ code: 0, data: { enabled: true, username: 'u' } }))
    const st = await api.getWebAuthSettings()
    expect(st.enabled).toBe(true)
  })

  it('getWebTLSSettings 默认不回显私钥，reveal=true 才携带 reveal_key=1', async () => {
    const api = await loadApi()
    const fetchMock = stubFetch(() => jsonRes({ code: 0, data: { active: false } }))
    await api.getWebTLSSettings()
    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/web/tls')
    await api.getWebTLSSettings(true)
    expect(String(fetchMock.mock.calls[1][0])).toBe('/api/web/tls?reveal_key=1')
  })

  it('面板 HTTPS 可提交 use_ipcert（使用 IP 证书），后端回显', async () => {
    const api = await loadApi()
    const fetchMock = stubFetch(() => jsonRes({ code: 0, data: { enabled: true, use_ipcert: true, active: true } }))
    await api.updateWebTLSSettings({ enabled: true, use_ipcert: true })
    const [, init] = fetchMock.mock.calls[0]
    expect(init!.method).toBe('PUT')
    expect(JSON.parse(String(init!.body))).toEqual({ enabled: true, use_ipcert: true })
    // 未开启时显式提交 use_ipcert: false，确保从 IP 证书模式切回 PEM 模式
    await api.updateWebTLSSettings({ enabled: true, use_ipcert: false, cert_pem: 'X' })
    expect(JSON.parse(String(fetchMock.mock.calls[1][1]!.body))).toEqual({ enabled: true, use_ipcert: false, cert_pem: 'X' })
  })
})

describe('api · desktop 模式（window.go 注入，走 Wails 绑定）', () => {
  function stubGoApp(methods: Record<string, any>) {
    (window as any).go = { main: { App: methods } }
  }

  it('listConfigs 走 window.go 绑定', async () => {
    const ListConfigs = vi.fn().mockResolvedValue([{ id: 't1', name: 'demo' }])
    stubGoApp({ ListConfigs })
    const api = await loadApi()
    const cfgs = await api.listConfigs()
    expect(cfgs).toEqual([{ id: 't1', name: 'demo' }])
    expect(ListConfigs).toHaveBeenCalledTimes(1)
  })

  it('getVersion 桌面端返回 { version } 包装', async () => {
    stubGoApp({ GetVersion: vi.fn().mockResolvedValue('v1.8.0') })
    const api = await loadApi()
    expect(await api.getVersion()).toEqual({ version: 'v1.8.0' })
  })

  it('searchAccessHistory 直传查询对象给绑定方法', async () => {
    const SearchAccessHistory = vi.fn().mockResolvedValue([])
    stubGoApp({ SearchAccessHistory })
    const api = await loadApi()
    await api.searchAccessHistory({ tunnel_id: 't1', result: 'denied' })
    expect(SearchAccessHistory).toHaveBeenCalledWith({ tunnel_id: 't1', result: 'denied' })
  })

  it('IP 证书设置走桌面端绑定', async () => {
    const state = { settings: { enabled: true }, status: { not_after: '2026-12-01T00:00:00Z' } }
    const GetIPCERTSettings = vi.fn().mockResolvedValue(state)
    const SaveIPCERTSettings = vi.fn().mockResolvedValue(state)
    const IssueIPCERTNow = vi.fn().mockResolvedValue(state.status)
    stubGoApp({ GetIPCERTSettings, SaveIPCERTSettings, IssueIPCERTNow })
    const api = await loadApi()

    expect(await api.getIPCertSettings()).toEqual(state)
    expect(await api.saveIPCertSettings({ enabled: true })).toEqual(state)
    expect(SaveIPCERTSettings).toHaveBeenCalledWith({ enabled: true })
    expect(await api.issueIPCertNow()).toEqual(state.status)
  })

  it('onLogNew 桌面端经 EventsOnMultiple 订阅 log:new', async () => {
    stubGoApp({})
    let registered: ((data: any) => void) | null = null
    ;(window as any).runtime = {
      EventsOnMultiple: vi.fn((name: string, cb: (d: any) => void) => {
        if (name === 'log:new') registered = cb
        return () => {}
      }),
    }
    const api = await loadApi()
    const cb = vi.fn()
    api.onLogNew(cb)
    expect(registered).toBeTruthy()
    registered!({ level: 'info', message: 'hi' })
    expect(cb).toHaveBeenCalledWith({ level: 'info', message: 'hi' })
  })

  it('testAlertWebhook 桌面端直接返回 true', async () => {
    const TestAlertWebhook = vi.fn().mockResolvedValue(undefined)
    stubGoApp({ TestAlertWebhook })
    const api = await loadApi()
    expect(await api.testAlertWebhook()).toBe(true)
    expect(TestAlertWebhook).toHaveBeenCalledTimes(1)
  })

  it('checkAuthStatus 桌面端直接返回关闭状态且不发请求', async () => {
    stubGoApp({})
    const fetchMock = stubFetch(() => jsonRes({ code: 0, data: {} }))
    const api = await loadApi()
    expect(await api.checkAuthStatus()).toEqual({ auth_enabled: false, tls_enabled: false })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('onTunnelStatus 桌面端经 EventsOnMultiple 订阅 tunnel:status', async () => {
    stubGoApp({})
    let registered: ((data: any) => void) | null = null
    ;(window as any).runtime = {
      EventsOnMultiple: vi.fn((name: string, cb: (d: any) => void) => {
        if (name === 'tunnel:status') registered = cb
        return () => {}
      }),
    }
    const api = await loadApi()
    const cb = vi.fn()
    api.onTunnelStatus(cb)
    expect(registered).toBeTruthy()
    registered!({ tunnel_id: 't1', status: 'running' })
    expect(cb).toHaveBeenCalledWith({ tunnel_id: 't1', status: 'running' })
  })

  it('仅 WEB 模式的接口在 desktop 模式抛错', async () => {
    stubGoApp({})
    const api = await loadApi()
    await expect(api.getWebTLSSettings()).rejects.toThrow('仅 WEB 模式支持面板 HTTPS 设置')
    await expect(api.regenerateDeployToken()).rejects.toThrow('仅 WEB 模式支持部署令牌')
  })
})
