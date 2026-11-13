/**
 * 分段视频合并下载工具。
 *
 * 用于修复「只能下载单个切片」的问题：当解析结果携带 `segments`（多分片直链）
 * 或 URL 为 HLS 清单时，将 N 个分片依次拉取并拼接为完整视频后下载。
 *
 * 设计要点：
 * - 每段独立请求，记录每段已收字节与总字节，向外部汇报整体进度；
 * - 全部收齐后才拼接并触发下载，避免「只下到第一片」；
 * - 支持 HLS BYTERANGE 清单（fMP4：EXT-X-MAP 初始化段 + 分片字节区间），
 *   按区间用 Range 头精确抓取，避免把分片文件的头部字节混进合并结果；
 * - 支持 AbortSignal 取消；
 * - 提供按分片打包为 ZIP 的能力（配合 JSZip）。
 */

import { fetchMediaBlob, probeMediaLength, linkAbortSignals } from "../downloads/buffered";
import { resourceSource, sourceUrl, type DownloadTarget, type TargetContext } from "./mediaContract";
import { declaredSegments, fileResource, mediaExtension } from "./mediaDelivery";
import type { Resource } from "../types/media";

export { declaredSegments };
export const ARCHIVE_LIMIT_ERROR = "打包总大小超过 500 MB 上限，请分批或单文件下载";

/** 带可选字节区间的分片描述（HLS BYTERANGE / 普通整文件分片通用） */
export interface HlsSegment {
  url: string;
  /** Range 起始字节（含）；缺省表示整个文件 */
  start?: number;
  /** 区间字节数；与 start 同时存在时按 Range 抓取 */
  length?: number;
}

export interface SegmentProgress {
  loaded: number;
  total: number;
  percent: number;
  /** 当前正在下载的分片序号（1 起始） */
  segment: number;
  segmentCount: number;
}

export interface MergeOptions {
  signal?: AbortSignal;
  filename: string;
  onProgress?: (progress: SegmentProgress) => void;
}

function triggerDownload(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  // 延迟回收，确保下载已开始
  (setTimeout(() => URL.revokeObjectURL(url), 45_000) as { unref?: () => void })?.unref?.();
}

/** Retain the existing bounded retry count; cancellation never retries. */
async function fetchSegmentBlob(
  url: string,
  signal: AbortSignal | undefined,
  onChunk?: (loaded: number, total: number) => void,
  range?: { start: number; length: number },
  retries = 2,
  maxBytes?: number,
  maxBytesError?: string,
): Promise<Blob> {
  let lastError: unknown;
  for (let attempt = 0; attempt <= retries; attempt++) {
    if (signal?.aborted) throw signal.reason;
    try { return await fetchMediaBlob(url, { signal, onChunk, range, maxBytes, maxBytesError }); }
    catch (error) {
      if (signal?.aborted) throw signal.reason;
      if (maxBytesError && error instanceof Error && error.message === maxBytesError) throw error;
      lastError = error;
      if (attempt < retries) await new Promise<void>((resolve, reject) => {
        const abort = () => { clearTimeout(timer); reject(signal?.reason); };
        const timer = setTimeout(() => { signal?.removeEventListener("abort", abort); resolve(); }, 400 * (attempt + 1));
        signal?.addEventListener("abort", abort, { once: true });
        if (signal?.aborted) abort();
      });
    }
  }
  throw lastError instanceof Error ? lastError : new Error("分片下载失败");
}

/** 解析 BYTERANGE 值 `len[@offset]`；offset 缺省时延续上一区间末尾（RFC 8216） */
function parseByteRange(
  value: string,
  previousEndExclusive: number | null,
): { start: number; length: number } | null {
  const match = /^(\d+)(?:@(\d+))?$/.exec(value.trim());
  if (!match) return null;
  const length = Number(match[1]);
  const start =
    match[2] !== undefined ? Number(match[2]) : (previousEndExclusive ?? 0);
  return Number.isSafeInteger(start) &&
    start >= 0 &&
    Number.isSafeInteger(length) &&
    length > 0
    ? { start, length }
    : null;
}

