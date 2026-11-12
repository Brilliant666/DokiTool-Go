import type { DownloadSnapshot, SourceProbe } from "../downloads/types";
import { confirmedFormatMime, extensionsFor } from "../downloads/file";
import type {
  DownloadTask,
  MediaItem,
  MediaSelection,
  ReadyVariant,
  Resource,
  Attachment,
  Source,
  ToastType,
  YouTubeHelperState,
} from "../types/media";
import {
  downloadViaYouTubeHelper,
  isValidGooglevideoPlaybackUrl,
  muxDownloadViaYouTubeHelper,
  selectYouTubeMuxPair,
} from "../utils/youtubeHelperCore";
import { buildMediaFilename } from "../utils/videoFilename";
import { fetchMediaBlob, linkAbortSignals, acquireBufferedSlot } from "../downloads/buffered";
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
import type { TbvideoItem } from "../services/tbvideo";
import { reportTbvideo } from "../../public/downloads/tbvideo.js";
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
): boolean {
  const isolate = newTab || !url.startsWith("blob:");
  if (isolate) {
    const activation =
      typeof navigator !== "undefined" ? navigator.userActivation : undefined;
    if (activation && !activation.isActive) {
      return false;
    }
    if (
      typeof window !== "undefined" &&
      "open" in window &&
      typeof window.open !== "function"
    ) {
      return false;
    }
  }
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  // 无 Referer 直连，降低 CDN 防盗链拒绝概率
  anchor.referrerPolicy = "no-referrer";
  if (isolate) {
    anchor.target = "_blank";
    anchor.rel = "noopener noreferrer";
  }
  let clickEvent: Event | undefined;
  let threw = false;
  const onClick = (event: Event) => {
    clickEvent = event;
  };
  anchor.addEventListener?.("click", onClick);
  document.body.appendChild(anchor);
  try {
    anchor.click();
  } catch {
    threw = true;
  } finally {
    anchor.removeEventListener?.("click", onClick);
    anchor.remove();
  }
  return !threw && !clickEvent?.defaultPrevented;
}

function isToutiaovodAttachmentUrl(rawUrl: string): boolean {
  try {
    const parsed = new URL(rawUrl);
    if (parsed.protocol !== "https:" || parsed.username || parsed.password) {
      return false;
    }
    const host = parsed.hostname.toLowerCase().replace(/\.$/, "");
    if (!host.endsWith(".toutiaovod.com") || host === ".toutiaovod.com") {
      return false;
    }
    return parsed.pathname.length > 1;
  } catch {
    return false;
  }
}

function inferredVideoFileExtension(
  rawUrl: string,
  platform?: string,
  hasTbvideo = false,
): string | undefined {
  try {
    const parsed = new URL(rawUrl);
    const queryMime = (parsed.searchParams.get("mime_type") || "")
      .trim()
      .toLowerCase();
    if (queryMime === "video_mp4" || queryMime === "video/mp4") {
      return "mp4";
    }
    if (queryMime === "video_webm" || queryMime === "video/webm") {
      return "webm";
    }
    if (queryMime === "video_quicktime" || queryMime === "video/quicktime") {
      return "mov";
    }
    if (queryMime) {
      return undefined;
    }
    const pathLower = parsed.pathname.toLowerCase();
    const pathExtMatch = /\.([a-z0-9]{2,5})$/.exec(pathLower);
    if (pathExtMatch) {
      const pathExt = pathExtMatch[1];
      if (pathExt === "mp4" || pathExt === "m4v") return "mp4";
      if (pathExt === "webm" || pathExt === "mov" || pathExt === "mkv") {
        return pathExt;
      }
      return undefined;
    }
    const host = parsed.hostname.toLowerCase().replace(/\.$/, "");
    if (isToutiaovodAttachmentUrl(rawUrl)) {
      return "mp4";
    }
    if (
      hasTbvideo ||
      ((platform === "zuiyou" || platform === "pipigx") &&
        (host === "web-v01.izuiyou.com" ||
          host === "tbvideo.ixiaochuan.cn" ||
          host === "video01.szsttkj.com" ||
          host === "video03.szsttkj.com" ||
          host === "xlal-ppvideo.szsttkj.com"))
    ) {
      return "mp4";
    }
    return undefined;
  } catch {
    return undefined;
  }
}

function isCorsBlockedDirectCdn(rawUrl: string): boolean {
  if (isToutiaovodAttachmentUrl(rawUrl)) {
    return true;
  }
  try {
    const parsed = new URL(rawUrl);
    const host = parsed.hostname.toLowerCase();
    return host === "googlevideo.com" || host.endsWith(".googlevideo.com");
  } catch {
    return false;
  }
}

