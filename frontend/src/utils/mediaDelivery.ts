import type {
  Chunk,
  LeafDelivery,
  MediaMetadata,
  Resource,
  Source,
} from "../types/media";
import {
  sourceAvailable,
  sourceUrl,
  type TargetContext,
} from "./mediaContract";
import { resolveHlsSegments, type HlsSegment } from "./segmentDownload";

export function declaredSource(
  sources: Source[],
  context: TargetContext = {},
): Source {
  const source = sources.find((candidate) =>
    sourceAvailable(candidate, context.now),
  );
  if (!source) throw new Error("资源没有可用来源，请重新解析");
  return source;
}
/** Extensions describe the declared media, never a signed URL's pathname. */
export function mediaExtension(
  metadata?: MediaMetadata,
  delivery?: LeafDelivery,
  observedMimeType?: string,
): string {
  if (delivery && "output" in delivery && delivery.output?.extension)
    return delivery.output.extension;
  if (metadata?.extension) return metadata.extension;
  const extensions: Record<string, string> = {
    "image/jpeg": "jpg",
    "image/png": "png",
    "image/webp": "webp",
    "image/gif": "gif",
    "image/avif": "avif",
    "video/mp4": "mp4",
    "audio/mp4": "m4a",
    "audio/mpeg": "mp3",
    "video/webm": "webm",
  };
  const mimeType = metadata?.mime_type || observedMimeType || "";
  const byMime = extensions[mimeType.split(";")[0].trim().toLowerCase()];
  if (byMime) return byMime;
  const containers: Record<string, string> = {
    mp4: "mp4",
    m4a: "m4a",
    mp3: "mp3",
    webm: "webm",
    mov: "mov",
    mkv: "mkv",
  };
  const container = (metadata?.container || "").trim().toLowerCase();
  return containers[container] || "bin";
}
export async function declaredSegments(
  delivery: LeafDelivery,
  context: TargetContext,
  signal?: AbortSignal,
): Promise<HlsSegment[]> {
  if (delivery.kind === "segments") {
    if (
      delivery.join !== "byte_concat" ||
      delivery.join_evidence !== "validated_fragment_layout"
    )
      throw new Error("该版本需要重封装，当前暂不支持");
    const chunks: Chunk[] = [
      ...(delivery.init ? [delivery.init] : []),
      ...delivery.pieces,
    ];
    return chunks.map((chunk) => ({
      url: sourceUrl(declaredSource(chunk.sources, context), context),
      ...(chunk.byte_range
        ? {
            start: chunk.byte_range.start,
            length: chunk.byte_range.end_inclusive - chunk.byte_range.start + 1,
          }
        : {}),
    }));
  }
  if (delivery.kind === "hls") {
    if (delivery.scope !== "single")
      throw new Error("自适应 HLS 需要服务端选定完整清单，当前暂不支持");
    for (const source of delivery.sources.filter((source) =>
      sourceAvailable(source, context.now),
    )) {
      const segments = await resolveHlsSegments(
        sourceUrl(source, context),
        signal,
      );
      if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
      if (segments) return segments;
    }
    throw new Error("HLS 清单不可读取，或需要当前不支持的处理能力");
  }
  throw new Error("该资源不支持分段交付");
}
export function fileResource(resource: Resource, context: TargetContext = {}) {
  if (resource.delivery.kind !== "file")
    throw new Error("该资源需要处理，当前不能作为完整文件下载");
  const source = declaredSource(resource.delivery.sources, context);
  const url = sourceUrl(source, context);
  return {
    source,
    url,
    routes: [
      url,
      ...resource.delivery.sources
        .filter(
          (other) => other !== source && sourceAvailable(other, context.now),
        )
        .map((other) => sourceUrl(other, context)),
    ],
  };
}
