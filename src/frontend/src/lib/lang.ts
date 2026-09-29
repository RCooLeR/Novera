// Map a file path to a Monaco language id by extension/basename.
const BY_EXT: Record<string, string> = {
  go: "go", mod: "go", sum: "plaintext",
  ts: "typescript", tsx: "typescript", js: "javascript", jsx: "javascript", mjs: "javascript", cjs: "javascript",
  json: "json", jsonc: "json",
  html: "html", htm: "html", css: "css", scss: "scss", less: "less",
  md: "markdown", markdown: "markdown",
  py: "python", rb: "ruby", rs: "rust", java: "java", kt: "kotlin", kts: "kotlin",
  c: "c", h: "c", cpp: "cpp", cc: "cpp", cxx: "cpp", hpp: "cpp", hh: "cpp", cs: "csharp",
  php: "php", swift: "swift", sql: "sql", graphql: "graphql", gql: "graphql", proto: "proto",
  yaml: "yaml", yml: "yaml", toml: "ini", ini: "ini", cfg: "ini", conf: "ini",
  sh: "shell", bash: "shell", zsh: "shell", fish: "shell", ps1: "powershell", psm1: "powershell", bat: "bat", cmd: "bat",
  xml: "xml", svg: "xml", dockerfile: "dockerfile", makefile: "makefile", mk: "makefile",
  vue: "vue", svelte: "html", astro: "html", dart: "dart", lua: "lua", r: "r",
  scala: "scala", groovy: "groovy", gradle: "groovy", pl: "perl", pm: "perl",
  ex: "elixir", exs: "elixir", erl: "erlang", clj: "clojure", cljs: "clojure",
  hs: "haskell", ml: "fsharp", fs: "fsharp", fsx: "fsharp", jl: "julia",
  txt: "plaintext", log: "plaintext", env: "ini", tf: "hcl", hcl: "hcl",
};

const BY_NAME: Record<string, string> = {
  dockerfile: "dockerfile",
  makefile: "makefile",
  "go.mod": "go",
  "go.sum": "plaintext",
  ".gitignore": "ignore",
  ".env": "ini",
};

// baseName returns the lowercased final path segment (slash- or backslash-
// separated), so extension detection isn't fooled by a dotted directory name.
function baseName(path: string): string {
  return (path.replace(/\\/g, "/").split("/").pop() ?? "").toLowerCase();
}

// extOf returns the lowercased extension of the basename (no leading dot), or "".
function extOf(path: string): string {
  const base = baseName(path);
  const dot = base.lastIndexOf(".");
  return dot >= 0 ? base.slice(dot + 1) : "";
}

// Tabular files open in the analytics grid instead of the text editor.
export function isTabular(path: string): boolean {
  const ext = extOf(path);
  return ext === "csv" || ext === "tsv" || ext === "xlsx" || ext === "xlsm";
}

export function languageForPath(path: string): string {
  const base = baseName(path);
  if (BY_NAME[base]) return BY_NAME[base];
  const ext = extOf(path);
  if (ext && BY_EXT[ext]) return BY_EXT[ext];
  return "plaintext";
}
