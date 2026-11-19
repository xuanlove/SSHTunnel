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
      path: '/status',
      name: 'status',
      component: () => import('@/views/Status.vue'),
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
router.beforeEach(async (to, from, next) => {
  // 桌面端无需登录
  if (!isWebMode) {
    next()
    return
  }

  // 每次导航都查询最新鉴权状态（本机请求，开销可忽略）。
  // 若缓存该结果，在「系统设置」中开启/关闭访问密码后会与后端不一致，
  // 导致误拦截或误放行（随后被 401 踢回登录页），表现为页面来回跳转。
  let authRequired = false
  try {
    const status = await checkAuthStatus()
    authRequired = status.auth_enabled
  } catch {
    // 查询失败时保守处理：按需要登录处理（已持有 token 仍可放行）
    authRequired = true
  }

  // 登录页：已登录则直接进入主界面，避免停留在登录页
  if (to.path === '/login') {
    if (authRequired && getToken()) {
      next('/')
      return
    }
    next()
    return
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

// 懒加载 chunk 失败（典型场景：升级后浏览器仍引用旧 hash 的文件）时，
// 路由导航会被中止，表现为「点击菜单没有任何反应」。此处强制整页刷新拉取新资源。
const RELOAD_FLAG = 'sshtunnel_chunk_reload'

router.onError((err: unknown) => {
  const msg = String((err as any)?.message || err || '')
  if (/dynamically imported module|module script|Loading chunk|Importing a module/i.test(msg)) {
    let reloaded = false
    try {
      reloaded = sessionStorage.getItem(RELOAD_FLAG) === '1'
      if (!reloaded) sessionStorage.setItem(RELOAD_FLAG, '1')
    } catch {
      // 忽略隐私模式下的存储异常
    }
    if (!reloaded) {
      window.location.reload()
    }
  }
})

// 导航成功说明资源正常，清除刷新标记
router.afterEach(() => {
  try {
    sessionStorage.removeItem(RELOAD_FLAG)
  } catch {
    // 忽略
  }
})

export default router
