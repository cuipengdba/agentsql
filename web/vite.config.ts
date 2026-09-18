import { fileURLToPath, URL } from "node:url";

import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react()],
  base: "/",
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules")) {
            return undefined;
          }

          const moduleId = id.replaceAll("\\", "/");
          if (/\/node_modules\/(?:react|react-dom|react-router|react-router-dom|scheduler|@remix-run\/router)\//.test(moduleId)) {
            return "react-vendor";
          }
          if (/\/node_modules\/(?:antd|rc-[^/]+|@ant-design\/[^/]+|@rc-component\/[^/]+)\//.test(moduleId)) {
            return "antd-vendor";
          }
          if (/\/node_modules\/(?:echarts|zrender)\//.test(moduleId)) {
            return "echarts-vendor";
          }
          return "utility-vendor";
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:7780",
        changeOrigin: true,
      },
      "/mcp": {
        target: "http://127.0.0.1:7780",
        changeOrigin: true,
      },
    },
  },
});
