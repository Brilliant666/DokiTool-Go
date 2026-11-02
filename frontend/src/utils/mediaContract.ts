import type {
  Capability,
  Delivery,
  MediaItem,
  MediaSelection,
  ReadyItem,
  Resource,
  Source,
  UnifiedMedia,
  Variant,
  ReadyVariant,
} from "../types/media";

export interface TargetContext {
  apiOrigin?: string;
  now?: number;
}
export type DownloadTarget =
  | {
      kind: "file";
      item: MediaItem;
      variant: ReadyVariant;
      source: Source;
      url: string;
      routes: string[];
    }
  | {
      kind: "hls" | "segments" | "pair";
      item: MediaItem;
      variant: ReadyVariant;
    }
  | {
      kind:
        | "needs_resolve"
        | "needs_refresh"
        | "unavailable"
        | "unsupported"
        | "invalid";
      message: string;
      requires?: Capability[];
    };
const fail = (message: string): never => {
  throw new Error(`媒体合同无效：${message}`);
};
const object = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === "object" && !Array.isArray(value);
const nonempty = (value: unknown): value is string =>
  typeof value === "string" && !!value.trim();
const identity = (value: unknown) =>
  nonempty(value) && !/[\x00-\x1f\x7f]/.test(value);
const unique = (values: string[], label: string) => {
  if (new Set(values).size !== values.length) fail(`${label}重复`);
};
export const readyVariant = (
  variant?: Variant | null,
): variant is ReadyVariant => !!variant && variant.state !== "deferred";
export const readyItem = (item?: MediaItem | null): item is ReadyItem =>
  !!item && (!item.state || item.state === "ready");

/** Preserve signed query bytes. Only relay paths are decoded for traversal checks. */
export function sourceUrl(
  source?: Source,
  context: TargetContext = {},
): string {
  if (!source) return "";
  const raw = source.url;
  if (!nonempty(raw) || /[\s\\\x00-\x1f\x7f]/.test(raw)) fail("资源地址不安全");
  if (source.access === "relay") {
    if (!raw.startsWith("/api/") || raw.includes("#"))
      fail("中转地址必须位于 API 路径");
    const path = raw.split("?")[0];
    let decoded: string;
    try {
      decoded = path;
      for (let depth = 0; depth < 6; depth++) {
        const next = decodeURIComponent(decoded);
        if (next === decoded) break;
        decoded = next;
      }
      if (/%[0-9a-f]{2}/i.test(decoded)) fail("中转路径多重编码");
    } catch {
      return fail("中转路径编码无效");
    }
    if (
      decoded.includes("\\") ||
      decoded.split("/").some((part) => part === "." || part === "..") ||
      /[\x00-\x20\x7f]/.test(decoded)
    )
      fail("中转路径不安全");
    const origin =
      context.apiOrigin ||
      (typeof location === "undefined"
        ? "https://dokitool.invalid"
        : location.origin);
    const parsedOrigin = new URL(origin);
    if (
      !/^https?:\/\/[^/?#]+\/?$/i.test(origin) ||
      /[\\\s]/.test(origin) ||
      !["http:", "https:"].includes(parsedOrigin.protocol) ||
      parsedOrigin.username ||
      parsedOrigin.password ||
      parsedOrigin.pathname !== "/" ||
      parsedOrigin.search ||
      parsedOrigin.hash
    )
      fail("API origin 无效");
    return parsedOrigin.origin + raw;
  }
  if (source.access && source.access !== "direct") fail("未知访问方式");
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    return fail("资源必须是完整 HTTP 地址");
  }
  if (
    !/^https?:\/\//i.test(raw) ||
    !["http:", "https:"].includes(parsed.protocol) ||
    parsed.username ||
    parsed.password ||
    parsed.hash
  )
    fail("资源地址不安全");
  return raw;
}
export function sourceAvailable(source: Source, now = Date.now()): boolean {
  return (
    source.availability !== "unavailable" &&
    (!source.expires_at || Date.parse(source.expires_at) > now)
  );
}
export function requiredCapabilities(delivery: Delivery): Capability[] {
  if (delivery.kind === "file") return [];
  if (delivery.kind === "hls" || delivery.kind === "dash")
    return [delivery.kind];
  if (delivery.kind === "segments")
    return [
      delivery.join === "byte_concat" ? "segment_byte_concat" : "segment_remux",
    ];
  if (delivery.kind === "mux" || delivery.kind === "pair")
    return [
      ...new Set<Capability>([
        delivery.kind,
        ...delivery.resources.flatMap((resource) =>
          requiredCapabilities(resource.delivery),
        ),
      ]),
    ];
  return fail("未知媒体交付能力");
}
export function sourcesOf(delivery: Delivery): Source[] {
  if ("sources" in delivery) return delivery.sources;
  if (delivery.kind === "segments")
    return [
      ...(delivery.init?.sources || []),
      ...delivery.pieces.flatMap((piece) => piece.sources),
    ];
  return delivery.resources.flatMap((resource) => sourcesOf(resource.delivery));
}
/** Server-attested alternatives retain selection and are tried only on transport failure. */
export function sourceRoutes(source: Source, context: TargetContext = {}): string[] {
  const primary = sourceUrl(source, context);
  if (source.fallback_urls === undefined) return [primary];
  if (!Array.isArray(source.fallback_urls) || source.fallback_urls.length < 1 || source.fallback_urls.length > 2 || source.access === "relay") fail("来源凭据候选无效");
  const base = new URL(primary);
  if (base.protocol !== "https:") fail("来源凭据候选必须使用 HTTPS");
  const routes = [primary];
  for (const raw of source.fallback_urls) {
    const route = sourceUrl({url: raw}, context);
    if (new URL(route).origin !== base.origin || routes.includes(route)) fail("来源凭据候选跨主机或重复");
    routes.push(route);
  }
  return routes;
}

