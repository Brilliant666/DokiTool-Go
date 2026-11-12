import { shallowRef } from "vue";
import snapshot from "../generated/platform-catalog.json";
import { apiConfig } from "../services/apiConfig";
export interface Platform {
  id: string;
  name: string;
  site_url: string;
  icon?: string;
  capabilities: string[];
}
export function readPlatformCatalog(value: unknown): Platform[] {
  const envelope = value as {
    schema_version?: string;
    code?: string;
    data?: { platforms?: Platform[] };
  };
  if (
    envelope?.schema_version !== "1.0" ||
    envelope.code !== "OK" ||
    !Array.isArray(envelope.data?.platforms)
  )
    throw new Error("平台目录格式无效");
  const platforms = envelope.data.platforms;
  if (
    new Set(platforms.map((platform) => platform.id)).size !==
      platforms.length ||
    platforms.some(
      (platform) =>
        !/^[a-z][a-z0-9_-]*$/.test(platform.id) ||
        !platform.name ||
        !Array.isArray(platform.capabilities) ||
        !/^https?:\/\//.test(platform.site_url),
    )
  )
    throw new Error("平台目录无效");
  return platforms;
}
export const supportedPlatforms = shallowRef(readPlatformCatalog(snapshot));
export async function refreshPlatformCatalog() {
  try {
    const response = await fetch(`${apiConfig.catalogUrl}?schema_version=1.0`, {
      signal: AbortSignal.timeout(10000),
    });
    if (response.ok)
      supportedPlatforms.value = readPlatformCatalog(await response.json());
  } catch {
    /* The backend-generated build snapshot keeps the homepage usable offline. */
  }
}
// Optional presentation assets do not register parsers or affect parsing behavior.
const tabIcons: Record<string, string> = {
  douyin: "fa-music",
  kuaishou: "fa-bolt",
  bilibili: "fa-play-circle",
  xiaohongshu: "fa-bookmark",
  toutiao: "fa-newspaper",
  weibo: "fa-comment",
  pipixia: "fa-face-laugh",
  pipigx: "fa-face-smile",
  zuiyou: "fa-comments",
  x: "fa-x-twitter",
  youtube: "fa-youtube",
  instagram: "fa-instagram",
  facebook: "fa-facebook",
  tumblr: "fa-tumblr",
  dailymotion: "fa-dailymotion",
};
export const POPULAR_PLATFORM_IDS = [
  "douyin",
  "kuaishou",
  "bilibili",
  "xiaohongshu",
  "weibo",
  "x",
  "youtube",
  "instagram",
  "facebook",
] as const;

const popularOrder = new Map<string, number>(
  POPULAR_PLATFORM_IDS.map((id, idx) => [id, idx]),
);

export interface PlatformTabItem {
  key: string;
  label: string;
  icon: string;
}

function toPlatformTab(platform: Platform, locale: string): PlatformTabItem {
  return {
    key: platform.id,
    label: locale === "en" ? platform.id : platform.name,
    icon: tabIcons[platform.icon || ""] || "fa-globe",
  };
}

export function groupedPlatformTabs(locale: string): {
  allTab: PlatformTabItem;
  popularTabs: PlatformTabItem[];
  moreTabs: PlatformTabItem[];
} {
  const byId = new Map(supportedPlatforms.value.map((p) => [p.id, p]));
  const popularTabs: PlatformTabItem[] = [];
  for (const id of POPULAR_PLATFORM_IDS) {
    const p = byId.get(id);
    if (p) popularTabs.push(toPlatformTab(p, locale));
  }
  const moreTabs = supportedPlatforms.value
    .filter((p) => !popularOrder.has(p.id))
    .map((p) => toPlatformTab(p, locale));
  return {
    allTab: {
      key: "all",
      label: locale === "en" ? "All" : "所有平台",
      icon: "fa-globe",
    },
    popularTabs,
    moreTabs,
  };
}

export function platformTabs(locale: string): PlatformTabItem[] {
  const { allTab, popularTabs, moreTabs } = groupedPlatformTabs(locale);
  return [allTab, ...popularTabs, ...moreTabs];
}
