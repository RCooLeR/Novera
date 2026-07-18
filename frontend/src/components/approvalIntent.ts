export const APPROVAL_INTENT_PAGE_CHARS = 4_000;

export function formatApprovalIntent(raw: string): string {
  // The backend already emits deterministic, indented canonical JSON. Keep it
  // byte-for-byte so the displayed text is exactly what its SHA-256 covers.
  return raw;
}

export function approvalIntentPages(raw: string, pageChars = APPROVAL_INTENT_PAGE_CHARS): string[] {
  const formatted = formatApprovalIntent(raw);
  const size = Math.max(1, Math.floor(pageChars));
  if (formatted.length === 0) return [""];
  const pages: string[] = [];
  for (let offset = 0; offset < formatted.length; offset += size) {
    pages.push(formatted.slice(offset, offset + size));
  }
  return pages;
}
