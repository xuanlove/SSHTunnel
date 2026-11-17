<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import * as api from '@/api'

const isWebMode = api.isWebMode

const portToCheck = ref(1080)
const portResult = ref<null | boolean>(null)
const certInfo = ref<{ certPath: string; keyPath: string } | null>(null)

// ===== WEB 访问密码 =====
const authForm = ref({ enabled: false, username: '', password: '' })
const authSaving = ref(false)
const authLoaded = ref(false)

onMounted(async () => {
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
      <div class="info-row"><span class="label">版本</span><span>v1.1.0</span></div>
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
      <template #header><span>HTTPS 代理证书</span></template>
      <p class="desc">为 HTTPS 代理生成自签名证书（ECDSA，10 年有效期）。生成后可在 HTTPS 监听器配置中引用。</p>
      <el-button type="primary" @click="handleGenCert">生成自签证书</el-button>
      <el-alert
        v-if="certInfo"
        type="success"
        :closable="false"
        style="margin-top: 12px"
      >
        <div>证书文件：{{ certInfo.certPath }}</div>
        <div>私钥文件：{{ certInfo.keyPath }}</div>
      </el-alert>
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
</style>
