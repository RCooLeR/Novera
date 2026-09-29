export const BIG_TOOLS_COLUMN_DOM_LIMIT = 256;
export const BIG_TOOLS_WARNING_DOM_LIMIT = 64;
export const BIG_TOOLS_TABLE_PAGE_SIZE = 200;
export const BIG_TOOLS_IDENTIFIER_INPUT_LIMIT = 256;
export const BIG_TOOLS_VALUE_INPUT_LIMIT = 64 * 1024;
export const BIG_TOOLS_SQL_ROW_LIMIT = 10_000;

const MAX_CSV_PAYLOAD_COLUMNS = 1_024;
const MAX_CSV_PAYLOAD_WARNINGS = 4_096;
const MAX_CSV_DELIMITER_CANDIDATES = 16;
const MAX_SQL_PAYLOAD_TABLES = 10_000;
const MAX_SQL_LINT_FINDINGS = 8;

type PayloadObject = Record<string, unknown>;

export interface BigToolsCsvInspect {
  generation: number;
  delimiter: string;
  hasHeader: boolean;
  candidates: Array<{
    delimiter: string;
    name: string;
    columns: number;
    score: number;
  }>;
  warnings: string[];
}

export interface BigToolsCsvSchema {
  generation: number;
  columns: string[];
  warnings: string[];
}

export interface BigToolsCsvProfile {
  generation: number;
  columns: Array<{
    name: string;
    sqlType: string;
    null: number;
    distinct: number;
    distinctCapped: boolean;
    min: string;
    max: string;
  }>;
  recordsScanned: number;
  raggedRows: number;
  truncated: boolean;
}

export interface BigToolsSqlLint {
  findings: Array<{
    severity: string;
    title: string;
    detail: string;
  }>;
}

export interface BigToolsSqlTable {
  name: string;
  createOffset: number;
  insertOffset: number;
  bytes: number;
}

export class BigToolsPayloadError extends Error {
  constructor(field: string, expected: string) {
    super(`Invalid backend response at ${field}: expected ${expected}`);
    this.name = "BigToolsPayloadError";
  }
}

function objectValue(value: unknown, field: string): PayloadObject {
  if (value == null || typeof value !== "object" || Array.isArray(value)) {
    throw new BigToolsPayloadError(field, "an object");
  }
  return value as PayloadObject;
}

function boundedArray(value: unknown, field: string, maximum: number): unknown[] {
  if (value == null) return [];
  if (!Array.isArray(value)) throw new BigToolsPayloadError(field, "an array or null");
  if (value.length > maximum) {
    throw new BigToolsPayloadError(field, `at most ${maximum} entries`);
  }
  return value;
}

function stringValue(value: unknown, field: string): string {
  if (value == null) return "";
  if (typeof value !== "string") throw new BigToolsPayloadError(field, "a string");
  return value;
}

function finiteNumber(value: unknown, field: string): number {
  if (value == null) return 0;
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new BigToolsPayloadError(field, "a finite number");
  }
  return value;
}

function safeInteger(value: unknown, field: string, minimum: number): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum) {
    throw new BigToolsPayloadError(field, `a safe integer greater than or equal to ${minimum}`);
  }
  return value;
}

function sourceGeneration(value: unknown, field: string): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value <= 0) {
    throw new BigToolsPayloadError(field, "a positive safe integer");
  }
  return value;
}

function booleanValue(value: unknown, field: string): boolean {
  if (value == null) return false;
  if (typeof value !== "boolean") throw new BigToolsPayloadError(field, "a boolean");
  return value;
}

export function isValidBigToolsCsvDelimiter(delimiter: string): boolean {
  return (
    Array.from(delimiter).length === 1 &&
    delimiter !== "\0" &&
    delimiter !== "\r" &&
    delimiter !== "\n" &&
    delimiter !== '"' &&
    delimiter !== "\uFFFD"
  );
}

function stringArray(value: unknown, field: string, maximum: number): string[] {
  return boundedArray(value, field, maximum).map((item, index) =>
    stringValue(item, `${field}[${index}]`),
  );
}

export function normalizeBigToolsCsvInspect(value: unknown): BigToolsCsvInspect {
  const result = objectValue(value, "CsvInspectResult");
  return {
    generation: sourceGeneration(result.generation, "CsvInspectResult.generation"),
    delimiter: stringValue(result.delimiter, "CsvInspectResult.delimiter"),
    hasHeader: booleanValue(result.hasHeader, "CsvInspectResult.hasHeader"),
    candidates: boundedArray(
      result.candidates,
      "CsvInspectResult.candidates",
      MAX_CSV_DELIMITER_CANDIDATES,
    ).map((candidate, index) => {
      const item = objectValue(candidate, `CsvInspectResult.candidates[${index}]`);
      return {
        delimiter: stringValue(item.delimiter, `CsvInspectResult.candidates[${index}].delimiter`),
        name: stringValue(item.name, `CsvInspectResult.candidates[${index}].name`),
        columns: finiteNumber(item.columns, `CsvInspectResult.candidates[${index}].columns`),
        score: finiteNumber(item.score, `CsvInspectResult.candidates[${index}].score`),
      };
    }),
    warnings: stringArray(
      result.warnings,
      "CsvInspectResult.warnings",
      MAX_CSV_PAYLOAD_WARNINGS,
    ),
  };
}

