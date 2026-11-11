import {
  BufferTarget,
  CustomSource,
  EncodedAudioPacketSource,
  EncodedPacketSink,
  EncodedVideoPacketSource,
  Input,
  MP4,
  Mp4OutputFormat,
  Output,
  StreamTarget,
  WEBM,
  WebMOutputFormat,
} from "mediabunny";
import {
  YOUTUBE_HELPER_VERSION,
  createUserscriptController,
} from "./youtubeHelperCore.js";

const gmXhr =
  typeof GM_xmlhttpRequest === "function" ? GM_xmlhttpRequest : undefined;
const gmDl = typeof GM_download === "function" ? GM_download : undefined;
const targetWindow = typeof window !== "undefined" ? window : globalThis;
const pageWindow =
  (typeof document !== "undefined" && document?.defaultView) || targetWindow;

const controller = createUserscriptController({
  gmXmlhttpRequest: gmXhr,
  gmDownload: gmDl,
  eventTarget: targetWindow,
  pageWindow,
  version: YOUTUBE_HELPER_VERSION,
  mediabunny: {
    BufferTarget,
    CustomSource,
    EncodedAudioPacketSource,
    EncodedPacketSink,
    EncodedVideoPacketSource,
    Input,
    MP4,
    Mp4OutputFormat,
    Output,
    StreamTarget,
    WEBM,
    WebMOutputFormat,
  },
});

controller.mount();
