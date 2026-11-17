<script setup lang="ts">
import { computed, ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import * as api from '@/api'
import type { FirewallConfig, FirewallMode, ProxyListener, ProxyProtocol } from '@/types'
import CityRuleEditor from './CityRuleEditor.vue'

const props = defineProps<{ modelValue: ProxyListener[] }>()
const emit = defineEmits<{ 'update:modelValue': [ProxyListener[]] }>()

// GeoIP 数据库是否可用（用于国家规则提示）
const geoReady = ref(false)
// 当前加载的是否为城市级数据库（城市规则才生效）
const geoCityReady = ref(false)
onMounted(async () => {
  try {
    const st = await api.getGeoIPStatus()
    geoReady.value = !!st.loaded
    geoCityReady.value = !!st.city_ready
  } catch {
    geoReady.value = false
    geoCityReady.value = false
  }
})

// 常用国家选项（ISO 3166-1 alpha-2）
const countryOptions = [
  { label: '中国 (CN)', value: 'CN' },
  { label: '中国香港 (HK)', value: 'HK' },
  { label: '中国台湾 (TW)', value: 'TW' },
  { label: '日本 (JP)', value: 'JP' },
  { label: '韩国 (KR)', value: 'KR' },
  { label: '新加坡 (SG)', value: 'SG' },
  { label: '美国 (US)', value: 'US' },
  { label: '加拿大 (CA)', value: 'CA' },
  { label: '英国 (GB)', value: 'GB' },
  { label: '德国 (DE)', value: 'DE' },
  { label: '法国 (FR)', value: 'FR' },
  { label: '荷兰 (NL)', value: 'NL' },
  { label: '俄罗斯 (RU)', value: 'RU' },
  { label: '澳大利亚 (AU)', value: 'AU' },
  { label: '印度 (IN)', value: 'IN' },
  { label: '巴西 (BR)', value: 'BR' },
  { label: '越南 (VN)', value: 'VN' },
  { label: '泰国 (TH)', value: 'TH' },
  { label: '马来西亚 (MY)', value: 'MY' },
  { label: '印度尼西亚 (ID)', value: 'ID' },
  { label: '菲律宾 (PH)', value: 'PH' },
  { label: '土耳其 (TR)', value: 'TR' },
  { label: '乌克兰 (UA)', value: 'UA' },
  { label: '瑞典 (SE)', value: 'SE' },
  { label: '瑞士 (CH)', value: 'CH' },
  { label: '阿联酋 (AE)', value: 'AE' },
]

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

// ===== 防火墙配置 =====
function defaultFirewall(): FirewallConfig {
  return { enabled: true, mode: 'blacklist', ips: [], countries: [] }
}

function onFirewallToggle(idx: number, enabled: boolean) {
  updateListener(idx, { firewall: enabled ? defaultFirewall() : null })
}

function updateFirewall(idx: number, patch: Partial<FirewallConfig>) {
  const arr = [...listeners.value]
  const cur = arr[idx]
  const base = cur.firewall || defaultFirewall()
  arr[idx] = { ...cur, firewall: { ...base, ...patch } }
  listeners.value = arr
}

// IP 文本框（每行一条），按监听器 id 维护原始文本，避免编辑过程中被格式化
const ipTexts = ref<Record<string, string>>({})

function ipTextOf(pl: ProxyListener): string {
  if (ipTexts.value[pl.id] !== undefined) return ipTexts.value[pl.id]
  return (pl.firewall?.ips || []).join('\n')
}

function onIPTextInput(idx: number, id: string, text: string) {
  ipTexts.value = { ...ipTexts.value, [id]: text }
  const ips = text
    .split('\n')
    .map((s) => s.trim())
    .filter((s) => s.length > 0)
  updateFirewall(idx, { ips })
}

// 城市规则文本框已改为下拉选择（CityRuleEditor）

function setFirewallMode(idx: number, mode: FirewallMode) {
  updateFirewall(idx, { mode })
}

// ===== 代理监听端口连通性测试 =====
interface ProbeState {
  loading: boolean
  ip?: string
  elapsed?: number
  target?: string
  error?: string
}
const probeStates = ref<Record<string, ProbeState>>({})

async function handleTestProxy(pl: ProxyListener) {
  probeStates.value[pl.id] = { loading: true }
  try {
    const res = await api.testProxyListener({
      protocol: pl.protocol,
      port: pl.listen_port,
      username: pl.auth?.username || '',
      password: pl.auth?.password || '',
    })
    probeStates.value[pl.id] = {
      loading: false,
      ip: res.ip,
      elapsed: res.elapsed_ms,
      target: res.target,
    }
    ElMessage.success(`代理连通，出口 IP: ${res.ip}`)
  } catch (e: any) {
    probeStates.value[pl.id] = { loading: false, error: e?.message || '测试失败' }
    ElMessage.error(e?.message || '测试失败')
  }
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

      <div class="fw-row">
        <el-switch
          :model-value="!!pl.firewall"
          active-text="启用简易防火墙"
          @update:model-value="(v: string | number | boolean) => onFirewallToggle(idx, v as boolean)"
        />
        <template v-if="pl.firewall">
          <el-radio-group
            :model-value="pl.firewall.mode"
            size="small"
            @update:model-value="(v: string | number | boolean) => setFirewallMode(idx, v as FirewallMode)"
          >
            <el-radio-button value="blacklist">黑名单（命中拒绝）</el-radio-button>
            <el-radio-button value="whitelist">白名单（仅命中放行）</el-radio-button>
          </el-radio-group>
        </template>
      </div>

      <div v-if="pl.firewall" class="fw-detail">
        <el-row :gutter="12">
          <el-col :span="12">
            <div class="field-label">自定义 IP / IP 段（每行一条，支持 IPv4 与 IPv6）</div>
            <el-input
              type="textarea"
              :rows="4"
              :model-value="ipTextOf(pl)"
              placeholder="203.0.113.7&#10;10.0.0.0/8&#10;2001:db8::/32"
              @update:model-value="(v: string) => onIPTextInput(idx, pl.id, v)"
            />
          </el-col>
          <el-col :span="12">
            <div class="field-label">GeoIP 国家（可搜索/自定义输入国家代码）</div>
            <el-select
              multiple
              filterable
              allow-create
              default-first-option
              collapse-tags
              collapse-tags-tooltip
              :model-value="pl.firewall.countries || []"
              placeholder="选择或输入国家代码，如 CN、US"
              style="width: 100%"
              @update:model-value="(v: string[]) => updateFirewall(idx, { countries: v })"
            >
              <el-option
                v-for="c in countryOptions"
                :key="c.value"
                :label="c.label"
                :value="c.value"
              />
            </el-select>
            <div v-if="!geoReady" class="fw-tip">
              服务器未加载 GeoIP 数据库，国家规则不会生效；IP/IP 段规则不受影响。
            </div>
          </el-col>
        </el-row>

        <el-row :gutter="12" style="margin-top: 12px">
          <el-col :span="24">
            <div class="field-label">城市规则（下拉选择，选「全部（*）」表示通配）</div>
            <CityRuleEditor
              :model-value="pl.firewall.cities || []"
              :country-options="countryOptions"
              :city-ready="geoCityReady"
              @update:model-value="(v: string[]) => updateFirewall(idx, { cities: v })"
            />
          </el-col>
        </el-row>
      </div>

      <div class="probe-row">
        <el-button
          size="small"
          :loading="probeStates[pl.id]?.loading"
          @click="handleTestProxy(pl)"
        >
          测试代理端口
        </el-button>
        <template v-if="probeStates[pl.id] && !probeStates[pl.id].loading">
          <el-tag v-if="probeStates[pl.id].ip" type="success" size="small">
            出口 IP: {{ probeStates[pl.id].ip }}（{{ probeStates[pl.id].elapsed }}ms）
          </el-tag>
          <el-tag v-else type="danger" size="small" class="probe-error">
            {{ probeStates[pl.id].error }}
          </el-tag>
        </template>
        <span class="hint">经该监听端口访问 {{ probeStates[pl.id]?.target || 'https://api.ip.sb/ip' }}，用于验证隧道连通性（需先启动隧道）</span>
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
.probe-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  border-top: 1px dashed #3a3f4b;
  padding-top: 8px;
}
.fw-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  border-top: 1px dashed #3a3f4b;
  padding-top: 8px;
}
.fw-detail {
  background-color: #1b1e24;
  border: 1px dashed #3a3f4b;
  border-radius: 4px;
  padding: 10px;
}
.fw-tip {
  margin-top: 6px;
  font-size: 12px;
  color: #ebb563;
  line-height: 1.5;
}
.fw-hint {
  margin-top: 6px;
  font-size: 12px;
  color: #8a8d95;
  line-height: 1.5;
}
.probe-error {
  max-width: 460px;
  white-space: normal;
  height: auto;
}
.add-btn {
  border-style: dashed;
  width: 100%;
}
</style>
