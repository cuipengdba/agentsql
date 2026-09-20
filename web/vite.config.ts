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
    dedupe: ["react", "react-dom"],
  },
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    target: "baseline-widely-available",
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              test: /[\\/]node_modules[\\/]/,
              name(moduleId) {
                const normalizedId = moduleId.replaceAll("\\", "/");
                if (/\/node_modules\/(?:react|react-dom|react-router|react-router-dom|scheduler)\//.test(normalizedId)) {
                  return "react-vendor";
                }
                if (/\/node_modules\/(?:antd|rc-[^/]+|@ant-design\/[^/]+|@rc-component\/[^/]+)\//.test(normalizedId)) {
                  return "antd-vendor";
                }
                if (/\/node_modules\/(?:echarts|zrender)\//.test(normalizedId)) {
                  return "echarts-vendor";
                }
                return "utility-vendor";
              },
            },
          ],
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
