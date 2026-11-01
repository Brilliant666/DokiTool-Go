import type { DownloadObserver, DownloadRequest, DownloadSnapshot } from "./types";
import { mediaCredentials, mediaUrl, networkWait, throwIfAborted } from "./file";
import { attachmentFormat } from "../../public/downloads/media-format.js";
import { openSource } from "./native";
import { decodedLength } from "../../public/downloads/decoded-length.js";

const IDLE_TIMEOUT_MS = 30000;
let queue: Promise<void> = Promise.resolve();

/** One page-side Blob at a time; a cancelled waiter must never retain the queue. */
async function acquire(signal: AbortSignal): Promise<() => void> {
  throwIfAborted(signal);
  const previous = queue;
  let release!: () => void;
  const current = new Promise<void>(resolve => { release = resolve; });
  queue = previous.then(() => current);
  let abort!: () => void;
  const cancelled = new Promise<never>((_, reject) => {
    abort = () => reject(new DOMException("Aborted", "AbortError"));
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) abort();
  });
  try { await Promise.race([previous, cancelled]); throwIfAborted(signal); return release; }
  catch (error) { release(); throw error; }
  finally { signal.removeEventListener("abort", abort); }
}

/** Serial compatibility transport. The coordinator alone decides whether fallback is safe. */
export async function downloadBlob(request: DownloadRequest, signal: AbortSignal, onUpdate: DownloadObserver): Promise<DownloadSnapshot> {
  const startedAt = request.startedAt ?? Date.now();
  let transferStartedAt: number | undefined;
  let snapshot: DownloadSnapshot = { id: request.id, transport: "browser_blob", status: "queued", filename: request.filename, url: request.url, loaded: 0, total: 0, speed: 0, elapsed: 0, averageSpeed: 0 };
  const publish = (patch: Partial<DownloadSnapshot>) => {
    const now = Date.now();
    const transferElapsed = transferStartedAt === undefined ? 0 : Math.max((now - transferStartedAt) / 1000, 0.001);
    snapshot = { ...snapshot, ...patch, elapsed: Math.max(0, (now - startedAt) / 1000), transferElapsed };
    snapshot.averageSpeed = transferElapsed ? snapshot.loaded / transferElapsed : 0;
    onUpdate({ ...snapshot });
  };
  publish({});
  let release: (() => void) | undefined;
  try {
    release = await acquire(signal);
    const routes = [...new Set([request.url, ...request.routes])].slice(0, 3);
    let failure: unknown = new Error("没有可用的下载线路");
    for (const route of routes) {
      throwIfAborted(signal);
      const controller = new AbortController();
      const abort = () => controller.abort();
      signal.addEventListener("abort", abort, { once: true });
      let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
      let response: Response | undefined;
      let progressTimer: ReturnType<typeof setInterval> | undefined;
      const chunks: BlobPart[] = [];
      let materializing = false;
      try {
        const url = mediaUrl(route);
        transferStartedAt = undefined;
        publish({ status: "preparing", url, loaded: 0, total: 0, speed: 0, averageSpeed: 0, error: undefined });
        response = await networkWait(fetch(url, { method: "GET", mode: "cors", credentials: mediaCredentials(url), referrerPolicy: "no-referrer", signal: controller.signal }), controller, IDLE_TIMEOUT_MS);
        throwIfAborted(signal);
        if (response.status !== 200 || response.type === "opaque" || response.headers.has("content-range") || !response.body) throw new Error(`资源不是完整文件响应（HTTP ${response.status}）`);
        const total = decodedLength(response, request.sourceSizes?.[url]);
        const { filename, mime: type } = attachmentFormat(request.filename, response.headers.get("content-type"), request.formatMime);
        reader = response.body.getReader();
        let loaded = 0, sampleBytes = 0, sampleTime = Date.now();
        let lastReceivedAt = 0;
        publish({ status: "downloading", filename, total });
        progressTimer = setInterval(() => {
          const now = Date.now();
          const currentSpeed = (loaded - sampleBytes) / Math.max((now - sampleTime) / 1000, 0.001);
          const speed = now - lastReceivedAt > 3000 ? 0 : snapshot.speed ? snapshot.speed * 0.6 + currentSpeed * 0.4 : currentSpeed;
          publish({ loaded, speed });
          sampleBytes = loaded; sampleTime = now;
        }, 500);
        while (true) {
          const { done, value } = await networkWait(reader.read(), controller, IDLE_TIMEOUT_MS);
          throwIfAborted(signal);
          if (done) break;
          if (!value.byteLength) continue;
          if (!loaded) {
            const prefix = new TextDecoder().decode(value.subarray(0, 100)).trimStart();
            if (/^(?:<!doctype|<html|#EXTM3U|\{"|\[\{)/i.test(prefix)) throw new Error("资源返回了页面或播放清单，不能作为完整视频保存");
            transferStartedAt = Date.now();
          }
          loaded += value.byteLength;
          lastReceivedAt = Date.now();
          if (total && loaded > total) throw new Error("下载内容超过声明大小，文件不完整或响应异常");
          // Fetch owns a distinct byte chunk; avoid an extra whole-file copy before Blob construction.
          chunks.push(value as BlobPart);
        }
        if (!loaded || (total && loaded !== total)) throw new Error(`下载不完整：期望 ${total || "未知"} 字节，实际 ${loaded} 字节`);
        throwIfAborted(signal);
        // Allocation or handoff failure must not retry every source and allocate the file again.
        materializing = true;
        const objectUrl = URL.createObjectURL(new Blob(chunks, { type: type || "application/octet-stream" }));
        chunks.length = 0;
        try { openSource(objectUrl, filename, false); }
        finally { setTimeout(() => URL.revokeObjectURL(objectUrl), 4000); }
        publish({ status: "completed", loaded, total: total || loaded, speed: 0 });
        return snapshot;
      } catch (error) {
        throwIfAborted(signal);
        if (materializing || error instanceof RangeError) throw error;
        failure = error;
      } finally {
        clearInterval(progressTimer);
        chunks.length = 0;
        controller.abort();
        void (reader ? reader.cancel() : response?.body?.cancel())?.catch(() => {});
        signal.removeEventListener("abort", abort);
      }
    }
    throw failure;
  } catch (error) {
    publish({ status: signal.aborted ? "cancelled" : "failed", speed: 0, error: signal.aborted ? undefined : error instanceof Error ? error.message : "下载失败" });
    throw error;
  } finally { release?.(); }
}
