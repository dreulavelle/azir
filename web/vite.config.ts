import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The browser talks to the Go gateway, never to NATS directly. In dev the
// proxy stands in for that gateway so the frontend uses one origin.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: process.env.AZIR_CORE_URL ?? "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
});
