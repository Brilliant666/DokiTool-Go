// ==UserScript==
// @name         DokiTool YouTube Helper
// @namespace    https://snap.dokitool.com/
// @version      1.0.0
// @description  DokiTool YouTube 用户侧取址与原生下载助手（无 Cookie、不碰账号、仅连接 www.youtube.com）
// @author       DokiTool
// @match        https://snap.dokitool.com/*
// @connect      www.youtube.com
// @grant        GM_xmlhttpRequest
// @grant        GM_download
// @noframes
// @run-at       document-start
// @updateURL    https://snap.dokitool.com/helper/dokitool-youtube.user.js
// @downloadURL  https://snap.dokitool.com/helper/dokitool-youtube.user.js
// ==/UserScript==

(function () {
  "use strict";

  const YOUTUBE_HELPER_VERSION = "1.0.0";
  const YOUTUBE_HELPER_REQUEST_EVENT = "dokitool:youtube-helper:request";
  const YOUTUBE_HELPER_RESPONSE_EVENT = "dokitool:youtube-helper:response";
  const YOUTUBE_HELPER_PROGRESS_EVENT = "dokitool:youtube-helper:progress";

  const YOUTUBE_SW_DATA_URL = "https://www.youtube.com/sw.js_data";
  const YOUTUBE_PLAYER_API_URL =
    "https://www.youtube.com/youtubei/v1/player?prettyPrint=false";
  const VISITOR_DATA_TTL_MS = 30 * 60 * 1000;
  const VISITOR_DATA_STORAGE_KEY = "dokitool_yt_visitor_data_v1";

  const VIDEO_ID_RE = /^[A-Za-z0-9_-]{11}$/;
  const TVHTML5_VERSION_RE = /^(?:7\.[0-9]{8}\.[0-9]{2}\.[0-9]{2}|5\.[0-9]{8})$/;
  const GOOGLEVIDEO_HOST_RE =
    /^[a-z0-9-]+(?:\.[a-z0-9-]+)*\.googlevideo\.com$/;
  const VISITOR_DATA_TOKEN_RE = /Cg[A-Za-z0-9_-]{8,1024}(?:%3D|=)*/;

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

  function isValidYouTubeVideoId(videoId) {
    return typeof videoId === "string" && VIDEO_ID_RE.test(videoId);
  }

  function isValidSignatureTimestamp(sts) {
    return (
      typeof sts === "number" &&
      Number.isInteger(sts) &&
      sts >= 10000 &&
      sts <= 999999
    );
  }

  function isValidTVHTML5Client(client) {
    if (!client || typeof client !== "object") return false;
    return (
      client.name === "TVHTML5" &&
      typeof client.version === "string" &&
      TVHTML5_VERSION_RE.test(client.version)
    );
  }

  function isValidGooglevideoPlaybackUrl(rawUrl) {
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

  function sanitizeHelperFilename(rawName) {
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

  function extractVisitorDataFromSwResponse(rawText) {
    if (typeof rawText !== "string" || !rawText) return "";
    const stripped = rawText.replace(/^\)\]\}'\s*/, "");
    try {
      const parsed = JSON.parse(stripped);
      const candidate =
        parsed?.[0]?.[2]?.[6] || parsed?.[0]?.[2]?.[0]?.[0]?.[13] || "";
      if (
        typeof candidate === "string" &&
        VISITOR_DATA_TOKEN_RE.test(candidate)
      ) {
        return candidate;
      }
    } catch {
      // Fall back to regex extraction
    }
    const match = VISITOR_DATA_TOKEN_RE.exec(rawText);
    return match ? match[0] : "";
  }

  function buildTVHTML5PlayerPayload(params) {
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
        (typeof clean.url === "string" ||
          typeof clean.signatureCipher === "string")
      ) {
        out.push(clean);
      }
    }
    return out;
  }

  function sanitizePlayerApiResponse(raw) {
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

    return { playabilityStatus, streamingData };
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

  function createUserscriptController(options) {
    const {
      gmXmlhttpRequest,
      gmDownload,
      eventTarget = typeof window !== "undefined" ? window : globalThis,
      storage = typeof localStorage !== "undefined" ? localStorage : null,
      now = () => Date.now(),
      version = YOUTUBE_HELPER_VERSION,
    } = options || {};

    let memoryVisitorCache = null;
    const activeDownloads = new Map();

    function readCachedVisitorData() {
      const current = now();
      if (memoryVisitorCache && current < memoryVisitorCache.expiresAt) {
        return memoryVisitorCache.value;
      }
      if (storage && typeof storage.getItem === "function") {
        try {
          const raw = storage.getItem(VISITOR_DATA_STORAGE_KEY);
          if (raw) {
            const parsed = JSON.parse(raw);
            if (
              parsed &&
              typeof parsed.value === "string" &&
              parsed.value &&
              typeof parsed.expiresAt === "number" &&
              current < parsed.expiresAt
            ) {
              memoryVisitorCache = parsed;
              return parsed.value;
            }
          }
        } catch {
          // Ignore storage read errors
        }
      }
      return "";
    }

    function writeCachedVisitorData(value) {
      if (!value) return;
      const entry = {
        value,
        expiresAt: now() + VISITOR_DATA_TTL_MS,
      };
      memoryVisitorCache = entry;
      if (storage && typeof storage.setItem === "function") {
        try {
          storage.setItem(VISITOR_DATA_STORAGE_KEY, JSON.stringify(entry));
        } catch {
          // Ignore storage quota errors
        }
      }
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
      const text =
        typeof resp?.responseText === "string" ? resp.responseText : "";
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

        const handle = gmDownload({
          url: url.trim(),
          name: cleanName.filename,
          saveAs: false,
          anonymous: true,
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
    }

    return { mount };
  }

  const controller = createUserscriptController({
    gmXmlhttpRequest:
      typeof GM_xmlhttpRequest === "function" ? GM_xmlhttpRequest : undefined,
    gmDownload: typeof GM_download === "function" ? GM_download : undefined,
  });
  controller.mount();
})();
