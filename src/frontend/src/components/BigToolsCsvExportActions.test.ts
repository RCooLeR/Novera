import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import BigToolsCsvExportActions from "./BigToolsCsvExportActions";

function render(disabled = false): string {
  return renderToStaticMarkup(
    createElement(BigToolsCsvExportActions, {
      disabled,
      numberKeys: false,
      onNumberKeysChange: vi.fn(),
      onExportJSONL: vi.fn(),
    }),
  );
}

describe("BigToolsCsvExportActions", () => {
  it("offers JSONL without exposing SQLite or XLSX mutation controls", () => {
    const markup = render();

    expect(markup).toContain(">JSONL</button>");
    expect(markup.match(/<button/g)).toHaveLength(1);
    expect(markup).not.toContain(">SQLite</button>");
    expect(markup).not.toContain(">XLSX</button>");
    expect(markup).toContain("SQLite and XLSX exports are unavailable");
  });

  it("honors the owning modal's disabled state", () => {
    expect(render(true)).toContain('disabled=""');
    expect(render(false)).not.toContain('disabled=""');
  });
});
