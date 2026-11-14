import type { Source } from "../types/media";
import {
  sourceAvailable,
  sourceRoutes,
  sourceUrl,
  type TargetContext,
} from "../utils/mediaContract";

export const ROUTE_PROBE_MAX_SOURCES = 3;
export const ROUTE_PROBE_BYTES = 512 * 1024;
export const ROUTE_PROBE_LARGE_FILE_THRESHOLD_BYTES = 50 * 1024 * 1024;
export const ROUTE_PROBE_LARGE_BYTES = 2 * 1024 * 1024;
export const ROUTE_PROBE_TIMEOUT_MS = 3000;

const BAD_PROBE_CONTENT_TYPE =
  /\b(?:text\/html|application\/xhtml\+xml|application\/(?:vnd\.apple\.|x-)?mpegurl|audio\/mpegurl|application\/dash\+xml|application\/json)\b/i;
const BAD_PROBE_PREFIX = /^\s*(?:<!doctype\b|<html\b|#EXTM3U\b|\{"|\[\{)/i;

export interface RouteProbeObservation {
  source: Source;
  url: string;
  ok: boolean;
  bytesRead: number;
  elapsedMs: number;
  bytesPerSecond: number;
}

export interface RouteProbeOptions {
  maxSources?: number;
  probeBytes?: number;
  sizeBytes?: number;
  timeoutMs?: number;
  fetchImpl?: typeof fetch;
  nowFn?: () => number;
}

export interface RouteProbeResult {
  didProbe: boolean;
  primary: Source | undefined;
  url: string;
  routes: string[];
  orderedSources: Source[];
  observations: RouteProbeObservation[];
}

function isDirectCorsAllowed(source: Source): boolean {
  return (
    source.browser_fetch === "allowed" &&
    (!source.access || source.access === "direct")
  );
}

function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) {
    throw signal.reason instanceof Error
      ? signal.reason
      : new DOMException("Aborted", "AbortError");
  }
}

function asciiPrefix(bytes: Uint8Array): string {
  let text = "";
  const limit = Math.min(bytes.byteLength, 64);
  for (let i = 0; i < limit; i++) {
    const code = bytes[i];
    if (code === 0) break;
    text += code < 128 ? String.fromCharCode(code) : " ";
  }
  return text;
}

function buildRoutesForOrder(
  orderedSources: Source[],
  context: TargetContext,
): string[] {
  const seen = new Set<string>();
  const routes: string[] = [];
  for (const source of orderedSources) {
    for (const route of sourceRoutes(source, context)) {
      if (!seen.has(route)) {
        seen.add(route);
        routes.push(route);
      }
    }
  }
  return routes;
}

function positiveSizeBytes(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? value
    : undefined;
}

function maxPositiveSourceSize(sources: Source[]): number | undefined {
  let max: number | undefined;
  for (const source of sources) {
    const size = positiveSizeBytes(source?.size_bytes);
    if (size !== undefined && (max === undefined || size > max)) {
      max = size;
    }
  }
  return max;
}

export function resolveRouteProbeBytes(
  options: Pick<RouteProbeOptions, "probeBytes" | "sizeBytes"> = {},
  sources: Source[] = [],
  available: Source[] = sources,
): number {
  if (
    options.probeBytes !== undefined &&
    Number.isFinite(options.probeBytes)
  ) {
    return Math.max(1, Math.floor(options.probeBytes));
  }
  const knownSizeBytes =
    positiveSizeBytes(options.sizeBytes) ??
    maxPositiveSourceSize(available) ??
    maxPositiveSourceSize(sources);
  if (
    knownSizeBytes !== undefined &&
    knownSizeBytes > ROUTE_PROBE_LARGE_FILE_THRESHOLD_BYTES
  ) {
    return ROUTE_PROBE_LARGE_BYTES;
  }
  return ROUTE_PROBE_BYTES;
}

export function shouldProbeRoutes(
  sources: Source[],
  context: TargetContext = {},
  maxSources = ROUTE_PROBE_MAX_SOURCES,
): boolean {
  const available = sources.filter((source) =>
    sourceAvailable(source, context.now),
  );
  if (available.length <= 1 || maxSources <= 0) return false;
  const eligible = available.filter(isDirectCorsAllowed).slice(0, maxSources);
  if (eligible.length === 0) return false;
  if (eligible.length === 1 && eligible[0] === available[0]) return false;
  return true;
}

