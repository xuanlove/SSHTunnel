<script setup lang="ts">
import { onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useTunnelStore } from '@/stores/tunnel'

const store = useTunnelStore()
const route = useRoute()

const isLoginPage = computed(() => route.path === '/login')

onMounted(async () => {
  if (isLoginPage.value) return
  await store.loadConfigs()
  store.setupLogListener()
})
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
