import { defineConfig } from "vitest/config";

// Standalone test config (doesn't load the app's vite plugins). Tests target the
// pure helper libs; component/DOM tests can add jsdom + testing-library later.
export default defineConfig({
  test: {
    include: ["src/**/*.test.ts"],
    environment: "node",
  },
});
