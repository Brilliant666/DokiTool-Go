import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import * as esbuild from "esbuild";
import { YOUTUBE_HELPER_VERSION } from "../src/utils/youtubeHelperCore.js";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const frontendRoot = path.resolve(scriptDir, "..");
const entryPath = path.join(
  frontendRoot,
  "src",
  "utils",
  "youtubeHelperEntry.js",
);
const outputPath = path.join(
  frontendRoot,
  "public",
  "helper",
  "dokitool-youtube.user.js",
);

export function buildUserscriptHeader(version = YOUTUBE_HELPER_VERSION) {
  return [
    "// ==UserScript==",
    "// @name         DokiTool YouTube Helper",
    "// @namespace    https://snap.dokitool.com/",
    `// @version      ${version}`,
    "// @description  DokiTool YouTube 用户侧取址、浏览器内无损合流与保存助手（无 Cookie、不碰账号）",
    "// @author       DokiTool",
    "// @match        https://snap.dokitool.com/*",
    "// @connect      www.youtube.com",
    "// @connect      googlevideo.com",
    "// @grant        GM_xmlhttpRequest",
    "// @grant        GM_download",
    "// @noframes",
    "// @run-at       document-start",
    "// @updateURL    https://snap.dokitool.com/helper/dokitool-youtube.user.js",
    "// @downloadURL  https://snap.dokitool.com/helper/dokitool-youtube.user.js",
    "// ==/UserScript==",
    "/*!",
    ` * DokiTool YouTube Helper v${version}`,
    " * Bundles unmodified Mediabunny v1.65.2 (Mozilla Public License 2.0, Copyright (c) Vanilagy).",
    " * Mediabunny Source Code Form: https://github.com/Vanilagy/mediabunny/tree/v1.65.2",
    " * Full MPL-2.0 license text: https://snap.dokitool.com/licenses/mediabunny-MPL-2.0.txt",
    " */",
    "",
  ].join("\n");
}

export async function generateYouTubeHelperCode() {
  const result = await esbuild.build({
    entryPoints: [entryPath],
    bundle: true,
    format: "iife",
    platform: "browser",
    target: "es2022",
    minify: true,
    legalComments: "inline",
    charset: "utf8",
    write: false,
  });

  const bundled = result.outputFiles?.[0]?.text;
  if (!bundled) {
    throw new Error("esbuild produced empty output for dokitool-youtube.user.js");
  }
  return buildUserscriptHeader(YOUTUBE_HELPER_VERSION) + bundled.trimEnd() + "\n";
}

export async function buildYouTubeHelper() {
  const code = await generateYouTubeHelperCode();
  fs.mkdirSync(path.dirname(outputPath), { recursive: true });
  fs.writeFileSync(outputPath, code, "utf8");
  return { outputPath, byteLength: Buffer.byteLength(code, "utf8") };
}

const isMain =
  process.argv[1] &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);

if (isMain) {
  buildYouTubeHelper().catch((err) => {
    console.error(err);
    process.exit(1);
  });
}
