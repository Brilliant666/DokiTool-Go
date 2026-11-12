// Standalone core module for the DokiTool YouTube userscript and page bridge.
// Designed so that the exact same validation and request logic runs in the
// userscript (Tampermonkey / Violentmonkey), the Vue frontend, and Node unit tests,
// and can be packaged into a browser extension in the future.

export const YOUTUBE_HELPER_VERSION = "1.3.0";
export const MIN_YOUTUBE_HELPER_VERSION = "1.3.0";
export const YOUTUBE_HELPER_SCRIPT_URL = "/helper/dokitool-youtube.user.js";
export const YOUTUBE_OPFS_TEMP_PREFIX = "dokitool-yt-tmp-";
export const YOUTUBE_FILE_PICKER_ID = "dokitool-youtube";
export const DEFAULT_OPFS_CLEANUP_DELAY_MS = 15000;
export const OPFS_QUOTA_HEADROOM_FACTOR = 1.2;

export const DEFAULT_MUX_CHUNK_BYTES = 10 * 1024 * 1024;
export const DEFAULT_MUX_VIDEO_CONCURRENCY = 3;
export const DEFAULT_MUX_AUDIO_CONCURRENCY = 2;
export const DEFAULT_MUX_MAX_RETRIES = 3;
export const MAX_IN_MEMORY_MUX_BYTES = 1024 * 1024 * 1024;

export const YOUTUBE_HELPER_REQUEST_EVENT = "dokitool:youtube-helper:request";
export const YOUTUBE_HELPER_RESPONSE_EVENT = "dokitool:youtube-helper:response";
export const YOUTUBE_HELPER_PROGRESS_EVENT = "dokitool:youtube-helper:progress";

export const YOUTUBE_SW_DATA_URL = "https://www.youtube.com/sw.js_data";
export const YOUTUBE_PLAYER_API_URL =
  "https://www.youtube.com/youtubei/v1/player?prettyPrint=false";
export const VISITOR_DATA_TTL_MS = 30 * 60 * 1000;
export const MAX_HELPER_TITLE_CHARS = 200;

const VIDEO_ID_RE = /^[A-Za-z0-9_-]{11}$/;
const TVHTML5_VERSION_RE = /^(?:7\.[0-9]{8}\.[0-9]{2}\.[0-9]{2}|5\.[0-9]{8})$/;
const GOOGLEVIDEO_HOST_RE = /^[a-z0-9-]+(?:\.[a-z0-9-]+)*\.googlevideo\.com$/;
const YTIMG_HOST_RE = /^(?:i|img|[a-z0-9-]+)\.ytimg\.com$/;
const SEMVER_RE = /^([0-9]+)\.([0-9]+)\.([0-9]+)$/;
const VISITOR_DATA_TOKEN_RE = /Cg[A-Za-z0-9_-]{8,1024}(?:%3D|=)*/;
const VISITOR_DATA_EXACT_RE = /^Cg[A-Za-z0-9_-]{8,1024}(?:%3D|=)*$/;

const ALLOWED_FORMAT_KEYS = [
  "itag",
  "url",
  "signatureCipher",
  "mimeType",
  "width",
  "height",
  "fps",
  "bitrate",
  "contentLength",
  "audioQuality",
  "audioSampleRate",
  "audioChannels",
];

export function isValidYouTubeVideoId(videoId) {
  return typeof videoId === "string" && VIDEO_ID_RE.test(videoId);
}

export function extractYouTubeVideoId(rawUrl) {
  if (typeof rawUrl !== "string" || !rawUrl.trim() || rawUrl.length > 2048) {
    return "";
  }
  let parsed;
  try {
    parsed = new URL(rawUrl.trim());
  } catch {
    return "";
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return "";
  }
  if (parsed.username || parsed.password) {
    return "";
  }
  const host = parsed.hostname.toLowerCase();
  const segments = parsed.pathname.split("/").filter(Boolean);
  let candidate = "";
  if (host === "youtu.be" || host === "www.youtu.be") {
    if (segments.length === 1) candidate = segments[0];
  } else if (
    host === "youtube.com" ||
    host === "www.youtube.com" ||
    host === "m.youtube.com" ||
    host === "music.youtube.com"
  ) {
    if (segments.length === 1 && segments[0].toLowerCase() === "watch") {
      candidate = parsed.searchParams.get("v") || "";
    } else if (
      segments.length === 2 &&
      ["shorts", "embed", "live", "v"].includes(segments[0].toLowerCase())
    ) {
      candidate = segments[1];
    }
  }
  return isValidYouTubeVideoId(candidate) ? candidate : "";
}

export function isValidSignatureTimestamp(sts) {
  return (
    typeof sts === "number" &&
    Number.isInteger(sts) &&
    sts >= 10000 &&
    sts <= 999999
  );
}

export function isValidTVHTML5Client(client) {
  if (!client || typeof client !== "object") return false;
  return (
    client.name === "TVHTML5" &&
    typeof client.version === "string" &&
    TVHTML5_VERSION_RE.test(client.version)
  );
}

export function isValidGooglevideoPlaybackUrl(rawUrl) {
  if (
    typeof rawUrl !== "string" ||
    !rawUrl.trim() ||
    rawUrl.length > 8192 ||
    rawUrl.includes("#")
  ) {
    return false;
  }
  for (let i = 0; i < rawUrl.length; i++) {
    const code = rawUrl.charCodeAt(i);
    if (code <= 0x20 || code >= 0x7f || code === 0x5c) {
      return false;
    }
  }
  let parsed;
  try {
    parsed = new URL(rawUrl.trim());
  } catch {
    return false;
  }
  if (parsed.protocol !== "https:") return false;
  if (parsed.username || parsed.password) return false;
  if (parsed.port && parsed.port !== "443") return false;
  const host = parsed.hostname.toLowerCase().replace(/\.$/, "");
  if (!GOOGLEVIDEO_HOST_RE.test(host)) return false;
  return parsed.pathname === "/videoplayback";
}

export function sanitizeHelperFilename(rawName) {
  if (typeof rawName !== "string") {
    return { ok: false, filename: "", error: "文件名无效" };
  }
  const trimmed = rawName.trim();
  if (!trimmed || trimmed.length > 512) {
    return { ok: false, filename: "", error: "文件名长度无效" };
  }
  const lower = trimmed.toLowerCase();
  let ext = "";
  if (lower.endsWith(".mp4")) {
    ext = ".mp4";
  } else if (lower.endsWith(".m4a")) {
    ext = ".m4a";
  } else if (lower.endsWith(".webm")) {
    ext = ".webm";
  } else {
    return {
      ok: false,
      filename: "",
      error: "仅允许保存 .mp4、.m4a 或 .webm 后缀的文件",
    };
  }
  const stemRaw = trimmed.slice(0, trimmed.length - ext.length);
  const cleanedStem = stemRaw
    .replace(/[\x00-\x1f\x7f/\\:*?"<>|]+/g, "_")
    .replace(/\s+/g, "_")
    .replace(/_+/g, "_")
    .replace(/^[._]+|[._]+$/g, "")
    .slice(0, 120);
  const safeStem = cleanedStem || "youtube_media";
  return { ok: true, filename: `${safeStem}${ext}` };
}

export function extractUrlClen(rawUrl) {
  if (typeof rawUrl !== "string" || !rawUrl.trim()) return 0;
  try {
    const u = new URL(rawUrl.trim());
    const raw = u.searchParams.get("clen");
    if (!raw || !/^[0-9]+$/.test(raw.trim())) return 0;
    const n = Number(raw.trim());
    return Number.isSafeInteger(n) && n > 0 ? n : 0;
  } catch {
    return 0;
  }
}

function pickUsableVariantSource(variant) {
  const sources = variant?.delivery?.sources;
  if (!Array.isArray(sources) || sources.length === 0) return null;
  return (
    sources.find(
      (s) =>
        s &&
        s.availability !== "unavailable" &&
        typeof s.url === "string" &&
        s.url.trim(),
    ) ||
    sources.find((s) => s && typeof s.url === "string" && s.url.trim()) ||
    null
  );
}

export function selectYouTubeMuxPair(
  videoVariant,
  allVariants,
  attachments = [],
) {
  if (
    !videoVariant ||
    typeof videoVariant !== "object" ||
    videoVariant.state === "deferred" ||
    videoVariant.delivery?.kind !== "file"
  ) {
    return { ok: false, reason: "当前画质不支持浏览器内合流" };
  }
  const vMeta = videoVariant.metadata || {};
  if (vMeta.has_audio !== false) {
    return { ok: false, reason: "当前画质已包含音轨，无需合流" };
  }
  const videoSource = pickUsableVariantSource(videoVariant);
  if (!videoSource || !isValidGooglevideoPlaybackUrl(videoSource.url)) {
    return { ok: false, reason: "视频轨地址无效或不可用" };
  }

  const vcodec = String(vMeta.video_codec || "")
    .trim()
    .toLowerCase();
  const vContainer = String(vMeta.container || vMeta.extension || "")
    .trim()
    .toLowerCase();

  const isH264OrAV1 =
    vcodec === "h264" ||
    vcodec.startsWith("avc1") ||
    vcodec === "av1" ||
    vcodec.startsWith("av01");
  const isVP9 =
    vcodec === "vp9" || vcodec.startsWith("vp9.") || vcodec.startsWith("vp09");

  const isMp4Video = isH264OrAV1 && (!vContainer || vContainer === "mp4");
  const isWebmVideo = isVP9 && (!vContainer || vContainer === "webm");

  if (!isMp4Video && !isWebmVideo) {
    return {
      ok: false,
      reason: `不支持的视频编码或容器组合（${vcodec || "未知"} / ${vContainer || "未知"}）`,
    };
  }

  const candidates = [
    ...(Array.isArray(allVariants) ? allVariants : []),
    ...(Array.isArray(attachments)
      ? attachments
          .filter(
            (att) =>
              att &&
              typeof att === "object" &&
              att.resource?.role === "audio" &&
              att.resource?.delivery?.kind === "file",
          )
          .map((att) => ({
            id: att.id || `att-${att.resource.id || "audio"}`,
            state: "ready",
            label: att.title || "原声轨",
            metadata: att.resource.metadata,
            delivery: att.resource.delivery,
          }))
      : []),
  ];
  for (const cand of candidates) {
    if (
      !cand ||
      typeof cand !== "object" ||
      cand.state === "deferred" ||
      cand.id === videoVariant.id ||
      cand.delivery?.kind !== "file"
    ) {
      continue;
    }
    const aMeta = cand.metadata || {};
    const aMime = String(aMeta.mime_type || "")
      .trim()
      .toLowerCase();
    const aExt = String(aMeta.extension || "")
      .trim()
      .toLowerCase();
    const aContainer = String(aMeta.container || "")
      .trim()
      .toLowerCase();
    const acodec = String(aMeta.audio_codec || "")
      .trim()
      .toLowerCase();

    const isStandaloneAudio =
      aMeta.has_audio === true &&
      !aMeta.video_codec &&
      (aMime.startsWith("audio/") || aExt === "m4a" || aExt === "webm");
    if (!isStandaloneAudio) continue;

    if (isMp4Video) {
      const aacMatch =
        (acodec === "aac" || acodec.startsWith("mp4a")) &&
        (aContainer === "mp4" || aContainer === "m4a" || aExt === "m4a");
      if (!aacMatch) continue;
      const audioSource = pickUsableVariantSource(cand);
      if (!audioSource || !isValidGooglevideoPlaybackUrl(audioSource.url)) {
        continue;
      }
      return {
        ok: true,
        outputContainer: "mp4",
        outputExtension: "mp4",
        outputMimeType: "video/mp4",
        videoVariant,
        audioVariant: cand,
        videoSource,
        audioSource,
      };
    }

    if (isWebmVideo) {
      const opusMatch =
        (acodec === "opus" || acodec.startsWith("opus")) &&
        (aContainer === "webm" || aExt === "webm");
      if (!opusMatch) continue;
      const audioSource = pickUsableVariantSource(cand);
      if (!audioSource || !isValidGooglevideoPlaybackUrl(audioSource.url)) {
        continue;
      }
      return {
        ok: true,
        outputContainer: "webm",
        outputExtension: "webm",
        outputMimeType: "video/webm",
        videoVariant,
        audioVariant: cand,
        videoSource,
        audioSource,
      };
    }
  }

  return {
    ok: false,
    reason: isMp4Video
      ? "未找到同容器 AAC (M4A) 音轨，不做跨容器合流"
      : "未找到同容器 Opus (WebM) 音轨，不做跨容器合流",
  };
}

export function compareSemver(a, b) {
  if (typeof a !== "string" || typeof b !== "string") return NaN;
  const ma = SEMVER_RE.exec(a.trim());
  const mb = SEMVER_RE.exec(b.trim());
  if (!ma || !mb) return NaN;
  for (let i = 1; i <= 3; i++) {
    const na = Number(ma[i]);
    const nb = Number(mb[i]);
    if (na > nb) return 1;
    if (na < nb) return -1;
  }
  return 0;
}

export function isHelperVersionSupported(
  version,
  minVersion = MIN_YOUTUBE_HELPER_VERSION,
) {
  const cmp = compareSemver(version, minVersion);
  return !Number.isNaN(cmp) && cmp >= 0;
}

export function isValidVisitorDataToken(token) {
  return typeof token === "string" && VISITOR_DATA_EXACT_RE.test(token.trim());
}

export function extractVisitorDataFromSwResponse(rawText) {
  if (typeof rawText !== "string" || !rawText) return "";
  const stripped = rawText.replace(/^\)\]\}'\s*/, "");
  try {
    const parsed = JSON.parse(stripped);
    const candidate =
      parsed?.[0]?.[2]?.[6] || parsed?.[0]?.[2]?.[0]?.[0]?.[13] || "";
    if (typeof candidate === "string" && isValidVisitorDataToken(candidate)) {
      return candidate.trim();
    }
  } catch {
    // Fall back to regex extraction below
  }
  const match = VISITOR_DATA_TOKEN_RE.exec(rawText);
  return match && isValidVisitorDataToken(match[0]) ? match[0] : "";
}

