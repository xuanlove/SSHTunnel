<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as api from '@/api'
import type { TunnelConfig } from '@/types'
import { formatBytes, formatTime } from '@/utils/format'
import { copyText } from '@/utils/clipboard'
import CertSummary from '@/components/CertSummary.vue'

const isWebMode = api.isWebMode

const portToCheck = ref(1080)
const portResult = ref<null | boolean>(null)
const certInfo = ref<{ certPEM: string; keyPEM: string } | null>(null)
const certMeta = ref<api.CertInfo | null>(null)
const versionText = ref('')

// ===== WEB 访问密码 =====
const authForm = ref({ enabled: false, username: '', password: '' })
const authSaving = ref(false)
const authLoaded = ref(false)
// 加载时访问密码是否已开启：用于区分「首次开启必须设置密码」与「留空表示不修改」
const authWasEnabled = ref(false)

onMounted(async () => {
  try {
    const v = await api.getVersion()
    versionText.value = v.version || ''
  } catch {
    // 版本获取失败时显示占位符
  }
  await loadGeoStatus()
  await loadWebTLS()
  await loadIPCert()
  await loadAlerts()
  if (!isWebMode) return
  try {
    const s = await api.getWebAuthSettings()
    authForm.value.enabled = s.enabled
    authWasEnabled.value = s.enabled
    authForm.value.username = s.username || 'admin'
    authLoaded.value = true
  } catch {
    // 加载失败时保持默认（不开启）
  }
})

async function handleSaveAuth() {
  if (authForm.value.enabled) {
    if (!authForm.value.username.trim()) {
      ElMessage.warning('请填写用户名')
      return
    }
    const pwd = authForm.value.password
    // 首次开启必须设置密码；已开启时留空表示沿用原密码（与后端语义一致）
    if (!pwd && !authWasEnabled.value) {
      ElMessage.warning('请设置访问密码（至少 6 位）')
      return
    }
    if (pwd && pwd.length < 6) {
      ElMessage.warning('密码长度至少 6 位')
      return
    }
  }
  authSaving.value = true
  try {
    const s = await api.updateWebAuthSettings({
      enabled: authForm.value.enabled,
      username: authForm.value.username.trim(),
      password: authForm.value.password,
    })
    authForm.value.enabled = s.enabled
    authWasEnabled.value = s.enabled
    authForm.value.username = s.username || authForm.value.username
    authForm.value.password = ''
    ElMessage.success(s.enabled ? '访问密码已开启' : '访问密码已关闭')
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  } finally {
    authSaving.value = false
  }
}

// ===== GeoIP 城市数据库 =====
const geoStatus = ref<api.GeoIPStatus | null>(null)
const geoDownloading = ref(false)
const geoSwitching = ref(false)

async function loadGeoStatus() {
  try {
    geoStatus.value = await api.getGeoIPStatus()
  } catch {
    // 忽略：状态不可用时不展示详情
  }
}

async function handleDownloadGeo() {
  geoDownloading.value = true
  try {
    geoStatus.value = await api.downloadGeoIPDatabase()
    ElMessage.success('城市数据库下载完成，可开启「启用城市级数据库」')
  } catch (e: any) {
    ElMessage.error(e?.message || '下载失败')
  } finally {
    geoDownloading.value = false
  }
}

async function handleToggleGeo(enabled: boolean) {
  geoSwitching.value = true
  try {
    geoStatus.value = await api.setGeoIPEnabled(enabled)
    ElMessage.success(enabled ? '已启用城市级数据库' : '已切换回内置 Country 数据库')
  } catch (e: any) {
    ElMessage.error(e?.message || '切换失败')
    await loadGeoStatus()
  } finally {
    geoSwitching.value = false
  }
}

