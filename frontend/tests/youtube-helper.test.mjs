import test, { after, before, describe } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createServer } from "vite";
import { createSSRApp, h, reactive, ref } from "vue";
import { renderToString } from "vue/server-renderer";

import {
  MIN_YOUTUBE_HELPER_VERSION,
  YOUTUBE_HELPER_VERSION,
  buildTVHTML5PlayerPayload,
  compareSemver,
  createUserscriptController,
  extractVisitorDataFromSwResponse,
  extractYouTubeVideoId,
  isHelperVersionSupported,
  isValidGooglevideoPlaybackUrl,
  isValidSignatureTimestamp,
  isValidTVHTML5Client,
  isValidYouTubeVideoId,
  pingYouTubeHelper,
  sanitizeHelperFilename,
  sanitizePlayerApiResponse,
  sendHelperRequest,
} from "../src/utils/youtubeHelperCore.js";

describe("YouTube helper userscript static header & validation contract", () => {
  test("static userscript file has exact required headers and single @connect", async () => {
    const scriptText = await readFile(
      new URL("../public/helper/dokitool-youtube.user.js", import.meta.url),
      "utf8",
    );
    const headerBlock = scriptText.match(
      /\/\/ ==UserScript==([\s\S]*?)\/\/ ==\/UserScript==/,
    )?.[1];
    assert.ok(headerBlock, "userscript must have a UserScript header block");

    const matches = [
      ...headerBlock.matchAll(/^\/\/ @match\s+(\S+)$/gm),
    ].map((m) => m[1]);
    assert.deepEqual(matches, ["https://snap.dokitool.com/*"]);

    const connects = [
      ...headerBlock.matchAll(/^\/\/ @connect\s+(\S+)$/gm),
    ].map((m) => m[1]);
    assert.deepEqual(
      connects,
      ["www.youtube.com"],
      "Phase 1 userscript must only connect to www.youtube.com",
    );

    const grants = [
      ...headerBlock.matchAll(/^\/\/ @grant\s+(\S+)$/gm),
    ].map((m) => m[1]);
    assert.deepEqual(grants.sort(), ["GM_download", "GM_xmlhttpRequest"]);
    assert.match(headerBlock, /^\/\/ @noframes$/m);
    assert.match(headerBlock, /^\/\/ @version\s+1\.0\.0$/m);
    assert.match(
      headerBlock,
      /^\/\/ @updateURL\s+https:\/\/snap\.dokitool\.com\/helper\/dokitool-youtube\.user\.js$/m,
    );
    assert.match(
      headerBlock,
      /^\/\/ @downloadURL\s+https:\/\/snap\.dokitool\.com\/helper\/dokitool-youtube\.user\.js$/m,
    );
  });

  test("userscript validators reject invalid video_id, non-googlevideo URLs, and invalid suffixes", () => {
    // 1. video_id validation
    assert.equal(isValidYouTubeVideoId("I9JvDaciaFk"), true);
    assert.equal(isValidYouTubeVideoId("aqz-KE-bpKQ"), true);
    for (const badId of [
      "",
      "short",
      "toolong12345",
      "bad/char123",
      "bad?char123",
      "I9JvDaciaF!",
      null,
      12345678901,
    ]) {
      assert.equal(
        isValidYouTubeVideoId(badId),
        false,
        `must reject video_id: ${badId}`,
      );
    }

    assert.equal(
      extractYouTubeVideoId("https://www.youtube.com/watch?v=I9JvDaciaFk"),
      "I9JvDaciaFk",
    );
    assert.equal(
      extractYouTubeVideoId("https://youtu.be/aqz-KE-bpKQ?si=test"),
      "aqz-KE-bpKQ",
    );
    assert.equal(
      extractYouTubeVideoId("https://www.youtube.com/shorts/dQw4w9WgXcQ"),
      "dQw4w9WgXcQ",
    );
    assert.equal(extractYouTubeVideoId("https://www.douyin.com/video/123"), "");

    // 2. signature_timestamp and TVHTML5 client validation
    assert.equal(isValidSignatureTimestamp(20760), true);
    for (const badSts of [0, -1, 999, 1000000, 20760.5, "20760", null]) {
      assert.equal(isValidSignatureTimestamp(badSts), false);
    }
    assert.equal(
      isValidTVHTML5Client({ name: "TVHTML5", version: "7.20260728.13.00" }),
      true,
    );
    assert.equal(
      isValidTVHTML5Client({ name: "TVHTML5", version: "5.20260707" }),
      true,
    );
    for (const badClient of [
      null,
      { name: "WEB", version: "2.20260728.01.00" },
      { name: "TVHTML5", version: "1.0.0" },
      { name: "TVHTML5", version: "7.20260728; evil" },
    ]) {
      assert.equal(isValidTVHTML5Client(badClient), false);
    }

    // 3. googlevideo URL validation
    assert.equal(
      isValidGooglevideoPlaybackUrl(
        "https://rr4---sn-ajoguc-5t.googlevideo.com/videoplayback?expire=1794238131&itag=299&id=o-AOdRLkc6ecTboXRtJhNBILt2v8AQeJUCoLbqEckV1dDI",
      ),
      true,
    );
    for (const badUrl of [
      "",
      "http://rr4---sn-ajoguc-5t.googlevideo.com/videoplayback?itag=299",
      "https://googlevideo.com/videoplayback?itag=299",
      "https://evil-googlevideo.com/videoplayback?itag=299",
      "https://rr4.googlevideo.com.evil.test/videoplayback?itag=299",
      "https://rr4---sn-ajoguc-5t.googlevideo.com/otherpath?itag=299",
      "https://user:pass@rr4---sn-ajoguc-5t.googlevideo.com/videoplayback?itag=299",
      "https://rr4---sn-ajoguc-5t.googlevideo.com:8443/videoplayback?itag=299",
      "https://rr4---sn-ajoguc-5t.googlevideo.com/videoplayback?itag=299#frag",
      "https://www.youtube.com/watch?v=I9JvDaciaFk",
    ]) {
      assert.equal(
        isValidGooglevideoPlaybackUrl(badUrl),
        false,
        `must reject URL: ${badUrl}`,
      );
    }

    // 4. filename suffix & sanitization validation
    assert.deepEqual(sanitizeHelperFilename("My Video: Test?.mp4"), {
      ok: true,
      filename: "My_Video_Test.mp4",
    });
    assert.deepEqual(sanitizeHelperFilename("Audio Track.M4A"), {
      ok: true,
      filename: "Audio_Track.m4a",
    });
    for (const badName of [
      "",
      "video.webm",
      "video.mkv",
      "payload.exe",
      "script.js",
      "page.html",
      "video.mp4.exe",
      "no_extension",
    ]) {
      const res = sanitizeHelperFilename(badName);
      assert.equal(res.ok, false, `must reject filename: ${badName}`);
    }
  });

  test("userscript controller enforces strict action allowlist, anonymous requests, and field stripping", async () => {
    const gmRequests = [];
    const gmDownloads = [];
    const controller = createUserscriptController({
      version: "1.0.0",
      gmXmlhttpRequest(details) {
        gmRequests.push(details);
        if (details.url === "https://www.youtube.com/sw.js_data") {
          details.onload({
            status: 200,
            responseText:
              ')]}\'\n[["yt.sw.adr",null,[[["en","US",null,"198.51.100.7",null,null,null,null,null,null,null,null,null,"Cgt0ZXN0VmlzaXRvckRhdGFTdHJpbmc%3D"]]]]]',
          });
          return;
        }
        if (
          details.url ===
          "https://www.youtube.com/youtubei/v1/player?prettyPrint=false"
        ) {
          details.onload({
            status: 200,
            responseText: JSON.stringify({
              playabilityStatus: {
                status: "OK",
                reason: "ok",
                extraSecretField: "dropped",
              },
              videoDetails: { title: "secret-dropped" },
              streamingData: {
                expiresInSeconds: "21540",
                formats: [
                  {
                    itag: 18,
                    url: "https://rr1---sn-test.googlevideo.com/videoplayback?itag=18",
                    mimeType: 'video/mp4; codecs="avc1.42001E, mp4a.40.2"',
                    width: 640,
                    height: 360,
                    contentLength: "144433",
                    unexpectedField: "must_be_removed",
                  },
                ],
                adaptiveFormats: [],
              },
            }),
          });
          return;
        }
        details.onerror();
      },
      gmDownload(details) {
        gmDownloads.push(details);
        details.onload();
      },
    });

    // Reject unknown actions (cannot be used as a generic proxy)
    const unknownResp = await controller.handleRequestObject({
      requestId: "req-unknown",
      action: "generic_fetch",
      payload: { url: "https://www.youtube.com/account" },
    });
    assert.equal(unknownResp.ok, false);
    assert.equal(unknownResp.error.code, "UNSUPPORTED_ACTION");

    // Reject invalid player parameters
    const badPlayerResp = await controller.handleRequestObject({
      requestId: "req-bad-player",
      action: "player",
      payload: {
        video_id: "invalid",
        signature_timestamp: 20760,
        client: { name: "TVHTML5", version: "7.20260728.13.00" },
      },
    });
    assert.equal(badPlayerResp.ok, false);

    // Valid player request: fetches visitorData with anonymous:true, calls player with anonymous:true, strips extra fields
    const okPlayerResp = await controller.handleRequestObject({
      requestId: "req-ok-player",
      action: "player",
      payload: {
        video_id: "I9JvDaciaFk",
        signature_timestamp: 20760,
        client: { name: "TVHTML5", version: "7.20260728.13.00" },
      },
    });
    assert.equal(okPlayerResp.ok, true);
    assert.equal(gmRequests.length, 2);
    assert.equal(gmRequests[0].anonymous, true);
    assert.equal(gmRequests[1].anonymous, true);
    assert.equal(
      gmRequests[1].headers["X-Goog-Visitor-Id"],
      "Cgt0ZXN0VmlzaXRvckRhdGFTdHJpbmc%3D",
    );
    assert.equal(okPlayerResp.data.videoDetails, undefined);
    assert.equal(
      okPlayerResp.data.playabilityStatus.extraSecretField,
      undefined,
    );
    assert.equal(
      okPlayerResp.data.streamingData.formats[0].unexpectedField,
      undefined,
    );

    // Reject download of non-googlevideo URL or disallowed suffix
    const badDlUrl = await controller.handleRequestObject({
      requestId: "req-bad-dl-url",
      action: "download",
      payload: {
        url: "https://www.youtube.com/watch?v=I9JvDaciaFk",
        filename: "video.mp4",
      },
    });
    assert.equal(badDlUrl.ok, false);

    const badDlExt = await controller.handleRequestObject({
      requestId: "req-bad-dl-ext",
      action: "download",
      payload: {
        url: "https://rr1---sn-test.googlevideo.com/videoplayback?itag=248",
        filename: "video.webm",
      },
    });
    assert.equal(badDlExt.ok, false);
    assert.equal(gmDownloads.length, 0);

    // Valid download
    const okDl = await controller.handleRequestObject({
      requestId: "req-ok-dl",
      action: "download",
      payload: {
        url: "https://rr1---sn-test.googlevideo.com/videoplayback?itag=299",
        filename: "Sample 1080p.mp4",
      },
    });
    assert.equal(okDl.ok, true);
    assert.equal(okDl.data.filename, "Sample_1080p.mp4");
    assert.equal(gmDownloads.length, 1);
  });

  test("CustomEvent handshake and semver version check", async () => {
    assert.equal(compareSemver("1.0.0", "1.0.0"), 0);
    assert.equal(compareSemver("1.1.0", "1.0.0"), 1);
    assert.equal(compareSemver("0.9.9", "1.0.0"), -1);
    assert.equal(isHelperVersionSupported("0.9.9", "1.0.0"), false);
    assert.equal(isHelperVersionSupported("1.0.0", "1.0.0"), true);

    const target = new EventTarget();
    // 1. No userscript mounted -> installed: false
    const missingPing = await pingYouTubeHelper({
      eventTarget: target,
      timeoutMs: 30,
    });
    assert.deepEqual(missingPing, {
      installed: false,
      version: null,
      supported: false,
    });

    // 2. Outdated userscript mounted -> installed: true, supported: false
    const unmountOld = createUserscriptController({
      eventTarget: target,
      version: "0.9.0",
    }).mount();
    const oldPing = await pingYouTubeHelper({
      eventTarget: target,
      timeoutMs: 50,
    });
    assert.deepEqual(oldPing, {
      installed: true,
      version: "0.9.0",
      supported: false,
    });
    unmountOld();

    // 3. Current userscript mounted -> installed: true, supported: true
    const unmountCurrent = createUserscriptController({
      eventTarget: target,
      version: YOUTUBE_HELPER_VERSION,
    }).mount();
    const readyPing = await pingYouTubeHelper({
      eventTarget: target,
      timeoutMs: 50,
    });
    assert.deepEqual(readyPing, {
      installed: true,
      version: YOUTUBE_HELPER_VERSION,
      supported: true,
    });
    unmountCurrent();
  });
});

