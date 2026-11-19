<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useTunnelStore } from '@/stores/tunnel'
import { useRouter } from 'vue-router'
import * as api from '@/api'
import type { TunnelConfig, TunnelStats } from '@/types'
import { formatBytes } from '@/utils/format'

const store = useTunnelStore()
const router = useRouter()

// ===== 运行统计（每 3 秒轮询，仅包含运行中的隧道）=====
const stats = ref<Record<string, TunnelStats>>({})
let statsTimer = 0
let statsLoading = false

async function loadStats() {
  if (statsLoading) return // 上一轮未返回时跳过，避免请求叠加
  statsLoading = true
  try {
    stats.value = await api.getTunnelStats()
  } catch {
    // 统计失败不影响主流程
  } finally {
    statsLoading = false
  }
}

onMounted(() => {
  void loadStats()
  statsTimer = window.setInterval(loadStats, 3000)
})

onUnmounted(() => {
  if (statsTimer) window.clearInterval(statsTimer)
})

const statusType = (s: string) => {
  return { running: 'success', stopped: 'info', error: 'danger', starting: 'warning' }[s] || 'info'
}
const statusText = (s: string) => {
  return { running: '运行中', stopped: '已停止', error: '错误', starting: '启动中' }[s] || s
}

const hopSummary = (cfg: TunnelConfig) => {
  return cfg.hop_chain.map(h => `${h.user}@${h.host}:${h.port}`).join(' → ')
}

const listenerSummary = (cfg: TunnelConfig) => {
  const items: { key: string; text: string; auth: boolean; tip: string }[] = []
  const st = stats.value[cfg.id]
  if (cfg.local_forwards) {
    cfg.local_forwards.forEach(lf => {
      const fs = st?.forwards?.find(f => f.id === lf.id)
      items.push({
        key: lf.id,
        text: `本地转发 :${lf.local_port}→${lf.remote_host}:${lf.remote_port}`,
        auth: false,
        tip: fs
          ? `活跃 ${fs.active_conns} · 累计 ${fs.total_conns} · ↑${formatBytes(fs.bytes_sent)} ↓${formatBytes(fs.bytes_recv)}`
          : '',
      })
    })
  }
  if (cfg.proxy_listeners) {
    cfg.proxy_listeners.forEach(pl => {
      const ls = st?.listeners?.find(l => l.id === pl.id)
      items.push({
        key: pl.id,
        text: `${pl.protocol.toUpperCase()} :${pl.listen_port}`,
        auth: !!pl.auth,
        tip: ls
          ? `活跃 ${ls.active_conns} · 累计 ${ls.total_conns} · ↑${formatBytes(ls.bytes_sent)} ↓${formatBytes(ls.bytes_recv)}`
          : '',
      })
    })
  }
  return items
}

async function handleStart(id: string) {
  try {
    await store.start(id)
    ElMessage.success('隧道已启动')
  } catch (e: any) {
    ElMessage.error(e?.message || '启动失败')
  }
}

async function handleStop(id: string) {
  try {
    await store.stop(id)
    ElMessage.success('隧道已停止')
  } catch (e: any) {
    ElMessage.error(e?.message || '停止失败')
  }
}

async function handleDelete(id: string, name: string) {
  try {
    await ElMessageBox.confirm(`确定删除隧道 "${name}" 吗？`, '提示', { type: 'warning' })
    await store.remove(id)
    ElMessage.success('已删除')
  } catch (e) {
    // 用户取消
  }
}

async function handleStartAll() {
  try {
    await store.startAll()
    ElMessage.success('已启动全部隧道')
  } catch (e: any) {
    ElMessage.error(e?.message || '批量启动失败')
  }
}

async function handleStopAll() {
  try {
    await store.stopAll()
    ElMessage.success('已停止全部隧道')
  } catch (e: any) {
    ElMessage.error(e?.message || '批量停止失败')
  }
}
</script>

