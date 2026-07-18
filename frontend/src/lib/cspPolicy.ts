const COMMON_DIRECTIVES = [
  "default-src 'self'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob:",
  "font-src 'self' data:",
  "worker-src 'self' blob:",
  "object-src 'none'",
  "base-uri 'self'",
  "form-action 'none'",
];

export const PRODUCTION_CSP = ["script-src 'self'", "connect-src 'self'", ...COMMON_DIRECTIVES].join("; ");

export const DEVELOPMENT_CSP = [
  "script-src 'self' 'unsafe-inline' 'unsafe-eval'",
  "connect-src 'self' ws://localhost:* ws://127.0.0.1:* wss://localhost:* wss://127.0.0.1:*",
  ...COMMON_DIRECTIVES,
].join("; ");

export function cspForVite(command: string, mode: string): string {
  return command === "build" && mode === "production" ? PRODUCTION_CSP : DEVELOPMENT_CSP;
}
