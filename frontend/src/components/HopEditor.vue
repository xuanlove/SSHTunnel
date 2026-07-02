<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage } from 'element-plus'
import * as api from '@/api'
import type { HopConfig } from '@/types'

const props = defineProps<{ modelValue: HopConfig[] }>()
const emit = defineEmits<{ 'update:modelValue': [HopConfig[]] }>()

const hops = computed<HopConfig[]>({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const simpleInput = ref('')

async function parseSimple() {
  if (!simpleInput.value.trim()) {
    ElMessage.warning('请输入简写格式')
    return
  }
  try {
    const parsed = await api.parseHopChainString(simpleInput.value)
    // 合并：保留已有认证信息，按索引匹配 user@host:port
    const merged = parsed.map((p, idx) => {
      const existing = hops.value[idx]
      if (existing && existing.user === p.user && existing.host === p.host && existing.port === p.port) {
        return { ...p, auth_type: existing.auth_type, password: existing.password, key_content: existing.key_content, passphrase: existing.passphrase }
      }
      return p
    })
    hops.value = merged
    ElMessage.success(`已解析 ${merged.length} 跳`)
  } catch (e: any) {
    ElMessage.error(e?.message || '解析失败')
  }
}

function addHop() {
  hops.value = [...hops.value, { user: '', host: '', port: 22, auth_type: 'password' }]
}

function removeHop(idx: number) {
  const arr = [...hops.value]
  arr.splice(idx, 1)
  hops.value = arr
}

function updateHop(idx: number, patch: Partial<HopConfig>) {
  const arr = [...hops.value]
  arr[idx] = { ...arr[idx], ...patch }
  hops.value = arr
}
</script>

<template>
  <div class="hop-editor">
    <div class="simple-input">
      <el-input
        v-model="simpleInput"
        placeholder="user1@host1:port1,user2@host2:port2"
        @keyup.enter="parseSimple"
      />
      <el-button @click="parseSimple">解析</el-button>
    </div>

    <div v-for="(hop, idx) in hops" :key="idx" class="hop-card">
      <div class="hop-title">
        <span>第 {{ idx + 1 }} 跳</span>
        <el-button type="danger" size="small" @click="removeHop(idx)">删除</el-button>
      </div>
      <el-row :gutter="12">
        <el-col :span="8">
          <div class="field-label">用户名</div>
          <el-input :model-value="hop.user" @update:model-value="(v: string) => updateHop(idx, { user: v })" />
        </el-col>
        <el-col :span="10">
          <div class="field-label">主机</div>
          <el-input :model-value="hop.host" @update:model-value="(v: string) => updateHop(idx, { host: v })" />
        </el-col>
        <el-col :span="6">
          <div class="field-label">端口</div>
          <el-input-number :model-value="hop.port" :min="1" :max="65535" controls-position="right" style="width: 100%" @update:model-value="(v: number | string | undefined) => updateHop(idx, { port: Number(v) })" />
        </el-col>
      </el-row>

      <div class="auth-section">
        <el-radio-group :model-value="hop.auth_type" @update:model-value="(v: string | number | boolean) => updateHop(idx, { auth_type: v as string })">
          <el-radio value="password">密码认证</el-radio>
          <el-radio value="key">密钥文本</el-radio>
        </el-radio-group>
      </div>

      <div v-if="hop.auth_type === 'password'" class="auth-fields">
        <el-input
          type="password"
          show-password
          placeholder="密码"
          :model-value="hop.password"
          @update:model-value="(v: string) => updateHop(idx, { password: v })"
        />
      </div>

      <div v-else class="auth-fields">
        <div class="field-label">密钥内容（PEM 格式，包含 -----BEGIN ... PRIVATE KEY----- 头尾）</div>
        <el-input
          type="textarea"
          :rows="6"
          :model-value="hop.key_content"
          @update:model-value="(v: string) => updateHop(idx, { key_content: v })"
          placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;...&#10;-----END OPENSSH PRIVATE KEY-----"
          class="key-textarea"
        />
        <div class="field-label" style="margin-top: 8px;">Passphrase（如密钥有加密）</div>
        <el-input
          type="password"
          show-password
          :model-value="hop.passphrase"
          @update:model-value="(v: string) => updateHop(idx, { passphrase: v })"
        />
      </div>
    </div>

    <el-button class="add-btn" @click="addHop">+ 添加跳板</el-button>
  </div>
</template>

<style scoped>
.hop-editor {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.simple-input {
  display: flex;
  gap: 8px;
}
.hop-card {
  background-color: #14161a;
  border: 1px solid #3a3f4b;
  border-radius: 4px;
  padding: 12px;
}
.hop-title {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 10px;
  color: #c0c3cb;
  font-size: 13px;
  font-weight: 600;
}
.field-label {
  font-size: 12px;
  color: #8a8d95;
  margin-bottom: 4px;
}
.auth-section {
  margin-top: 10px;
}
.auth-fields {
  margin-top: 8px;
}
.key-textarea :deep(.el-textarea__inner) {
  background-color: #14161a !important;
  color: #f5f6f7 !important;
  border-color: #3a3f4b !important;
  font-family: 'Consolas', 'Monaco', monospace !important;
  font-size: 12px !important;
}
.key-textarea :deep(.el-textarea__inner:focus) {
  border-color: #409eff !important;
}
.add-btn {
  border-style: dashed;
  width: 100%;
}
</style>
