import { reactRouter } from "@react-router/dev/vite"
import tailwindcss from "@tailwindcss/vite"
import { defineConfig } from "vite"

export default defineConfig({
  resolve: { tsconfigPaths: true },
  plugins: [tailwindcss(), reactRouter()],
  server: {
    // Dev proxy: dashboard calls /api/* and hits the Go gateway on :7420.
    proxy: {
      "/api": "http://127.0.0.1:7420",
    },
  },
})