export function sanitizeHelperText(raw, maxChars = MAX_HELPER_TITLE_CHARS) {
  if (typeof raw !== "string") return "";
  const stripped = raw.replace(/[\u0000-\u001f\u007f-\u009f]/g, "").trim();
  if (!stripped || maxChars <= 0) return stripped;
  const chars = Array.from(stripped);
  if (chars.length > maxChars) {
    return chars.slice(0, maxChars).join("").trim();
  }
  return stripped;
}

export function isValidYouTubeThumbnailUrl(rawUrl) {
  if (
    typeof rawUrl !== "string" ||
    !rawUrl.trim() ||
    rawUrl.length > 2048 ||
    rawUrl.includes("#")
  ) {
    return false;
  }
  for (let i = 0; i < rawUrl.length; i++) {
    const code = rawUrl.charCodeAt(i);
    if (code <= 0x20 || code >= 0x7f || code === 0x5c) {
      return false;
    }
  }
  let parsed;
  try {
    parsed = new URL(rawUrl.trim());
  } catch {
    return false;
  }
  if (parsed.protocol !== "https:") return false;
  if (parsed.username || parsed.password) return false;
  if (parsed.port && parsed.port !== "443") return false;
  const host = parsed.hostname.toLowerCase().replace(/\.$/, "");
  return YTIMG_HOST_RE.test(host) || host === "img.youtube.com";
}

export function sanitizeVideoDetails(rawDetails) {
  if (!rawDetails || typeof rawDetails !== "object") return null;
  const out = {};
  const title = sanitizeHelperText(rawDetails.title, MAX_HELPER_TITLE_CHARS);
  if (title) {
    out.title = title;
  }
  const author = sanitizeHelperText(rawDetails.author, MAX_HELPER_TITLE_CHARS);
  if (author) {
    out.author = author;
  }
  const rawLen = rawDetails.lengthSeconds;
  if (typeof rawLen === "number" || typeof rawLen === "string") {
    const parsedLen = Number.parseInt(String(rawLen).trim(), 10);
    if (
      Number.isInteger(parsedLen) &&
      parsedLen > 0 &&
      parsedLen <= 86400 * 365
    ) {
      out.lengthSeconds = parsedLen;
    }
  }
  const thumbs = rawDetails.thumbnail?.thumbnails;
  if (Array.isArray(thumbs)) {
    let bestUrl = "";
    let bestScore = -1;
    for (const item of thumbs) {
      if (!item || typeof item !== "object") continue;
      const candidateUrl =
        typeof item.url === "string" ? item.url.trim() : "";
      if (!isValidYouTubeThumbnailUrl(candidateUrl)) continue;
      const w = Number(item.width) > 0 ? Number(item.width) : 0;
      const h = Number(item.height) > 0 ? Number(item.height) : 0;
      const score = w * h;
      if (score >= bestScore) {
        bestScore = score;
        bestUrl = candidateUrl;
      }
    }
    if (bestUrl) {
      out.thumbnail = bestUrl;
    }
  }
  return Object.keys(out).length > 0 ? out : null;
}

export function buildTVHTML5PlayerPayload(params) {
  if (!params || typeof params !== "object") {
    throw new Error("无效的播放器请求参数");
  }
  const { video_id, signature_timestamp, client, visitorData } = params;
  if (!isValidYouTubeVideoId(video_id)) {
    throw new Error("无效的 YouTube video_id");
  }
  if (!isValidSignatureTimestamp(signature_timestamp)) {
    throw new Error("无效的 signature_timestamp");
  }
  if (!isValidTVHTML5Client(client)) {
    throw new Error("仅支持 TVHTML5 客户端及合法版本号");
  }
  const TVHTML5_USER_AGENT =
    "Mozilla/5.0 (SMART-TV; Linux; Tizen 2.4.0) AppleWebKit/538.1 (KHTML, like Gecko) Version/2.4.0 TV Safari/538.1";
  const clientObj = {
    clientName: "TVHTML5",
    clientVersion: client.version,
    deviceMake: "Samsung",
    deviceModel: "UKS9800",
    userAgent: TVHTML5_USER_AGENT,
    osName: "Tizen",
    osVersion: "2.4.0",
    hl: "en",
    gl: "US",
    timeZone: "UTC",
    utcOffsetMinutes: 0,
  };
  if (typeof visitorData === "string" && visitorData.trim()) {
    clientObj.visitorData = visitorData.trim();
  }
  return {
    videoId: video_id,
    context: {
      client: clientObj,
    },
    playbackContext: {
      contentPlaybackContext: {
        html5Preference: "HTML5_PREF_WANTS",
        signatureTimestamp: signature_timestamp,
      },
    },
    contentCheckOk: true,
    racyCheckOk: true,
  };
}

function sanitizeFormatArray(list) {
  if (!Array.isArray(list)) return [];
  const out = [];
  for (const item of list) {
    if (!item || typeof item !== "object") continue;
    const clean = {};
    for (const key of ALLOWED_FORMAT_KEYS) {
      if (item[key] !== undefined && item[key] !== null) {
        clean[key] = item[key];
      }
    }
    if (
      typeof clean.itag === "number" &&
      clean.itag > 0 &&
      (typeof clean.url === "string" || typeof clean.signatureCipher === "string")
    ) {
      out.push(clean);
    }
  }
  return out;
}

export function sanitizePlayerApiResponse(raw) {
  if (!raw || typeof raw !== "object") {
    throw new Error("YouTube 播放器响应无效");
  }
  const ps = raw.playabilityStatus;
  const playabilityStatus =
    ps && typeof ps === "object"
      ? {
          status: typeof ps.status === "string" ? ps.status : "ERROR",
          ...(typeof ps.reason === "string" ? { reason: ps.reason } : {}),
        }
      : { status: "ERROR" };

  const sd = raw.streamingData;
  const streamingData =
    sd && typeof sd === "object"
      ? {
          formats: sanitizeFormatArray(sd.formats),
          adaptiveFormats: sanitizeFormatArray(sd.adaptiveFormats),
        }
      : null;

  const videoDetails = sanitizeVideoDetails(raw.videoDetails);

  return {
    playabilityStatus,
    streamingData,
    ...(videoDetails ? { videoDetails } : {}),
  };
}

function parseDetail(rawDetail) {
  if (!rawDetail) return null;
  if (typeof rawDetail === "string") {
    try {
      return JSON.parse(rawDetail);
    } catch {
      return null;
    }
  }
  if (typeof rawDetail === "object") return rawDetail;
  return null;
}

