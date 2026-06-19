import js from "@eslint/js";
import tseslint from "typescript-eslint";
import globals from "globals";

// Flat config. Generated bindings, build output, and deps are not linted.
export default tseslint.config(
  { ignores: ["dist", "bindings", "node_modules"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      globals: { ...globals.browser },
    },
    rules: {
      // This app interops with untyped bridge/event payloads; `any` is sometimes
      // unavoidable at those seams, and unused names are a warning, not an error.
      "@typescript-eslint/no-explicit-any": "off",
      "@typescript-eslint/no-unused-vars": ["warn", { argsIgnorePattern: "^_", varsIgnorePattern: "^_" }],
    },
  },
);
