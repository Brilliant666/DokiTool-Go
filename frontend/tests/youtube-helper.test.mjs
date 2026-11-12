import test, { after, before, describe } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import vm from "node:vm";
import { createServer } from "vite";
import { createSSRApp, h, reactive, ref } from "vue";
import { renderToString } from "vue/server-renderer";

import {
  MIN_YOUTUBE_HELPER_VERSION,
  OPFS_QUOTA_HEADROOM_FACTOR,
  YOUTUBE_FILE_PICKER_ID,
  YOUTUBE_HELPER_VERSION,
  YOUTUBE_OPFS_TEMP_PREFIX,
  buildTVHTML5PlayerPayload,
  cleanupLeftoverYouTubeOpfsTempFiles,
  compareSemver,
  createUserscriptController,
  extractUrlClen,
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
  sanitizeVideoDetails,
  selectYouTubeMuxPair,
  sendHelperRequest,
} from "../src/utils/youtubeHelperCore.js";
import { generateYouTubeHelperCode } from "../scripts/build-youtube-helper.mjs";
import * as mediabunny from "mediabunny";

function createMockOpfsEnvironment({
  quota = 500_000_000,
  usage = 0,
  initialFiles = {},
} = {}) {
  const files = new Map();
  const activeLocks = new Set();
  for (const [name, content] of Object.entries(initialFiles)) {
    files.set(
      name,
      content instanceof Uint8Array
        ? content
        : new TextEncoder().encode(String(content)),
    );
  }

  function currentUsageBytes() {
    let sum = usage;
    for (const data of files.values()) {
      sum += data.byteLength;
    }
    return sum;
  }

  const rootDir = {
    async *entries() {
      for (const name of [...files.keys()]) {
        yield [name, { kind: "file", name }];
      }
    },
    async getFileHandle(name, options = {}) {
      if (!files.has(name)) {
        if (!options?.create) {
          throw new DOMException("File not found", "NotFoundError");
        }
        files.set(name, new Uint8Array(0));
      }
      return {
        kind: "file",
        name,
        async createWritable() {
          if (activeLocks.has(name)) {
            throw new DOMException("File already locked", "NoModificationAllowedError");
          }
          activeLocks.add(name);
          const chunks = [];
          let cursor = 0;
          let closed = false;
          return {
            async write(chunk) {
              if (closed) throw new Error("Writable already closed");
              if (chunk && typeof chunk === "object" && chunk.type === "write") {
                if (typeof chunk.position === "number") {
                  cursor = chunk.position;
                }
                const bytes =
                  chunk.data instanceof Uint8Array
                    ? chunk.data
                    : new Uint8Array(chunk.data);
                chunks.push({ pos: cursor, bytes: bytes.slice() });
                cursor += bytes.byteLength;
                return;
              }
              const bytes =
                chunk instanceof Uint8Array ? chunk : new Uint8Array(chunk);
              chunks.push({ pos: cursor, bytes: bytes.slice() });
              cursor += bytes.byteLength;
            },
            async seek(pos) {
              cursor = Number(pos) || 0;
            },
            async close() {
              if (closed) return;
              closed = true;
              activeLocks.delete(name);
              let maxLen = 0;
              for (const c of chunks) {
                maxLen = Math.max(maxLen, c.pos + c.bytes.byteLength);
              }
              const out = new Uint8Array(maxLen);
              for (const c of chunks) {
                out.set(c.bytes, c.pos);
              }
              files.set(name, out);
            },
            async abort() {
              closed = true;
              activeLocks.delete(name);
            },
          };
        },
        async getFile() {
          if (!files.has(name)) {
            throw new DOMException("File not found", "NotFoundError");
          }
          const bytes = files.get(name);
          const blob = new Blob([bytes]);
          Object.defineProperty(blob, "name", { value: name });
          return blob;
        },
      };
    },
    async removeEntry(name) {
      if (activeLocks.has(name)) {
        throw new DOMException("File is locked", "InvalidModificationError");
      }
      if (!files.has(name)) {
        throw new DOMException("File not found", "NotFoundError");
      }
      files.delete(name);
    },
  };

  return {
    files,
    activeLocks,
    storage: {
      async estimate() {
        return { quota, usage: currentUsageBytes() };
      },
      async getDirectory() {
        return rootDir;
      },
    },
  };
}

async function createSyntheticMuxFixtures() {
  const avcC = new Uint8Array([
    1, 0x42, 0xc0, 0x1e, 0xff, 0xe1, 0x00, 0x09, 0x67, 0x42, 0xc0, 0x1e, 0xd9,
    0x03, 0xc8, 0x40, 0x00, 0x01, 0x00, 0x04, 0x68, 0xcb, 0x83, 0xcb,
  ]);
  const mp4VideoTarget = new mediabunny.BufferTarget();
  const mp4VideoOut = new mediabunny.Output({
    format: new mediabunny.Mp4OutputFormat({ fastStart: "fragmented" }),
    target: mp4VideoTarget,
  });
  const mp4VideoSrc = new mediabunny.EncodedVideoPacketSource("avc");
  mp4VideoOut.addVideoTrack(mp4VideoSrc);
  await mp4VideoOut.start();
  for (let i = 0; i < 6; i++) {
    const nal = new Uint8Array(2048);
    new DataView(nal.buffer).setUint32(0, 2044);
    nal[4] = i === 0 ? 0x65 : 0x41;
    await mp4VideoSrc.add(
      new mediabunny.EncodedPacket(
        nal,
        i === 0 ? "key" : "delta",
        i * 0.04,
        0.04,
      ),
      i === 0
        ? {
            decoderConfig: {
              codec: "avc1.42c01e",
              codedWidth: 640,
              codedHeight: 360,
              description: avcC,
            },
          }
        : undefined,
    );
  }
  mp4VideoSrc.close();
  await mp4VideoOut.finalize();

  const m4aAudioTarget = new mediabunny.BufferTarget();
  const m4aAudioOut = new mediabunny.Output({
    format: new mediabunny.Mp4OutputFormat({ fastStart: "fragmented" }),
    target: m4aAudioTarget,
  });
  const m4aAudioSrc = new mediabunny.EncodedAudioPacketSource("aac");
  m4aAudioOut.addAudioTrack(m4aAudioSrc);
  await m4aAudioOut.start();
  for (let i = 0; i < 6; i++) {
    const frame = new Uint8Array(1024);
    frame[0] = 0xde;
    await m4aAudioSrc.add(
      new mediabunny.EncodedPacket(frame, "key", i * 0.04, 0.04),
      i === 0
        ? {
            decoderConfig: {
              codec: "mp4a.40.2",
              numberOfChannels: 2,
              sampleRate: 48000,
              description: new Uint8Array([0x11, 0x90]),
            },
          }
        : undefined,
    );
  }
  m4aAudioSrc.close();
  await m4aAudioOut.finalize();

  const webmVideoTarget = new mediabunny.BufferTarget();
  const webmVideoOut = new mediabunny.Output({
    format: new mediabunny.WebMOutputFormat(),
    target: webmVideoTarget,
  });
  const webmVideoSrc = new mediabunny.EncodedVideoPacketSource("vp9");
  webmVideoOut.addVideoTrack(webmVideoSrc);
  await webmVideoOut.start();
  for (let i = 0; i < 5; i++) {
    const pkt = new Uint8Array(2048);
    pkt[0] = i === 0 ? 0x82 : 0x86;
    await webmVideoSrc.add(
      new mediabunny.EncodedPacket(
        pkt,
        i === 0 ? "key" : "delta",
        i * 0.04,
        0.04,
      ),
      i === 0
        ? {
            decoderConfig: {
              codec: "vp09.00.10.08",
              codedWidth: 1280,
              codedHeight: 720,
            },
          }
        : undefined,
    );
  }
  webmVideoSrc.close();
  await webmVideoOut.finalize();

  const webmAudioTarget = new mediabunny.BufferTarget();
  const webmAudioOut = new mediabunny.Output({
    format: new mediabunny.WebMOutputFormat(),
    target: webmAudioTarget,
  });
  const webmAudioSrc = new mediabunny.EncodedAudioPacketSource("opus");
  webmAudioOut.addAudioTrack(webmAudioSrc);
  await webmAudioOut.start();
  const opusHead = new Uint8Array([
    0x4f, 0x70, 0x75, 0x73, 0x48, 0x65, 0x61, 0x64, 1, 2, 0x38, 0x01, 0x80,
    0xbb, 0x00, 0x00, 0, 0, 0,
  ]);
  for (let i = 0; i < 5; i++) {
    const pkt = new Uint8Array(512);
    pkt[0] = 0xf8;
    await webmAudioSrc.add(
      new mediabunny.EncodedPacket(pkt, "key", i * 0.02, 0.02),
      i === 0
        ? {
            decoderConfig: {
              codec: "opus",
              numberOfChannels: 2,
              sampleRate: 48000,
              description: opusHead,
            },
          }
        : undefined,
    );
  }
  webmAudioSrc.close();
  await webmAudioOut.finalize();

  return {
    mp4VideoBytes: new Uint8Array(mp4VideoTarget.buffer),
    m4aAudioBytes: new Uint8Array(m4aAudioTarget.buffer),
    webmVideoBytes: new Uint8Array(webmVideoTarget.buffer),
    webmAudioBytes: new Uint8Array(webmAudioTarget.buffer),
  };
}

function sliceRangeFromFixture(bytes, rawUrl) {
  const u = new URL(rawUrl);
  const rangeStr = u.searchParams.get("range") || `0-${bytes.byteLength - 1}`;
  const [startStr, endStr] = rangeStr.split("-");
  const start = Number(startStr);
  const end = Math.min(bytes.byteLength - 1, Number(endStr));
  const slice = bytes.slice(start, end + 1);
  return {
    status: 200,
    responseHeaders: `content-range: bytes ${start}-${end}/${bytes.byteLength}\r\n`,
    response: slice.buffer.slice(
      slice.byteOffset,
      slice.byteOffset + slice.byteLength,
    ),
  };
}

