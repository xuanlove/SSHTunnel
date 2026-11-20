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

  it('仅 WEB 模式的接口在 desktop 模式抛错（反向：此处 isWeb 时正常走 HTTP）', async () => {
    const api = await loadApi()
    stubFetch(() => jsonRes({ code: 0, data: { enabled: true, username: 'u' } }))
    const st = await api.getWebAuthSettings()
    expect(st.enabled).toBe(true)
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
})
