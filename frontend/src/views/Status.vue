<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import * as api from '@/api'
import type { AccessRecord, ConnSnapshot, NodeStatus } from '@/types'
import { formatBytes, formatDuration, formatElapsed, formatTime } from '@/utils/format'

const status = ref<NodeStatus | null>(null)
const autoRefresh = ref(true)
const loading = ref(false)
const lastUpdated = ref<Date | null>(null)
const now = ref(Date.now())

let pollTimer = 0
let tickTimer = 0

async function load() {
  if (loading.value) return // 上一轮未返回时跳过，避免请求叠加
  loading.value = true
  try {
    status.value = await api.getNodeStatus(200)
    lastUpdated.value = new Date()
    now.value = Date.now()
  } catch {
    // 轮询失败时保留上次数据
  } finally {
    loading.value = false
  }
}

function restartTimer() {
  if (pollTimer) window.clearInterval(pollTimer)
  pollTimer = 0
  if (autoRefresh.value) {
    pollTimer = window.setInterval(load, 3000)
  }
}

onMounted(() => {
  void load()
  restartTimer()
  // 每秒刷新活动连接的「已持续」显示
  tickTimer = window.setInterval(() => { now.value = Date.now() }, 1000)
})

onUnmounted(() => {
  if (pollTimer) window.clearInterval(pollTimer)
  if (tickTimer) window.clearInterval(tickTimer)
})

const overviewCards = computed(() => {
  const s = status.value
  return [
    {
      label: '运行时长',
      value: formatDuration(s?.uptime_seconds),
      sub: s?.started_at ? `启动于 ${formatTime(s.started_at)}` : '',
    },
    {
      label: '隧道',
      value: `${s?.tunnel_running ?? 0} / ${s?.tunnel_total ?? 0}`,
      sub: '运行中 / 总数',
    },
    {
      label: '监听服务',
      value: String(s?.listener_count ?? 0),
      sub: `本地转发 ${s?.forward_count ?? 0}`,
    },
    {
      label: '活动连接',
      value: String(s?.active_conns ?? 0),
      sub: `累计 ${s?.total_conns ?? 0}`,
    },
    {
      label: '累计上传',
      value: formatBytes(s?.bytes_sent),
      sub: '客户端 → 目标',
    },
    {
      label: '累计下载',
      value: formatBytes(s?.bytes_recv),
      sub: '目标 → 客户端',
    },
  ]
})

const connections = computed<ConnSnapshot[]>(() => status.value?.connections || [])
const accessHistory = computed<AccessRecord[]>(() => status.value?.access_history || [])

const versionText = computed(() => {
  const v = status.value?.version || ''
  if (!v) return ''
  if (v === 'dev') return 'dev'
  return v.startsWith('v') ? v : `v${v}`
})

function protocolText(p: string): string {
  return p === 'local' ? '本地' : p.toUpperCase()
}

function resultType(row: AccessRecord): 'success' | 'warning' | 'danger' {
  if (row.denied) return 'danger'
  return String(row.result || '').startsWith('成功') ? 'success' : 'warning'
}
</script>

