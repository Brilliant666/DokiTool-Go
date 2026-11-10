// Standalone core module for the DokiTool YouTube userscript and page bridge.
// Designed so that the exact same validation and request logic runs in the
// userscript (Tampermonkey / Violentmonkey), the Vue frontend, and Node unit tests,
// and can be packaged into a browser extension in the future.

export const YOUTUBE_HELPER_VERSION = "1.1.0";
export const MIN_YOUTUBE_HELPER_VERSION = "1.1.0";
export const YOUTUBE_HELPER_SCRIPT_URL = "/helper/dokitool-youtube.user.js";

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
  } else {
    return {
      ok: false,
      filename: "",
      error: "仅允许保存 .mp4 或 .m4a 后缀的文件",
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

export function createUserscriptController(options = {}) {
  const {
    gmXmlhttpRequest,
    gmDownload,
    eventTarget = typeof window !== "undefined" ? window : globalThis,
    now = () => Date.now(),
    version = YOUTUBE_HELPER_VERSION,
  } = options;

  let memoryVisitorCache = null;
  const activeDownloads = new Map();

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

  function handleDownloadAction(requestId, payload) {
    return new Promise((resolve, reject) => {
      if (!payload || typeof payload !== "object") {
        reject(new Error("下载请求参数无效"));
        return;
      }
      const { url, filename } = payload;
      if (!isValidGooglevideoPlaybackUrl(url)) {
        reject(
          new Error(
            "拒绝下载：仅允许 https://*.googlevideo.com/videoplayback 地址",
          ),
        );
        return;
      }
      const cleanName = sanitizeHelperFilename(filename);
      if (!cleanName.ok) {
        reject(new Error(`拒绝下载：${cleanName.error}`));
        return;
      }
      if (typeof gmDownload !== "function") {
        reject(new Error("GM_download 不可用"));
        return;
      }

      const isM4a = cleanName.filename.toLowerCase().endsWith(".m4a");
      const handle = gmDownload({
        url: url.trim(),
        name: cleanName.filename,
        saveAs: false,
        anonymous: true,
        ...(isM4a ? { mode: "native" } : {}),
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
          });
        },
        onload() {
          activeDownloads.delete(requestId);
          resolve({ filename: cleanName.filename });
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
          code: "HELPER_REJECTED",
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
      if (action === "download") {
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
        const err = new Error(detail.error?.message || "助手请求失败");
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
    { url: payload.url, filename: payload.filename },
    {
      timeoutMs: 0,
      onProgress: payload.onProgress,
      signal: payload.signal,
      eventTarget: options.eventTarget,
    },
  );
}
