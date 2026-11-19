import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { TunnelConfig, TunnelStatus, LogEntry } from '@/types'
import * as api from '@/api'

export const useTunnelStore = defineStore('tunnel', () => {
  const configs = ref<TunnelConfig[]>([])
  const logs = ref<LogEntry[]>([])
  const loading = ref(false)

  // 请求序号：并发加载时仅应用最新一次响应，避免迟到响应覆盖新状态
  let loadSeq = 0

  async function loadConfigs() {
    loading.value = true
    const seq = ++loadSeq
    try {
      const list = await api.listConfigs()
      if (seq === loadSeq) configs.value = list
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  async function save(cfg: TunnelConfig): Promise<TunnelConfig> {
    const saved = await api.saveConfig(cfg)
    await loadConfigs()
    return saved
  }

  async function remove(id: string) {
    await api.deleteConfig(id)
    await loadConfigs()
  }

  async function start(id: string) {
    await api.startTunnel(id)
    await loadConfigs()
  }

  async function stop(id: string) {
    await api.stopTunnel(id)
    await loadConfigs()
  }

  async function startAll() {
    await api.startAll()
    await loadConfigs()
  }

  async function stopAll() {
    await api.stopAll()
    await loadConfigs()
  }

  async function loadLogs(limit = 500) {
    logs.value = await api.getRecentLogs(limit)
  }

  // 应用隧道状态变更（由后端 WebSocket/事件实时推送）
  function applyStatus(id: string, status: string) {
    const idx = configs.value.findIndex(c => c.id === id)
    if (idx >= 0) {
      configs.value[idx] = { ...configs.value[idx], status: status as TunnelStatus }
    }
  }

  // 状态订阅只建立一次
  let statusListenerBound = false

  function setupStatusListener() {
    if (statusListenerBound) return
    statusListenerBound = true
    api.onTunnelStatus((data: any) => {
      if (data?.tunnel_id && data?.status) {
        applyStatus(data.tunnel_id, data.status)
      }
    })
  }

  // 日志订阅只建立一次，避免多个页面同时调用导致重复推送
  let logListenerBound = false

  function setupLogListener() {
    if (logListenerBound) return
    logListenerBound = true
    api.onLogNew((entry) => {
      logs.value.push(entry)
      if (logs.value.length > 1000) {
        logs.value = logs.value.slice(-500)
      }
    })
  }

  return {
    configs,
    logs,
    loading,
    loadConfigs,
    save,
    remove,
    start,
    stop,
    startAll,
    stopAll,
    loadLogs,
    setupLogListener,
    setupStatusListener,
    applyStatus,
  }
})
