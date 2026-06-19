import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import wails from "@wailsio/runtime/plugins/vite";

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react(), wails("./bindings")],
  build: {
    // WebView2 on Windows 11 tracks recent Chromium, so target modern output.
    target: "es2022",
    // No source maps in the shipped binary (smaller, and the embed is internal).
    sourcemap: false,
    rollupOptions: {
      output: {
        // Split the two heavy editors into their own chunks so the main bundle
        // stays small and they can be cached independently.
        manualChunks: {
          monaco: ["monaco-editor", "@monaco-editor/react"],
          xterm: ["@xterm/xterm", "@xterm/addon-fit", "@xterm/addon-web-links"],
        },
      },
    },
  },
});