function dispatchBridgeEvent(eventTarget, eventName, payloadObj) {
  if (!eventTarget || typeof eventTarget.dispatchEvent !== "function") return;
  const jsonText = JSON.stringify(payloadObj);
  const EventCtor =
    (typeof window !== "undefined" && window.CustomEvent) ||
    globalThis.CustomEvent;
  if (typeof EventCtor === "function") {
    eventTarget.dispatchEvent(new EventCtor(eventName, { detail: jsonText }));
  }
}

const activeOpfsTempNames = new Set();
let opfsTempCounter = 0;

export async function cleanupLeftoverYouTubeOpfsTempFiles(options = {}) {
  const storage =
    options?.storage ??
    options?.pageWindow?.navigator?.storage ??
    (typeof navigator !== "undefined" ? navigator.storage : null);
  if (!storage || typeof storage.getDirectory !== "function") {
    return 0;
  }
  let root;
  try {
    root = await storage.getDirectory();
  } catch {
    return 0;
  }
  if (!root || typeof root.removeEntry !== "function") {
    return 0;
  }
  const namesToRemove = [];
  try {
    if (typeof root.keys === "function") {
      for await (const name of root.keys()) {
        if (
          typeof name === "string" &&
          name.startsWith(YOUTUBE_OPFS_TEMP_PREFIX) &&
          !activeOpfsTempNames.has(name)
        ) {
          namesToRemove.push(name);
        }
      }
    } else if (typeof root.entries === "function") {
      for await (const entry of root.entries()) {
        const name = Array.isArray(entry) ? entry[0] : entry?.name;
        if (
          typeof name === "string" &&
          name.startsWith(YOUTUBE_OPFS_TEMP_PREFIX) &&
          !activeOpfsTempNames.has(name)
        ) {
          namesToRemove.push(name);
        }
      }
    } else if (typeof root.values === "function") {
      for await (const handle of root.values()) {
        const name = handle?.name;
        if (
          typeof name === "string" &&
          name.startsWith(YOUTUBE_OPFS_TEMP_PREFIX) &&
          !activeOpfsTempNames.has(name)
        ) {
          namesToRemove.push(name);
        }
      }
    }
  } catch {
    // Ignore directory iteration failures on restricted profiles
  }
  let removed = 0;
  for (const name of namesToRemove) {
    if (activeOpfsTempNames.has(name)) continue;
    try {
      await root.removeEntry(name);
      removed += 1;
    } catch {
      // Ignore locked or already-removed entries
    }
  }
  return removed;
}