/** Read only an explicitly declared, complete single-rendition HLS VOD.
 * Adaptive masters, external audio, encryption and discontinuities need a
 * processing executor; they cannot be silently flattened into a complete file.
 */
export async function resolveHlsSegments(
  url: string,
  signal?: AbortSignal,
): Promise<HlsSegment[] | null> {
  try {
    const manifest = await fetchMediaBlob(url, { signal, maxBytes: 2 * 1024 * 1024, referrerPolicy: "no-referrer" });
    const text = await manifest.text();
    if (
      !text.trimStart().startsWith("#EXTM3U") ||
      !text.includes("#EXT-X-ENDLIST") ||
      /#EXT-X-STREAM-INF|#EXT-X-MEDIA:|#EXT-X-KEY|#EXT-X-DISCONTINUITY/.test(
        text,
      )
    )
      return null;
    const toAbsolute = (uri: string) =>
      sourceUrl({ url: new URL(uri, url).href });
    const segments: HlsSegment[] = [];
    let previousEnd: number | null = null,
      previousUrl = "";
    let pending: string | null = null;
    for (const line of text.split(/\r?\n/).map((value) => value.trim())) {
      if (!line) continue;
      if (line.startsWith("#EXT-X-MAP:")) {
        const uri = /URI="([^"]+)"/.exec(line)?.[1];
        if (!uri || segments.length) return null;
        const rawRange = /BYTERANGE="([^"]+)"/.exec(line)?.[1];
        const range = rawRange ? parseByteRange(rawRange, null) : null;
        if (rawRange && !range) return null;
        segments.push({ url: toAbsolute(uri), ...(range || {}) });
      } else if (line.startsWith("#EXT-X-BYTERANGE:")) {
        if (pending) return null;
        pending = line.slice("#EXT-X-BYTERANGE:".length);
      } else if (!line.startsWith("#")) {
        const absolute = toAbsolute(line);
        if (
          pending &&
          !pending.includes("@") &&
          (previousEnd === null || absolute !== previousUrl)
        )
          return null;
        const range: { start: number; length: number } | null = pending
          ? parseByteRange(pending, previousEnd)
          : null;
        if (pending && !range) return null;
        segments.push({ url: absolute, ...(range || {}) });
        previousEnd = range ? range.start + range.length : null;
        previousUrl = absolute;
        pending = null;
        if (segments.length > 2000) return null;
      }
    }
    return segments.length && !pending ? segments : null;
  } catch (error) {
    if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
    return null;
  }
}

/** 归一化：字符串分片 → 整文件 HlsSegment */
function normalizeSegments(segments: (HlsSegment | string)[]): HlsSegment[] {
  return segments
    .map((s) => (typeof s === "string" ? ({ url: s } as HlsSegment) : s))
    .filter((s) => Boolean(s.url));
}

/** Optional sizes share a five-second budget and at most four in-flight HEADs. */
async function measureSegments(segments: HlsSegment[], signal?: AbortSignal): Promise<number[]> {
  const lengths = segments.map(s => s.length || 0);
  if (lengths.every(size => size > 0)) return lengths;
  const linked = linkAbortSignals([signal]);
  const timer = setTimeout(() => linked.controller.abort(new Error("分片元数据探测超时")), 5000);
  let next = 0, unknown = false;
  try {
    await Promise.all(Array.from({ length: Math.min(4, segments.length) }, async () => {
      while (!unknown && !linked.signal.aborted && next < segments.length) {
        const i = next++;
        if (lengths[i]) continue;
        try { lengths[i] = await probeMediaLength(segments[i].url, linked.signal); }
        catch { lengths[i] = 0; }
        if (!lengths[i]) unknown = true;
      }
    }));
    if (signal?.aborted) throw signal.reason;
    return lengths;
  } finally { clearTimeout(timer); linked.dispose(); linked.controller.abort(); }
}

/** Join byte-concatenable segments as ordered Blob parts, not a second full array.
 * ZIP may still materialize an entry for JSZip; this is not zero-memory streaming.
 */
