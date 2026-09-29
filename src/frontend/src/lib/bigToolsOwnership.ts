export type BigToolsFileKind = "pending" | "csv" | "sql" | "unsupported";

export function bigToolsFileKind(detected: string): BigToolsFileKind {
  switch (detected.trim().toUpperCase()) {
    case "CSV":
    case "TSV":
      return "csv";
    case "SQL":
      return "sql";
    default:
      return "unsupported";
  }
}

export function unsupportedBigToolsTypeMessage(detected: string): string {
  const normalized = detected.trim().slice(0, 80);
  return normalized
    ? `Data tools support CSV, TSV, and SQL files; the backend detected ${normalized}.`
    : "Data tools support CSV, TSV, and SQL files; the backend could not detect a supported type.";
}

export interface BigToolsSqlAvailability {
  independentUnavailable: boolean;
  analysisDependentUnavailable: boolean;
}

/**
 * SQL replacement, reshaping, and regex harvesting own their source checks and
 * do not require the cached table analysis used by extraction-oriented tools.
 */
export function bigToolsSqlAvailability(
  baseUnavailable: boolean,
  analysisReady: boolean,
): BigToolsSqlAvailability {
  return {
    independentUnavailable: baseUnavailable,
    analysisDependentUnavailable: baseUnavailable || !analysisReady,
  };
}

export interface BigToolsRequestToken {
  readonly channel: string;
  readonly targetEpoch: number;
  readonly requestEpoch: number;
}

/**
 * Owns asynchronous work for one mounted Big Tools modal.
 *
 * A target epoch is never reused, so an A -> B -> A path sequence cannot make
 * the first A's response current again. Channels independently retain only
 * their newest request within the current target.
 */
export class BigToolsRequestGate {
  private targetEpoch = 0;
  private requestEpoch = 0;
  private readonly latestByChannel = new Map<string, number>();

  beginTarget(): number {
    this.targetEpoch++;
    this.latestByChannel.clear();
    return this.targetEpoch;
  }

  invalidateTarget(): void {
    this.targetEpoch++;
    this.latestByChannel.clear();
  }

  isTargetCurrent(targetEpoch: number): boolean {
    return targetEpoch === this.targetEpoch;
  }

  begin(channel: string): BigToolsRequestToken {
    const requestEpoch = ++this.requestEpoch;
    this.latestByChannel.set(channel, requestEpoch);
    return {
      channel,
      targetEpoch: this.targetEpoch,
      requestEpoch,
    };
  }

  isCurrent(token: BigToolsRequestToken): boolean {
    return (
      token.targetEpoch === this.targetEpoch &&
      this.latestByChannel.get(token.channel) === token.requestEpoch
    );
  }
}
