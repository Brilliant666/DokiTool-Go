import { shallowRef, watch, type Ref } from "vue";
import { useButtonControl } from "./useButtonControl";
import { apiConfig } from "../services/apiConfig";
import { secureApiClient } from "../services/secureApiClient";
import { withTbvideoItems } from "../services/tbvideo";
import { validateMediaEnvelope, readyItem } from "../utils/mediaContract";
import type { useVideoStore } from "../stores/video";
import type { MediaItem, ToastType } from "../types/media";

async function fetchJSON(url: string, signal?: AbortSignal): Promise<unknown> {
  const controller = new AbortController();
  const abort = () => controller.abort(signal?.reason);
  if (signal?.aborted) abort();
  else signal?.addEventListener("abort", abort, { once: true });
  const timeout = setTimeout(
    () => controller.abort(new Error("解析请求超时，请重试")),
    55000,
  );
  try {
    const response = await fetch(url, { signal: controller.signal });
    const payload = await response.json().catch(() => {
      throw new Error(`接口返回无效数据（HTTP ${response.status}）`);
    });
    if (!response.ok)
      throw new Error(
        payload?.error?.message || `解析失败（HTTP ${response.status}）`,
      );
    return payload;
  } finally {
    clearTimeout(timeout);
    signal?.removeEventListener("abort", abort);
  }
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
}) {
  const { videoStore, showToast } = options;
  const inputUrl = shallowRef(""),
    parseError = shallowRef(""),
    currentPlatform = shallowRef("all");
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
  async function parseVideo() {
    if (active) return;
    const url = (
      inputUrl.value.match(/https?:\/\/[^\s<>]+/)?.[0] || inputUrl.value
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
      const payload =
        apiConfig.mode === "secure"
          ? await secureApiClient!.parse(
              {
                url,
                platform: currentPlatform.value,
                locale: options.locale.value === "en" ? "en" : "zh-CN",
                schema_version: "1.0",
              },
              { signal: controller.signal },
            )
          : await fetchJSON(
              `${apiConfig.parseUrl}?schema_version=1.0&platform=${encodeURIComponent(currentPlatform.value)}&url=${encodeURIComponent(url)}`,
              controller.signal,
            );
      const data = withTbvideoItems(validateMediaEnvelope(payload));
      if (run === generation && !controller.signal.aborted)
        videoStore.setResult(data);
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
    const data = withTbvideoItems(validateMediaEnvelope(
      await fetchJSON(resolveApiUrl(item.resolve_url), signal),
    ));
    const resolved = data.items.find((candidate) => candidate.id === item.id);
    if (!resolved) throw new Error("解析响应缺少所请求条目");
    return resolved;
  }
  return {
    inputUrl,
    parseError,
    currentPlatform,
    parseButton,
    parseVideo,
    resolveItem,
    selectPlatform: (platform: string) => {
      currentPlatform.value = platform;
    },
  };
}