<template>
  <div class="dashboard">
    <div class="toolbar">
      <h2>隧道管理</h2>
      <div class="actions">
        <el-button @click="handleStartAll" :disabled="store.loading">批量启动</el-button>
        <el-button @click="handleStopAll" :disabled="store.loading">批量停止</el-button>
        <el-button type="primary" @click="router.push('/config')">新建隧道</el-button>
      </div>
    </div>

    <el-empty v-if="!store.configs.length && !store.loading" description="暂无隧道配置">
      <el-button type="primary" @click="router.push('/config')">立即创建</el-button>
    </el-empty>

    <el-row :gutter="16" v-else>
      <el-col :xs="24" :sm="12" :lg="8" v-for="cfg in store.configs" :key="cfg.id">
        <el-card class="tunnel-card" shadow="hover">
          <div class="card-header">
            <span class="name">{{ cfg.name }}</span>
            <div class="tags">
              <el-tag v-if="cfg.auto_start" size="small" type="info">开机启动</el-tag>
              <el-tag :type="statusType(cfg.status)" size="small">{{ statusText(cfg.status) }}</el-tag>
            </div>
          </div>
          <div class="hop-chain">{{ hopSummary(cfg) || '未配置跳板' }}</div>
          <div class="listeners">
            <el-tooltip
              v-for="item in listenerSummary(cfg)"
              :key="item.key"
              :content="item.tip"
              :disabled="!item.tip"
              placement="top"
            >
              <el-tag size="small" class="listener-tag">
                <el-icon v-if="item.auth" class="lock-icon"><Lock /></el-icon>
                {{ item.text }}
              </el-tag>
            </el-tooltip>
          </div>
          <div class="traffic">
            <span class="traffic-item">累计上传 {{ formatBytes(stats[cfg.id]?.bytes_sent) }}</span>
            <span class="traffic-item">累计下载 {{ formatBytes(stats[cfg.id]?.bytes_recv) }}</span>
            <span class="traffic-item">
              连接 {{ stats[cfg.id]?.active_conns ?? 0 }} · 累计 {{ stats[cfg.id]?.total_conns ?? 0 }}
            </span>
          </div>
          <div class="card-actions">
            <el-button
              v-if="cfg.status !== 'running' && cfg.status !== 'starting'"
              type="success"
              size="small"
              @click="handleStart(cfg.id)"
            >启动</el-button>
            <el-button
              v-else
              type="warning"
              size="small"
              :loading="cfg.status === 'starting'"
              @click="handleStop(cfg.id)"
            >停止</el-button>
            <el-button size="small" @click="router.push(`/config/${cfg.id}`)">编辑</el-button>
            <el-button type="danger" size="small" @click="handleDelete(cfg.id, cfg.name)">删除</el-button>
          </div>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<style scoped>
.dashboard {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.toolbar h2 {
  font-size: 18px;
  color: #f5f6f7;
}
.actions {
  display: flex;
  gap: 8px;
}
.tunnel-card {
  background-color: #252830;
  border: 1px solid #3a3f4b;
  margin-bottom: 16px;
}
.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}
.name {
  font-size: 15px;
  font-weight: 600;
  color: #f5f6f7;
}
.tags {
  display: flex;
  align-items: center;
  gap: 6px;
}
.hop-chain {
  font-size: 12px;
  color: #c0c3cb;
  margin-bottom: 10px;
  word-break: break-all;
}
.listeners {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 12px;
  min-height: 24px;
}
.listener-tag {
  background-color: #323640;
  border-color: #4a4f5b;
  color: #e0e3e8;
}
.traffic {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-bottom: 10px;
  font-size: 12px;
  color: #8a8d95;
}
.traffic-item {
  font-variant-numeric: tabular-nums;
}
.lock-icon {
  margin-right: 2px;
  vertical-align: middle;
}
.card-actions {
  display: flex;
  gap: 6px;
  border-top: 1px solid #3a3f4b;
  padding-top: 10px;
}
</style>
