import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import TrendChart from './TrendChart.vue'
import type { MetricPoint } from '@/api'

const pts: MetricPoint[] = [
  { time: '2026-11-21T02:00:00Z', active_conns: 1, conn_delta: 1, sent_delta: 1024, recv_delta: 2048 },
  { time: '2026-11-21T02:00:10Z', active_conns: 3, conn_delta: 2, sent_delta: 512, recv_delta: 4096 },
  { time: '2026-11-21T02:00:20Z', active_conns: 2, conn_delta: 1, sent_delta: 256, recv_delta: 8192 },
]

function mountChart(props: { points: MetricPoint[]; loading?: boolean }) {
  return mount(TrendChart, { props, global: { plugins: [ElementPlus] } })
}

describe('TrendChart', () => {
  it('空数据显示占位提示，不渲染 svg', () => {
    const wrapper = mountChart({ points: [] })
    expect(wrapper.find('.trend-empty').exists()).toBe(true)
    expect(wrapper.find('svg').exists()).toBe(false)
    expect(wrapper.text()).toContain('暂无采样数据')
  })

  it('仅 1 个采样点时仍显示占位（不足 2 点无法绘制折线）', () => {
    const wrapper = mountChart({ points: pts.slice(0, 1) })
    expect(wrapper.find('.trend-empty').exists()).toBe(true)
    expect(wrapper.find('svg').exists()).toBe(false)
  })

  it('loading 优先展示加载文案', () => {
    const wrapper = mountChart({ points: [], loading: true })
    expect(wrapper.text()).toContain('加载中…')
  })

  it('正常序列渲染折线/面积路径与时间轴', () => {
    const wrapper = mountChart({ points: pts })
    const line = wrapper.find('svg path.trend-line')
    const area = wrapper.find('svg path.trend-area')
    expect(line.exists()).toBe(true)
    expect(area.exists()).toBe(true)
    // 结构断言而非精确坐标：3 个点 → 1 个 M + 2 个 L，面积路径闭合
    const linePath = line.attributes('d') || ''
    expect(linePath).toMatch(/^M[\d.]+,[\d.]+ L/)
    expect((linePath.match(/L/g) || []).length).toBe(2)
    expect(area.attributes('d')).toMatch(/Z$/)
    // 时间轴显示首/中/末三个标签
    const axis = wrapper.findAll('.trend-axis span')
    expect(axis).toHaveLength(3)
    // 默认「活动连接」指标：峰值 3 / 均值 2
    expect(wrapper.text()).toContain('活动连接 · 峰值 3 · 均值 2')
  })

  it('切换到「上行」后按 sent_delta 重算峰值与均值', async () => {
    const wrapper = mountChart({ points: pts })
    const sent = wrapper.find('input[type="radio"][value="sent"]')
    expect(sent.exists()).toBe(true)
    await sent.setValue()
    // sent_delta 最大 1024（1.0 KB）；均值 (1024+512+256)/3≈597，未达 KB 档位
    expect(wrapper.text()).toContain('上行流量 · 峰值 1.0 KB · 均值 597 B')
  })

  it('切换到「下行」后按 recv_delta 重算峰值与均值', async () => {
    const wrapper = mountChart({ points: pts })
    const recv = wrapper.find('input[type="radio"][value="recv"]')
    expect(recv.exists()).toBe(true)
    await recv.setValue()
    // recv_delta 最大 8192（8.0 KB）；均值 (2048+4096+8192)/3≈4779 → 4.7 KB
    expect(wrapper.text()).toContain('下行流量 · 峰值 8.0 KB · 均值 4.7 KB')
  })
})
