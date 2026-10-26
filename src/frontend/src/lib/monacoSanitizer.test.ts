// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import purify from "dompurify";
// Monaco's internal sanitizer is JavaScript without public type declarations.
// @ts-expect-error Exercise the real editor boundary, including the build alias.
import { safeSetInnerHtml } from "monaco-editor/base/browser/domSanitize.js";

afterEach(() => vi.restoreAllMocks());

describe("Monaco's patched sanitizer", () => {
  it("uses the installed sanitizer and preserves ordinary documentation", () => {
    const sanitize = vi.spyOn(purify, "sanitize");
    const target = document.createElement("div");
    safeSetInnerHtml(target, '<p><strong>Documentation</strong> <code>foo()</code> <a href="https://example.com/help">help</a></p>');
    expect(sanitize).toHaveBeenCalled();
    expect(target.querySelector("strong")?.textContent).toBe("Documentation");
    expect(target.querySelector("code")?.textContent).toBe("foo()");
    expect(target.querySelector("a")?.getAttribute("href")).toBe("https://example.com/help");
  });

  it.each([
    '<img src="https://example.com/image" onerror="alert(1)"><script>alert(1)</script>',
    '<a href="java&#x73;cript:alert(1)">link</a><svg><animate attributeName="href" values="javascript:alert(1)"></animate></svg>',
  ])("removes executable markup from documentation: %s", (input) => {
    const target = document.createElement("div");
    safeSetInnerHtml(target, input);
    expect(target.querySelector("script, animate, set, [onerror]")).toBeNull();
    expect(target.querySelector('a[href^="javascript:"]')).toBeNull();
  });

  // GHSA-c6gr-qvv4-5v3f: guard case-preserved attributeName in XHTML.
  // Monaco does not enable this mode; this additionally checks that the selected
  // implementation contains the upstream fix, not just an updated package label.
  it.each(["animate", "set"])("rejects XHTML %s href rewriting", (tag) => {
    const clean = purify.sanitize(
      `<svg xmlns="http://www.w3.org/2000/svg"><a><${tag} attributeName="href" values="x;javascript:alert(1)"/></a></svg>`,
      {
        PARSER_MEDIA_TYPE: "application/xhtml+xml",
        ALLOWED_TAGS: ["svg", "a", tag],
        ALLOWED_ATTR: ["attributeName", "values"],
      },
    );
    expect(clean).not.toContain('attributeName="href"');
  });
});
