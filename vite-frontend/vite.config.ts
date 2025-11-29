import path from "path";

import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwind from "@tailwindcss/vite";
// inject app version from package.json

// @ts-ignore
import pkg from "./package.json";

export default defineConfig({
  plugins: [tailwind(), react()],
  base: "/app/",
  define: {
    "import.meta.env.VITE_APP_VERSION": JSON.stringify(pkg.version || ""),
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
    minify: false,
    rollupOptions: {
      treeshake: false,
    },
  },
});