describe("YouTube frontend parser, ResultSection UI states, and download flows", () => {
  let vite, parser, selectionMod, downloadMod, ResultSection;
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const originalDocument = globalThis.document;
  const originalNavigator = globalThis.navigator;

  const itag18Url =
    "https://rr4---sn-ajoguc-5t.googlevideo.com/videoplayback?expire=1794238131&ip=198.51.100.7&id=o-AOdRLkc6ecTboXRtJhNBILt2v8AQeJUCoLbqEckV1dDI&itag=18&source=youtube&c=TVHTML5&n=5GuFTFbCJY0P_g&sparams=expire%2Cip%2Cid%2Citag%2Csource&sig=AAA&title=Sample_360p";
  const itag299Url =
    "https://rr4---sn-ajoguc-5t.googlevideo.com/videoplayback?expire=1794238131&ip=198.51.100.7&id=o-AOdRLkc6ecTboXRtJhNBILt2v8AQeJUCoLbqEckV1dDI&itag=299&source=youtube&gir=yes&clen=488502&c=TVHTML5&n=F7GraLOqBD6ahA&sparams=expire%2Cip%2Cid%2Citag%2Csource%2Cgir%2Cclen&sig=BBB";
  const itag140Url =
    "https://rr4---sn-ajoguc-5t.googlevideo.com/videoplayback?expire=1794238131&ip=198.51.100.7&id=o-AOdRLkc6ecTboXRtJhNBILt2v8AQeJUCoLbqEckV1dDI&itag=140&source=youtube&gir=yes&clen=82734&c=TVHTML5&n=F7GraLOqBD6ahA&sparams=expire%2Cip%2Cid%2Citag%2Csource%2Cgir%2Cclen&sig=CCC";

  const sampleYouTubeUnifiedData = () => ({
    platform: "youtube",
    content_id: "I9JvDaciaFk",
    source_url: "https://www.youtube.com/watch?v=I9JvDaciaFk",
    generated_at: "2026-11-09T00:00:00Z",
    title: "Sample YouTube Video",
    author: { name: "Sample Channel", id: "channel-1" },
    default_item_id: "video-I9JvDaciaFk",
    items: [
      {
        id: "video-I9JvDaciaFk",
        type: "video",
        state: "ready",
        title: "Sample YouTube Video",
        default: {
          variant_id: "v-18",
          source_id: "s-18",
          url: itag18Url,
          access: "direct",
        },
        variants: [
          {
            id: "v-299",
            label: "1080p60 (仅视频, 无音频)",
            provenance: "original_endpoint",
            metadata: {
              width: 1920,
              height: 1080,
              fps: 60,
              has_audio: false,
              container: "mp4",
              extension: "mp4",
              mime_type: "video/mp4",
              evidence: "upstream",
            },
            delivery: {
              kind: "file",
              sources: [
                {
                  id: "s-299",
                  url: itag299Url,
                  access: "direct",
                  size_bytes: 488502,
                },
              ],
            },
          },
          {
            id: "v-18",
            label: "360p (含音频)",
            provenance: "original_endpoint",
            metadata: {
              width: 640,
              height: 360,
              fps: 30,
              has_audio: true,
              container: "mp4",
              extension: "mp4",
              mime_type: "video/mp4",
              evidence: "upstream",
            },
            delivery: {
              kind: "file",
              sources: [
                {
                  id: "s-18",
                  url: itag18Url,
                  access: "direct",
                  size_bytes: 144433,
                },
              ],
            },
          },
        ],
      },
    ],
    attachments: [
      {
        id: "att-m4a",
        role: "background_audio",
        title: "Sample YouTube Video (M4A)",
        resource: {
          id: "audio-140",
          role: "audio",
          metadata: {
            has_audio: true,
            container: "m4a",
            extension: "m4a",
            mime_type: "audio/mp4",
            evidence: "upstream",
          },
          delivery: {
            kind: "file",
            sources: [
              {
                id: "s-140",
                url: itag140Url,
                access: "direct",
                size_bytes: 82734,
              },
            ],
          },
        },
      },
    ],
  });

  before(async () => {
    vite = await createServer({
      server: { middlewareMode: true, hmr: false, ws: false, watch: null },
      appType: "custom",
    });
    parser = await vite.ssrLoadModule("/src/composables/useVideoParser.ts");
    selectionMod = await vite.ssrLoadModule(
      "/src/composables/useVideoSelection.ts",
    );
    downloadMod = await vite.ssrLoadModule(
      "/src/composables/useMediaDownload.ts",
    );
    ResultSection = (
      await vite.ssrLoadModule("/src/components/ResultSection.vue")
    ).default;
    (
      await vite.ssrLoadModule("/src/services/operationLogger.js")
    ).operationLogger.enableConsole = false;
  });

  after(async () => {
    globalThis.fetch = originalFetch;
    globalThis.window = originalWindow;
    globalThis.document = originalDocument;
    Object.defineProperty(globalThis, "navigator", {
      configurable: true,
      value: originalNavigator,
    });
    await vite?.close();
  });

  function setupEnvironment() {
    const bus = new EventTarget();
    const clickedAnchors = [];
    globalThis.window = {
      setTimeout,
      clearTimeout,
      CustomEvent: globalThis.CustomEvent,
      addEventListener: bus.addEventListener.bind(bus),
      removeEventListener: bus.removeEventListener.bind(bus),
      dispatchEvent: bus.dispatchEvent.bind(bus),
      open: () => ({}),
    };
    globalThis.document = {
      body: {
        style: {},
        appendChild() {},
      },
      createElement(tag) {
        return {
          tag,
          href: "",
          download: "",
          target: "",
          rel: "",
          referrerPolicy: "",
          addEventListener() {},
          removeEventListener() {},
          click() {
            clickedAnchors.push({
              href: this.href,
              download: this.download,
              target: this.target,
            });
          },
          remove() {},
        };
      },
    };
    Object.defineProperty(globalThis, "navigator", {
      configurable: true,
      value: { userActivation: { isActive: true } },
    });

    const store = reactive({
      resultData: null,
      resultVersion: 0,
      downloads: [],
      resolvingItems: new Set(),
      clearResult() {
        this.resultData = null;
        this.resultVersion++;
      },
      setResult(data) {
        this.resultData = data;
        this.resultVersion++;
      },
      addDownload(input) {
        const id = this.downloads.length + 1;
        this.downloads.push({
          id,
          status: "preparing",
          percent: 0,
          loaded: 0,
          total: 0,
          speed: 0,
          error: null,
          statusText: "准备中...",
          show: true,
          abortController: null,
          ...input,
        });
        return id;
      },
      updateDownload(id, updates) {
        const idx = this.downloads.findIndex((d) => d.id === id);
        if (idx !== -1) {
          this.downloads[idx] = { ...this.downloads[idx], ...updates };
        }
      },
      removeDownload(id) {
        const idx = this.downloads.findIndex((d) => d.id === id);
        if (idx !== -1) this.downloads.splice(idx, 1);
      },
      cancelDownload(id) {
        const idx = this.downloads.findIndex((d) => d.id === id);
        if (idx !== -1) {
          this.downloads[idx].abortController?.abort();
          this.downloads[idx].status = "cancelled";
        }
      },
    });
    const toasts = [];
    const showToast = (msg, type = "success") => toasts.push([msg, type]);
    const parserInstance = parser.useVideoParser({
      videoStore: store,
      locale: ref("zh-CN"),
      showToast,
    });
    const selection = selectionMod.useVideoSelection({ videoStore: store });
    const downloader = downloadMod.useMediaDownload({
      videoStore: store,
      showToast,
      selection,
      resolveItem: parserInstance.resolveItem,
    });
    return {
      bus,
      clickedAnchors,
      store,
      toasts,
      parserInstance,
      selection,
      downloader,
    };
  }

  async function renderResultHtml(store, selection) {
    selection.syncResult();
    const app = createSSRApp({
      render: () =>
        h(ResultSection, {
          resultData: store.resultData,
          resultVersion: store.resultVersion,
          locale: "zh-CN",
          selection,
          isDownloading: false,
          resolvingIds: new Set(),
          formatNumber: String,
          formatDuration: String,
        }),
    });
    return renderToString(app);
  }

  test("without helper or with outdated helper: shows parse result + install guide (with Chrome 允许用户脚本), strips server-IP googlevideo links, and refuses download without 已交给浏览器", async () => {
    const env = setupEnvironment();
    globalThis.fetch = async (url) => {
      const u = new URL(url, "https://snap.dokitool.com");
      assert.equal(u.pathname, "/api/parse");
      return Response.json({
        schema_version: "1.0",
        code: "OK",
        data: sampleYouTubeUnifiedData(),
      });
    };

    env.parserInstance.inputUrl.value =
      "https://www.youtube.com/watch?v=I9JvDaciaFk";
    await env.parserInstance.parseVideo();

    assert.ok(env.store.resultData);
    assert.equal(env.store.resultData.extra.youtube_helper.status, "missing");
    // Ensure server-IP-bound googlevideo URLs are completely stripped from resultData
    assert.doesNotMatch(
      JSON.stringify(env.store.resultData),
      /googlevideo\.com/,
    );

    const html = await renderResultHtml(env.store, env.selection);
    assert.ok(html.includes("需要安装 YouTube 下载助手脚本"));
    assert.ok(html.includes("允许用户脚本"));
    assert.ok(html.includes("/helper/dokitool-youtube.user.js"));
    assert.ok(!html.includes("googlevideo.com"));

    // Attempting to download must not open any tab or claim 已交给浏览器
    await env.downloader.downloadMainVideo();
    assert.equal(env.store.downloads.length, 0);
    assert.equal(env.clickedAnchors.length, 0);
    assert.ok(
      !env.toasts.some(([msg]) => msg.includes("已交给浏览器")),
      "must never show 已交给浏览器 when helper is missing",
    );

    // Outdated helper version (0.9.0 < 1.0.0) -> status: 'outdated', prompts user to update
    const unmountOld = createUserscriptController({
      eventTarget: env.bus,
      version: "0.9.0",
    }).mount();
    try {
      env.parserInstance.inputUrl.value =
        "https://www.youtube.com/watch?v=aqz-KE-bpKQ";
      await env.parserInstance.parseVideo();
      assert.equal(env.store.resultData.extra.youtube_helper.status, "outdated");
      const oldHtml = await renderResultHtml(env.store, env.selection);
      assert.ok(oldHtml.includes("YouTube 下载助手脚本需要更新"));
      assert.ok(oldHtml.includes("更新 YouTube 助手脚本"));
      assert.ok(!oldHtml.includes("googlevideo.com"));
    } finally {
      unmountOld();
    }
  });

  test("with helper returning LOGIN_REQUIRED: prompts to switch network exit and never asks for login or cookies", async () => {
    const env = setupEnvironment();
    const unmount = createUserscriptController({
      eventTarget: env.bus,
      version: "1.0.0",
      gmXmlhttpRequest(details) {
        if (details.url.includes("sw.js_data")) {
          details.onload({
            status: 200,
            responseText: '["Cgt0ZXN0VmlzaXRvckRhdGFTdHJpbmc%3D"]',
          });
          return;
        }
        details.onload({
          status: 200,
          responseText: JSON.stringify({
            playabilityStatus: {
              status: "LOGIN_REQUIRED",
              reason: "Sign in to confirm you’re not a bot",
            },
          }),
        });
      },
    }).mount();

    try {
      globalThis.fetch = async (url) => {
        const u = new URL(url, "https://snap.dokitool.com");
        assert.equal(u.pathname, "/api/youtube/context");
        return Response.json({
          schema_version: "1.0",
          code: "OK",
          data: {
            player_id: "22f02d3d",
            signature_timestamp: 20760,
            client: { name: "TVHTML5", version: "7.20260728.13.00" },
            expires_at: "2026-11-09T06:00:00Z",
          },
        });
      };

      env.parserInstance.inputUrl.value =
        "https://www.youtube.com/watch?v=I9JvDaciaFk";
      await env.parserInstance.parseVideo();

      assert.equal(
        env.parserInstance.parseError.value,
        "当前网络出口被 YouTube 要求验证，请更换节点后重试",
      );
      assert.equal(
        env.store.resultData.extra.youtube_helper.status,
        "login_required",
      );

      const html = await renderResultHtml(env.store, env.selection);
      assert.ok(
        html.includes("当前网络出口被 YouTube 要求验证，请更换节点后重试"),
      );
      assert.ok(
        html.includes("不提供也不要求登录 YouTube 账号或导入 Cookie"),
      );
    } finally {
      unmount();
    }
  });

  test("with helper ready: fetches context -> helper player -> finalize, shows 2x bitrate notice, uses _blank for itag=18 and GM_download for 1080p video-only and M4A", async () => {
    const env = setupEnvironment();
    const gmSaved = [];
    const unmount = createUserscriptController({
      eventTarget: env.bus,
      version: "1.0.0",
      gmXmlhttpRequest(details) {
        if (details.url.includes("sw.js_data")) {
          details.onload({
            status: 200,
            responseText: '["Cgt0ZXN0VmlzaXRvckRhdGFTdHJpbmc%3D"]',
          });
          return;
        }
        details.onload({
          status: 200,
          responseText: JSON.stringify({
            playabilityStatus: { status: "OK" },
            streamingData: {
              formats: [
                {
                  itag: 18,
                  url: itag18Url,
                  mimeType: 'video/mp4; codecs="avc1.42001E, mp4a.40.2"',
                  width: 640,
                  height: 360,
                  contentLength: "144433",
                },
              ],
              adaptiveFormats: [
                {
                  itag: 299,
                  url: itag299Url,
                  mimeType: 'video/mp4; codecs="avc1.64002a"',
                  width: 1920,
                  height: 1080,
                  fps: 60,
                  contentLength: "488502",
                },
                {
                  itag: 140,
                  url: itag140Url,
                  mimeType: 'audio/mp4; codecs="mp4a.40.2"',
                  contentLength: "82734",
                },
              ],
            },
          }),
        });
      },
      gmDownload(details) {
        gmSaved.push({ url: details.url, name: details.name });
        details.onprogress?.({ loaded: 50000, total: 100000 });
        details.onload();
      },
    }).mount();

    try {
      const apiCalls = [];
      globalThis.fetch = async (url, init) => {
        const u = new URL(url, "https://snap.dokitool.com");
        apiCalls.push(u.pathname);
        if (u.pathname === "/api/youtube/context") {
          return Response.json({
            schema_version: "1.0",
            code: "OK",
            data: {
              player_id: "22f02d3d",
              signature_timestamp: 20760,
              client: { name: "TVHTML5", version: "7.20260728.13.00" },
              expires_at: "2026-11-09T06:00:00Z",
            },
          });
        }
        if (u.pathname === "/api/youtube/finalize") {
          const body = JSON.parse(init.body);
          assert.equal(body.video_id, "I9JvDaciaFk");
          assert.equal(body.player_id, "22f02d3d");
          assert.equal(body.formats.length, 1);
          assert.equal(body.adaptiveFormats.length, 2);
          return Response.json({
            schema_version: "1.0",
            code: "OK",
            data: sampleYouTubeUnifiedData(),
          });
        }
        throw new Error(`unexpected fetch: ${u.pathname}`);
      };

      env.parserInstance.inputUrl.value =
        "https://www.youtube.com/watch?v=I9JvDaciaFk";
      await env.parserInstance.parseVideo();

      assert.deepEqual(apiCalls, [
        "/api/youtube/context",
        "/api/youtube/finalize",
      ]);
      assert.equal(env.store.resultData.extra.youtube_helper.status, "ready");

      const html = await renderResultHtml(env.store, env.selection);
      assert.ok(html.includes("受 2 倍码率限速，预计耗时较长"));

      // 1. Default 360p (itag=18) -> uses _blank browser native handoff
      await env.downloader.downloadMainVideo();
      assert.equal(env.store.downloads.length, 1);
      assert.equal(env.store.downloads[0].status, "handed-off");
      assert.equal(env.store.downloads[0].transport, "browser_native");
      assert.equal(env.clickedAnchors.length, 1);
      assert.equal(env.clickedAnchors[0].href, itag18Url);
      assert.equal(env.clickedAnchors[0].target, "_blank");
      assert.equal(gmSaved.length, 0);

      // 2. 1080p video-only (itag=299) -> uses userscript GM_download
      env.selection.choose({ variant_id: "v-299", source_id: "s-299" });
      await env.downloader.downloadMainVideo();
      await new Promise((r) => setTimeout(r, 20));
      assert.equal(env.store.downloads.length, 2);
      assert.equal(env.store.downloads[1].status, "completed");
      assert.equal(gmSaved.length, 1);
      assert.equal(gmSaved[0].url, itag299Url);
      assert.match(gmSaved[0].name, /\.mp4$/);

      // 3. M4A audio attachment (itag=140) -> uses userscript GM_download
      env.downloader.downloadMusic(env.store.resultData.attachments[0]);
      await new Promise((r) => setTimeout(r, 20));
      assert.equal(env.store.downloads.length, 3);
      assert.equal(env.store.downloads[2].status, "completed");
      assert.equal(gmSaved.length, 2);
      assert.equal(gmSaved[1].url, itag140Url);
      assert.match(gmSaved[1].name, /\.m4a$/);
    } finally {
      unmount();
    }
  });
});
