import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  inferTableSchema: vi.fn(),
  analyzeSqlDump: vi.fn(),
  previewCsvToSql: vi.fn(),
  writeFile: vi.fn(),
  createArtifact: vi.fn(),
  listArtifacts: vi.fn(),
}));

vi.mock("../lib/services", () => ({
  Workspace: {
    InferTableSchema: mocks.inferTableSchema,
    AnalyzeSQLDump: mocks.analyzeSqlDump,
    PreviewCsvToSql: mocks.previewCsvToSql,
    WriteFile: mocks.writeFile,
  },
  Settings: {},
  Git: {},
  LLM: {},
  Agent: {},
  Db: {},
  Watcher: {},
  Jobs: {},
  Artifacts: {
    CreateArtifact: mocks.createArtifact,
    ListArtifacts: mocks.listArtifacts,
  },
  SecretService: {},
  Shell: {},
  STALE_MARKER: "file changed on disk",
  errMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import { useStore, type Tab, type TabKind } from "./store";

let tabId = 10_000;

function tab(path: string, kind: TabKind, patch: Partial<Tab> = {}): Tab {
  return {
    instanceId: ++tabId,
    path,
    name: path.split("/").pop() ?? path,
    kind,
    language: "plaintext",
    content: "",
    savedContent: "",
    revision: "revision",
    binary: false,
    tooLarge: false,
    ...patch,
  };
}

function select(active: Tab) {
  useStore.setState({
    tabs: [active],
    activePath: active.path,
    toolBusy: false,
    toolResult: null,
    cleanDump: null,
    dataTools: null,
    status: null,
  });
}

afterEach(() => {
  useStore.setState({
    tabs: [],
    activePath: null,
    toolBusy: false,
    toolResult: null,
    cleanDump: null,
    dataTools: null,
    status: null,
    artifacts: [],
  });
  for (const mock of Object.values(mocks)) mock.mockReset();
});

describe("file command execution eligibility", () => {
  it("rejects a CSV-looking diff key before any workspace or artifact bridge call", async () => {
    select(tab("diff:u:exports/data.csv", "diff", { rel: "exports/data.csv" }));

    await useStore.getState().runCsvSchema();
    await useStore.getState().runCsvToSql();
    await useStore.getState().saveActive();
    useStore.getState().openDataTools();
    await useStore.getState().saveActiveAsArtifact("file");

    expect(mocks.inferTableSchema).not.toHaveBeenCalled();
    expect(mocks.previewCsvToSql).not.toHaveBeenCalled();
    expect(mocks.writeFile).not.toHaveBeenCalled();
    expect(mocks.createArtifact).not.toHaveBeenCalled();
    expect(useStore.getState().dataTools).toBeNull();
  });

  it("rejects a SQL-looking table path before dump bridge calls or modal state", async () => {
    select(tab("exports/backup.sql", "table", { rel: "exports/backup.sql" }));

    await useStore.getState().runDumpAnalyze();
    useStore.getState().openCleanDump();
    useStore.getState().openDataTools();

    expect(mocks.analyzeSqlDump).not.toHaveBeenCalled();
    expect(useStore.getState().cleanDump).toBeNull();
    expect(useStore.getState().dataTools).toBeNull();
  });

  it("passes a real large CSV's workspace-relative path to the backend", async () => {
    const schema = { columns: [], delimiter: ",", rows: 0, truncated: false };
    mocks.inferTableSchema.mockResolvedValueOnce(schema);
    select(tab("exports/huge.csv", "file", { tooLarge: true }));

    await useStore.getState().runCsvSchema();

    expect(mocks.inferTableSchema).toHaveBeenCalledOnce();
    expect(mocks.inferTableSchema).toHaveBeenCalledWith("exports/huge.csv", "");
    expect(useStore.getState().toolResult).toMatchObject({ kind: "schema", rel: "exports/huge.csv", schema });
  });

  it("rejects an invalid file path even when the tab kind and suffix look valid", async () => {
    select(tab("../outside.csv", "file"));

    await useStore.getState().runCsvSchema();
    await useStore.getState().saveActiveAsArtifact("file");

    expect(mocks.inferTableSchema).not.toHaveBeenCalled();
    expect(mocks.createArtifact).not.toHaveBeenCalled();
  });
});