// URL parameters only indicate that a direct attachment attempt can be made;
// they never prove that the actual upstream HTTP response is a verified attachment.
function canAttemptDirectAttachment(rawUrl: string): boolean {
  if (isToutiaovodAttachmentUrl(rawUrl)) {
    return true;
  }
  try {
    const parsed = new URL(rawUrl);
    const host = parsed.hostname.toLowerCase();
    if (host !== "googlevideo.com" && !host.endsWith(".googlevideo.com")) {
      return false;
    }
    if (parsed.pathname.replace(/\/+$/, "") !== "/videoplayback") {
      return false;
    }
    const itag = parsed.searchParams.get("itag")?.trim() ?? "";
    const title = parsed.searchParams.get("title")?.trim() ?? "";
    if (itag !== "18" || !title) {
      return false;
    }
    const sparams = (parsed.searchParams.get("sparams") ?? "")
      .split(",")
      .map((s) => s.trim().toLowerCase());
    return !sparams.includes("title");
  } catch {
    return false;
  }
}

function formatYouTubeSavePathLabel(mode?: string): string | undefined {
  switch (mode) {
    case "opfs":
      return "保存路径：默认下载目录（OPFS 流式写盘）";
    case "stream_picker":
      return "保存路径：保存对话框流式写盘";
    case "memory_buffer":
      return "保存路径：内存合成后浏览器下载";
    case "gm_download":
      return "保存路径：助手直接下载";
    case "browser_native":
      return "保存路径：默认下载目录（浏览器附件直下）";
    default:
      return undefined;
  }
}

