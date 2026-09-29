// Validation for user-entered file/folder names created from the explorer.
// Returns an error message string when the name is invalid, or "" when it's OK.
// Mirrors the constraints the OS (and the workspace service) would otherwise
// reject — but here we can explain the problem before the round-trip.

// Windows reserved device names (case-insensitive, with or without extension).
const RESERVED = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\.|$)/i;
// Characters illegal in a single path segment on Windows (and a poor idea
// elsewhere): the path separators plus < > : " | ? *.
const ILLEGAL = /[<>:"|?*\\/]/;

export function validateFileName(raw: string): string {
  const name = raw.trim();
  if (!name) return "A name is required.";
  if (name === "." || name === "..") return "That name is reserved.";
  if (name.includes("/") || name.includes("\\")) return "Name can't contain a path separator.";
  if (ILLEGAL.test(name)) return 'Name can\'t contain any of < > : " | ? *';
  // Control characters.
  for (let i = 0; i < name.length; i++) {
    if (name.charCodeAt(i) < 0x20) return "Name can't contain control characters.";
  }
  if (/[ .]$/.test(name)) return "Name can't end with a space or dot.";
  if (RESERVED.test(name)) return `"${name}" is a reserved name on Windows.`;
  return "";
}
