import type { Attachment, ReadyVariant, Source, Variant } from "../types/media";

export const YOUTUBE_HELPER_VERSION: string;
export const MIN_YOUTUBE_HELPER_VERSION: string;
export const YOUTUBE_HELPER_SCRIPT_URL: string;
export const YOUTUBE_OPFS_TEMP_PREFIX: string;
export const YOUTUBE_FILE_PICKER_ID: string;
export const DEFAULT_OPFS_CLEANUP_DELAY_MS: number;
export const OPFS_QUOTA_HEADROOM_FACTOR: number;

export const DEFAULT_MUX_CHUNK_BYTES: number;
export const DEFAULT_MUX_VIDEO_CONCURRENCY: number;
export const DEFAULT_MUX_AUDIO_CONCURRENCY: number;
export const DEFAULT_MUX_MAX_RETRIES: number;
export const MAX_IN_MEMORY_MUX_BYTES: number;

export const YOUTUBE_HELPER_REQUEST_EVENT: string;
export const YOUTUBE_HELPER_RESPONSE_EVENT: string;
export const YOUTUBE_HELPER_PROGRESS_EVENT: string;

export const YOUTUBE_SW_DATA_URL: string;
export const YOUTUBE_PLAYER_API_URL: string;
export const VISITOR_DATA_TTL_MS: number;
export const MAX_HELPER_TITLE_CHARS: number;

export interface YouTubeHelperClientInfo {
  name: string;
  version: string;
}

export interface YouTubeHelperContextResponse {
  player_id: string;
  signature_timestamp: number;
  client: YouTubeHelperClientInfo;
  expires_at: string;
}

export interface YouTubeHelperPingResult {
  installed: boolean;
  version: string | null;
  supported: boolean;
}

export interface YouTubeHelperPlayerPayload {
  video_id: string;
  signature_timestamp: number;
  client: YouTubeHelperClientInfo;
}

export interface YouTubeHelperVideoDetails {
  title?: string;
  author?: string;
  lengthSeconds?: number;
  thumbnail?: string;
}

export interface YouTubeHelperPlayerResult {
  playabilityStatus: {
    status: string;
    reason?: string;
  };
  streamingData: {
    formats: Record<string, unknown>[];
    adaptiveFormats: Record<string, unknown>[];
  } | null;
  videoDetails?: YouTubeHelperVideoDetails;
}

export type YouTubeHelperSaveMode =
  | "opfs"
  | "stream_picker"
  | "memory_buffer"
  | "gm_download";

export interface YouTubeHelperProgressDetail {
  requestId: string;
  loaded: number;
  total: number;
  percent: number;
  mode?: YouTubeHelperSaveMode;
}

export interface YouTubeHelperDownloadPayload {
  url: string;
  filename: string;
  size?: number;
  taskId?: string;
  onProgress?: (detail: YouTubeHelperProgressDetail) => void;
  signal?: AbortSignal;
}

export interface YouTubeHelperMuxDownloadPayload {
  videoUrl: string;
  audioUrl: string;
  videoSize?: number;
  audioSize?: number;
  filename: string;
  outputContainer: "mp4" | "webm";
  taskId?: string;
  onProgress?: (detail: YouTubeHelperProgressDetail) => void;
  signal?: AbortSignal;
}

export type YouTubeMuxPairResult =
  | {
      ok: true;
      outputContainer: "mp4" | "webm";
      outputExtension: "mp4" | "webm";
      outputMimeType: "video/mp4" | "video/webm";
      videoVariant: ReadyVariant;
      audioVariant: ReadyVariant;
      videoSource: Source;
      audioSource: Source;
    }
  | {
      ok: false;
      reason: string;
    };

