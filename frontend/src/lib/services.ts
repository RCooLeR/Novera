// Friendly re-exports of the generated Wails bindings, plus shared model types.
import { Service as Workspace } from "../../bindings/novera/internal/workspace";
import { Service as Settings } from "../../bindings/novera/internal/settings";
import { Service as Git } from "../../bindings/novera/internal/gitsvc";
import { Service as Term } from "../../bindings/novera/internal/terminal";
import { Service as LLM } from "../../bindings/novera/internal/llm";
import { Service as Agent } from "../../bindings/novera/internal/agent";
import { Service as Db } from "../../bindings/novera/internal/db";
import { Service as Watcher } from "../../bindings/novera/internal/watcher";
import { Service as Jobs } from "../../bindings/novera/internal/jobs";
import { Service as Artifacts } from "../../bindings/novera/internal/artifacts";
import { FileService as BigFile } from "../../bindings/novera/internal/bigfile";
import { Shell, SecretService } from "../../bindings/novera";

export { Workspace, Settings, Git, Term, LLM, Agent, Db, Watcher, Jobs, Artifacts, BigFile, Shell, SecretService };

// Big-file engine (ported from Quarry) — bounded windowed access to large files.
export type {
  Window as BigWindow,
  HexWindow,
  FileMeta as BigFileMeta,
  SearchHit,
  SearchAllResult,
  StagingState,
  StagedEdit,
  DiffWindow,
  SaveResult,
  TransformResult,
  CsvProfileResult,
  CsvInspectResult,
  CsvSchemaResult,
  SqlLintResult,
  SqlSummaryResult,
} from "../../bindings/novera/internal/bigfile";

export type {
  Profile as DbProfile,
  Table as DbTable,
  Column as DbColumn,
  QueryResult as DbQueryResult,
  TestResult as DbTestResult,
} from "../../bindings/novera/internal/db";

export type {
  Entry,
  FileContent,
  Info,
  WriteResult,
  SearchResult,
  SearchMatch,
  Diagnostic,
  DiagnosticsResult,
  TablePage,
  TableQuery,
  FileChunk,
} from "../../bindings/novera/internal/workspace";
export type { Settings as SettingsModel, Editor as EditorSettings } from "../../bindings/novera/internal/settings";
export type { AuditEntry } from "../../bindings/novera/internal/agent";
export type { Job } from "../../bindings/novera/internal/jobs";
export type { Artifact } from "../../bindings/novera/internal/artifacts";
export type { SchemaResult, SchemaColumn, DumpSummary, DumpTable, DumpTransform } from "../../bindings/novera/internal/datatools";
export type {
  Status as GitStatus,
  FileChange as GitFileChange,
  DiffContent as GitDiff,
  CommitResult,
} from "../../bindings/novera/internal/gitsvc";

// Normalise a thrown Wails error (which may be a string, Error, or object) into
// a readable message for the UI.
export function errMessage(err: unknown): string {
  if (err == null) return "Unknown error";
  if (typeof err === "string") return err;
  if (err instanceof Error) return err.message;
  if (typeof err === "object" && "message" in err) return String((err as { message: unknown }).message);
  return String(err);
}

// The Go workspace service returns this exact sentinel when a save would clobber
// a file that changed on disk since it was read.
export const STALE_MARKER = "file changed on disk";
