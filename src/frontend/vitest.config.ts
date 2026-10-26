import { defineConfig } from "vitest/config";
import { resolveMonacoSanitizer } from "./scripts/monaco-sanitizer.ts";

// Standalone test config (doesn't load the app's vite plugins). Tests target the
// helpers by default; DOM regressions opt into jsdom per test file.
export default defineConfig({
  plugins: [resolveMonacoSanitizer()],
  test: {
    server: { deps: { inline: [/monaco-editor/] } },
    include: ["src/**/*.test.ts"],
    environment: "node",
    // Vitest 5 can reuse transforms across runs without sharing test state.
    fsModuleCache: true,
  },
});
