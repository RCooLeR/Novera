import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import LargeFileSaveActions from "./LargeFileSaveActions";

function render(saving = false, hasEdits = true): string {
  return renderToStaticMarkup(
    createElement(LargeFileSaveActions, {
      saving,
      hasEdits,
      discardDanger: true,
      onSaveCopy: vi.fn(),
      onDiscard: vi.fn(),
    }),
  );
}

describe("LargeFileSaveActions", () => {
  it("offers copy and discard without exposing direct source replacement", () => {
    const markup = render();

    expect(markup).toContain("Save as copy");
    expect(markup).toContain("Discard");
    expect(markup).not.toContain("Save in place");
    expect(markup).not.toContain("crash-safe");
  });

  it("disables both settlement actions while saving or without staged edits", () => {
    expect(render(true, true).match(/disabled=""/g)).toHaveLength(2);
    expect(render(false, false).match(/disabled=""/g)).toHaveLength(2);
    expect(render(false, true)).not.toContain('disabled=""');
  });
});
