<script setup lang="ts">
import type { CertInfo } from '@/api'

defineProps<{
  info: CertInfo | null
  error?: string
}>()

function formatTime(t: string): string {
  const d = new Date(t)
  if (Number.isNaN(d.getTime())) return t
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}
</script>

<template>
  <div v-if="info" class="cert-meta">
    <el-tag size="small" type="info">
      使用者 CN: {{ info.subject_cn || '（无）' }}
    </el-tag>
    <el-tag
      size="small"
      :type="info.expired ? 'danger' : (info.days_left <= 30 ? 'warning' : 'success')"
    >
      到期: {{ formatTime(info.not_after) }}
      {{ info.expired ? '（已过期）' : `（剩余 ${info.days_left} 天）` }}
    </el-tag>
  </div>
  <div v-else-if="error" class="cert-error">{{ error }}</div>
</template>

<style scoped>
.cert-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 8px;
}
.cert-error {
  margin-top: 6px;
  font-size: 12px;
  color: #f56c6c;
  line-height: 1.5;
}
</style>