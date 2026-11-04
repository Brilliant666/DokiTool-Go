import type { MediaItem, UnifiedMedia } from "../types/media";

export interface TbvideoItem {
  platform: "pipigx" | "zuiyou";
  pid: string;
  vid: string;
  sources: Record<string, string>;
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function objectURL(raw: unknown): raw is string {
  if (typeof raw !== "string") return false;
  try {
    const url = new URL(raw);
    return url.origin === "https://tbvideo.ixiaochuan.cn" && raw === url.origin + url.pathname && url.pathname.length > 1;
  } catch { return false; }
}

/** Bind public download hints to the item from this particular parse/resolve response.
 * Empty/missing hints still select manual delivery. Preview reads the original source. */
export function withTbvideoItems(media: UnifiedMedia): UnifiedMedia {
  const platform = media.platform;
  if (platform !== "pipigx" && platform !== "zuiyou") return media;
  const extra = media.extra?.tbvideo;
  const items = record(extra) && record(extra.items) ? extra.items : {};
  const pid = record(extra) && typeof extra.pid === "string" ? extra.pid : media.content_id || "";
  return { ...media, items: media.items.map((item: MediaItem) => {
    if (item.type !== "video") return item;
    const entry = Object.hasOwn(items, item.id) ? items[item.id] : undefined;
    const sources = record(entry) && record(entry.sources) ? entry.sources : {};
    const vid = record(entry) && typeof entry.vid === "string" ? entry.vid : "";
    return { ...item, tbvideo: { platform, pid, vid,
      sources: Object.fromEntries(Object.entries(sources).filter((pair): pair is [string, string] => objectURL(pair[1]))),
    } };
  }) };
}
