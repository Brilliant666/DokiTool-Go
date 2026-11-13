import { shallowRef, watch, type Ref } from "vue";
import { useButtonControl } from "./useButtonControl";
import { apiConfig } from "../services/apiConfig";
import { secureApiClient } from "../services/secureApiClient";
import { withTbvideoItems } from "../services/tbvideo";
import { validateMediaEnvelope, readyItem, readyVariant } from "../utils/mediaContract";
import {
  MIN_YOUTUBE_HELPER_VERSION,
  YOUTUBE_HELPER_SCRIPT_URL,
  extractYouTubeVideoId,
  pingYouTubeHelper,
  requestYouTubePlayerViaHelper,
  type YouTubeHelperContextResponse,
} from "../utils/youtubeHelperCore";
import type { useVideoStore } from "../stores/video";
import type {
  MediaItem,
  ToastType,
  UnifiedMedia,
  YouTubeHelperState,
} from "../types/media";

class HttpStatusError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

async function fetchJSON(
  url: string,
  signal?: AbortSignal,
  init?: RequestInit,
): Promise<unknown> {
  const controller = new AbortController();
  const abort = () => controller.abort(signal?.reason);
  if (signal?.aborted) abort();
  else signal?.addEventListener("abort", abort, { once: true });
  const timeout = setTimeout(
    () => controller.abort(new Error("解析请求超时，请重试")),
    55000,
  );
  try {
    const response = await fetch(url, { ...init, signal: controller.signal });
    const payload = await response.json().catch(() => {
      throw new HttpStatusError(
        `接口返回无效数据（HTTP ${response.status}）`,
        response.status,
      );
    });
    if (!response.ok)
      throw new HttpStatusError(
        (payload as any)?.error?.message || `解析失败（HTTP ${response.status}）`,
        response.status,
      );
    return payload;
  } finally {
    clearTimeout(timeout);
    signal?.removeEventListener("abort", abort);
  }
}

function isGooglevideoUrl(rawUrl: string): boolean {
  try {
    const host = new URL(rawUrl).hostname.toLowerCase();
    return host === "googlevideo.com" || host.endsWith(".googlevideo.com");
  } catch {
    return false;
  }
}

export function redactServerBoundYouTubeMedia(
  data: UnifiedMedia,
  helperState: YouTubeHelperState,
  videoId: string,
): UnifiedMedia {
  const safeBase = `https://www.youtube.com/watch?v=${encodeURIComponent(videoId)}`;
  let counter = 0;
  const nextPlaceholder = (tag: string) =>
    `${safeBase}&helper_required=${encodeURIComponent(tag)}_${++counter}`;

  const items: MediaItem[] = data.items.map((item) => {
    if (!readyItem(item)) return item;
    const variants = item.variants.map((variant) => {
      if (!readyVariant(variant) || variant.delivery.kind !== "file") {
        return variant;
      }
      const sources = variant.delivery.sources.map((source) => {
        const redactedUrl = isGooglevideoUrl(source.url)
          ? nextPlaceholder(source.id || variant.id)
          : source.url;
        const { fallback_urls: _drop, ...rest } = source;
        return {
          ...rest,
          url: redactedUrl,
        };
      });
      return {
        ...variant,
        delivery: {
          ...variant.delivery,
          sources,
        },
      };
    });

    let nextDefault = item.default;
    if (nextDefault) {
      const matchedVariant = variants.find(
        (v) => v.id === nextDefault!.variant_id,
      );
      if (
        readyVariant(matchedVariant) &&
        matchedVariant.delivery.kind === "file"
      ) {
        const matchedSource = nextDefault.source_id
          ? matchedVariant.delivery.sources.find(
              (s) => s.id === nextDefault!.source_id,
            )
          : matchedVariant.delivery.sources[0];
        if (matchedSource) {
          nextDefault = {
            ...nextDefault,
            url: matchedSource.url,
          };
        }
      }
    }

    return {
      ...item,
      variants,
      default: nextDefault,
    };
  });

  const attachments = data.attachments?.map((att) => {
    if (att.resource.delivery.kind !== "file") return att;
    const sources = att.resource.delivery.sources.map((source) => {
      const redactedUrl = isGooglevideoUrl(source.url)
        ? nextPlaceholder(source.id || att.id)
        : source.url;
      const { fallback_urls: _drop, ...rest } = source;
      return {
        ...rest,
        url: redactedUrl,
      };
    });
    return {
      ...att,
      resource: {
        ...att.resource,
        delivery: {
          ...att.resource.delivery,
          sources,
        },
      },
    };
  });

  return {
    ...data,
    items,
    ...(attachments ? { attachments } : {}),
    extra: {
      ...(data.extra || {}),
      youtube_helper: helperState,
    },
  };
}

