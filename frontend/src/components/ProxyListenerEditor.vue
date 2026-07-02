<script setup lang="ts">
import { computed } from 'vue'
import { ElMessage } from 'element-plus'
import * as api from '@/api'
import type { ProxyListener, ProxyProtocol } from '@/types'

const props = defineProps<{ modelValue: ProxyListener[] }>()
const emit = defineEmits<{ 'update:modelValue': [ProxyListener[]] }>()

const listeners = computed<ProxyListener[]>({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const protocols: { label: string; value: ProxyProtocol }[] = [
  { label: 'HTTP 代理', value: 'http' },
  { label: 'HTTPS 代理', value: 'https' },
  { label: 'SOCKS4', value: 'socks4' },
  { label: 'SOCKS5', value: 'socks5' },
]

function addListener() {
  listeners.value = [...listeners.value, {
    id: `pl-${Date.now()}`,
    protocol: 'socks5',
    listen_port: 1080,
    allow_external: false,
    auth: null,
    tls: null,
  }]
}

function removeListener(idx: number) {
  const arr = [...listeners.value]
  arr.splice(idx, 1)
  listeners.value = arr
}

function updateListener(idx: number, patch: Partial<ProxyListener>) {
  const arr = [...listeners.value]
  arr[idx] = { ...arr[idx], ...patch }
  listeners.value = arr
}

function onProtocolChange(idx: number, protocol: ProxyProtocol) {
  const patch: Partial<ProxyListener> = { protocol }
  if (protocol === 'https') {
    patch.tls = { cert_file: '', key_file: '' }
  } else {
    patch.tls = null
  }
  updateListener(idx, patch)
}

function onAuthToggle(idx: number, enabled: boolean) {
  updateListener(idx, { auth: enabled ? { username: '', password: '' } : null })
}

function updateAuth(idx: number, field: 'username' | 'password', value: string) {
  const arr = [...listeners.value]
  const cur = arr[idx]
  if (cur.auth) {
    arr[idx] = { ...cur, auth: { ...cur.auth, [field]: value } }
    listeners.value = arr
  }
}

function updateTLS(idx: number, field: 'cert_file' | 'key_file', value: string) {
  const arr = [...listeners.value]
  const cur = arr[idx]
  if (cur.tls) {
    arr[idx] = { ...cur, tls: { ...cur.tls, [field]: value } }
    listeners.value = arr
  }
}

async function genCert(idx: number) {
  try {
    const { certPath, keyPath } = await api.generateSelfSignedCert()
    const arr = [...listeners.value]
    const cur = arr[idx]
    if (cur.tls) {
      arr[idx] = { ...cur, tls: { ...cur.tls, cert_file: certPath, key_file: keyPath } }
      listeners.value = arr
    }
    ElMessage.success('证书已生成并填入')
  } catch (e: any) {
    ElMessage.error(e?.message || '生成失败')
  }
}

// 端口冲突检测
const portCounts = computed(() => {
  const counts: Record<number, number> = {}
  listeners.value.forEach(l => {
    counts[l.listen_port] = (counts[l.listen_port] || 0) + 1
  })
  return counts
})

function isPortDuplicated(port: number) {
  return (portCounts.value[port] || 0) > 1
}
</script>

<template>
  <div class="proxy-editor">
    <div v-for="(pl, idx) in listeners" :key="pl.id" class="listener-card">
      <div class="listener-row">
        <el-select
          :model-value="pl.protocol"
          style="width: 150px"
          @update:model-value="(v: string | number | boolean) => onProtocolChange(idx, v as ProxyProtocol)"
        >
          <el-option v-for="p in protocols" :key="p.value" :label="p.label" :value="p.value" />
        </el-select>
        <el-input-number
          :model-value="pl.listen_port"
          :min="1"
          :max="65535"
          controls-position="right"
          style="width: 130px"
          @update:model-value="(v: number | string | undefined) => updateListener(idx, { listen_port: Number(v) })"
        />
        <span class="port-status" v-if="isPortDuplicated(pl.listen_port)">
          <el-tag type="danger" size="small">端口重复</el-tag>
        </span>
        <el-switch
          :model-value="pl.allow_external"
          active-text="允许外部访问"
          @update:model-value="(v: string | number | boolean) => updateListener(idx, { allow_external: v as boolean })"
        />
        <el-button type="danger" size="small" @click="removeListener(idx)">删除</el-button>
      </div>

      <div class="auth-row">
        <el-switch
          :model-value="!!pl.auth"
          active-text="启用认证"
          @update:model-value="(v: string | number | boolean) => onAuthToggle(idx, v as boolean)"
        />
        <template v-if="pl.auth">
          <el-input
            placeholder="用户名"
            :model-value="pl.auth.username"
            style="width: 160px"
            @update:model-value="(v: string) => updateAuth(idx, 'username', v)"
          />
          <el-input
            v-if="pl.protocol !== 'socks4'"
            type="password"
            show-password
            placeholder="密码"
            :model-value="pl.auth.password"
            style="width: 160px"
            @update:model-value="(v: string) => updateAuth(idx, 'password', v)"
          />
          <span v-else class="hint">SOCKS4 仅支持 UserID，无需密码</span>
        </template>
      </div>

      <div v-if="pl.protocol === 'https'" class="tls-row">
        <el-row :gutter="12">
          <el-col :span="10">
            <div class="field-label">证书文件</div>
            <el-input
              :model-value="pl.tls?.cert_file"
              @update:model-value="(v: string) => updateTLS(idx, 'cert_file', v)"
              placeholder="证书路径"
            />
          </el-col>
          <el-col :span="10">
            <div class="field-label">私钥文件</div>
            <el-input
              :model-value="pl.tls?.key_file"
              @update:model-value="(v: string) => updateTLS(idx, 'key_file', v)"
              placeholder="私钥路径"
            />
          </el-col>
          <el-col :span="4">
            <div class="field-label">&nbsp;</div>
            <el-button size="small" @click="genCert(idx)">生成</el-button>
          </el-col>
        </el-row>
      </div>
    </div>

    <el-button class="add-btn" @click="addListener">+ 添加监听器</el-button>
  </div>
</template>

<style scoped>
.proxy-editor {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.listener-card {
  background-color: #14161a;
  border: 1px solid #3a3f4b;
  border-radius: 4px;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.listener-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.auth-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.tls-row {
  border-top: 1px dashed #3a3f4b;
  padding-top: 8px;
}
.field-label {
  font-size: 12px;
  color: #8a8d95;
  margin-bottom: 4px;
}
.port-status {
  margin-left: -8px;
}
.hint {
  font-size: 12px;
  color: #ebb563;
}
.add-btn {
  border-style: dashed;
  width: 100%;
}
</style>
