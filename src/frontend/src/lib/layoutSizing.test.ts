import { describe, expect, it } from "vitest";
import { fitPanelLayout, MIN_EDITOR_WIDTH } from "./layoutSizing";

describe("fitPanelLayout", () => {
  it("reserves a usable editor at the supported 900px minimum", () => {
    const layout = fitPanelLayout(900, true, true, 640, 720);
    expect(layout.showSidebar).toBe(true);
    expect(layout.showAssistant).toBe(true);
    expect(48 + layout.sidebarWidth + layout.assistantWidth + MIN_EDITOR_WIDTH).toBeLessThanOrEqual(900);
  });

  it("collapses the assistant before the sidebar when both minimums do not fit", () => {
    expect(fitPanelLayout(700, true, true, 280, 360)).toMatchObject({
      showSidebar: true,
      showAssistant: false,
    });
  });

  it("collapses all optional panels when the editor minimum cannot fit beside them", () => {
    expect(fitPanelLayout(450, true, true, 280, 360)).toEqual({
      showSidebar: false,
      showAssistant: false,
      sidebarWidth: 0,
      assistantWidth: 0,
    });
  });
});
