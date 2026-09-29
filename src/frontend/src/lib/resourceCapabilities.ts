export type ResourceKind = "file" | "diff" | "db" | "table";

export interface ResourceDescriptor {
  path: string;
  kind: ResourceKind;
  binary: boolean;
  tooLarge: boolean;
}

export interface ResourceCapabilities<T extends ResourceDescriptor = ResourceDescriptor> {
  resource: T | null;
  kind: ResourceKind | null;
  /** A backend-addressable, workspace-relative file path. */
  path: string | null;
  realFile: boolean;
  editable: boolean;
  csv: boolean;
  dump: boolean;
  diff: boolean;
  database: boolean;
  table: boolean;
  largeFile: boolean;
  binary: boolean;
}

export interface ResourceCommandEligibility {
  save: boolean;
  formatDocument: boolean;
  inferCsvSchema: boolean;
  csvToSql: boolean;
  analyzeDump: boolean;
  cleanDump: boolean;
  dataTools: boolean;
  saveArtifact: boolean;
}

function isWorkspaceRelativeFilePath(path: string): boolean {
  if (!path || path.includes("\0")) return false;
  const normalized = path.replace(/\\/g, "/");
  if (normalized.startsWith("/") || normalized.endsWith("/") || /^[a-zA-Z]:/.test(normalized)) return false;
  const segments = normalized.split("/");
  return segments.every((segment) => segment !== "" && segment !== "." && segment !== "..");
}

/**
 * Describe what the active tab represents before exposing commands that call
 * workspace file APIs. A display key alone is never evidence that a resource
 * is a file: diff, database, and table tabs all use synthetic/path-like keys.
 */
export function resourceCapabilities<T extends ResourceDescriptor>(
  resource: T | null | undefined,
): ResourceCapabilities<T> {
  const item = resource ?? null;
  const realFile = item?.kind === "file" && isWorkspaceRelativeFilePath(item.path);
  const path = realFile ? item.path : null;
  const binary = realFile && item.binary;
  const largeFile = realFile && item.tooLarge;
  const dataFile = realFile && !binary;

  return {
    resource: item,
    kind: item?.kind ?? null,
    path,
    realFile,
    editable: realFile && !binary && !largeFile,
    csv: dataFile && /\.(csv|tsv)$/i.test(path ?? ""),
    dump: dataFile && /\.(sql|dump)$/i.test(path ?? ""),
    diff: item?.kind === "diff",
    database: item?.kind === "db",
    table: item?.kind === "table",
    largeFile,
    binary,
  };
}

export function activeResourceCapabilities<T extends ResourceDescriptor>(
  resources: readonly T[],
  activePath: string | null,
): ResourceCapabilities<T> {
  return resourceCapabilities(activePath ? resources.find((resource) => resource.path === activePath) : null);
}

/** One command policy shared by the application menu and its unit tests. */
export function resourceCommandEligibility(
  resource: ResourceCapabilities,
  toolBusy: boolean,
): ResourceCommandEligibility {
  return {
    save: resource.editable,
    formatDocument: resource.editable,
    inferCsvSchema: resource.csv && !toolBusy,
    csvToSql: resource.csv && !toolBusy,
    analyzeDump: resource.dump && !toolBusy,
    cleanDump: resource.dump && !toolBusy,
    dataTools: resource.csv || resource.dump,
    saveArtifact: resource.realFile,
  };
}
