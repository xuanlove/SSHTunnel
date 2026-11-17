import { Call, Events } from '@wailsio/runtime'
import type {
  TunnelConfig,
  HopConfig,
  LogEntry,
} from '@/types'

// ===== 运行环境检测 =====
// 桌面端（Wails WebView）会注入 window.go；浏览器环境无此对象
const isWeb = typeof window !== 'undefined' && !(window as any).go

// ===== Token 管理（仅 WEB 模式使用）=====
const TOKEN_KEY = 'sshtunnel_token'
let authToken: string | null = isWeb ? localStorage.getItem(TOKEN_KEY) : null

export function setToken(token: string | null) {
  authToken = token
  if (isWeb) {
    if (token) localStorage.setItem(TOKEN_KEY, token)
    else localStorage.removeItem(TOKEN_KEY)
  }
}

export function getToken(): string | null {
  return authToken
}

function authHeaders(): Record<string, string> {
  return authToken ? { Authorization: `Bearer ${authToken}` } : {}
}

// ===== 401 统一处理 =====
// 说明：本项目使用 hash 路由（#/xxx），因此跳转必须写 hash，
// 否则会整页刷新到 /login（无 hash），随后又被路由守卫重定向，表现为页面来回跳转。
// 同时做幂等处理：并发/迟到的 401 响应不得清除刚登录得到的新 Token。
let redirectingToLogin = false

function handleUnauthorized(sentToken: string | null) {
  // 仅当当前 Token 仍是本次请求发送的那个（或本就没带）时才清除，
  // 避免迟到的 401 清掉用户刚登录获得的新 Token，从而反复被踢回登录页。
  if (!sentToken || authToken === sentToken) {
    setToken(null)
  }
  if (!isWeb || redirectingToLogin) return
  redirectingToLogin = true
  if (!location.hash.startsWith('#/login')) {
    location.hash = '#/login'
  }
}

// 登录成功后调用，允许后续再次触发 401 跳转
export function resetAuthRedirect() {
  redirectingToLogin = false
}

// ===== 鉴权状态 =====
export interface AuthStatus {
  auth_enabled: boolean
  tls_enabled: boolean
}

export async function checkAuthStatus(): Promise<AuthStatus> {
  if (!isWeb) return { auth_enabled: false, tls_enabled: false }
  const res = await fetch('/api/auth/status')
  const json = await res.json()
  return json.data
}

export async function login(username: string, password: string): Promise<string> {
  const res = await fetch('/api/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  const json = await res.json()
  if (json.code !== 0) throw new Error(json.message || '登录失败')
  return json.data.token
}

// ===== 通用 HTTP 请求封装 =====
async function httpGet<T>(path: string): Promise<T> {
  const sent = authToken
  const res = await fetch(path, { headers: authHeaders() })
  if (res.status === 401) {
    handleUnauthorized(sent)
    throw new Error('未授权')
  }
  const json = await res.json()
  if (json.code !== 0) throw new Error(json.message)
  return json.data as T
}

async function httpPost<T>(path: string, body?: any): Promise<T> {
  const sent = authToken
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: body ? JSON.stringify(body) : undefined,
  })
  if (res.status === 401) {
    handleUnauthorized(sent)
    throw new Error('未授权')
  }
  const json = await res.json()
  if (json.code !== 0) throw new Error(json.message)
  return json.data as T
}

async function httpPut<T>(path: string, body: any): Promise<T> {
  const sent = authToken
  const res = await fetch(path, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify(body),
  })
  if (res.status === 401) {
    handleUnauthorized(sent)
    throw new Error('未授权')
  }
  const json = await res.json()
  if (json.code !== 0) throw new Error(json.message)
  return json.data as T
}

async function httpDelete<T>(path: string): Promise<T> {
  const sent = authToken
  const res = await fetch(path, {
    method: 'DELETE',
    headers: authHeaders(),
  })
  if (res.status === 401) {
    handleUnauthorized(sent)
    throw new Error('未授权')
  }
  const json = await res.json()
  if (json.code !== 0) throw new Error(json.message)
  return json.data as T
}

// ===== 配置相关 =====
export async function listConfigs(): Promise<TunnelConfig[]> {
  if (!isWeb) return Call.ByName('App.ListConfigs') as Promise<TunnelConfig[]>
  return httpGet<TunnelConfig[]>('/api/configs')
}

