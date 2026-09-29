// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { query, tableInfo, setStatus } = vi.hoisted(() => ({
  query: vi.fn(), tableInfo: vi.fn(), setStatus: vi.fn(),
}));
vi.mock("../lib/services", () => ({
  Workspace: { QueryTable: query, TableInfo: tableInfo },
  Shell: {}, errMessage: (error: unknown) => String(error),
}));
vi.mock("../state/store", () => ({
  useStore: (select: (state: { setStatus: typeof setStatus }) => unknown) => select({ setStatus }),
}));
vi.mock("./VirtualGrid", () => ({
  default: ({ rows, onVisibleRange }: { rows: string[][]; onVisibleRange: (start: number, end: number) => void }) =>
    createElement("button", { onClick: () => onVisibleRange(4000, 4020) }, rows.flat().join(",")),
}));
import TableView from "./TableView";

let host: HTMLDivElement;
let root: Root;
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((yes) => { resolve = yes; });
  return { promise, resolve };
}
function page(value: string) {
  return { columns: ["value"], rows: [[value]], sheet: "", delimiter: ",", capped: false, message: "", hasMore: true, totalRows: 10_000 };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  query.mockReset();
  tableInfo.mockResolvedValue({ rows: 10_000 });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
function render(sourceVersion = 0) {
  return act(async () => root.render(createElement(TableView, { rel: "data.csv", sourceVersion })));
}
function requestDistantWindow() {
  return act(async () => host.querySelector(".dbq__results button")!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
}

describe("TableView query ownership", () => {
  it("cancels a debounced scroll request when the source version changes", async () => {
    query.mockResolvedValue(page("old"));
    await render();
    await requestDistantWindow();
    query.mockResolvedValue(page("new"));
    await render(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });
    expect(query.mock.calls.map(([, options]) => options.offset)).toEqual([0, 0]);
    expect(host.querySelector(".dbq__results")!.textContent).toBe("new");
  });

  it("ignores an in-flight page from the replaced source", async () => {
    const obsolete = deferred<ReturnType<typeof page>>();
    query.mockResolvedValueOnce(page("initial")).mockReturnValueOnce(obsolete.promise).mockResolvedValueOnce(page("new"));
    await render();
    await requestDistantWindow();
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });
    await render(1);
    await act(async () => { obsolete.resolve(page("obsolete")); await obsolete.promise; });
    expect(host.querySelector(".dbq__results")!.textContent).toBe("new");
    expect(setStatus).not.toHaveBeenCalled();
  });
});
