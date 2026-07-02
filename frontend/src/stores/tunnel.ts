import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { TunnelConfig, LogEntry } from '@/types'
import * as api from '@/api'

export const useTunnelStore = defineStore('tunnel', () => {
  const configs = ref<TunnelConfig[]>([])
  const logs = ref<LogEntry[]>([])
  const loading = ref(false)

  async function loadConfigs() {
    loading.value = true
    try {
      configs.value = await api.listConfigs()
    } finally {
      loading.value = false
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

  function setupLogListener() {
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
  }
})