export async function saveConfig(cfg: TunnelConfig): Promise<TunnelConfig> {
  if (!isWeb) return Call.ByName('App.SaveConfig', cfg) as Promise<TunnelConfig>
  if (cfg.id) {
    return httpPut<TunnelConfig>(`/api/configs/${cfg.id}`, cfg)
  }
  return httpPost<TunnelConfig>('/api/configs', cfg)
}

export async function deleteConfig(id: string): Promise<void> {
  if (!isWeb) {
    await Call.ByName('App.DeleteConfig', id)
    return
  }
  await httpDelete(`/api/configs/${id}`)
}

// ===== 隧道控制 =====
export async function startTunnel(id: string): Promise<void> {
  if (!isWeb) {
    await Call.ByName('App.StartTunnel', id)
    return
  }
  await httpPost(`/api/configs/${id}/start`)
}

export async function stopTunnel(id: string): Promise<void> {
  if (!isWeb) {
    await Call.ByName('App.StopTunnel', id)
    return
  }
  await httpPost(`/api/configs/${id}/stop`)
}

export async function startAll(): Promise<void> {
  if (!isWeb) {
    await Call.ByName('App.StartAll')
    return
  }
  await httpPost('/api/tunnels/start-all')
}

export async function stopAll(): Promise<void> {
  if (!isWeb) {
    await Call.ByName('App.StopAll')
    return
  }
  await httpPost('/api/tunnels/stop-all')
}

// ===== 系统服务 =====
export async function checkPort(port: number): Promise<boolean> {
  if (!isWeb) return Call.ByName('App.CheckPort', port) as Promise<boolean>
  const result = await httpPost<{ available: boolean }>('/api/port/check', { port })
  return result.available
}

export async function getRecentLogs(limit: number): Promise<LogEntry[]> {
  if (!isWeb) return Call.ByName('App.GetRecentLogs', limit) as Promise<LogEntry[]>
  return httpGet<LogEntry[]>(`/api/logs?limit=${limit}`)
}

export async function generateSelfSignedCert(): Promise<{ certPath: string; keyPath: string }> {
  if (!isWeb) {
    const result = await Call.ByName('App.GenerateSelfSignedCert')
    const r = result as any
    if (r && typeof r === 'object' && !Array.isArray(r)) {
      return {
        certPath: r.certPath || r[0] || r.cert_path || '',
        keyPath: r.keyPath || r[1] || r.key_path || '',
      }
    }
    if (Array.isArray(r)) {
      return { certPath: r[0] || '', keyPath: r[1] || '' }
    }
    return { certPath: String(r), keyPath: '' }
  }
  const result = await httpPost<{ cert_path: string; key_path: string }>('/api/cert/generate')
  return { certPath: result.cert_path, keyPath: result.key_path }
}

export async function parseHopChainString(s: string): Promise<HopConfig[]> {
  if (!isWeb) return Call.ByName('App.ParseHopChainString', s) as Promise<HopConfig[]>
  return httpPost<HopConfig[]>('/api/hopchain/parse', { chain: s })
}

export async function testTunnelConfig(cfg: TunnelConfig): Promise<void> {
  if (!isWeb) {
    await Call.ByName('App.TestTunnelConfig', cfg)
    return
  }
  await httpPost('/api/tunnel/test', cfg)
}

// ===== 代理监听端口连通性测试 =====
export interface ProxyProbeResult {
  ip: string
  raw: string
  elapsed_ms: number
  target: string
}

// 经指定代理监听端口访问目标（默认 https://api.ip.sb/ip），返回出口 IP
export async function testProxyListener(payload: {
  protocol: string
  port: number
  username?: string
  password?: string
}): Promise<ProxyProbeResult> {
  if (!isWeb) {
    return Call.ByName(
      'App.TestProxyListener',
      payload.protocol,
      payload.port,
      payload.username || '',
      payload.password || '',
    ) as Promise<ProxyProbeResult>
  }
  return httpPost<ProxyProbeResult>('/api/proxy/test', payload)
}

// GeoIP 数据库是否可用（用于代理防火墙的国家规则提示）
export async function geoIPAvailable(): Promise<boolean> {
  if (!isWeb) {
    return Call.ByName('App.GeoIPAvailable') as Promise<boolean>
  }
  try {
    const info = await httpGet<{ geoip_available?: boolean }>('/api/version')
    return !!info?.geoip_available
  } catch {
    return false
  }
}