export function useMediaDownload({
  videoStore,
  showToast,
  selection,
  resolveItem,
}: UseMediaDownloadOptions) {
  const apiContext = { apiOrigin: apiConfig.baseUrl };
  const currentYouTubeHelper = (): YouTubeHelperState | undefined =>
    videoStore.resultData?.platform === "youtube"
      ? ((videoStore.resultData.extra as Record<string, unknown> | undefined)
          ?.youtube_helper as YouTubeHelperState | undefined)
      : undefined;
  const freeze = <T>(value: T): T => JSON.parse(JSON.stringify(value));
  const effectiveSelectedUrl = () => selection.currentVideoUrl.value;
  const archiveRetries = new Map<number, () => Promise<void>>();
  const videoRetries = new Map<number, (refresh?: boolean) => Promise<boolean>>();
  const pendingTaskActions = new Set<number>();
  // Page-owned, exact selections survive the URL-free resolve/acknowledgement
  // window. Never infer these identities from restored URL-only history.
  const fileTaskSelections = new Map<number, string>();
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
      tbvideo?: { item: TbvideoItem; candidate: string };
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
    (!task.observation && ["completed", "cancelled", "failed", "handed-off", "interrupted", "manual"].includes(task.status));
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
  async function fetchBlob(url: string, signal?: AbortSignal, expectedSize?: number): Promise<Blob> {
    return fetchMediaBlob(url, { signal, expectedSize });
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
    manual: "打开源链接",
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
      timing: snapshot.timing,
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
    const isCorsBlockedManualTask = (task: DownloadTask) =>
      task.status === "manual" &&
      typeof task.url === "string" &&
      task.url.length > 0 &&
      isCorsBlockedDirectCdn(task.url);
    let duplicate = matchingTasks().some(
      (task) =>
        (active(task.status) && !task.ownerGone) || isCorsBlockedManualTask(task),
    );
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
            isCorsBlockedManualTask(task) ||
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
      fileTaskSelections.delete(downloadId);
      sourceContexts.delete(downloadId);
      showToast("该资源已有下载任务", "info");
      return;
    }
    const deliveryId =
      globalThis.crypto?.randomUUID?.() ??
      `page-${Date.now()}-${++pageTaskSequence}`;
    deliveryTasks.set(deliveryId, downloadId);
    if (isCorsBlockedDirectCdn(url)) {
      if (controller.signal.aborted) {
        videoStore.updateDownload(downloadId, {
          deliveryId,
          status: "cancelled",
          statusText: "已取消",
          abortController: null,
          error: "用户取消下载",
          nativeAvailable: true,
        });
        return;
      }
      const ytHelper = currentYouTubeHelper();
      if (ytHelper && ytHelper.status !== "ready") {
        videoStore.removeDownload(downloadId);
        showToast(
          ytHelper.message || "请先安装并启用 YouTube 下载助手脚本后再下载",
          "warning",
          5000,
        );
        return;
      }
      if (canAttemptDirectAttachment(url)) {
        const isYtNative = !isToutiaovodAttachmentUrl(url);
        if (triggerBrowserDownload(url, filename, true)) {
          videoStore.updateDownload(downloadId, {
            deliveryId,
            transport: "browser_native",
            status: "handed-off",
            statusText: "已交给浏览器",
            ...(isYtNative
              ? {
                  savePath: "browser_native",
                  savePathLabel: formatYouTubeSavePathLabel("browser_native"),
                }
              : {}),
            abortController: null,
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
            timing: undefined,
            workerBuild: undefined,
          });
          showToast(
            "已在新标签页尝试浏览器附件下载，请在浏览器下载列表或打开的标签页查看",
            "info",
            6000,
          );
          return;
        }
        videoStore.updateDownload(downloadId, {
          deliveryId,
          transport: undefined,
          status: "manual",
          statusText: "打开源链接",
          abortController: null,
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
          timing: undefined,
          workerBuild: undefined,
        });
        showToast(
          "浏览器限制了新标签页打开，请点击下载卡片中的“打开源链接”继续",
          "info",
          6000,
        );
        return;
      }
      if (ytHelper?.status === "ready") {
        let lastBytes = 0;
        let lastSampleAt = startedAt;
        const ctxSources = sourceContexts.get(downloadId)?.sources ?? [];
        const matchedSource = ctxSources.find(
          (s) => comparableUrl(sourceUrl(s, apiContext)) === comparableUrl(url),
        );
        const knownSize = matchedSource?.size_bytes || 0;
        videoStore.updateDownload(downloadId, {
          deliveryId,
          abortController: controller,
          status: "downloading",
          statusText: "通过助手分块并发保存中...",
          percent: 0,
          loaded: 0,
          total: knownSize,
          speed: 0,
          elapsed: 0,
          averageSpeed: 0,
          transferred: 0,
          error: null,
          progressKind: "bytes",
          nativeAvailable: false,
          observation: undefined,
          transferElapsed: undefined,
          timing: undefined,
          workerBuild: undefined,
        });
        try {
          const saved = await downloadViaYouTubeHelper({
            url,
            filename,
            size: knownSize || undefined,
            taskId: deliveryId,
            signal: controller.signal,
            onProgress(detail) {
              if (!ownsTask(downloadId, controller)) return;
              const now = Date.now();
              const elapsed = Math.max(0.001, (now - startedAt) / 1000);
              const dt = Math.max(0.001, (now - lastSampleAt) / 1000);
              const speed = Math.max(0, (detail.loaded - lastBytes) / dt);
              lastBytes = detail.loaded;
              lastSampleAt = now;
              videoStore.updateDownload(downloadId, {
                loaded: detail.loaded,
                total: detail.total,
                transferred: detail.loaded,
                percent: detail.percent,
                elapsed,
                speed,
                averageSpeed: detail.loaded / elapsed,
                ...(detail.mode
                  ? {
                      savePath: detail.mode,
                      savePathLabel: formatYouTubeSavePathLabel(detail.mode),
                    }
                  : {}),
                statusText: `通过助手分块并发保存中（${detail.percent}%）...`,
              });
            },
          });
          if (!ownsTask(downloadId, controller)) return;
          const elapsed = Math.max(0, (Date.now() - startedAt) / 1000);
          const finalMode = saved?.mode || taskFor(downloadId)?.savePath;
          videoStore.updateDownload(downloadId, {
            filename: saved?.filename || filename,
            status: "completed",
            statusText: "已完成",
            ...(finalMode
              ? {
                  savePath: finalMode,
                  savePathLabel: formatYouTubeSavePathLabel(finalMode),
                }
              : {}),
            percent: 100,
            speed: 0,
            elapsed,
            abortController: null,
          });
          showToast(`已通过助手保存：${saved?.filename || filename}`);
        } catch (error) {
          if (!ownsTask(downloadId, controller)) return;
          const cancelled =
            controller.signal.aborted ||
            (error instanceof Error && error.name === "AbortError");
          const message =
            error instanceof Error ? error.message : "通过助手保存失败";
          videoStore.updateDownload(downloadId, {
            status: cancelled ? "cancelled" : "failed",
            statusText: cancelled ? "已取消" : "下载失败",
            speed: 0,
            error: cancelled ? null : message,
            abortController: null,
          });
          if (!cancelled) {
            showToast(message, "error");
          }
        }
        return;
      }
      videoStore.updateDownload(downloadId, {
        deliveryId,
        transport: undefined,
        status: "manual",
        statusText: "打开源链接",
        abortController: null,
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
        timing: undefined,
        workerBuild: undefined,
      });
      showToast(
        "该直链未提供附件下载头，请点击下载卡片中的“打开源链接”手动保存",
        "info",
        6000,
      );
      return;
    }
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
      timing: undefined,
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
      const metadata = context?.tbvideo?.item;
      const tbvideoEvent = metadata && { platform: metadata.platform, pid: metadata.pid, vid: metadata.vid };
      const result = await downloadManager.run(
        context?.tbvideo
          ? { id: deliveryId, url: context.tbvideo.candidate, routes: [], filename, startedAt, formatMime: context.formatMime, tbvideo: true, tbvideoEvent }
          : { id: deliveryId, url, filename, routes, sourceSizes, startedAt, probe, formatMime: context?.formatMime },
        controller.signal,
        (snapshot) => {
          if (
            taskFor(downloadId)?.deliveryId === deliveryId &&
            taskFor(downloadId)?.status !== "handed-off"
          ) {
            // Use this attempt's confirmed delivery, never a previous task error.
            cancellationReason = snapshot.status === "cancelled" && !snapshot.observation
              ? snapshot.error : undefined;
            applyDelivery(context?.tbvideo ? { ...snapshot, url } : snapshot);
          }
        },
      );
      if (!ownsTask(downloadId, controller)) return;
      if (context?.tbvideo) {
        // Every admitted Worker task (even an unclaimed one) owns its report.
        // A pre-START fallback has no tbvideoAttempted marker.
        if (result.status === "manual" && result.tbvideoAttempted === undefined) void reportTbvideo(tbvideoEvent, result);
        return;
      }
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
    let release: (() => void) | undefined;
    try {
      videoStore.updateDownload(downloadId, { status: "queued", statusText: "等待分片或打包任务..." });
      release = await acquireBufferedSlot(abortController.signal);
      checkAbort(abortController.signal);
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
      release?.();
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
    let release: (() => void) | undefined;
    try {
      videoStore.updateDownload(id, { status: "queued", statusText: "等待分片或打包任务..." });
      release = await acquireBufferedSlot(controller.signal);
      checkAbort(controller.signal);
      videoStore.updateDownload(id, { status: "preparing", statusText: "准备打包..." });
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
      const entryCount = entries.length;
      while (entries.length) {
        const entry = entries.shift()!;
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
      showToast(`已打包 ${entryCount} 个文件`);
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
      release?.();
      monitor.finish();
    }
  }

  type ItemSnapshot = {
    item: MediaItem;
    choice: MediaSelection | null;
    title?: string;
    total: number;
    version: number;
    selectionKey: string;
    platform?: string;
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
      total: videoStore.resultData?.items?.length ?? 1,
      platform: videoStore.resultData?.platform,
      version: videoStore.resultVersion,
    });
  };
  function refreshable(snapshot: ItemSnapshot): boolean {
    if (snapshot.resource || !readyItem(snapshot.item) || !snapshot.item.resolve_url || !snapshot.choice?.source_id) return false;
    const variant = snapshot.item.variants.find(candidate => candidate.id === snapshot.choice!.variant_id);
    return readyVariant(variant) && variant.delivery.kind === "file" &&
      variant.delivery.sources.some(source => source.id === snapshot.choice!.source_id);
  }
  function fileSelectionKey(snapshot: ItemSnapshot): string | undefined {
    if (snapshot.resource || !readyItem(snapshot.item) || !snapshot.choice?.source_id)
      return;
    const variants = snapshot.item.variants.filter(candidate => candidate.id === snapshot.choice!.variant_id);
    if (variants.length !== 1 || !readyVariant(variants[0])) return;
    const delivery = variants[0].delivery;
    if (delivery.kind !== "file" ||
        delivery.sources.filter(source => source.id === snapshot.choice!.source_id).length !== 1)
      return;
    return JSON.stringify([snapshot.version, snapshot.item.id, snapshot.item.type,
      snapshot.choice.variant_id, snapshot.choice.source_id]);
  }
  function selectionInFlight(key: string | undefined, exceptId?: number): boolean {
    if (!key) return false;
    for (const [id, selected] of fileTaskSelections) {
      const task = taskFor(id);
      if (!task) {
        fileTaskSelections.delete(id);
        continue;
      }
      if (id !== exceptId && selected === key &&
          (pendingTaskActions.has(id) || task.observation === "unknown" ||
           ["queued", "preparing", "downloading"].includes(task.status)))
        return true;
    }
    return false;
  }
  function selectionBusyNotice(): void {
    showToast("该版本和来源已有任务正在处理，请在下载列表查看进度", "info");
  }
  function resolveYouTubeTrackSuffix(
    snapshot: ItemSnapshot,
    variant?: {
      label?: string;
      metadata?: {
        mime_type?: string;
        width?: number;
        height?: number;
        has_audio?: boolean;
        video_codec?: string;
      };
    },
    ext?: string,
  ): string | undefined {
    if (snapshot.platform !== "youtube") return undefined;
    const mime = String(variant?.metadata?.mime_type || "").toLowerCase();
    if (
      snapshot.item.type === "audio" ||
      ext === "m4a" ||
      mime.startsWith("audio/") ||
      (variant?.metadata?.has_audio === true &&
        !variant?.metadata?.video_codec &&
        !variant?.metadata?.width &&
        !variant?.metadata?.height)
    ) {
      return "audio";
    }
    const w = Number(variant?.metadata?.width);
    const h = Number(variant?.metadata?.height);
    if (Number.isFinite(w) && w > 0 && Number.isFinite(h) && h > 0) {
      return `${Math.min(w, h)}p`;
    }
    const labelMatch = String(variant?.label || "").match(/(\d{3,4}p)/i);
    if (labelMatch) {
      return labelMatch[1].toLowerCase();
    }
    return undefined;
  }
  const filenameFor = (
    snapshot: ItemSnapshot,
    extension: string,
    variant?: {
      label?: string;
      metadata?: {
        mime_type?: string;
        width?: number;
        height?: number;
        has_audio?: boolean;
        video_codec?: string;
      };
    },
  ) => {
    const rawTitle =
      snapshot.platform === "youtube" && snapshot.item.type === "audio"
        ? snapshot.title ||
          (snapshot.item.title || "").replace(/\s*\((?:M4A|WEBM)[^)]*\)$/i, "")
        : snapshot.item.title || snapshot.title;
    return buildMediaFilename({
      title: rawTitle,
      index: snapshot.item.index,
      total: snapshot.total,
      suffix: resolveYouTubeTrackSuffix(snapshot, variant, extension),
      fallback: snapshot.item.id,
      extension,
    });
  };
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
  async function resolvedTarget(snapshot: ItemSnapshot, signal: AbortSignal, refresh = false) {
    checkAbort(signal);
    let item = snapshot.item;
    let target = snapshotTarget(snapshot, item);
    if (refresh || target.kind === "needs_resolve" || target.kind === "needs_refresh") {
      // Explicit user recovery is not an inference that a 403 means expiry.
      // Each invocation performs at most one resolve, never a retry loop.
      if (refresh && !refreshable(snapshot)) throw new Error("原任务缺少可核对的刷新入口，请重新解析并选择");
      const deadline = new AbortController();
      const timeout = setTimeout(
        () => deadline.abort(new Error("解析准备超过 30 秒，请重试")),
        30000,
      );
      const linked = linkAbortSignals([signal, deadline.signal]);
      const combined = linked.signal;
      try {
        item = await abortable(resolveItem(item, combined), combined);
      } catch (error) {
        if (deadline.signal.aborted && !signal.aborted)
          throw deadline.signal.reason;
        throw error;
      } finally {
        clearTimeout(timeout);
        linked.dispose();
      }
      checkAbort(signal);
      if (item.id !== snapshot.item.id || item.type !== snapshot.item.type)
        throw new Error("刷新响应与原作品不一致，请重新解析并选择");
      target = snapshotTarget(snapshot, item);
      if (refresh && target.kind !== "file" && "variant" in target)
        throw new Error("原版本不再是完整文件，请重新选择");
    }
    // Never substitute a different variant when an explicit choice disappeared.
    if (!("variant" in target)) throw new Error(target.message);
    return { item, target };
  }
  function resolveYouTubeVideoExt(variant: ReadyVariant, rawUrl: string): string {
    const delivery = variant.delivery;
    const extension = mediaExtension(
      variant.metadata,
      delivery.kind === "mux" || delivery.kind === "pair" ? undefined : delivery,
    );
    const formatMime = confirmedFormatMime(variant.metadata);
    const suffixes = extensionsFor(formatMime || "");
    let resolvedExt =
      suffixes.length && !suffixes.includes(extension) ? suffixes[0] : extension;
    if (resolvedExt === "bin") {
      resolvedExt = inferredVideoFileExtension(rawUrl, "youtube", false) || "mp4";
    }
    return resolvedExt === "webm" ? "webm" : "mp4";
  }

  function findBestYouTubeAudioCandidate(
    item: MediaItem,
    preferredAudioVariant?: ReadyVariant,
  ): { variant: ReadyVariant; url: string; ext: "m4a" | "webm" } | null {
    const candidates: ReadyVariant[] = [];
    if (preferredAudioVariant) {
      candidates.push(preferredAudioVariant);
    }
    if (readyItem(item)) {
      for (const v of item.variants) {
        if (readyVariant(v) && v.delivery.kind === "file") {
          candidates.push(v);
        }
      }
    }
    for (const att of videoStore.resultData?.attachments ?? []) {
      if (
        att?.resource?.role === "audio" &&
        att.resource.delivery.kind === "file"
      ) {
        candidates.push({
          id: att.id || `att-${att.resource.id || "audio"}`,
          state: "ready",
          label: att.title || "原声轨",
          metadata: att.resource.metadata,
          delivery: att.resource.delivery,
        });
      }
    }
    const seen = new Set<string>();
    for (const cand of candidates) {
      if (seen.has(cand.id) || cand.delivery.kind !== "file") continue;
      seen.add(cand.id);
      const meta = cand.metadata || {};
      const mime = String(meta.mime_type || "").toLowerCase();
      const rawExt = String(meta.extension || meta.container || "").toLowerCase();
      const isStandaloneAudio =
        meta.has_audio === true &&
        !meta.video_codec &&
        !meta.width &&
        !meta.height &&
        (mime.startsWith("audio/") || rawExt === "m4a" || rawExt === "webm" || rawExt === "mp4");
      if (!isStandaloneAudio) continue;
      const src =
        cand.delivery.sources.find(
          (s) => s.availability !== "unavailable" && isValidGooglevideoPlaybackUrl(sourceUrl(s, apiContext)),
        ) ||
        cand.delivery.sources.find((s) => isValidGooglevideoPlaybackUrl(sourceUrl(s, apiContext)));
      if (!src) continue;
      const ext: "m4a" | "webm" =
        rawExt === "webm" || mime === "audio/webm" ? "webm" : "m4a";
      return {
        variant: cand,
        url: sourceUrl(src, apiContext),
        ext,
      };
    }
    return null;
  }

  async function runYouTubeSeparateTrackSave(params: {
    snapshot: ItemSnapshot;
    item: MediaItem;
    videoTarget: Extract<DownloadTarget, { kind: "file" }>;
    preferredAudioVariant?: ReadyVariant;
    primaryTaskId?: number;
    fallbackReason: string | null;
    startedAt: number;
  }): Promise<boolean> {
    const {
      snapshot,
      item,
      videoTarget,
      preferredAudioVariant,
      primaryTaskId,
      fallbackReason,
      startedAt,
    } = params;
    const videoExt = resolveYouTubeVideoExt(videoTarget.variant, videoTarget.url);
    const videoFilename = filenameFor(snapshot, videoExt, videoTarget.variant);
    const audioCand = findBestYouTubeAudioCandidate(item, preferredAudioVariant);
    const effectiveReason =
      !audioCand && fallbackReason
        ? `${fallbackReason}（未找到独立音轨，仅保存视频轨）`
        : !audioCand
          ? "未找到独立音轨，仅保存视频轨"
          : fallbackReason;

    const videoTaskId =
      primaryTaskId ??
      videoStore.addDownload({
        filename: videoFilename,
        url: videoTarget.url,
        fallbackReason: effectiveReason,
        statusText: "准备下载视频轨...",
      });
    const videoController = new AbortController();
    videoStore.updateDownload(videoTaskId, {
      filename: videoFilename,
      url: videoTarget.url,
      fallbackReason: effectiveReason,
      abortController: videoController,
      error: null,
      refreshAvailable: false,
    });
    sourceContexts.set(videoTaskId, {
      routes: videoTarget.routes,
      selectedUrl: videoTarget.url,
      resultVersion: snapshot.version,
      itemId: item.id,
      variantId: videoTarget.variant.id,
      sources:
        videoTarget.variant.delivery.kind === "file"
          ? videoTarget.variant.delivery.sources
          : [],
    });
    videoRetries.set(videoTaskId, async () => {
      await downloadFile(
        videoTarget.url,
        videoFilename,
        videoTaskId,
        new AbortController(),
        Date.now(),
      );
      return true;
    });
    const videoPromise = downloadFile(
      videoTarget.url,
      videoFilename,
      videoTaskId,
      videoController,
      startedAt,
    );

    if (!audioCand) {
      await videoPromise;
      return true;
    }

    const audioSnapshot = freeze({
      ...snapshot,
      item: { ...item, type: "audio" } as MediaItem,
    });
    const audioFilename = filenameFor(
      audioSnapshot,
      audioCand.ext,
      audioCand.variant,
    );
    const audioController = new AbortController();
    const audioTaskId = videoStore.addDownload({
      filename: audioFilename,
      url: audioCand.url,
      fallbackReason: effectiveReason,
      statusText: "准备下载音轨...",
      abortController: audioController,
    });
    sourceContexts.set(audioTaskId, {
      routes: [audioCand.url],
      selectedUrl: audioCand.url,
      resultVersion: snapshot.version,
      itemId: item.id,
      variantId: audioCand.variant.id,
      sources:
        audioCand.variant.delivery.kind === "file"
          ? audioCand.variant.delivery.sources
          : [],
    });
    videoRetries.set(audioTaskId, async () => {
      await downloadFile(
        audioCand.url,
        audioFilename,
        audioTaskId,
        new AbortController(),
        Date.now(),
      );
      return true;
    });
    const audioPromise = downloadFile(
      audioCand.url,
      audioFilename,
      audioTaskId,
      audioController,
      Date.now(),
    );
    await Promise.all([videoPromise, audioPromise]);
    return true;
  }

  async function enqueueItem(
    snapshot: ItemSnapshot,
    retryId?: number,
    refresh = false,
    options?: { separateOnly?: boolean },
  ): Promise<boolean> {
    if (snapshot.platform === "youtube") {
      const ytHelper = currentYouTubeHelper();
      if (ytHelper && ytHelper.status !== "ready") {
        showToast(
          ytHelper.message || "请先安装并启用 YouTube 下载助手脚本后再下载",
          "warning",
          5000,
        );
        return false;
      }
      if (ytHelper?.status === "ready" && !refresh && snapshot.item.type === "video") {
        const syncTarget = snapshotTarget(snapshot, snapshot.item);
        if (
          syncTarget.kind === "file" &&
          syncTarget.variant.metadata?.has_audio === false
        ) {
          const selectedKey = fileSelectionKey(snapshot);
          if (selectionInFlight(selectedKey, retryId)) {
            selectionBusyNotice();
            return false;
          }
          const pair = selectYouTubeMuxPair(
            syncTarget.variant,
            readyItem(snapshot.item) ? snapshot.item.variants : [],
            videoStore.resultData?.attachments ?? [],
          );
          if (options?.separateOnly) {
            return runYouTubeSeparateTrackSave({
              snapshot,
              item: snapshot.item,
              videoTarget: syncTarget,
              preferredAudioVariant: pair.ok ? pair.audioVariant : undefined,
              primaryTaskId: retryId,
              fallbackReason: null,
              startedAt: Date.now(),
            });
          }
          if (!pair.ok) {
            const fallbackReason = `合并保存不可用（${pair.reason}），已自动回退为音视频分开保存`;
            showToast(fallbackReason, "warning", 6000);
            return runYouTubeSeparateTrackSave({
              snapshot,
              item: snapshot.item,
              videoTarget: syncTarget,
              preferredAudioVariant: undefined,
              primaryTaskId: retryId,
              fallbackReason,
              startedAt: Date.now(),
            });
          }

          const mergedFilename = filenameFor(
            snapshot,
            pair.outputExtension,
            syncTarget.variant,
          );
          const id =
            retryId ??
            videoStore.addDownload({
              filename: mergedFilename,
              url: syncTarget.url,
              statusText: "准备浏览器内无损合流...",
            });
          if (selectedKey) fileTaskSelections.set(id, selectedKey);
          const controller = new AbortController();
          const startedAt = Date.now();
          let lastBytes = 0;
          let lastSampleAt = startedAt;
          const estTotal =
            (syncTarget.source.size_bytes || 0) +
            (pair.audioSource.size_bytes || 0);
          const deliveryId =
            globalThis.crypto?.randomUUID?.() ??
            `yt-mux-${Date.now()}-${++pageTaskSequence}`;
          deliveryTasks.set(deliveryId, id);
          videoStore.updateDownload(id, {
            deliveryId,
            filename: mergedFilename,
            url: syncTarget.url,
            abortController: controller,
            status: "downloading",
            statusText: "浏览器内无损合流保存中...",
            error: null,
            fallbackReason: null,
            savePath: undefined,
            savePathLabel: undefined,
            nativeAvailable: false,
            refreshAvailable: false,
            loaded: 0,
            total: estTotal,
            transferred: 0,
            progressKind: "bytes",
            elapsed: 0,
            speed: 0,
            averageSpeed: 0,
            percent: 0,
            observation: undefined,
            transferElapsed: undefined,
            timing: undefined,
            workerBuild: undefined,
          });
          videoRetries.set(id, (forceRefresh = false) =>
            enqueueItem(snapshot, id, forceRefresh),
          );

          const muxPromise = muxDownloadViaYouTubeHelper({
            videoUrl: syncTarget.url,
            audioUrl: sourceUrl(pair.audioSource, apiContext),
            videoSize: syncTarget.source.size_bytes,
            audioSize: pair.audioSource.size_bytes,
            filename: mergedFilename,
            outputContainer: pair.outputContainer,
            taskId: deliveryId,
            signal: controller.signal,
            onProgress(detail) {
              if (!ownsTask(id, controller)) return;
              const now = Date.now();
              const elapsed = Math.max(0.001, (now - startedAt) / 1000);
              const dt = Math.max(0.001, (now - lastSampleAt) / 1000);
              const speed = Math.max(0, (detail.loaded - lastBytes) / dt);
              lastBytes = detail.loaded;
              lastSampleAt = now;
              videoStore.updateDownload(id, {
                loaded: detail.loaded,
                total: detail.total,
                transferred: detail.loaded,
                percent: detail.percent,
                elapsed,
                speed,
                averageSpeed: detail.loaded / elapsed,
                ...(detail.mode
                  ? {
                      savePath: detail.mode,
                      savePathLabel: formatYouTubeSavePathLabel(detail.mode),
                    }
                  : {}),
                statusText: `浏览器内无损合流保存中（${detail.percent}%）...`,
              });
            },
          });

          try {
            const saved = await muxPromise;
            if (!ownsTask(id, controller)) return false;
            const elapsed = Math.max(0, (Date.now() - startedAt) / 1000);
            const finalMode = saved?.mode || taskFor(id)?.savePath;
            videoStore.updateDownload(id, {
              filename: saved?.filename || mergedFilename,
              status: "completed",
              statusText: "已完成",
              ...(finalMode
                ? {
                    savePath: finalMode,
                    savePathLabel: formatYouTubeSavePathLabel(finalMode),
                  }
                : {}),
              percent: 100,
              speed: 0,
              elapsed,
              abortController: null,
            });
            showToast(`已合并保存：${saved?.filename || mergedFilename}`);
            return true;
          } catch (error) {
            if (!ownsTask(id, controller)) return false;
            const cancelled =
              controller.signal.aborted ||
              (error instanceof Error && error.name === "AbortError") ||
              (error as { code?: string })?.code === "USER_CANCELLED";
            if (cancelled) {
              videoStore.updateDownload(id, {
                status: "cancelled",
                statusText: "已取消",
                speed: 0,
                error: null,
                abortController: null,
              });
              return false;
            }
            const errMsg =
              error instanceof Error && error.message
                ? error.message
                : "合流过程异常";
            const fallbackReason = `合并保存未完成（${errMsg}），已自动回退为音视频分开保存`;
            fileTaskSelections.delete(id);
            videoStore.updateDownload(id, {
              abortController: null,
              fallbackReason,
            });
            showToast(fallbackReason, "warning", 6000);
            return runYouTubeSeparateTrackSave({
              snapshot,
              item: snapshot.item,
              videoTarget: syncTarget,
              preferredAudioVariant: pair.audioVariant,
              primaryTaskId: id,
              fallbackReason,
              startedAt: Date.now(),
            });
          }
        }
      }
    }
    const selected = fileSelectionKey(snapshot);
    if (selectionInFlight(selected, retryId)) {
      selectionBusyNotice();
      return false;
    }
    const id =
      retryId ??
      videoStore.addDownload({
        filename: filenameFor(snapshot, "bin"),
        statusText: "准备下载...",
      });
    if (selected) fileTaskSelections.set(id, selected);
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
    videoStore.updateDownload(id, { refreshAvailable: refreshable(snapshot) });
    // A settled old attempt is already acknowledged before this recovery starts.
    // Do not expose its URL or delivery identity while awaiting a new response.
    if (refresh) {
      sourceContexts.delete(id);
      videoStore.updateDownload(id, { url: undefined, deliveryId: undefined, transport: undefined });
    }
    videoRetries.set(id, (forceRefresh = false) => enqueueItem(snapshot, id, forceRefresh));
    try {
      const { item, target } = await resolvedTarget(
        snapshot,
        controller.signal,
        refresh,
      );
      if (!ownsTask(id, controller)) return false;
      const prepared = preparedSnapshot(snapshot, item, target);
      videoRetries.set(id, (forceRefresh = false) => enqueueItem(prepared, id, forceRefresh));
      videoStore.updateDownload(id, { refreshAvailable: refreshable(prepared) });
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
      let resolvedExt =
        suffixes.length && !suffixes.includes(extension)
          ? suffixes[0]
          : extension;
      if (
        resolvedExt === "bin" &&
        target.kind === "file" &&
        item.type === "video"
      ) {
        resolvedExt =
          inferredVideoFileExtension(
            target.url,
            snapshot.platform,
            Boolean(item.tbvideo?.vid),
          ) || resolvedExt;
      }
      const filename = filenameFor(snapshot, resolvedExt, target.variant);
      videoStore.updateDownload(id, { filename });
      if (target.kind === "file") {
        const hints = item.tbvideo;
        const tbvideo = item.type === "video" && (snapshot.platform === "pipigx" || snapshot.platform === "zuiyou")
          && hints?.platform === snapshot.platform ? hints : undefined;
        const sourceId = target.source.id;
        sourceContexts.set(id, {
          tbvideo: tbvideo && { item: tbvideo, candidate: sourceId && Object.hasOwn(tbvideo.sources, sourceId) ? tbvideo.sources[sourceId] : "" },
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
    // These two video platforms only offer individually verified saves and the original player.
    // ZIP's fetchBlob path cannot verify their ranged candidates.
    if (snapshots.some(s => s.item.type === "video" && (s.platform === "pipigx" || s.platform === "zuiyou"))) return;
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
  async function downloadYouTubeSeparate(choice?: MediaSelection) {
    const item = selection.currentVideo.value;
    if (item) {
      await enqueueItem(snapshotItem(item, choice), undefined, false, {
        separateOnly: true,
      });
    }
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

  function handleRefreshDownload(id: number): Promise<void> {
    return handleRetryDownload(id, true);
  }
  async function handleRetryDownload(id: number, refresh = false): Promise<void> {
    const current = taskFor(id);
    if (
      !current ||
      current.observation ||
      pendingTaskActions.has(id) ||
      ["queued", "preparing", "downloading"].includes(current.status)
    )
      return;
    // Only an owned, settled file task has the frozen identity required for refresh.
    // Restored Worker history does not reconstruct that identity from a stale URL.
    if (refresh && (!current.refreshAvailable || !videoRetries.has(id) ||
        !["failed", "interrupted"].includes(current.status))) return;
    if (selectionInFlight(fileTaskSelections.get(id), id)) {
      selectionBusyNotice();
      return;
    }
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
      if (!refresh && retryArchive) {
        await retryArchive();
        return;
      }
      const retryVideo = videoRetries.get(id);
      if (retryVideo) {
        await retryVideo(refresh);
        return;
      }
      if (refresh) return;
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
      !["failed", "cancelled", "interrupted", "manual"].includes(task.status)
    )
      return;
    task.abortController?.abort();
    if (!triggerBrowserDownload(task.url, task.filename, true)) {
      videoStore.updateDownload(id, {
        status: "manual",
        statusText: "打开源链接",
        abortController: null,
        speed: 0,
        averageSpeed: 0,
        percent: 0,
        error: null,
        nativeAvailable: true,
      });
      showToast(
        "浏览器限制了新标签页打开，请允许弹窗后再次点击“打开源链接”",
        "warning",
        6000,
      );
      return;
    }
    videoStore.updateDownload(id, {
      status: "handed-off",
      statusText: "已交给浏览器",
      abortController: null,
      speed: 0,
      averageSpeed: 0,
      percent: 0,
      error: null,
    });
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
        fileTaskSelections.delete(task.id);
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
    downloadYouTubeSeparate,
    downloadMusic,
    downloadLiveVideo,
    downloadLiveCover,
    downloadImage,
    downloadAllImages,
    handleCancelDownload,
    handleRetryDownload,
    handleRefreshDownload,
    handleQueryDownload,
    handleBrowserDownload,
    handleClearCompleted,
  };
}
