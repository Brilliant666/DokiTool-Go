import type { DeliveryStatus, DownloadTransport } from "../downloads/types";

export interface Source {
  id?: string;
  url: string;
  access?: "direct" | "relay";
  expires_at?: string;
  size_bytes?: number;
  availability?: "unknown" | "available" | "unavailable";
  browser_fetch?: "unknown" | "allowed" | "blocked";
}
export interface MediaMetadata {
  width?: number;
  height?: number;
  duration_ms?: number;
  fps?: number;
  hdr_type?: "sdr" | "hdr10" | "hdr10_plus" | "hlg" | "dolby_vision" | "hdr_unknown";
  bitrate_bps?: number;
  video_bitrate_bps?: number;
  audio_bitrate_bps?: number;
  video_codec?: string;
  audio_codec?: string;
  has_audio?: boolean;
  audio_language?: string;
  audio_language_role?: "main" | "alternate";
  audio_channels?: number;
  sample_rate_hz?: number;
  container?: string;
  extension?: string;
  mime_type?: string;
  evidence?: "upstream" | "probe" | "mixed" | "unknown";
  field_evidence?: Record<string, "upstream" | "probe">;
  /** No scalar fact is authoritative while this media identity has conflicting observations. */
  field_conflicts?: Record<string, { value: number | string | boolean; evidence: "upstream" | "probe" }[]>;
}
export interface FileDelivery {
  kind: "file";
  sources: Source[];
}
export interface OutputFormat {
  container: string;
  extension: string;
  mime_type?: string;
}
export interface ManifestDelivery {
  kind: "hls" | "dash";
  sources: Source[];
  scope: "single" | "adaptive";
  selection?: {
    video_representation_id?: string;
    audio_representation_id?: string;
    audio_group_id?: string;
  };
  output?: OutputFormat;
}
export interface Chunk {
  sources: Source[];
  byte_range?: { start: number; end_inclusive: number };
}
export interface SegmentDelivery {
  kind: "segments";
  init?: Chunk;
  pieces: (Chunk & { index: number; duration_ms?: number })[];
  join: "byte_concat" | "remux";
  join_evidence?: "validated_fragment_layout";
  output?: OutputFormat;
}
export type LeafDelivery = FileDelivery | ManifestDelivery | SegmentDelivery;
export interface Resource {
  id: string;
  role: "video" | "audio" | "image" | "motion" | "subtitle" | "media" | "muxed";
  metadata?: MediaMetadata;
  delivery: LeafDelivery;
}
export interface PairDelivery {
  kind: "pair";
  resources: Resource[];
}
export interface MuxDelivery {
  kind: "mux";
  resources: Resource[];
  output: OutputFormat;
}
export type Delivery = LeafDelivery | PairDelivery | MuxDelivery;
export type Capability =
  | "hls"
  | "dash"
  | "mux"
  | "pair"
  | "segment_remux"
  | "segment_byte_concat";