export function normalizeBigToolsCsvSchema(value: unknown): BigToolsCsvSchema {
  const result = objectValue(value, "CsvSchemaResult");
  return {
    generation: sourceGeneration(result.generation, "CsvSchemaResult.generation"),
    columns: boundedArray(
      result.columns,
      "CsvSchemaResult.columns",
      MAX_CSV_PAYLOAD_COLUMNS,
    ).map((column, index) => {
      const item = objectValue(column, `CsvSchemaResult.columns[${index}]`);
      return stringValue(item.name, `CsvSchemaResult.columns[${index}].name`);
    }),
    warnings: stringArray(
      result.warnings,
      "CsvSchemaResult.warnings",
      MAX_CSV_PAYLOAD_WARNINGS,
    ),
  };
}

export function normalizeBigToolsCsvProfile(value: unknown): BigToolsCsvProfile {
  const result = objectValue(value, "CsvProfileResult");
  return {
    generation: sourceGeneration(result.generation, "CsvProfileResult.generation"),
    columns: boundedArray(
      result.columns,
      "CsvProfileResult.columns",
      MAX_CSV_PAYLOAD_COLUMNS,
    ).map((column, index) => {
      const item = objectValue(column, `CsvProfileResult.columns[${index}]`);
      return {
        name: stringValue(item.name, `CsvProfileResult.columns[${index}].name`),
        sqlType: stringValue(item.sqlType, `CsvProfileResult.columns[${index}].sqlType`),
        null: finiteNumber(item.null, `CsvProfileResult.columns[${index}].null`),
        distinct: finiteNumber(item.distinct, `CsvProfileResult.columns[${index}].distinct`),
        distinctCapped: booleanValue(
          item.distinctCapped,
          `CsvProfileResult.columns[${index}].distinctCapped`,
        ),
        min: stringValue(item.min, `CsvProfileResult.columns[${index}].min`),
        max: stringValue(item.max, `CsvProfileResult.columns[${index}].max`),
      };
    }),
    recordsScanned: finiteNumber(result.recordsScanned, "CsvProfileResult.recordsScanned"),
    raggedRows: finiteNumber(result.raggedRows, "CsvProfileResult.raggedRows"),
    truncated: booleanValue(result.truncated, "CsvProfileResult.truncated"),
  };
}

export function normalizeBigToolsSqlTables(value: unknown): BigToolsSqlTable[] {
  const result = objectValue(value, "SqlSummaryResult");
  return boundedArray(
    result.tables,
    "SqlSummaryResult.tables",
    MAX_SQL_PAYLOAD_TABLES,
  ).map((table, index) => {
    const item = objectValue(table, `SqlSummaryResult.tables[${index}]`);
    return {
      name: stringValue(item.name, `SqlSummaryResult.tables[${index}].name`),
      createOffset: safeInteger(
        item.createOffset,
        `SqlSummaryResult.tables[${index}].createOffset`,
        -1,
      ),
      insertOffset: safeInteger(
        item.insertOffset,
        `SqlSummaryResult.tables[${index}].insertOffset`,
        -1,
      ),
      bytes: safeInteger(item.bytes, `SqlSummaryResult.tables[${index}].bytes`, 0),
    };
  });
}

export interface BigToolsSqlTableCapabilities {
  extract: boolean;
  schema: boolean;
  data: boolean;
}

export function bigToolsSqlTableCapabilities(
  table: BigToolsSqlTable | null | undefined,
): BigToolsSqlTableCapabilities {
  return {
    extract: table != null,
    schema: table != null && table.createOffset >= 0,
    data: table != null && table.insertOffset >= 0,
  };
}

export function normalizeBigToolsSqlLint(value: unknown): BigToolsSqlLint {
  const result = objectValue(value, "SqlLintResult");
  return {
    findings: boundedArray(
      result.findings,
      "SqlLintResult.findings",
      MAX_SQL_LINT_FINDINGS,
    ).map((finding, index) => {
      const item = objectValue(finding, `SqlLintResult.findings[${index}]`);
      return {
        severity: stringValue(item.severity, `SqlLintResult.findings[${index}].severity`),
        title: stringValue(item.title, `SqlLintResult.findings[${index}].title`),
        detail: stringValue(item.detail, `SqlLintResult.findings[${index}].detail`),
      };
    }),
  };
}

/**
 * Bounds controlled text without leaving a dangling UTF-16 high surrogate.
 */
export function boundedBigToolsText(value: string, maximum: number): string {
  const bounded = value.slice(0, maximum);
  if (!bounded) return bounded;
  const last = bounded.charCodeAt(bounded.length - 1);
  return last >= 0xd800 && last <= 0xdbff ? bounded.slice(0, -1) : bounded;
}

export function boundedBigToolsInteger(
  value: string,
  minimum: number,
  maximum: number,
  fallback: number,
): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return fallback;
  return Math.min(maximum, Math.max(minimum, Math.trunc(parsed)));
}

export interface StringPage {
  matches: string[];
  items: string[];
  page: number;
  pageCount: number;
}

export function pageMatchingStrings(
  values: readonly string[],
  query: string,
  requestedPage: number,
  pageSize = BIG_TOOLS_TABLE_PAGE_SIZE,
): StringPage {
  const needle = query.trim().toLocaleLowerCase();
  const matches = needle
    ? values.filter((value) => value.toLocaleLowerCase().includes(needle))
    : [...values];
  const safePageSize = Math.max(1, Math.trunc(pageSize) || 1);
  const pageCount = Math.max(1, Math.ceil(matches.length / safePageSize));
  const page = Math.min(pageCount - 1, Math.max(0, Math.trunc(requestedPage) || 0));
  return {
    matches,
    items: matches.slice(page * safePageSize, (page + 1) * safePageSize),
    page,
    pageCount,
  };
}
