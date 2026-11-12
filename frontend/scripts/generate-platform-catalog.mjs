import { readFile, writeFile, mkdir } from "node:fs/promises";
const registry = JSON.parse(
  await readFile(
    new URL("../../backend/config/platforms.json", import.meta.url),
    "utf8",
  ),
);
const onlineExtras = [
  {
    id: "x",
    name: "X",
    site_url: "https://x.com/",
    icon: "x",
    capabilities: ["video", "image"],
  },
  {
    id: "youtube",
    name: "YouTube",
    site_url: "https://www.youtube.com/",
    icon: "youtube",
    capabilities: ["video"],
  },
  {
    id: "instagram",
    name: "Instagram",
    site_url: "https://www.instagram.com/",
    icon: "instagram",
    capabilities: ["video", "image"],
  },
  {
    id: "facebook",
    name: "Facebook",
    site_url: "https://www.facebook.com/",
    icon: "facebook",
    capabilities: ["video"],
  },
  {
    id: "tumblr",
    name: "Tumblr",
    site_url: "https://www.tumblr.com/",
    icon: "tumblr",
    capabilities: ["video"],
  },
  {
    id: "dailymotion",
    name: "Dailymotion",
    site_url: "https://www.dailymotion.com/",
    icon: "dailymotion",
    capabilities: ["video"],
  },
];
const seenIds = new Set(registry.map(({ id }) => id));
const platforms = [
  ...registry,
  ...onlineExtras.filter(({ id }) => !seenIds.has(id)),
].map(
  ({ id, name, site_url, icon, capabilities }) => ({
    id,
    name,
    site_url,
    ...(icon ? { icon } : {}),
    capabilities,
  }),
);
const output = new URL(
  "../src/generated/platform-catalog.json",
  import.meta.url,
);
await mkdir(new URL("../src/generated/", import.meta.url), { recursive: true });
await writeFile(
  output,
  JSON.stringify(
    { schema_version: "1.0", code: "OK", data: { platforms } },
    null,
    2,
  ) + "\n",
);
