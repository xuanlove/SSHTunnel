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
  cert_file: string
  key_file: string
}

// 防火墙模式：blacklist=黑名单（默认，命中拒绝）；whitelist=白名单（仅命中放行）
export type FirewallMode = 'blacklist' | 'whitelist'

export interface FirewallConfig {
  enabled: boolean
  mode: FirewallMode
  // 自定义 IP / CIDR，支持 IPv4 与 IPv6（如 10.0.0.0/8、203.0.113.7、2001:db8::/32）
  ips?: string[]
  // GeoIP 国家代码（ISO 3166-1 alpha-2，如 CN、US），需服务器配置 GeoLite2 数据库
  countries?: string[]
}

export interface ProxyListener {
  id: string
  protocol: ProxyProtocol
  listen_port: number
  allow_external: boolean
  auth?: AuthConfig | null
  tls?: TLSConfig | null
  firewall?: FirewallConfig | null
}

export interface TunnelConfig {
  id: string
  name: string
  hop_chain: HopConfig[]
  tunnel_type: TunnelType
  local_forwards?: LocalForward[]
  proxy_listeners?: ProxyListener[]
  auto_reconnect: boolean
  status: TunnelStatus
}

export interface LogEntry {
  time: string
  level: string
  message: string
  tunnel_id?: string
}
