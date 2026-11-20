import * as App from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import type {
  TunnelConfig,
  HopConfig,
  LogEntry,
  TunnelStats,
  NodeStatus,
  AccessRecord,
} from '@/types'

// ===== 运行环境检测 =====
// 桌面端（Wails WebView）会注入 window.go；浏览器环境无此对象
const isWeb = typeof window !== 'undefined' && !(window as any).go

// ===== 请求超时 =====
// 避免网络异常/服务无响应时请求永久挂起，导致页面 loading 无法结束
const REQUEST_TIMEOUT_MS = 15000
async function fetchWithTimeout(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const controller = new AbortController()
  const timer = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)
  try {
    return await fetch(input, { ...init, signal: controller.signal })
  } finally {
    window.clearTimeout(timer)
  }
}

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
  // 请求发出时未携带 Token，但此后用户已登录获得新 Token：
  // 这是一条迟到的 401，直接忽略，不能清掉刚登录的 Token 或把用户踢回登录页。
  if (!sentToken && authToken) {
    return
  }
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
  const res = await fetchWithTimeout('/api/auth/status')
  const json = await res.json()
  return json.data
}

export async function login(username: string, password: string): Promise<string> {
  const res = await fetchWithTimeout('/api/login', {
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
  const res = await fetchWithTimeout(path, { headers: authHeaders() })
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
  const res = await fetchWithTimeout(path, {
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
  const res = await fetchWithTimeout(path, {
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
  const res = await fetchWithTimeout(path, {
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
  if (!isWeb) return App.ListConfigs() as unknown as Promise<TunnelConfig[]>
  return httpGet<TunnelConfig[]>('/api/configs')
}

export async function saveConfig(cfg: TunnelConfig): Promise<TunnelConfig> {
  if (!isWeb) return App.SaveConfig(cfg as any) as unknown as Promise<TunnelConfig>
  if (cfg.id) {
    return httpPut<TunnelConfig>(`/api/configs/${cfg.id}`, cfg)
  }
  return httpPost<TunnelConfig>('/api/configs', cfg)
}

export async function deleteConfig(id: string): Promise<void> {
  if (!isWeb) {
    await App.DeleteConfig(id)
    return
  }
  await httpDelete(`/api/configs/${id}`)
}

// ===== 隧道控制 =====
export async function startTunnel(id: string): Promise<void> {
  if (!isWeb) {
    await App.StartTunnel(id)
    return
  }
  await httpPost(`/api/configs/${id}/start`)
}

export async function stopTunnel(id: string): Promise<void> {
  if (!isWeb) {
    await App.StopTunnel(id)
    return
  }
  await httpPost(`/api/configs/${id}/stop`)
}

export async function startAll(): Promise<void> {
  if (!isWeb) {
    await App.StartAll()
    return
  }
  await httpPost('/api/tunnels/start-all')
}

export async function stopAll(): Promise<void> {
  if (!isWeb) {
    await App.StopAll()
    return
  }
  await httpPost('/api/tunnels/stop-all')
}

// ===== 系统服务 =====
export async function checkPort(port: number): Promise<boolean> {
  if (!isWeb) return App.CheckPort(port)
  const result = await httpPost<{ available: boolean }>('/api/port/check', { port })
  return result.available
}

export async function getRecentLogs(limit: number): Promise<LogEntry[]> {
  if (!isWeb) return App.GetRecentLogs(limit) as unknown as Promise<LogEntry[]>
  return httpGet<LogEntry[]>(`/api/logs?limit=${limit}`)
}

// 获取所有运行中隧道的连接与流量统计（以隧道 ID 为键）
export async function getTunnelStats(): Promise<Record<string, TunnelStats>> {
  if (!isWeb) return App.GetTunnelStats() as unknown as Promise<Record<string, TunnelStats>>
  return httpGet<Record<string, TunnelStats>>('/api/tunnels/stats')
}

// 获取节点级运行状态：概览、活动连接、流量与访问历史
export async function getNodeStatus(historyLimit = 200): Promise<NodeStatus> {
  if (!isWeb) {
    return App.GetNodeStatus(historyLimit) as Promise<NodeStatus>
  }
  return httpGet<NodeStatus>(`/api/node/status?history_limit=${historyLimit}`)
}

export async function generateSelfSignedCert(): Promise<{ certPEM: string; keyPEM: string }> {
  if (!isWeb) {
    const result = await App.GenerateSelfSignedCert()
    const r = result as any
    if (Array.isArray(r)) return { certPEM: r[0] || '', keyPEM: r[1] || '' }
    if (r && typeof r === 'object') {
      return { certPEM: r.certPEM || r.cert_pem || '', keyPEM: r.keyPEM || r.key_pem || '' }
    }
    return { certPEM: '', keyPEM: '' }
  }
  const result = await httpPost<{ cert_pem: string; key_pem: string }>('/api/cert/generate')
  return { certPEM: result.cert_pem, keyPEM: result.key_pem }
}

// ===== 证书信息解析 =====
export interface CertInfo {
  subject_cn: string // 使用者公用名（CN）
  issuer_cn: string // 颁发者公用名
  not_before: string // 生效时间（RFC3339）
  not_after: string // 到期时间（RFC3339）
  dns_names?: string[]
  expired: boolean
  days_left: number
}

// 解析证书 PEM，返回 CN 与有效期（用于界面展示）
export async function inspectCertificate(certPEM: string): Promise<CertInfo> {
  if (!isWeb) return App.InspectCertificate(certPEM) as unknown as Promise<CertInfo>
  return httpPost<CertInfo>('/api/cert/inspect', { cert_pem: certPEM })
}

export async function parseHopChainString(s: string): Promise<HopConfig[]> {
  if (!isWeb) return App.ParseHopChainString(s) as unknown as Promise<HopConfig[]>
  return httpPost<HopConfig[]>('/api/hopchain/parse', { chain: s })
}

export async function testTunnelConfig(cfg: TunnelConfig): Promise<void> {
  if (!isWeb) {
    await App.TestTunnelConfig(cfg as any)
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
    return App.TestProxyListener(
      payload.protocol,
      payload.port,
      payload.username || '',
      payload.password || '',
    ) as unknown as Promise<ProxyProbeResult>
  }
  return httpPost<ProxyProbeResult>('/api/proxy/test', payload)
}

// GeoIP 数据库是否可用（用于代理防火墙的国家规则提示）
export async function geoIPAvailable(): Promise<boolean> {
  if (!isWeb) {
    return App.GeoIPAvailable()
  }
  try {
    const info = await httpGet<{ geoip_available?: boolean }>('/api/version')
    return !!info?.geoip_available
  } catch {
    return false
  }
}

// ===== GeoIP 城市级数据库 =====
export interface VersionInfo {
  version: string
  commit?: string
  variant?: string
  repo?: string
}

// 获取当前构建版本（设置页展示，跟随发布包）
export async function getVersion(): Promise<VersionInfo> {
  if (!isWeb) {
    return { version: await App.GetVersion() }
  }
  return httpGet<VersionInfo>('/api/version')
}

// 获取指定节点（隧道）最近的访问历史
export async function getNodeHistory(tunnelID: string, limit = 500): Promise<AccessRecord[]> {
  if (!isWeb) {
    return App.GetNodeHistory(tunnelID, limit) as unknown as Promise<AccessRecord[]>
  }
  return httpGet<AccessRecord[]>(`/api/node/history?tunnel_id=${encodeURIComponent(tunnelID)}&limit=${limit}`)
}

// ===== 趋势指标与告警 =====
export interface MetricPoint {
  time: string
  active_conns: number
  conn_delta: number
  sent_delta: number
  recv_delta: number
}

export interface AlertRule {
  id: string
  name: string
  type: 'tunnel_down' | 'conn_threshold' | 'traffic_threshold'
  enabled: boolean
  tunnel_id?: string
  threshold: number
  duration_seconds: number
  cooldown_seconds: number
  created_at?: string
}

export interface AlertWebhook {
  enabled: boolean
  url: string
  format: 'generic' | 'wecom' | 'feishu' | ''
}

export interface AlertHistory {
  time: string
  rule_id: string
  rule_name: string
  tunnel_id?: string
  tunnel_name?: string
  type: string
  value: number
  status: 'firing' | 'resolved'
  message: string
}

// 获取趋势数据（rangeKey: 1h/6h/24h；tunnelID 为空表示全局）
export async function getMetrics(rangeKey: string, tunnelID = '', limit = 120): Promise<MetricPoint[]> {
  if (!isWeb) {
    return App.GetMetrics(rangeKey, tunnelID) as unknown as Promise<MetricPoint[]>
  }
  const q = `range=${encodeURIComponent(rangeKey)}&tunnel_id=${encodeURIComponent(tunnelID)}&limit=${limit}`
  return httpGet<MetricPoint[]>(`/api/metrics?${q}`)
}

export async function getAlertRules(): Promise<AlertRule[]> {
  if (!isWeb) {
    return App.GetAlertRules() as unknown as Promise<AlertRule[]>
  }
  return httpGet<AlertRule[]>('/api/alerts/rules')
}

export async function saveAlertRule(rule: AlertRule): Promise<AlertRule> {
  if (!isWeb) {
    return App.SaveAlertRule(rule) as unknown as Promise<AlertRule>
  }
  return httpPost<AlertRule>('/api/alerts/rules', rule)
}

export async function deleteAlertRule(id: string): Promise<void> {
  if (!isWeb) {
    await App.DeleteAlertRule(id)
    return
  }
  await httpDelete<null>(`/api/alerts/rules?id=${encodeURIComponent(id)}`)
}

export async function getAlertHistory(limit = 100): Promise<AlertHistory[]> {
  if (!isWeb) {
    return App.GetAlertHistory(limit) as unknown as Promise<AlertHistory[]>
  }
  return httpGet<AlertHistory[]>(`/api/alerts/history?limit=${limit}`)
}

export async function getAlertWebhook(): Promise<AlertWebhook> {
  if (!isWeb) {
    return App.GetAlertWebhook() as unknown as Promise<AlertWebhook>
  }
  return httpGet<AlertWebhook>('/api/alerts/webhook')
}

export async function saveAlertWebhook(hook: AlertWebhook): Promise<AlertWebhook> {
  if (!isWeb) {
    return App.SaveAlertWebhook(hook) as unknown as Promise<AlertWebhook>
  }
  return httpPost<AlertWebhook>('/api/alerts/webhook', hook)
}

export async function testAlertWebhook(): Promise<boolean> {
  if (!isWeb) {
    await App.TestAlertWebhook()
    return true
  }
  return httpPost<boolean>('/api/alerts/test', {})
}

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
    return App.GeoIPStatus() as unknown as Promise<GeoIPStatus>
  }
  return httpGet<GeoIPStatus>('/api/geoip/status')
}

// 下载 / 更新城市级数据库（Merged-IP，约 93MB，服务端发起下载）；url 可选，用于指定自定义下载地址
export async function downloadGeoIPDatabase(url = ''): Promise<GeoIPStatus> {
  if (!isWeb) {
    return App.DownloadGeoIPDatabase(url) as unknown as Promise<GeoIPStatus>
  }
  return httpPost<GeoIPStatus>('/api/geoip/download', { url })
}

// 启用 / 关闭城市级数据库（立即热加载，无需重启）
export async function setGeoIPEnabled(enabled: boolean): Promise<GeoIPStatus> {
  if (!isWeb) {
    return App.SetGeoIPEnabled(enabled) as unknown as Promise<GeoIPStatus>
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
    return App.CityCatalog(country) as unknown as Promise<CityCatalog>
  }
  return httpGet<CityCatalog>(`/api/geoip/cities?country=${encodeURIComponent(country)}`)
}

// ===== WEB 面板 HTTPS（SSL）设置 =====
export interface WebTLSSettings {
  enabled: boolean
  cert_file: string
  key_file: string
  cert_pem?: string
  key_pem?: string
  active: boolean
  deploy_token: string
}

export async function getWebTLSSettings(): Promise<WebTLSSettings> {
  if (!isWeb) throw new Error('仅 WEB 模式支持面板 HTTPS 设置')
  return httpGet<WebTLSSettings>('/api/web/tls')
}

// 保存 HTTPS 设置；可直接提交证书/私钥 PEM 内容
export async function updateWebTLSSettings(payload: {
  enabled: boolean
  cert_pem?: string
  key_pem?: string
  cert_file?: string
  key_file?: string
}): Promise<WebTLSSettings> {
  if (!isWeb) throw new Error('仅 WEB 模式支持面板 HTTPS 设置')
  return httpPut<WebTLSSettings>('/api/web/tls', payload)
}

// 重新生成 certd 部署令牌（旧令牌立即失效）
export async function regenerateDeployToken(): Promise<string> {
  if (!isWeb) throw new Error('仅 WEB 模式支持部署令牌')
  const res = await httpPost<{ deploy_token: string }>('/api/web/tls/token', {})
  return res?.deploy_token || ''
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
// 建立带自动重连的 WebSocket：服务重启或网络抖动后自动恢复，无需刷新页面
function connectWS(path: string, onMessage: (msg: any) => void): () => void {
  let ws: WebSocket | null = null
  let closed = false
  let retry = 0
  let timer = 0

  const connect = () => {
    if (closed) return
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const tokenParam = authToken ? `?token=${encodeURIComponent(authToken)}` : ''
    ws = new WebSocket(`${proto}//${location.host}${path}${tokenParam}`)
    ws.onopen = () => { retry = 0 }
    ws.onmessage = (ev) => {
      try {
        onMessage(JSON.parse(ev.data))
      } catch {
        // 忽略解析错误
      }
    }
    ws.onclose = () => {
      ws = null
      if (closed) return
      // 指数退避重连：1s、2s、4s… 最长 30s
      const delay = Math.min(30000, 1000 * 2 ** retry)
      retry++
      timer = window.setTimeout(connect, delay)
    }
    ws.onerror = () => {
      ws?.close()
    }
  }

  connect()
  return () => {
    closed = true
    if (timer) window.clearTimeout(timer)
    ws?.close()
  }
}

export function onLogNew(callback: (entry: LogEntry) => void): () => void {
  if (!isWeb) {
    return EventsOn('log:new', (data: any) => {
      callback(data as LogEntry)
    })
  }
  return connectWS('/api/logs/stream', (msg) => {
    if (msg.type === 'log') callback(msg.data as LogEntry)
  })
}

export function onTunnelStatus(callback: (data: any) => void): () => void {
  if (!isWeb) {
    return EventsOn('tunnel:status', (data: any) => {
      callback(data)
    })
  }
  return connectWS('/api/tunnels/status/stream', (msg) => {
    if (msg.type === 'status') callback(msg.data)
  })
}

// 导出运行环境标识供路由守卫使用
export const isWebMode = isWeb
