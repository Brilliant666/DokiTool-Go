import type { MediaMetadata } from "../types/media";

import { extensionsFor, mediaTypes } from "../../public/downloads/media-format.js";
export { extensionOf, extensionsFor, isSafeFilename, mimeType } from "../../public/downloads/media-format.js";

/** A generated filename alone cannot tell the transport whether its suffix was a guess. */
export function confirmedFormatMime(metadata?: MediaMetadata): string | undefined {
  if (!metadata) return;
  const fields = ["container", "extension", "mime_type"] as const;
  if (fields.some(field => metadata.field_conflicts?.[field])) return;
  const fact = (field: typeof fields[number]) => {
    const evidence = metadata.evidence === "mixed" ? metadata.field_evidence?.[field] : metadata.evidence;
    return evidence === "probe" || evidence === "upstream" ? metadata[field]?.toLowerCase() : undefined;
  };
  const type = fact("mime_type");
  const suffixes = [fact("extension"), fact("container")].filter((value): value is string => Boolean(value));
  if (!type && !suffixes.length) return;
  const candidates = (type ? [type] : mediaTypes).filter(key => {
    const extensions = extensionsFor(key);
    return extensions.length && suffixes.every(value => extensions.includes(value));
  });
  return candidates.length === 1 ? candidates[0] : undefined;
}

export function mediaUrl(value: string): string {
  if (!/^https?:\/\//i.test(value) && !/^\/(?!\/)/.test(value)) throw new Error("资源地址必须是 HTTP(S) 完整文件链接");
  const url = new URL(value, typeof location === "undefined" ? "https://dokitool.invalid/" : location.href);
  if (url.username || url.password || /\.(?:m3u8|mpd)(?:$|\/)/i.test(url.pathname)) {
    throw new Error("该地址不是可直接下载的完整媒体文件");
  }
  return url.href;
}

/** Retain the site's own access cookies, never send site credentials to a CDN. */
export function mediaCredentials(url: string): RequestCredentials {
  return typeof location !== "undefined" && new URL(url, location.href).origin === new URL(location.href).origin
    ? "same-origin" : "omit";
}

export function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
}

/** Bound a single network wait, without counting time spent waiting on the consumer. */
export async function networkWait<T>(operation: Promise<T>, controller: AbortController, timeoutMs: number): Promise<T> {
  let timer: ReturnType<typeof setTimeout>;
  let rejectAbort: () => void;
  const aborted = new Promise<never>((_, reject) => {
    rejectAbort = () => reject(controller.signal.reason instanceof Error ? controller.signal.reason : new DOMException("Aborted", "AbortError"));
    controller.signal.addEventListener("abort", rejectAbort, { once: true });
    timer = setTimeout(() => controller.abort(new Error(`资源超过 ${timeoutMs / 1000} 秒没有返回数据`)), timeoutMs);
    if (controller.signal.aborted) rejectAbort();
  });
  try { return await Promise.race([operation, aborted]); }
  finally {
    clearTimeout(timer!);
    controller.signal.removeEventListener("abort", rejectAbort!);
  }
}