<template>
  <div class="status-page">
    <div class="toolbar">
      <div class="title-box">
        <h2>节点状态</h2>
        <span class="version" v-if="versionText">{{ versionText }}</span>
      </div>
      <div class="actions">
        <span class="updated" v-if="lastUpdated">{{ formatTime(lastUpdated) }} 更新</span>
        <el-switch v-model="autoRefresh" active-text="自动刷新" @change="restartTimer" />
        <el-button :loading="loading" @click="load">刷新</el-button>
      </div>
    </div>

    <div class="overview">
      <div class="stat-card" v-for="card in overviewCards" :key="card.label">
        <div class="stat-label">{{ card.label }}</div>
        <div class="stat-value">{{ card.value }}</div>
        <div class="stat-sub" v-if="card.sub">{{ card.sub }}</div>
      </div>
    </div>

    <el-card class="panel" shadow="never">
      <template #header>
        <div class="panel-header">
          <span class="panel-title">隧道流量</span>
          <span class="panel-note">累计值包含本进程内已停止阶段</span>
        </div>
      </template>
      <el-table
        :data="status?.tunnels || []"
        size="small"
        class="dark-table"
        empty-text="暂无隧道"
      >
        <el-table-column prop="name" label="隧道" min-width="160" show-overflow-tooltip />
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.running ? 'success' : 'info'">
              {{ row.running ? '运行中' : '已停止' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="active_conns" label="活动连接" width="100" />
        <el-table-column prop="total_conns" label="累计连接" width="100" />
        <el-table-column label="累计上传" width="120">
          <template #default="{ row }">{{ formatBytes(row.bytes_sent) }}</template>
        </el-table-column>
        <el-table-column label="累计下载" width="120">
          <template #default="{ row }">{{ formatBytes(row.bytes_recv) }}</template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card class="panel" shadow="never">
      <template #header>
        <div class="panel-header">
          <span class="panel-title">当前连接</span>
          <span class="panel-note">{{ connections.length }} 条</span>
        </div>
      </template>
      <el-table
        :data="connections"
        size="small"
        class="dark-table"
        empty-text="暂无活动连接"
      >
        <el-table-column label="协议" width="90">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ protocolText(row.protocol) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="tunnel_name" label="隧道" min-width="140" show-overflow-tooltip />
        <el-table-column label="监听" width="130">
          <template #default="{ row }">{{ protocolText(row.protocol) }} :{{ row.listen_port }}</template>
        </el-table-column>
        <el-table-column prop="source" label="来源" width="140" show-overflow-tooltip />
        <el-table-column prop="target" label="目标" min-width="180" show-overflow-tooltip />
        <el-table-column label="已持续" width="90">
          <template #default="{ row }">{{ formatElapsed(row.started_at, now) }}</template>
        </el-table-column>
        <el-table-column label="上传" width="105">
          <template #default="{ row }">{{ formatBytes(row.bytes_sent) }}</template>
        </el-table-column>
        <el-table-column label="下载" width="105">
          <template #default="{ row }">{{ formatBytes(row.bytes_recv) }}</template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card class="panel" shadow="never">
      <template #header>
        <div class="panel-header">
          <span class="panel-title">访问历史</span>
          <span class="panel-note">
            仅在监听器开启「记录访问日志」时采集，最多保留最近 2000 条
          </span>
        </div>
      </template>
      <el-table
        :data="accessHistory"
        size="small"
        class="dark-table"
        empty-text="暂无访问记录（请为代理监听器开启「记录访问日志」）"
      >
        <el-table-column label="时间" width="165">
          <template #default="{ row }">{{ formatTime(row.time) }}</template>
        </el-table-column>
        <el-table-column prop="tunnel_name" label="隧道" min-width="130" show-overflow-tooltip />
        <el-table-column label="监听" width="125">
          <template #default="{ row }">{{ protocolText(row.protocol) }} :{{ row.listen_port }}</template>
        </el-table-column>
        <el-table-column label="来源" width="130" show-overflow-tooltip>
          <template #default="{ row }">{{ row.source || '-' }}</template>
        </el-table-column>
        <el-table-column label="目标" min-width="170" show-overflow-tooltip>
          <template #default="{ row }">{{ row.target || '-' }}</template>
        </el-table-column>
        <el-table-column label="结果" min-width="160">
          <template #default="{ row }">
            <el-tag size="small" :type="resultType(row)">{{ row.result }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="耗时" width="90">
          <template #default="{ row }">{{ row.denied ? '-' : `${row.duration_ms} ms` }}</template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>

<style scoped>
.status-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.title-box {
  display: flex;
  align-items: baseline;
  gap: 10px;
}
.toolbar h2 {
  font-size: 18px;
  color: #f5f6f7;
}
.version {
  font-size: 12px;
  color: #8a8d95;
}
.actions {
  display: flex;
  gap: 12px;
  align-items: center;
}
.updated {
  font-size: 12px;
  color: #8a8d95;
  font-variant-numeric: tabular-nums;
}
.overview {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 12px;
}
.stat-card {
  background-color: #252830;
  border: 1px solid #3a3f4b;
  border-radius: 6px;
  padding: 14px 16px;
}
.stat-label {
  font-size: 12px;
  color: #8a8d95;
  margin-bottom: 6px;
}
.stat-value {
  font-size: 22px;
  font-weight: 600;
  color: #f5f6f7;
  font-variant-numeric: tabular-nums;
  word-break: break-all;
}
.stat-sub {
  margin-top: 4px;
  font-size: 12px;
  color: #6d717a;
}
.panel {
  --el-card-bg-color: #252830;
  border: 1px solid #3a3f4b;
}
.panel :deep(.el-card__header) {
  border-bottom: 1px solid #3a3f4b;
  padding: 12px 16px;
}
.panel :deep(.el-card__body) {
  padding: 8px 12px 12px;
}
.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
}
.panel-title {
  font-size: 14px;
  font-weight: 600;
  color: #f5f6f7;
}
.panel-note {
  font-size: 12px;
  color: #8a8d95;
}
.dark-table {
  --el-table-bg-color: #252830;
  --el-table-tr-bg-color: #252830;
  --el-table-header-bg-color: #2b2f38;
  --el-table-row-hover-bg-color: #2f333e;
  --el-table-border-color: #3a3f4b;
  --el-table-text-color: #e0e3e8;
  --el-table-header-text-color: #c0c3cb;
  --el-table-empty-text-color: #8a8d95;
}
</style>