export function createUserscriptController(options = {}) {
  const {
    gmXmlhttpRequest,
    gmDownload,
    eventTarget = typeof window !== "undefined" ? window : globalThis,
    pageWindow = (typeof document !== "undefined" && document?.defaultView) ||
      (typeof window !== "undefined" ? window : globalThis),
    opfsStorage = null,
    now = () => Date.now(),
    version = YOUTUBE_HELPER_VERSION,
    mediabunny = null,
    muxConfig = {},
    saveBuffer = null,
    saveOpfsFile = null,
  } = options;

  let memoryVisitorCache = null;
  const activeDownloads = new Map();
  let muxRnCounter = 0;

  function makeCodedError(code, message) {
    const err = new Error(message);
    err.code = code;
    return err;
  }

  function makeUserCancelledError(message = "已取消保存") {
    const err = new Error(message);
    err.name = "AbortError";
    err.code = "USER_CANCELLED";
    return err;
  }

  function wrapLocalWritableStream(rawWritable) {
    if (!rawWritable || typeof rawWritable !== "object") {
      return { stream: null, forceAbort: async () => {} };
    }
    const hasGetWriter = typeof rawWritable.getWriter === "function";
    const hasWrite = typeof rawWritable.write === "function";
    if (typeof WritableStream !== "function") {
      return {
        stream: rawWritable,
        async forceAbort(reason) {
          try {
            await rawWritable.abort?.(reason);
          } catch {
            // Ignore
          }
        },
      };
    }
    if (!hasGetWriter && !hasWrite) {
      return { stream: null, forceAbort: async () => {} };
    }
    let underlyingWriter = null;
    const stream = new WritableStream({
      start() {
        if (hasGetWriter && !hasWrite) {
          underlyingWriter = rawWritable.getWriter();
        }
      },
      async write(chunk) {
        if (underlyingWriter) {
          await underlyingWriter.write(chunk);
        } else {
          await rawWritable.write(chunk);
        }
      },
      async close() {
        if (underlyingWriter) {
          await underlyingWriter.close();
          try {
            underlyingWriter.releaseLock?.();
          } catch {
            // Ignore
          }
          underlyingWriter = null;
        } else if (typeof rawWritable.close === "function") {
          await rawWritable.close();
        }
      },
      async abort(reason) {
        if (underlyingWriter) {
          await underlyingWriter.abort(reason);
          try {
            underlyingWriter.releaseLock?.();
          } catch {
            // Ignore
          }
          underlyingWriter = null;
        } else if (typeof rawWritable.abort === "function") {
          await rawWritable.abort(reason);
        }
      },
    });
    return {
      stream,
      async forceAbort(reason) {
        try {
          await stream.abort(reason);
        } catch {
          // Stream may be locked by StreamTarget or a manual writer
        }
        if (underlyingWriter) {
          try {
            await underlyingWriter.abort(reason);
          } catch {
            // Ignore
          }
          try {
            underlyingWriter.releaseLock?.();
          } catch {
            // Ignore
          }
          underlyingWriter = null;
        }
        if (typeof rawWritable.abort === "function") {
          try {
            await rawWritable.abort(reason);
          } catch {
            // Ignore
          }
        } else if (typeof rawWritable.close === "function") {
          try {
            await rawWritable.close();
          } catch {
            // Ignore
          }
        }
      },
    };
  }

  function sanitizeOpfsTaskId(rawTaskId) {
    if (typeof rawTaskId !== "string" && typeof rawTaskId !== "number") {
      return "task";
    }
    const cleaned = String(rawTaskId)
      .trim()
      .replace(/[^A-Za-z0-9_-]+/g, "_")
      .replace(/^_+|_+$/g, "")
      .slice(0, 64);
    return cleaned || "task";
  }

  function resolveOpfsStorage() {
    return (
      opfsStorage ||
      pageWindow?.navigator?.storage ||
      (typeof navigator !== "undefined" ? navigator.storage : null)
    );
  }

  async function openPickerSaveTarget({
    cleanFilename,
    mimeType,
    fileDescription,
    acceptExt,
    cancelState,
  }) {
    if (typeof pageWindow?.showSaveFilePicker !== "function") {
      return null;
    }
    try {
      const fileHandle = await pageWindow.showSaveFilePicker({
        id: YOUTUBE_FILE_PICKER_ID,
        suggestedName: cleanFilename,
        types: [
          {
            description: fileDescription,
            accept: { [mimeType]: [acceptExt] },
          },
        ],
      });
      if (cancelState.aborted) throw makeUserCancelledError();
      const rawWritable = await fileHandle.createWritable();
      const wrapped = wrapLocalWritableStream(rawWritable);
      if (!wrapped.stream) return null;
      let finalized = false;
      return {
        mode: "stream_picker",
        writable: wrapped.stream,
        tempName: null,
        async verifyResolvedSize() {
          return true;
        },
        async finalizeAndSave() {
          finalized = true;
        },
        async abortAndCleanup(reason) {
          if (finalized) return;
          await wrapped.forceAbort(reason);
        },
      };
    } catch (pickerErr) {
      if (
        cancelState.aborted ||
        pickerErr?.name === "AbortError" ||
        pickerErr?.code === "USER_CANCELLED"
      ) {
        throw makeUserCancelledError();
      }
      return null;
    }
  }

  async function openStreamingSaveTarget({
    cleanFilename,
    mimeType,
    fileDescription,
    acceptExt,
    estimatedBytes,
    taskId,
    cancelState,
  }) {
    const storage = resolveOpfsStorage();
    if (
      storage &&
      typeof storage.getDirectory === "function" &&
      typeof storage.estimate === "function"
    ) {
      let quota = 0;
      let usage = 0;
      let opfsEligible = false;
      try {
        const est = await storage.estimate();
        if (cancelState.aborted) throw makeUserCancelledError();
        quota = Number(est?.quota);
        usage = Math.max(0, Number(est?.usage ?? 0));
        const remaining = quota - usage;
        const required =
          estimatedBytes > 0
            ? Math.ceil(estimatedBytes * OPFS_QUOTA_HEADROOM_FACTOR)
            : 64 * 1024 * 1024;
        opfsEligible =
          Number.isFinite(quota) && quota > 0 && remaining >= required;
      } catch (estErr) {
        if (
          cancelState.aborted ||
          estErr?.code === "USER_CANCELLED" ||
          estErr?.name === "AbortError"
        ) {
          throw makeUserCancelledError();
        }
        opfsEligible = false;
      }

      if (opfsEligible) {
        let opfsRoot = null;
        const safeTask = sanitizeOpfsTaskId(taskId);
        const tempName = `${YOUTUBE_OPFS_TEMP_PREFIX}${safeTask}-${now()}-${++opfsTempCounter}.part`;
        activeOpfsTempNames.add(tempName);
        try {
          opfsRoot = await storage.getDirectory();
          if (cancelState.aborted) throw makeUserCancelledError();
          const fileHandle = await opfsRoot.getFileHandle(tempName, {
            create: true,
          });
          if (cancelState.aborted) throw makeUserCancelledError();
          const rawWritable = await fileHandle.createWritable();
          const wrapped = wrapLocalWritableStream(rawWritable);
          if (!wrapped.stream) {
            throw new Error("OPFS writable unavailable");
          }

          let cleaned = false;
          let streamClosedCleanly = false;
          const cleanupImmediate = async (reason) => {
            if (cleaned) return;
            cleaned = true;
            if (!streamClosedCleanly) {
              await wrapped.forceAbort(reason);
            }
            try {
              await opfsRoot.removeEntry(tempName);
            } catch {
              // Ignore if already removed
            }
            activeOpfsTempNames.delete(tempName);
          };

          if (cancelState.aborted) {
            await cleanupImmediate();
            throw makeUserCancelledError();
          }

          return {
            mode: "opfs",
            writable: wrapped.stream,
            tempName,
            async verifyResolvedSize(actualBytes) {
              const remaining = quota - usage;
              const required = Math.ceil(
                Math.max(0, Number(actualBytes) || 0) *
                  OPFS_QUOTA_HEADROOM_FACTOR,
              );
              if (remaining < required) {
                await cleanupImmediate();
                return false;
              }
              return true;
            },
            async finalizeAndSave() {
              streamClosedCleanly = true;
              if (cancelState.aborted) {
                await cleanupImmediate();
                throw makeUserCancelledError();
              }
              const file = await fileHandle.getFile();
              if (!file || file.size === 0) {
                await cleanupImmediate();
                throw makeCodedError("MUX_EMPTY_OUTPUT", "输出文件为空");
              }
              const scaledDefaultDelayMs = Math.min(
                180000,
                Math.max(
                  DEFAULT_OPFS_CLEANUP_DELAY_MS,
                  Math.ceil(file.size / (25 * 1024 * 1024)) * 1000,
                ),
              );
              const delayMs =
                Number.isFinite(muxConfig.opfsCleanupDelayMs) &&
                muxConfig.opfsCleanupDelayMs >= 0
                  ? muxConfig.opfsCleanupDelayMs
                  : scaledDefaultDelayMs;

              if (typeof saveOpfsFile === "function") {
                await saveOpfsFile(file, cleanFilename, mimeType, tempName);
                const removeTemp = async () => {
                  if (cleaned) return;
                  cleaned = true;
                  try {
                    await opfsRoot.removeEntry(tempName);
                  } catch {
                    // Ignore
                  }
                  activeOpfsTempNames.delete(tempName);
                };
                if (delayMs === 0) {
                  await removeTemp();
                } else {
                  const timer = setTimeout(() => {
                    void removeTemp();
                  }, delayMs);
                  timer?.unref?.();
                }
                return;
              }

              const doc =
                pageWindow?.document ||
                (typeof document !== "undefined" ? document : null);
              const URLObj = pageWindow?.URL || globalThis.URL;
              if (
                !doc ||
                typeof doc.createElement !== "function" ||
                typeof URLObj?.createObjectURL !== "function"
              ) {
                await cleanupImmediate();
                throw makeCodedError(
                  "SAVE_UNAVAILABLE",
                  "当前环境无法触发 OPFS 文件下载",
                );
              }
              const blobUrl = URLObj.createObjectURL(file);
              try {
                const anchor = doc.createElement("a");
                anchor.href = blobUrl;
                anchor.download = cleanFilename;
                anchor.rel = "noopener";
                anchor.style.display = "none";
                (doc.body || doc.documentElement)?.appendChild?.(anchor);
                anchor.click();
                anchor.remove?.();
              } catch (clickErr) {
                try {
                  URLObj.revokeObjectURL?.(blobUrl);
                } catch {
                  // Ignore
                }
                await cleanupImmediate();
                throw clickErr;
              }

              const delayedCleanup = async () => {
                if (cleaned) return;
                cleaned = true;
                try {
                  URLObj.revokeObjectURL?.(blobUrl);
                } catch {
                  // Ignore
                }
                try {
                  await opfsRoot.removeEntry(tempName);
                } catch {
                  // Ignore
                }
                activeOpfsTempNames.delete(tempName);
              };
              if (delayMs === 0) {
                await delayedCleanup();
              } else {
                const timer = setTimeout(() => {
                  void delayedCleanup();
                }, delayMs);
                timer?.unref?.();
              }
            },
            abortAndCleanup: cleanupImmediate,
          };
        } catch (opfsErr) {
          try {
            await opfsRoot?.removeEntry?.(tempName);
          } catch {
            // Ignore
          }
          activeOpfsTempNames.delete(tempName);
          if (
            cancelState.aborted ||
            opfsErr?.code === "USER_CANCELLED" ||
            opfsErr?.name === "AbortError"
          ) {
            throw makeUserCancelledError();
          }
          // Non-abort OPFS initialization error -> fall through to showSaveFilePicker
        }
      }
    }

    const pickerTarget = await openPickerSaveTarget({
      cleanFilename,
      mimeType,
      fileDescription,
      acceptExt,
      cancelState,
    });
    if (pickerTarget) {
      return pickerTarget;
    }

    return {
      mode: "memory_buffer",
      writable: null,
      tempName: null,
      async verifyResolvedSize() {
        return true;
      },
      async finalizeAndSave() {},
      async abortAndCleanup() {},
    };
  }

  function positiveSafeInt(val) {
    const n = typeof val === "string" ? Number(val.trim()) : Number(val);
    return Number.isSafeInteger(n) && n > 0 ? n : 0;
  }

  function readCachedVisitorData() {
    const current = now();
    if (
      memoryVisitorCache &&
      typeof memoryVisitorCache.expiresAt === "number" &&
      current < memoryVisitorCache.expiresAt &&
      isValidVisitorDataToken(memoryVisitorCache.value)
    ) {
      return memoryVisitorCache.value.trim();
    }
    memoryVisitorCache = null;
    return "";
  }

  function writeCachedVisitorData(value) {
    if (!isValidVisitorDataToken(value)) return;
    memoryVisitorCache = {
      value: value.trim(),
      expiresAt: now() + VISITOR_DATA_TTL_MS,
    };
  }

  function gmRequest(reqConfig) {
    return new Promise((resolve, reject) => {
      if (typeof gmXmlhttpRequest !== "function") {
        reject(new Error("GM_xmlhttpRequest 不可用"));
        return;
      }
      gmXmlhttpRequest({
        ...reqConfig,
        anonymous: true,
        onload(response) {
          resolve(response);
        },
        onerror() {
          reject(new Error("请求 YouTube 接口网络失败"));
        },
        ontimeout() {
          reject(new Error("请求 YouTube 接口超时"));
        },
      });
    });
  }

  async function getVisitorData() {
    const cached = readCachedVisitorData();
    if (cached) return cached;
    const resp = await gmRequest({
      method: "GET",
      url: YOUTUBE_SW_DATA_URL,
      timeout: 10000,
      headers: {
        Accept: "*/*",
        "Accept-Language": "en-US,en;q=0.9",
      },
    });
    const text = typeof resp?.responseText === "string" ? resp.responseText : "";
    const token = extractVisitorDataFromSwResponse(text);
    if (token) {
      writeCachedVisitorData(token);
    }
    return token;
  }

  async function handlePlayerAction(payload) {
    if (!payload || typeof payload !== "object") {
      throw new Error("播放器请求参数无效");
    }
    const { video_id, signature_timestamp, client } = payload;
    if (!isValidYouTubeVideoId(video_id)) {
      throw new Error("非法的 video_id");
    }
    if (!isValidSignatureTimestamp(signature_timestamp)) {
      throw new Error("非法的 signature_timestamp");
    }
    if (!isValidTVHTML5Client(client)) {
      throw new Error("非法的客户端参数：仅允许 TVHTML5");
    }

    let visitorData = "";
    try {
      visitorData = await getVisitorData();
    } catch {
      visitorData = "";
    }

    const requestBody = buildTVHTML5PlayerPayload({
      video_id,
      signature_timestamp,
      client,
      visitorData,
    });

    const headers = {
      "Content-Type": "application/json",
      "Accept-Language": "en-US,en;q=0.9",
      "User-Agent":
        "Mozilla/5.0 (SMART-TV; Linux; Tizen 2.4.0) AppleWebKit/538.1 (KHTML, like Gecko) Version/2.4.0 TV Safari/538.1",
      "X-YouTube-Client-Name": "7",
      "X-YouTube-Client-Version": client.version,
      Origin: "https://www.youtube.com",
      Referer: "https://www.youtube.com/tv",
    };
    if (visitorData) {
      headers["X-Goog-Visitor-Id"] = visitorData;
    }

    const resp = await gmRequest({
      method: "POST",
      url: YOUTUBE_PLAYER_API_URL,
      timeout: 15000,
      headers,
      data: JSON.stringify(requestBody),
    });

    if (!resp || resp.status < 200 || resp.status >= 300) {
      throw new Error(`YouTube 播放器接口返回 HTTP ${resp?.status ?? 0}`);
    }
    let parsed;
    try {
      parsed = JSON.parse(resp.responseText);
    } catch {
      throw new Error("YouTube 播放器接口返回非 JSON 数据");
    }
    if (
      parsed &&
      typeof parsed === "object" &&
      parsed.playabilityStatus?.status === "OK" &&
      (!parsed.videoDetails ||
        typeof parsed.videoDetails.title !== "string" ||
        !parsed.videoDetails.title.trim())
    ) {
      try {
        const oembedUrl = `https://www.youtube.com/oembed?url=${encodeURIComponent(`https://www.youtube.com/watch?v=${video_id.trim()}`)}&format=json`;
        const oembedResp = await gmRequest({
          method: "GET",
          url: oembedUrl,
          timeout: 8000,
          headers: {
            Accept: "application/json",
            "Accept-Language": "en-US,en;q=0.9",
          },
        });
        if (
          oembedResp &&
          oembedResp.status >= 200 &&
          oembedResp.status < 300 &&
          oembedResp.responseText
        ) {
          const oembed = JSON.parse(oembedResp.responseText);
          if (oembed && typeof oembed === "object") {
            const vd =
              parsed.videoDetails && typeof parsed.videoDetails === "object"
                ? { ...parsed.videoDetails }
                : {};
            if (!vd.title && typeof oembed.title === "string") {
              vd.title = oembed.title;
            }
            if (!vd.author && typeof oembed.author_name === "string") {
              vd.author = oembed.author_name;
            }
            if (
              !vd.thumbnail &&
              typeof oembed.thumbnail_url === "string" &&
              oembed.thumbnail_url.trim()
            ) {
              vd.thumbnail = { thumbnails: [{ url: oembed.thumbnail_url }] };
            }
            parsed.videoDetails = vd;
          }
        }
      } catch {
        // Best-effort oEmbed metadata enrichment on www.youtube.com
      }
    }
    return sanitizePlayerApiResponse(parsed);
  }

  function runLegacyGmDownload(requestId, rawUrl, cleanFilename) {
    return new Promise((resolve, reject) => {
      if (typeof gmDownload !== "function") {
        reject(new Error("GM_download 不可用"));
        return;
      }
      const lowerName = cleanFilename.toLowerCase();
      const useNativeMode =
        lowerName.endsWith(".m4a") || lowerName.endsWith(".webm");
      const handle = gmDownload({
        url: rawUrl.trim(),
        name: cleanFilename,
        saveAs: false,
        anonymous: true,
        ...(useNativeMode ? { mode: "native" } : {}),
        onprogress(progress) {
          const loaded = Number(progress?.loaded ?? progress?.done ?? 0);
          const total = Number(progress?.total ?? progress?.totalSize ?? 0);
          const percent =
            total > 0 ? Math.min(99, Math.round((loaded / total) * 100)) : 0;
          dispatchBridgeEvent(eventTarget, YOUTUBE_HELPER_PROGRESS_EVENT, {
            requestId,
            loaded: Number.isFinite(loaded) && loaded > 0 ? loaded : 0,
            total: Number.isFinite(total) && total > 0 ? total : 0,
            percent,
            mode: "gm_download",
          });
        },
        onload() {
          activeDownloads.delete(requestId);
          resolve({ filename: cleanFilename, mode: "gm_download" });
        },
        onerror(err) {
          activeDownloads.delete(requestId);
          const detail =
            err && typeof err.error === "string" ? ` (${err.error})` : "";
          reject(new Error(`GM_download 保存失败${detail}`));
        },
        ontimeout() {
          activeDownloads.delete(requestId);
          reject(new Error("GM_download 保存超时"));
        },
      });
      if (handle && typeof handle.abort === "function") {
        activeDownloads.set(requestId, handle);
      }
    });
  }

  async function handleDownloadAction(requestId, payload) {
    if (!payload || typeof payload !== "object") {
      throw new Error("下载请求参数无效");
    }
    const { url, filename } = payload;
    if (!isValidGooglevideoPlaybackUrl(url)) {
      throw new Error(
        "拒绝下载：仅允许 https://*.googlevideo.com/videoplayback 地址",
      );
    }
    const cleanName = sanitizeHelperFilename(filename);
    if (!cleanName.ok) {
      throw new Error(`拒绝下载：${cleanName.error}`);
    }

    if (typeof gmXmlhttpRequest !== "function") {
      return runLegacyGmDownload(requestId, url, cleanName.filename);
    }

    const lowerName = cleanName.filename.toLowerCase();
    const acceptExt = lowerName.endsWith(".m4a")
      ? ".m4a"
      : lowerName.endsWith(".webm")
        ? ".webm"
        : ".mp4";
    const mimeType =
      acceptExt === ".m4a"
        ? "audio/mp4"
        : acceptExt === ".webm"
          ? "video/webm"
          : "video/mp4";
    const fileDescription =
      acceptExt === ".m4a"
        ? "M4A Audio"
        : acceptExt === ".webm"
          ? "WebM Media"
          : "MP4 Video";

    const chunkSize =
      positiveSafeInt(muxConfig.chunkSize) || DEFAULT_MUX_CHUNK_BYTES;
    const concurrency =
      acceptExt === ".m4a"
        ? positiveSafeInt(muxConfig.audioConcurrency) ||
          DEFAULT_MUX_AUDIO_CONCURRENCY
        : positiveSafeInt(muxConfig.videoConcurrency) ||
          DEFAULT_MUX_VIDEO_CONCURRENCY;
    const maxRetries =
      Number.isInteger(muxConfig.maxRetries) && muxConfig.maxRetries >= 0
        ? muxConfig.maxRetries
        : DEFAULT_MUX_MAX_RETRIES;
    const retryBaseDelayMs =
      Number.isFinite(muxConfig.retryBaseDelayMs) &&
      muxConfig.retryBaseDelayMs >= 0
        ? muxConfig.retryBaseDelayMs
        : 500;
    const chunkTimeoutMs = positiveSafeInt(muxConfig.chunkTimeoutMs) || 60000;
    const maxBufferBytes =
      positiveSafeInt(muxConfig.maxBufferBytes) || MAX_IN_MEMORY_MUX_BYTES;

    const cancelState = {
      aborted: false,
      handles: new Set(),
      abortListeners: new Set(),
    };
    let saveTarget = null;
    let stream = null;
    let streamWriter = null;

    activeDownloads.set(requestId, {
      abort() {
        cancelState.aborted = true;
        for (const h of Array.from(cancelState.handles)) {
          try {
            h?.abort?.();
          } catch {
            // Ignore
          }
        }
        cancelState.handles.clear();
        for (const fn of Array.from(cancelState.abortListeners)) {
          try {
            fn();
          } catch {
            // Ignore
          }
        }
        cancelState.abortListeners.clear();
      },
    });

    try {
      const estSize = positiveSafeInt(payload.size) || extractUrlClen(url);
      saveTarget = await openStreamingSaveTarget({
        cleanFilename: cleanName.filename,
        mimeType,
        fileDescription,
        acceptExt,
        estimatedBytes: estSize,
        taskId: payload.taskId || requestId,
        cancelState,
      });

      const emitProgress = () => {
        if (!stream || cancelState.aborted) return;
        const total = stream.getKnownSize() || estSize;
        const loaded = stream.getLoadedBytes();
        const percent =
          total > 0 ? Math.min(99, Math.round((loaded / total) * 100)) : 0;
        dispatchBridgeEvent(eventTarget, YOUTUBE_HELPER_PROGRESS_EVENT, {
          requestId,
          loaded,
          total,
          percent,
          mode: saveTarget?.mode,
        });
      };

      stream = createChunkedGooglevideoSource(
        url,
        estSize,
        {
          chunkSize,
          concurrency,
          maxRetries,
          maxCachedChunks: Math.max(2, concurrency + 1),
          retryBaseDelayMs,
          chunkTimeoutMs,
          CustomSourceCtor: null,
        },
        cancelState,
        emitProgress,
      );

      const totalBytes = await stream.ensureSize();
      if (
        saveTarget.mode === "opfs" &&
        !(await saveTarget.verifyResolvedSize(totalBytes))
      ) {
        saveTarget =
          (await openPickerSaveTarget({
            cleanFilename: cleanName.filename,
            mimeType,
            fileDescription,
            acceptExt,
            cancelState,
          })) || {
            mode: "memory_buffer",
            writable: null,
            tempName: null,
            async verifyResolvedSize() {
              return true;
            },
            async finalizeAndSave() {},
            async abortAndCleanup() {},
          };
      }

      if (!saveTarget.writable && totalBytes >= maxBufferBytes) {
        stream.clear();
        activeDownloads.delete(requestId);
        if (typeof gmDownload === "function") {
          return runLegacyGmDownload(requestId, url, cleanName.filename);
        }
        throw makeCodedError(
          "MUX_TOO_LARGE_FOR_MEMORY",
          "当前环境不支持流式落盘，且文件不小于 1 GB",
        );
      }

      const totalChunks = stream.getTotalChunks();
      stream.schedulePrefetchFrom(0);

      if (saveTarget.writable) {
        streamWriter = saveTarget.writable.getWriter();
        for (let idx = 0; idx < totalChunks; idx++) {
          if (cancelState.aborted) throw makeUserCancelledError();
          const chunk = await stream.getChunk(idx);
          stream.schedulePrefetchFrom(idx + 1);
          await streamWriter.write(chunk);
          stream.evictChunk(idx);
        }
        await streamWriter.close();
        try {
          streamWriter.releaseLock?.();
        } catch {
          // Ignore
        }
        streamWriter = null;
        await saveTarget.finalizeAndSave();
        return { filename: cleanName.filename, mode: saveTarget.mode };
      }

      const out = new Uint8Array(totalBytes);
      let offset = 0;
      for (let idx = 0; idx < totalChunks; idx++) {
        if (cancelState.aborted) throw makeUserCancelledError();
        const chunk = await stream.getChunk(idx);
        stream.schedulePrefetchFrom(idx + 1);
        out.set(chunk, offset);
        offset += chunk.byteLength;
        stream.evictChunk(idx);
      }
      await defaultSaveBufferToPage(out.buffer, cleanName.filename, mimeType);
      return { filename: cleanName.filename, mode: "memory_buffer" };
    } catch (err) {
      if (streamWriter) {
        try {
          await streamWriter.abort?.(err);
        } catch {
          // Ignore
        }
        try {
          streamWriter.releaseLock?.();
        } catch {
          // Ignore
        }
        streamWriter = null;
      }
      await saveTarget?.abortAndCleanup?.(err);
      throw err;
    } finally {
      activeDownloads.delete(requestId);
      stream?.clear();
    }
  }

  function abortableSleep(ms, cancelState) {
    if (!(ms > 0)) return Promise.resolve();
    return new Promise((resolve, reject) => {
      if (cancelState.aborted) {
        reject(makeUserCancelledError());
        return;
      }
      const timer = setTimeout(() => {
        cancelState.abortListeners.delete(onCancel);
        resolve();
      }, ms);
      const onCancel = () => {
        clearTimeout(timer);
        reject(makeUserCancelledError());
      };
      cancelState.abortListeners.add(onCancel);
    });
  }

  function gmRequestRangeBuffer(reqUrl, timeoutMs, cancelState) {
    return new Promise((resolve, reject) => {
      if (cancelState.aborted) {
        reject(makeUserCancelledError());
        return;
      }
      if (typeof gmXmlhttpRequest !== "function") {
        reject(makeCodedError("GM_XHR_UNAVAILABLE", "GM_xmlhttpRequest 不可用"));
        return;
      }
      if (!isValidGooglevideoPlaybackUrl(reqUrl)) {
        reject(
          makeCodedError(
            "INVALID_MEDIA_URL",
            "拒绝请求：仅允许 https://*.googlevideo.com/videoplayback 地址",
          ),
        );
        return;
      }
      let finished = false;
      let handle = null;
      const onCancel = () => {
        if (finished) return;
        finished = true;
        try {
          handle?.abort?.();
        } catch {
          // Ignore abort errors
        }
        reject(makeUserCancelledError());
      };
      cancelState.abortListeners.add(onCancel);
      const finish = (fn) => {
        if (finished) return;
        finished = true;
        cancelState.abortListeners.delete(onCancel);
        if (handle) cancelState.handles.delete(handle);
        fn();
      };
      handle = gmXmlhttpRequest({
        method: "GET",
        url: reqUrl,
        responseType: "arraybuffer",
        anonymous: true,
        timeout: timeoutMs,
        onload(response) {
          finish(() => resolve(response));
        },
        onerror() {
          finish(() => reject(new Error("读取媒体分块网络失败")));
        },
        ontimeout() {
          finish(() => reject(new Error("读取媒体分块超时")));
        },
      });
      if (handle && !finished) {
        cancelState.handles.add(handle);
      }
    });
  }

  function extractMetSeconds(rawUrl) {
    try {
      const u = new URL(rawUrl);
      const metRaw = (u.searchParams.get("met") || "").split(",")[0];
      const n = Number(metRaw);
      return Number.isFinite(n) && n > 0 ? n : 0;
    } catch {
      return 0;
    }
  }

  function createChunkedGooglevideoSource(
    initialUrl,
    declaredSize,
    streamOpts,
    cancelState,
    onProgressTick,
  ) {
    const {
      chunkSize,
      concurrency,
      maxRetries,
      maxCachedChunks,
      retryBaseDelayMs,
      chunkTimeoutMs,
      CustomSourceCtor,
    } = streamOpts;

    let activeBaseUrl = initialUrl.trim();
    let resolvedSize =
      positiveSafeInt(declaredSize) || extractUrlClen(activeBaseUrl);
    let totalChunks = resolvedSize > 0 ? Math.ceil(resolvedSize / chunkSize) : 0;
    let loadedBytes = 0;
    const completedChunks = new Set();
    const cache = new Map();
    const inFlight = new Map();
    const prefetchQueue = [];
    let useTick = 0;

    async function ensureMetGate() {
      const metSec = extractMetSeconds(activeBaseUrl);
      if (metSec <= 0) return;
      const waitMs = Math.ceil((metSec + 4.5) * 1000 - now());
      if (waitMs > 0 && waitMs <= 15000) {
        await abortableSleep(waitMs, cancelState);
      }
    }

    function adoptFinalUrl(finalUrl) {
      if (typeof finalUrl !== "string" || !finalUrl.trim()) return;
      if (!isValidGooglevideoPlaybackUrl(finalUrl)) {
        throw makeCodedError(
          "DISALLOWED_REDIRECT",
          "媒体重定向地址不在允许的 *.googlevideo.com/videoplayback 范围内",
        );
      }
      const fu = new URL(finalUrl.trim());
      fu.searchParams.delete("range");
      fu.searchParams.delete("rn");
      activeBaseUrl = fu.toString();
    }

    function touchCache(chunkIdx, bytes) {
      cache.set(chunkIdx, { data: bytes, lastUsed: ++useTick });
      while (cache.size > maxCachedChunks) {
        let oldestKey = null;
        let oldestTick = Infinity;
        for (const [k, v] of cache.entries()) {
          if (v.lastUsed < oldestTick) {
            oldestTick = v.lastUsed;
            oldestKey = k;
          }
        }
        if (oldestKey !== null) cache.delete(oldestKey);
        else break;
      }
    }

    async function ensureSize() {
      if (cancelState.aborted) throw makeUserCancelledError();
      if (resolvedSize > 0) {
        totalChunks = Math.ceil(resolvedSize / chunkSize);
        return resolvedSize;
      }
      await ensureMetGate();
      const u = new URL(activeBaseUrl);
      u.searchParams.set("range", "0-0");
      u.searchParams.set("rn", String(++muxRnCounter));
      const resp = await gmRequestRangeBuffer(
        u.toString(),
        Math.min(20000, chunkTimeoutMs),
        cancelState,
      );
      if (resp?.finalUrl) adoptFinalUrl(resp.finalUrl);
      const clenParam = extractUrlClen(resp?.finalUrl || activeBaseUrl);
      const headerMatch = /bytes\s+0-0\/(\d+)/i.exec(
        String(resp?.responseHeaders || ""),
      );
      const parsed = headerMatch ? Number(headerMatch[1]) : clenParam;
      if (!Number.isSafeInteger(parsed) || parsed <= 0) {
        throw makeCodedError(
          "MUX_SIZE_UNKNOWN",
          `无法获取媒体流总字节数（HTTP ${resp?.status ?? 0}）`,
        );
      }
      resolvedSize = parsed;
      totalChunks = Math.ceil(resolvedSize / chunkSize);
      return resolvedSize;
    }

    async function downloadSingleChunk(chunkIdx) {
      await ensureMetGate();
      const size = await ensureSize();
      const start = chunkIdx * chunkSize;
      const end = Math.min(size - 1, start + chunkSize - 1);
      const expectedLen = end - start + 1;

      let lastErr = null;
      for (let attempt = 0; attempt <= maxRetries; attempt++) {
        if (cancelState.aborted) throw makeUserCancelledError();
        if (attempt > 0) {
          await abortableSleep(
            retryBaseDelayMs * Math.pow(2, attempt - 1),
            cancelState,
          );
        }
        const u = new URL(activeBaseUrl);
        u.searchParams.set("range", `${start}-${end}`);
        u.searchParams.set("rn", String(++muxRnCounter));
        let resp;
        try {
          resp = await gmRequestRangeBuffer(
            u.toString(),
            chunkTimeoutMs,
            cancelState,
          );
        } catch (err) {
          if (
            cancelState.aborted ||
            err?.code === "USER_CANCELLED" ||
            err?.code === "INVALID_MEDIA_URL"
          ) {
            throw err;
          }
          lastErr = err;
          if (attempt === maxRetries) throw err;
          continue;
        }
        if (resp?.finalUrl) {
          adoptFinalUrl(resp.finalUrl);
        }
        if (resp?.status !== 200 && resp?.status !== 206) {
          lastErr = makeCodedError(
            "MUX_CHUNK_HTTP_ERROR",
            `分块 #${chunkIdx} 请求失败（HTTP ${resp?.status ?? 0}）`,
          );
          if (attempt === maxRetries) throw lastErr;
          continue;
        }
        const rawBuf = resp?.response;
        const u8 =
          rawBuf instanceof Uint8Array
            ? rawBuf
            : rawBuf instanceof ArrayBuffer
              ? new Uint8Array(rawBuf)
              : ArrayBuffer.isView(rawBuf)
                ? new Uint8Array(
                    rawBuf.buffer,
                    rawBuf.byteOffset,
                    rawBuf.byteLength,
                  )
                : null;
        const gotLen = u8 ? u8.byteLength : 0;
        if (gotLen !== expectedLen) {
          lastErr = makeCodedError(
            "MUX_CHUNK_TRUNCATED",
            `分块 #${chunkIdx} 字节不完整（收到 ${gotLen}，预期 ${expectedLen}）`,
          );
          if (attempt === maxRetries) throw lastErr;
          continue;
        }
        if (!completedChunks.has(chunkIdx)) {
          completedChunks.add(chunkIdx);
          loadedBytes += gotLen;
          onProgressTick();
        }
        touchCache(chunkIdx, u8);
        return u8;
      }
      throw lastErr || new Error(`分块 #${chunkIdx} 重试耗尽`);
    }

    function getChunk(chunkIdx) {
      if (cancelState.aborted) {
        return Promise.reject(makeUserCancelledError());
      }
      if (cache.has(chunkIdx)) {
        const entry = cache.get(chunkIdx);
        entry.lastUsed = ++useTick;
        return Promise.resolve(entry.data);
      }
      if (inFlight.has(chunkIdx)) {
        return inFlight.get(chunkIdx);
      }
      const p = downloadSingleChunk(chunkIdx).finally(() => {
        inFlight.delete(chunkIdx);
        pumpPrefetch();
      });
      inFlight.set(chunkIdx, p);
      return p;
    }

    function pumpPrefetch() {
      while (
        !cancelState.aborted &&
        prefetchQueue.length > 0 &&
        inFlight.size < concurrency &&
        cache.size + inFlight.size < maxCachedChunks
      ) {
        const nextIdx = prefetchQueue.shift();
        if (cache.has(nextIdx) || inFlight.has(nextIdx)) continue;
        getChunk(nextIdx)
          .catch(() => {})
          .finally(() => {
            pumpPrefetch();
          });
      }
    }

    function schedulePrefetchFrom(startChunkIdx) {
      if (!totalChunks || cancelState.aborted) return;
      prefetchQueue.length = 0;
      const limit = Math.min(totalChunks, startChunkIdx + concurrency);
      for (let idx = startChunkIdx; idx < limit; idx++) {
        if (!cache.has(idx) && !inFlight.has(idx)) {
          prefetchQueue.push(idx);
        }
      }
      pumpPrefetch();
    }

    const customSource =
      typeof CustomSourceCtor === "function"
        ? new CustomSourceCtor({
            maxCacheSize: 2 * 1024 * 1024,
            getSize: () => ensureSize(),
            read: async (start, end) => {
              if (cancelState.aborted) throw makeUserCancelledError();
              const size = await ensureSize();
              const clampedEnd = Math.min(size, end);
              const len = clampedEnd - start;
              if (len <= 0) return new Uint8Array(0);

              const firstChunk = Math.floor(start / chunkSize);
              const lastChunk = Math.floor((clampedEnd - 1) / chunkSize);

              if (firstChunk === lastChunk) {
                const chunkBytes = await getChunk(firstChunk);
                schedulePrefetchFrom(lastChunk + 1);
                const offset = start - firstChunk * chunkSize;
                return chunkBytes.slice(offset, offset + len);
              }

              const needed = [];
              for (let c = firstChunk; c <= lastChunk; c++) {
                needed.push(getChunk(c));
              }
              const chunks = await Promise.all(needed);
              schedulePrefetchFrom(lastChunk + 1);
              const out = new Uint8Array(len);
              let written = 0;
              for (let i = 0; i < chunks.length; i++) {
                const c = firstChunk + i;
                const chunkBytes = chunks[i];
                const chunkStart = c * chunkSize;
                const sliceStart = Math.max(0, start - chunkStart);
                const sliceEnd = Math.min(
                  chunkBytes.byteLength,
                  clampedEnd - chunkStart,
                );
                const part = chunkBytes.subarray(sliceStart, sliceEnd);
                out.set(part, written);
                written += part.byteLength;
              }
              return out;
            },
          })
        : null;

    return {
      source: customSource,
      ensureSize,
      getTotalChunks: () => totalChunks,
      getChunk,
      schedulePrefetchFrom,
      evictChunk: (idx) => {
        cache.delete(idx);
      },
      getKnownSize: () => resolvedSize,
      getLoadedBytes: () => loadedBytes,
      clear: () => {
        prefetchQueue.length = 0;
        cache.clear();
      },
    };
  }

  function defaultSaveBufferToPage(arrayBuffer, filename, mimeType) {
    if (typeof saveBuffer === "function") {
      return saveBuffer(arrayBuffer, filename, mimeType);
    }
    const doc =
      pageWindow?.document ||
      (typeof document !== "undefined" ? document : null);
    const BlobCtor = pageWindow?.Blob || globalThis.Blob;
    const URLObj = pageWindow?.URL || globalThis.URL;
    if (
      !doc ||
      typeof doc.createElement !== "function" ||
      typeof BlobCtor !== "function" ||
      typeof URLObj?.createObjectURL !== "function"
    ) {
      throw makeCodedError(
        "SAVE_UNAVAILABLE",
        "当前环境无法触发内存合流文件下载",
      );
    }
    const blob = new BlobCtor([arrayBuffer], { type: mimeType });
    const blobUrl = URLObj.createObjectURL(blob);
    try {
      const anchor = doc.createElement("a");
      anchor.href = blobUrl;
      anchor.download = filename;
      anchor.rel = "noopener";
      anchor.style.display = "none";
      (doc.body || doc.documentElement)?.appendChild?.(anchor);
      anchor.click();
      anchor.remove?.();
    } finally {
      setTimeout(() => {
        try {
          URLObj.revokeObjectURL(blobUrl);
        } catch {
          // Ignore revoke errors
        }
      }, 30000);
    }
  }

  async function handleMuxDownloadAction(requestId, payload) {
    if (!payload || typeof payload !== "object") {
      throw makeCodedError("INVALID_MUX_PARAMS", "合流请求参数无效");
    }
    const { videoUrl, audioUrl, filename, outputContainer } = payload;
    if (
      !isValidGooglevideoPlaybackUrl(videoUrl) ||
      !isValidGooglevideoPlaybackUrl(audioUrl)
    ) {
      throw makeCodedError(
        "INVALID_MEDIA_URL",
        "拒绝合流：仅允许 https://*.googlevideo.com/videoplayback 地址",
      );
    }
    if (outputContainer !== "mp4" && outputContainer !== "webm") {
      throw makeCodedError(
        "UNSUPPORTED_CONTAINER",
        "仅支持封装为 mp4 或 webm 容器",
      );
    }
    const cleanName = sanitizeHelperFilename(filename);
    if (!cleanName.ok) {
      throw makeCodedError("INVALID_FILENAME", `拒绝合流：${cleanName.error}`);
    }
    const expectedExt = `.${outputContainer}`;
    if (!cleanName.filename.toLowerCase().endsWith(expectedExt)) {
      throw makeCodedError(
        "CONTAINER_FILENAME_MISMATCH",
        `文件名后缀与目标容器（${expectedExt}）不一致`,
      );
    }
    if (!mediabunny || typeof mediabunny.Input !== "function") {
      throw makeCodedError("MEDIABUNNY_UNAVAILABLE", "合流组件尚未加载");
    }
    if (typeof gmXmlhttpRequest !== "function") {
      throw makeCodedError("GM_XHR_UNAVAILABLE", "GM_xmlhttpRequest 不可用");
    }

    const chunkSize = positiveSafeInt(muxConfig.chunkSize) || DEFAULT_MUX_CHUNK_BYTES;
    const videoConcurrency =
      positiveSafeInt(muxConfig.videoConcurrency) ||
      DEFAULT_MUX_VIDEO_CONCURRENCY;
    const audioConcurrency =
      positiveSafeInt(muxConfig.audioConcurrency) ||
      DEFAULT_MUX_AUDIO_CONCURRENCY;
    const maxRetries =
      Number.isInteger(muxConfig.maxRetries) && muxConfig.maxRetries >= 0
        ? muxConfig.maxRetries
        : DEFAULT_MUX_MAX_RETRIES;
    const retryBaseDelayMs =
      Number.isFinite(muxConfig.retryBaseDelayMs) &&
      muxConfig.retryBaseDelayMs >= 0
        ? muxConfig.retryBaseDelayMs
        : 500;
    const chunkTimeoutMs =
      positiveSafeInt(muxConfig.chunkTimeoutMs) || 60000;
    const maxBufferBytes =
      positiveSafeInt(muxConfig.maxBufferBytes) || MAX_IN_MEMORY_MUX_BYTES;

    const cancelState = {
      aborted: false,
      handles: new Set(),
      abortListeners: new Set(),
      writable: null,
    };
    activeDownloads.set(requestId, {
      abort() {
        cancelState.aborted = true;
        for (const h of Array.from(cancelState.handles)) {
          try {
            h?.abort?.();
          } catch {
            // Ignore
          }
        }
        cancelState.handles.clear();
        for (const fn of Array.from(cancelState.abortListeners)) {
          try {
            fn();
          } catch {
            // Ignore
          }
        }
        cancelState.abortListeners.clear();
      },
    });

    const mimeType = outputContainer === "webm" ? "video/webm" : "video/mp4";
    const fileDescription =
      outputContainer === "webm" ? "WebM Video" : "MP4 Video";
    const acceptExt = `.${outputContainer}`;
    const estVideo =
      positiveSafeInt(payload.videoSize) || extractUrlClen(videoUrl);
    const estAudio =
      positiveSafeInt(payload.audioSize) || extractUrlClen(audioUrl);

    let saveTarget = null;
    let vStream = null;
    let aStream = null;
    let vInput = null;
    let aInput = null;

    try {
      saveTarget = await openStreamingSaveTarget({
        cleanFilename: cleanName.filename,
        mimeType,
        fileDescription,
        acceptExt,
        estimatedBytes: estVideo + estAudio,
        taskId: payload.taskId || requestId,
        cancelState,
      });

      if (!saveTarget.writable && estVideo + estAudio >= maxBufferBytes) {
        throw makeCodedError(
          "MUX_TOO_LARGE_FOR_MEMORY",
          "当前环境不支持流式落盘（OPFS / showSaveFilePicker），且预计文件不小于 1 GB",
        );
      }

      const emitProgress = () => {
        if (!vStream || !aStream || cancelState.aborted) return;
        const total =
          (vStream.getKnownSize() || estVideo) +
          (aStream.getKnownSize() || estAudio);
        const loaded = vStream.getLoadedBytes() + aStream.getLoadedBytes();
        const percent =
          total > 0 ? Math.min(99, Math.round((loaded / total) * 100)) : 0;
        dispatchBridgeEvent(eventTarget, YOUTUBE_HELPER_PROGRESS_EVENT, {
          requestId,
          loaded,
          total,
          percent,
          mode: saveTarget?.mode,
        });
      };

      vStream = createChunkedGooglevideoSource(
        videoUrl,
        estVideo,
        {
          chunkSize,
          concurrency: videoConcurrency,
          maxRetries,
          maxCachedChunks: 3,
          retryBaseDelayMs,
          chunkTimeoutMs,
          CustomSourceCtor: mediabunny.CustomSource,
        },
        cancelState,
        emitProgress,
      );
      aStream = createChunkedGooglevideoSource(
        audioUrl,
        estAudio,
        {
          chunkSize,
          concurrency: audioConcurrency,
          maxRetries,
          maxCachedChunks: 2,
          retryBaseDelayMs,
          chunkTimeoutMs,
          CustomSourceCtor: mediabunny.CustomSource,
        },
        cancelState,
        emitProgress,
      );

      const [resolvedVSize, resolvedASize] = await Promise.all([
        vStream.ensureSize(),
        aStream.ensureSize(),
      ]);
      if (
        saveTarget.mode === "opfs" &&
        !(await saveTarget.verifyResolvedSize(resolvedVSize + resolvedASize))
      ) {
        saveTarget =
          (await openPickerSaveTarget({
            cleanFilename: cleanName.filename,
            mimeType,
            fileDescription,
            acceptExt,
            cancelState,
          })) || {
            mode: "memory_buffer",
            writable: null,
            tempName: null,
            async verifyResolvedSize() {
              return true;
            },
            async finalizeAndSave() {},
            async abortAndCleanup() {},
          };
      }
      if (
        !saveTarget.writable &&
        resolvedVSize + resolvedASize >= maxBufferBytes
      ) {
        throw makeCodedError(
          "MUX_TOO_LARGE_FOR_MEMORY",
          "当前环境不支持流式落盘（OPFS / showSaveFilePicker），且文件不小于 1 GB",
        );
      }

      vInput = new mediabunny.Input({
        source: vStream.source,
        formats: [mediabunny.MP4, mediabunny.WEBM],
      });
      aInput = new mediabunny.Input({
        source: aStream.source,
        formats: [mediabunny.MP4, mediabunny.WEBM],
      });

      const [vTrack, aTrack] = await Promise.all([
        vInput.getPrimaryVideoTrack(),
        aInput.getPrimaryAudioTrack(),
      ]);
      if (!vTrack) {
        throw makeCodedError("MUX_NO_VIDEO_TRACK", "视频流中未找到主视频轨");
      }
      if (!aTrack) {
        throw makeCodedError("MUX_NO_AUDIO_TRACK", "音频流中未找到主音轨");
      }

      const [vCodec, aCodec, vDecoderConfig, aDecoderConfig] =
        await Promise.all([
          vTrack.getCodec(),
          aTrack.getCodec(),
          vTrack.getDecoderConfig(),
          aTrack.getDecoderConfig(),
        ]);
      if (!vCodec || !aCodec) {
        throw makeCodedError(
          "MUX_CODEC_UNSUPPORTED",
          `音视频编码不支持转封装（video=${vCodec || "未知"}, audio=${aCodec || "未知"}）`,
        );
      }

      const format =
        outputContainer === "webm"
          ? new mediabunny.WebMOutputFormat()
          : new mediabunny.Mp4OutputFormat({ fastStart: false });
      const target = saveTarget.writable
        ? new mediabunny.StreamTarget(saveTarget.writable, {
            chunked: true,
            chunkSize: 8 * 1024 * 1024,
          })
        : new mediabunny.BufferTarget();
      const output = new mediabunny.Output({ format, target });

      const vOut = new mediabunny.EncodedVideoPacketSource(vCodec);
      const aOut = new mediabunny.EncodedAudioPacketSource(aCodec);
      output.addVideoTrack(vOut, { rotation: vTrack.rotation });
      output.addAudioTrack(aOut);

      await output.start();

      const vSink = new mediabunny.EncodedPacketSink(vTrack);
      const aSink = new mediabunny.EncodedPacketSink(aTrack);
      const vIter = vSink.packets()[Symbol.asyncIterator]();
      const aIter = aSink.packets()[Symbol.asyncIterator]();

      let [vNext, aNext] = await Promise.all([vIter.next(), aIter.next()]);
      let vFirst = true;
      let aFirst = true;

      while (!vNext.done || !aNext.done) {
        if (cancelState.aborted) throw makeUserCancelledError();
        const takeVideo =
          !vNext.done &&
          (aNext.done || vNext.value.timestamp <= aNext.value.timestamp);
        if (takeVideo) {
          await vOut.add(
            vNext.value,
            vFirst && vDecoderConfig
              ? { decoderConfig: vDecoderConfig }
              : undefined,
          );
          vFirst = false;
          vNext = await vIter.next();
        } else {
          await aOut.add(
            aNext.value,
            aFirst && aDecoderConfig
              ? { decoderConfig: aDecoderConfig }
              : undefined,
          );
          aFirst = false;
          aNext = await aIter.next();
        }
      }

      vOut.close();
      aOut.close();
      await output.finalize();

      if (saveTarget.writable) {
        await saveTarget.finalizeAndSave();
      } else {
        const outBuf = target.buffer;
        if (!outBuf || outBuf.byteLength === 0) {
          throw makeCodedError("MUX_EMPTY_OUTPUT", "合流输出为空");
        }
        if (outBuf.byteLength >= maxBufferBytes) {
          throw makeCodedError(
            "MUX_TOO_LARGE_FOR_MEMORY",
            "内存合流输出不小于 1 GB，已中止",
          );
        }
        await defaultSaveBufferToPage(outBuf, cleanName.filename, mimeType);
      }

      return {
        filename: cleanName.filename,
        mode: saveTarget.mode,
      };
    } catch (err) {
      await saveTarget?.abortAndCleanup?.(err);
      throw err;
    } finally {
      activeDownloads.delete(requestId);
      vStream?.clear();
      aStream?.clear();
      try {
        vInput?.dispose?.();
      } catch {
        // Ignore
      }
      try {
        aInput?.dispose?.();
      } catch {
        // Ignore
      }
    }
  }

  async function handleRequestObject(message) {
    if (!message || typeof message !== "object") {
      return null;
    }
    const { requestId, action, payload } = message;
    if (typeof requestId !== "string" || !requestId.trim()) {
      return null;
    }
    try {
      switch (action) {
        case "ping":
          return {
            requestId,
            ok: true,
            data: {
              version,
              name: "DokiTool YouTube Helper",
            },
          };
        case "player": {
          const data = await handlePlayerAction(payload);
          return { requestId, ok: true, data };
        }
        case "download": {
          const data = await handleDownloadAction(requestId, payload);
          return { requestId, ok: true, data };
        }
        case "mux_download": {
          const data = await handleMuxDownloadAction(requestId, payload);
          return { requestId, ok: true, data };
        }
        case "cancel_download": {
          const targetId = payload?.targetRequestId;
          if (typeof targetId === "string" && activeDownloads.has(targetId)) {
            const handle = activeDownloads.get(targetId);
            activeDownloads.delete(targetId);
            try {
              handle.abort();
            } catch {
              // Ignore abort errors
            }
          }
          return { requestId, ok: true, data: { cancelled: true } };
        }
        default:
          return {
            requestId,
            ok: false,
            error: {
              code: "UNSUPPORTED_ACTION",
              message: "不支持的助手操作",
            },
          };
      }
    } catch (error) {
      return {
        requestId,
        ok: false,
        error: {
          code:
            error && typeof error.code === "string" && error.code
              ? error.code
              : "HELPER_REJECTED",
          message: error instanceof Error ? error.message : "助手执行失败",
        },
      };
    }
  }

  function onRequestEvent(event) {
    const message = parseDetail(event?.detail);
    if (!message) return;
    void handleRequestObject(message).then((response) => {
      if (response) {
        dispatchBridgeEvent(
          eventTarget,
          YOUTUBE_HELPER_RESPONSE_EVENT,
          response,
        );
      }
    });
  }

  function mount() {
    void cleanupLeftoverYouTubeOpfsTempFiles({
      storage: resolveOpfsStorage(),
      pageWindow,
    }).catch(() => {});
    if (eventTarget && typeof eventTarget.addEventListener === "function") {
      eventTarget.addEventListener(
        YOUTUBE_HELPER_REQUEST_EVENT,
        onRequestEvent,
      );
    }
    return () => {
      if (
        eventTarget &&
        typeof eventTarget.removeEventListener === "function"
      ) {
        eventTarget.removeEventListener(
          YOUTUBE_HELPER_REQUEST_EVENT,
          onRequestEvent,
        );
      }
    };
  }

  return {
    mount,
    handleRequestObject,
    getVisitorData,
  };
}

