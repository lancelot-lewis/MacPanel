import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// 开发模式下把 /api 代理到本地 Go 服务，避免跨域。
// 默认端口 7788，可通过 MACPANEL_DEV_BACKEND 覆盖。
const backend = process.env.MACPANEL_DEV_BACKEND || 'http://127.0.0.1:7788'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  build: {
    // 产物输出到 web/dist，由 Go 的 embed 打包进二进制。
    outDir: 'dist',
    emptyOutDir: true,
    // 关闭 sourcemap：生产二进制不携带源码。
    sourcemap: false,
    // 单包体积告警阈值。naive-ui 单独成 chunk 后约 400 kB，
    // 统一拆包后业务代码可以保持在很小体积。
    chunkSizeWarningLimit: 700,
    rollupOptions: {
      output: {
        // 便于长期缓存的文件名策略。
        entryFileNames: 'assets/[name]-[hash].js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
        // 手动分包：把体积大且很少变动的依赖单独拆出。
        // 好处是升级业务代码时用户不必重新下载整个组件库。
        manualChunks(id) {
          if (id.includes('node_modules/naive-ui')) return 'naive'
          if (id.includes('node_modules/@css-render')) return 'naive'
          if (id.includes('node_modules/seemly')) return 'naive'
          if (id.includes('node_modules/vueuc')) return 'naive'
          if (id.includes('node_modules/vdirs')) return 'naive'
          if (id.includes('node_modules/vue-router')) return 'vue'
          if (id.includes('node_modules/@vue/')) return 'vue'
          if (id.includes('node_modules/vue/')) return 'vue'
          return undefined
        },
      },
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': {
        target: backend,
        // 必须改写成后端自己的 Host，否则会被后端的 HostGuard 拒绝
        // （它只允许 127.0.0.1:7788 / localhost:7788，防 DNS rebinding）。
        // 之前设为 false 时，开发模式下所有 API 都返回「Host 头不被允许」。
        changeOrigin: true,
      },
    },
  },
})
