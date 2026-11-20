<script setup lang="ts">
import { computed, ref } from 'vue'
import type { MetricPoint } from '@/api'
import { formatBytes } from '@/utils/format'

const props = defineProps<{
  points: MetricPoint[]
  loading?: boolean
}>()

type MetricKey = 'conns' | 'sent' | 'recv'
const metric = ref<MetricKey>('conns')

const metricLabel: Record<MetricKey, string> = {
  conns: '活动连接',
  sent: '上行流量',
  recv: '下行流量',
}

const W = 600
const H = 150
const PAD = 10

const values = computed<number[]>(() => {
  return props.points.map(p => {
    if (metric.value === 'conns') return p.active_conns
    return metric.value === 'sent' ? p.sent_delta : p.recv_delta
  })
})

const maxValue = computed(() => Math.max(1, ...values.value))

const path = computed(() => {
  const vs = values.value
  if (vs.length < 2) return ''
  const step = (W - PAD * 2) / (vs.length - 1)
  const scale = (H - PAD * 2) / maxValue.value
  return vs
    .map((v, i) => `${i === 0 ? 'M' : 'L'}${(PAD + i * step).toFixed(1)},${(H - PAD - v * scale).toFixed(1)}`)
    .join(' ')
})

const areaPath = computed(() => {
  const vs = values.value
  if (vs.length < 2) return ''
  const step = (W - PAD * 2) / (vs.length - 1)
  const scale = (H - PAD * 2) / maxValue.value
  const lastX = PAD + (vs.length - 1) * step
  return `${path.value} L${lastX.toFixed(1)},${H - PAD} L${PAD},${H - PAD} Z`
})

const summary = computed(() => {
  const vs = values.value
  if (!vs.length) return { max: '-', avg: '-' }
  const sum = vs.reduce((a, b) => a + b, 0)
  const fmt = metric.value === 'conns'
    ? (v: number) => String(Math.round(v))
    : (v: number) => formatBytes(v)
  return { max: fmt(Math.max(...vs)), avg: fmt(sum / vs.length) }
})

function shortTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

const timeLabels = computed(() => {
  const pts = props.points
  if (!pts.length) return []
  const idx = [0, Math.floor((pts.length - 1) / 2), pts.length - 1]
  return idx.map(i => shortTime(pts[i].time))
})
</script>

<template>
  <div class="trend-chart">
    <div class="trend-toolbar">
      <el-radio-group v-model="metric" size="small">
        <el-radio-button value="conns">连接数</el-radio-button>
        <el-radio-button value="sent">上行</el-radio-button>
        <el-radio-button value="recv">下行</el-radio-button>
      </el-radio-group>
      <span class="trend-summary">
        {{ metricLabel[metric] }} · 峰值 {{ summary.max }} · 均值 {{ summary.avg }}
      </span>
    </div>

    <div v-if="points.length < 2" class="trend-empty">
      {{ loading ? '加载中…' : '暂无采样数据（每 10 秒采样一次，约 1 分钟后可见趋势）' }}
    </div>
    <template v-else>
      <svg :viewBox="`0 0 ${W} ${H}`" class="trend-svg" preserveAspectRatio="none">
        <path :d="areaPath" class="trend-area" />
        <path :d="path" class="trend-line" />
      </svg>
      <div class="trend-axis">
        <span>{{ timeLabels[0] }}</span>
        <span>{{ timeLabels[1] }}</span>
        <span>{{ timeLabels[2] }}</span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.trend-chart {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.trend-toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.trend-summary {
  font-size: 12px;
  color: #8a8d95;
}
.trend-empty {
  height: 120px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #8a8d95;
  font-size: 12px;
}
.trend-svg {
  width: 100%;
  height: 150px;
  display: block;
}
.trend-line {
  fill: none;
  stroke: #5b8ff9;
  stroke-width: 2;
  vector-effect: non-scaling-stroke;
}
.trend-area {
  fill: rgba(91, 143, 249, 0.15);
  stroke: none;
}
.trend-axis {
  display: flex;
  justify-content: space-between;
  font-size: 11px;
  color: #6d717a;
  font-variant-numeric: tabular-nums;
}
</style>
