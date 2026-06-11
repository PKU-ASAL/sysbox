import path from "node:path"
import react from "@vitejs/plugin-react"
import { defineConfig, loadEnv } from "vite"

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "")
  const apiTarget = env.SYSBOX_WEB_API_TARGET || "http://127.0.0.1:9876"

  return {
    plugins: [react()],
    resolve: {
      alias: {
        "@": path.resolve(__dirname, "./src"),
      },
    },
    server: {
      port: Number(env.SYSBOX_WEB_DEV_PORT || 3001),
      proxy: {
        "/v1": {
          target: apiTarget,
          changeOrigin: true,
          ws: true,
        },
      },
    },
    build: {
      rollupOptions: {
        output: {
          manualChunks(id) {
            if (id.includes("elkjs")) return "topology-layout"
            if (id.includes("node_modules/@xyflow")) return "topology-canvas"
            if (id.includes("node_modules/@xterm")) return "terminal"
            if (id.includes("node_modules")) return "vendor"
          },
        },
      },
      chunkSizeWarningLimit: 1500,
    },
  }
})
