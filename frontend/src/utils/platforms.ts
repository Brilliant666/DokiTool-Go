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

export const POPULAR_PLATFORMS: ReadonlyArray<{
  id: (typeof POPULAR_PLATFORM_IDS)[number];
  name: string;
  icon: string;
}> = [
  { id: "douyin", name: "抖音", icon: "douyin" },
  { id: "kuaishou", name: "快手", icon: "kuaishou" },
  { id: "bilibili", name: "B站", icon: "bilibili" },
  { id: "xiaohongshu", name: "小红书", icon: "xiaohongshu" },
  { id: "weibo", name: "微博", icon: "weibo" },
  { id: "x", name: "X", icon: "x" },
  { id: "youtube", name: "YouTube", icon: "youtube" },
  { id: "instagram", name: "Instagram", icon: "instagram" },
  { id: "facebook", name: "Facebook", icon: "facebook" },
];

export interface PlatformTabItem {
  key: string;
  label: string;
  icon: string;
  disabled?: boolean;
}

export function platformTabs(locale: string): PlatformTabItem[] {
  const enabledIds = new Set(supportedPlatforms.value.map((p) => p.id));
  return [
    {
      key: "all",
      label: locale === "en" ? "All" : "所有平台",
      icon: "fa-globe",
      disabled: false,
    },
    ...POPULAR_PLATFORMS.map((p) => ({
      key: p.id,
      label: locale === "en" ? p.id : p.name,
      icon: tabIcons[p.icon] || "fa-globe",
      disabled: !enabledIds.has(p.id),
    })),
  ];
}
