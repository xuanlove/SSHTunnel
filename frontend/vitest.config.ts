import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import path from 'path'

// 独立于 vite.config.ts：仅测试使用（test.environment / plugin / alias）
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
  test: {
    environment: 'happy-dom',
    include: ['src/**/*.{spec,test}.ts'],
    // Status 等视图组件较重，放宽单文件超时
    testTimeout: 15000,
  },
})