function validateSources(value: unknown): asserts value is Source[] {
  if (!Array.isArray(value) || !value.length) fail("来源不能为空");
  const sources = value as Source[];
  sources.forEach((source) => {
    if (!object(source) || !identity(source.url)) fail("来源无效");
    sourceRoutes(source);
    if (source.id !== undefined && !identity(source.id)) fail("来源 ID 无效");
    if (sources.length > 1 && !source.id) fail("多来源必须有 ID");
    if (
      source.expires_at !== undefined &&
      !Number.isFinite(Date.parse(source.expires_at))
    )
      fail("到期时间无效");
    if (
      source.availability &&
      !["unknown", "available", "unavailable"].includes(source.availability)
    )
      fail("来源可用性无效");
    if (
      source.size_bytes !== undefined &&
      (!Number.isSafeInteger(source.size_bytes) || source.size_bytes <= 0)
    )
      fail("文件大小无效");
    if ("headers" in source || "cookie" in source) fail("来源不能携带请求凭据");
  });
  unique(
    sources.filter((source) => source.id).map((source) => source.id!),
    "来源 ID",
  );
}
const numericMetadata = new Set(["width", "height", "duration_ms", "fps", "bitrate_bps", "video_bitrate_bps", "audio_bitrate_bps", "audio_channels", "sample_rate_hz"]);
const textMetadata = new Set(["video_codec", "audio_codec", "audio_language", "container", "extension", "mime_type"]);
function validateMetadataConflicts(metadata: Record<string, unknown>, role?: Resource["role"]): string[] {
  if (metadata.field_conflicts === undefined) return [];
  if (!object(metadata.field_conflicts) || !Object.keys(metadata.field_conflicts).length) return fail("媒体冲突规格无效");
  const fields = Object.keys(metadata.field_conflicts);
  for (const field of fields) {
    const facts = metadata.field_conflicts[field];
    if (metadata[field] !== undefined || !Array.isArray(facts) || facts.length < 2) fail("冲突规格不能同时声明确定值");
    const values = [];
    for (const fact of facts as unknown[]) {
      if (!object(fact) || !["upstream", "probe"].includes(fact.evidence as string)
        || Object.keys(fact).some((key) => !["value", "evidence"].includes(key))) return fail("规格冲突缺少可靠证据");
      const value = fact.value;
      const valid = numericMetadata.has(field) ? typeof value === "number" && Number.isFinite(value) && value > 0 && (field === "fps" || Number.isInteger(value))
        : textMetadata.has(field) ? nonempty(value) && (field !== "extension" || /^[a-z0-9]+$/.test(value)) && (field !== "mime_type" || /^[a-z0-9.+-]+\/[a-z0-9.+-]+$/.test(value))
        : field === "has_audio" ? typeof value === "boolean"
        : field === "hdr_type" ? ["sdr", "hdr10", "hdr10_plus", "hlg", "dolby_vision", "hdr_unknown"].includes(value as string)
        : field === "audio_language_role" && ["main", "alternate"].includes(value as string);
      if (!valid) fail("规格冲突值无效");
      if (field === "has_audio" && ["video", "image", "subtitle"].includes(role || "") && value === true) fail("无音频资源不能声明内嵌音轨");
      if (field === "mime_type" && ["image", "audio"].includes(role || "") && !(value as string).startsWith(`${role}/`)) fail("媒体类型与资源角色不符");
      values.push(JSON.stringify(value));
    }
    unique(values, "规格冲突值");
  }
  if (fields.includes("audio_language_role") && !nonempty(metadata.audio_language) && !fields.includes("audio_language")) fail("音轨语言角色缺少有效语言");
  if (role === "audio" && fields.some((field) => ["width", "height", "fps", "hdr_type", "video_codec", "video_bitrate_bps"].includes(field))) fail("音频资源不能声明视频规格");
  if ((metadata.has_audio === false || ["video", "image", "subtitle"].includes(role || ""))
    && fields.some((field) => ["audio_codec", "audio_language", "audio_language_role", "audio_channels", "sample_rate_hz", "audio_bitrate_bps"].includes(field))) fail("无音频资源不能声明音轨规格");
  return fields;
}
/** Check each layer's facts independently; conflicting layers remain diagnostic data. */
function validateCapabilityMetadata(value: unknown, role?: Resource["role"]): void {
  if (value === undefined) return;
  if (!object(value)) return fail("媒体规格无效");
  const conflicts = validateMetadataConflicts(value, role);
  if (
    value.hdr_type !== undefined &&
    !["sdr", "hdr10", "hdr10_plus", "hlg", "dolby_vision", "hdr_unknown"].includes(value.hdr_type as string)
  )
    fail("HDR 类型无效");
  if (value.audio_language !== undefined && !nonempty(value.audio_language))
    fail("音轨语言无效");
  if (
    value.audio_language_role !== undefined &&
    (!["main", "alternate"].includes(value.audio_language_role as string) || (!nonempty(value.audio_language) && !conflicts.includes("audio_language")))
  )
    fail("音轨语言角色缺少有效语言或类型");
  const facts = ["hdr_type", "audio_language", "audio_language_role"].filter(
    (field) => value[field] !== undefined,
  );
  if (!facts.length) return;
  if (!["upstream", "probe", "mixed"].includes(value.evidence as string)) fail("媒体能力缺少可靠证据");
  if (value.evidence === "mixed") {
    const technical = Object.keys(value).filter(
      (field) => !["container", "extension", "mime_type", "evidence", "field_evidence", "field_conflicts"].includes(field),
    );
    for (const field of technical) {
      if (!object(value.field_evidence) || !["upstream", "probe"].includes(value.field_evidence[field] as string))
        fail("媒体能力字段缺少可靠证据");
    }
  }
  if (role === "audio" && value.hdr_type !== undefined) fail("音频资源不能声明 HDR");
  if (
    (value.has_audio === false || ["video", "image", "subtitle"].includes(role || "")) &&
    (value.audio_language !== undefined || value.audio_language_role !== undefined)
  )
    fail("无音频资源不能声明音轨语言");
}
function validateDelivery(delivery: Delivery): void {
  if (!object(delivery)) fail("缺少交付结构");
  if (
    delivery.kind === "file" ||
    delivery.kind === "hls" ||
    delivery.kind === "dash"
  ) {
    validateSources(delivery.sources);
    if (
      delivery.kind !== "file" &&
      !["single", "adaptive"].includes(delivery.scope)
    )
      fail("清单 scope 无效");
    return;
  }
  if (delivery.kind === "segments") {
    if (
      !Array.isArray(delivery.pieces) ||
      !delivery.pieces.length ||
      !["byte_concat", "remux"].includes(delivery.join)
    )
      fail("分段结构无效");
    if (
      delivery.join === "byte_concat" &&
      delivery.join_evidence !== "validated_fragment_layout"
    )
      fail("分段拼接缺少证据");
    const chunks = [
      ...(delivery.init ? [delivery.init] : []),
      ...delivery.pieces,
    ];
    chunks.forEach((chunk) => {
      validateSources(chunk.sources);
      if (
        chunk.byte_range &&
        (!Number.isInteger(chunk.byte_range.start) ||
          chunk.byte_range.start < 0 ||
          !Number.isInteger(chunk.byte_range.end_inclusive) ||
          chunk.byte_range.end_inclusive < chunk.byte_range.start)
      )
        fail("分段范围无效");
    });
    delivery.pieces.forEach((piece, index) => {
      if (piece.index !== index + 1) fail("分段顺序无效");
    });
    return;
  }
  if (delivery.kind === "mux" || delivery.kind === "pair") {
    if (!Array.isArray(delivery.resources) || !delivery.resources.length)
      fail("组合资源缺失");
    unique(
      delivery.resources.map((resource) => resource.id),
      "资源 ID",
    );
    delivery.resources.forEach((resource) => {
      if (
        !identity(resource.id) ||
        ![
          "image",
          "motion",
          "video",
          "audio",
          "subtitle",
          "media",
          "muxed",
        ].includes(resource.role)
      )
        fail("资源角色无效");
      if (
        !["file", "hls", "dash", "segments"].includes(resource.delivery?.kind)
      )
        fail("组合只允许叶资源");
      validateDelivery(resource.delivery);
      validateCapabilityMetadata(resource.metadata, resource.role);
    });
    const roles = delivery.resources.map((resource) => resource.role);
    if (
      delivery.kind === "pair" &&
      (roles.length !== 2 ||
        !roles.includes("image") ||
        !roles.includes("motion"))
    )
      fail("实况配对不完整");
    if (
      delivery.kind === "mux" &&
      (roles.filter((role) => role === "video").length !== 1 ||
        !roles.includes("audio") ||
        !delivery.output?.extension)
    )
      fail("合流结构不完整");
    return;
  }
  fail("未知媒体交付能力");
}

