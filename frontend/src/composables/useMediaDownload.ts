import type { DownloadSnapshot, SourceProbe } from "../downloads/types";
import { confirmedFormatMime, extensionsFor } from "../downloads/file";
import type {
  DownloadTask,
  MediaItem,
  MediaSelection,
  Resource,
  Attachment,
  Source,
  ToastType,
} from "../types/media";
import { buildMediaFilename } from "../utils/videoFilename";
import { decodedLength } from "../../public/downloads/decoded-length.js";
import {
  getDownloadTarget,
  readyItem,
  readyVariant,
  resourceSource,
  sourceUrl,
  type DownloadTarget,
} from "../utils/mediaContract";
import {
  declaredSegments,
  fileResource,
  mediaExtension,
} from "../utils/mediaDelivery";
import {
  mergeAndDownload,
  mergeSegments,
  type HlsSegment,
} from "../utils/segmentDownload";
import { apiConfig } from "../services/apiConfig";
import type { useVideoStore } from "../stores/video";
import type { VideoSelection } from "./useVideoSelection";
interface UseMediaDownloadOptions {
  videoStore: ReturnType<typeof useVideoStore>;
  showToast: (message: string, type?: ToastType, duration?: number) => unknown;
  selection: VideoSelection;
  resolveItem: (item: MediaItem, signal?: AbortSignal) => Promise<MediaItem>;
}
function triggerBrowserDownload(
  url: string,
  filename: string,
  newTab = false,
): void {
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  // 无 Referer 直连，降低 CDN 防盗链拒绝概率
  anchor.referrerPolicy = "no-referrer";
  if (newTab) {
    anchor.target = "_blank";
    anchor.rel = "noopener noreferrer";
  }
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
}