export async function mergeSegments(
  segments: (HlsSegment | string)[],
  options?: {
    signal?: AbortSignal;
    onProgress?: (progress: SegmentProgress) => void;
    maxBytes?: number;
    maxBytesError?: string;
  },
): Promise<Blob> {
  const usable = normalizeSegments(segments);
  if (!usable.length || usable.length > 2000) throw new Error("分片数量无效");
  const { signal, onProgress, maxBytes = Number.MAX_SAFE_INTEGER, maxBytesError } = options || {};
  const lengths = await measureSegments(usable, signal);
  let totalBytes = lengths.every(size => size > 0) ? lengths.reduce((a, b) => a + b, 0) : 0;
  if (!Number.isSafeInteger(totalBytes)) totalBytes = 0;
  if (totalBytes > maxBytes) throw new Error(maxBytesError || "资源超过允许的读取范围");
  const parts: Blob[] = [];
  let totalLoaded = 0;
  for (let i = 0; i < usable.length; i++) {
    const segment = usable[i];
    const remaining = maxBytes - totalLoaded;
    if (remaining <= 0) throw new Error(maxBytesError || "资源超过允许的读取范围");
    const part = await fetchSegmentBlob(segment.url, signal, loaded => onProgress?.({
      loaded: totalLoaded + loaded, total: totalBytes,
      percent: totalBytes ? Math.min(99, Math.floor((totalLoaded + loaded) / totalBytes * 100)) : Math.floor(i / usable.length * 100),
      segment: i + 1, segmentCount: usable.length,
    }), segment.start !== undefined && segment.length !== undefined ? { start: segment.start, length: segment.length } : undefined, 2, remaining, maxBytesError);
    parts.push(part);
    totalLoaded += part.size;
    // HEAD is advisory, not proof of a later GET's byte length.
    if (totalBytes && part.size !== lengths[i]) totalBytes = 0;
    onProgress?.({ loaded: totalLoaded, total: totalBytes,
      percent: Math.min(99, Math.floor((i + 1) / usable.length * 100)), segment: i + 1, segmentCount: usable.length });
  }
  if (signal?.aborted) throw signal.reason;
  const result = new Blob(parts);
  onProgress?.({ loaded: totalLoaded, total: totalLoaded, percent: 100, segment: usable.length, segmentCount: usable.length });
  return result;
}

/**
 * 合并分片并下载为单个文件。
 * @param segments 分片列表（按播放顺序，支持 BYTERANGE 区间）
 * @returns 下载是否成功触发
 */
export async function mergeAndDownload(
  segments: (HlsSegment | string)[],
  options: MergeOptions,
): Promise<boolean> {
  const { signal, filename } = options;
  const merged = await mergeSegments(segments, {
    signal,
    onProgress: options.onProgress,
  });
  if (signal?.aborted) throw signal.reason;
  triggerDownload(merged, filename);
  return true;
}

/** 把分片列表打包为 ZIP（依赖调用方传入的 zip 工厂） */
export async function appendSegmentsToZip(
  zip: { file: (name: string, data: Uint8Array | ArrayBuffer) => void },
  segments: (HlsSegment | string)[],
  baseName: string,
  signal?: AbortSignal,
): Promise<void> {
  const usable = normalizeSegments(segments);
  for (let i = 0; i < usable.length; i++) {
    const segment = usable[i];
    const buf = await fetchSegmentBlob(
      segment.url,
      signal,
      undefined,
      segment.start !== undefined && segment.length !== undefined
        ? { start: segment.start, length: segment.length }
        : undefined,
    );
    const ext = /\.([a-z0-9]+)(\?|$)/i.exec(segment.url)?.[1] || "mp4";
    zip.file(`${baseName}_part${String(i + 1).padStart(2, "0")}.${ext}`, await buf.arrayBuffer());
  }
}

export interface ArchiveEntryItem {
  filename: string;
  data: Blob | Uint8Array<ArrayBuffer>;
}