let reqCounter = 0;
function nextRequestId(prefix = "yt-req") {
  reqCounter += 1;
  return `${prefix}-${Date.now()}-${reqCounter}`;
}

export function sendHelperRequest(action, payload = {}, options = {}) {
  const {
    eventTarget = typeof window !== "undefined" ? window : globalThis,
    timeoutMs = 15000,
    signal,
    onProgress,
  } = options;

  const requestId = nextRequestId(action);
  return new Promise((resolve, reject) => {
    if (
      !eventTarget ||
      typeof eventTarget.addEventListener !== "function" ||
      typeof eventTarget.dispatchEvent !== "function"
    ) {
      reject(new Error("当前环境不支持助手通信"));
      return;
    }
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }

    let settled = false;
    let timer = null;

    const cleanup = () => {
      if (timer !== null) {
        clearTimeout(timer);
        timer = null;
      }
      eventTarget.removeEventListener(
        YOUTUBE_HELPER_RESPONSE_EVENT,
        onResponse,
      );
      if (typeof onProgress === "function") {
        eventTarget.removeEventListener(
          YOUTUBE_HELPER_PROGRESS_EVENT,
          onProgressEvent,
        );
      }
      signal?.removeEventListener("abort", onAbort);
    };

    const onAbort = () => {
      if (settled) return;
      settled = true;
      cleanup();
      if (action === "download" || action === "mux_download") {
        dispatchBridgeEvent(eventTarget, YOUTUBE_HELPER_REQUEST_EVENT, {
          requestId: nextRequestId("cancel"),
          action: "cancel_download",
          payload: { targetRequestId: requestId },
        });
      }
      reject(new DOMException("Aborted", "AbortError"));
    };

    const onProgressEvent = (event) => {
      const detail = parseDetail(event?.detail);
      if (!detail || detail.requestId !== requestId) return;
      onProgress(detail);
    };

    const onResponse = (event) => {
      const detail = parseDetail(event?.detail);
      if (!detail || detail.requestId !== requestId) return;
      if (settled) return;
      settled = true;
      cleanup();
      if (detail.ok) {
        resolve(detail.data);
      } else {
        const err = new Error(
          detail.error?.message ||
            (detail.error?.code === "USER_CANCELLED" ? "Aborted" : "助手请求失败"),
        );
        if (detail.error?.code === "USER_CANCELLED") {
          err.name = "AbortError";
        }
        if (detail.error?.code) {
          err.code = detail.error.code;
        }
        reject(err);
      }
    };

    eventTarget.addEventListener(YOUTUBE_HELPER_RESPONSE_EVENT, onResponse);
    if (typeof onProgress === "function") {
      eventTarget.addEventListener(
        YOUTUBE_HELPER_PROGRESS_EVENT,
        onProgressEvent,
      );
    }
    signal?.addEventListener("abort", onAbort, { once: true });

    if (timeoutMs > 0) {
      timer = setTimeout(() => {
        if (settled) return;
        settled = true;
        cleanup();
        reject(new Error("助手响应超时"));
      }, timeoutMs);
    }

    dispatchBridgeEvent(eventTarget, YOUTUBE_HELPER_REQUEST_EVENT, {
      requestId,
      action,
      payload,
    });
  });
}