export function useMediaDownload({
  videoStore,
  showToast,
  selection,
  resolveItem,
}: UseMediaDownloadOptions) {
  const apiContext = { apiOrigin: apiConfig.baseUrl };
  const freeze = <T>(value: T): T => JSON.parse(JSON.stringify(value));
  const effectiveSelectedUrl = () => selection.currentVideoUrl.value;
  const archiveRetries = new Map<number, () => Promise<void>>();
  const videoRetries = new Map<number, () => Promise<boolean>>();
  const pendingTaskActions = new Set<number>();
  const sourceContexts = new Map<
    number,
    {
      routes: string[];
      resultVersion: number;
      selectedUrl: string;
      itemId?: string;
      variantId?: string;
      sources?: Source[];
      formatMime?: string;
      selectionKey?: string;
    }
  >();
  const taskFor = (id: number) =>
    videoStore.downloads.find((task) => task.id === id);
  const ownsTask = (id: number, controller: AbortController) =>
    taskFor(id)?.abortController === controller;
  const clearableUnknown = (task: DownloadTask) =>
    task.transport === "browser_streaming" &&
    task.observation === "unknown" &&
    task.ownerGone === true && task.recoveryExpired === true;
  const clearable = (task: DownloadTask) => clearableUnknown(task) ||
    (!task.observation && ["completed", "cancelled", "failed", "handed-off", "interrupted"].includes(task.status));
  const matchesSettledAttempt = (snapshot: DownloadTask) => {
    const current = taskFor(snapshot.id);
    return (
      current &&
      clearable(current) &&
      current.deliveryId === snapshot.deliveryId &&
      current.abortController === snapshot.abortController &&
      current.status === snapshot.status &&
      current.transport === snapshot.transport
    );
  };
  const checkAbort = (signal: AbortSignal) => {
    if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  };
  /** Settle immediately on cancellation even if a resolver ignores its signal. */
  function abortable<T>(request: Promise<T>, signal?: AbortSignal): Promise<T> {
    if (!signal) return request;
    return new Promise<T>((resolve, reject) => {
      const onAbort = () => {
        signal.removeEventListener("abort", onAbort);
        reject(new DOMException("Aborted", "AbortError"));
      };
      if (signal.aborted) {
        void request.catch(() => {});
        onAbort();
        return;
      }
      signal.addEventListener("abort", onAbort, { once: true });
      request.then(
        (value) => {
          signal.removeEventListener("abort", onAbort);
          if (signal.aborted) onAbort();
          else resolve(value);
        },
        (error) => {
          signal.removeEventListener("abort", onAbort);
          reject(error);
        },
      );
    });
  }

  /** Timed updates continue during waits, so a stalled request never shows its old speed. */
  function monitorTransfer(
    id: number,
    controller: AbortController,
    startedAt = Date.now(),
    bytes = true,
  ) {
    let received = 0;
    let sampleBytes = 0;
    let sampleTime = startedAt;
    let lastReceivedAt = startedAt;
    let stopped = false;
    const publish = (finished = false) => {
      if (!ownsTask(id, controller)) return;
      const now = Date.now();
      const elapsed = Math.max(0, (now - startedAt) / 1000);
      const sampleElapsed = Math.max(0.001, (now - sampleTime) / 1000);
      const task = taskFor(id);
      videoStore.updateDownload(id, {
        elapsed,
        speed:
          bytes && !finished ? (received - sampleBytes) / sampleElapsed : 0,
        ...(bytes
          ? {
              transferred: received,
              averageSpeed: received / Math.max(elapsed, 0.001),
            }
          : {}),
        ...(!finished &&
        bytes &&
        task?.status === "downloading" &&
        now - lastReceivedAt >= 3000
          ? { statusText: received ? "等待数据..." : "等待服务器响应..." }
          : {}),
      });
      sampleBytes = received;
      sampleTime = now;
    };
    const timer = setInterval(publish, 500);
    return {
      receive(count: number) {
        received += count;
        lastReceivedAt = Date.now();
      },
      finish() {
        if (!stopped) {
          stopped = true;
          clearInterval(timer);
          publish(true);
        }
      },
    };
  }

  /** 单文件抓取（用于 ZIP 打包） */
  async function fetchBlob(
    url: string,
    signal?: AbortSignal,
    expectedSize?: number,
  ): Promise<Blob> {
    const response = await fetch(url, { method: "GET", mode: "cors", signal });
    if (!response.ok)
      throw new Error(`资源请求失败（HTTP ${response.status}）`);
    const blob = await response.blob();
    if (signal) checkAbort(signal);
    if (!blob.size) throw new Error("资源为空");
    const declared = decodedLength(response);
    if ((declared && blob.size !== declared) || (expectedSize && blob.size !== expectedSize))
      throw new Error("资源下载不完整");
    if (/text\/html|application\/json/i.test(blob.type))
      throw new Error("资源地址返回了错误页面");
    return blob;
  }

  let pageTaskSequence = 0;
  const deliveryTasks = new Map<string, number>();
  const comparableUrl = (url: string) => {
    try {
      return new URL(
        url,
        typeof location === "undefined"
          ? "https://dokitool.invalid/"
          : location.href,
      ).href;
    } catch {
      return url;
    }
  };
  const deliveryStatusText: Record<DownloadSnapshot["status"], string> = {
    queued: "排队中...",
    preparing: "准备下载...",
    downloading: "下载中...",
    completed: "传输完成，请在浏览器确认保存",
    failed: "下载失败",
    cancelled: "已取消",
    "handed-off": "已交给浏览器",
    interrupted: "下载连接已中断",
  };

  function applyDelivery(snapshot: DownloadSnapshot, restore = false): void {
    let id = deliveryTasks.get(snapshot.id);
    if (id === undefined && restore) {
      id = videoStore.addDownload({
        filename: snapshot.filename,
        url: snapshot.url,
      });
      deliveryTasks.set(snapshot.id, id);
    }
    if (id === undefined) return;
    const task = taskFor(id);
    if (
      !task ||
      (task.deliveryId !== undefined && task.deliveryId !== snapshot.id)
    )
      return;
    // A status-only/uncertain Worker reply has no URL. Keep the frozen source
    // in this page so a second click cannot silently create a duplicate task.
    const url = snapshot.url || task.url || "";
    videoStore.updateDownload(id, {
      deliveryId: snapshot.id,
      transport: snapshot.transport,
      filename: snapshot.filename,
      url,
      status: snapshot.status,
      statusText: deliveryStatusText[snapshot.status],
      loaded: snapshot.loaded,
      total: snapshot.total,
      speed: snapshot.speed,
      elapsed: snapshot.elapsed,
      averageSpeed: snapshot.averageSpeed,
      transferElapsed: snapshot.transferElapsed,
      observation: snapshot.observation,
      workerBuild: snapshot.workerBuild,
      workerInstance: snapshot.workerInstance,
      recoveryDeadline: snapshot.recoveryDeadline,
      recoveryExpired: snapshot.recoveryExpired,
      ownerGone: snapshot.ownerGone,
      transferred: snapshot.loaded,
      error: snapshot.error || null,
      progressKind: "bytes",
      percent:
        snapshot.status === "completed"
          ? 100
          : snapshot.total > 0
            ? Math.min(99, Math.round((snapshot.loaded / snapshot.total) * 100))
            : 0,
      nativeAvailable: Boolean(url) && true,
    });
  }

  /** Called on client mount only; the homepage and SSR never register a Worker. */
  async function restoreDownloads(): Promise<void> {
    try {
      // No download history means no extra chunk or registration on the first visit.
      if (!sessionStorage.getItem("dokitool-downloads-v1")) return;
      const { reconnectStreaming } = await import("../downloads/streaming");
      await reconnectStreaming((snapshot) => applyDelivery(snapshot, true));
    } catch {
      /* Restricted storage must not prevent the parser from starting. */
    }
  }

  async function downloadFile(
    url: string,
    filename: string,
    downloadId: number,
    controller = new AbortController(),
    startedAt = Date.now(),
    probe?: SourceProbe,
  ): Promise<void> {
    if (!url) return;
    const matchingTasks = () =>
      videoStore.downloads.filter(
        (task) =>
          task.id !== downloadId &&
          [task.url, sourceContexts.get(task.id)?.selectedUrl].some(
            (source) => source && comparableUrl(source) === comparableUrl(url),
          ),
      );
    const active = (status: string) =>
      ["queued", "preparing", "downloading"].includes(status);
    let duplicate = matchingTasks().some((task) => active(task.status) && !task.ownerGone);
    if (
      !duplicate &&
      matchingTasks().some(
        (task) => task.status === "interrupted" && task.deliveryId,
      )
    ) {
      try {
        const { hasStreamingTask } = await import("../downloads/streaming");
        duplicate = matchingTasks().some(
          (task) =>
            (active(task.status) && !task.ownerGone) ||
            (task.status === "interrupted" &&
              task.deliveryId &&
              hasStreamingTask(task.deliveryId)),
        );
      } catch {
        // A failed module load is not evidence that a previously started download stopped.
        duplicate = true;
      }
    }
    if (duplicate) {
      videoStore.removeDownload(downloadId);
      archiveRetries.delete(downloadId);
      videoRetries.delete(downloadId);
      sourceContexts.delete(downloadId);
      showToast("该资源已有下载任务", "info");
      return;
    }
    const deliveryId =
      globalThis.crypto?.randomUUID?.() ??
      `page-${Date.now()}-${++pageTaskSequence}`;
    deliveryTasks.set(deliveryId, downloadId);
    videoStore.updateDownload(downloadId, {
      deliveryId,
      abortController: controller,
      status: "queued",
      statusText: "排队中...",
      percent: 0,
      loaded: 0,
      total: 0,
      speed: 0,
      elapsed: 0,
      averageSpeed: 0,
      transferred: 0,
      error: null,
      progressKind: "bytes",
      nativeAvailable: true,
      observation: undefined,
      transferElapsed: undefined,
      workerBuild: undefined,
    });
    const context = sourceContexts.get(downloadId);
    const routes = Array.from(new Set([url, ...(context?.routes ?? [])])).slice(
      0,
      3,
    );
    const sourceSizes: Record<string, number> = {};
    for (const source of context?.sources ?? []) {
      if (!source.size_bytes) continue;
      const address = sourceUrl(source, apiContext);
      if (!routes.includes(address)) continue;
      const key = comparableUrl(address);
      // Two conflicting declarations for the same request give no usable bound.
      const previous = sourceSizes[key];
      sourceSizes[key] = previous === undefined || previous === source.size_bytes ? source.size_bytes : 0;
    }
    let cancellationReason: string | undefined;
    try {
      const { downloadManager } = await import("../downloads/manager");
      checkAbort(controller.signal);
      const result = await downloadManager.run(
        { id: deliveryId, url, filename, routes, sourceSizes, startedAt, probe, formatMime: context?.formatMime },
        controller.signal,
        (snapshot) => {
          if (
            taskFor(downloadId)?.deliveryId === deliveryId &&
            taskFor(downloadId)?.status !== "handed-off"
          ) {
            // Use this attempt's confirmed delivery, never a previous task error.
            cancellationReason = snapshot.status === "cancelled" && !snapshot.observation
              ? snapshot.error : undefined;
            applyDelivery(snapshot);
          }
        },
      );
      if (!ownsTask(downloadId, controller)) return;
      // Finishing an older work must never alter a newly parsed work's player.
      if (
        result.status === "completed" &&
        comparableUrl(result.url) !== comparableUrl(url) &&
        context &&
        videoStore.resultVersion === context.resultVersion &&
        effectiveSelectedUrl() === context.selectedUrl &&
        JSON.stringify(selection.currentSelection.value) ===
          context.selectionKey
      ) {
        const source = context.sources?.find(
          (source) =>
            comparableUrl(sourceUrl(source, apiContext)) ===
            comparableUrl(result.url),
        );
        if (
          source &&
          context.itemId &&
          context.variantId &&
          selection.currentVideo.value?.id === context.itemId
        )
          selection.choose(
            {
              variant_id: context.variantId,
              ...(source.id ? { source_id: source.id } : {}),
            },
            context.itemId,
          );
        showToast("下载已切换到同画质可用线路，播放器与复制链接已同步", "info");
      }
    } catch (error) {
      if (!ownsTask(downloadId, controller)) return;
      if (
        error instanceof Error &&
        error.name === "StreamingStateUnknownError"
      ) {
        videoStore.updateDownload(downloadId, {
          observation: "unknown",
          speed: 0,
          statusText: "下载状态待确认",
          error: error.message,
        });
        return;
      }
      const cancelled =
        controller.signal.aborted ||
        (error instanceof Error && error.name === "AbortError");
      videoStore.updateDownload(downloadId, {
        status: cancelled ? "cancelled" : "failed",
        statusText: cancelled ? "已取消" : "下载失败",
        speed: 0,
        error: cancelled
          ? cancellationReason ?? null
          : error instanceof Error
            ? error.message
            : "下载失败",
      });
    } finally {
      if (ownsTask(downloadId, controller))
        videoStore.updateDownload(downloadId, { abortController: null });
    }
  }

  /** 分片下载（写入 store 进度） */
  async function downloadSegments(
    segments: (HlsSegment | string)[],
    filename: string,
    downloadId: number,
    abortController = new AbortController(),
    startedAt = Date.now(),
  ): Promise<void> {
    videoStore.updateDownload(downloadId, {
      abortController,
      progressKind: "segments",
      nativeAvailable: false,
      error: null,
      speed: 0,
      averageSpeed: 0,
      elapsed: 0,
    });
    const monitor = monitorTransfer(
      downloadId,
      abortController,
      startedAt,
      false,
    );
    try {
      videoStore.updateDownload(downloadId, {
        status: "downloading",
        statusText: "下载分片中...",
        percent: 0,
        loaded: 0,
        total: 0,
      });
      const ok = await mergeAndDownload(segments, {
        signal: abortController.signal,
        filename,
        onProgress: (p) =>
          videoStore.updateDownload(downloadId, {
            loaded: p.loaded,
            total: p.total,
            percent: p.percent,
            statusText: `下载分片 ${p.segment}/${p.segmentCount}（${p.percent}%）`,
          }),
      });
      if (!ok) throw new Error("没有可用的分片");
      if (!ownsTask(downloadId, abortController)) return;
      monitor.finish();
      videoStore.updateDownload(downloadId, {
        status: "completed",
        statusText: "已完成",
        percent: 100,
        loaded: 0,
        total: 0,
        abortController: null,
      });
    } catch (error) {
      if (!ownsTask(downloadId, abortController)) return;
      monitor.finish();
      if (
        abortController.signal.aborted ||
        (error instanceof Error && error.name === "AbortError")
      ) {
        videoStore.updateDownload(downloadId, {
          status: "cancelled",
          statusText: "已取消",
          abortController: null,
        });
        return;
      }
      const message = error instanceof Error ? error.message : "下载失败";
      videoStore.updateDownload(downloadId, {
        status: "failed",
        statusText: "下载失败",
        error: message,
        abortController: null,
      });
      showToast("分片下载失败：" + message, "error");
    } finally {
      monitor.finish();
    }
  }

  type ArchiveEntry = {
    filename: string;
    data: Blob | Uint8Array<ArrayBuffer>;
  };
  type ArchiveBuilder = (
    signal: AbortSignal,
    progress: (done: number, total: number) => void,
  ) => Promise<ArchiveEntry[]>;

  async function runArchive(
    filename: string,
    build: ArchiveBuilder,
    retryId?: number,
  ): Promise<void> {
    const id =
      retryId ??
      videoStore.addDownload({
        filename,
        status: "preparing",
        statusText: "准备中...",
      });
    const controller = new AbortController();
    archiveRetries.set(id, () => runArchive(filename, build, id));
    videoStore.updateDownload(id, {
      abortController: controller,
      status: "preparing",
      statusText: "准备打包...",
      percent: 0,
      error: null,
      progressKind: "archive",
      nativeAvailable: false,
      elapsed: 0,
      speed: 0,
      averageSpeed: 0,
    });
    const monitor = monitorTransfer(id, controller, Date.now(), false);
    try {
      // Only ZIP actions need this library. The import itself cannot be aborted,
      // so cancellation must settle the task and guard all work after it resolves.
      const { default: JSZip } = await abortable(
        import("jszip"),
        controller.signal,
      );
      checkAbort(controller.signal);
      if (!ownsTask(id, controller)) return;
      videoStore.updateDownload(id, {
        status: "downloading",
        statusText: "正在打包...",
      });
      const entries = await build(controller.signal, (done, total) => {
        checkAbort(controller.signal);
        if (!ownsTask(id, controller)) return;
        videoStore.updateDownload(id, {
          percent: Math.round((done / total) * 85),
          statusText: `打包 ${done}/${total}...`,
        });
      });
      checkAbort(controller.signal);
      if (!ownsTask(id, controller)) return;
      if (!entries.length) throw new Error("没有可打包的资源");
      const zip = new JSZip();
      for (const entry of entries) {
        checkAbort(controller.signal);
        // ArrayBuffer works consistently in browsers and Node's offline regression harness.
        const data =
          entry.data instanceof Blob
            ? await entry.data.arrayBuffer()
            : entry.data;
        checkAbort(controller.signal);
        if (!ownsTask(id, controller)) return;
        zip.file(entry.filename, data);
      }
      const blob = await zip.generateAsync(
        { type: "blob", compression: "STORE" },
        (metadata) => {
          checkAbort(controller.signal);
          if (!ownsTask(id, controller)) return;
          videoStore.updateDownload(id, {
            percent: 85 + Math.round(metadata.percent * 0.14),
            statusText: "生成压缩包...",
          });
        },
      );
      checkAbort(controller.signal);
      if (!ownsTask(id, controller)) return;
      const blobUrl = URL.createObjectURL(blob);
      triggerBrowserDownload(blobUrl, filename);
      setTimeout(() => URL.revokeObjectURL(blobUrl), 4000);
      monitor.finish();
      videoStore.updateDownload(id, {
        status: "completed",
        statusText: "已完成",
        percent: 100,
        abortController: null,
      });
      archiveRetries.delete(id);
      showToast(`已打包 ${entries.length} 个文件`);
    } catch (error) {
      if (
        videoStore.downloads.find((task) => task.id === id)?.abortController !==
        controller
      )
        return;
      monitor.finish();
      if (
        controller.signal.aborted ||
        (error instanceof Error && error.name === "AbortError")
      ) {
        videoStore.updateDownload(id, {
          status: "cancelled",
          statusText: "已取消",
          abortController: null,
        });
        return;
      }
      const message = error instanceof Error ? error.message : "打包失败";
      videoStore.updateDownload(id, {
        status: "failed",
        statusText: "打包失败",
        error: message,
        abortController: null,
      });
      showToast("打包失败：" + message, "error");
    } finally {
      monitor.finish();
    }
  }

  type ItemSnapshot = {
    item: MediaItem;
    choice: MediaSelection | null;
    title?: string;
    version: number;
    selectionKey: string;
    resource?: {
      id: string;
      role: Resource["role"];
      source_id?: string;
    };
  };
  const snapshotItem = (
    item: MediaItem,
    choice = selection.selectionFor(item.id),
  ): ItemSnapshot => {
    let pinnedChoice = choice || (item.default
      ? { variant_id: item.default.variant_id, source_id: item.default.source_id }
      : null);
    if (pinnedChoice && !pinnedChoice.source_id && readyItem(item)) {
      const variant = item.variants.find(candidate => candidate.id === pinnedChoice!.variant_id);
      if (readyVariant(variant) && variant.delivery.kind === "file") {
        const sources = variant.delivery.sources;
        // A quality-only choice uses the first current source, even if expired.
        // Freeze its identity before resolve can reorder or replace the routes.
        // A default without source_id is only unambiguous for one source.
        if ((choice || sources.length === 1) && sources[0]?.id) {
          pinnedChoice = { ...pinnedChoice, source_id: sources[0].id };
        }
      }
    }
    return freeze({
      item,
      choice: pinnedChoice,
      // Keep the actual UI choice for concurrent-selection checks; task pinning
      // must not change the user's selection or the displayed server default.
      selectionKey: JSON.stringify(choice),
      title: videoStore.resultData?.title,
      version: videoStore.resultVersion,
    });
  };
  const filenameFor = (snapshot: ItemSnapshot, extension: string) =>
    buildMediaFilename({
      title: snapshot.item.title || snapshot.title,
      index: snapshot.item.index,
      fallback: snapshot.item.id,
      extension,
    });
  function snapshotTarget(
    snapshot: ItemSnapshot,
    item: MediaItem,
  ): DownloadTarget {
    if (!snapshot.resource || !readyItem(item))
      return getDownloadTarget(item, snapshot.choice, apiContext);
    const variant = item.variants.find(
      (candidate) => candidate.id === snapshot.choice?.variant_id,
    );
    if (!readyVariant(variant))
      return getDownloadTarget(item, snapshot.choice, apiContext);
    if (variant.delivery.kind !== "pair")
      return {
        kind: "invalid",
        message: "所选实况版本不再包含配对资源，请重新选择",
      };
    const resource = variant.delivery.resources.find(
      (candidate) =>
        candidate.id === snapshot.resource!.id &&
        candidate.role === snapshot.resource!.role,
    );
    if (!resource)
      return { kind: "invalid", message: "所选实况资源不存在，请重新选择" };
    // The executor reads one declared leaf, while its task retains the parent
    // item/variant/resource identity needed to refresh this exact resource.
    return getDownloadTarget(
      {
        id: item.id,
        type: resource.role === "image" ? "image" : "video",
        variants: [
          {
            id: variant.id,
            metadata: resource.metadata,
            delivery: resource.delivery,
          },
        ],
        default: null,
        default_unavailable_reason: "processing_required",
      },
      { variant_id: variant.id, source_id: snapshot.resource.source_id },
      apiContext,
    );
  }
  function preparedSnapshot(
    snapshot: ItemSnapshot,
    item: MediaItem,
    target: Extract<DownloadTarget, { variant: unknown }>,
  ): ItemSnapshot {
    const source =
      target.kind === "file" && target.source.id
        ? { source_id: target.source.id }
        : {};
    return freeze({
      ...snapshot,
      item,
      choice: {
        variant_id: target.variant.id,
        ...(!snapshot.resource ? source : {}),
      },
      ...(snapshot.resource
        ? { resource: { ...snapshot.resource, ...source } }
        : {}),
    });
  }
  async function resolvedTarget(snapshot: ItemSnapshot, signal: AbortSignal) {
    checkAbort(signal);
    let item = snapshot.item;
    let target = snapshotTarget(snapshot, item);
    if (target.kind === "needs_resolve" || target.kind === "needs_refresh") {
      const deadline = new AbortController();
      const timeout = setTimeout(
        () => deadline.abort(new Error("解析准备超过 30 秒，请重试")),
        30000,
      );
      const combined = AbortSignal.any([signal, deadline.signal]);
      try {
        item = await abortable(resolveItem(item, combined), combined);
      } catch (error) {
        if (deadline.signal.aborted && !signal.aborted)
          throw deadline.signal.reason;
        throw error;
      } finally {
        clearTimeout(timeout);
      }
      checkAbort(signal);
      target = snapshotTarget(snapshot, item);
    }
    // Never substitute a different variant when an explicit choice disappeared.
    if (!("variant" in target)) throw new Error(target.message);
    return { item, target };
  }
  async function enqueueItem(
    snapshot: ItemSnapshot,
    retryId?: number,
  ): Promise<boolean> {
    const id =
      retryId ??
      videoStore.addDownload({
        filename: filenameFor(snapshot, "bin"),
        statusText: "准备下载...",
      });
    const controller = new AbortController(),
      startedAt = Date.now();
    videoStore.updateDownload(id, {
      abortController: controller,
      status: "preparing",
      statusText: "准备下载...",
      error: null,
      nativeAvailable: false,
      loaded: 0,
      total: 0,
      transferred: 0,
      progressKind: undefined,
      elapsed: 0,
      speed: 0,
      averageSpeed: 0,
      percent: 0,
    });
    videoRetries.set(id, () => enqueueItem(snapshot, id));
    try {
      const { item, target } = await resolvedTarget(
        snapshot,
        controller.signal,
      );
      if (!ownsTask(id, controller)) return false;
      const prepared = preparedSnapshot(snapshot, item, target);
      videoRetries.set(id, () => enqueueItem(prepared, id));
      const delivery = target.variant.delivery;
      if (target.kind === "pair") {
        if (delivery.kind !== "pair") throw new Error("实况资源不完整");
        videoStore.updateDownload(id, { abortController: null });
        await archiveItems([prepared], filenameFor(snapshot, "zip"), id);
        return true;
      }
      const extension = mediaExtension(
        target.variant.metadata,
        delivery.kind === "mux" || delivery.kind === "pair"
          ? undefined
          : delivery,
      );
      const formatMime = target.kind === "file" ? confirmedFormatMime(target.variant.metadata) : undefined;
      const suffixes = extensionsFor(formatMime || "");
      const filename = filenameFor(snapshot, suffixes.length && !suffixes.includes(extension) ? suffixes[0] : extension);
      videoStore.updateDownload(id, { filename });
      if (target.kind === "file") {
        sourceContexts.set(id, {
          routes: target.routes,
          selectedUrl: target.url,
          resultVersion: snapshot.version,
          itemId: item.id,
          variantId: target.variant.id,
          selectionKey: snapshot.selectionKey,
          sources: delivery.kind === "file" ? delivery.sources : [],
          formatMime,
        });
        videoStore.updateDownload(id, { url: target.url });
        void downloadFile(target.url, filename, id, controller, startedAt);
      } else {
        if (delivery.kind !== "hls" && delivery.kind !== "segments")
          throw new Error("该版本暂不支持");
        const segments = await declaredSegments(
          delivery,
          apiContext,
          controller.signal,
        );
        checkAbort(controller.signal);
        videoStore.updateDownload(id, { segments });
        void downloadSegments(segments, filename, id, controller, startedAt);
      }
      return true;
    } catch (error) {
      if (!ownsTask(id, controller)) return false;
      const cancelled =
        controller.signal.aborted ||
        (error instanceof Error && error.name === "AbortError");
      videoStore.updateDownload(id, {
        status: cancelled ? "cancelled" : "failed",
        statusText: cancelled ? "已取消" : "下载失败",
        error: cancelled
          ? null
          : error instanceof Error
            ? error.message
            : "下载失败",
        abortController: null,
        speed: 0,
      });
      return false;
    }
  }
  async function resourceEntry(
    resource: Resource,
    name: string,
    signal: AbortSignal,
  ): Promise<ArchiveEntry> {
    const delivery = resource.delivery;
    if (delivery.kind === "file") {
      const target = fileResource(resource, apiContext);
      const data = await fetchBlob(
        target.url,
        signal,
        target.source.size_bytes,
      );
      return {
        filename: `${name}.${mediaExtension(resource.metadata, delivery, data.type)}`,
        data,
      };
    }
    if (delivery.kind === "hls" || delivery.kind === "segments")
      return {
        filename: `${name}.${mediaExtension(resource.metadata, delivery)}`,
        data: await mergeSegments(
          await declaredSegments(delivery, apiContext, signal),
          { signal },
        ),
      };
    throw new Error("该资源需要 DASH 处理，当前暂不支持");
  }
  async function archiveItems(
    snapshots: ItemSnapshot[],
    filename: string,
    retryId?: number,
  ) {
    const taskSnapshots = [...snapshots];
    await runArchive(
      filename,
      async (signal, progress) => {
        const entries: ArchiveEntry[] = [];
        for (let index = 0; index < taskSnapshots.length; index++) {
          const snapshot = taskSnapshots[index];
          const { item, target } = await resolvedTarget(snapshot, signal);
          // Resolve once into this archive's private state. Retries retain the
          // exact variant/source even if a later server default would differ.
          taskSnapshots[index] = preparedSnapshot(snapshot, item, target);
          const delivery = target.variant.delivery;
          const base = `${String(index + 1).padStart(3, "0")}_${filenameFor(snapshot, "bin").replace(/\.bin$/, "")}`;
          if (target.kind === "file") {
            const data = await fetchBlob(
              target.url,
              signal,
              target.source.size_bytes,
            );
            entries.push({
              filename: `${base}.${mediaExtension(target.variant.metadata, delivery.kind === "file" ? delivery : undefined, data.type)}`,
              data,
            });
          } else if (delivery.kind === "pair") {
            // Both files share a stable base; incomplete pairs fail the whole archive.
            for (const resource of delivery.resources)
              entries.push(
                await resourceEntry(
                  resource,
                  `${base}_${resource.role}`,
                  signal,
                ),
              );
          } else if (delivery.kind !== "mux") {
            entries.push(
              await resourceEntry(
                {
                  id: snapshot.item.id,
                  role: "media",
                  metadata: target.variant.metadata,
                  delivery,
                },
                base,
                signal,
              ),
            );
          } else throw new Error("音视频合并尚未支持");
          checkAbort(signal);
          progress(index + 1, taskSnapshots.length);
        }
        return entries;
      },
      retryId,
    );
  }
  async function downloadVideosAsZip(
    items: MediaItem[],
    zipName = `media_${Date.now()}.zip`,
  ) {
    if (!items.length) {
      showToast("没有可下载的条目", "warning");
      return;
    }
    await archiveItems(
      items.map((item) => snapshotItem(item)),
      zipName,
    );
  }
  async function downloadSelected() {
    const snapshots = selection
      .getBatchVideos()
      .map((item) => snapshotItem(item));
    for (const snapshot of snapshots) await enqueueItem(snapshot);
  }
  async function downloadAllVideos() {
    const snapshots = selection.list.value.map((item) => snapshotItem(item));
    for (const snapshot of snapshots) await enqueueItem(snapshot);
  }
  async function downloadMainVideo() {
    if (selection.currentVideo.value)
      await enqueueItem(snapshotItem(selection.currentVideo.value));
  }
  function downloadVideoByIndex(index: number) {
    const item = selection.visibleList.value[index];
    if (item) void enqueueItem(snapshotItem(item));
  }
  function downloadBackupVideo(choice: MediaSelection) {
    const item = selection.currentVideo.value;
    if (item) void enqueueItem(snapshotItem(item, choice));
  }
  function downloadImage(item: MediaItem) {
    void enqueueItem(snapshotItem(item));
  }
  async function enqueueResource(resource: Resource, title?: string) {
    const item: MediaItem = {
      id: resource.id,
      type: resource.role === "image" ? "image" : "audio",
      title,
      variants: [
        {
          id: "resource",
          metadata: resource.metadata,
          delivery: resource.delivery,
        },
      ],
      default: null,
      default_unavailable_reason: "processing_required",
    };
    await enqueueItem(snapshotItem(item, { variant_id: "resource" }));
  }
  function downloadMusic(attachment: Attachment) {
    void enqueueResource(freeze(attachment.resource), attachment.title);
  }
  function pairResource(item: MediaItem, role: "image" | "motion") {
    if (!readyItem(item)) return;
    const choice = selection.selectionFor(item.id);
    const variant = choice
      ? item.variants.find((variant) => variant.id === choice.variant_id)
      : item.variants.length === 1
        ? item.variants[0]
        : undefined;
    if (!readyVariant(variant) || variant.delivery.kind !== "pair") {
      showToast(
        choice ? "所选实况版本已不可用，请重新选择" : "请先选择实况版本",
        "warning",
      );
      return;
    }
    const resource = variant.delivery.resources.find(
      (resource) => resource.role === role,
    );
    if (resource) {
      const source =
        resourceSource(resource, apiContext) ||
        ("sources" in resource.delivery
          ? resource.delivery.sources[0]
          : undefined);
      void enqueueItem(
        freeze({
          ...snapshotItem(item, { variant_id: variant.id }),
          resource: {
            id: resource.id,
            role: resource.role,
            ...(source?.id ? { source_id: source.id } : {}),
          },
        }),
      );
    }
  }
  const downloadLiveVideo = (item: MediaItem) => pairResource(item, "motion");
  const downloadLiveCover = (item: MediaItem) => pairResource(item, "image");
  async function downloadAllMedia(
    mode: "all" | "images" | "live" | "covers" = "all",
  ) {
    const items =
      videoStore.resultData?.items.filter(
        (item) =>
          mode === "all" ||
          (mode === "images" && item.type === "image") ||
          (["live", "covers"].includes(mode) && item.type === "live_photo"),
      ) || [];
    if (mode === "covers") {
      for (const item of items) downloadLiveCover(item);
      return;
    }
    // A pair is intentionally requested as a pair; it is never promoted into a file default.
    const snapshots = items.map((item) =>
      snapshotItem(
        item,
        selection.selectionFor(item.id) ||
          (readyItem(item) &&
          item.variants.length === 1 &&
          readyVariant(item.variants[0]) &&
          item.variants[0].delivery.kind === "pair"
            ? { variant_id: item.variants[0].id }
            : null),
      ),
    );
    if (!snapshots.length) {
      showToast("没有可下载的条目", "warning");
      return;
    }
    await archiveItems(snapshots, `media_${Date.now()}.zip`);
  }
  const downloadAllImages = () => downloadAllMedia("images");
  async function handleCancelDownload(id: number): Promise<void> {
    const task = taskFor(id);
    if (!task) return;
    const { deliveryId, abortController } = task;
    // Imports and Worker acknowledgements can race completion, native handoff,
    // or a retry reusing this card. Cancellation belongs only to this attempt.
    const currentAttempt = () => {
      const current = taskFor(id);
      return current &&
        current.deliveryId === deliveryId &&
        current.abortController === abortController &&
        (["queued", "preparing", "downloading"].includes(current.status) ||
          current.observation === "unknown")
        ? current
        : undefined;
    };
    if (!currentAttempt()) return;
    if (task.transport === "browser_streaming" && deliveryId) {
      videoStore.updateDownload(id, { statusText: "正在确认取消..." });
      try {
        const { cancelStreaming, hasStreamingTask } = await import(
          "../downloads/streaming"
        );
        const current = currentAttempt();
        if (!current) return;
        if (
          current.transport !== "browser_streaming" ||
          (current.abortController && !hasStreamingTask(deliveryId))
        ) {
          videoStore.cancelDownload(id);
          return;
        }
        await cancelStreaming(deliveryId);
      } catch (error) {
        if (currentAttempt())
          videoStore.updateDownload(id, {
            observation: "unknown",
            speed: 0,
            statusText: "无法确认取消结果",
            error:
              error instanceof Error ? error.message : "请检查浏览器下载列表",
          });
        return;
      }
    }
    if (currentAttempt()) videoStore.cancelDownload(id);
  }

  async function handleRetryDownload(id: number): Promise<void> {
    const current = taskFor(id);
    if (
      !current ||
      current.observation ||
      pendingTaskActions.has(id) ||
      ["queued", "preparing", "downloading"].includes(current.status)
    )
      return;
    // Retry and clear share the old attempt's acknowledgement; only one may act.
    const task = { ...current };
    pendingTaskActions.add(id);
    try {
      if (task.deliveryId && task.transport === "browser_streaming") {
        try {
          const { forgetStreaming } = await import("../downloads/streaming");
          if (!matchesSettledAttempt(task)) return;
          await forgetStreaming(task.deliveryId);
        } catch (error) {
          if (matchesSettledAttempt(task))
            showToast(
              error instanceof Error
                ? error.message
                : "原任务状态未知，请先检查浏览器下载列表",
              "warning",
            );
          return;
        }
      }
      if (!matchesSettledAttempt(task)) return;
      if (task.deliveryId) deliveryTasks.delete(task.deliveryId);
      const retryArchive = archiveRetries.get(id);
      if (retryArchive) {
        await retryArchive();
        return;
      }
      const retryVideo = videoRetries.get(id);
      if (retryVideo) {
        await retryVideo();
        return;
      }
      // 分片任务重试：重走合并下载，避免退化成只下首片
      const rawSegments = task.segments;
      if (Array.isArray(rawSegments) && rawSegments.length > 0) {
        const segments = rawSegments.map((s) =>
          typeof s === "string" ? s : (s as HlsSegment),
        );
        await downloadSegments(segments, task.filename, id);
        return;
      }
      if (task.url) await downloadFile(task.url, task.filename, id);
    } finally {
      pendingTaskActions.delete(id);
    }
  }

  async function handleQueryDownload(id: number): Promise<void> {
    const task = taskFor(id);
    if (!task?.deliveryId || task.transport !== "browser_streaming" || !task.recoveryExpired) return;
    const { queryStreaming } = await import("../downloads/streaming");
    if (taskFor(id)?.deliveryId !== task.deliveryId) return;
    await queryStreaming(task.deliveryId);
  }

  /** Called synchronously by a real user click. Browser-owned progress is unknowable here. */
  function handleBrowserDownload(id: number): void {
    if (pendingTaskActions.has(id)) return;
    const task = taskFor(id);
    if (
      !task?.nativeAvailable ||
      !task.url ||
      !/^(?:https?:\/\/|\/(?!\/))/i.test(task.url)
    )
      return;
    // Opening a source is a recovery action. A stale click must not cancel a live transfer.
    if (
      task.observation ||
      !["failed", "cancelled", "interrupted"].includes(task.status)
    )
      return;
    task.abortController?.abort();
    videoStore.updateDownload(id, {
      status: "handed-off",
      statusText: "已交给浏览器",
      abortController: null,
      speed: 0,
      averageSpeed: 0,
      percent: 0,
      error: null,
    });
    triggerBrowserDownload(task.url, task.filename, true);
    showToast(
      "请在打开的页面或浏览器下载列表查看进度；本站无法确认下载是否完成",
      "info",
      6000,
    );
  }

  async function handleClearCompleted(id: number | "all"): Promise<void> {
    const tasks = videoStore.downloads
      .filter(
        (task) =>
          clearable(task) &&
          !pendingTaskActions.has(task.id) &&
          (id === "all" || task.id === id),
      )
      .map((task) => ({ ...task }));
    // Reserve the whole batch before waiting: legacy ZIP/segment retries have no delivery ID.
    tasks.forEach((task) => pendingTaskActions.add(task.id));
    try {
      for (const task of tasks) {
        if (!matchesSettledAttempt(task)) continue;
        if (task.deliveryId && task.transport === "browser_streaming") {
          try {
            const { forgetStreaming } = await import("../downloads/streaming");
            if (!matchesSettledAttempt(task)) continue;
            await forgetStreaming(task.deliveryId);
          } catch (error) {
            if (matchesSettledAttempt(task))
              showToast(
                error instanceof Error
                  ? error.message
                  : "原任务状态未知，暂不能清除",
                "warning",
              );
            continue;
          }
        }
        if (!matchesSettledAttempt(task)) continue;
        if (task.deliveryId) deliveryTasks.delete(task.deliveryId);
        archiveRetries.delete(task.id);
        videoRetries.delete(task.id);
        sourceContexts.delete(task.id);
        // Clearing a confirmed-gone execution is history cleanup, not a new
        // cancellation intent. Pinia's removeDownload aborts a saved controller.
        if (clearableUnknown(task)) videoStore.updateDownload(task.id, { abortController: null });
        videoStore.removeDownload(task.id);
      }
    } finally {
      tasks.forEach((task) => pendingTaskActions.delete(task.id));
    }
  }

  return {
    restoreDownloads,
    downloadFile,
    downloadMainVideo,
    downloadVideoByIndex,
    downloadSelected,
    downloadAllVideos,
    downloadVideosAsZip,
    downloadAllMedia,
    downloadBackupVideo,
    downloadMusic,
    downloadLiveVideo,
    downloadLiveCover,
    downloadImage,
    downloadAllImages,
    handleCancelDownload,
    handleRetryDownload,
    handleQueryDownload,
    handleBrowserDownload,
    handleClearCompleted,
  };
}
