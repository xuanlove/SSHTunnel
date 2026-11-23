<script setup lang="ts">
import { computed, ref, watch } from 'vue'
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
        return {
          ...p,
          auth_type: existing.auth_type || p.auth_type || 'password',
          password: existing.password,
          key_content: existing.key_content,
          passphrase: existing.passphrase,
        }
      }
      // 简写解析结果不含 auth_type，默认按密码认证，避免界面误显示密钥输入
      return { ...p, auth_type: p.auth_type || 'password' }
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

// 最小权限 SSH 账户配置（在跳板机执行）：无登录 Shell、仅密钥、仅允许 TCP 转发
function buildAccountCommands(user: string, pubKey: string): string {
  return `# 1) 创建无登录 Shell 的专用账户（默认无密码，仅密钥可登录）
sudo useradd --system --user-group --create-home --shell "$(command -v nologin)" ${user}

# 2) 写入公钥（替换为你的公钥内容）
sudo install -d -m 700 -o ${user} -g ${user} /home/${user}/.ssh
echo '${pubKey}' | sudo tee /home/${user}/.ssh/authorized_keys > /dev/null
sudo chown ${user}:${user} /home/${user}/.ssh/authorized_keys
sudo chmod 600 /home/${user}/.ssh/authorized_keys

# 3) 限制该账户只能做端口转发（追加到 sshd_config 末尾）
sudo tee -a /etc/ssh/sshd_config > /dev/null <<'EOF'

Match User ${user}
    AllowTcpForwarding yes
    PermitTTY no
    X11Forwarding no
    AllowAgentForwarding no
    PermitTunnel no
EOF
sudo sshd -t && { sudo systemctl reload ssh 2>/dev/null || sudo systemctl reload sshd; }

# 4) 如确需密码认证：sudo passwd ${user}（不推荐）`
}

const DEFAULT_USER = 'sshtunnel'
const DEFAULT_PUBKEY = 'ssh-ed25519 AAAA... your-key'
const ACCOUNT_USER_RE = /^[a-z_][a-z0-9_-]{0,31}$/

const accountUser = ref('')
const accountPubKey = ref('')
const accountCommands = ref(buildAccountCommands(DEFAULT_USER, DEFAULT_PUBKEY))
const accountGenerated = ref(false)

// 默认跟随第 1 跳用户名，用户手动填写后不再覆盖
watch(
  () => hops.value[0]?.user,
  (u) => {
    if (u && !accountUser.value) accountUser.value = u
  },
  { immediate: true },
)

// 输入变化后清除「已生成」标记，避免提示与实际内容不符
watch([accountUser, accountPubKey], () => {
  accountGenerated.value = false
})

// 复制文本：优先 Clipboard API，HTTP 非安全上下文下降级为 execCommand
async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 继续走降级方案
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

// 一键生成：按输入的用户名/公钥替换示例中的占位符，并复制到剪贴板
async function generateAccountConfig() {
  const user = (accountUser.value.trim() || hops.value[0]?.user?.trim() || DEFAULT_USER)
  if (!ACCOUNT_USER_RE.test(user)) {
    ElMessage.warning('用户名只能包含小写字母、数字、下划线和短横线，且以字母或下划线开头')
    return
  }
  const pubKey = accountPubKey.value.trim()
  if (pubKey && (!/^(ssh-|ecdsa-|sk-)/.test(pubKey) || /['\r\n]/.test(pubKey))) {
    ElMessage.warning('公钥格式不正确（应为 ssh-ed25519 / ssh-rsa / ecdsa- 开头的单行内容）')
    return
  }
  accountCommands.value = buildAccountCommands(user, pubKey || DEFAULT_PUBKEY)
  accountGenerated.value = true
  const ok = await copyText(accountCommands.value)
  if (ok) {
    ElMessage.success('已生成配置并复制到剪贴板')
  } else {
    ElMessage.success('已生成配置（自动复制失败，请手动复制）')
  }
}

async function copyAccountCommands() {
  const ok = await copyText(accountCommands.value)
  if (ok) {
    ElMessage.success('账户创建命令已复制')
  } else {
    ElMessage.error('复制失败，请手动选择命令复制')
  }
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

    <el-collapse class="ssh-account-hint" data-testid="ssh-account-hint">
      <el-collapse-item name="ssh-account">
        <template #title>
          <span class="hint-title">如何创建最小权限 SSH 账户</span>
        </template>
        <div class="hint-body">
          <p class="hint-desc">
            在 Linux 跳板机上以 root 执行。用户名默认取第 1 跳，可修改；填入公钥后命令即为可直接执行的完整配置。
            该账户无登录 Shell，仅用于本项目的 TCP 端口转发 / 代理。
          </p>
          <div class="hint-form">
            <el-input
              v-model="accountUser"
              size="small"
              placeholder="用户名（默认取第 1 跳）"
              data-testid="ssh-account-user"
              class="hint-user"
            />
            <el-input
              v-model="accountPubKey"
              size="small"
              placeholder="公钥内容（可选，ssh-ed25519 AAAA...）"
              data-testid="ssh-account-pubkey"
              class="hint-pubkey"
            />
            <el-button size="small" type="primary" data-testid="ssh-account-generate" @click="generateAccountConfig">
              一键生成配置
            </el-button>
          </div>
          <pre class="hint-code">{{ accountCommands }}</pre>
          <div class="hint-actions">
            <el-button size="small" data-testid="ssh-account-copy" @click="copyAccountCommands">复制命令</el-button>
            <span v-if="accountGenerated" class="hint-generated">已按当前输入生成</span>
          </div>
        </div>
      </el-collapse-item>
    </el-collapse>
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
.ssh-account-hint {
  border: none;
  --el-collapse-header-bg-color: transparent;
  --el-collapse-content-bg-color: transparent;
}
.ssh-account-hint :deep(.el-collapse-item__header) {
  height: 32px;
  border-bottom: none;
  background-color: transparent;
  color: #8a8d95;
  font-size: 12px;
}
.ssh-account-hint :deep(.el-collapse-item__wrap) {
  border-bottom: none;
  background-color: transparent;
}
.ssh-account-hint :deep(.el-collapse-item__content) {
  padding-bottom: 4px;
}
.hint-title {
  color: #409eff;
  font-size: 12px;
}
.hint-body {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.hint-form {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.hint-user {
  width: 180px;
}
.hint-pubkey {
  flex: 1;
  min-width: 220px;
}
.hint-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}
.hint-generated {
  font-size: 12px;
  color: #67c23a;
}
.hint-desc {
  margin: 0;
  font-size: 12px;
  color: #8a8d95;
  line-height: 1.6;
}
.hint-desc code {
  color: #c0c3cb;
}
.hint-code {
  margin: 0;
  padding: 10px;
  max-height: 280px;
  overflow: auto;
  background-color: #14161a;
  border: 1px solid #3a3f4b;
  border-radius: 4px;
  color: #c0c3cb;
  font-family: 'Consolas', 'Monaco', monospace;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre;
}
</style>
