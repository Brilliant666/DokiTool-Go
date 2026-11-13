import { supportedPlatforms } from "./platforms";
import { withTbvideoItems } from "../services/tbvideo";
import {
  validateMediaEnvelope,
  readyItem,
  readyVariant,
} from "./mediaContract";
import {
  MIN_YOUTUBE_HELPER_VERSION,
  YOUTUBE_HELPER_SCRIPT_URL,
  requestYouTubePlayerViaHelper,
  type pingYouTubeHelper,
  type YouTubeHelperContextResponse,
} from "./youtubeHelperCore";
import type {
  MediaItem,
  UnifiedMedia,
  YouTubeHelperState,
} from "../types/media";

const ALIAS_PAIRS =
  "douyin:iesdouyin.com bilibili:b23.tv xiaohongshu:xhslink.com,rednote.com toutiao:toutiaoimg.com weibo:weibo.cn pipigx:ippzone.com x:twitter.com youtube:youtu.be facebook:fb.watch dailymotion:dai.ly";

export function detectUrlPlatform(rawUrl: string): string | null {
  try {
    const host = new URL(rawUrl).hostname.toLowerCase();
    const matchHost = (d: string) => host === d || host.endsWith(`.${d}`);
    for (const entry of ALIAS_PAIRS.split(" ")) {
      const [id, list] = entry.split(":");
      if (list.split(",").some(matchHost)) return id;
    }
    for (const p of supportedPlatforms.value) {
      const d = new URL(p.site_url)
        .hostname.replace(/^(?:www|h5)\./i, "")
        .toLowerCase();
      if (d && matchHost(d)) return p.id;
    }
  } catch {}
  return null;
}

export function isMobileBrowser(): boolean {
  if (typeof navigator === "undefined") return false;
  if ((navigator as any).userAgentData?.mobile === true) return true;
  const ua = navigator.userAgent || "";
  return (
    /Android|iPhone|iPad|iPod|Mobile|HarmonyOS/i.test(ua) ||
    (/Macintosh/i.test(ua) && Number(navigator.maxTouchPoints) > 1)
  );
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

export function buildFallbackYouTubeMedia(
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

export async function runYouTubeHelperParse(ctx: {
  videoId: string;
  locale: string;
  signal: AbortSignal;
  isCurrent: () => boolean;
  pingHelper: typeof pingYouTubeHelper;
  requestStandardParse: () => Promise<UnifiedMedia>;
  fetchJSON: (
    url: string,
    signal?: AbortSignal,
    init?: RequestInit,
  ) => Promise<unknown>;
  baseUrl: string;
  onHelperReady: () => void;
  setResult: (media: UnifiedMedia) => void;
  setError: (message: string, duration?: number) => void;
}): Promise<void> {
  const { videoId, signal, isCurrent } = ctx;
  const ping = await ctx.pingHelper();
  if (!isCurrent()) return;

  if (!ping.installed || !ping.supported) {
    if (!ping.installed && isMobileBrowser()) {
      ctx.setError(
        ctx.locale === "en"
          ? "YouTube requires a browser with a userscript manager. Using a computer is recommended on mobile."
          : "YouTube 需要在装有用户脚本管理器的浏览器里使用，手机上建议改用电脑",
      );
      return;
    }
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
      const parsed = await ctx.requestStandardParse();
      media = redactServerBoundYouTubeMedia(parsed, helperState, videoId);
    } catch {
      if (!isCurrent()) return;
      media = buildFallbackYouTubeMedia(videoId, helperState);
    }
    if (isCurrent()) {
      ctx.setResult(media);
    }
    return;
  }

  ctx.onHelperReady();

  let contextData: YouTubeHelperContextResponse;
  try {
    const contextEnvelope = (await ctx.fetchJSON(
      `${ctx.baseUrl}/api/youtube/context?schema_version=1.0`,
      signal,
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
  } catch (error: any) {
    if (error?.status === 404) {
      const data = await ctx.requestStandardParse();
      if (isCurrent()) {
        ctx.setResult(data);
      }
      return;
    }
    throw error;
  }

  if (!isCurrent()) return;

  const playerResult = await requestYouTubePlayerViaHelper(
    {
      video_id: videoId,
      signature_timestamp: contextData.signature_timestamp,
      client: contextData.client,
    },
    { signal },
  );

  if (!isCurrent()) return;

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
    ctx.setResult(buildFallbackYouTubeMedia(videoId, helperState));
    ctx.setError(loginMsg, 5000);
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

  const finalizePayload = await ctx.fetchJSON(
    `${ctx.baseUrl}/api/youtube/finalize?schema_version=1.0`,
    signal,
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
  if (isCurrent()) {
    ctx.setResult(data);
  }
}
