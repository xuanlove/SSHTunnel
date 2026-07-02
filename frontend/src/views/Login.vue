<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { login, setToken } from '@/api'

const router = useRouter()
const username = ref('')
const password = ref('')
const loading = ref(false)

async function handleLogin() {
  if (!username.value || !password.value) {
    ElMessage.warning('请输入用户名和密码')
    return
  }
  loading.value = true
  try {
    const token = await login(username.value, password.value)
    setToken(token)
    ElMessage.success('登录成功')
    router.push('/')
  } catch (e: any) {
    ElMessage.error(e?.message || '登录失败')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-title">SSH Tunnel Manager</div>
      <div class="login-subtitle">WEB 控制面板</div>
      <el-form @submit.prevent="handleLogin" class="login-form">
        <el-form-item>
          <el-input
            v-model="username"
            placeholder="用户名"
            size="large"
            @keyup.enter="handleLogin"
          >
            <template #prefix>
              <span class="input-icon">👤</span>
            </template>
          </el-input>
        </el-form-item>
        <el-form-item>
          <el-input
            v-model="password"
            type="password"
            show-password
            placeholder="密码"
            size="large"
            @keyup.enter="handleLogin"
          >
            <template #prefix>
              <span class="input-icon">🔒</span>
            </template>
          </el-input>
        </el-form-item>
        <el-button
          type="primary"
          size="large"
          :loading="loading"
          @click="handleLogin"
          class="login-btn"
        >
          登 录
        </el-button>
      </el-form>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, #14161a 0%, #1a1d23 100%);
}

.login-card {
  width: 380px;
  padding: 40px 32px;
  background: #252830;
  border-radius: 12px;
  border: 1px solid #3a3f4b;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.4);
}

.login-title {
  font-size: 24px;
  font-weight: 600;
  color: #f5f6f7;
  text-align: center;
  margin-bottom: 4px;
}

.login-subtitle {
  font-size: 13px;
  color: #8a8d95;
  text-align: center;
  margin-bottom: 32px;
}

.login-form {
  margin-top: 8px;
}

.login-btn {
  width: 100%;
  margin-top: 8px;
}

.input-icon {
  font-size: 16px;
  opacity: 0.7;
}
</style>
