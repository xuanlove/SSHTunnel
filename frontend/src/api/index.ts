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
  const res = await fetch(path, { headers: authHeaders() })
  if (res.status === 401) {
    setToken(null)
    if (isWeb) location.href = '/login'
    throw new Error('未授权')
  }
  const json = await res.json()
  if (json.code !== 0) throw new Error(json.message)
  return json.data as T
}

async function httpPost<T>(path: string, body?: any): Promise<T> {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: body ? JSON.stringify(body) : undefined,
  })
  if (res.status === 401) {
    setToken(null)
    if (isWeb) location.href = '/login'
    throw new Error('未授权')
  }
  const json = await res.json()
  if (json.code !== 0) throw new Error(json.message)
  return json.data as T
}

async function httpPut<T>(path: string, body: any): Promise<T> {
  const res = await fetch(path, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify(body),
  })
  if (res.status === 401) {
    setToken(null)
    if (isWeb) location.href = '/login'
    throw new Error('未授权')
  }
  const json = await res.json()
  if (json.code !== 0) throw new Error(json.message)
  return json.data as T
}

async function httpDelete<T>(path: string): Promise<T> {
  const res = await fetch(path, {
    method: 'DELETE',
    headers: authHeaders(),
  })
  if (res.status === 401) {
    setToken(null)
    if (isWeb) location.href = '/login'
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