/** A reader, never a sorter: an invalid server decision stays an explicit error. */
export function getDefaultDownloadTarget(
  item: MediaItem,
  context: TargetContext = {},
): DownloadTarget {
  if (item.state === "deferred")
    return { kind: "needs_resolve", message: "该条目需要解析" };
  if (item.state === "unavailable")
    return { kind: "unavailable", message: item.error.message };
  if (!item.default) {
    const kind = item.default_unavailable_reason;
    if (kind === "needs_resolve")
      return {
        kind: "unsupported",
        message: "该条目的候选版本待解析，当前暂不可用",
      };
    if (kind === "needs_refresh")
      return { kind, message: "来源已过期，请刷新此条目" };
    if (kind === "unavailable") return { kind, message: "该条目暂无可用来源" };
    return {
      kind: "unsupported",
      message: "没有可直接交付的完整文件，请选择具体版本",
      requires: item.variants
        .filter(readyVariant)
        .flatMap((v) => requiredCapabilities(v.delivery)),
    };
  }
  const chosen = item.variants.find(
    (variant) => variant.id === item.default!.variant_id,
  );
  if (!readyVariant(chosen) || chosen.delivery.kind !== "file")
    return { kind: "invalid", message: "默认引用没有指向本条目的完整文件" };
  const sources = chosen.delivery.sources;
  const source = item.default.source_id
    ? sources.find((source) => source.id === item.default!.source_id)
    : sources.length === 1
      ? sources[0]
      : undefined;
  if (
    !source ||
    source.url !== item.default.url ||
    (source.access || "direct") !== (item.default.access || "direct") ||
    source.expires_at !== item.default.expires_at
  )
    return { kind: "invalid", message: "默认来源投影与媒体来源不一致" };
  return readFile(item, chosen, source, context);
}
function readFile(
  item: MediaItem,
  variant: ReadyVariant,
  source: Source,
  context: TargetContext,
): DownloadTarget {
  if (source.availability === "unavailable")
    return {
      kind: "unavailable",
      message: "已选来源不可用，请选择其他来源或刷新",
    };
  if (!sourceAvailable(source, context.now))
    return { kind: "needs_refresh", message: "已选来源已过期，请刷新此条目" };
  try {
    const url = sourceUrl(source, context);
    const routes =
      variant.delivery.kind === "file"
        ? variant.delivery.sources
            .filter((candidate) => sourceAvailable(candidate, context.now))
            .map((candidate) => sourceUrl(candidate, context))
        : [];
    return {
      kind: "file",
      item,
      variant,
      source,
      url,
      routes: [...sourceRoutes(source, context), ...routes.filter((route) => route !== url)],
    };
  } catch (error) {
    return {
      kind: "invalid",
      message: error instanceof Error ? error.message : "来源无效",
    };
  }
}
export function getDefaultFileUrl(
  item: MediaItem,
  context: TargetContext = {},
): string | null {
  const target = getDefaultDownloadTarget(item, context);
  return target.kind === "file" ? target.url : null;
}
function hasAdaptiveHls(delivery: Delivery): boolean {
  if (delivery.kind === "hls") return delivery.scope !== "single";
  return "resources" in delivery && delivery.resources.some(
    (resource) => hasAdaptiveHls(resource.delivery),
  );
}
export function getDownloadTarget(
  item: MediaItem,
  selection?: MediaSelection | null,
  context: TargetContext = {},
): DownloadTarget {
  if (!selection) return getDefaultDownloadTarget(item, context);
  if (!readyItem(item)) return getDefaultDownloadTarget(item, context);
  const variant = item.variants.find(
    (variant) => variant.id === selection.variant_id,
  );
  if (!variant)
    return { kind: "invalid", message: "已选版本不存在，请明确重新选择" };
  if (!readyVariant(variant))
    return {
      kind: "unsupported",
      message: `待解析，当前暂不可用：${variant.reason.message}`,
    };
  const delivery = variant.delivery;
  if (delivery.kind === "file") {
    const source = selection.source_id
      ? delivery.sources.find((source) => source.id === selection.source_id)
      : delivery.sources[0];
    if (!source)
      return { kind: "invalid", message: "已选来源不存在，请明确重新选择" };
    return readFile(item, variant, source, context);
  }
  const capabilities = requiredCapabilities(delivery);
  if (
    hasAdaptiveHls(delivery) ||
    capabilities.some((capability) =>
      ["dash", "mux", "segment_remux"].includes(capability),
    )
  )
    return {
      kind: "unsupported",
      requires: capabilities,
      message: `该版本需要 ${capabilities.join("、")}，当前暂不支持`,
    };
  if (
    sourcesOf(delivery).some(
      (source) =>
        source.expires_at &&
        Date.parse(source.expires_at) <= (context.now ?? Date.now()),
    ) &&
    !deliveryAvailable(delivery, context.now)
  )
    return { kind: "needs_refresh", message: "该版本所需来源已过期" };
  if (!deliveryAvailable(delivery, context.now))
    return { kind: "unavailable", message: "该版本缺少可用的完整资源" };
  return { kind: delivery.kind as "hls" | "segments" | "pair", item, variant };
}
export function deliveryAvailable(
  delivery: Delivery,
  now = Date.now(),
): boolean {
  if ("sources" in delivery)
    return delivery.sources.some((source) => sourceAvailable(source, now));
  if (delivery.kind === "segments")
    return [
      ...(delivery.init ? [delivery.init] : []),
      ...delivery.pieces,
    ].every((piece) =>
      piece.sources.some((source) => sourceAvailable(source, now)),
    );
  return delivery.resources.every((resource) =>
    deliveryAvailable(resource.delivery, now),
  );
}
export function resourceSource(
  resource: Resource,
  context: TargetContext = {},
): Source | undefined {
  return "sources" in resource.delivery
    ? resource.delivery.sources.find((source) =>
        sourceAvailable(source, context.now),
      )
    : undefined;
}
export function validateMediaEnvelope(value: unknown): UnifiedMedia {
  if (!object(value) || value.schema_version !== "1.0")
    return fail("接口已更新，请刷新页面");
  if (value.code !== "OK")
    throw new Error(
      object(value.error) && nonempty(value.error.message)
        ? value.error.message
        : "解析失败",
    );
  const data = value.data;
  if (
    !object(data) ||
    !nonempty(data.platform) ||
    !Array.isArray(data.items) ||
    !data.items.length
  )
    return fail("缺少媒体条目");
  if (
    [
      "url",
      "images",
      "videos",
      "collections",
      "video_backup",
      "live_photo",
      "music",
    ].some((key) => key in data)
  )
    fail("不接受旧版媒体字段");
  const media = data as unknown as UnifiedMedia;
  unique(
    media.items.map((item) => item.id),
    "条目 ID",
  );
  if (
    (media.items.length > 1 && !media.default_item_id) ||
    (media.default_item_id &&
      !media.items.some((item) => item.id === media.default_item_id))
  )
    fail("入口条目引用无效");
  const generated = media.generated_at
    ? Date.parse(media.generated_at)
    : Date.now();
  if (!Number.isFinite(generated)) fail("生成时间无效");
  media.items.forEach((item) => {
    if (
      !object(item) ||
      !identity(item.id) ||
      !["video", "image", "live_photo", "audio"].includes(item.type) ||
      !("default" in item)
    )
      fail("条目结构无效");
    if (item.cover) validateSources([item.cover]);
    if (item.state === "deferred") {
      if (
        item.default !== null ||
        item.default_unavailable_reason !== "needs_resolve" ||
        !item.resolve_url
      )
        fail("待解析条目无效");
      return;
    }
    if (item.state === "unavailable") {
      if (
        item.default !== null ||
        item.default_unavailable_reason !== "unavailable" ||
        !item.error?.message
      )
        fail("不可用条目无效");
      return;
    }
    if (
      (item.state && item.state !== "ready") ||
      !Array.isArray(item.variants) ||
      !item.variants.length
    )
      fail("未知条目状态");
    unique(
      item.variants.map((variant) => variant.id),
      "版本 ID",
    );
    item.variants.forEach((variant) => {
      if (!identity(variant.id)) fail("版本 ID 无效");
      if (!readyVariant(variant)) {
        if (
          !variant.reason?.code ||
          !variant.reason?.message ||
          "delivery" in variant ||
          "metadata" in variant
        )
          fail("待解析版本无效");
        return;
      }
      validateDelivery(variant.delivery);
      validateCapabilityMetadata(variant.metadata, item.type === "audio" ? "audio" : item.type === "image" ? "image" : undefined);
      if (
        sourcesOf(variant.delivery).some((source) => source.expires_at) &&
        !media.generated_at
      )
        fail("到期来源缺少生成时间");
    });
    if (item.default === null) {
      if (
        ![
          "processing_required",
          "needs_refresh",
          "needs_resolve",
          "unavailable",
        ].includes(item.default_unavailable_reason || "")
      )
        fail("空默认缺少原因");
    } else {
      if (item.default_unavailable_reason) fail("非空默认不能附失败原因");
      const target = getDefaultDownloadTarget(item, { now: generated });
      if (target.kind !== "file") fail("默认引用或来源无效");
    }
    const higher = item.higher_quality_variants || [];
    if (higher.length && !item.default) fail("空默认不能引用更高版本");
    unique(
      higher.map((entry) => entry.variant_id),
      "更高版本引用",
    );
    higher.forEach((entry) => {
      const variant = item.variants.find((v) => v.id === entry.variant_id);
      if (
        !readyVariant(variant) ||
        variant.id === item.default?.variant_id ||
        !entry.requires.length ||
        JSON.stringify([...requiredCapabilities(variant.delivery)].sort()) !==
          JSON.stringify([...entry.requires].sort())
      )
        fail("更高版本能力引用无效");
    });
  });
  for (const group of media.groups || [])
    if (
      !group.item_ids.every((id) => media.items.some((item) => item.id === id))
    )
      fail("分组引用无效");
  for (const attachment of media.attachments || []) {
    validateDelivery(attachment.resource.delivery);
    validateCapabilityMetadata(attachment.resource.metadata, attachment.resource.role);
  }
  if (media.cover) validateSources([media.cover]);
  if (media.author?.avatar) validateSources([media.author.avatar]);
  return media;
}
