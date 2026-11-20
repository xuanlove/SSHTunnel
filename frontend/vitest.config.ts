import { defineConfig, mergeConfig } from 'vitest/config'
import viteConfig from './vite.config'

// 复用 vite.config.ts 的插件与 @ alias，避免两处配置漂移
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: 'happy-dom',
      include: ['src/**/*.{spec,test}.ts'],
      // 单个测试的超时（挂载视图组件较重）
      testTimeout: 15000,
      coverage: {
        provider: 'v8',
        // 只统计应用源码，排除测试文件与生成/声明文件
        include: ['src/**/*.{ts,vue}'],
        exclude: ['src/main.ts', 'src/types.ts', 'src/env.d.ts', 'src/**/*.spec.ts'],
        reporter: ['text', 'html'],
        // 渐进式阈值：先锁住已有高质量覆盖，防止回归；范围外文件只出报告不设门槛
        thresholds: {
          'src/utils/**': { statements: 100, branches: 100, functions: 100, lines: 100 },
          'src/api/**': { statements: 45, branches: 60, functions: 50, lines: 45 },
          'src/components/TrendChart.vue': { statements: 95, branches: 80, functions: 100, lines: 95 },
        },
      },
    },
  }),
)
