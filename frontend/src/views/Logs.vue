<script setup lang="ts">
import { ref, computed, onMounted, watch, nextTick } from 'vue'
import { useTunnelStore } from '@/stores/tunnel'

const store = useTunnelStore()

const levels = ref<string[]>([])
const keyword = ref('')
const logContainer = ref<HTMLElement | null>(null)

const levelOptions = [
  { label: 'DEBUG', value: 'DEBUG' },
  { label: 'INFO', value: 'INFO' },
  { label: 'WARN', value: 'WARN' },
  { label: 'ERROR', value: 'ERROR' },
]

const filteredLogs = computed(() => {
  return store.logs.filter(log => {
    if (levels.value.length && !levels.value.includes(log.level)) return false
    if (keyword.value && !log.message.toLowerCase().includes(keyword.value.toLowerCase())) return false
    return true
  })
})

const levelType = (lvl: string) => {
  return { DEBUG: 'info', INFO: 'primary', WARN: 'warning', ERROR: 'danger' }[lvl] || 'info'
}

const levelColor = (lvl: string) => {
  return {
    DEBUG: '#909399',
    INFO: '#409eff',
    WARN: '#e6a23c',
    ERROR: '#f56c6c',
  }[lvl] || '#909399'
}

function clearLogs() {
  store.logs.splice(0, store.logs.length)
}

function exportLogs() {
  const text = filteredLogs.value
    .map(l => `[${l.time}] [${l.level}] ${l.tunnel_id ? '(' + l.tunnel_id + ') ' : ''}${l.message}`)
    .join('\n')
  const blob = new Blob([text], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `sshtunnel-logs-${Date.now()}.txt`
  a.click()
  URL.revokeObjectURL(url)
}

watch(() => store.logs.length, async () => {
  await nextTick()
  if (logContainer.value) {
    logContainer.value.scrollTop = logContainer.value.scrollHeight
  }
})

onMounted(async () => {
  await store.loadLogs(500)
  store.setupLogListener()
})
</script>

<template>
  <div class="logs-page">
    <div class="toolbar">
      <h2>操作日志</h2>
      <div class="actions">
        <el-select
          v-model="levels"
          multiple
          collapse-tags
          placeholder="级别过滤"
          style="width: 220px"
        >
          <el-option
            v-for="opt in levelOptions"
            :key="opt.value"
            :label="opt.label"
            :value="opt.value"
          />
        </el-select>
        <el-input v-model="keyword" placeholder="关键字搜索" clearable style="width: 200px" />
        <el-button @click="exportLogs">导出</el-button>
        <el-button @click="clearLogs">清空</el-button>
      </div>
    </div>

    <div class="log-container" ref="logContainer">
      <div v-for="(log, idx) in filteredLogs" :key="idx" class="log-line">
        <span class="log-time">{{ log.time }}</span>
        <el-tag :type="levelType(log.level)" size="small">{{ log.level }}</el-tag>
        <span class="log-msg" :style="{ color: levelColor(log.level) }">
          {{ log.tunnel_id ? `[${log.tunnel_id}] ` : '' }}{{ log.message }}
        </span>
      </div>
      <el-empty v-if="!filteredLogs.length" description="暂无日志" :image-size="80" />
    </div>
  </div>
</template>

<style scoped>
.logs-page {
  display: flex;
  flex-direction: column;
  height: 100%;
  gap: 12px;
}
.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-shrink: 0;
}
.toolbar h2 {
  font-size: 18px;
  color: #f5f6f7;
}
.actions {
  display: flex;
  gap: 8px;
  align-items: center;
}
.log-container {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  background-color: #14161a;
  border: 1px solid #3a3f4b;
  border-radius: 4px;
  padding: 12px;
  font-family: 'Consolas', 'Monaco', monospace;
  font-size: 13px;
}
.log-line {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 0;
  border-bottom: 1px dashed #2a2d33;
}
.log-time {
  color: #8a8d95;
  flex-shrink: 0;
  width: 150px;
}
.log-msg {
  flex: 1;
  word-break: break-all;
}
</style>