export async function pingYouTubeHelper(options = {}) {
  const { timeoutMs = 600, eventTarget } = options;
  try {
    const data = await sendHelperRequest(
      "ping",
      {},
      { timeoutMs, eventTarget },
    );
    const version = typeof data?.version === "string" ? data.version.trim() : "";
    if (!version) {
      return { installed: false, version: null, supported: false };
    }
    return {
      installed: true,
      version,
      supported: isHelperVersionSupported(version, MIN_YOUTUBE_HELPER_VERSION),
    };
  } catch {
    return { installed: false, version: null, supported: false };
  }
}

export async function requestYouTubePlayerViaHelper(payload, options = {}) {
  return sendHelperRequest("player", payload, {
    timeoutMs: 20000,
    ...options,
  });
}

export async function downloadViaYouTubeHelper(payload, options = {}) {
  return sendHelperRequest(
    "download",
    {
      url: payload.url,
      filename: payload.filename,
      size: payload.size,
      taskId: payload.taskId,
    },
    {
      timeoutMs: 0,
      onProgress: payload.onProgress,
      signal: payload.signal,
      eventTarget: options.eventTarget,
    },
  );
}

export async function muxDownloadViaYouTubeHelper(payload, options = {}) {
  return sendHelperRequest(
    "mux_download",
    {
      videoUrl: payload.videoUrl,
      audioUrl: payload.audioUrl,
      videoSize: payload.videoSize,
      audioSize: payload.audioSize,
      filename: payload.filename,
      outputContainer: payload.outputContainer,
      taskId: payload.taskId,
    },
    {
      timeoutMs: 0,
      onProgress: payload.onProgress,
      signal: payload.signal,
      eventTarget: options.eventTarget,
    },
  );
}
