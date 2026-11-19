<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { ElMessage } from 'element-plus'
import * as api from '@/api'
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

onMounted(async () => {
  try {
    const v = await api.getVersion()
    versionText.value = v.version || ''
  } catch {
    // 版本获取失败时显示占位符
  }
  await loadGeoStatus()
  await loadWebTLS()
  if (!isWebMode) return
  try {
    const s = await api.getWebAuthSettings()
    authForm.value.enabled = s.enabled
    authForm.value.username = s.username || 'admin'
    authLoaded.value = true
  } catch {
    // 加载失败时保持默认（不开启）
  }
})

async function handleSaveAuth() {
  if (authForm.value.enabled && !authForm.value.username.trim()) {
    ElMessage.warning('请填写用户名')
    return
  }
  authSaving.value = true
  try {
    const s = await api.updateWebAuthSettings({
      enabled: authForm.value.enabled,
      username: authForm.value.username.trim(),
      password: authForm.value.password,
    })
    authForm.value.enabled = s.enabled
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

// ===== WEB 面板 HTTPS（SSL）=====
const webTls = ref({
  enabled: false,
  certPem: '',
  keyPem: '',
  certFile: '',
  keyFile: '',
  active: false,
  deployToken: '',
})
const webTlsSaving = ref(false)

// WEB 面板证书摘要（使用者 CN / 到期时间）
const webCertMeta = ref<api.CertInfo | null>(null)
const webCertError = ref('')
let webCertTimer = 0

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
    webTls.value.active = s.active
    webTls.value.certFile = s.cert_file || ''
    webTls.value.keyFile = s.key_file || ''
    webTls.value.deployToken = s.deploy_token || ''
    webTls.value.certPem = s.cert_pem || ''
    webTls.value.keyPem = s.key_pem || ''
  } catch {
    // 忽略：仅 WEB 模式支持
  }
}

async function handleSaveWebTLS() {
  if (webTls.value.enabled && !webTls.value.certPem.trim() && !webTls.value.certFile) {
    ElMessage.warning('请填写证书内容，或先通过 certd 部署证书')
    return
  }
  webTlsSaving.value = true
  try {
    const res = await api.updateWebTLSSettings({
      enabled: webTls.value.enabled,
      cert_pem: webTls.value.certPem.trim() || undefined,
      key_pem: webTls.value.keyPem.trim() || undefined,
    })
    webTls.value.active = res.active
    webTls.value.certFile = res.cert_file || ''
    webTls.value.keyFile = res.key_file || ''
    webTls.value.keyPem = ''
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
  try {
    await navigator.clipboard.writeText(webTls.value.deployToken)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败，请手动选择复制')
  }
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
        <el-tag v-if="webTls.active" type="success" size="small">HTTPS 已生效</el-tag>
      </div>
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
        placeholder="-----BEGIN PRIVATE KEY-----"
      />
      <div class="info-row" v-if="webTls.certFile">
        <span class="label">证书文件</span><span>{{ webTls.certFile }}</span>
      </div>
      <div class="info-row" v-if="webTls.keyFile">
        <span class="label">私钥文件</span><span>{{ webTls.keyFile }}</span>
      </div>
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
.fw-tip code {
  color: #e0e3e8;
  background-color: #14161a;
  padding: 1px 4px;
  border-radius: 3px;
}
</style>