function buildFallbackYouTubeMedia(
  videoId: string,
  helperState: YouTubeHelperState,
): UnifiedMedia {
  return {
    platform: "youtube",
    content_id: videoId,
    source_url: `https://www.youtube.com/watch?v=${videoId}`,
    title: `YouTube (${videoId})`,
    items: [
      {
        id: `video-${videoId}`,
        type: "video",
        state: "unavailable",
        default: null,
        default_unavailable_reason: "unavailable",
        error: {
          code:
            helperState.status === "login_required"
              ? "LOGIN_REQUIRED"
              : "HELPER_REQUIRED",
          message:
            helperState.message ||
            "需要安装并启用 DokiTool YouTube 助手脚本后方可解析下载",
        },
      },
    ],
    extra: {
      youtube_helper: helperState,
    },
  };
}

export const YT_HELPER_INSTALL_WAIT_KEY = "dokitool.yt_helper_install_wait";

interface PendingYouTubeHelperInstallState {
  waiting: boolean;
  url: string;
  platform: string;
  reloaded: boolean;
  updatedAt: number;
}

export function resolveApiUrl(raw: string): string {
  if (!raw.startsWith("/api/resolve?") || /[\\\s#]/.test(raw))
    throw new Error("条目解析地址无效");
  return (
    apiConfig.baseUrl +
    raw +
    (new URLSearchParams(raw.split("?")[1]).has("schema_version")
      ? ""
      : "&schema_version=1.0")
  );
}
export function useVideoParser(options: {
  videoStore: ReturnType<typeof useVideoStore>;
  locale: Ref<string>;
  showToast: (message: string, type?: ToastType, duration?: number) => unknown;
  sessionStorage?: Pick<Storage, "getItem" | "setItem" | "removeItem"> | null;
  reloadPage?: () => void;
  pingHelper?: typeof pingYouTubeHelper;
}) {
  const { videoStore, showToast } = options;
  const resolveSessionStorage = () => {
    if (options.sessionStorage !== undefined) return options.sessionStorage;
    try {
      return typeof window !== "undefined" ? window.sessionStorage : null;
    } catch {
      return null;
    }
  };
  const reloadPage =
    options.reloadPage ??
    (() => {
      if (typeof window !== "undefined" && window.location) {
        window.location.reload();
      }
    });
  const pingHelper = options.pingHelper ?? pingYouTubeHelper;

  const inputUrl = shallowRef(""),
    parseError = shallowRef(""),
    currentPlatform = shallowRef("all"),
    needsManualHelperRefresh = shallowRef(false);
  let checkingHelperInstall = false;

  function readHelperWaitState(): PendingYouTubeHelperInstallState | null {
    const store = resolveSessionStorage();
    if (!store) return null;
    try {
      const raw = store.getItem(YT_HELPER_INSTALL_WAIT_KEY);
      if (!raw) return null;
      const parsed = JSON.parse(raw);
      if (!parsed || typeof parsed !== "object" || parsed.waiting !== true) {
        return null;
      }
      return {
        waiting: true,
        url: typeof parsed.url === "string" ? parsed.url.trim() : "",
        platform:
          typeof parsed.platform === "string" && parsed.platform.trim()
            ? parsed.platform.trim()
            : "all",
        reloaded: parsed.reloaded === true,
        updatedAt:
          typeof parsed.updatedAt === "number" ? parsed.updatedAt : Date.now(),
      };
    } catch {
      return null;
    }
  }

  function writeHelperWaitState(state: PendingYouTubeHelperInstallState): void {
    const store = resolveSessionStorage();
    if (!store) return;
    try {
      store.setItem(YT_HELPER_INSTALL_WAIT_KEY, JSON.stringify(state));
    } catch {
      // Ignore storage quota / privacy mode errors
    }
  }

  function clearHelperWaitState(): void {
    const store = resolveSessionStorage();
    if (!store) return;
    try {
      store.removeItem(YT_HELPER_INSTALL_WAIT_KEY);
    } catch {
      // Ignore storage errors
    }
  }
  const parseButton = useButtonControl({
    action: "parse",
    timeout: 0,
    retry: false,
  });
  let generation = 0,
    active: AbortController | null = null;
  const invalidate = () => {
    generation++;
    active?.abort();
    active = null;
    parseButton.reset();
    parseError.value = "";
    videoStore.clearResult();
  };
  watch([inputUrl, currentPlatform], invalidate, { flush: "sync" });

  async function requestStandardParse(
    url: string,
    signal: AbortSignal,
  ): Promise<UnifiedMedia> {
    const payload =
      apiConfig.mode === "secure"
        ? await secureApiClient!.parse(
            {
              url,
              platform: currentPlatform.value,
              locale: options.locale.value === "en" ? "en" : "zh-CN",
              schema_version: "1.0",
            },
            { signal },
          )
        : await fetchJSON(
            `${apiConfig.parseUrl}?schema_version=1.0&platform=${encodeURIComponent(currentPlatform.value)}&url=${encodeURIComponent(url)}`,
            signal,
          );
    return withTbvideoItems(validateMediaEnvelope(payload));
  }

  async function parseYouTubeWithHelper(
    url: string,
    videoId: string,
    run: number,
    controller: AbortController,
  ): Promise<void> {
    const ping = await pingHelper();
    if (run !== generation || controller.signal.aborted) return;

    if (!ping.installed || !ping.supported) {
      const helperState: YouTubeHelperState = {
        status: ping.installed ? "outdated" : "missing",
        version: ping.version,
        min_version: MIN_YOUTUBE_HELPER_VERSION,
        script_url: YOUTUBE_HELPER_SCRIPT_URL,
        message: ping.installed
          ? `当前已安装的 YouTube 助手版本（v${ping.version}）低于最低要求（v${MIN_YOUTUBE_HELPER_VERSION}），请更新脚本后重试`
          : "当前未检测到 YouTube 下载助手脚本，请按说明安装并启用后重试",
      };
      let media: UnifiedMedia;
      try {
        const parsed = await requestStandardParse(url, controller.signal);
        media = redactServerBoundYouTubeMedia(parsed, helperState, videoId);
      } catch {
        if (run !== generation || controller.signal.aborted) return;
        media = buildFallbackYouTubeMedia(videoId, helperState);
      }
      if (run === generation && !controller.signal.aborted) {
        videoStore.setResult(media);
      }
      return;
    }

    clearHelperWaitState();
    needsManualHelperRefresh.value = false;

    let contextData: YouTubeHelperContextResponse;
    try {
      const contextEnvelope = (await fetchJSON(
        `${apiConfig.baseUrl}/api/youtube/context?schema_version=1.0`,
        controller.signal,
      )) as {
        schema_version?: string;
        code?: string;
        data?: YouTubeHelperContextResponse;
      };
      if (
        !contextEnvelope ||
        contextEnvelope.schema_version !== "1.0" ||
        contextEnvelope.code !== "OK" ||
        !contextEnvelope.data?.player_id
      ) {
        throw new Error("获取 YouTube 解析上下文失败");
      }
      contextData = contextEnvelope.data;
    } catch (error) {
      if (error instanceof HttpStatusError && error.status === 404) {
        const data = await requestStandardParse(url, controller.signal);
        if (run === generation && !controller.signal.aborted) {
          videoStore.setResult(data);
        }
        return;
      }
      throw error;
    }

    if (run !== generation || controller.signal.aborted) return;

    const playerResult = await requestYouTubePlayerViaHelper(
      {
        video_id: videoId,
        signature_timestamp: contextData.signature_timestamp,
        client: contextData.client,
      },
      { signal: controller.signal },
    );

    if (run !== generation || controller.signal.aborted) return;

    const playStatus = playerResult?.playabilityStatus?.status || "ERROR";
    if (playStatus === "LOGIN_REQUIRED") {
      const loginMsg = "当前网络出口被 YouTube 要求验证，请更换节点后重试";
      const helperState: YouTubeHelperState = {
        status: "login_required",
        version: ping.version,
        min_version: MIN_YOUTUBE_HELPER_VERSION,
        script_url: YOUTUBE_HELPER_SCRIPT_URL,
        message: loginMsg,
      };
      parseError.value = loginMsg;
      videoStore.setResult(buildFallbackYouTubeMedia(videoId, helperState));
      showToast(loginMsg, "error", 5000);
      return;
    }
    if (playStatus !== "OK") {
      throw new Error(
        playerResult?.playabilityStatus?.reason ||
          `YouTube 返回不可播放状态（${playStatus}）`,
      );
    }

    const vd = playerResult.videoDetails;
    const finalizeBody: Record<string, unknown> = {
      video_id: videoId,
      player_id: contextData.player_id,
      formats: playerResult.streamingData?.formats || [],
      adaptiveFormats: playerResult.streamingData?.adaptiveFormats || [],
    };
    if (typeof vd?.title === "string" && vd.title.trim()) {
      finalizeBody.title = vd.title.trim();
    }
    if (typeof vd?.author === "string" && vd.author.trim()) {
      finalizeBody.author = vd.author.trim();
    }
    if (
      typeof vd?.lengthSeconds === "number" &&
      Number.isInteger(vd.lengthSeconds) &&
      vd.lengthSeconds > 0
    ) {
      finalizeBody.duration_seconds = vd.lengthSeconds;
    }
    if (typeof vd?.thumbnail === "string" && vd.thumbnail.trim()) {
      finalizeBody.thumbnail = vd.thumbnail.trim();
    }

    const finalizePayload = await fetchJSON(
      `${apiConfig.baseUrl}/api/youtube/finalize?schema_version=1.0`,
      controller.signal,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(finalizeBody),
      },
    );

    const finalized = withTbvideoItems(validateMediaEnvelope(finalizePayload));
    const readyState: YouTubeHelperState = {
      status: "ready",
      version: ping.version,
      min_version: MIN_YOUTUBE_HELPER_VERSION,
      script_url: YOUTUBE_HELPER_SCRIPT_URL,
    };
    const data: UnifiedMedia = {
      ...finalized,
      extra: {
        ...(finalized.extra || {}),
        youtube_helper: readyState,
      },
    };
    if (run === generation && !controller.signal.aborted) {
      videoStore.setResult(data);
    }
  }

  async function parseVideo() {
    if (active) return;
    const url = (
      inputUrl.value
        .match(
          /https?:\/\/[^\s<>"'\p{Script=Han}\u2014\u2018-\u201D\u2026\u203B\u3000-\u303F\uFE10-\uFE1F\uFE30-\uFE6F\uFF00-\uFFEF]+/iu,
        )?.[0]
        .replace(/[.,;!?]+$/, "") || inputUrl.value
    ).trim();
    try {
      if (!["http:", "https:"].includes(new URL(url).protocol))
        throw new Error();
    } catch {
      invalidate();
      parseError.value = "请输入有效分享链接";
      return;
    }
    if (url !== inputUrl.value) inputUrl.value = url;
    const run = ++generation,
      controller = new AbortController();
    active = controller;
    videoStore.clearResult();
    parseError.value = "";
    parseButton.setLoading(true);
    try {
      const youtubeVideoId =
        currentPlatform.value === "all" || currentPlatform.value === "youtube"
          ? extractYouTubeVideoId(url)
          : "";
      if (youtubeVideoId) {
        await parseYouTubeWithHelper(url, youtubeVideoId, run, controller);
      } else {
        const data = await requestStandardParse(url, controller.signal);
        if (run === generation && !controller.signal.aborted)
          videoStore.setResult(data);
      }
    } catch (error) {
      if (run !== generation || controller.signal.aborted) return;
      parseError.value = error instanceof Error ? error.message : "解析失败";
      showToast(parseError.value, "error");
    } finally {
      if (run === generation) {
        active = null;
        parseButton.reset();
      }
    }
  }
  async function resolveItem(
    item: MediaItem,
    signal?: AbortSignal,
  ): Promise<MediaItem> {
    if (item.state === "unavailable") throw new Error(item.error.message);
    if (!item.resolve_url) {
      if (readyItem(item)) return item;
      throw new Error("条目缺少解析入口");
    }
    const data = withTbvideoItems(
      validateMediaEnvelope(
        await fetchJSON(resolveApiUrl(item.resolve_url), signal),
      ),
    );
    const resolved = data.items.find((candidate) => candidate.id === item.id);
    if (!resolved) throw new Error("解析响应缺少所请求条目");
    return resolved;
  }
  function markWaitingForYouTubeHelperInstall(): void {
    const candidateUrl =
      inputUrl.value.trim() || videoStore.resultData?.source_url || "";
    needsManualHelperRefresh.value = false;
    writeHelperWaitState({
      waiting: true,
      url: candidateUrl,
      platform: currentPlatform.value || "all",
      reloaded: false,
      updatedAt: Date.now(),
    });
  }

  async function handleYouTubeHelperTabReturn(): Promise<boolean> {
    if (checkingHelperInstall) return false;
    const state = readHelperWaitState();
    if (!state?.waiting) return false;

    checkingHelperInstall = true;
    try {
      const ping = await pingHelper();
      if (ping.installed && ping.supported) {
        clearHelperWaitState();
        needsManualHelperRefresh.value = false;
        if (state.platform && state.platform !== currentPlatform.value) {
          currentPlatform.value = state.platform;
        }
        if (state.url && state.url !== inputUrl.value) {
          inputUrl.value = state.url;
        }
        if (inputUrl.value.trim()) {
          await parseVideo();
        }
        return true;
      }

      if (!state.reloaded) {
        writeHelperWaitState({
          ...state,
          reloaded: true,
          updatedAt: Date.now(),
        });
        reloadPage();
        return false;
      }

      needsManualHelperRefresh.value = true;
      return false;
    } finally {
      checkingHelperInstall = false;
    }
  }

  async function resumePendingYouTubeHelperInstall(): Promise<boolean> {
    if (checkingHelperInstall) return false;
    const state = readHelperWaitState();
    if (!state?.waiting) return false;

    checkingHelperInstall = true;
    try {
      if (state.platform && state.platform !== currentPlatform.value) {
        currentPlatform.value = state.platform;
      }
      if (state.url && state.url !== inputUrl.value) {
        inputUrl.value = state.url;
      }

      const ping = await pingHelper();
      if (ping.installed && ping.supported) {
        clearHelperWaitState();
        needsManualHelperRefresh.value = false;
        if (inputUrl.value.trim()) {
          await parseVideo();
        }
        return true;
      }

      writeHelperWaitState({
        ...state,
        reloaded: true,
        updatedAt: Date.now(),
      });
      needsManualHelperRefresh.value = true;
      if (inputUrl.value.trim()) {
        await parseVideo();
      }
      return false;
    } finally {
      checkingHelperInstall = false;
    }
  }

  function triggerManualHelperRefresh(): void {
    const candidateUrl =
      inputUrl.value.trim() ||
      readHelperWaitState()?.url ||
      videoStore.resultData?.source_url ||
      "";
    writeHelperWaitState({
      waiting: true,
      url: candidateUrl,
      platform: currentPlatform.value || "all",
      reloaded: true,
      updatedAt: Date.now(),
    });
    reloadPage();
  }

  return {
    inputUrl,
    parseError,
    currentPlatform,
    needsManualHelperRefresh,
    parseButton,
    parseVideo,
    resolveItem,
    markWaitingForYouTubeHelperInstall,
    handleYouTubeHelperTabReturn,
    resumePendingYouTubeHelperInstall,
    triggerManualHelperRefresh,
    selectPlatform: (platform: string) => {
      currentPlatform.value = platform;
    },
  };
}
