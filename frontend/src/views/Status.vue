<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import * as api from '@/api'
import type { AccessRecord, ConnSnapshot, NodeStatus, TunnelTraffic } from '@/types'
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
    // 节点详情打开时同步刷新该节点的访问历史；节点已被删除则关闭抽屉
    if (drawerOpen.value && activeNodeId.value) {
      if (!activeNode.value) {
        closeNode()
      } else if (!nodeHistoryLoading.value) {
        void loadNodeHistory()
      }
    }
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
const nodes = computed<TunnelTraffic[]>(() => status.value?.tunnels || [])

// ===== 节点详情（抽屉） =====
const activeNodeId = ref('')
const drawerOpen = ref(false)
const detailTab = ref<'conns' | 'history'>('conns')
const nodeHistory = ref<AccessRecord[]>([])
const nodeHistoryLoading = ref(false)

const activeNode = computed<TunnelTraffic | null>(
  () => nodes.value.find(n => n.id === activeNodeId.value) || null,
)

const nodeConnections = computed<ConnSnapshot[]>(() => {
  if (!activeNodeId.value) return []
  return connections.value.filter(c => c.tunnel_id === activeNodeId.value)
})

async function loadNodeHistory() {
  const id = activeNodeId.value
  if (!id) return
  nodeHistoryLoading.value = true
  try {
    const recs = await api.getNodeHistory(id, 500)
    // 节点可能已切换，仅应用当前节点的结果
    if (id === activeNodeId.value) nodeHistory.value = recs
  } catch {
    // 保留上次数据
  } finally {
    if (id === activeNodeId.value) nodeHistoryLoading.value = false
  }
}

function openNode(node: TunnelTraffic) {
  activeNodeId.value = node.id
  nodeHistory.value = []
  detailTab.value = 'conns'
  drawerOpen.value = true
  void loadNodeHistory()
}