async function archiveResourceEntry(
  resource: Resource,
  name: string,
  apiContext: TargetContext,
  signal: AbortSignal,
  maxBytes: number,
): Promise<ArchiveEntryItem> {
  const delivery = resource.delivery;
  if (delivery.kind === "file") {
    const target = fileResource(resource, apiContext);
    if ((target.source.size_bytes || 0) > maxBytes) throw new Error(ARCHIVE_LIMIT_ERROR);
    const data = await fetchMediaBlob(target.url, {
      signal,
      expectedSize: target.source.size_bytes,
      maxBytes,
      maxBytesError: ARCHIVE_LIMIT_ERROR,
    });
    return {
      filename: `${name}.${mediaExtension(resource.metadata, delivery, data.type)}`,
      data,
    };
  }
  if (delivery.kind === "hls" || delivery.kind === "segments") {
    const resolvedSegments = await declaredSegments(delivery, apiContext, signal);
    if (signal.aborted) throw new DOMException("Aborted", "AbortError");
    return {
      filename: `${name}.${mediaExtension(resource.metadata, delivery)}`,
      data: await mergeSegments(resolvedSegments, {
        signal,
        maxBytes,
        maxBytesError: ARCHIVE_LIMIT_ERROR,
      }),
    };
  }
  throw new Error("该资源需要 DASH 处理，当前暂不支持");
}

export function assertArchiveKnownSizeLimit(
  targets: DownloadTarget[],
  apiContext: TargetContext,
  maxArchiveBytes: number,
): void {
  let upfrontKnown = 0;
  for (const snapTarget of targets) {
    if (snapTarget.kind === "file") {
      upfrontKnown += snapTarget.source.size_bytes || 0;
    } else if (snapTarget.kind === "pair" && snapTarget.variant.delivery.kind === "pair") {
      for (const res of snapTarget.variant.delivery.resources) {
        if (res.delivery.kind === "file") {
          upfrontKnown += resourceSource(res, apiContext)?.size_bytes || 0;
        }
      }
    }
    if (upfrontKnown > maxArchiveBytes) throw new Error(ARCHIVE_LIMIT_ERROR);
  }
}

export async function collectArchiveTargetEntries(params: {
  itemId: string;
  target: Extract<DownloadTarget, { variant: unknown }>;
  base: string;
  apiContext: TargetContext;
  signal: AbortSignal;
  maxArchiveBytes: number;
  currentBytes: number;
}): Promise<{ entries: ArchiveEntryItem[]; totalBytes: number }> {
  const { itemId, target, base, apiContext, signal, maxArchiveBytes } = params;
  let archiveBytes = params.currentBytes;
  const entries: ArchiveEntryItem[] = [];
  const push = (entry: ArchiveEntryItem) => {
    archiveBytes += entry.data instanceof Blob ? entry.data.size : entry.data.byteLength;
    if (archiveBytes > maxArchiveBytes) throw new Error(ARCHIVE_LIMIT_ERROR);
    entries.push(entry);
  };
  const delivery = target.variant.delivery;
  if (target.kind === "file") {
    const remaining = maxArchiveBytes - archiveBytes;
    if (remaining <= 0 || (target.source.size_bytes || 0) > remaining) {
      throw new Error(ARCHIVE_LIMIT_ERROR);
    }
    const data = await fetchMediaBlob(target.url, {
      signal,
      expectedSize: target.source.size_bytes,
      maxBytes: remaining,
      maxBytesError: ARCHIVE_LIMIT_ERROR,
    });
    push({
      filename: `${base}.${mediaExtension(target.variant.metadata, delivery.kind === "file" ? delivery : undefined, data.type)}`,
      data,
    });
  } else if (delivery.kind === "pair") {
    for (const resource of delivery.resources) {
      const remaining = maxArchiveBytes - archiveBytes;
      if (remaining <= 0) throw new Error(ARCHIVE_LIMIT_ERROR);
      push(await archiveResourceEntry(resource, `${base}_${resource.role}`, apiContext, signal, remaining));
    }
  } else if (delivery.kind !== "mux") {
    const remaining = maxArchiveBytes - archiveBytes;
    if (remaining <= 0) throw new Error(ARCHIVE_LIMIT_ERROR);
    push(
      await archiveResourceEntry(
        { id: itemId, role: "media", metadata: target.variant.metadata, delivery },
        base,
        apiContext,
        signal,
        remaining,
      ),
    );
  } else {
    throw new Error("音视频合并尚未支持");
  }
  return { entries, totalBytes: archiveBytes };
}