function formatSize(bytes: number): string {
  if (!bytes) return '-'
  if (bytes > 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024).toFixed(0)} KB`
}

const geoCityDownloaded = computed(() => !!geoStatus.value?.city_db_exists)

const PRODUCTION_DIRECTORY = 'https://acme-v02.api.letsencrypt.org/directory'
const STAGING_DIRECTORY = 'https://acme-staging-v02.api.letsencrypt.org/directory'

// ===== IP 证书（Let's Encrypt 自动签发/续期）=====
const ipCert = ref<api.IPCERTSettings>({
  enabled: false,
  email: '',
  // 使用显式生产地址：el-option 的 value 不能为空字符串，否则会被当作占位而无法选中
  directory_url: PRODUCTION_DIRECTORY,
  ip: '',
  http_port: 80,
})
const ipCertStatus = ref<api.IPCERTStatus | null>(null)
const ipCertSaving = ref(false)
const ipCertIssuing = ref(false)

async function loadIPCert() {
  try {
    const st = await api.getIPCertSettings()
    ipCert.value = {
      ...st.settings,
      // 后端留空表示生产环境；界面用显式地址，避免空值选项无法选中
      directory_url: st.settings.directory_url || PRODUCTION_DIRECTORY,
      http_port: st.settings.http_port || 80,
    }
    ipCertStatus.value = st.status
  } catch {
    // 旧版本后端或未启用时忽略
  }
}

async function refreshIPCertStatus() {
  try {
    const st = await api.getIPCertSettings()
    ipCertStatus.value = st.status
  } catch {
    // 忽略
  }
}

async function handleSaveIPCert() {
  ipCertSaving.value = true
  try {
    const st = await api.saveIPCertSettings({ ...ipCert.value })
    ipCert.value = { ...st.settings, http_port: st.settings.http_port || 80 }
    ipCertStatus.value = st.status
    ElMessage.success(st.settings.enabled ? '已保存，正在自动签发（需要该 IP 的 80 端口公网可达）' : '已保存')
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  } finally {
    ipCertSaving.value = false
  }
}

async function handleIssueIPCert() {
  ipCertIssuing.value = true
  try {
    await api.issueIPCertNow()
    ElMessage.info('已开始签发，通常 10-60 秒完成，可稍后刷新状态')
    // 轮询几次状态，便于用户看到结果
    for (let i = 0; i < 12; i++) {
      await new Promise(r => setTimeout(r, 5000))
      await refreshIPCertStatus()
      const st = ipCertStatus.value
      if (st && !st.issuing && (st.not_after || st.last_error)) break
    }
  } catch (e: any) {
    ElMessage.error(e?.message || '签发请求失败')
  } finally {
    ipCertIssuing.value = false
  }
}

// ===== WEB 面板 HTTPS（SSL）=====
const webTls = ref({
  enabled: false,
  // 使用「系统设置 → IP 证书」中的证书：无需填写证书内容（需 IP 证书已启用并完成签发）
  useIPCert: false,
  certPem: '',
  keyPem: '',
  certFile: '',
  keyFile: '',
  active: false,
  deployToken: '',
  // 服务器是否已保存私钥（默认查询不再返回明文，仅展示占位）
  hasKey: false,
})
const webTlsSaving = ref(false)
// 「查看私钥」拉取中
const keyRevealing = ref(false)
// 已回显到输入框的私钥原文基线：保存时与输入框内容比对，仅变更才提交 key_pem
let revealedKeyPem = ''

const keyPlaceholder = computed(() =>
  webTls.value.hasKey
    ? '已保存私钥（不显示）。点击「查看私钥」回显，或直接粘贴新私钥进行替换'
    : '-----BEGIN PRIVATE KEY-----',
)

// WEB 面板证书摘要（使用者 CN / 到期时间）
const webCertMeta = ref<api.CertInfo | null>(null)
const webCertError = ref('')
let webCertTimer = 0

onUnmounted(() => {
  if (webCertTimer) window.clearTimeout(webCertTimer)
})

async function inspectWebCert(pem: string) {
  if (!pem.trim()) {
    webCertMeta.value = null
    webCertError.value = ''
    return
  }
  try {
    webCertMeta.value = await api.inspectCertificate(pem)
    webCertError.value = ''
  } catch (e: any) {
    webCertMeta.value = null
    webCertError.value = e?.message || '证书解析失败'
  }
}

watch(
  () => webTls.value.certPem,
  (pem) => {
    if (webCertTimer) window.clearTimeout(webCertTimer)
    webCertTimer = window.setTimeout(() => void inspectWebCert(pem), 400)
  },
)

async function loadWebTLS() {
  try {
    const s = await api.getWebTLSSettings()
    webTls.value.enabled = s.enabled
    webTls.value.useIPCert = !!s.use_ipcert
    webTls.value.active = s.active
    webTls.value.certFile = s.cert_file || ''
    webTls.value.keyFile = s.key_file || ''
    webTls.value.deployToken = s.deploy_token || ''
    webTls.value.certPem = s.cert_pem || ''
    // 默认查询不返回明文私钥：仅记录是否存在，避免把掩码占位串当作真私钥提交
    webTls.value.hasKey = !!s.has_key || !!s.key_masked
    webTls.value.keyPem = ''
    revealedKeyPem = ''
  } catch {
    // 忽略：仅 WEB 模式支持
  }
}

// 点击才以 reveal_key=1 拉取明文私钥并填入输入框
async function handleRevealKey() {
  keyRevealing.value = true
  try {
    const s = await api.getWebTLSSettings(true)
    webTls.value.keyPem = s.key_pem || ''
    revealedKeyPem = webTls.value.keyPem
  } catch (e: any) {
    ElMessage.error(e?.message || '私钥获取失败')
  } finally {
    keyRevealing.value = false
  }
}

async function handleSaveWebTLS() {
  const usingIPCert = webTls.value.useIPCert
  if (webTls.value.enabled && !usingIPCert && !webTls.value.certPem.trim() && !webTls.value.certFile) {
    ElMessage.warning('请填写证书内容，或开启「使用 IP 证书」')
    return
  }
  webTlsSaving.value = true
  try {
    const keyTrim = webTls.value.keyPem.trim()
    // 仅当用户实际修改过私钥（新粘贴或改动回显内容）才提交，空值/未变表示保持不变
    const keyChanged = !!keyTrim && keyTrim !== revealedKeyPem.trim()
    const res = await api.updateWebTLSSettings({
      enabled: webTls.value.enabled,
      use_ipcert: usingIPCert,
      cert_pem: usingIPCert ? undefined : (webTls.value.certPem.trim() || undefined),
      key_pem: usingIPCert ? undefined : (keyChanged ? keyTrim : undefined),
    })
    webTls.value.active = res.active
    webTls.value.certFile = res.cert_file || ''
    webTls.value.keyFile = res.key_file || ''
    if (typeof res.has_key === 'boolean') webTls.value.hasKey = res.has_key
    webTls.value.keyPem = ''
    revealedKeyPem = ''
    ElMessage.success(
      res.active
        ? '已启用 HTTPS，监听正在重启，请改用 https:// 访问'
        : '已保存，监听正在重启',
    )
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  } finally {
    webTlsSaving.value = false
  }
}

async function handleRegenerateToken() {
  try {
    webTls.value.deployToken = await api.regenerateDeployToken()
    ElMessage.success('部署令牌已重置，旧令牌立即失效')
  } catch (e: any) {
    ElMessage.error(e?.message || '重置失败')
  }
}

async function handleCopyToken() {
  const ok = await copyText(webTls.value.deployToken)
  if (ok) ElMessage.success('已复制')
  else ElMessage.warning('复制失败，请手动选择复制')
}

async function handleCheckPort() {
  try {
    portResult.value = await api.checkPort(portToCheck.value)
  } catch (e: any) {
    ElMessage.error(e?.message || '检测失败')
  }
}

async function handleGenCert() {
  try {
    certInfo.value = await api.generateSelfSignedCert()
    certMeta.value = null
    if (certInfo.value.certPEM) {
      try {
        certMeta.value = await api.inspectCertificate(certInfo.value.certPEM)
      } catch {
        // 摘要失败不影响证书生成
      }
    }
    ElMessage.success('证书已生成')
  } catch (e: any) {
    ElMessage.error(e?.message || '生成失败')
  }
}

// ===== 告警 =====
const alertRules = ref<api.AlertRule[]>([])
const alertHistory = ref<api.AlertHistory[]>([])
const alertWebhook = ref<api.AlertWebhook>({ enabled: false, url: '', format: 'generic' })
const tunnels = ref<TunnelConfig[]>([])
const ruleDialog = ref(false)
const ruleSaving = ref(false)
const ruleForm = ref<api.AlertRule>({
  id: '',
  name: '',
  type: 'tunnel_down',
  enabled: true,
  tunnel_id: '',
  threshold: 0,
  duration_seconds: 60,
  cooldown_seconds: 300,
})

const trafficMB = computed({
  get: () => {
    const v = ruleForm.value.threshold || 0
    return Math.round((v / (1024 * 1024)) * 100) / 100
  },
  set: (v: number) => {
    ruleForm.value.threshold = Math.round(v * 1024 * 1024)
  },
})

async function loadAlerts() {
  try {
    const [rules, hook, hist] = await Promise.all([
      api.getAlertRules(),
      api.getAlertWebhook(),
      api.getAlertHistory(20),
    ])
    alertRules.value = rules
    alertWebhook.value = hook
    alertHistory.value = hist
    try {
      tunnels.value = await api.listConfigs()
    } catch {
      // 隧道列表加载失败时规则仍可查看
    }
  } catch {
    // 告警数据加载失败时保持空列表
  }
}

function openRuleDialog(rule?: api.AlertRule) {
  if (rule) {
    ruleForm.value = { ...rule }
  } else {
    ruleForm.value = {
      id: '',
      name: '',
      type: 'tunnel_down',
      enabled: true,
      tunnel_id: tunnels.value[0]?.id || '',
      threshold: 0,
      duration_seconds: 60,
      cooldown_seconds: 300,
    }
  }
  ruleDialog.value = true
}

async function handleSaveRule() {
  ruleSaving.value = true
  try {
    const saved = await api.saveAlertRule(ruleForm.value)
    const idx = alertRules.value.findIndex(r => r.id === saved.id)
    if (idx >= 0) alertRules.value[idx] = saved
    else alertRules.value.push(saved)
    ruleDialog.value = false
    ElMessage.success('告警规则已保存')
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  } finally {
    ruleSaving.value = false
  }
}

async function handleDeleteRule(rule: api.AlertRule) {
  try {
    await ElMessageBox.confirm(`确定删除规则「${rule.name}」？`, '提示', { type: 'warning' })
  } catch {
    return
  }
  try {
    await api.deleteAlertRule(rule.id)
    alertRules.value = alertRules.value.filter(r => r.id !== rule.id)
    ElMessage.success('规则已删除')
  } catch (e: any) {
    ElMessage.error(e?.message || '删除失败')
  }
}

async function handleSaveWebhook() {
  try {
    alertWebhook.value = await api.saveAlertWebhook(alertWebhook.value)
    ElMessage.success('通知配置已保存')
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  }
}

async function handleTestWebhook() {
  try {
    await api.testAlertWebhook()
    ElMessage.success('测试通知已发送')
  } catch (e: any) {
    ElMessage.error(e?.message || '测试通知发送失败')
  }
}

function alertTypeText(t: string): string {
  return { tunnel_down: '隧道断开', conn_threshold: '连接数', traffic_threshold: '流量' }[t] || t
}

function tunnelName(id?: string): string {
  if (!id) return '全局'
  return tunnels.value.find(t => t.id === id)?.name || id
}

</script>

<template>
  <div class="settings-page">
    <h2>系统设置</h2>

    <el-card class="panel">
      <template #header><span>关于</span></template>
      <div class="info-row"><span class="label">应用名称</span><span>SSHTunnel</span></div>
      <div class="info-row"><span class="label">版本</span><span>{{ versionText || '-' }}</span></div>
      <div class="info-row"><span class="label">技术栈</span><span>Wails v2 + Vue 3 + Go</span></div>
      <div class="info-row"><span class="label">功能</span><span>SSH 多跳隧道、本地端口转发、HTTP/HTTPS/SOCKS4/SOCKS5 代理</span></div>
    </el-card>

    <el-card class="panel">
      <template #header>
        <div class="panel-header">
          <span>告警</span>
          <el-button size="small" type="primary" @click="openRuleDialog()">新增规则</el-button>
        </div>
      </template>

      <el-table :data="alertRules" size="small" class="dark-table" empty-text="暂无告警规则">
        <el-table-column prop="name" label="名称" min-width="120" show-overflow-tooltip />
        <el-table-column label="类型" width="100">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ alertTypeText(row.type) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="范围" min-width="120" show-overflow-tooltip>
          <template #default="{ row }">{{ tunnelName(row.tunnel_id) }}</template>
        </el-table-column>
        <el-table-column label="阈值" width="120">
          <template #default="{ row }">
            {{ row.type === 'traffic_threshold' ? formatBytes(row.threshold) : row.type === 'conn_threshold' ? row.threshold : '-' }}
          </template>
        </el-table-column>
        <el-table-column label="持续/冷却" width="110">
          <template #default="{ row }">{{ row.duration_seconds }}s / {{ row.cooldown_seconds }}s</template>
        </el-table-column>
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '启用' : '停用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="130">
          <template #default="{ row }">
            <el-button size="small" text @click="openRuleDialog(row)">编辑</el-button>
            <el-button size="small" text type="danger" @click="handleDeleteRule(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="webhook-form">
        <span class="form-title">Webhook 通知</span>
        <el-switch v-model="alertWebhook.enabled" active-text="启用" />
        <el-select v-model="alertWebhook.format" style="width: 130px">
          <el-option label="通用 JSON" value="generic" />
          <el-option label="企业微信" value="wecom" />
          <el-option label="飞书" value="feishu" />
        </el-select>
        <el-input v-model="alertWebhook.url" placeholder="https://..." class="webhook-url" />
        <el-button @click="handleSaveWebhook">保存</el-button>
        <el-button :disabled="!alertWebhook.enabled" @click="handleTestWebhook">测试</el-button>
      </div>

      <div class="form-title history-title">最近告警</div>
      <el-table :data="alertHistory" size="small" class="dark-table" empty-text="暂无告警记录">
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ formatTime(row.time) }}</template>
        </el-table-column>
        <el-table-column prop="rule_name" label="规则" width="130" show-overflow-tooltip />
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.status === 'firing' ? 'danger' : 'success'">
              {{ row.status === 'firing' ? '告警' : '恢复' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="message" label="内容" min-width="240" show-overflow-tooltip />
      </el-table>
    </el-card>

    <el-dialog v-model="ruleDialog" :title="ruleForm.id ? '编辑告警规则' : '新增告警规则'" width="520px">
      <el-form label-width="96px">
        <el-form-item label="名称">
          <el-input v-model="ruleForm.name" placeholder="留空使用默认名称" />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="ruleForm.type" style="width: 100%">
            <el-option label="隧道断开" value="tunnel_down" />
            <el-option label="连接数阈值" value="conn_threshold" />
            <el-option label="流量阈值（5 分钟）" value="traffic_threshold" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="ruleForm.type === 'tunnel_down'" label="隧道">
          <el-select v-model="ruleForm.tunnel_id" style="width: 100%">
            <el-option v-for="t in tunnels" :key="t.id" :label="t.name" :value="t.id" />
          </el-select>
        </el-form-item>
        <el-form-item v-else label="范围">
          <el-select v-model="ruleForm.tunnel_id" style="width: 100%">
            <el-option label="全局" value="" />
            <el-option v-for="t in tunnels" :key="t.id" :label="t.name" :value="t.id" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="ruleForm.type === 'conn_threshold'" label="连接数">
          <el-input-number v-model="ruleForm.threshold" :min="1" />
        </el-form-item>
        <el-form-item v-if="ruleForm.type === 'traffic_threshold'" label="流量 (MB)">
          <el-input-number v-model="trafficMB" :min="0.1" :step="1" />
        </el-form-item>
        <el-form-item label="持续 (秒)">
          <el-input-number v-model="ruleForm.duration_seconds" :min="0" />
        </el-form-item>
        <el-form-item label="冷却 (秒)">
          <el-input-number v-model="ruleForm.cooldown_seconds" :min="0" />
          <div class="form-tip">为 0 时持续触发期间仅通知一次，恢复后再次触发才重新告警</div>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="ruleForm.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="ruleDialog = false">取消</el-button>
        <el-button type="primary" :loading="ruleSaving" @click="handleSaveRule">保存</el-button>
      </template>
    </el-dialog>

    <el-card v-if="isWebMode" class="panel">
      <template #header><span>WEB 访问密码</span></template>
      <p class="desc">
        开启后访问 WEB 面板需要登录验证（默认不开启）。设置保存在服务器本地 settings.json，服务重启后仍然生效。
      </p>
      <div class="auth-form">
        <el-switch v-model="authForm.enabled" active-text="启用访问密码" :disabled="!authLoaded" />
        <template v-if="authForm.enabled">
          <el-input
            v-model="authForm.username"
            placeholder="用户名"
            style="width: 160px"
            :disabled="!authLoaded"
          />
          <el-input
            v-model="authForm.password"
            type="password"
            show-password
            placeholder="密码（至少 6 位，留空表示不修改）"
            style="width: 300px"
            :disabled="!authLoaded"
          />
        </template>
        <el-button type="primary" :loading="authSaving" :disabled="!authLoaded" @click="handleSaveAuth">
          保存
        </el-button>
      </div>
    </el-card>

    <el-card class="panel">
      <template #header><span>GeoIP 城市级数据库</span></template>
      <p class="desc">
        内置数据库精确到国家（约 3.3MB）；如需在代理防火墙中按「城市」过滤，请下载城市级数据库（Merged-IP，约 93MB，多镜像自动测速选择最快下载）。
      </p>
      <div class="info-row">
        <span class="label">当前数据库</span>
        <span>{{ geoStatus?.type === 'City' ? '城市级（Merged-IP）' : '内置国家库（GeoLite2-Country）' }}</span>
      </div>
      <div class="info-row">
        <span class="label">数据来源</span>
        <span>{{ geoStatus?.source === 'builtin' ? '内置' : (geoStatus?.source || '-') }}</span>
      </div>
      <div class="info-row">
        <span class="label">大小</span>
        <span>{{ formatSize(geoStatus?.size_bytes || 0) }}</span>
      </div>
      <div class="info-row" v-if="geoStatus?.updated_at">
        <span class="label">更新时间</span>
        <span>{{ geoStatus.updated_at }}</span>
      </div>
      <div class="geo-actions">
        <el-button type="primary" :loading="geoDownloading" @click="handleDownloadGeo">
          {{ geoDownloading ? '下载中…（约 93MB，请耐心等待）' : (geoCityDownloaded ? '更新城市数据库' : '下载城市数据库') }}
        </el-button>
        <el-switch
          :model-value="!!geoStatus?.enabled"
          active-text="启用城市级数据库"
          :disabled="!geoCityDownloaded || geoSwitching"
          @update:model-value="(v: string | number | boolean) => handleToggleGeo(!!v)"
        />
      </div>
      <div class="fw-tip" v-if="!geoCityDownloaded">
        尚未下载城市数据库，启用开关将在下载完成后可用。
      </div>
      <div class="fw-tip" v-else-if="!geoStatus?.enabled">
        已下载但未启用，当前仍使用内置国家库；城市规则不会生效。
      </div>
    </el-card>

    <el-card v-if="isWebMode" class="panel">
      <template #header><span>WEB 面板 HTTPS（SSL）</span></template>
      <p class="desc">
        开启后 WEB 面板以 HTTPS 提供服务。保存后监听会立即重启，请改用 https:// 访问（自签证书浏览器会提示不安全）。
      </p>
      <div class="auth-form">
        <el-switch v-model="webTls.enabled" active-text="启用 HTTPS" />
        <el-switch v-model="webTls.useIPCert" active-text="使用 IP 证书" />
        <el-tag v-if="webTls.active" type="success" size="small">HTTPS 已生效</el-tag>
      </div>
      <div class="fw-tip" v-if="webTls.useIPCert">
        使用「系统设置 → IP 证书」中的证书，需已启用并完成签发；续期后自动生效，无需重启监听。
        由 certd 等工具部署证书后，此开关会自动关闭（以部署的证书为准）。
      </div>
      <div class="auth-form" v-if="webTls.useIPCert">
        <el-tag v-if="ipCertStatus?.issuing" type="warning" size="small">IP 证书签发中…</el-tag>
        <el-tag v-else-if="ipCertStatus?.not_after" type="success" size="small">IP 证书已签发</el-tag>
        <el-tag v-else type="danger" size="small">IP 证书未签发</el-tag>
        <span class="key-hint" v-if="ipCertStatus?.ip">IP: {{ ipCertStatus.ip }}</span>
        <span class="key-hint" v-if="ipCertStatus?.not_after">有效期至 {{ formatTime(ipCertStatus.not_after) }}</span>
      </div>
      <template v-if="!webTls.useIPCert">
        <div class="field-label">证书内容（PEM，含完整证书链）</div>
        <el-input
          v-model="webTls.certPem"
          type="textarea"
          :rows="5"
          placeholder="-----BEGIN CERTIFICATE-----"
        />
        <CertSummary :info="webCertMeta" :error="webCertError" />
        <div class="field-label" style="margin-top: 10px">私钥内容（PEM）</div>
        <el-input
          v-model="webTls.keyPem"
          type="textarea"
          :rows="4"
          :placeholder="keyPlaceholder"
        />
        <div class="key-actions" v-if="webTls.hasKey">
          <el-button size="small" :loading="keyRevealing" @click="handleRevealKey">查看私钥</el-button>
          <span class="key-hint">私钥已保存在服务器，默认不显示；仅当修改后保存才会重新提交</span>
        </div>
        <div class="info-row" v-if="webTls.certFile">
          <span class="label">证书文件</span><span>{{ webTls.certFile }}</span>
        </div>
        <div class="info-row" v-if="webTls.keyFile">
          <span class="label">私钥文件</span><span>{{ webTls.keyFile }}</span>
        </div>
      </template>
      <div class="geo-actions">
        <el-button type="primary" :loading="webTlsSaving" @click="handleSaveWebTLS">保存并应用</el-button>
      </div>

      <el-divider />
      <div class="field-label">certd 部署 Token（外部工具自动部署证书用）</div>
      <div class="auth-form">
        <el-input :model-value="webTls.deployToken" readonly style="width: 420px" />
        <el-button size="small" @click="handleCopyToken">复制</el-button>
        <el-button size="small" type="danger" plain @click="handleRegenerateToken">重置</el-button>
      </div>
      <div class="fw-tip">
        certd 等工具可通过 API 部署证书（请求头 <code>X-API-Token: &lt;上方 Token&gt;</code>）：<br />
        <code>GET /api/cert/targets</code> 列出可部署目标（WEB 面板与所有 HTTPS 监听器）<br />
        <code>GET /api/cert/read?target=web</code> 读取证书；隧道用 <code>?target=tunnel&amp;tunnel_id=..&amp;listener_id=..</code><br />
        <code>POST /api/cert/deploy</code> 部署证书，body 示例：<br />
        <code>{"target":"tunnel","tunnel_id":"...","listener_id":"...","cert_pem":"...","key_pem":"...","restart":true}</code><br />
        <code>target=web</code> 时写入 WEB 面板证书并重启监听；<code>target=tunnel</code> 时写入 HTTPS 监听器证书并重启对应隧道。
      </div>
    </el-card>

    <el-card class="panel">
      <template #header><span>IP 证书（Let's Encrypt 自动签发 / 续期）</span></template>
      <p class="desc">
        Let's Encrypt 的 IP 证书为 6 天短期证书，到期前自动续期。签发需要该公网 IP 的
        <b>80 端口从互联网可达</b>（http-01 验证）。启用后，HTTPS 代理监听器的证书留空时默认使用此证书。
      </p>
      <div class="auth-form">
        <el-switch v-model="ipCert.enabled" active-text="启用 IP 证书" />
        <el-tag v-if="ipCertStatus?.issuing" type="warning" size="small">签发中…</el-tag>
        <el-tag v-else-if="ipCertStatus?.not_after" type="success" size="small">已签发</el-tag>
      </div>
      <el-row :gutter="12">
        <el-col :span="12">
          <div class="field-label">公网 IP（留空自动探测）</div>
          <el-input v-model="ipCert.ip" placeholder="例如 203.0.113.10" />
        </el-col>
        <el-col :span="6">
          <div class="field-label">http-01 端口</div>
          <el-input-number v-model="ipCert.http_port" :min="1" :max="65535" controls-position="right" style="width: 100%" />
        </el-col>
        <el-col :span="6">
          <div class="field-label">ACME 环境</div>
          <el-select v-model="ipCert.directory_url" style="width: 100%">
            <el-option label="生产（Let's Encrypt）" :value="PRODUCTION_DIRECTORY" />
            <el-option label="测试（Staging，不受信任）" :value="STAGING_DIRECTORY" />
          </el-select>
        </el-col>
      </el-row>
      <div class="field-label" style="margin-top: 10px">账户邮箱（可选，用于到期提醒）</div>
      <el-input v-model="ipCert.email" placeholder="you@example.com" style="width: 320px" />
      <div class="info-row" v-if="ipCertStatus?.ip">
        <span class="label">证书 IP</span><span>{{ ipCertStatus.ip }}</span>
      </div>
      <div class="info-row" v-if="ipCertStatus?.not_after">
        <span class="label">有效期至</span><span>{{ formatTime(ipCertStatus.not_after) }}</span>
      </div>
      <div class="info-row" v-if="ipCertStatus?.renew_at">
        <span class="label">下次续期</span><span>{{ formatTime(ipCertStatus.renew_at) }}</span>
      </div>
      <div class="fw-tip" v-if="ipCertStatus?.last_error" style="color: #f56c6c">
        最近错误：{{ ipCertStatus.last_error }}
      </div>
      <div class="geo-actions" style="margin-top: 10px">
        <el-button type="primary" :loading="ipCertSaving" @click="handleSaveIPCert">保存</el-button>
        <el-button :loading="ipCertIssuing" @click="handleIssueIPCert">立即签发 / 续期</el-button>
      </div>
    </el-card>

    <el-card class="panel">
      <template #header><span>HTTPS 代理证书</span></template>
      <p class="desc">为 HTTPS 代理生成自签名证书（ECDSA，10 年有效期）。隧道 HTTPS 监听器采用 PEM 文本方式保存证书，可在监听器编辑页直接粘贴，或点击「生成自签证书」自动填入。</p>
      <el-button type="primary" @click="handleGenCert">生成自签证书</el-button>
      <template v-if="certInfo">
        <CertSummary :info="certMeta" />
        <div class="field-label">证书内容（PEM）</div>
        <el-input type="textarea" :rows="4" readonly :model-value="certInfo.certPEM" />
        <div class="field-label">私钥内容（PEM）</div>
        <el-input type="textarea" :rows="3" readonly :model-value="certInfo.keyPEM" />
        <div class="fw-tip">certd 等工具也可通过部署 API 自动写入 HTTPS 监听器的证书内容。</div>
      </template>
    </el-card>

    <el-card class="panel">
      <template #header><span>端口检测</span></template>
      <div class="port-check">
        <el-input-number v-model="portToCheck" :min="1" :max="65535" />
        <el-button @click="handleCheckPort">检测</el-button>
        <el-tag v-if="portResult === true" type="success">端口可用</el-tag>
        <el-tag v-else-if="portResult === false" type="danger">端口已被占用</el-tag>
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.settings-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.settings-page h2 {
  font-size: 18px;
  color: #f5f6f7;
}
.panel {
  background-color: #252830;
  border: 1px solid #3a3f4b;
}
.info-row {
  display: flex;
  padding: 6px 0;
  color: #e0e3e8;
}
.info-row .label {
  width: 100px;
  color: #8a8d95;
}
.desc {
  color: #a0a3ab;
  font-size: 13px;
  margin-bottom: 12px;
}
.port-check {
  display: flex;
  align-items: center;
  gap: 12px;
}
.auth-form {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.geo-actions {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
  margin-top: 12px;
}
.fw-tip {
  margin-top: 8px;
  font-size: 12px;
  color: #ebb563;
  line-height: 1.5;
}
.field-label {
  font-size: 12px;
  color: #8a8d95;
  margin: 10px 0 4px;
}
.key-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 6px;
}
.key-hint {
  font-size: 12px;
  color: #8a8d95;
}
.fw-tip code {
  color: #e0e3e8;
  background-color: #14161a;
  padding: 1px 4px;
  border-radius: 3px;
}
.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
}
.webhook-form {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 14px;
}
.form-title {
  font-size: 13px;
  font-weight: 600;
  color: #e0e3e8;
}
.history-title {
  margin-top: 16px;
  margin-bottom: 8px;
}
.webhook-url {
  flex: 1;
  min-width: 220px;
}
.form-tip {
  width: 100%;
  margin-top: 4px;
  font-size: 12px;
  color: #8a8d95;
  line-height: 1.4;
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