async function probeSingleSource(
  source: Source,
  context: TargetContext,
  signal: AbortSignal,
  probeBytes: number,
  timeoutMs: number,
  fetchImpl: typeof fetch,
  nowFn: () => number,
): Promise<RouteProbeObservation> {
  throwIfAborted(signal);
  const url = sourceUrl(source, context);
  const attempt = new AbortController();
  let timedOut = false;
  const onParentAbort = () => attempt.abort(signal.reason);
  signal.addEventListener("abort", onParentAbort, { once: true });
  const timer = setTimeout(() => {
    timedOut = true;
    attempt.abort();
  }, timeoutMs);

  let transferStartedAt: number | undefined;
  let bytesRead = 0;
  let checkedPrefix = false;
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;

  try {
    const response = await fetchImpl(url, {
      method: "GET",
      headers: { Range: `bytes=0-${probeBytes - 1}` },
      mode: "cors",
      credentials: "omit",
      referrerPolicy: "no-referrer",
      cache: "no-store",
      signal: attempt.signal,
    });
    throwIfAborted(signal);
    transferStartedAt = nowFn();

    const validStatus =
      response.status === 206
        ? response.headers.has("content-range")
        : response.status === 200;
    const contentType = response.headers.get("content-type") || "";
    if (
      response.type === "opaque" ||
      !validStatus ||
      BAD_PROBE_CONTENT_TYPE.test(contentType) ||
      !response.body ||
      typeof response.body.getReader !== "function"
    ) {
      const elapsedMs = Math.max(1, nowFn() - transferStartedAt);
      await response.body?.cancel?.().catch(() => {});
      return {
        source,
        url,
        ok: false,
        bytesRead: 0,
        elapsedMs,
        bytesPerSecond: 0,
      };
    }

    reader = response.body.getReader();
    while (bytesRead < probeBytes) {
      const { done, value } = await reader.read();
      if (done) break;
      if (!value || value.byteLength === 0) continue;
      if (!checkedPrefix) {
        checkedPrefix = true;
        if (BAD_PROBE_PREFIX.test(asciiPrefix(value))) {
          bytesRead = 0;
          break;
        }
      }
      bytesRead += Math.min(value.byteLength, probeBytes - bytesRead);
      if (bytesRead >= probeBytes) break;
    }

    const elapsedMs = Math.max(1, nowFn() - transferStartedAt);
    attempt.abort();
    await reader.cancel().catch(() => {});
    throwIfAborted(signal);

    if (bytesRead <= 0) {
      return {
        source,
        url,
        ok: false,
        bytesRead: 0,
        elapsedMs,
        bytesPerSecond: 0,
      };
    }
    return {
      source,
      url,
      ok: true,
      bytesRead,
      elapsedMs,
      bytesPerSecond: (bytesRead * 1000) / elapsedMs,
    };
  } catch {
    const elapsedMs =
      transferStartedAt !== undefined
        ? Math.max(1, nowFn() - transferStartedAt)
        : 0;
    await reader?.cancel?.().catch(() => {});
    if (signal.aborted) {
      throw signal.reason instanceof Error
        ? signal.reason
        : new DOMException("Aborted", "AbortError");
    }
    if (transferStartedAt !== undefined && timedOut && bytesRead > 0) {
      return {
        source,
        url,
        ok: true,
        bytesRead,
        elapsedMs,
        bytesPerSecond: (bytesRead * 1000) / elapsedMs,
      };
    }
    return {
      source,
      url,
      ok: false,
      bytesRead: 0,
      elapsedMs,
      bytesPerSecond: 0,
    };
  } finally {
    clearTimeout(timer);
    signal.removeEventListener("abort", onParentAbort);
  }
}

/**
 * Probes up to the first N (<= 3) CORS-readable (`browser_fetch === "allowed"`)
 * direct sources of a file variant using bounded Range requests (512 KiB for
 * <= 50 MiB or unknown size, 2 MiB for > 50 MiB) and ranks candidates by
 * post-header body transfer throughput. Sources whose probe traffic cannot be
 * read/counted are skipped without failing the download; remaining sources stay
 * ordered as failover backups.
 */