// ===== GeoIP 城市级数据库 =====
export interface GeoIPStatus {
  loaded: boolean
  type: string // Country | City
  source: string // builtin 或文件路径
  size_bytes: number
  updated_at: string
  city_ready: boolean
  city_db_cache: string
  city_db_exists: boolean
  enabled: boolean
}

export async function getGeoIPStatus(): Promise<GeoIPStatus> {
  if (!isWeb) {
    return Call.ByName('App.GeoIPStatus') as Promise<GeoIPStatus>
  }
  return httpGet<GeoIPStatus>('/api/geoip/status')
}

// 下载 / 更新城市级数据库（约 49MB，服务端发起下载）；url 可选，用于指定自定义下载地址
export async function downloadGeoIPDatabase(url = ''): Promise<GeoIPStatus> {
  if (!isWeb) {
    return Call.ByName('App.DownloadGeoIPDatabase', url) as Promise<GeoIPStatus>
  }
  return httpPost<GeoIPStatus>('/api/geoip/download', { url })
}

// 启用 / 关闭城市级数据库（立即热加载，无需重启）
export async function setGeoIPEnabled(enabled: boolean): Promise<GeoIPStatus> {
  if (!isWeb) {
    return Call.ByName('App.SetGeoIPEnabled', enabled) as Promise<GeoIPStatus>
  }
  return httpPut<GeoIPStatus>('/api/geoip/settings', { enabled })
}

// ===== GeoIP 城市下拉选项 =====
export interface CityProvince {
  code: string // ISO 3166-2 省级后缀（可能为空）
  name: string // 省级英文名
  cities: string[] // 城市英文名列表
}

export interface CityCatalog {
  ready: boolean // 索引是否已就绪
  building: boolean // 是否正在构建索引
  country: string
  provinces: CityProvince[]
}

// 查询指定国家的省/城市列表；首次调用会在服务端后台扫描城市库
export async function getCityCatalog(country: string): Promise<CityCatalog> {
  if (!isWeb) {
    return Call.ByName('App.CityCatalog', country) as Promise<CityCatalog>
  }
  return httpGet<CityCatalog>(`/api/geoip/cities?country=${encodeURIComponent(country)}`)
}

// ===== WEB 访问密码设置（仅 WEB 模式）=====
export interface WebAuthSettings {
  enabled: boolean
  username: string
}

export async function getWebAuthSettings(): Promise<WebAuthSettings> {
  if (!isWeb) return { enabled: false, username: '' }
  return httpGet<WebAuthSettings>('/api/auth/settings')
}

// 开启/关闭访问密码。开启成功后后端返回 token，自动保存当前会话以免立即掉线。
export async function updateWebAuthSettings(payload: {
  enabled: boolean
  username: string
  password: string
}): Promise<WebAuthSettings> {
  if (!isWeb) throw new Error('仅 WEB 模式支持访问密码设置')
  const res = await httpPut<any>('/api/auth/settings', payload)
  if (res?.token) setToken(res.token)
  if (res && !res.enabled) setToken(null)
  return { enabled: !!res?.enabled, username: res?.username || '' }
}

// ===== 事件订阅 =====
export function onLogNew(callback: (entry: LogEntry) => void): () => void {
  if (!isWeb) {
    return Events.On('log:new', (ev: any) => {
      callback(ev.data as LogEntry)
    })
  }
  // WEB 模式：WebSocket
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const tokenParam = authToken ? `?token=${authToken}` : ''
  const ws = new WebSocket(`${proto}//${location.host}/api/logs/stream${tokenParam}`)
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data)
      if (msg.type === 'log') callback(msg.data as LogEntry)
    } catch (e) {
      // 忽略解析错误
    }
  }
  return () => ws.close()
}

export function onTunnelStatus(callback: (data: any) => void): () => void {
  if (!isWeb) {
    return Events.On('tunnel:status', (ev: any) => {
      callback(ev.data)
    })
  }
  // WEB 模式：复用日志流 WebSocket（status 消息也在同一通道）
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const tokenParam = authToken ? `?token=${authToken}` : ''
  const ws = new WebSocket(`${proto}//${location.host}/api/tunnels/status/stream${tokenParam}`)
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data)
      if (msg.type === 'status') callback(msg.data)
    } catch (e) {
      // 忽略解析错误
    }
  }
  return () => ws.close()
}

// 导出运行环境标识供路由守卫使用
export const isWebMode = isWeb
