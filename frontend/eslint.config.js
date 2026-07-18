import js from "@eslint/js";
import tseslint from "typescript-eslint";
import globals from "globals";

// Flat config. Generated bindings, build output, and deps are not linted.
export default tseslint.config(
  { ignores: ["dist", "bindings", "node_modules"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["scripts/**/*.mjs"],
    languageOptions: {
      globals: { ...globals.node },
    },
  },
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      globals: { ...globals.browser },
    },
    rules: {
			// Bridge and event payloads enter as unknown and are validated before use.
			"@typescript-eslint/no-explicit-any": "error",
      "@typescript-eslint/no-unused-vars": ["warn", { argsIgnorePattern: "^_", varsIgnorePattern: "^_" }],
    },
  },
);
