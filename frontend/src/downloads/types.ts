/** Complete files only. Playlists, muxing and archives keep their own pipelines. */
export type DownloadTransport = "browser_native" | "browser_streaming" | "browser_blob";
export type DeliveryStatus = "queued" | "preparing" | "downloading" | "completed" | "failed" | "cancelled" | "handed-off" | "interrupted";

/** Metadata from this attempt's complete-file probe; never reused across attempts. */
export interface SourceProbe {
  url: string;
  attachment: { url: string; filename: string } | null;
}

export interface DownloadRequest {
  id: string;
  url: string;
  routes: string[];
  /** Decoded file size declared for the exact source URL, never for a sibling route. */
  sourceSizes?: Record<string, number>;
  filename: string;
  /** From the selected file's evidenced format, independent of the CDN's Content-Type. */
  formatMime?: string;
  startedAt?: number;
  probe?: SourceProbe;
}

export interface DownloadSnapshot {
  id: string;
  transport: DownloadTransport;
  status: DeliveryStatus;
  filename: string;
  url: string;
  loaded: number;
  total: number;
  speed: number;
  elapsed: number;
  averageSpeed: number;
  transferElapsed?: number;
  workerBuild?: string;
  workerInstance?: string;
  finishedAt?: number;
  recoveryDeadline?: number;
  recoveryExpired?: boolean;
  ownerGone?: boolean;
  /** Worker snapshots are ordered independently of message arrival. Legacy workers omit this. */
  revision?: number;
  /** Admission precedes the browser's attachment request. */
  delivery?: "waiting" | "ready" | "claimed";
  /** Observation trouble is not proof that the underlying transfer stopped. */
  observation?: "unknown" | "stalled";
  error?: string;
}

export type DownloadObserver = (snapshot: DownloadSnapshot) => void;

export class StreamingUnavailableError extends Error {
  readonly safeToFallback = true;
  constructor(message: string) { super(message); this.name = "StreamingUnavailableError"; }
}
