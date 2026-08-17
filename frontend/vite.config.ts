import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import wails from "@wailsio/runtime/plugins/vite";
import { cspForVite } from "./src/lib/cspPolicy.ts";

// https://vitejs.dev/config/
export default defineConfig(({ command, mode }) => {
  const csp = cspForVite(command, mode);
  return {
    plugins: [
      {
        name: "novera-renderer-csp",
        transformIndexHtml(html) {
          if (!html.includes("__NOVERA_CSP__")) {
            throw new Error("index.html is missing the Novera CSP placeholder");
          }
          return html.replace("__NOVERA_CSP__", csp);
        },
      },
      react(),
      wails("./bindings"),
    ],
    build: {
      // WebView2 on Windows 11 tracks recent Chromium, so target modern output.
      target: "es2022",
      // No source maps in the shipped binary (smaller, and the embed is internal).
      sourcemap: false,
      // EditorPane and TerminalView are dynamic imports. Let Rollup preserve
      // those natural boundaries: assigning their packages to manual chunks
      // pulled shared React exports into the editor chunk and made it eager.
      manifest: true,
    },
  };
});