export function isValidYouTubeVideoId(videoId: unknown): videoId is string;
export function extractYouTubeVideoId(rawUrl: string): string;
export function isValidSignatureTimestamp(sts: unknown): sts is number;
export function isValidTVHTML5Client(
  client: unknown,
): client is YouTubeHelperClientInfo;
export function isValidGooglevideoPlaybackUrl(rawUrl: unknown): rawUrl is string;
export function sanitizeHelperFilename(rawName: unknown): {
  ok: boolean;
  filename: string;
  error?: string;
};
export function extractUrlClen(rawUrl: unknown): number;
export function selectYouTubeMuxPair(
  videoVariant: Variant | null | undefined,
  allVariants: Variant[] | null | undefined,
  attachments?: Attachment[] | null,
): YouTubeMuxPairResult;
export function compareSemver(a: string, b: string): number;
export function isHelperVersionSupported(
  version: string,
  minVersion?: string,
): boolean;
export function isValidVisitorDataToken(token: unknown): token is string;
export function extractVisitorDataFromSwResponse(rawText: string): string;
export function sanitizeHelperText(raw: unknown, maxChars?: number): string;
export function isValidYouTubeThumbnailUrl(rawUrl: unknown): rawUrl is string;
export function sanitizeVideoDetails(
  rawDetails: unknown,
): YouTubeHelperVideoDetails | null;
export function buildTVHTML5PlayerPayload(params: {
  video_id: string;
  signature_timestamp: number;
  client: YouTubeHelperClientInfo;
  visitorData?: string;
}): Record<string, unknown>;
export function sanitizePlayerApiResponse(
  raw: unknown,
): YouTubeHelperPlayerResult;
export function cleanupLeftoverYouTubeOpfsTempFiles(options?: {
  storage?: StorageManager | Record<string, unknown> | null;
  pageWindow?: Window | typeof globalThis;
}): Promise<number>;
export function createUserscriptController(options?: {
  gmXmlhttpRequest?: (details: Record<string, unknown>) => unknown;
  gmDownload?: (details: Record<string, unknown>) => { abort?: () => void } | void;
  eventTarget?: EventTarget;
  pageWindow?: Window | typeof globalThis;
  opfsStorage?: StorageManager | Record<string, unknown> | null;
  storage?: Pick<Storage, "getItem" | "setItem"> | null;
  now?: () => number;
  version?: string;
  mediabunny?: Record<string, unknown> | null;
  muxConfig?: {
    chunkSize?: number;
    videoConcurrency?: number;
    audioConcurrency?: number;
    maxRetries?: number;
    retryBaseDelayMs?: number;
    chunkTimeoutMs?: number;
    maxBufferBytes?: number;
    opfsCleanupDelayMs?: number;
  };
  saveBuffer?: (
    buffer: ArrayBuffer,
    filename: string,
    mimeType: string,
  ) => void | Promise<void>;
  saveOpfsFile?: (
    file: File | Blob,
    filename: string,
    mimeType: string,
    tempName: string,
  ) => void | Promise<void>;
}): {
  mount: () => () => void;
  handleRequestObject: (message: unknown) => Promise<Record<string, unknown> | null>;
  getVisitorData: () => Promise<string>;
};
export function sendHelperRequest(
  action: string,
  payload?: Record<string, unknown>,
  options?: {
    eventTarget?: EventTarget;
    timeoutMs?: number;
    signal?: AbortSignal;
    onProgress?: (detail: YouTubeHelperProgressDetail) => void;
  },
): Promise<any>;
export function pingYouTubeHelper(options?: {
  timeoutMs?: number;
  eventTarget?: EventTarget;
}): Promise<YouTubeHelperPingResult>;
export function requestYouTubePlayerViaHelper(
  payload: YouTubeHelperPlayerPayload,
  options?: {
    timeoutMs?: number;
    signal?: AbortSignal;
    eventTarget?: EventTarget;
  },
): Promise<YouTubeHelperPlayerResult>;
export function downloadViaYouTubeHelper(
  payload: YouTubeHelperDownloadPayload,
  options?: {
    eventTarget?: EventTarget;
  },
): Promise<{ filename: string; mode?: YouTubeHelperSaveMode }>;
export function muxDownloadViaYouTubeHelper(
  payload: YouTubeHelperMuxDownloadPayload,
  options?: {
    eventTarget?: EventTarget;
  },
): Promise<{ filename: string; mode: YouTubeHelperSaveMode }>;