export async function probeAndOrderSources(
  sources: Source[],
  context: TargetContext = {},
  signal: AbortSignal,
  options: RouteProbeOptions = {},
): Promise<RouteProbeResult> {
  throwIfAborted(signal);
  const maxSources = Math.min(
    ROUTE_PROBE_MAX_SOURCES,
    Math.max(0, options.maxSources ?? ROUTE_PROBE_MAX_SOURCES),
  );
  const timeoutMs = Math.max(1, options.timeoutMs ?? ROUTE_PROBE_TIMEOUT_MS);
  const fetchImpl = options.fetchImpl ?? globalThis.fetch;
  const nowFn =
    options.nowFn ??
    (() =>
      typeof performance !== "undefined" && typeof performance.now === "function"
        ? performance.now()
        : Date.now());

  const available = sources.filter((source) =>
    sourceAvailable(source, context.now),
  );
  if (available.length === 0) {
    return {
      didProbe: false,
      primary: undefined,
      url: "",
      routes: [],
      orderedSources: [],
      observations: [],
    };
  }

  if (
    typeof fetchImpl !== "function" ||
    !shouldProbeRoutes(available, context, maxSources)
  ) {
    const primary = available[0];
    return {
      didProbe: false,
      primary,
      url: sourceUrl(primary, context),
      routes: buildRoutesForOrder(available, context),
      orderedSources: available,
      observations: [],
    };
  }

  const probeBytes = resolveRouteProbeBytes(options, sources, available);
  const eligible = available.filter(isDirectCorsAllowed).slice(0, maxSources);
  const observations = await Promise.all(
    eligible.map((source) =>
      probeSingleSource(
        source,
        context,
        signal,
        probeBytes,
        timeoutMs,
        fetchImpl,
        nowFn,
      ),
    ),
  );
  throwIfAborted(signal);

  const indexedWinners = observations
    .map((obs, index) => ({ obs, index }))
    .filter(({ obs }) => obs.ok && obs.bytesRead > 0)
    .sort((a, b) => {
      if (b.obs.bytesPerSecond !== a.obs.bytesPerSecond) {
        return b.obs.bytesPerSecond - a.obs.bytesPerSecond;
      }
      if (b.obs.bytesRead !== a.obs.bytesRead) {
        return b.obs.bytesRead - a.obs.bytesRead;
      }
      return a.index - b.index;
    });

  if (indexedWinners.length === 0) {
    const primary = available[0];
    return {
      didProbe: true,
      primary,
      url: sourceUrl(primary, context),
      routes: buildRoutesForOrder(available, context),
      orderedSources: available,
      observations,
    };
  }

  const winnerSet = new Set(indexedWinners.map(({ obs }) => obs.source));
  const orderedSources = [
    ...indexedWinners.map(({ obs }) => obs.source),
    ...available.filter((source) => !winnerSet.has(source)),
  ];
  const primary = orderedSources[0];
  return {
    didProbe: true,
    primary,
    url: sourceUrl(primary, context),
    routes: buildRoutesForOrder(orderedSources, context),
    orderedSources,
    observations,
  };
}

export async function probeFileTarget<
  T extends {
    source: Source;
    url: string;
    routes: string[];
    variant: {
      metadata?: unknown;
      delivery: { kind: string; sources?: Source[] };
    };
  },
>(
  target: T,
  item: { type?: string; tbvideo?: { platform?: string } },
  platform: string | undefined,
  context: TargetContext,
  signal: AbortSignal,
  onStatus?: (statusText: string) => unknown,
  options: RouteProbeOptions = {},
): Promise<T> {
  throwIfAborted(signal);
  if (
    item.type === "video" &&
    (platform === "pipigx" || platform === "zuiyou") &&
    item.tbvideo?.platform === platform
  ) {
    return target;
  }
  const sources =
    target.variant.delivery.kind === "file"
      ? (target.variant.delivery.sources ?? [])
      : [];
  const maxSources = Math.min(
    ROUTE_PROBE_MAX_SOURCES,
    Math.max(0, options.maxSources ?? ROUTE_PROBE_MAX_SOURCES),
  );
  if (!shouldProbeRoutes(sources, context, maxSources)) return target;
  const metadataSize =
    target.variant.metadata && typeof target.variant.metadata === "object"
      ? positiveSizeBytes(
          (target.variant.metadata as { size_bytes?: unknown }).size_bytes,
        )
      : undefined;
  const sizeBytes =
    positiveSizeBytes(options.sizeBytes) ??
    positiveSizeBytes(target.source?.size_bytes) ??
    metadataSize;
  onStatus?.("正在优选下载线路...");
  const probed = await probeAndOrderSources(sources, context, signal, {
    ...options,
    ...(sizeBytes !== undefined ? { sizeBytes } : {}),
  });
  return probed.primary && probed.url
    ? {
        ...target,
        source: probed.primary,
        url: probed.url,
        routes: probed.routes,
      }
    : target;
}

export default probeFileTarget;
