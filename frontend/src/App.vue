<script setup lang="ts">
import { computed, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useTunnelStore } from '@/stores/tunnel'

const store = useTunnelStore()
const route = useRoute()

const isLoginPage = computed(() => route.path === '/login')

// 首次进入主界面时加载数据并订阅日志。
// 用 watch 而非 onMounted：若页面首次落在登录页（onMounted 被跳过），
// 登录成功跳回主界面后仍能正常加载，避免主界面空白。
// 仅在实际加载成功后标记 loaded：初始进入时路由守卫可能尚未重定向到登录页，
// 此时请求不带 Token 会失败（401），登录成功后需再次尝试加载。
let loaded = false
watch(
  () => route.path,
  async (path, prevPath) => {
    // 从登录页离开（重新登录成功）后重置加载标记并重新拉取，避免列表陈旧
    if (prevPath === '/login' && path !== '/login') loaded = false
    if (path === '/login' || loaded) return
    try {
      await store.loadConfigs()
      loaded = true
      // 仅在加载成功后订阅日志与隧道状态，避免未授权时建立无效 WebSocket
      store.setupLogListener()
      store.setupStatusListener()
    } catch {
      // 未授权等失败场景保持 loaded=false，待登录成功进入主界面后重试
    }
  },
  { immediate: true },
)
</script>

<template>
  <router-view v-if="isLoginPage" />
  <el-container v-else class="app-container">
    <el-aside width="210px" class="sidebar">
      <div class="logo">
        <el-icon :size="22"><Connection /></el-icon>
        <span>SSH Tunnel</span>
      </div>
      <el-menu
        :default-active="$route.path"
        router
        class="side-menu"
        background-color="#1a1d23"
        text-color="#a0a3ab"
        active-text-color="#409eff"
      >
        <el-menu-item index="/">
          <el-icon><Odometer /></el-icon>
          <span>仪表盘</span>
        </el-menu-item>
        <el-menu-item index="/status">
          <el-icon><Monitor /></el-icon>
          <span>节点状态</span>
        </el-menu-item>
        <el-menu-item index="/config">
          <el-icon><Setting /></el-icon>
          <span>新建隧道</span>
        </el-menu-item>
        <el-menu-item index="/logs">
          <el-icon><Document /></el-icon>
          <span>操作日志</span>
        </el-menu-item>
        <el-menu-item index="/settings">
          <el-icon><Tools /></el-icon>
          <span>系统设置</span>
        </el-menu-item>
      </el-menu>
    </el-aside>
    <el-container>
      <el-main class="main-content">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<style scoped>
.app-container {
  height: 100%;
}
.sidebar {
  background-color: #14161a;
  border-right: 1px solid #2a2d33;
}
.logo {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 18px 20px;
  font-size: 16px;
  font-weight: 600;
  color: #409eff;
}
.side-menu {
  border-right: none;
}
.main-content {
  background-color: #1a1d23;
  padding: 24px;
}
</style>
