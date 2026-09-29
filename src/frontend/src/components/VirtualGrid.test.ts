// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import VirtualGrid from "./VirtualGrid";

let host: HTMLDivElement;
let root: Root;
let viewportHeight: number;
let onResize: () => void;
const disconnect = vi.fn();

beforeEach(() => {
  viewportHeight = 250;
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("ResizeObserver", class {
    constructor(callback: () => void) { onResize = callback; }
    observe() {}
    disconnect = disconnect;
  });
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockImplementation(() => viewportHeight);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function render(rows: string[][]) {
  return act(async () => root.render(createElement(VirtualGrid, {
    columns: ["value"], rows, sort: null, onSort: vi.fn(),
  })));
}

describe("VirtualGrid DOM lifecycle", () => {
  it("shows new results after shrinking a deeply scrolled dataset", async () => {
    await render(Array.from({ length: 10_000 }, (_, i) => [String(i)]));
    const grid = host.querySelector<HTMLDivElement>(".vgrid")!;
    await act(async () => {
      grid.scrollTop = 100_000;
      grid.dispatchEvent(new Event("scroll", { bubbles: true }));
    });
    expect(host.textContent).not.toContain("replacement");

    await render([["replacement"], ["second"], ["third"]]);
    expect(host.textContent).toContain("replacement");
    expect(host.querySelectorAll("tbody tr")).toHaveLength(3);
    expect(grid.scrollTop).toBe(0);
  });

  it("expands the virtual window on container resize without a prop change", async () => {
    await render(Array.from({ length: 10_000 }, (_, i) => [String(i)]));
    const before = host.querySelectorAll("tbody tr:not([aria-hidden])").length;
    await act(async () => {
      viewportHeight = 1000;
      onResize();
    });
    const after = host.querySelectorAll("tbody tr:not([aria-hidden])").length;
    expect(after).toBeGreaterThan(before);
    expect(after).toBeLessThanOrEqual(60);
    expect(disconnect).not.toHaveBeenCalled();
  });
});
