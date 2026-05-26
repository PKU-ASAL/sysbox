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
      port: 3000,
      proxy: {
        "/v1": {
          target: apiTarget,
          changeOrigin: true,
          ws: true,
        },
      },
    },
  }
})
