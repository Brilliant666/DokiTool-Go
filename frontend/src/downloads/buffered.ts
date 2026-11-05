import { mediaCredentials, networkWait } from "./file";
import { decodedLength } from "../../public/downloads/decoded-length.js";

/** Equivalent cancellation without requiring AbortSignal.any; detach on completion. */
export function linkAbortSignals(signals: (AbortSignal | undefined)[]) {
  const controller = new AbortController();
  const listeners: (() => void)[] = [];
  const dispose = () => { for (const remove of listeners.splice(0)) remove(); };
  for (const signal of signals) {
    if (!signal) continue;
    const abort = () => { controller.abort(signal.reason); dispose(); };
    if (signal.aborted) { abort(); break; }
    signal.addEventListener("abort", abort, { once: true });
    listeners.push(() => signal.removeEventListener("abort", abort));
  }
  return { controller, signal: controller.signal, dispose };
}

function requestScope(parent: AbortSignal | undefined, timeoutMs: number) {
  const linked = linkAbortSignals([parent]);
  return {
    signal: linked.signal,
    wait<T>(operation: () => Promise<T>): Promise<T> {
      if (linked.signal.aborted) return Promise.reject(linked.signal.reason);
      return networkWait(operation(), linked.controller, timeoutMs);
    },
    close() { linked.dispose(); linked.controller.abort(); },
  };
}

export interface BufferedReadOptions {
  signal?: AbortSignal;
  expectedSize?: number;
  range?: { start: number; length: number };
  onChunk?: (loaded: number, total: number) => void;
  timeoutMs?: number;
  maxBytes?: number;
}

/** Page-side ZIP/segment reads. Each header/read wait is bounded, not total download time. */
export async function fetchMediaBlob(url: string, options: BufferedReadOptions = {}): Promise<Blob> {
  const { signal, range, expectedSize = 0, onChunk, maxBytes = Number.MAX_SAFE_INTEGER } = options;
  if (range && (!Number.isSafeInteger(range.start) || range.start < 0 || !Number.isSafeInteger(range.length) ||
    range.length <= 0 || !Number.isSafeInteger(range.start + range.length))) throw new Error("无效的分片字节范围");
  const scope = requestScope(signal, options.timeoutMs ?? 30_000);
  let response: Response | undefined, reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  let finished = false;
  try {
    const headers: Record<string, string> = {};
    if (range) headers.Range = `bytes=${range.start}-${range.start + range.length - 1}`;
    response = await scope.wait(() => fetch(url, {
      method: "GET", mode: "cors", headers, signal: scope.signal,
      credentials: mediaCredentials(url), referrerPolicy: "no-referrer",
    }));
    if (range) {
      const declared = /^bytes (\d+)-(\d+)\/(?:\d+|\*)$/.exec(response.headers.get("Content-Range") || "");
      if (response.status !== 206 || !declared || Number(declared[1]) !== range.start ||
        Number(declared[2]) !== range.start + range.length - 1) throw new Error("分片服务器没有返回请求的字节范围");
    } else if (response.status !== 200 || response.headers.has("Content-Range")) {
      throw new Error(`资源请求未返回完整文件（HTTP ${response.status}）`);
    }
    const type = response.headers.get("Content-Type") || "";
    if (/text\/html|application\/json/i.test(type)) throw new Error("资源地址返回了错误页面");
    if (!response.body) throw new Error("该资源不支持读取");
    const declared = decodedLength(response) || 0;
    const total = range?.length || expectedSize || declared;
    if (total > maxBytes) throw new Error("资源超过允许的读取范围");
    reader = response.body.getReader();
    const parts: BlobPart[] = [];
    let loaded = 0;
    for (;;) {
      const chunk = await scope.wait(() => reader!.read());
      if (chunk.done) break;
      loaded += chunk.value.byteLength;
      if (loaded > maxBytes || (declared && loaded > declared) || (expectedSize && loaded > expectedSize) ||
        (range && loaded > range.length)) throw new Error("资源下载长度不符");
      parts.push(chunk.value as Uint8Array<ArrayBuffer>);
      onChunk?.(loaded, total);
    }
    if (!loaded || (declared && loaded !== declared) || (expectedSize && loaded !== expectedSize) ||
      (range && loaded !== range.length)) throw new Error("资源下载不完整");
    if (scope.signal.aborted) throw scope.signal.reason;
    finished = true;
    return new Blob(parts, { type });
  } finally {
    if (reader) {
      if (!finished) void reader.cancel().catch(() => {});
      reader.releaseLock();
    } else if (response?.body) void response.body.cancel().catch(() => {});
    scope.close();
  }
}

/** Optional metadata only. Missing or encoded lengths stay unknown. */
export async function probeMediaLength(url: string, signal?: AbortSignal, timeoutMs = 5000): Promise<number> {
  const scope = requestScope(signal, timeoutMs);
  let response: Response | undefined;
  try {
    response = await scope.wait(() => fetch(url, {
      method: "HEAD", mode: "cors", signal: scope.signal,
      credentials: mediaCredentials(url), referrerPolicy: "no-referrer",
    }));
    return response.status === 200 ? decodedLength(response) || 0 : 0;
  } catch (error) {
    if (signal?.aborted) throw signal.reason;
    return 0;
  } finally {
    if (response?.body) void response.body.cancel().catch(() => {});
    scope.close();
  }
}

/** One buffered job per page; independent from the ordinary streaming queue. */
export function createBufferedQueue() {
  let active = false;
  const waiting: (() => void)[] = [];
  return (signal: AbortSignal): Promise<() => void> => new Promise((resolve, reject) => {
    if (signal.aborted) { reject(signal.reason); return; }
    const abort = () => {
      const index = waiting.indexOf(start);
      if (index !== -1) waiting.splice(index, 1);
      reject(signal.reason);
    };
    const start = () => {
      signal.removeEventListener("abort", abort);
      active = true;
      let released = false;
      resolve(() => {
        if (released) return;
        released = true;
        active = false;
        waiting.shift()?.();
      });
    };
    if (!active) start();
    else if (waiting.length >= 100) reject(new Error("等待打包或分片下载的任务过多"));
    else { waiting.push(start); signal.addEventListener("abort", abort, { once: true }); }
  });
}
export const acquireBufferedSlot = createBufferedQueue();