function closeNode() {
  drawerOpen.value = false
  activeNodeId.value = ''
  nodeHistory.value = []
  nodeHistoryLoading.value = false
}

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
          <span class="panel-title">节点</span>
          <span class="panel-note">点击节点卡片查看该节点的连接与访问历史</span>
        </div>
      </template>
      <div class="node-grid" v-if="nodes.length">
        <div
          class="node-card"
          v-for="node in nodes"
          :key="node.id"
          role="button"
          tabindex="0"
          :aria-label="`查看节点 ${node.name} 详情`"
          @click="openNode(node)"
          @keydown.enter="openNode(node)"
          @keydown.space.prevent="openNode(node)"
        >
          <div class="node-head">
            <span class="node-name">{{ node.name }}</span>
            <el-tag size="small" :type="node.running ? 'success' : 'info'">
              {{ node.running ? '运行中' : '已停止' }}
            </el-tag>
          </div>
          <div class="node-metrics">
            <div class="metric"><span class="k">活动连接</span><span class="v">{{ node.active_conns }}</span></div>
            <div class="metric"><span class="k">累计连接</span><span class="v">{{ node.total_conns }}</span></div>
            <div class="metric"><span class="k">累计上传</span><span class="v">{{ formatBytes(node.bytes_sent) }}</span></div>
            <div class="metric"><span class="k">累计下载</span><span class="v">{{ formatBytes(node.bytes_recv) }}</span></div>
          </div>
          <div class="node-foot">查看详情 →</div>
        </div>
      </div>
      <el-empty v-else description="暂无隧道" :image-size="60" />
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

    <el-drawer
      v-model="drawerOpen"
      :title="activeNode ? `节点：${activeNode.name}` : '节点详情'"
      size="62%"
      class="node-drawer"
    >
      <div v-if="activeNode" class="drawer-body">
        <div class="drawer-stats">
          <div class="stat-card">
            <div class="stat-label">活动连接</div>
            <div class="stat-value">{{ activeNode.active_conns }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">累计连接</div>
            <div class="stat-value">{{ activeNode.total_conns }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">累计上传</div>
            <div class="stat-value">{{ formatBytes(activeNode.bytes_sent) }}</div>
          </div>
          <div class="stat-card">
            <div class="stat-label">累计下载</div>
            <div class="stat-value">{{ formatBytes(activeNode.bytes_recv) }}</div>
          </div>
        </div>

        <el-tabs v-model="detailTab">
          <el-tab-pane :label="`当前连接 (${nodeConnections.length})`" name="conns">
            <el-table
              :data="nodeConnections"
              size="small"
              class="dark-table"
              empty-text="暂无活动连接"
            >
              <el-table-column label="协议" width="80">
                <template #default="{ row }">
                  <el-tag size="small" type="info">{{ protocolText(row.protocol) }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="监听" width="120">
                <template #default="{ row }">{{ protocolText(row.protocol) }} :{{ row.listen_port }}</template>
              </el-table-column>
              <el-table-column prop="source" label="来源" width="130" show-overflow-tooltip />
              <el-table-column label="目标" min-width="170" show-overflow-tooltip>
                <template #default="{ row }">{{ row.target || '-' }}</template>
              </el-table-column>
              <el-table-column label="已持续" width="90">
                <template #default="{ row }">{{ formatElapsed(row.started_at, now) }}</template>
              </el-table-column>
              <el-table-column label="上传" width="100">
                <template #default="{ row }">{{ formatBytes(row.bytes_sent) }}</template>
              </el-table-column>
              <el-table-column label="下载" width="100">
                <template #default="{ row }">{{ formatBytes(row.bytes_recv) }}</template>
              </el-table-column>
            </el-table>
          </el-tab-pane>

          <el-tab-pane :label="`访问历史 (${nodeHistory.length})`" name="history">
            <el-table
              :data="nodeHistory"
              size="small"
              class="dark-table"
              empty-text="暂无访问记录（请为监听器开启「记录访问日志」）"
            >
              <el-table-column label="时间" width="160">
                <template #default="{ row }">{{ formatTime(row.time) }}</template>
              </el-table-column>
              <el-table-column label="监听" width="120">
                <template #default="{ row }">{{ protocolText(row.protocol) }} :{{ row.listen_port }}</template>
              </el-table-column>
              <el-table-column label="来源" width="130" show-overflow-tooltip>
                <template #default="{ row }">{{ row.source || '-' }}</template>
              </el-table-column>
              <el-table-column label="目标" min-width="170" show-overflow-tooltip>
                <template #default="{ row }">{{ row.target || '-' }}</template>
              </el-table-column>
              <el-table-column label="结果" min-width="150">
                <template #default="{ row }">
                  <el-tag size="small" :type="resultType(row)">{{ row.result }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="耗时" width="85">
                <template #default="{ row }">{{ row.denied ? '-' : `${row.duration_ms} ms` }}</template>
              </el-table-column>
            </el-table>
          </el-tab-pane>
        </el-tabs>
      </div>
    </el-drawer>
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
.node-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
  gap: 12px;
  padding: 4px;
}
.node-card {
  background-color: #2b2f38;
  border: 1px solid #3a3f4b;
  border-radius: 8px;
  padding: 14px;
  cursor: pointer;
  transition: border-color 0.15s, transform 0.15s;
}
.node-card:hover {
  border-color: #5b8ff9;
  transform: translateY(-1px);
}
.node-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.node-name {
  font-weight: 600;
  color: #f5f6f7;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.node-metrics {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px 12px;
}
.metric {
  display: flex;
  justify-content: space-between;
  gap: 6px;
  font-size: 12px;
}
.metric .k {
  color: #8a8d95;
}
.metric .v {
  color: #e0e3e8;
  font-variant-numeric: tabular-nums;
}
.node-foot {
  margin-top: 10px;
  font-size: 12px;
  color: #5b8ff9;
}
.drawer-body {
  padding: 0 4px;
}
.drawer-stats {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
  margin-bottom: 16px;
}
:deep(.el-drawer) {
  background-color: #1e2128;
}
:deep(.el-drawer__header) {
  color: #f5f6f7;
  margin-bottom: 8px;
}
:deep(.el-drawer__body) {
  overflow-y: auto;
}
:deep(.el-tabs__item) {
  color: #a0a4ad;
}
:deep(.el-tabs__item.is-active) {
  color: #5b8ff9;
}
</style>
