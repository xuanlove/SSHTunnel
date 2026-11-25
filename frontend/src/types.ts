// 与 Go internal/config/config.go 对应的类型定义

export type TunnelType = 'local_forward' | 'proxy'

export type ProxyProtocol = 'http' | 'https' | 'socks4' | 'socks5'

export type TunnelStatus = 'stopped' | 'starting' | 'running' | 'error'

export interface HopConfig {
  user: string
  host: string
  port: number
  auth_type: string // password | key
  password?: string
  key_content?: string // 密钥文本内容（PEM 格式）
  passphrase?: string
  host_key?: string // 主机密钥指纹（SHA256:...，连接时必填）
}

export interface LocalForward {
  id: string
  local_port: number
  remote_host: string
  remote_port: number
  allow_external: boolean
}

export interface AuthConfig {
  username: string
  password: string
}

export interface TLSConfig {
  // 证书 PEM 文本（可含完整证书链）
  cert_pem?: string
  // 私钥 PEM 文本
  key_pem?: string
}

// 防火墙模式：blacklist=黑名单（默认，命中拒绝）；whitelist=白名单（仅命中放行）
export type FirewallMode = 'blacklist' | 'whitelist'

export interface FirewallConfig {
  enabled: boolean
  mode: FirewallMode
  // 自定义 IP / CIDR，支持 IPv4 与 IPv6（如 10.0.0.0/8、203.0.113.7、2001:db8::/32）
  ips?: string[]
  // GeoIP 国家代码（ISO 3166-1 alpha-2，如 CN、US），内置 Country 库即可生效
  countries?: string[]
  // 城市级规则「国家/省/城市」，各段支持 * 通配，如 CN/GD/Shenzhen、CN/*/Shenzhen
  // 需要启用城市级数据库（Merged-IP，系统设置中下载并开启）
  cities?: string[]
}

export interface ProxyListener {
  id: string
  protocol: ProxyProtocol
  listen_port: number
  allow_external: boolean
  auth?: AuthConfig | null
  tls?: TLSConfig | null
  firewall?: FirewallConfig | null
  // 是否记录代理访问日志（目标地址、耗时），默认关闭
  access_log?: boolean
}

export interface TunnelConfig {
  id: string
  name: string
  hop_chain: HopConfig[]
  tunnel_type: TunnelType
  local_forwards?: LocalForward[]
  proxy_listeners?: ProxyListener[]
  auto_reconnect: boolean
  // 程序/服务启动时自动启动该隧道（配合 systemd 服务实现开机自启）
  auto_start: boolean
  status: TunnelStatus
  // SSH 心跳间隔（秒），0 表示关闭
  keepalive_seconds?: number
  // 最大重连次数，0 表示无限
  max_reconnect_attempts?: number
  // 重连最大退避间隔（秒），0 表示默认 60
  reconnect_max_delay_seconds?: number
}

// ===== 运行统计（流量与连接）=====

export interface ListenerStats {
  id: string
  protocol: string
  listen_port: number
  active_conns: number
  total_conns: number
  bytes_sent: number
  bytes_recv: number
}

export interface ForwardStats {
  id: string
  local_port: number
  remote_host: string
  remote_port: number
  active_conns: number
  total_conns: number
  bytes_sent: number
  bytes_recv: number
}

export interface TunnelStats {
  id: string
  listeners?: ListenerStats[]
  forwards?: ForwardStats[]
  active_conns: number
  total_conns: number
  bytes_sent: number
  bytes_recv: number
}

export interface LogEntry {
  time: string
  level: string
  message: string
  tunnel_id?: string
}

// ===== 节点状态（活动连接、流量、访问历史）=====

export interface ConnSnapshot {
  id: string
  tunnel_id: string
  tunnel_name: string
  listener_id: string
  protocol: string
  listen_port: number
  source: string
  target: string
  started_at: string
  bytes_sent: number
  bytes_recv: number
}

export interface AccessRecord {
  time: string
  tunnel_id: string
  tunnel_name: string
  listener_id: string
  protocol: string
  listen_port: number
  target: string
  source: string
  result: string
  duration_ms: number
  denied: boolean
}

export interface TunnelTraffic {
  id: string
  name: string
  running: boolean
  active_conns: number
  total_conns: number
  bytes_sent: number
  bytes_recv: number
}

export interface NodeStatus {
  version: string
  started_at: string
  uptime_seconds: number
  tunnel_total: number
  tunnel_running: number
  listener_count: number
  forward_count: number
  active_conns: number
  total_conns: number
  bytes_sent: number
  bytes_recv: number
  tunnels: TunnelTraffic[]
  connections: ConnSnapshot[]
  access_history: AccessRecord[]
}
