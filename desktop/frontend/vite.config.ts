import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  build: {
    // ECharts dominates the bundle; the desktop shell loads it from disk, so
    // the default 500 kB chunk warning is noise here.
    chunkSizeWarningLimit: 900,
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.{spec,test}.ts'],
  },
})
