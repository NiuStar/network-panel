import path from "path";

import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwind from "@tailwindcss/vite";
import compression from "vite-plugin-compression";
// inject app version from package.json

// @ts-ignore
import pkg from "./package.json";

export default defineConfig({
  plugins: [
    tailwind(),
    react(),
    compression({ algorithm: "gzip" }),
    compression({ algorithm: "brotliCompress", ext: ".br" }),
  ],
  base: "/app/",
  define: {
    "import.meta.env.VITE_APP_VERSION": JSON.stringify(pkg.version || ""),
    "import.meta.env.VITE_BUILD_DATE": JSON.stringify(
      new Date().toISOString(),
    ),
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    port: 3000,
    host: "0.0.0.0",
  },
  build: {
    outDir: "dist",
    sourcemap: false,
    minify: "esbuild",
    rollupOptions: {
      treeshake: true,
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules")) return;
          const parts = id.split("node_modules/")[1];
          if (!parts) return;
          const segs = parts.split("/");
          let pkg = segs[0];
          if (pkg.startsWith("@") && segs.length > 1) {
            pkg = `${pkg}/${segs[1]}`;
          }
          if (
            pkg.startsWith("@heroui/") ||
            pkg.startsWith("@nextui-org/") ||
            pkg.startsWith("@react-aria/") ||
            pkg.startsWith("@react-stately/") ||
            pkg.startsWith("@react-types/") ||
            pkg.startsWith("@internationalized/")
          ) {
            return "ui";
          }
          if (pkg.startsWith("@dnd-kit/") || pkg === "react-beautiful-dnd") {
            return "dnd";
          }
          if (pkg === "echarts" || pkg === "recharts") {
            return "charts";
          }
          if (pkg === "xterm") {
            return "xterm";
          }
          if (pkg === "framer-motion") {
            return "motion";
          }
          if (pkg === "react-hot-toast" || pkg === "sonner") {
            return "toast";
          }
          if (pkg === "axios") {
            return "http";
          }
          if (pkg === "dayjs") {
            return "date";
          }
          return "vendor";
        },
      },
    },
  },
});
