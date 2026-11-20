import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import Status from './Status.vue'
import * as api from '@/api'
import type { NodeStatus } from '@/types'
import type { AlertHistory } from '@/api'

// mock @/api：Status.vue 依赖的 5 个异步方法（其余导出透传原模块）
vi.mock('@/api', async (importOriginal) => {
  const mod = await importOriginal<typeof import('@/api')>()
  return {
    ...mod,
    getNodeStatus: vi.fn(),
    getAlertHistory: vi.fn().mockResolvedValue([]),
    searchAccessHistory: vi.fn().mockResolvedValue([]),
    exportAccessHistoryCSV: vi.fn().mockResolvedValue('time,tunnel'),
    getMetrics: vi.fn().mockResolvedValue([]),
  }
})

function makeStatus(over: Partial<NodeStatus> = {}): NodeStatus {
  return {
    version: 'v1.8.0',
    started_at: '2026-11-21T00:00:00Z',
    uptime_seconds: 3600,
    tunnel_total: 2,
    tunnel_running: 1,
    listener_count: 1,
    forward_count: 1,
    active_conns: 2,
    total_conns: 10,
    bytes_sent: 2048,
    bytes_recv: 4096,
    tunnels: [
      { id: 't1', name: 'demo-1', running: true, active_conns: 2, total_conns: 8, bytes_sent: 1024, bytes_recv: 2048 },
      { id: 't2', name: 'demo-2', running: false, active_conns: 0, total_conns: 2, bytes_sent: 1024, bytes_recv: 2048 },
    ],
    connections: [],
    access_history: [],
    ...over,
  }
}

function makeAlert(over: Partial<AlertHistory>): AlertHistory {
  return {
    time: '2026-11-21T01:00:00Z',
    rule_id: 'r1',
    rule_name: 'rule-1',
    type: 'tunnel_down',
    value: 0,
    status: 'firing',
    message: 'm',
    ...over,
  }
}

let wrapper: ReturnType<typeof mount> | null = null

function mountStatus() {
  wrapper = mount(Status, {
    global: {
      plugins: [ElementPlus],
      // VTU 默认 stub 内建 Transition 且不渲染插槽，el-drawer 内容会整体丢失；
      // 关闭 stub 让 EP 的 overlay/抽屉真实渲染（happy-dom 无 CSS 动画，立即完成）
      stubs: { transition: false },
    },
  })
  return wrapper
}

beforeEach(() => {
  vi.mocked(api.getNodeStatus).mockResolvedValue(makeStatus())
  vi.mocked(api.getAlertHistory).mockResolvedValue([])
  vi.mocked(api.searchAccessHistory).mockResolvedValue([])
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  vi.clearAllMocks()
  document.body.innerHTML = ''
})

describe('Status 视图', () => {
  it('加载后渲染概览卡片、节点卡片与版本号', async () => {
    const w = mountStatus()
    await flushPromises()
    // 6 张概览卡片
    expect(w.findAll('.stat-card')).toHaveLength(6)
    expect(w.text()).toContain('1小时0分') // 运行时长 formatDuration(3600)
    expect(w.text()).toContain('2.0 KB') // 累计上传 formatBytes(2048)
    expect(w.find('.version').text()).toBe('v1.8.0')
    // 2 张节点卡片，含运行状态标签
    expect(w.findAll('.node-card')).toHaveLength(2)
    expect(w.text()).toContain('demo-1')
    expect(w.text()).toContain('已停止')
  })

  it('告警横幅按规则取最新状态：r1 已恢复不显示，仅剩 r2 firing', async () => {
    // getAlertHistory 返回时间倒序（最新在前）
    vi.mocked(api.getAlertHistory).mockResolvedValue([
      makeAlert({ rule_id: 'r1', status: 'resolved', message: 'r1-新' }),
      makeAlert({ rule_id: 'r1', status: 'firing', message: 'r1-旧' }),
      makeAlert({ rule_id: 'r2', rule_name: 'rule-2', status: 'firing', message: 'r2-告警' }),
    ])
    const w = mountStatus()
    await flushPromises()
    const banner = w.find('.alert-banner')
    expect(banner.exists()).toBe(true)
    expect(banner.text()).toContain('当前 1 条告警')
    expect(banner.text()).toContain('r2-告警')
    expect(banner.text()).not.toContain('r1-')
  })

  it('全部规则已恢复时不显示告警横幅', async () => {
    vi.mocked(api.getAlertHistory).mockResolvedValue([
      makeAlert({ rule_id: 'r1', status: 'resolved' }),
      makeAlert({ rule_id: 'r2', status: 'resolved' }),
    ])
    const w = mountStatus()
    await flushPromises()
    expect(w.find('.alert-banner').exists()).toBe(false)
  })

  it('点击节点卡片打开抽屉，并按默认 24h 范围检索该节点历史', async () => {
    const w = mountStatus()
    await flushPromises()
    vi.mocked(api.searchAccessHistory).mockClear()
    await w.findAll('.node-card')[0].trigger('click')
    await flushPromises()
    expect(api.searchAccessHistory).toHaveBeenCalledTimes(1)
    const q = vi.mocked(api.searchAccessHistory).mock.calls[0][0]
    expect(q.tunnel_id).toBe('t1')
    expect(q.result).toBe('')
    expect(q.limit).toBe(500)
    // 默认范围 24h：from 距今约 86400 秒（允许 2s 误差）
    const ageMs = Date.now() - Date.parse(q.from!)
    expect(ageMs).toBeGreaterThan(86400e3 - 2000)
    expect(ageMs).toBeLessThan(86400e3 + 2000)
    // Teleport 被 VTU stub 在组件树内，抽屉内容渲染于 wrapper 中
    expect(w.text()).toContain('节点：demo-1')
  })

  it('节点数据刷新时若该节点已被删除则关闭抽屉', async () => {
    const w = mountStatus()
    await flushPromises()
    await w.findAll('.node-card')[1].trigger('click') // 打开 t2
    await flushPromises()
    expect(w.text()).toContain('节点：demo-2')
    // 下一轮状态中 t2 消失：手动触发刷新（轮询定时器不由 flushPromises 驱动）
    vi.mocked(api.getNodeStatus).mockResolvedValue(makeStatus({
      tunnels: [makeStatus().tunnels[0]],
    }))
    const refreshBtn = w.findAll('button').find(b => b.text() === '刷新')
    expect(refreshBtn).toBeTruthy()
    await refreshBtn!.trigger('click')
    await flushPromises()
    expect(w.text()).not.toContain('节点：demo-2')
    expect(w.text()).toContain('节点详情')
  })
})
