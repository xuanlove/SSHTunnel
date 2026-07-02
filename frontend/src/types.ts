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

export interface ProxyListener {
  id: string
  protocol: ProxyProtocol
  listen_port: number
  allow_external: boolean
  auth?: AuthConfig | null
  tls?: TLSConfig | null
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
