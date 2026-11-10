export const YOUTUBE_HELPER_VERSION: string;
export const MIN_YOUTUBE_HELPER_VERSION: string;
export const YOUTUBE_HELPER_SCRIPT_URL: string;

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

export interface YouTubeHelperProgressDetail {
  requestId: string;
  loaded: number;
  total: number;
  percent: number;
}

export interface YouTubeHelperDownloadPayload {
  url: string;
  filename: string;
  onProgress?: (detail: YouTubeHelperProgressDetail) => void;
  signal?: AbortSignal;
}

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
export function createUserscriptController(options?: {
  gmXmlhttpRequest?: (details: Record<string, unknown>) => unknown;
  gmDownload?: (details: Record<string, unknown>) => { abort?: () => void } | void;
  eventTarget?: EventTarget;
  storage?: Pick<Storage, "getItem" | "setItem"> | null;
  now?: () => number;
  version?: string;
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
): Promise<{ filename: string }>;
