import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// 开发模式下把 /api 代理到本地 Go 服务，避免跨域。
// 默认端口 7788，可通过 HLM_DEV_BACKEND 覆盖。
const backend = process.env.HLM_DEV_BACKEND || 'http://127.0.0.1:7788'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  build: {
    // 产物输出到 web/dist，由 Go 的 embed 打包进二进制。
    outDir: 'dist',
    emptyOutDir: true,
    // 关闭 sourcemap：生产二进制不携带源码。
    sourcemap: false,
    rollupOptions: {
      output: {
        // 便于长期缓存的文件名策略。
        entryFileNames: 'assets/[name]-[hash].js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': {
        target: backend,
        changeOrigin: false,
      },
    },
  },
})
