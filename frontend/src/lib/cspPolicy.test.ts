import { describe, expect, it } from "vitest";
import { cspForVite, DEVELOPMENT_CSP, PRODUCTION_CSP } from "./cspPolicy";

describe("renderer CSP policy", () => {
  it("keeps development-only script and websocket allowances out of production", () => {
    const scriptSource = PRODUCTION_CSP.match(/(?:^|;\s*)script-src\s+([^;]+)/)?.[1];
    const connectSource = PRODUCTION_CSP.match(/(?:^|;\s*)connect-src\s+([^;]+)/)?.[1];

    expect(scriptSource).toBe("'self'");
    expect(connectSource).toBe("'self'");
    expect(PRODUCTION_CSP).not.toContain("unsafe-eval");
    expect(PRODUCTION_CSP).not.toMatch(/\bws:/);
    expect(PRODUCTION_CSP).not.toMatch(/\bwss:/);
  });

  it("retains Vite HMR allowances only for development", () => {
    expect(DEVELOPMENT_CSP).toContain("'unsafe-eval'");
    expect(DEVELOPMENT_CSP).toContain("ws://localhost:*");
    expect(DEVELOPMENT_CSP).toContain("ws://127.0.0.1:*");
    expect(DEVELOPMENT_CSP).not.toContain("connect-src 'self' ws: wss:");
    expect(cspForVite("serve", "development")).toBe(DEVELOPMENT_CSP);
    expect(cspForVite("build", "development")).toBe(DEVELOPMENT_CSP);
    expect(cspForVite("build", "production")).toBe(PRODUCTION_CSP);
  });
});
