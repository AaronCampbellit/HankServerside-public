import { resolve } from "node:path";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react()],
  build: {
    target: "es2020",
    outDir: ".mcp-kanban-dist",
    emptyOutDir: true,
    cssCodeSplit: false,
    minify: "oxc",
    lib: {
      entry: resolve(__dirname, "src/mcp-kanban/main.tsx"),
      formats: ["es"],
      fileName: () => "kanban.js",
      cssFileName: "kanban",
    },
  },
});
