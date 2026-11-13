import type { Variant } from "../types/media";

/** Facts only: keep conflicting layers visible instead of inventing a combined ranking. */
export function variantSpecifications(variant: Variant): string[] {
  if (variant.state === "deferred") return [];
  const layers = [
    variant.metadata,
    ...("resources" in variant.delivery ? variant.delivery.resources.map((resource) => resource.metadata) : []),
  ];
  const hdrLabels = {
    sdr: "SDR", hdr10: "HDR10", hdr10_plus: "HDR10+",
    hlg: "HLG", dolby_vision: "Dolby Vision", hdr_unknown: "HDR",
  };
  const specifications = layers.flatMap((metadata) => metadata ? [
      metadata.width && metadata.height ? `${metadata.width}×${metadata.height}` : metadata.width ? `${metadata.width}w` : "",
      metadata.fps ? `${metadata.fps} fps` : "",
      metadata.hdr_type ? hdrLabels[metadata.hdr_type] : "",
      metadata.video_codec || "",
      metadata.audio_language || "",
      metadata.audio_channels ? `${metadata.audio_channels} ch` : "",
    ].filter(Boolean) : [],
  );
  return [...new Set(specifications)];
}

/** Presentation only: never selects a default or merges delivery identities. */
export function groupVariantsForDisplay(variants: Variant[]): Variant[][] {
  const groups = new Map<string, Variant[]>();
  for (const variant of variants) {
    const metadata = variant.state === "deferred" ? undefined : variant.metadata;
    const canGroup =
      variant.state !== "deferred" &&
      variant.delivery.kind === "file" &&
      variant.provenance !== "original_endpoint" &&
      metadata?.width && metadata.height;
    const key = canGroup
      ? JSON.stringify(["resolution", metadata.width, metadata.height])
      : JSON.stringify(["variant", variant.id]);
    const group = groups.get(key);
    if (group) group.push(variant);
    else groups.set(key, [variant]);
  }
  return [...groups.values()];
}
const WEIBO_QUALITY_LABELS: Record<string, [string, string]> = {
  mp4_8k_mp4: ["8K", "8K"],
  hevc_mp4_8k: ["8K", "8K"],
  h265_mp4_8k: ["8K", "8K"],
  mp4_4k_mp4: ["4K", "4K"],
  hevc_mp4_4k: ["4K", "4K"],
  h265_mp4_4k: ["4K", "4K"],
  mp4_2k_mp4: ["2K", "2K"],
  hevc_mp4_2k: ["2K", "2K"],
  h265_mp4_2k: ["2K", "2K"],
  mp4_1080p_mp4: ["1080p", "1080p"],
  mp4_1080p: ["1080p", "1080p"],
  hevc_mp4_1080p: ["1080p", "1080p"],
  h265_mp4_1080p: ["1080p", "1080p"],
  mp4_720p_mp4: ["720p", "720p"],
  mp4_720p: ["720p", "720p"],
  hevc_mp4_720p: ["720p", "720p"],
  h265_mp4_720p: ["720p", "720p"],
  mp4_hd: ["高清", "HD"],
  mp4_hd_mp4: ["高清", "HD"],
  mp4_hd_url: ["高清", "HD"],
  hevc_mp4_hd: ["高清", "HD"],
  h265_mp4_hd: ["高清", "HD"],
  inch_4_mp4_hd: ["高清", "HD"],
  inch_5_mp4_hd: ["高清", "HD"],
  inch_5_5_mp4_hd: ["高清", "HD"],
  stream_url_hd: ["高清", "HD"],
  mp4_sd: ["标清", "SD"],
  mp4_sd_mp4: ["标清", "SD"],
  mp4_sd_url: ["标清", "SD"],
  stream_url: ["标清", "SD"],
  mp4_ld: ["流畅", "LD"],
  mp4_ld_mp4: ["流畅", "LD"],
  hevc_mp4_ld: ["流畅", "LD"],
  h265_mp4_ld: ["流畅", "LD"],
};

function normalizeRawVariantLabel(raw: string, locale: string): string {
  const mapped = WEIBO_QUALITY_LABELS[raw.trim().toLowerCase()];
  if (mapped) return locale === "en" ? mapped[1] : mapped[0];
  const resMatch = /^(?:hevc_|h265_)?mp4_(\d{3,4}p|[248]k)(?:_mp4)?$/i.exec(
    raw.trim(),
  );
  if (resMatch) {
    const token = resMatch[1];
    return /k$/i.test(token) ? token.toUpperCase() : token.toLowerCase();
  }
  return raw;
}

/** Display backend metadata without ranking or changing the selected version. */
export function variantLabel(
  variant: Variant | undefined,
  locale = "zh-CN",
): string {
  if (!variant) return locale === "en" ? "Unknown quality" : "画质未知";
  if (variant.label) return normalizeRawVariantLabel(variant.label, locale);
  const { width, height } =
    variant.state === "deferred" ? {} : variant.metadata || {};
  if (width && height) return `${Math.min(width, height)}p`;
  if (width) return `${width}w`;
  return locale === "en" ? "Unknown quality" : "画质未知";
}
export function formatMediaSize(size: unknown, locale = "zh-CN"): string {
  const bytes =
    typeof size === "number" || (typeof size === "string" && size.trim())
      ? Number(size)
      : NaN;
  if (!Number.isSafeInteger(bytes) || bytes <= 0)
    return locale === "en" ? "Size unknown" : "大小未知";
  if (bytes < 100000) return "<0.1 MB";
  return `${(bytes / 1000000).toFixed(1)} MB`;
}
