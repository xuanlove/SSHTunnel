import { createRouter, createWebHashHistory } from 'vue-router'
import { checkAuthStatus, isWebMode, getToken } from '@/api'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('@/views/Login.vue'),
      meta: { public: true },
    },
    {
      path: '/',
      name: 'dashboard',
      component: () => import('@/views/Dashboard.vue'),
    },
    {
      path: '/config/:id?',
      name: 'config',
      component: () => import('@/views/ConfigEdit.vue'),
    },
    {
      path: '/logs',
      name: 'logs',
      component: () => import('@/views/Logs.vue'),
    },
    {
      path: '/settings',
      name: 'settings',
      component: () => import('@/views/Settings.vue'),
    },
  ],
})

// 路由守卫：WEB 模式下根据鉴权状态决定是否需要登录
let authChecked = false
let authRequired = false

router.beforeEach(async (to, from, next) => {
  // 桌面端无需登录
  if (!isWebMode) {
    next()
    return
  }

  // 登录页直接放行
  if (to.path === '/login') {
    next()
    return
  }

  // 首次访问时查询鉴权状态
  if (!authChecked) {
    try {
      const status = await checkAuthStatus()
      authRequired = status.auth_enabled
      authChecked = true
    } catch (e) {
      // 查询失败默认需要登录
      authRequired = true
      authChecked = true
    }
  }

  // 无密码模式直接放行
  if (!authRequired) {
    next()
    return
  }

  // 密码模式：检查 token
  const token = getToken()
  if (!token) {
    next('/login')
    return
  }
  next()
})

export default router
