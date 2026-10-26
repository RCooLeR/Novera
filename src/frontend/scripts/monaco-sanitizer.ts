import { fileURLToPath } from "node:url";
import type { Plugin } from "vite";

// Monaco embeds an old DOMPurify source file instead of importing its declared
// npm dependency. An npm override alone therefore does not patch the editor.
const patchedSanitizer = fileURLToPath(import.meta.resolve("dompurify"));
// Install the same resolver in Vite and its separate dependency optimizer:
// the optimizer does not apply Vite's aliases to this relative vendor import.
export function resolveMonacoSanitizer(): Plugin {
  return {
    name: "novera-resolve-monaco-sanitizer",
    enforce: "pre",
    resolveId: {
      order: "pre",
      handler(source, importer) {
        if (source === "./dompurify/dompurify.js" &&
            importer?.replaceAll("\\", "/").endsWith("/monaco-editor/esm/vs/base/browser/domSanitize.js")) {
          return patchedSanitizer;
        }
      },
    },
  };
}

export function checkMonacoSanitizer(): Plugin {
  return {
    name: "novera-patched-monaco-sanitizer",
    generateBundle(_options, bundle) {
      const modules = Object.values(bundle).flatMap((output) =>
        output.type === "chunk" ? Object.keys(output.modules) : [],
      ).map((id) => id.replaceAll("\\", "/"));
      if (modules.some((id) => id.includes("/monaco-editor/") && id.endsWith("/dompurify/dompurify.js"))) {
        this.error("Monaco's embedded DOMPurify must not enter the production bundle");
      }
      if (!modules.includes(patchedSanitizer.replaceAll("\\", "/"))) {
        this.error("The patched DOMPurify package is missing from the production bundle");
      }
    },
  };
}