describe("YouTube helper userscript static header & validation contract", () => {
  test("static userscript file has exact v1.3.0 headers, strict @connect allowlist, MPL-2.0 notice, and build parity", async () => {
    const scriptText = await readFile(
      new URL("../public/helper/dokitool-youtube.user.js", import.meta.url),
      "utf8",
    );
    const rebuiltText = await generateYouTubeHelperCode();
    assert.equal(
      scriptText,
      rebuiltText,
      "frontend/public/helper/dokitool-youtube.user.js must match buildYouTubeHelper() output byte-for-byte",
    );

    const mplLicense = await readFile(
      new URL("../public/licenses/mediabunny-MPL-2.0.txt", import.meta.url),
      "utf8",
    );
    assert.ok(
      mplLicense.includes("Mozilla Public License Version 2.0"),
      "MPL-2.0 license text for Mediabunny must be present in public/licenses",
    );
    assert.ok(scriptText.includes("Mediabunny"));
    assert.ok(scriptText.includes("MPL-2.0"));

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
      ["www.youtube.com", "googlevideo.com"],
      "Userscript must only declare www.youtube.com and googlevideo.com",
    );

    const grants = [
      ...headerBlock.matchAll(/^\/\/ @grant\s+(\S+)$/gm),
    ].map((m) => m[1]);
    assert.deepEqual(grants.sort(), ["GM_download", "GM_xmlhttpRequest"]);
    assert.match(headerBlock, /^\/\/ @noframes$/m);
    assert.match(headerBlock, /^\/\/ @version\s+1\.3\.0$/m);
    assert.equal(YOUTUBE_HELPER_VERSION, "1.3.0");
    assert.equal(MIN_YOUTUBE_HELPER_VERSION, "1.3.0");
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

    // 4. filename suffix & sanitization validation (.mp4, .m4a, .webm allowed)
    assert.deepEqual(sanitizeHelperFilename("My Video: Test?.mp4"), {
      ok: true,
      filename: "My_Video_Test.mp4",
    });
    assert.deepEqual(sanitizeHelperFilename("Audio Track.M4A"), {
      ok: true,
      filename: "Audio_Track.m4a",
    });
    assert.deepEqual(sanitizeHelperFilename("4K VP9 Clip.WEBM"), {
      ok: true,
      filename: "4K_VP9_Clip.webm",
    });
    assert.equal(
      extractUrlClen(
        "https://rr1---sn-test.googlevideo.com/videoplayback?itag=299&clen=488502",
      ),
      488502,
    );
    assert.equal(extractUrlClen("https://rr1---sn-test.googlevideo.com/videoplayback?itag=18"), 0);
    for (const badName of [
      "",
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

  test("userscript controller enforces strict action allowlist, anonymous requests, memory-only visitorData cache, and field stripping", async () => {
    const gmRequests = [];
    const gmDownloads = [];
    const savedBuffers = [];
    const sampleTrackBytes = new Uint8Array(2048);
    for (let i = 0; i < sampleTrackBytes.length; i++) sampleTrackBytes[i] = i & 0xff;
    const controller = createUserscriptController({
      version: "1.3.0",
      pageWindow: {},
      saveBuffer(buf, filename, mimeType) {
        savedBuffers.push({ byteLength: buf.byteLength, filename, mimeType });
      },
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
              videoDetails: {
                title: "  Me at\u0000 the zoo  ",
                author: "jawed",
                lengthSeconds: "19",
                thumbnail: {
                  thumbnails: [
                    { url: "https://evil.example.com/thumb.jpg" },
                    { url: "https://i.ytimg.com/vi/jNQXAC9IVRw/hqdefault.jpg" },
                  ],
                },
                secretToken: "dropped",
              },
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
        if (details.url.startsWith("https://rr1---sn-test.googlevideo.com/videoplayback")) {
          details.onload(sliceRangeFromFixture(sampleTrackBytes, details.url));
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
    assert.deepEqual(okPlayerResp.data.videoDetails, {
      title: "Me at the zoo",
      author: "jawed",
      lengthSeconds: 19,
      thumbnail: "https://i.ytimg.com/vi/jNQXAC9IVRw/hqdefault.jpg",
    });
    assert.equal(
      okPlayerResp.data.playabilityStatus.extraSecretField,
      undefined,
    );
    assert.equal(
      okPlayerResp.data.streamingData.formats[0].unexpectedField,
      undefined,
    );

    // Second player request reuses in-memory visitorData without calling sw.js_data again
    const okPlayerResp2 = await controller.handleRequestObject({
      requestId: "req-ok-player-2",
      action: "player",
      payload: {
        video_id: "I9JvDaciaFk",
        signature_timestamp: 20760,
        client: { name: "TVHTML5", version: "7.20260728.13.00" },
      },
    });
    assert.equal(okPlayerResp2.ok, true);
    assert.equal(gmRequests.length, 3);
    assert.equal(
      gmRequests[2].url,
      "https://www.youtube.com/youtubei/v1/player?prettyPrint=false",
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
        filename: "video.mkv",
      },
    });
    assert.equal(badDlExt.ok, false);
    assert.equal(gmDownloads.length, 0);
    assert.equal(savedBuffers.length, 0);

    // Valid MP4, M4A, and WebM downloads use chunked anonymous GM_xmlhttpRequest (not single-connection GM_download)
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
    assert.equal(okDl.data.mode, "memory_buffer");
    assert.equal(savedBuffers.length, 1);
    assert.equal(savedBuffers[0].filename, "Sample_1080p.mp4");
    assert.equal(savedBuffers[0].byteLength, sampleTrackBytes.byteLength);

    const okM4aDl = await controller.handleRequestObject({
      requestId: "req-ok-m4a-dl",
      action: "download",
      payload: {
        url: "https://rr1---sn-test.googlevideo.com/videoplayback?itag=140",
        filename: "Sample_audio.m4a",
      },
    });
    assert.equal(okM4aDl.ok, true);
    assert.equal(okM4aDl.data.filename, "Sample_audio.m4a");
    assert.equal(okM4aDl.data.mode, "memory_buffer");
    assert.equal(savedBuffers[1].mimeType, "audio/mp4");

    const okWebmDl = await controller.handleRequestObject({
      requestId: "req-ok-webm-dl",
      action: "download",
      payload: {
        url: "https://rr1---sn-test.googlevideo.com/videoplayback?itag=315",
        filename: "Sample_2160p.webm",
      },
    });
    assert.equal(okWebmDl.ok, true);
    assert.equal(okWebmDl.data.filename, "Sample_2160p.webm");
    assert.equal(okWebmDl.data.mode, "memory_buffer");
    assert.equal(savedBuffers[2].mimeType, "video/webm");
    assert.equal(gmDownloads.length, 0);
    assert.ok(gmRequests.every((r) => r.anonymous === true));

    // Only when >= 1 GiB and both OPFS & showSaveFilePicker are unavailable does download fall back to gmDownload
    const hugeM4aDl = await controller.handleRequestObject({
      requestId: "req-huge-m4a-dl",
      action: "download",
      payload: {
        url: "https://rr1---sn-test.googlevideo.com/videoplayback?itag=140&clen=1200000000",
        filename: "Huge_audio.m4a",
        size: 1_200_000_000,
      },
    });
    assert.equal(hugeM4aDl.ok, true);
    assert.equal(hugeM4aDl.data.mode, "gm_download");
    assert.equal(gmDownloads.length, 1);
    assert.equal(gmDownloads[0].mode, "native");
  });

  test("published frontend/public/helper/dokitool-youtube.user.js executes in node:vm and passes rejection, player, download, and lossless mux_download cases", async () => {
    const scriptPath = new URL(
      "../public/helper/dokitool-youtube.user.js",
      import.meta.url,
    );
    const scriptText = await readFile(scriptPath, "utf8");
    const fixtures = await createSyntheticMuxFixtures();
    const bus = new EventTarget();
    const gmRequests = [];
    const gmDownloads = [];
    const streamedChunks = [];
    const pickerCalls = [];
    let localStorageAccessed = false;

    const fakePageWindow = {
      async showSaveFilePicker(opts) {
        pickerCalls.push(opts);
        return {
          name: opts.suggestedName,
          async createWritable() {
            return {
              async write(chunk) {
                streamedChunks.push(chunk);
              },
              async close() {},
              async abort() {},
            };
          },
        };
      },
    };

    const fakeWindow = {
      addEventListener: bus.addEventListener.bind(bus),
      removeEventListener: bus.removeEventListener.bind(bus),
      dispatchEvent: bus.dispatchEvent.bind(bus),
      CustomEvent: globalThis.CustomEvent,
      get localStorage() {
        localStorageAccessed = true;
        throw new Error("userscript must not access window.localStorage");
      },
    };

    const sandbox = {
      window: fakeWindow,
      document: { defaultView: fakePageWindow },
      CustomEvent: globalThis.CustomEvent,
      URL: globalThis.URL,
      Blob: globalThis.Blob,
      WritableStream: globalThis.WritableStream,
      ReadableStream: globalThis.ReadableStream,
      TextEncoder: globalThis.TextEncoder,
      TextDecoder: globalThis.TextDecoder,
      JSON: globalThis.JSON,
      Date: globalThis.Date,
      Math: globalThis.Math,
      Number: globalThis.Number,
      String: globalThis.String,
      Boolean: globalThis.Boolean,
      Object: globalThis.Object,
      Array: globalThis.Array,
      Promise: globalThis.Promise,
      RegExp: globalThis.RegExp,
      Error: globalThis.Error,
      TypeError: globalThis.TypeError,
      RangeError: globalThis.RangeError,
      DOMException: globalThis.DOMException,
      Symbol: globalThis.Symbol,
      BigInt: globalThis.BigInt,
      Map: globalThis.Map,
      Set: globalThis.Set,
      WeakMap: globalThis.WeakMap,
      WeakSet: globalThis.WeakSet,
      ArrayBuffer: globalThis.ArrayBuffer,
      DataView: globalThis.DataView,
      Int8Array: globalThis.Int8Array,
      Uint8Array: globalThis.Uint8Array,
      Uint8ClampedArray: globalThis.Uint8ClampedArray,
      Int16Array: globalThis.Int16Array,
      Uint16Array: globalThis.Uint16Array,
      Int32Array: globalThis.Int32Array,
      Uint32Array: globalThis.Uint32Array,
      Float32Array: globalThis.Float32Array,
      Float64Array: globalThis.Float64Array,
      BigInt64Array: globalThis.BigInt64Array,
      BigUint64Array: globalThis.BigUint64Array,
      TextEncoder: globalThis.TextEncoder,
      TextDecoder: globalThis.TextDecoder,
      ReadableStream: globalThis.ReadableStream,
      WritableStream: globalThis.WritableStream,
      TransformStream: globalThis.TransformStream,
      setTimeout: globalThis.setTimeout,
      clearTimeout: globalThis.clearTimeout,
      queueMicrotask: globalThis.queueMicrotask,
      performance: globalThis.performance,
      console: globalThis.console,
      GM_xmlhttpRequest(details) {
        gmRequests.push(details);
        if (details.url === "https://www.youtube.com/sw.js_data") {
          details.onload({
            status: 200,
            responseText:
              ')]}\'\n[["yt.sw.adr",null,[[["en","US",null,"198.51.100.7",null,null,null,null,null,null,null,null,null,"Cgt2bVRlc3RWaXNpdG9yRGF0YVRva2Vu"]]]]]',
          });
          return {};
        }
        if (
          details.url ===
          "https://www.youtube.com/youtubei/v1/player?prettyPrint=false"
        ) {
          details.onload({
            status: 200,
            responseText: JSON.stringify({
              playabilityStatus: { status: "OK", extra: "stripped" },
              videoDetails: {
                title: "VM\u0007 Test Title",
                author: "VM Author",
                lengthSeconds: "42",
                thumbnail: {
                  thumbnails: [
                    { url: "https://i.ytimg.com/vi/I9JvDaciaFk/hqdefault.jpg" },
                  ],
                },
              },
              streamingData: {
                expiresInSeconds: "21540",
                formats: [
                  {
                    itag: 18,
                    url: "https://rr2---sn-vm.googlevideo.com/videoplayback?itag=18",
                    mimeType: 'video/mp4; codecs="avc1.42001E, mp4a.40.2"',
                    width: 640,
                    height: 360,
                    contentLength: "144433",
                  },
                ],
                adaptiveFormats: [
                  {
                    itag: 140,
                    url: "https://rr2---sn-vm.googlevideo.com/videoplayback?itag=140",
                    mimeType: 'audio/mp4; codecs="mp4a.40.2"',
                    contentLength: "82734",
                  },
                ],
              },
            }),
          });
          return {};
        }
        if (details.url.startsWith("https://rr2---sn-vm.googlevideo.com/videoplayback")) {
          const u = new URL(details.url);
          const itag = u.searchParams.get("itag");
          if (itag === "137" || itag === "299") {
            details.onload(sliceRangeFromFixture(fixtures.mp4VideoBytes, details.url));
            return {};
          }
          if (itag === "140") {
            details.onload(sliceRangeFromFixture(fixtures.m4aAudioBytes, details.url));
            return {};
          }
        }
        details.onerror();
        return {};
      },
      GM_download(details) {
        gmDownloads.push(details);
        details.onprogress?.({ loaded: 41367, total: 82734 });
        details.onload();
      },
    };

    vm.runInNewContext(scriptText, sandbox, {
      filename: "dokitool-youtube.user.js",
    });

    // 1. Ping published script -> version 1.3.0, supported: true
    const pingRes = await pingYouTubeHelper({
      eventTarget: fakeWindow,
      timeoutMs: 100,
    });
    assert.deepEqual(pingRes, {
      installed: true,
      version: "1.3.0",
      supported: true,
    });

    // 2. Rejection cases against published script
    await assert.rejects(
      () =>
        sendHelperRequest("unknown_action", {}, { eventTarget: fakeWindow }),
      (err) =>
        err.code === "UNSUPPORTED_ACTION" &&
        err.message.includes("不支持的助手操作"),
    );
    await assert.rejects(
      () =>
        sendHelperRequest(
          "player",
          {
            video_id: "bad/id",
            signature_timestamp: 20760,
            client: { name: "TVHTML5", version: "7.20260728.13.00" },
          },
          { eventTarget: fakeWindow },
        ),
      (err) =>
        err.code === "HELPER_REJECTED" &&
        err.message.includes("非法的 video_id"),
    );
    await assert.rejects(
      () =>
        sendHelperRequest(
          "player",
          {
            video_id: "I9JvDaciaFk",
            signature_timestamp: -5,
            client: { name: "TVHTML5", version: "7.20260728.13.00" },
          },
          { eventTarget: fakeWindow },
        ),
      (err) =>
        err.code === "HELPER_REJECTED" &&
        err.message.includes("非法的 signature_timestamp"),
    );
    await assert.rejects(
      () =>
        sendHelperRequest(
          "player",
          {
            video_id: "I9JvDaciaFk",
            signature_timestamp: 20760,
            client: { name: "WEB", version: "2.20260728.00.00" },
          },
          { eventTarget: fakeWindow },
        ),
      (err) =>
        err.code === "HELPER_REJECTED" &&
        err.message.includes("仅允许 TVHTML5"),
    );
    await assert.rejects(
      () =>
        sendHelperRequest(
          "download",
          {
            url: "https://evil.example.com/videoplayback?itag=18",
            filename: "test.mp4",
          },
          { eventTarget: fakeWindow },
        ),
      (err) =>
        err.code === "HELPER_REJECTED" &&
        err.message.includes("googlevideo.com/videoplayback"),
    );
    await assert.rejects(
      () =>
        sendHelperRequest(
          "download",
          {
            url: "https://rr2---sn-vm.googlevideo.com/videoplayback?itag=18",
            filename: "test.mkv",
          },
          { eventTarget: fakeWindow },
        ),
      (err) =>
        err.code === "HELPER_REJECTED" &&
        err.message.includes(".mp4、.m4a 或 .webm"),
    );

    // 3. Happy-path player call on published script
    const playerData = await sendHelperRequest(
      "player",
      {
        video_id: "I9JvDaciaFk",
        signature_timestamp: 20760,
        client: { name: "TVHTML5", version: "7.20260728.13.00" },
      },
      { eventTarget: fakeWindow },
    );
    assert.equal(localStorageAccessed, false);
    assert.equal(gmRequests.length, 2);
    assert.equal(gmRequests[0].anonymous, true);
    assert.equal(gmRequests[1].anonymous, true);
    assert.deepEqual(playerData.videoDetails, {
      title: "VM Test Title",
      author: "VM Author",
      lengthSeconds: 42,
      thumbnail: "https://i.ytimg.com/vi/I9JvDaciaFk/hqdefault.jpg",
    });

    // Second player call reuses in-memory visitorData
    await sendHelperRequest(
      "player",
      {
        video_id: "I9JvDaciaFk",
        signature_timestamp: 20760,
        client: { name: "TVHTML5", version: "7.20260728.13.00" },
      },
      { eventTarget: fakeWindow },
    );
    assert.equal(gmRequests.length, 3);

    // 4. Happy-path MP4 and M4A download on published script (chunked streaming -> showSaveFilePicker with id: "dokitool-youtube" when OPFS absent)
    const mp4Dl = await sendHelperRequest(
      "download",
      {
        url: `https://rr2---sn-vm.googlevideo.com/videoplayback?itag=299&clen=${fixtures.mp4VideoBytes.byteLength}`,
        filename: "VM Test Title_1080p.mp4",
      },
      { eventTarget: fakeWindow },
    );
    assert.equal(mp4Dl.filename, "VM_Test_Title_1080p.mp4");
    assert.equal(mp4Dl.mode, "stream_picker");
    assert.equal(pickerCalls[0].id, "dokitool-youtube");
    assert.equal(gmDownloads.length, 0);

    const progressEvents = [];
    const m4aDl = await sendHelperRequest(
      "download",
      {
        url: `https://rr2---sn-vm.googlevideo.com/videoplayback?itag=140&clen=${fixtures.m4aAudioBytes.byteLength}`,
        filename: "VM Test Title_audio.m4a",
      },
      {
        eventTarget: fakeWindow,
        onProgress(detail) {
          progressEvents.push(detail);
        },
      },
    );
    assert.equal(m4aDl.filename, "VM_Test_Title_audio.m4a");
    assert.equal(m4aDl.mode, "stream_picker");
    assert.equal(pickerCalls[1].id, "dokitool-youtube");
    assert.ok(progressEvents.length >= 1);
    assert.equal(progressEvents[progressEvents.length - 1].loaded, fixtures.m4aAudioBytes.byteLength);

    // 5. Happy-path lossless mux_download inside node:vm using bundled Mediabunny
    const muxRes = await sendHelperRequest(
      "mux_download",
      {
        videoUrl: `https://rr2---sn-vm.googlevideo.com/videoplayback?itag=137&clen=${fixtures.mp4VideoBytes.byteLength}`,
        audioUrl: `https://rr2---sn-vm.googlevideo.com/videoplayback?itag=140&clen=${fixtures.m4aAudioBytes.byteLength}`,
        videoSize: fixtures.mp4VideoBytes.byteLength,
        audioSize: fixtures.m4aAudioBytes.byteLength,
        filename: "VM Muxed_1080p.mp4",
        outputContainer: "mp4",
      },
      { eventTarget: fakeWindow, timeoutMs: 10000 },
    );
    assert.equal(muxRes.filename, "VM_Muxed_1080p.mp4");
    assert.equal(muxRes.mode, "stream_picker");
    assert.equal(pickerCalls[2].id, "dokitool-youtube");
    assert.ok(streamedChunks.length > 0, "StreamTarget must stream chunks into picker writable");
    assert.ok(gmRequests.every((r) => r.anonymous === true), "every GM_xmlhttpRequest must set anonymous: true");
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

describe("YouTube Phase 2 pair selection and chunked Mediabunny muxing", () => {
  const sampleUrl = (itag, clen = 100000) =>
    `https://rr4---sn-ajoguc-5t.googlevideo.com/videoplayback?expire=1794238131&ip=198.51.100.7&id=o-AOdRLkc6ecTboXRtJhNBILt2v8AQeJUCoLbqEckV1dDI&itag=${itag}&source=youtube&gir=yes&clen=${clen}&c=TVHTML5&n=F7GraLOqBD6ahA&sparams=expire%2Cip%2Cid%2Citag%2Csource%2Cgir%2Cclen&sig=AAA`;

  const makeVariant = (id, metadata, itag, clen = 100000) => ({
    id,
    state: "ready",
    label: id,
    provenance: "original_endpoint",
    metadata,
    delivery: {
      kind: "file",
      sources: [
        {
          id: `s-${id}`,
          url: sampleUrl(itag, clen),
          access: "direct",
          size_bytes: clen,
        },
      ],
    },
  });

  test("selectYouTubeMuxPair pairs H.264/AV1+AAC->mp4 and VP9+Opus->webm, and rejects cross-container or progressive tracks", () => {
    const h264Video = makeVariant(
      "v-299",
      {
        width: 1920,
        height: 1080,
        fps: 60,
        has_audio: false,
        video_codec: "h264",
        container: "mp4",
        extension: "mp4",
        mime_type: "video/mp4",
      },
      299,
      500000,
    );
    const av1Video = makeVariant(
      "v-701",
      {
        width: 3840,
        height: 2160,
        fps: 60,
        has_audio: false,
        video_codec: "av01.0.12M.10",
        container: "mp4",
        extension: "mp4",
        mime_type: "video/mp4",
      },
      701,
      900000,
    );
    const vp9Video = makeVariant(
      "v-315",
      {
        width: 3840,
        height: 2160,
        fps: 60,
        has_audio: false,
        video_codec: "vp9",
        container: "webm",
        extension: "webm",
        mime_type: "video/webm",
      },
      315,
      800000,
    );
    const vp9HdrVideo = makeVariant(
      "v-337",
      {
        width: 3840,
        height: 2160,
        fps: 60,
        has_audio: false,
        video_codec: "vp9.2",
        container: "webm",
        extension: "webm",
        mime_type: "video/webm",
      },
      337,
      850000,
    );
    const prog360 = makeVariant(
      "v-18",
      {
        width: 640,
        height: 360,
        fps: 30,
        has_audio: true,
        video_codec: "h264",
        audio_codec: "aac",
        container: "mp4",
        extension: "mp4",
        mime_type: "video/mp4",
      },
      18,
      144433,
    );
    const aacAudio = makeVariant(
      "v-140",
      {
        has_audio: true,
        audio_codec: "aac",
        container: "mp4",
        extension: "m4a",
        mime_type: "audio/mp4",
      },
      140,
      80000,
    );
    const opusAudio = makeVariant(
      "v-251",
      {
        has_audio: true,
        audio_codec: "opus",
        container: "webm",
        extension: "webm",
        mime_type: "audio/webm",
      },
      251,
      90000,
    );

    const allVariants = [
      av1Video,
      vp9HdrVideo,
      vp9Video,
      h264Video,
      prog360,
      aacAudio,
      opusAudio,
    ];

    const pH264 = selectYouTubeMuxPair(h264Video, allVariants, []);
    assert.equal(pH264.ok, true);
    assert.equal(pH264.outputContainer, "mp4");
    assert.equal(pH264.outputExtension, "mp4");
    assert.equal(pH264.audioVariant.id, "v-140");

    const pAv1 = selectYouTubeMuxPair(av1Video, allVariants, []);
    assert.equal(pAv1.ok, true);
    assert.equal(pAv1.outputContainer, "mp4");
    assert.equal(pAv1.outputExtension, "mp4");
    assert.equal(pAv1.audioVariant.id, "v-140");

    const pVp9 = selectYouTubeMuxPair(vp9Video, allVariants, []);
    assert.equal(pVp9.ok, true);
    assert.equal(pVp9.outputContainer, "webm");
    assert.equal(pVp9.outputExtension, "webm");
    assert.equal(pVp9.audioVariant.id, "v-251");

    const pVp9Hdr = selectYouTubeMuxPair(vp9HdrVideo, allVariants, []);
    assert.equal(pVp9Hdr.ok, true);
    assert.equal(pVp9Hdr.outputContainer, "webm");
    assert.equal(pVp9Hdr.outputExtension, "webm");
    assert.equal(pVp9Hdr.audioVariant.id, "v-251");

    // Cross-container combinations must be rejected
    const crossVp9OnlyAac = selectYouTubeMuxPair(
      vp9Video,
      [vp9Video, aacAudio],
      [],
    );
    assert.equal(crossVp9OnlyAac.ok, false);
    assert.match(crossVp9OnlyAac.reason, /不做跨容器合流/);

    const crossH264OnlyOpus = selectYouTubeMuxPair(
      h264Video,
      [h264Video, opusAudio],
      [],
    );
    assert.equal(crossH264OnlyOpus.ok, false);
    assert.match(crossH264OnlyOpus.reason, /不做跨容器合流/);

    // Progressive video (has_audio: true) is not muxed
    const progResult = selectYouTubeMuxPair(prog360, allVariants, []);
    assert.equal(progResult.ok, false);

    // Missing audio altogether
    const noAudioResult = selectYouTubeMuxPair(h264Video, [h264Video], []);
    assert.equal(noAudioResult.ok, false);
  });

  test("mux_download recovers from per-chunk 403/network/truncated failures, muxes MP4 and WebM via StreamTarget & BufferTarget, enforces 1 GiB memory cap, and supports cancel_download", async () => {
    const fixtures = await createSyntheticMuxFixtures();
    const mp4VideoUrl = sampleUrl(137, fixtures.mp4VideoBytes.byteLength);
    const m4aAudioUrl = sampleUrl(140, fixtures.m4aAudioBytes.byteLength);
    const webmVideoUrl = sampleUrl(315, fixtures.webmVideoBytes.byteLength);
    const opusAudioUrl = sampleUrl(251, fixtures.webmAudioBytes.byteLength);

    function createMemoryStreamPicker() {
      let fileBytes = new Uint8Array(0);
      let closed = false;
      return {
        getBytes: () => fileBytes,
        isClosed: () => closed,
        pageWindow: {
          async showSaveFilePicker(opts) {
            assert.ok(opts.suggestedName);
            return {
              async createWritable() {
                let cursor = 0;
                return {
                  async write(chunk) {
                    let data;
                    let pos = cursor;
                    if (
                      chunk &&
                      typeof chunk === "object" &&
                      chunk.type === "write"
                    ) {
                      if (typeof chunk.position === "number") {
                        pos = chunk.position;
                      }
                      data = chunk.data;
                    } else {
                      data = chunk;
                    }
                    const u8 =
                      data instanceof Uint8Array
                        ? data
                        : new Uint8Array(data);
                    const end = pos + u8.byteLength;
                    if (end > fileBytes.byteLength) {
                      const grown = new Uint8Array(end);
                      grown.set(fileBytes);
                      fileBytes = grown;
                    }
                    fileBytes.set(u8, pos);
                    cursor = end;
                  },
                  async close() {
                    closed = true;
                  },
                  async abort() {
                    closed = false;
                  },
                };
              },
            };
          },
        },
      };
    }

    // 1. Per-chunk retry recovery (403 -> network error -> truncated -> valid) + MP4 StreamTarget
    let chunk4096Attempts = 0;
    const seenAnonymous = [];
    const mp4Picker = createMemoryStreamPicker();
    const retryTarget = new EventTarget();
    const unmountRetry = createUserscriptController({
      eventTarget: retryTarget,
      pageWindow: mp4Picker.pageWindow,
      mediabunny,
      muxConfig: {
        chunkSize: 4096,
        maxRetries: 3,
        retryBaseDelayMs: 5,
      },
      gmXmlhttpRequest(details) {
        seenAnonymous.push(details.anonymous);
        const u = new URL(details.url);
        const itag = u.searchParams.get("itag");
        const range = u.searchParams.get("range") || "";
        if (itag === "137" && range.startsWith("4096-")) {
          chunk4096Attempts += 1;
          if (chunk4096Attempts === 1) {
            details.onload({
              status: 403,
              responseHeaders: "",
              response: new ArrayBuffer(0),
            });
            return;
          }
          if (chunk4096Attempts === 2) {
            details.onerror(new Error("transient reset"));
            return;
          }
          if (chunk4096Attempts === 3) {
            details.onload({
              status: 200,
              responseHeaders: "",
              response: new ArrayBuffer(64),
            });
            return;
          }
        }
        const bytes =
          itag === "137" ? fixtures.mp4VideoBytes : fixtures.m4aAudioBytes;
        details.onload(sliceRangeFromFixture(bytes, details.url));
      },
    }).mount();

    try {
      const progressEvents = [];
      const res = await sendHelperRequest(
        "mux_download",
        {
          videoUrl: mp4VideoUrl,
          audioUrl: m4aAudioUrl,
          videoSize: fixtures.mp4VideoBytes.byteLength,
          audioSize: fixtures.m4aAudioBytes.byteLength,
          filename: "Retry_Test_1080p.mp4",
          outputContainer: "mp4",
        },
        {
          eventTarget: retryTarget,
          timeoutMs: 10000,
          onProgress: (p) => progressEvents.push(p),
        },
      );
      assert.equal(res.filename, "Retry_Test_1080p.mp4");
      assert.equal(res.mode, "stream_picker");
      assert.equal(chunk4096Attempts, 4);
      assert.ok(seenAnonymous.every((v) => v === true));
      assert.equal(mp4Picker.isClosed(), true);
      assert.ok(progressEvents.length > 0);

      const mergedMp4 = mp4Picker.getBytes();
      const verifyIn = new mediabunny.Input({
        source: new mediabunny.BufferSource(mergedMp4),
        formats: [mediabunny.MP4],
      });
      const vTracks = await verifyIn.getVideoTracks();
      const aTracks = await verifyIn.getAudioTracks();
      assert.equal(vTracks.length, 1);
      assert.equal(aTracks.length, 1);
      assert.equal(vTracks[0].codec, "avc");
      assert.equal(aTracks[0].codec, "aac");
      verifyIn.dispose();
    } finally {
      unmountRetry();
    }

    // 2. WebM StreamTarget (VP9 + Opus -> .webm)
    const webmPicker = createMemoryStreamPicker();
    const webmTarget = new EventTarget();
    const unmountWebm = createUserscriptController({
      eventTarget: webmTarget,
      pageWindow: webmPicker.pageWindow,
      mediabunny,
      muxConfig: { chunkSize: 4096, maxRetries: 2, retryBaseDelayMs: 5 },
      gmXmlhttpRequest(details) {
        const itag = new URL(details.url).searchParams.get("itag");
        const bytes =
          itag === "315" ? fixtures.webmVideoBytes : fixtures.webmAudioBytes;
        details.onload(sliceRangeFromFixture(bytes, details.url));
      },
    }).mount();
    try {
      const res = await sendHelperRequest(
        "mux_download",
        {
          videoUrl: webmVideoUrl,
          audioUrl: opusAudioUrl,
          videoSize: fixtures.webmVideoBytes.byteLength,
          audioSize: fixtures.webmAudioBytes.byteLength,
          filename: "WebM_Test_2160p.webm",
          outputContainer: "webm",
        },
        { eventTarget: webmTarget, timeoutMs: 10000 },
      );
      assert.equal(res.filename, "WebM_Test_2160p.webm");
      assert.equal(res.mode, "stream_picker");
      const verifyWebm = new mediabunny.Input({
        source: new mediabunny.BufferSource(webmPicker.getBytes()),
        formats: [mediabunny.WEBM],
      });
      assert.equal((await verifyWebm.getVideoTracks())[0].codec, "vp9");
      assert.equal((await verifyWebm.getAudioTracks())[0].codec, "opus");
      verifyWebm.dispose();
    } finally {
      unmountWebm();
    }

    // 3. < 1 GiB BufferTarget fallback when showSaveFilePicker is unavailable, and >= 1 GiB rejection
    const savedBuffers = [];
    const bufTarget = new EventTarget();
    const unmountBuf = createUserscriptController({
      eventTarget: bufTarget,
      pageWindow: {}, // No showSaveFilePicker
      mediabunny,
      muxConfig: { chunkSize: 4096, maxRetries: 2, retryBaseDelayMs: 5 },
      saveBuffer(arrayBuffer, filename, mimeType) {
        savedBuffers.push({ arrayBuffer, filename, mimeType });
      },
      gmXmlhttpRequest(details) {
        const itag = new URL(details.url).searchParams.get("itag");
        const bytes =
          itag === "137" ? fixtures.mp4VideoBytes : fixtures.m4aAudioBytes;
        details.onload(sliceRangeFromFixture(bytes, details.url));
      },
    }).mount();
    try {
      const bufRes = await sendHelperRequest(
        "mux_download",
        {
          videoUrl: mp4VideoUrl,
          audioUrl: m4aAudioUrl,
          videoSize: fixtures.mp4VideoBytes.byteLength,
          audioSize: fixtures.m4aAudioBytes.byteLength,
          filename: "Memory_Fallback_1080p.mp4",
          outputContainer: "mp4",
        },
        { eventTarget: bufTarget, timeoutMs: 10000 },
      );
      assert.equal(bufRes.mode, "memory_buffer");
      assert.equal(savedBuffers.length, 1);
      assert.equal(savedBuffers[0].filename, "Memory_Fallback_1080p.mp4");
      assert.equal(savedBuffers[0].mimeType, "video/mp4");
      assert.ok(savedBuffers[0].arrayBuffer.byteLength > 1000);

      // >= 1 GiB without showSaveFilePicker -> rejects with MUX_TOO_LARGE_FOR_MEMORY
      await assert.rejects(
        () =>
          sendHelperRequest(
            "mux_download",
            {
              videoUrl: sampleUrl(701, 900_000_000),
              audioUrl: sampleUrl(140, 250_000_000),
              videoSize: 900_000_000,
              audioSize: 250_000_000,
              filename: "Huge_2160p.mp4",
              outputContainer: "mp4",
            },
            { eventTarget: bufTarget, timeoutMs: 5000 },
          ),
        (err) => err.code === "MUX_TOO_LARGE_FOR_MEMORY",
      );
    } finally {
      unmountBuf();
    }

    // 4. User cancelling showSaveFilePicker -> AbortError / USER_CANCELLED, and cancel_download aborts XHR
    const cancelPickerTarget = new EventTarget();
    const unmountCancelPicker = createUserscriptController({
      eventTarget: cancelPickerTarget,
      pageWindow: {
        async showSaveFilePicker() {
          throw new DOMException("The user aborted a request.", "AbortError");
        },
      },
      mediabunny,
      gmXmlhttpRequest() {
        throw new Error("must not fetch chunks after picker cancel");
      },
    }).mount();
    try {
      await assert.rejects(
        () =>
          sendHelperRequest(
            "mux_download",
            {
              videoUrl: mp4VideoUrl,
              audioUrl: m4aAudioUrl,
              videoSize: fixtures.mp4VideoBytes.byteLength,
              audioSize: fixtures.m4aAudioBytes.byteLength,
              filename: "Cancelled_1080p.mp4",
              outputContainer: "mp4",
            },
            { eventTarget: cancelPickerTarget, timeoutMs: 5000 },
          ),
        (err) => err.name === "AbortError" && err.code === "USER_CANCELLED",
      );
    } finally {
      unmountCancelPicker();
    }

    let abortedChunkXhr = false;
    const inFlightTarget = new EventTarget();
    const unmountInFlight = createUserscriptController({
      eventTarget: inFlightTarget,
      pageWindow: createMemoryStreamPicker().pageWindow,
      mediabunny,
      gmXmlhttpRequest() {
        return {
          abort() {
            abortedChunkXhr = true;
          },
        };
      },
    }).mount();
    try {
      const ac = new AbortController();
      const pending = sendHelperRequest(
        "mux_download",
        {
          videoUrl: mp4VideoUrl,
          audioUrl: m4aAudioUrl,
          videoSize: fixtures.mp4VideoBytes.byteLength,
          audioSize: fixtures.m4aAudioBytes.byteLength,
          filename: "InFlightCancel_1080p.mp4",
          outputContainer: "mp4",
        },
        {
          eventTarget: inFlightTarget,
          timeoutMs: 5000,
          signal: ac.signal,
        },
      );
      await new Promise((r) => setTimeout(r, 25));
      ac.abort();
      await assert.rejects(pending, (err) => err.name === "AbortError");
      await new Promise((r) => setTimeout(r, 25));
      assert.equal(abortedChunkXhr, true);
    } finally {
      unmountInFlight();
    }
  });
});

describe("YouTube frontend parser, ResultSection UI states, and download flows", () => {
  let vite,
    parser,
    selectionMod,
    downloadMod,
    videoFilenameMod,
    ResultSection,
    DownloadCard,
    fixtures;
  const originalFetch = globalThis.fetch;
  const originalWindow = globalThis.window;
  const originalDocument = globalThis.document;
  const originalNavigator = globalThis.navigator;

  const itag18Url =
    "https://rr4---sn-ajoguc-5t.googlevideo.com/videoplayback?expire=1794238131&ip=198.51.100.7&id=o-AOdRLkc6ecTboXRtJhNBILt2v8AQeJUCoLbqEckV1dDI&itag=18&source=youtube&c=TVHTML5&n=5GuFTFbCJY0P_g&sparams=expire%2Cip%2Cid%2Citag%2Csource&sig=AAA&title=Sample_360p";
  const makeItagUrl = (itag, clen) =>
    `https://rr4---sn-ajoguc-5t.googlevideo.com/videoplayback?expire=1794238131&ip=198.51.100.7&id=o-AOdRLkc6ecTboXRtJhNBILt2v8AQeJUCoLbqEckV1dDI&itag=${itag}&source=youtube&gir=yes&clen=${clen}&c=TVHTML5&n=F7GraLOqBD6ahA&sparams=expire%2Cip%2Cid%2Citag%2Csource%2Cgir%2Cclen&sig=BBB`;

  const sampleYouTubeUnifiedData = (opts = {}) => {
    const videoClen = opts.videoClen ?? fixtures.mp4VideoBytes.byteLength;
    const audioClen = opts.audioClen ?? fixtures.m4aAudioBytes.byteLength;
    const itag299Url = makeItagUrl(299, videoClen);
    const itag140Url = makeItagUrl(140, audioClen);
    const extraVariants = opts.extraVariants ?? [];
    return {
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
            ...extraVariants,
            {
              id: "v-299",
              label: "1080p60 (仅视频, 无音频)",
              provenance: "original_endpoint",
              metadata: {
                width: 1920,
                height: 1080,
                fps: 60,
                has_audio: false,
                video_codec: "h264",
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
                    size_bytes: videoClen,
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
                video_codec: "h264",
                audio_codec: "aac",
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
            {
              id: "v-140",
              label: "音轨 (M4A)",
              provenance: "original_endpoint",
              metadata: {
                has_audio: true,
                audio_codec: "aac",
                container: "mp4",
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
                    size_bytes: audioClen,
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
              audio_codec: "aac",
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
                  size_bytes: audioClen,
                },
              ],
            },
          },
        },
      ],
    };
  };

  before(async () => {
    fixtures = await createSyntheticMuxFixtures();
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
    videoFilenameMod = await vite.ssrLoadModule(
      "/src/utils/videoFilename.ts",
    );
    ResultSection = (
      await vite.ssrLoadModule("/src/components/ResultSection.vue")
    ).default;
    DownloadCard = (
      await vite.ssrLoadModule("/src/components/DownloadCard.vue")
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
          fallbackReason: null,
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

  async function renderResultHtml(store, selection, sync = true) {
    if (sync) {
      selection.syncResult();
    }
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

  async function renderDownloadCardHtml(store) {
    const app = createSSRApp({
      render: () => h(DownloadCard, { downloads: store.downloads }),
    });
    return renderToString(app);
  }

  test("without helper or with outdated helper (1.0.0 / 1.1.0 < 1.2.0): shows parse result + install/update guide, strips server-IP googlevideo links, and refuses download without 已交给浏览器", async () => {
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
    assert.doesNotMatch(
      JSON.stringify(env.store.resultData),
      /googlevideo\.com/,
    );

    const html = await renderResultHtml(env.store, env.selection);
    assert.ok(html.includes("需要安装 YouTube 下载助手脚本"));
    assert.ok(html.includes("允许用户脚本"));
    assert.ok(html.includes("/helper/dokitool-youtube.user.js"));
    assert.ok(!html.includes("googlevideo.com"));

    await env.downloader.downloadMainVideo();
    assert.equal(env.store.downloads.length, 0);
    assert.equal(env.clickedAnchors.length, 0);
    assert.ok(
      !env.toasts.some(([msg]) => msg.includes("已交给浏览器")),
      "must never show 已交给浏览器 when helper is missing",
    );

    for (const outdatedVer of ["1.0.0", "1.1.0"]) {
      const unmountOld = createUserscriptController({
        eventTarget: env.bus,
        version: outdatedVer,
      }).mount();
      try {
        env.parserInstance.inputUrl.value =
          "https://www.youtube.com/watch?v=aqz-KE-bpKQ";
        await env.parserInstance.parseVideo();
        assert.equal(
          env.store.resultData.extra.youtube_helper.status,
          "outdated",
        );
        const oldHtml = await renderResultHtml(env.store, env.selection);
        assert.ok(oldHtml.includes("YouTube 下载助手脚本需要更新"));
        assert.ok(oldHtml.includes("更新 YouTube 助手脚本"));
        assert.ok(!oldHtml.includes("googlevideo.com"));
      } finally {
        unmountOld();
      }
    }
  });

  test("with helper returning LOGIN_REQUIRED: prompts to switch network exit and never asks for login or cookies", async () => {
    const env = setupEnvironment();
    const unmount = createUserscriptController({
      eventTarget: env.bus,
      version: YOUTUBE_HELPER_VERSION,
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

  test("with helper v1.3.0 ready: renders single primary 合并保存 button (no 分开保存), streams 1080p mux & 仅音频 M4A via OPFS default path, and displays savePathLabel on DownloadCard", async () => {
    const env = setupEnvironment();
    const opfs = createMockOpfsEnvironment({ quota: 500_000_000 });
    const gmSaved = [];
    const opfsClicked = [];
    const itag299Url = makeItagUrl(299, fixtures.mp4VideoBytes.byteLength);
    const itag140Url = makeItagUrl(140, fixtures.m4aAudioBytes.byteLength);

    const fakePageWindow = {
      navigator: { storage: opfs.storage },
      URL: {
        createObjectURL(file) {
          return `blob:opfs-${file.name}-${file.size}`;
        },
        revokeObjectURL() {},
      },
      document: {
        body: { appendChild() {} },
        createElement(tag) {
          return {
            tag,
            href: "",
            download: "",
            rel: "",
            style: {},
            click() {
              opfsClicked.push({ href: this.href, download: this.download });
            },
            remove() {},
          };
        },
      },
    };

    const unmount = createUserscriptController({
      eventTarget: env.bus,
      pageWindow: fakePageWindow,
      version: YOUTUBE_HELPER_VERSION,
      mediabunny,
      muxConfig: {
        chunkSize: 4096,
        maxRetries: 2,
        retryBaseDelayMs: 5,
        opfsCleanupDelayMs: 25,
      },
      gmXmlhttpRequest(details) {
        if (details.url.includes("sw.js_data")) {
          details.onload({
            status: 200,
            responseText: '["Cgt0ZXN0VmlzaXRvckRhdGFTdHJpbmc%3D"]',
          });
          return;
        }
        if (details.url.includes("/youtubei/v1/player")) {
          details.onload({
            status: 200,
            responseText: JSON.stringify({
              playabilityStatus: { status: "OK" },
              videoDetails: {
                title: "Sample YouTube Video",
                author: "Sample Channel",
                lengthSeconds: "19",
                thumbnail: {
                  thumbnails: [
                    { url: "https://i.ytimg.com/vi/I9JvDaciaFk/hqdefault.jpg" },
                  ],
                },
              },
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
                    contentLength: String(fixtures.mp4VideoBytes.byteLength),
                  },
                  {
                    itag: 140,
                    url: itag140Url,
                    mimeType: 'audio/mp4; codecs="mp4a.40.2"',
                    contentLength: String(fixtures.m4aAudioBytes.byteLength),
                  },
                ],
              },
            }),
          });
          return;
        }
        const itag = new URL(details.url).searchParams.get("itag");
        const bytes =
          itag === "299" ? fixtures.mp4VideoBytes : fixtures.m4aAudioBytes;
        details.onload(sliceRangeFromFixture(bytes, details.url));
      },
      gmDownload(details) {
        gmSaved.push({
          url: details.url,
          name: details.name,
          mode: details.mode,
        });
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
          assert.equal(body.title, "Sample YouTube Video");
          assert.equal(body.author, "Sample Channel");
          assert.equal(body.duration_seconds, 19);
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

      // 1. Default 360p (itag=18) -> updated top notice without 分开保存 or 受 2 倍码率限速
      const html360 = await renderResultHtml(env.store, env.selection);
      assert.ok(html360.includes("默认分块并发无损「合并保存」至浏览器默认下载目录"));
      assert.ok(html360.includes("仅音频"));
      assert.ok(!html360.includes("受 2 倍码率限速"));
      assert.ok(!html360.includes("分开保存"));
      assert.ok(html360.includes("下载视频"));
      assert.ok(!html360.includes("data-youtube-separate-save"));

      await env.downloader.downloadMainVideo();
      assert.equal(env.store.downloads.length, 1);
      assert.equal(env.store.downloads[0].status, "handed-off");
      assert.equal(env.store.downloads[0].transport, "browser_native");
      assert.equal(
        env.store.downloads[0].filename,
        "Sample YouTube Video_360p.mp4",
      );
      assert.equal(env.clickedAnchors.length, 1);
      assert.equal(env.clickedAnchors[0].href, itag18Url);
      assert.equal(env.clickedAnchors[0].target, "_blank");

      // 2. Select 1080p60 split track (v-299) -> ResultSection renders ONLY 合并保存 (NO 分开保存) + keeps 仅音频
      env.selection.choose({ variant_id: "v-299", source_id: "s-299" });
      const html1080 = await renderResultHtml(env.store, env.selection, false);
      assert.ok(html1080.includes("data-youtube-mux-save"));
      assert.ok(html1080.includes("合并保存"));
      assert.ok(!html1080.includes("data-youtube-separate-save"));
      assert.ok(!html1080.includes("分开保存"));

      env.selection.choose({ variant_id: "v-140", source_id: "s-140" });
      const htmlAudioOnly = await renderResultHtml(env.store, env.selection, false);
      assert.ok(htmlAudioOnly.includes("data-youtube-audio-only-save"));
      assert.ok(htmlAudioOnly.includes("仅音频"));
      env.selection.choose({ variant_id: "v-299", source_id: "s-299" });

      // Primary 合并保存 streams mux output into OPFS temp file named with task ID and hands off via <a download>
      await env.downloader.downloadMainVideo();
      assert.equal(env.store.downloads.length, 2);
      const muxTask = env.store.downloads[1];
      assert.equal(muxTask.status, "completed");
      assert.equal(muxTask.filename, "Sample_YouTube_Video_1080p.mp4");
      assert.equal(muxTask.fallbackReason, null);
      assert.equal(muxTask.savePath, "opfs");
      assert.equal(muxTask.savePathLabel, "保存路径：默认下载目录（OPFS 流式写盘）");
      assert.equal(opfsClicked.length, 1);
      assert.equal(opfsClicked[0].download, "Sample_YouTube_Video_1080p.mp4");
      assert.match(opfsClicked[0].href, /dokitool-yt-tmp-[0-9a-z-]+-\d+-\d+\.part/);
      assert.equal(gmSaved.length, 0);

      const cardHtml = await renderDownloadCardHtml(env.store);
      assert.ok(cardHtml.includes("data-download-save-path"));
      assert.ok(cardHtml.includes("默认下载目录（OPFS 流式写盘）"));

      // 3. Standalone M4A attachment ("仅音频") ALSO streams via chunked GM_xmlhttpRequest + OPFS (no GM_download)
      env.downloader.downloadMusic(env.store.resultData.attachments[0]);
      await new Promise((r) => setTimeout(r, 30));
      assert.equal(env.store.downloads.length, 3);
      assert.equal(env.store.downloads[2].status, "completed");
      assert.equal(env.store.downloads[2].savePath, "opfs");
      assert.equal(opfsClicked.length, 2);
      assert.equal(opfsClicked[1].download, "Sample_YouTube_Video_audio.m4a");
      assert.match(opfsClicked[1].href, /dokitool-yt-tmp-[0-9a-z-]+-\d+-\d+\.part/);
      assert.equal(gmSaved.length, 0);

      // Delayed OPFS cleanup removes temp files after handoff -> OPFS usage returns to 0
      await new Promise((r) => setTimeout(r, 40));
      assert.equal(opfs.files.size, 0);
    } finally {
      unmount();
    }
  });

  test("automatic 2-file fallback: missing same-container audio, >= 1 GiB without OPFS/picker, or chunk 403 retry exhaustion falls back to separate tracks and renders fallbackReason on DownloadCard; user cancel does NOT fall back", async () => {
    const env = setupEnvironment();
    const gmSaved = [];
    const savedBuffers = [];
    let failVideoRangeRemaining = 0;
    let forceUserCancel = false;
    const itag315Url = makeItagUrl(315, fixtures.webmVideoBytes.byteLength);

    const unmount = createUserscriptController({
      eventTarget: env.bus,
      pageWindow: {
        get showSaveFilePicker() {
          if (!forceUserCancel) return undefined;
          return async () => {
            throw new DOMException("User cancelled", "AbortError");
          };
        },
      },
      version: YOUTUBE_HELPER_VERSION,
      mediabunny,
      muxConfig: { chunkSize: 4096, maxRetries: 1, retryBaseDelayMs: 5 },
      saveBuffer(buf, filename, mimeType) {
        savedBuffers.push({ filename, mimeType, size: buf.byteLength });
      },
      gmXmlhttpRequest(details) {
        const itag = new URL(details.url).searchParams.get("itag");
        if (itag === "299" && failVideoRangeRemaining > 0) {
          failVideoRangeRemaining -= 1;
          details.onload({
            status: 403,
            responseHeaders: "",
            response: new ArrayBuffer(0),
          });
          return;
        }
        const bytes =
          itag === "299"
            ? fixtures.mp4VideoBytes
            : itag === "315"
              ? fixtures.webmVideoBytes
              : fixtures.m4aAudioBytes;
        details.onload(sliceRangeFromFixture(bytes, details.url));
      },
      gmDownload(details) {
        gmSaved.push({
          url: details.url,
          name: details.name,
          mode: details.mode,
        });
        details.onload();
      },
    }).mount();

    try {
      // Case A: Cross-container (VP9 WebM video when only AAC M4A audio exists)
      const dataA = sampleYouTubeUnifiedData({
        extraVariants: [
          {
            id: "v-315",
            label: "2160p60 4K (仅视频, 无音频)",
            provenance: "original_endpoint",
            metadata: {
              width: 3840,
              height: 2160,
              fps: 60,
              has_audio: false,
              video_codec: "vp9",
              container: "webm",
              extension: "webm",
              mime_type: "video/webm",
              evidence: "upstream",
            },
            delivery: {
              kind: "file",
              sources: [
                {
                  id: "s-315",
                  url: itag315Url,
                  access: "direct",
                  size_bytes: fixtures.webmVideoBytes.byteLength,
                },
              ],
            },
          },
        ],
      });
      dataA.extra = { youtube_helper: { status: "ready", version: "1.3.0" } };
      env.store.setResult(dataA);
      env.selection.syncResult();
      env.selection.choose({ variant_id: "v-315", source_id: "s-315" });

      await env.downloader.downloadMainVideo();
      assert.equal(savedBuffers.length, 2);
      assert.deepEqual(
        savedBuffers.map((b) => b.filename).sort(),
        [
          "Sample_YouTube_Video_2160p.webm",
          "Sample_YouTube_Video_audio.m4a",
        ],
      );
      assert.equal(gmSaved.length, 0);
      assert.match(
        env.store.downloads[0].fallbackReason,
        /不做跨容器合流.*已自动回退为音视频分开保存/,
      );
      const cardHtmlA = await renderDownloadCardHtml(env.store);
      assert.ok(cardHtmlA.includes("data-download-fallback-reason"));
      assert.ok(cardHtmlA.includes("已自动回退为音视频分开保存"));

      // Case B: >= 1 GiB without OPFS or showSaveFilePicker -> MUX_TOO_LARGE_FOR_MEMORY -> 2-file fallback
      env.store.downloads.splice(0);
      savedBuffers.splice(0);
      gmSaved.splice(0);
      const dataB = sampleYouTubeUnifiedData({
        videoClen: 1_100_000_000,
        audioClen: fixtures.m4aAudioBytes.byteLength,
      });
      dataB.extra = { youtube_helper: { status: "ready", version: "1.3.0" } };
      env.store.setResult(dataB);
      env.selection.syncResult();
      env.selection.choose({ variant_id: "v-299", source_id: "s-299" });

      await env.downloader.downloadMainVideo();
      assert.equal(gmSaved.length, 1);
      assert.equal(gmSaved[0].name, "Sample_YouTube_Video_1080p.mp4");
      assert.equal(savedBuffers.length, 1);
      assert.equal(savedBuffers[0].filename, "Sample_YouTube_Video_audio.m4a");
      assert.match(
        env.store.downloads[0].fallbackReason,
        /1 GB.*已自动回退为音视频分开保存/,
      );
      const cardHtmlB = await renderDownloadCardHtml(env.store);
      assert.ok(cardHtmlB.includes("data-download-fallback-reason"));

      // Case C: Chunk HTTP 403 exhausting mux retries -> 2-file chunked fallback
      env.store.downloads.splice(0);
      savedBuffers.splice(0);
      gmSaved.splice(0);
      failVideoRangeRemaining = 2; // Exhausts maxRetries: 1 (2 attempts on itag 299) in mux_download, then succeeds in 2-file fallback
      const dataC = sampleYouTubeUnifiedData();
      dataC.extra = { youtube_helper: { status: "ready", version: "1.3.0" } };
      env.store.setResult(dataC);
      env.selection.syncResult();
      env.selection.choose({ variant_id: "v-299", source_id: "s-299" });

      await env.downloader.downloadMainVideo();
      failVideoRangeRemaining = 0;
      assert.equal(savedBuffers.length, 2);
      assert.match(
        env.store.downloads[0].fallbackReason,
        /HTTP 403.*已自动回退为音视频分开保存/,
      );

      // Case D: User cancelling showSaveFilePicker -> cancelled, NO 2-file fallback
      env.store.downloads.splice(0);
      savedBuffers.splice(0);
      gmSaved.splice(0);
      forceUserCancel = true;
      await env.downloader.downloadMainVideo();
      forceUserCancel = false;
      assert.equal(savedBuffers.length, 0);
      assert.equal(gmSaved.length, 0);
      assert.equal(env.store.downloads.length, 1);
      assert.equal(env.store.downloads[0].status, "cancelled");
      assert.equal(env.store.downloads[0].fallbackReason, null);
    } finally {
      unmount();
    }
  });

  test("buildMediaFilename omits _P1 for single-item works, supports .mp4/.m4a/.webm, and keeps _P1/_P2 for multi-item works", () => {
    const { buildMediaFilename, sanitizeFilename, hashMediaName } =
      videoFilenameMod;

    // Single item (total omitted or total === 1): no _P1 suffix
    assert.equal(
      buildMediaFilename({
        title: "Single Work",
        index: 1,
        fallback: "single-1",
        extension: "mp4",
      }),
      "Single Work.mp4",
    );
    assert.equal(
      buildMediaFilename({
        title: "Single Work",
        index: 1,
        total: 1,
        fallback: "single-1",
        extension: "mp4",
      }),
      "Single Work.mp4",
    );
    assert.equal(
      buildMediaFilename({
        title: "Me at the zoo",
        index: 1,
        total: 1,
        suffix: "1080p",
        fallback: "jNQXAC9IVRw",
        extension: "mp4",
      }),
      "Me at the zoo_1080p.mp4",
    );
    assert.equal(
      buildMediaFilename({
        title: "Costa Rica 4K",
        index: 1,
        total: 1,
        suffix: "2160p",
        fallback: "LXb3EKWsInQ",
        extension: "webm",
      }),
      "Costa Rica 4K_2160p.webm",
    );
    assert.equal(
      buildMediaFilename({
        title: "Me at the zoo",
        index: 1,
        total: 1,
        suffix: "audio",
        fallback: "jNQXAC9IVRw",
        extension: "m4a",
      }),
      "Me at the zoo_audio.m4a",
    );
    assert.equal(
      buildMediaFilename({
        title: "Costa Rica 4K",
        index: 1,
        total: 1,
        suffix: "audio",
        fallback: "LXb3EKWsInQ",
        extension: "webm",
      }),
      "Costa Rica 4K_audio.webm",
    );

    // Multi-item (total > 1): keeps _P1, _P2, _P3
    assert.equal(
      buildMediaFilename({
        title: "Album Work",
        index: 1,
        total: 3,
        fallback: "album-1",
        extension: "jpg",
      }),
      "Album Work_P1.jpg",
    );
    assert.equal(
      buildMediaFilename({
        title: "Album Work",
        index: 2,
        total: 3,
        fallback: "album-2",
        extension: "jpg",
      }),
      "Album Work_P2.jpg",
    );
    assert.equal(
      buildMediaFilename({
        title: "Album Work",
        index: 3,
        total: 3,
        fallback: "album-3",
        extension: "mp4",
      }),
      "Album Work_P3.mp4",
    );

    // Strip Unicode format characters (Cf, e.g. U+202E RLO, U+200B ZWSP, U+FEFF BOM)
    assert.equal(
      buildMediaFilename({
        title: " \ufeffClip\u202e4pm.exe\u200b\u202c ",
        index: 1,
        total: 1,
        suffix: "360\u202ep",
        fallback: "I9JvDaciaFk",
        extension: "mp4",
      }),
      "Clip4pm.exe_360p.mp4",
    );
    assert.equal(
      buildMediaFilename({
        title: "\u202e\u200b\ufeff",
        index: 1,
        total: 1,
        fallback: "I9JvDaciaFk",
        extension: "mp4",
      }),
      `${hashMediaName("I9JvDaciaFk")}.mp4`,
    );
    assert.equal(
      sanitizeFilename("test\u202e4pm.exe.mp4"),
      "test4pm.exe.mp4",
    );
  });

  test("2-file fallback path streams both tracks via chunked GM_xmlhttpRequest + OPFS in published userscript with exact <title>_<quality>.<ext> filenames, and video-track retry does not duplicate audio", async () => {
    const env = setupEnvironment();
    const scriptPath = new URL(
      "../public/helper/dokitool-youtube.user.js",
      import.meta.url,
    );
    const scriptText = await readFile(scriptPath, "utf8");
    const opfs = createMockOpfsEnvironment({ quota: 500_000_000 });
    const bridgeDownloads = [];
    const opfsDownloads = [];
    let failMp4Requests = false;

    env.bus.addEventListener("dokitool:youtube-helper:request", (evt) => {
      const detail =
        typeof evt?.detail === "string" ? JSON.parse(evt.detail) : evt?.detail;
      if (detail?.action === "download") {
        bridgeDownloads.push({
          url: detail.payload?.url,
          filename: detail.payload?.filename,
          taskId: detail.payload?.taskId,
        });
      }
    });

    const fakePageWin = {
      navigator: { storage: opfs.storage },
      URL: {
        createObjectURL(file) {
          return `blob:opfs-${file.name}`;
        },
        revokeObjectURL() {},
      },
      document: {
        body: { appendChild() {} },
        createElement(tag) {
          return {
            tag,
            href: "",
            download: "",
            rel: "",
            style: {},
            click() {
              opfsDownloads.push({ href: this.href, download: this.download });
            },
            remove() {},
          };
        },
      },
    };

    const sandboxWindow = {
      addEventListener: env.bus.addEventListener.bind(env.bus),
      removeEventListener: env.bus.removeEventListener.bind(env.bus),
      dispatchEvent: env.bus.dispatchEvent.bind(env.bus),
      CustomEvent: globalThis.CustomEvent,
      document: { defaultView: fakePageWin },
    };

    vm.runInNewContext(
      scriptText,
      {
        window: sandboxWindow,
        document: { defaultView: fakePageWin },
        CustomEvent: globalThis.CustomEvent,
        URL: globalThis.URL,
        Blob: globalThis.Blob,
        Object: globalThis.Object,
        Array: globalThis.Array,
        String: globalThis.String,
        Number: globalThis.Number,
        Boolean: globalThis.Boolean,
        RegExp: globalThis.RegExp,
        Math: globalThis.Math,
        Date: globalThis.Date,
        JSON: globalThis.JSON,
        Promise: globalThis.Promise,
        Error: globalThis.Error,
        TypeError: globalThis.TypeError,
        RangeError: globalThis.RangeError,
        DOMException: globalThis.DOMException,
        Symbol: globalThis.Symbol,
        BigInt: globalThis.BigInt,
        Map: globalThis.Map,
        Set: globalThis.Set,
        WeakMap: globalThis.WeakMap,
        WeakSet: globalThis.WeakSet,
        ArrayBuffer: globalThis.ArrayBuffer,
        DataView: globalThis.DataView,
        Int8Array: globalThis.Int8Array,
        Uint8Array: globalThis.Uint8Array,
        Uint8ClampedArray: globalThis.Uint8ClampedArray,
        Int16Array: globalThis.Int16Array,
        Uint16Array: globalThis.Uint16Array,
        Int32Array: globalThis.Int32Array,
        Uint32Array: globalThis.Uint32Array,
        Float32Array: globalThis.Float32Array,
        Float64Array: globalThis.Float64Array,
        BigInt64Array: globalThis.BigInt64Array,
        BigUint64Array: globalThis.BigUint64Array,
        TextEncoder: globalThis.TextEncoder,
        TextDecoder: globalThis.TextDecoder,
        ReadableStream: globalThis.ReadableStream,
        WritableStream: globalThis.WritableStream,
        TransformStream: globalThis.TransformStream,
        setTimeout: (fn, ms) => globalThis.setTimeout(fn, Math.min(Number(ms) || 0, 5)),
        clearTimeout: globalThis.clearTimeout,
        queueMicrotask: globalThis.queueMicrotask,
        performance: globalThis.performance,
        console: globalThis.console,
        GM_xmlhttpRequest(details) {
          assert.equal(details.anonymous, true);
          const itag = new URL(details.url).searchParams.get("itag");
          if (failMp4Requests && itag === "137") {
            details.onload({
              status: 403,
              responseHeaders: "",
              response: new ArrayBuffer(0),
            });
            return {};
          }
          const bytes =
            itag === "137"
              ? fixtures.mp4VideoBytes
              : itag === "313"
                ? fixtures.webmVideoBytes
                : itag === "251"
                  ? fixtures.webmAudioBytes
                  : fixtures.m4aAudioBytes;
          details.onload(sliceRangeFromFixture(bytes, details.url));
          return {};
        },
        GM_download() {
          throw new Error("GM_download must not be called when OPFS streaming is available");
        },
      },
      { filename: "dokitool-youtube.user.js" },
    );

    const itag137Url = `https://rr2---sn-o097znz7.googlevideo.com/videoplayback?expire=1794238131&ei=abc&ip=198.51.100.7&id=o-ABlender1080p&itag=137&source=youtube&gir=yes&clen=${fixtures.mp4VideoBytes.byteLength}&c=TVHTML5&sparams=expire%2Cei%2Cip%2Cid%2Citag%2Csource%2Cgir%2Cclen&sig=CCC`;
    const itag313Url = `https://rr2---sn-o097znz7.googlevideo.com/videoplayback?expire=1794238131&ei=abc&ip=198.51.100.7&id=o-ABlender2160p&itag=313&source=youtube&gir=yes&clen=${fixtures.webmVideoBytes.byteLength}&c=TVHTML5&sparams=expire%2Cei%2Cip%2Cid%2Citag%2Csource%2Cgir%2Cclen&sig=DDD`;
    const itag140Url = `https://rr2---sn-o097znz7.googlevideo.com/videoplayback?expire=1794238131&ei=abc&ip=198.51.100.7&id=o-ABlenderAudio&itag=140&source=youtube&gir=yes&clen=${fixtures.m4aAudioBytes.byteLength}&c=TVHTML5&sparams=expire%2Cei%2Cip%2Cid%2Citag%2Csource%2Cgir%2Cclen&sig=EEE`;
    const itag251Url = `https://rr2---sn-o097znz7.googlevideo.com/videoplayback?expire=1794238131&ei=abc&ip=198.51.100.7&id=o-ABlenderOpus&itag=251&source=youtube&gir=yes&clen=${fixtures.webmAudioBytes.byteLength}&c=TVHTML5&sparams=expire%2Cei%2Cip%2Cid%2Citag%2Csource%2Cgir%2Cclen&sig=FFF`;

    const blenderData = {
      platform: "youtube",
      content_id: "u2qFOq_rVBM",
      source_url: "https://www.youtube.com/watch?v=u2qFOq_rVBM",
      generated_at: "2026-11-12T00:00:00Z",
      title: "THE DREAM - A Short Film Trailer I Blender",
      author: { name: "Blender Studio", id: "blender" },
      default_item_id: "video-u2qFOq_rVBM",
      extra: { youtube_helper: { status: "ready", version: "1.3.0" } },
      items: [
        {
          id: "video-u2qFOq_rVBM",
          type: "video",
          state: "ready",
          title: "THE DREAM - A Short Film Trailer I Blender",
          default: {
            variant_id: "v-18",
            source_id: "s-18",
            url: itag18Url,
            access: "direct",
          },
          variants: [
            {
              id: "v-313",
              label: "2160p 4K (仅视频, 无音频)",
              provenance: "original_endpoint",
              metadata: {
                width: 3840,
                height: 2160,
                fps: 24,
                has_audio: false,
                video_codec: "vp9",
                container: "webm",
                extension: "webm",
                mime_type: "video/webm",
                evidence: "upstream",
              },
              delivery: {
                kind: "file",
                sources: [
                  {
                    id: "s-313",
                    url: itag313Url,
                    access: "direct",
                    size_bytes: fixtures.webmVideoBytes.byteLength,
                  },
                ],
              },
            },
            {
              id: "v-137",
              label: "1080p (仅视频, 无音频)",
              provenance: "original_endpoint",
              metadata: {
                width: 1920,
                height: 1080,
                fps: 24,
                has_audio: false,
                video_codec: "h264",
                container: "mp4",
                extension: "mp4",
                mime_type: "video/mp4",
                evidence: "upstream",
              },
              delivery: {
                kind: "file",
                sources: [
                  {
                    id: "s-137",
                    url: itag137Url,
                    access: "direct",
                    size_bytes: fixtures.mp4VideoBytes.byteLength,
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
                fps: 24,
                has_audio: true,
                video_codec: "h264",
                audio_codec: "aac",
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
                    size_bytes: 2158619,
                  },
                ],
              },
            },
            {
              id: "v-140",
              label: "音轨 (M4A)",
              provenance: "original_endpoint",
              metadata: {
                has_audio: true,
                audio_codec: "aac",
                container: "mp4",
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
                    size_bytes: fixtures.m4aAudioBytes.byteLength,
                  },
                ],
              },
            },
            {
              id: "v-251",
              label: "音轨 (WebM)",
              provenance: "original_endpoint",
              metadata: {
                has_audio: true,
                audio_codec: "opus",
                container: "webm",
                extension: "webm",
                mime_type: "audio/webm",
                evidence: "upstream",
              },
              delivery: {
                kind: "file",
                sources: [
                  {
                    id: "s-251",
                    url: itag251Url,
                    access: "direct",
                    size_bytes: fixtures.webmAudioBytes.byteLength,
                  },
                ],
              },
            },
          ],
        },
      ],
      attachments: [],
    };

    env.store.setResult(blenderData);
    env.selection.syncResult();

    // 1. 2-file fallback on selected 1080p MP4 passes exact <title>_1080p.mp4 + <title>_audio.m4a and saves via OPFS
    env.selection.choose({ variant_id: "v-137", source_id: "s-137" });
    await env.downloader.downloadYouTubeSeparate();

    assert.deepEqual(
      bridgeDownloads.map((d) => ({ url: d.url, filename: d.filename })),
      [
        {
          url: itag137Url,
          filename: "THE DREAM - A Short Film Trailer I Blender_1080p.mp4",
        },
        {
          url: itag140Url,
          filename: "THE DREAM - A Short Film Trailer I Blender_audio.m4a",
        },
      ],
    );
    assert.deepEqual(
      opfsDownloads.map((d) => d.download),
      [
        "THE_DREAM_-_A_Short_Film_Trailer_I_Blender_1080p.mp4",
        "THE_DREAM_-_A_Short_Film_Trailer_I_Blender_audio.m4a",
      ],
    );
    assert.equal(env.store.downloads.length, 2);
    assert.equal(env.store.downloads[0].savePath, "opfs");
    assert.equal(env.store.downloads[1].savePath, "opfs");

    // 2. Explicit choice 2160p WebM while 360p is selected
    env.store.downloads.splice(0);
    bridgeDownloads.splice(0);
    opfsDownloads.splice(0);
    env.selection.choose({ variant_id: "v-18", source_id: "s-18" });
    await env.downloader.downloadYouTubeSeparate({
      variant_id: "v-313",
      source_id: "s-313",
    });

    assert.deepEqual(
      opfsDownloads.map((d) => d.download),
      [
        "THE_DREAM_-_A_Short_Film_Trailer_I_Blender_2160p.webm",
        "THE_DREAM_-_A_Short_Film_Trailer_I_Blender_audio.webm",
      ],
    );

    // 3. Video-track retry retries ONLY the failed video track without duplicating audio, and failed OPFS temp file was immediately cleaned up
    env.store.downloads.splice(0);
    bridgeDownloads.splice(0);
    opfsDownloads.splice(0);
    await new Promise((r) => setTimeout(r, 15));
    assert.equal(opfs.files.size, 0);

    failMp4Requests = true;
    env.selection.choose({ variant_id: "v-137", source_id: "s-137" });
    await env.downloader.downloadYouTubeSeparate();

    assert.equal(env.store.downloads.length, 2);
    assert.equal(env.store.downloads[0].status, "failed");
    assert.equal(env.store.downloads[1].status, "completed");

    failMp4Requests = false;
    opfsDownloads.splice(0);
    await env.downloader.handleRetryDownload(env.store.downloads[0].id);

    assert.equal(
      env.store.downloads.length,
      2,
      "retrying video track must not spawn a duplicate audio task",
    );
    assert.equal(env.store.downloads[0].status, "completed");
    assert.deepEqual(
      opfsDownloads.map((d) => d.download),
      ["THE_DREAM_-_A_Short_Film_Trailer_I_Blender_1080p.mp4"],
    );
  });

  test("OPFS quota headroom (< 1.2x -> showSaveFilePicker with id 'dokitool-youtube'), immediate temp-file deletion on cancel/error, and page-load leftover temp-file sweep", async () => {
    assert.equal(OPFS_QUOTA_HEADROOM_FACTOR, 1.2);
    assert.equal(YOUTUBE_FILE_PICKER_ID, "dokitool-youtube");

    // 1. Page-load sweep deletes dokitool-yt-tmp-* files while preserving unrelated OPFS files
    const sweepOpfs = createMockOpfsEnvironment({
      initialFiles: {
        [`${YOUTUBE_OPFS_TEMP_PREFIX}old-task-1.mp4.part`]: "leftover-1",
        [`${YOUTUBE_OPFS_TEMP_PREFIX}old-task-2.m4a.part`]: "leftover-2",
        "keep-other-data.json": '{"ok":true}',
      },
    });
    const removedCount = await cleanupLeftoverYouTubeOpfsTempFiles({
      storage: sweepOpfs.storage,
    });
    assert.equal(removedCount, 2);
    assert.deepEqual([...sweepOpfs.files.keys()], ["keep-other-data.json"]);

    // 2. Insufficient OPFS quota (remaining = 1.1x < 1.2x estimatedBytes) -> falls back to showSaveFilePicker({ id: "dokitool-youtube" })
    const totalEstimated =
      fixtures.mp4VideoBytes.byteLength + fixtures.m4aAudioBytes.byteLength;
    const tightOpfs = createMockOpfsEnvironment({
      quota: Math.floor(totalEstimated * 1.1),
      usage: 0,
    });
    const pickerInvocations = [];
    const tightBus = new EventTarget();
    const unmountTight = createUserscriptController({
      eventTarget: tightBus,
      version: YOUTUBE_HELPER_VERSION,
      mediabunny,
      muxConfig: { chunkSize: 4096, maxRetries: 1, retryBaseDelayMs: 5 },
      pageWindow: {
        navigator: { storage: tightOpfs.storage },
        async showSaveFilePicker(opts) {
          pickerInvocations.push(opts);
          return {
            name: opts.suggestedName,
            async createWritable() {
              return {
                async write() {},
                async close() {},
                async abort() {},
              };
            },
          };
        },
      },
      gmXmlhttpRequest(details) {
        const itag = new URL(details.url).searchParams.get("itag");
        const bytes =
          itag === "299" ? fixtures.mp4VideoBytes : fixtures.m4aAudioBytes;
        details.onload(sliceRangeFromFixture(bytes, details.url));
      },
    }).mount();

    try {
      const res = await sendHelperRequest(
        "mux_download",
        {
          taskId: "quota-check-1",
          videoUrl: makeItagUrl(299, fixtures.mp4VideoBytes.byteLength),
          audioUrl: makeItagUrl(140, fixtures.m4aAudioBytes.byteLength),
          videoSize: fixtures.mp4VideoBytes.byteLength,
          audioSize: fixtures.m4aAudioBytes.byteLength,
          filename: "Quota_Test_1080p.mp4",
          outputContainer: "mp4",
        },
        { eventTarget: tightBus },
      );
      assert.equal(res.mode, "stream_picker");
      assert.equal(pickerInvocations.length, 1);
      assert.equal(pickerInvocations[0].id, "dokitool-youtube");
      assert.equal(tightOpfs.files.size, 0);
    } finally {
      unmountTight();
    }

    // 3. Cancellation mid-mux immediately unlocks & deletes the OPFS temp file (zero leftover files or active locks)
    const cancelOpfs = createMockOpfsEnvironment({ quota: 500_000_000 });
    const cancelBus = new EventTarget();
    let pendingAudioLoad = null;
    const unmountCancel = createUserscriptController({
      eventTarget: cancelBus,
      version: YOUTUBE_HELPER_VERSION,
      mediabunny,
      muxConfig: {
        chunkSize: 4096,
        maxRetries: 0,
        retryBaseDelayMs: 5,
        opfsCleanupDelayMs: 60_000,
      },
      pageWindow: {
        navigator: { storage: cancelOpfs.storage },
        URL: { createObjectURL: () => "blob:unused", revokeObjectURL() {} },
        document: {
          body: { appendChild() {} },
          createElement: () => ({ style: {}, click() {}, remove() {} }),
        },
      },
      gmXmlhttpRequest(details) {
        const itag = new URL(details.url).searchParams.get("itag");
        if (itag === "140") {
          pendingAudioLoad = () =>
            details.onload({
              status: 403,
              responseHeaders: "",
              response: new ArrayBuffer(0),
            });
          return {
            abort() {
              details.onabort?.();
            },
          };
        }
        details.onload(sliceRangeFromFixture(fixtures.mp4VideoBytes, details.url));
        return {};
      },
    }).mount();

    try {
      const abortController = new AbortController();
      const muxPromise = sendHelperRequest(
        "mux_download",
        {
          taskId: "cancel-opfs-99",
          videoUrl: makeItagUrl(299, fixtures.mp4VideoBytes.byteLength),
          audioUrl: makeItagUrl(140, fixtures.m4aAudioBytes.byteLength),
          videoSize: fixtures.mp4VideoBytes.byteLength,
          audioSize: fixtures.m4aAudioBytes.byteLength,
          filename: "Cancel_Test_1080p.mp4",
          outputContainer: "mp4",
        },
        { eventTarget: cancelBus, signal: abortController.signal },
      );
      await new Promise((r) => setTimeout(r, 20));
      assert.equal(cancelOpfs.files.size, 1, "temp OPFS file must exist while muxing");
      const tempFileName = [...cancelOpfs.files.keys()][0];
      assert.match(tempFileName, /^dokitool-yt-tmp-cancel-opfs-99-/);

      abortController.abort();
      await assert.rejects(() => muxPromise, (err) => err.name === "AbortError");
      await new Promise((r) => setTimeout(r, 20));
      assert.equal(cancelOpfs.activeLocks.size, 0, "OPFS writable lock must be released immediately");
      assert.equal(cancelOpfs.files.size, 0, "OPFS temp file must be deleted immediately on cancel");

      // 4. Error mid-mux ALSO immediately unlocks & deletes OPFS temp file
      pendingAudioLoad = null;
      const errPromise = sendHelperRequest(
        "mux_download",
        {
          taskId: "error-opfs-100",
          videoUrl: makeItagUrl(299, fixtures.mp4VideoBytes.byteLength),
          audioUrl: makeItagUrl(140, fixtures.m4aAudioBytes.byteLength),
          videoSize: fixtures.mp4VideoBytes.byteLength,
          audioSize: fixtures.m4aAudioBytes.byteLength,
          filename: "Error_Test_1080p.mp4",
          outputContainer: "mp4",
        },
        { eventTarget: cancelBus },
      );
      await new Promise((r) => setTimeout(r, 20));
      assert.equal(cancelOpfs.files.size, 1);
      pendingAudioLoad?.();
      await assert.rejects(() => errPromise, /HTTP 403/);
      assert.equal(cancelOpfs.activeLocks.size, 0);
      assert.equal(cancelOpfs.files.size, 0, "OPFS temp file must be deleted immediately on error");
    } finally {
      unmountCancel();
    }
  });

  test("centered YouTube helper modal & sessionStorage install-return state machine: handshake on return, single auto-reload guard, URL restore + auto-re-parse, and manual refresh button when still missing", async () => {
    const env = setupEnvironment();
    const storageMap = new Map();
    const mockSessionStorage = {
      getItem: (k) => (storageMap.has(k) ? storageMap.get(k) : null),
      setItem: (k, v) => storageMap.set(k, String(v)),
      removeItem: (k) => storageMap.delete(k),
    };

    let helperReady = false;
    let reloadCount = 0;
    const pinger = async () =>
      helperReady
        ? { installed: true, version: "1.3.0", supported: true }
        : { installed: false, version: null, supported: false };

    const createTestParser = () =>
      parser.useVideoParser({
        videoStore: env.store,
        locale: ref("zh-CN"),
        showToast: (msg, type = "success") => env.toasts.push([msg, type]),
        sessionStorage: mockSessionStorage,
        pingHelper: pinger,
        reloadPage: () => {
          reloadCount += 1;
        },
      });

    // 1. Parse without helper -> renders centered modal (role="dialog", aria-modal="true", data-yt-helper-modal)
    globalThis.fetch = async (url) => {
      const u = new URL(url, "https://snap.dokitool.com");
      if (u.pathname === "/api/parse") {
        return Response.json({
          schema_version: "1.0",
          code: "OK",
          data: sampleYouTubeUnifiedData(),
        });
      }
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
        return Response.json({
          schema_version: "1.0",
          code: "OK",
          data: sampleYouTubeUnifiedData(),
        });
      }
      throw new Error(`unexpected ${u.pathname}`);
    };

    const unmountHelper = createUserscriptController({
      eventTarget: env.bus,
      version: "1.3.0",
      gmXmlhttpRequest(details) {
        if (details.url.includes("sw.js_data")) {
          details.onload({ status: 200, responseText: '["Cgt0ZXN0"]' });
          return;
        }
        details.onload({
          status: 200,
          responseText: JSON.stringify({
            playabilityStatus: { status: "OK" },
            videoDetails: {
              title: "Sample YouTube Video",
              author: "Sample Channel",
              lengthSeconds: "19",
              thumbnail: { thumbnails: [{ url: "https://i.ytimg.com/vi/I9JvDaciaFk/hqdefault.jpg" }] },
            },
            streamingData: { formats: [], adaptiveFormats: [] },
          }),
        });
      },
    }).mount();

    try {
      const p1 = createTestParser();
      p1.inputUrl.value = "https://www.youtube.com/watch?v=I9JvDaciaFk";
      await p1.parseVideo();

      const modalHtml = await renderToString(
        createSSRApp({
          render: () =>
            h(ResultSection, {
              resultData: env.store.resultData,
              resultVersion: env.store.resultVersion,
              locale: "zh-CN",
              selection: env.selection,
              needsManualHelperRefresh: p1.needsManualHelperRefresh.value,
              formatNumber: String,
              formatDuration: String,
            }),
        }),
      );
      assert.ok(modalHtml.includes("data-yt-helper-modal"));
      assert.ok(modalHtml.includes('role="dialog"'));
      assert.ok(modalHtml.includes('aria-modal="true"'));
      assert.ok(!modalHtml.includes("data-yt-helper-manual-refresh"));

      // 2. Click install button -> records waiting state + URL in sessionStorage
      p1.markWaitingForYouTubeHelperInstall();
      const savedRaw = mockSessionStorage.getItem(parser.YT_HELPER_INSTALL_WAIT_KEY);
      assert.ok(savedRaw);
      assert.equal(JSON.parse(savedRaw).reloaded, false);
      assert.equal(
        JSON.parse(savedRaw).url,
        "https://www.youtube.com/watch?v=I9JvDaciaFk",
      );

      // 3. Return to tab while Tampermonkey hasn't injected into existing page -> handshakes once, fails, auto-reloads ONCE
      await p1.handleYouTubeHelperTabReturn();
      assert.equal(reloadCount, 1);
      assert.equal(
        JSON.parse(mockSessionStorage.getItem(parser.YT_HELPER_INSTALL_WAIT_KEY)).reloaded,
        true,
      );

      // 4. Loop-guard check: if reloaded page STILL fails handshake -> NO second auto-reload, restores URL, and renders 刷新页面 button
      const pReloadedStillMissing = createTestParser();
      await pReloadedStillMissing.resumePendingYouTubeHelperInstall();
      assert.equal(reloadCount, 1, "must NOT auto-reload a second time");
      assert.equal(
        pReloadedStillMissing.inputUrl.value,
        "https://www.youtube.com/watch?v=I9JvDaciaFk",
      );
      assert.equal(pReloadedStillMissing.needsManualHelperRefresh.value, true);

      // Further tab focus events also do NOT trigger another reload loop
      await pReloadedStillMissing.handleYouTubeHelperTabReturn();
      assert.equal(reloadCount, 1);

      const fallbackModalHtml = await renderToString(
        createSSRApp({
          render: () =>
            h(ResultSection, {
              resultData: env.store.resultData,
              resultVersion: env.store.resultVersion,
              locale: "zh-CN",
              selection: env.selection,
              needsManualHelperRefresh:
                pReloadedStillMissing.needsManualHelperRefresh.value,
              formatNumber: String,
              formatDuration: String,
            }),
        }),
      );
      assert.ok(fallbackModalHtml.includes("data-yt-helper-manual-refresh"));
      assert.ok(fallbackModalHtml.includes("刷新页面"));

      // 5. Happy path after reload when helper v1.3.0 IS now active -> restores URL, clears sessionStorage, and auto-re-parses to ready!
      helperReady = true;
      mockSessionStorage.setItem(
        parser.YT_HELPER_INSTALL_WAIT_KEY,
        JSON.stringify({
          waiting: true,
          url: "https://www.youtube.com/watch?v=I9JvDaciaFk",
          platform: "all",
          reloaded: true,
          updatedAt: Date.now(),
        }),
      );
      env.store.clearResult();
      const pReadyAfterReload = createTestParser();
      await pReadyAfterReload.resumePendingYouTubeHelperInstall();
      assert.equal(
        pReadyAfterReload.inputUrl.value,
        "https://www.youtube.com/watch?v=I9JvDaciaFk",
      );
      assert.equal(
        mockSessionStorage.getItem(parser.YT_HELPER_INSTALL_WAIT_KEY),
        null,
      );
      assert.equal(pReadyAfterReload.needsManualHelperRefresh.value, false);
      assert.equal(env.store.resultData?.extra?.youtube_helper?.status, "ready");
    } finally {
      unmountHelper();
    }
  });
});