export interface ReadyVariant {
  state?: "ready";
  id: string;
  label?: string;
  provenance?: "original_endpoint" | "transcode" | "unknown";
  metadata?: MediaMetadata;
  delivery: Delivery;
}
export interface DeferredVariant {
  id: string;
  state: "deferred";
  label?: string;
  provenance?: "original_endpoint" | "transcode" | "unknown";
  reason: { code: string; message: string };
}
export type Variant = ReadyVariant | DeferredVariant;
export interface DefaultTarget {
  variant_id: string;
  source_id?: string;
  url: string;
  access?: "direct" | "relay";
  expires_at?: string;
}
export interface MediaError {
  code: string;
  message: string;
  retryable?: boolean;
  retry_after_seconds?: number;
}
export interface ItemBase {
  id: string;
  type: "video" | "image" | "live_photo" | "audio";
  index?: number;
  title?: string;
  source_url?: string;
  cover?: Source;
  duration_ms?: number;
}
export interface ReadyItem extends ItemBase {
  state?: "ready";
  variants: Variant[];
  default: DefaultTarget | null;
  default_unavailable_reason?:
    | "processing_required"
    | "needs_refresh"
    | "needs_resolve"
    | "unavailable";
  resolve_url?: string;
  higher_quality_variants?: { variant_id: string; requires: Capability[] }[];
}
export interface DeferredItem extends ItemBase {
  state: "deferred";
  default: null;
  default_unavailable_reason: "needs_resolve";
  resolve_url: string;
}
export interface UnavailableItem extends ItemBase {
  state: "unavailable";
  default: null;
  default_unavailable_reason: "unavailable";
  error: MediaError;
}
export type MediaItem = ReadyItem | DeferredItem | UnavailableItem;
export interface Author {
  id?: string;
  name?: string;
  description?: string;
  url?: string;
  avatar?: Source;
}
export interface MediaGroup {
  id: string;
  kind: "multipart" | "album" | "collection";
  item_ids: string[];
  title?: string;
  total_count?: number;
  complete?: boolean;
}
export interface Attachment {
  id: string;
  role: "background_audio" | "subtitle";
  resource: Resource;
  title?: string;
  language?: string;
  author?: Author;
  cover?: Source;
}
export interface UnifiedMedia {
  platform: string;
  content_id?: string;
  source_url?: string;
  items: MediaItem[];
  default_item_id?: string;
  generated_at?: string;
  refresh_url?: string;
  title?: string;
  description?: string;
  author?: Author;
  cover?: Source;
  published_at?: string;
  statistics?: Partial<
    Record<
      "plays" | "views" | "likes" | "comments" | "shares" | "saves",
      { value: number; state?: "known" | "unverified" }
    >
  >;
  tags?: string[];
  groups?: MediaGroup[];
  attachments?: Attachment[];
  extra?: Record<string, unknown>;
}
export type MediaEnvelope =
  | {
      schema_version: "1.0";
      code: "OK";
      data: UnifiedMedia;
      request_id?: string;
      warnings?: { code: string; message: string; item_id?: string }[];
    }
  | {
      schema_version: "1.0";
      code: string;
      error: MediaError;
      request_id?: string;
    };
export interface MediaSelection {
  variant_id: string;
  source_id?: string;
}

export type ToastType = "success" | "error" | "warning" | "info";
export interface ToastMessage {
  id: string;
  message: string;
  type: ToastType;
}
export type DownloadStatus = DeliveryStatus;
export interface DownloadTask {
  deliveryId?: string;
  transport?: DownloadTransport;
  id: number;
  filename: string;
  url?: string;
  status: DownloadStatus;
  percent: number;
  loaded: number;
  total: number;
  speed: number;
  /** Seconds from the beginning of this attempt, including preparation/waiting. */
  elapsed?: number;
  averageSpeed?: number;
  /** Active transfer duration, excluding preparation and queue waits. */
  transferElapsed?: number;
  observation?: "unknown" | "stalled";
  workerBuild?: string;
  workerInstance?: string;
  recoveryDeadline?: number;
  recoveryExpired?: boolean;
  ownerGone?: boolean;
  /** Bytes actually received, including failed routes when retried. */
  transferred?: number;
  progressKind?: "bytes" | "segments" | "archive";
  /** Manual source-link capability, not verified native attachment eligibility. */
  nativeAvailable?: boolean;
  /** In-memory capability: refresh the owned, pinned complete-file task once. */
  refreshAvailable?: boolean;
  error: string | null;
  statusText: string;
  show: boolean;
  abortController: AbortController | null;
  [key: string]: unknown;
}
export type DownloadInput = Partial<Omit<DownloadTask, "id">> &
  Pick<DownloadTask, "filename">;
export interface Particle {
  x: number;
  y: number;
  vx: number;
  vy: number;
  size: number;
  opacity: number;
}
