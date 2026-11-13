import { shallowRef, watch, type Ref } from "vue";
import { useButtonControl } from "./useButtonControl";
import { apiConfig } from "../services/apiConfig";
import { secureApiClient } from "../services/secureApiClient";
import { withTbvideoItems } from "../services/tbvideo";
import { validateMediaEnvelope, readyItem } from "../utils/mediaContract";
import {
  extractYouTubeVideoId,
  pingYouTubeHelper,
} from "../utils/youtubeHelperCore";
import type { useVideoStore } from "../stores/video";
import type {
  MediaItem,
  ToastType,
  UnifiedMedia,
} from "../types/media";

export class HttpStatusError extends Error {
  status: number;
  retry_after_seconds?: number;
  rateLimitRetried?: boolean;
  code?: string;
  constructor(
    message: string,
    status: number,
    retryAfterSeconds?: number,
    code?: string,
  ) {
    super(message);
    this.status = status;
    if (retryAfterSeconds! > 0 && Number.isFinite(retryAfterSeconds)) {
      this.retry_after_seconds = Math.ceil(retryAfterSeconds!);
    }
    this.code = code;
  }
}

function parseRetryAfterSeconds(
  response: Response,
  payload?: unknown,
): number | undefined {
  if (response.status !== 429) return undefined;
  for (const v of [
    (payload as any)?.error?.retry_after_seconds,
    response.headers.get("Retry-After"),
  ]) {
    const n = Number(v);
    if (n > 0 && Number.isFinite(n)) return Math.ceil(n);
  }
  return undefined;
}

export async function withRateLimitRetry<T>(
  operation: () => Promise<T>,
  signal?: AbortSignal,
  onRateLimitWait?: (seconds: number) => void,
): Promise<T> {
  const abortErr = () =>
    signal?.reason ?? new DOMException("Aborted", "AbortError");
  for (let attempt = 0; ; attempt++) {
    if (signal?.aborted) throw abortErr();
    try {
      return await operation();
    } catch (error: any) {
      if (error?.rateLimitRetried) throw error;
      const wait = error?.retry_after_seconds;
      if (error?.status !== 429 || !(wait > 0 && wait <= 15) || attempt >= 2) {
        if (error?.status === 429 && !Object.isFrozen(error)) {
          error.rateLimitRetried = true;
        }
        throw error;
      }
      for (let sec = Math.ceil(wait); sec > 0; sec--) {
        if (signal?.aborted) throw abortErr();
        onRateLimitWait?.(sec);
        await new Promise<void>((res, rej) => {
          const stop = () => {
            clearTimeout(timer);
            rej(abortErr());
          };
          const timer = setTimeout(() => {
            signal?.removeEventListener("abort", stop);
            res();
          }, 1000);
          signal?.addEventListener("abort", stop, { once: true });
          if (signal?.aborted) stop();
        });
      }
    }
  }
}

export async function fetchJSON(
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
        parseRetryAfterSeconds(response),
      );
    });
    if (!response.ok)
      throw new HttpStatusError(
        (payload as any)?.error?.message || `解析失败（HTTP ${response.status}）`,
        response.status,
        parseRetryAfterSeconds(response, payload),
        (payload as any)?.error?.code || (payload as any)?.code,
      );
    return payload;
  } finally {
    clearTimeout(timeout);
    signal?.removeEventListener("abort", abort);
  }
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
    const { runYouTubeHelperParse } = await import("../utils/parseHelpers");
    if (run !== generation || controller.signal.aborted) return;
    await runYouTubeHelperParse({
      videoId,
      locale: options.locale.value,
      signal: controller.signal,
      isCurrent: () => run === generation && !controller.signal.aborted,
      pingHelper,
      requestStandardParse: () => requestStandardParse(url, controller.signal),
      fetchJSON,
      baseUrl: apiConfig.baseUrl,
      onHelperReady: () => {
        clearHelperWaitState();
        needsManualHelperRefresh.value = false;
      },
      setResult: (media) => videoStore.setResult(media),
      setError: (msg, duration) => {
        parseError.value = msg;
        showToast(msg, "error", duration);
      },
    });
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
    const isEn = options.locale.value === "en";
    const mismatchMsg = isEn
      ? 'Link does not match the selected platform. Please switch back to "All".'
      : "链接与所选平台不匹配，请切回「所有平台」再试";
    const run = ++generation,
      controller = new AbortController();
    active = controller;
    videoStore.clearResult();
    parseError.value = "";
    parseButton.setLoading(true);
    try {
      if (currentPlatform.value !== "all") {
        const { detectUrlPlatform } = await import("../utils/parseHelpers");
        if (run !== generation || controller.signal.aborted) return;
        const detected = detectUrlPlatform(url);
        if (detected && detected !== currentPlatform.value) {
          invalidate();
          parseError.value = mismatchMsg;
          showToast(mismatchMsg, "error");
          return;
        }
      }
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
      const rawMsg = error instanceof Error ? error.message : "解析失败";
      const isMismatch =
        currentPlatform.value !== "all" &&
        ((error instanceof HttpStatusError &&
          error.code === "PLATFORM_MISMATCH") ||
          /对应平台|平台不匹配|platform mismatch/i.test(rawMsg));
      parseError.value =
        isMismatch && (isEn || !rawMsg.includes("所有平台"))
          ? mismatchMsg
          : rawMsg;
      showToast(parseError.value, "error");
    } finally {
      if (run === generation) {
        active = null;
        parseButton.reset();
      }
    }
  }
  function cancelParse(): void {
    if (active) invalidate();
  }
  async function resolveItem(
    item: MediaItem,
    signal?: AbortSignal,
    options?: { onRateLimitWait?: (seconds: number) => void },
  ): Promise<MediaItem> {
    if (item.state === "unavailable") throw new Error(item.error.message);
    if (!item.resolve_url) {
      if (readyItem(item)) return item;
      throw new Error("条目缺少解析入口");
    }
    const url = resolveApiUrl(item.resolve_url);
    const data = withTbvideoItems(
      validateMediaEnvelope(
        await withRateLimitRetry(
          () => fetchJSON(url, signal),
          signal,
          options?.onRateLimitWait,
        ),
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
    cancelParse,
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
