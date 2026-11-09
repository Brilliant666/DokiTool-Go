<script setup>
import { computed, ref, watch, onMounted, onUnmounted, nextTick } from "vue";
import AppIcon from "./AppIcon.vue";
import VideoPlaylist from "./VideoPlaylist.vue";
import {
  readyItem,
  readyVariant,
  getDownloadTarget,
  sourceUrl,
  resourceSource,
  requiredCapabilities,
  sourceAvailable,
} from "../utils/mediaContract";
import {
  variantLabel,
  formatMediaSize,
  groupVariantsForDisplay,
  variantSpecifications,
} from "../utils/mediaSources";
import { apiConfig } from "../services/apiConfig";
const props = defineProps({
  resultData: Object,
  resultVersion: Number,
  locale: String,
  selection: Object,
  isDownloading: Boolean,
  resolvingIds: Object,
  formatNumber: Function,
  formatDuration: Function,
});
const emit = defineEmits([
  "download-main",
  "download-all",
  "download-images",
  "download-image",
  "download-music",
  "download-live-video",
  "download-live-cover",
  "download-all-live",
  "download-all-live-covers",
  "copy-url",
  "switch-video",
  "select-video",
  "toggle-video",
  "select-all-videos",
  "clear-selection",
  "select-collection",
  "download-index",
  "download-selected",
  "download-all-videos",
  "download-selected-zip",
  "download-all-zip",
  "refresh-item",
]);
const context = { apiOrigin: apiConfig.baseUrl };
const urlOf = (source) => (source ? sourceUrl(source, context) : "");
const currentItem = computed(() => props.selection?.currentVideo.value || null);
const target = computed(() => props.selection?.currentTarget.value || null);
const youtubeHelper = computed(() =>
  props.resultData?.platform === "youtube"
    ? props.resultData?.extra?.youtube_helper || null
    : null,
);
const isYouTubeHelperBlocked = computed(
  () =>
    !!youtubeHelper.value &&
    ["missing", "outdated", "login_required"].includes(
      youtubeHelper.value.status,
    ),
);
const isYouTubeHelperReady = computed(
  () => youtubeHelper.value?.status === "ready",
);
const youtubeHelperScriptUrl = computed(
  () => youtubeHelper.value?.script_url || "/helper/dokitool-youtube.user.js",
);
const activeVideoUrl = computed(() =>
  isYouTubeHelperBlocked.value
    ? ""
    : target.value?.kind === "file"
      ? target.value.url
      : "",
);
const activeVariant = computed(() =>
  target.value && "variant" in target.value
    ? target.value.variant
    : readyItem(currentItem.value)
      ? currentItem.value.variants.find(
          (v) => v.id === props.selection?.currentSelection.value?.variant_id,
        )
      : null,
);
const metadata = computed(() => activeVariant.value?.metadata || {});
const hasVideo = computed(
  () =>
    currentItem.value && ["video", "audio"].includes(currentItem.value.type),
);
const imageItems = computed(() =>
  props.resultData.items.filter((item) => item.type === "image"),
);
const liveItems = computed(() =>
  props.resultData.items.filter((item) => item.type === "live_photo"),
);
const attachments = computed(() => props.resultData.attachments || []);
const hasImages = computed(() => imageItems.value.length > 0);
const activeLiveIndex = ref(0),
  liveViewMode = ref("video");
const totalLivePhotos = computed(() => liveItems.value.length);
const hasLivePhotos = computed(() => liveItems.value.length > 0);
const hasMusic = computed(() => attachments.value.length > 0);
const isMulti = computed(() => props.selection?.isMulti.value);
const currentIndex = computed(() => props.selection?.selectedIndex.value ?? 0);
const visibleVideos = computed(() => props.selection?.visibleList.value || []);
const collections = computed(() => props.selection?.collections.value || []);
const activeCollection = computed(
  () => props.selection?.sectionIndex.value || 0,
);
const selectedCount = computed(() => props.selection?.selectedCount.value || 0);
const batchZipAvailable = computed(() => {
  if (!["pipigx", "zuiyou"].includes(props.resultData?.platform)) return true;
  const items = selectedCount.value ? props.selection.getBatchVideos() : props.selection.list.value;
  return !items.some(item => item.type === "video");
});
const selectedTotal = computed(() => props.selection?.total.value || 0);
const isResolving = computed(() =>
  props.resolvingIds?.has(currentItem.value?.id),
);
const allQualityOptions = computed(() =>
  readyItem(currentItem.value)
    ? currentItem.value.variants.map((variant) => {
        const selected = props.selection.currentSelection.value;
        const active =
          (selected?.variant_id || currentItem.value.default?.variant_id) ===
          variant.id;
        const source =
          readyVariant(variant) && variant.delivery.kind === "file"
            ? variant.delivery.sources.find(
                (source) =>
                  active &&
                  sourceAvailable(source) &&
                  source.id ===
                    (selected?.source_id ||
                      currentItem.value.default?.source_id),
              ) ||
              variant.delivery.sources.find((source) => sourceAvailable(source)) ||
              variant.delivery.sources[0]
            : null;
        const downloadTarget = getDownloadTarget(currentItem.value, {
          variant_id: variant.id,
          ...(source?.id ? { source_id: source.id } : {}),
        }, context);
        return {
          variant,
          source: downloadTarget.kind === "file" ? downloadTarget.source : source,
          active:
            active && target.value?.kind === downloadTarget.kind &&
            (downloadTarget.kind !== "file" || target.value.url === downloadTarget.url),
          downloadTarget,
          downloadable: ["file", "hls", "segments", "pair"].includes(downloadTarget.kind),
          capabilities: readyVariant(variant)
            ? requiredCapabilities(variant.delivery)
            : [],
        };
      })
    : [],
);
const showAllQualityVariants = ref(false);
const downloadableQualityOptions = computed(() =>
  allQualityOptions.value.filter((option) => option.downloadable),
);
const otherQualityOptions = computed(() =>
  allQualityOptions.value.filter((option) => !option.downloadable),
);
const higherQualityNotice = computed(() =>
  (currentItem.value?.higher_quality_variants?.length || 0) > 0,
);
const unavailableQualityReason = computed(() => {
  if (!currentItem.value || downloadableQualityOptions.value.some((option) => option.downloadTarget.kind === "file"))
    return "";
  const decision = getDownloadTarget(currentItem.value, null, context);
  return props.locale === "en"
    ? "No complete downloadable file is currently available."
    : `暂无可直接下载的完整文件。${"message" in decision ? decision.message : ""}`;
});
const qualityGroups = computed(() =>
  groupVariantsForDisplay(
    downloadableQualityOptions.value.map((option) => option.variant),
  ),
);
const hasHiddenQualityVariants = computed(
  () => qualityGroups.value.length < downloadableQualityOptions.value.length,
);
const qualityOptions = computed(() => {
  if (showAllQualityVariants.value) return downloadableQualityOptions.value;
  const optionsById = new Map(
    downloadableQualityOptions.value.map((option) => [option.variant.id, option]),
  );
  return qualityGroups.value.map((group) => {
    const options = group.map((variant) => optionsById.get(variant.id));
    // A group's representative is display state, never an automatic choice.
    return options.find((option) => option.active) || options[0];
  });
});
const hasBackup = computed(() => allQualityOptions.value.length > 0);
const imageUrl = (item) => {
  const selected = props.selection?.selectionFor(item.id);
  const result = getDownloadTarget(item, selected, context);
  return result.kind === "file" ? result.url : "";
};
const liveVariant = (item) => {
  if (!readyItem(item)) return null;
  const choice = props.selection?.selectionFor(item.id);
  return choice
    ? item.variants.find((variant) => variant.id === choice.variant_id)
    : item.variants.length === 1
      ? item.variants[0]
      : null;
};
const liveResources = (item) =>
  readyVariant(liveVariant(item)) && liveVariant(item).delivery.kind === "pair"
    ? liveVariant(item).delivery.resources
    : [];
const liveUrl = (item, role) => {
  const resource = liveResources(item).find(
    (resource) => resource.role === role,
  );
  return resource ? urlOf(resourceSource(resource)) : "";
};
const attachmentUrl = (attachment) =>
  !isYouTubeHelperBlocked.value && attachment.resource.delivery.kind === "file"
    ? urlOf(resourceSource(attachment.resource))
    : "";
const targetMessage = computed(() =>
  isYouTubeHelperBlocked.value
    ? youtubeHelper.value?.message ||
      "请先按说明安装并启用 YouTube 助手脚本后下载"
    : target.value && "message" in target.value
      ? target.value.message
      : target.value?.kind === "file"
        ? ""
        : "该版本需要处理后下载，暂不能在播放器直接播放",
);
const sourceHint = computed(() =>
  t(
    props.locale,
    target.value?.kind === "file" && target.value.source.access === "relay"
      ? "relaySourceHint"
      : "sourceHint",
  ),
);
const author = computed(() => props.resultData.author || null);
const stats = computed(() =>
  Object.entries(props.resultData.statistics || {}).map(([key, stat]) => ({
    key,
    value: stat.value,
    unverified: stat.state === "unverified",
    icon:
      {
        likes: "fa-heart",
        comments: "fa-comment",
        saves: "fa-bookmark",
        shares: "fa-share-alt",
      }[key] || "fa-play",
    label: t(
      props.locale,
      {
        plays: "statPlay",
        views: "statPlay",
        likes: "statLike",
        comments: "statComment",
        shares: "statShare",
        saves: "statCollect",
      }[key] || key,
    ),
  })),
);
const allTags = computed(() => props.resultData.tags || []);
const playCountNote = computed(() =>
  props.locale === "en"
    ? "The platform did not provide verified counts."
    : "平台未提供可确认的统计值，未将缺失值当作 0。",
);
const mediaTypeBadge = computed(() => ({
  icon: hasImages.value ? "fa-images" : "fa-play",
  text: isMulti.value
    ? t(props.locale, "collection")
    : t(
        props.locale,
        currentItem.value?.type === "live_photo"
          ? "live"
          : currentItem.value?.type === "image"
            ? "images"
            : "video",
      ),
  style: hasImages.value ? "image" : "video",
}));
const infoCards = computed(() => [
  {
    icon: "fa-pen",
    label: t(props.locale, "infoTitle"),
    value:
      currentItem.value?.title ||
      props.resultData.title ||
      t(props.locale, "unknownTitle"),
  },
  {
    icon: "fa-calendar",
    label: t(props.locale, "publishTime"),
    value:
      formatDate(props.resultData.published_at) ||
      t(props.locale, "unknownTime"),
  },
  {
    icon: "fa-cube",
    label: t(props.locale, "type"),
    value: currentItem.value?.type || t(props.locale, "unknownType"),
  },
  {
    icon: "fa-expand",
    label: t(props.locale, "quality"),
    value: variantLabel(activeVariant.value, props.locale),
  },
  {
    icon: "fa-clock",
    label: t(props.locale, "duration"),
    value:
      props.formatDuration?.(
        (metadata.value.duration_ms || currentItem.value?.duration_ms || 0) /
          1000,
      ) || "—",
  },
]);
const activeTab = ref("info"),
  videoEl = ref(null),
  playbackFailed = ref(false),
  manualSelection = ref(0);
const mediaIdentity = computed(() =>
  JSON.stringify([props.resultVersion, currentItem.value?.id]),
);
const playerKey = computed(() =>
  JSON.stringify([
    mediaIdentity.value,
    activeVideoUrl.value,
    manualSelection.value,
  ]),
);
const attemptedSources = new Set();
watch(mediaIdentity, () => {
  attemptedSources.clear();
  playbackFailed.value = false;
  activeTab.value = "info";
  showAllQualityVariants.value = false;
});
watch(activeVideoUrl, () => {
  playbackFailed.value = false;
});
const selectQuality = (option) => {
  if (!option.downloadable) return;
  manualSelection.value++;
  attemptedSources.clear();
  playbackFailed.value = false;
  emit("switch-video", {
    variant_id: option.variant.id,
    ...(option.source?.id ? { source_id: option.source.id } : {}),
  });
};
const originalPageUrl = computed(() => {
  try {
    const raw = currentItem.value?.source_url || props.resultData.source_url;
    return raw ? sourceUrl({ url: raw }) : "";
  } catch {
    return "";
  }
});
const updatePlaybackStatus = (event, failed) => {
  const element = event?.currentTarget || event?.target;
  if (
    !element ||
    (element.dataset?.playbackKey ||
      element.getAttribute?.("data-playback-key") ||
      element.props?.["data-playback-key"]) !== playerKey.value ||
    (element.currentSrc && element.currentSrc !== activeVideoUrl.value)
  )
    return;
  if (!failed) {
    playbackFailed.value = false;
    return;
  }
  if (target.value?.kind !== "file") return;
  attemptedSources.add(target.value.url);
  const next = target.value.routes.find((url) => !attemptedSources.has(url));
  if (next && attemptedSources.size < 3) {
    const source = target.value.variant.delivery.sources.find(
      (source) => urlOf(source) === next,
    );
    if (source) {
      emit("switch-video", {
        variant_id: target.value.variant.id,
        ...(source.id ? { source_id: source.id } : {}),
      });
      return;
    }
  }
  playbackFailed.value = true;
};
const activeImageIndex = ref(0),
  imageModalOpen = ref(false),
  showCopyToast = ref(false),
  copyToastText = ref("");
let copyTimer;
const copyToClipboard = async (text) => {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(String(text));
    copyToastText.value = String(text);
    showCopyToast.value = true;
    clearTimeout(copyTimer);
    copyTimer = setTimeout(() => (showCopyToast.value = false), 2000);
  } catch {
    emit("copy-url", text);
  }
};
const openImageModal = (index) => {
  activeImageIndex.value = index;
  imageModalOpen.value = true;
  props.selection.selectItem(imageItems.value[index].id);
  document.body.style.overflow = "hidden";
};
const closeImageModal = () => {
  imageModalOpen.value = false;
  document.body.style.overflow = "";
};
const nextImage = () => {
  activeImageIndex.value =
    (activeImageIndex.value + 1) % imageItems.value.length;
  props.selection.selectItem(imageItems.value[activeImageIndex.value].id);
};
const prevImage = () => {
  activeImageIndex.value =
    (activeImageIndex.value - 1 + imageItems.value.length) %
    imageItems.value.length;
  props.selection.selectItem(imageItems.value[activeImageIndex.value].id);
};
const handleKeydown = (event) => {
  if (!imageModalOpen.value) return;
  if (event.key === "Escape") closeImageModal();
  if (event.key === "ArrowRight") nextImage();
  if (event.key === "ArrowLeft") prevImage();
};
watch(
  () => props.resultVersion,
  () => {
    activeImageIndex.value = 0;
    activeLiveIndex.value = 0;
    closeImageModal();
  },
);
onMounted(() => window.addEventListener("keydown", handleKeydown));
onUnmounted(() => {
  window.removeEventListener("keydown", handleKeydown);
  document.body.style.overflow = "";
  clearTimeout(copyTimer);
});
const infoColumnEl = ref(null),
  authorCardEl = ref(null),
  infoCardEl = ref(null),
  tabsEl = ref(null),
  detailsPanelEl = ref(null);
const desktopLayout = ref(false),
  detailsCardHeight = ref(0),
  detailsColumnHeight = ref(0);
const canAlignDetails = computed(
  () =>
    desktopLayout.value &&
    hasVideo.value &&
    !isMulti.value &&
    !hasImages.value &&
    !hasLivePhotos.value,
);
const hasDetailsBaseline = computed(
  () => canAlignDetails.value && detailsColumnHeight.value > 0,
);
const detailsLayoutStyle = computed(() =>
  hasDetailsBaseline.value
    ? {
        "--rs-details-card-height": `${detailsCardHeight.value}px`,
        "--rs-details-column-height": `${detailsColumnHeight.value}px`,
      }
    : undefined,
);
const tabs = computed(() => [
  { key: "info", icon: "fa-info-circle", label: t(props.locale, "tabInfo") },
  ...(stats.value.length
    ? [
        {
          key: "stats",
          icon: "fa-chart-bar",
          label: t(props.locale, "tabStats"),
        },
      ]
    : []),
  ...(hasBackup.value
    ? [
        {
          key: "backup",
          icon: "fa-layer-group",
          label: t(props.locale, "tabBackup"),
        },
      ]
    : []),
  ...(allTags.value.length
    ? [{ key: "tags", icon: "fa-tags", label: t(props.locale, "tabTags") }]
    : []),
  ...(hasMusic.value
    ? [{ key: "music", icon: "fa-music", label: t(props.locale, "tabMusic") }]
    : []),
]);
const content = {
  "zh-CN": {
    resultTitle: "解析结果",
    sourceHint: "当前使用源链接直连播放与下载",
    relaySourceHint: "当前通过本站媒体服务播放与下载",
    playbackFailed: "该视频源无法在本站直接播放，可尝试其他画质或打开原作品。",
    openOriginal: "打开原作品",
    downloadSingle: "下载视频",
    downloadLive: "下载实况",
    downloadAll: "下载全部",
    downloadSelected: "下载选中",
    downloadAllVideos: "下载全部",
    downloadSelectedZip: "选中打包ZIP",
    downloadAllZip: "全部打包ZIP",
    resolving: "解析中…",
    episodes: "分集",
    collections: "合集",
    copyVideo: "复制链接",
    backupTitle: "备用画质",
    unknown: "未知",
    allImages: "下载全部图片",
    downloadAllLive: "下载全部实况",
    downloadAllCovers: "下载全部封面",
    musicTitle: "背景音乐",
    unknownMusic: "未知音乐",
    unknownAuthor: "未知作者",
    liveVideo: "实况",
    infoTitle: "作品标题",
    unknownTitle: "未知标题",
    publishTime: "发布时间",
    unknownTime: "未知时间",
    type: "作品类型",
    video: "视频",
    images: "图片集",
    live: "实况",
    collection: "合集",
    unknownType: "未知",
    quality: "画质",
    duration: "时长",
    unknownQuality: "未知",
    overlayVideo: "下载视频",
    overlayCover: "下载封面",
    infoLikes: "获赞 {count}",
    copied: "已复制",
    copiedHint: "链接已复制到剪贴板",
    statPlay: "播放",
    statLike: "点赞",
    statComment: "评论",
    statCollect: "收藏",
    statShare: "分享",
    tabInfo: "详情",
    tabStats: "数据",
    tabTags: "标签",
    tabMusic: "音乐",
    tabBackup: "画质选项",
    imagesTitle: "图片集",
    imagesCount: "{count} 张",
    liveCount: "{count} 组",
    bitRate: "码率",
    codec: "编码",
    resolution: "分辨率",
    currentPlaying: "当前播放",
    preview: "预览",
    close: "关闭",
    page: "页",
    videoSource: "视频源",
    imageSource: "图片来源",
    liveSource: "实况来源",
    noBackups: "暂无备用画质",
    selectQuality: "选择画质",
  },
  en: {
    resultTitle: "Result",
    sourceHint: "Using direct source link",
    relaySourceHint: "Playback and downloads use this site’s media service",
    playbackFailed:
      "This source cannot play directly here. Try another quality or open the original post.",
    openOriginal: "Open original post",
    downloadSingle: "Download Video",
    downloadLive: "Download Live",
    downloadAll: "Download All",
    downloadSelected: "Download Selected",
    downloadAllVideos: "Download All",
    downloadSelectedZip: "Selected as ZIP",
    downloadAllZip: "All as ZIP",
    resolving: "Resolving…",
    episodes: "Episodes",
    collections: "Collections",
    copyVideo: "Copy URL",
    backupTitle: "Backup Quality",
    unknown: "Unknown",
    allImages: "Download Images",
    downloadAllLive: "Download All Live",
    downloadAllCovers: "Download All Covers",
    musicTitle: "Background Music",
    unknownMusic: "Unknown Music",
    unknownAuthor: "Unknown Artist",
    liveVideo: "Live",
    infoTitle: "Title",
    unknownTitle: "Unknown Title",
    publishTime: "Published",
    unknownTime: "Unknown",
    type: "Type",
    video: "Video",
    images: "Album",
    live: "Live",
    collection: "Collection",
    unknownType: "Unknown",
    quality: "Quality",
    duration: "Duration",
    unknownQuality: "Unknown",
    overlayVideo: "Download Video",
    overlayCover: "Download Cover",
    infoLikes: "{count} likes",
    copied: "Copied",
    copiedHint: "URL copied to clipboard",
    statPlay: "Plays",
    statLike: "Likes",
    statComment: "Comments",
    statCollect: "Favorites",
    statShare: "Shares",
    tabInfo: "Details",
    tabStats: "Stats",
    tabTags: "Tags",
    tabMusic: "Music",
    tabBackup: "Quality",
    imagesTitle: "Album",
    imagesCount: "{count} images",
    liveCount: "{count} groups",
    bitRate: "Bitrate",
    codec: "Codec",
    resolution: "Resolution",
    currentPlaying: "Now Playing",
    preview: "Preview",
    close: "Close",
    page: "page",
    videoSource: "Video Source",
    imageSource: "Image Source",
    liveSource: "Live Source",
    noBackups: "No backup qualities",
    selectQuality: "Select Quality",
  },
};

const t = (locale, key) =>
  content[locale]?.[key] || content["zh-CN"][key] || key;

const formatDate = (timestamp) => {
  if (!timestamp) return "";
  const numeric =
    typeof timestamp === "number" || /^\d+(\.\d+)?$/.test(String(timestamp));
  const value = numeric
    ? Number(timestamp) * (Number(timestamp) < 1e12 ? 1000 : 1)
    : String(timestamp).replace(/^(\d{4}-\d{2}-\d{2}) /, "$1T");
  const date = new Date(value);
  if (isNaN(date.getTime())) return "";
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, "0");
  const d = String(date.getDate()).padStart(2, "0");
  const time = [date.getHours(), date.getMinutes(), date.getSeconds()]
    .map((value) => String(value).padStart(2, "0"))
    .join(":");
  return `${y}-${m}-${d} ${time}`;
};

const qualityBitrates = (metadata = {}) => [
  { key: "bitrate_bps", label: props.locale === "en" ? "Total" : "总码率" },
  { key: "video_bitrate_bps", label: props.locale === "en" ? "Video" : "视频码率" },
  { key: "audio_bitrate_bps", label: props.locale === "en" ? "Audio" : "音频码率" },
].filter(rate => metadata[rate.key] > 0).map(rate => ({ ...rate, value: metadata[rate.key] }));

const formatBitrate = (val) => {
  const n = Number(val || 0);
  if (n <= 0) return "";
  if (n >= 1000000) return (n / 1000000).toFixed(1) + " Mbps";
  if (n >= 1000) return (n / 1000).toFixed(0) + " Kbps";
  return n + " bps";
};

// Measure the details panel, never the selected tab. While another desktop tab
// is open the same panel stays laid out, but hidden/inert and out of normal flow.
// Its natural height therefore follows translations, wrapping and source details
// without letting a long quality/tag list resize the player.
let layoutDisposed = false;
let layoutFrame = 0;
let layoutQueued = false;
let layoutObserver;
let observedLayoutNodes = [];
let desktopQuery;
const clearDetailsBaseline = () => {
  detailsCardHeight.value = 0;
  detailsColumnHeight.value = 0;
};
const measureDetailsLayout = () => {
  if (layoutDisposed) return;
  const nodes = canAlignDetails.value
    ? [
        detailsPanelEl.value,
        tabsEl.value,
        authorCardEl.value,
        infoCardEl.value,
      ].filter(Boolean)
    : [];
  if (
    layoutObserver &&
    (nodes.length !== observedLayoutNodes.length ||
      nodes.some((node, index) => node !== observedLayoutNodes[index]))
  ) {
    layoutObserver.disconnect();
    nodes.forEach((node) => layoutObserver.observe(node));
    observedLayoutNodes = nodes;
  }
  if (!canAlignDetails.value) {
    clearDetailsBaseline();
    return;
  }
  const details = detailsPanelEl.value?.getBoundingClientRect?.();
  const navigation = tabsEl.value?.getBoundingClientRect?.();
  if (
    !details?.width ||
    !details.height ||
    !navigation?.height ||
    !window.getComputedStyle
  )
    return;
  const cardStyle = window.getComputedStyle(infoCardEl.value);
  const columnStyle = window.getComputedStyle(infoColumnEl.value);
  const pixels = (value) => Number.parseFloat(value) || 0;
  const cardHeight = Math.ceil(
    details.height +
      navigation.height +
      pixels(cardStyle.borderTopWidth) +
      pixels(cardStyle.borderBottomWidth),
  );
  const authorHeight =
    authorCardEl.value?.getBoundingClientRect?.().height || 0;
  const columnHeight = Math.ceil(
    cardHeight + authorHeight + (authorHeight ? pixels(columnStyle.rowGap) : 0),
  );
  if (detailsCardHeight.value !== cardHeight)
    detailsCardHeight.value = cardHeight;
  if (detailsColumnHeight.value !== columnHeight)
    detailsColumnHeight.value = columnHeight;
};
const queueDetailsMeasurement = () => {
  if (layoutDisposed || layoutQueued) return;
  layoutQueued = true;
  nextTick(() => {
    if (layoutDisposed) {
      layoutQueued = false;
      return;
    }
    const measure = () => {
      layoutFrame = 0;
      layoutQueued = false;
      measureDetailsLayout();
    };
    if (window.requestAnimationFrame)
      layoutFrame = window.requestAnimationFrame(measure);
    else measure();
  });
};
const updateLayoutViewport = () => {
  desktopLayout.value = desktopQuery?.matches ?? window.innerWidth >= 1040;
  if (!desktopLayout.value) clearDetailsBaseline();
  queueDetailsMeasurement();
};
watch(
  [canAlignDetails, mediaIdentity],
  () => {
    clearDetailsBaseline();
    queueDetailsMeasurement();
  },
  { flush: "post" },
);
watch(
  [infoCards, author, tabs, activeTab, () => props.locale],
  queueDetailsMeasurement,
  { flush: "post" },
);
onMounted(() => {
  desktopQuery = window.matchMedia?.("(min-width: 1040px)");
  if (window.ResizeObserver)
    layoutObserver = new window.ResizeObserver(queueDetailsMeasurement);
  desktopQuery?.addEventListener?.("change", updateLayoutViewport);
  window.addEventListener("resize", updateLayoutViewport, { passive: true });
  updateLayoutViewport();
});
onUnmounted(() => {
  layoutDisposed = true;
  if (layoutFrame) window.cancelAnimationFrame?.(layoutFrame);
  layoutObserver?.disconnect();
  desktopQuery?.removeEventListener?.("change", updateLayoutViewport);
  window.removeEventListener("resize", updateLayoutViewport);
});
</script>
<template>
  <section
    v-if="resultData"
    class="rs"
    role="region"
    :aria-label="t(locale, 'resultTitle')"
  >
    <div class="rs-header">
      <div class="rs-header-inner">
        <div class="rs-header-left">
          <div class="rs-status-dot-wrapper">
            <div class="rs-status-dot"></div>
            <div class="rs-status-dot-pulse"></div>
          </div>
          <span class="rs-header-title">{{ t(locale, "resultTitle") }}</span
          ><span
            class="rs-header-badge"
            :class="'rs-header-badge--' + mediaTypeBadge.style"
            ><AppIcon :class="['fas', mediaTypeBadge.icon]" />{{
              mediaTypeBadge.text
            }}</span
          >
        </div>
        <div class="rs-header-right">
          <span class="rs-header-hint"
            ><AppIcon class="fas fa-link rs-header-hint-icon" /><span
              class="rs-header-hint-text"
              >{{ sourceHint }}</span
            ></span
          >
        </div>
      </div>
    </div>
    <div v-if="playbackFailed" class="rs-playback-error" role="alert">
      <span>{{ t(locale, "playbackFailed") }}</span
      ><a
        v-if="originalPageUrl"
        :href="originalPageUrl"
        target="_blank"
        rel="noopener noreferrer"
        >{{ t(locale, "openOriginal") }}</a
      >
    </div>
    <div
      v-if="youtubeHelper?.status === 'login_required'"
      class="rs-yt-helper-alert"
      role="alert"
    >
      <div class="rs-yt-helper-alert-title">
        <AppIcon class="fas fa-exclamation-triangle" />
        <span>当前网络出口被 YouTube 要求验证，请更换节点后重试</span>
      </div>
      <p class="rs-yt-helper-alert-desc">
        本站与助手脚本均采用无 Cookie 匿名请求，不提供也不要求登录 YouTube 账号或导入 Cookie。请切换至未被风控的网络出口节点后重新点击解析。
      </p>
    </div>
    <div
      v-else-if="
        youtubeHelper?.status === 'missing' ||
        youtubeHelper?.status === 'outdated'
      "
      class="rs-yt-helper-card"
      role="region"
      aria-label="YouTube 下载助手安装说明"
    >
      <div class="rs-yt-helper-card-hd">
        <AppIcon class="fas fa-download" />
        <span>{{
          youtubeHelper.status === "outdated"
            ? `YouTube 下载助手脚本需要更新（当前 v${youtubeHelper.version || "未知"}，要求 v${youtubeHelper.min_version || "1.0.0"}+）`
            : "需要安装 YouTube 下载助手脚本"
        }}</span>
      </div>
      <p class="rs-yt-helper-card-intro">
        YouTube 媒体直链绑定请求方网络出口 IP，本站不再提供绑定服务器 IP 的下载链接。请按以下步骤在浏览器中安装并启用用户脚本：
      </p>
      <ol class="rs-yt-helper-steps">
        <li>
          <strong>安装脚本管理器扩展：</strong>Chrome / Edge 请安装
          <strong>Tampermonkey（篡改猴）</strong>，Firefox 请安装
          <strong>Violentmonkey（暴力猴）</strong>。
        </li>
        <li>
          <strong>在 Chrome 中打开「允许用户脚本」：</strong>在 Chrome / Edge
          地址栏打开 <code>chrome://extensions</code>，点击 Tampermonkey 的「详情」，开启<strong>「允许用户脚本」</strong>（Allow User Scripts）开关（或开启右上角开发者模式）。
        </li>
        <li>
          <strong>{{
            youtubeHelper.status === "outdated"
              ? "更新本站助手脚本："
              : "安装本站助手脚本："
          }}</strong>点击下方按钮打开脚本地址完成安装或更新，随后刷新本页并重新点击「开始解析」。
        </li>
      </ol>
      <div class="rs-yt-helper-actions">
        <a
          :href="youtubeHelperScriptUrl"
          target="_blank"
          rel="noopener noreferrer"
          class="rs-btn rs-btn--primary rs-yt-helper-install-link"
        >
          <AppIcon class="fas fa-download" />
          <span>{{
            youtubeHelper.status === "outdated"
              ? "更新 YouTube 助手脚本 (/helper/dokitool-youtube.user.js)"
              : "安装 YouTube 助手脚本 (/helper/dokitool-youtube.user.js)"
          }}</span>
        </a>
      </div>
    </div>
    <div
      v-else-if="isYouTubeHelperReady"
      class="rs-yt-helper-notice"
      role="status"
    >
      <AppIcon class="fas fa-info-circle" />
      <span>
        YouTube 助手已就绪（v{{ youtubeHelper.version }}）：360p 音画合一由浏览器原生附件直下；仅视频轨（如 1080p）和 M4A 音频由助手保存，受 2 倍码率限速，预计耗时较长。
      </span>
    </div>
    <div
      class="rs-main"
      :class="{ 'rs-main--align-details': hasDetailsBaseline }"
      :style="detailsLayoutStyle"
    >
      <div class="rs-media-col">
        <div v-if="hasVideo" class="rs-card rs-card--video">
          <div class="rs-card-hd">
            <div class="rs-card-hd-left">
              <AppIcon class="fas fa-play-circle rs-card-icon" /><span>{{
                t(locale, "videoSource")
              }}</span>
            </div>
            <span v-if="activeVariant" class="rs-quality-tag">{{
              variantLabel(activeVariant, locale)
            }}</span>
          </div>
          <div class="rs-video-stage">
            <audio
              v-if="currentItem.type === 'audio' && activeVideoUrl"
              :key="playerKey"
              :data-playback-key="playerKey"
              :src="activeVideoUrl"
              :preload="resultData?.platform === 'youtube' ? 'none' : undefined"
              controls
              class="rs-music-player"
              @error="updatePlaybackStatus($event, true)"
              @loadeddata="updatePlaybackStatus($event, false)"
            />
            <video
              v-else
              :key="playerKey"
              ref="videoEl"
              :data-playback-key="playerKey"
              :src="activeVideoUrl || undefined"
              :preload="resultData?.platform === 'youtube' ? 'none' : undefined"
              controls
              referrerpolicy="no-referrer"
              class="rs-video"
              :poster="
                urlOf(currentItem.cover || resultData.cover) || undefined
              "
              @error="updatePlaybackStatus($event, true)"
              @loadeddata="updatePlaybackStatus($event, false)"
            >
              <track kind="captions" :src="undefined" />
            </video>
            <div v-if="isResolving || !activeVideoUrl" class="rs-stage-overlay">
              <div v-if="isResolving" class="rs-stage-spinner"></div>
              <span>{{
                isResolving ? t(locale, "resolving") : targetMessage
              }}</span
              ><button
                v-if="currentItem.resolve_url && !isResolving"
                class="rs-btn rs-btn--ghost"
                @click="emit('refresh-item', currentItem)"
              >
                {{ locale === "en" ? "Refresh item" : "刷新此条目" }}
              </button>
            </div>
          </div>
          <div class="rs-card-actions">
            <button
              @click="emit('download-main')"
              class="rs-btn rs-btn--primary"
              :disabled="
                isYouTubeHelperBlocked ||
                isResolving ||
                target?.kind === 'unsupported' ||
                target?.kind === 'unavailable' ||
                target?.kind === 'invalid'
              "
            >
              <AppIcon class="fas fa-download" /><span>{{
                t(locale, "downloadSingle")
              }}</span>
            </button>
            <button
              v-if="isMulti"
              @click="
                selectedCount
                  ? emit('download-selected')
                  : emit('download-all-videos')
              "
              class="rs-btn rs-btn--accent"
            >
              <AppIcon class="fas fa-layer-group" /><span>{{
                selectedCount
                  ? t(locale, "downloadSelected") + "（" + selectedCount + "）"
                  : t(locale, "downloadAllVideos")
              }}</span>
            </button>
            <button
              v-if="isMulti && batchZipAvailable"
              @click="
                emit(
                  'download-selected-zip',
                  selectedCount
                    ? selection.getBatchVideos()
                    : selection.list.value,
                )
              "
              class="rs-btn rs-btn--ghost"
            >
              <AppIcon class="fas fa-file-archive" />{{
                t(locale, "downloadAllZip")
              }}
            </button>
            <button
              @click="emit('copy-url', activeVideoUrl)"
              class="rs-btn rs-btn--ghost-light"
              :disabled="!activeVideoUrl"
            >
              <AppIcon class="fas fa-copy" /><span>{{
                t(locale, "copyVideo")
              }}</span>
            </button>
          </div>
        </div>
        <div v-if="isMulti" class="rs-card rs-card--playlist">
          <div class="rs-card-hd">
            <div class="rs-card-hd-left">
              <AppIcon class="fas fa-list rs-card-icon" /><span
                >{{ t(locale, "episodes") }}（{{ visibleVideos.length }}）</span
              >
            </div>
          </div>
          <div class="rs-playlist">
            <VideoPlaylist
              :videos="visibleVideos"
              :current-index="currentIndex"
              :selected-set="selection.selectedSet.value"
              :total="selectedTotal"
              :collections="collections"
              :active-collection="activeCollection"
              :resolving-ids="resolvingIds"
              @select="emit('select-video', $event)"
              @toggle="emit('toggle-video', $event)"
              @select-all="emit('select-all-videos')"
              @clear-selection="emit('clear-selection')"
              @select-collection="emit('select-collection', $event)"
            />
          </div>
        </div>
        <!-- ── IMAGE GALLERY ── -->
        <div v-if="hasImages" class="rs-card rs-card--gallery">
          <div class="rs-card-hd">
            <div class="rs-card-hd-left">
              <AppIcon class="fas fa-images rs-card-icon"></AppIcon>
              <span>{{ t(locale, "imageSource") }}</span>
            </div>
            <span class="rs-count-tag">{{
              t(locale, "imagesCount").replace("{count}", imageItems.length)
            }}</span>
          </div>

          <div
            class="rs-gallery"
            :class="{
              'rs-gallery--1': imageItems.length === 1,
              'rs-gallery--2': imageItems.length === 2,
              'rs-gallery--3': imageItems.length >= 3 && imageItems.length <= 6,
              'rs-gallery--4': imageItems.length > 6,
            }"
          >
            <button
              v-for="(item, idx) in imageItems"
              :key="item.id"
              @click="openImageModal(idx)"
              class="rs-gallery-item"
              :aria-label="`${t(locale, 'preview')} ${idx + 1}`"
            >
              <img
                :src="imageUrl(item)"
                class="rs-gallery-img"
                :alt="`${t(locale, 'imagesTitle')} ${idx + 1}`"
                loading="lazy"
              />
              <div class="rs-gallery-mask">
                <AppIcon class="fas fa-search-plus"></AppIcon>
              </div>
            </button>
          </div>

          <div class="rs-card-actions">
            <button
              @click="emit('download-images')"
              class="rs-btn rs-btn--primary"
            >
              <AppIcon class="fas fa-download"></AppIcon>
              <span
                >{{ t(locale, "allImages") }} ({{ imageItems.length }})</span
              >
            </button>
          </div>
        </div>

        <!-- ── LIVE PHOTOS ── -->
        <div v-if="hasLivePhotos" class="rs-card rs-card--live">
          <div class="rs-live-carousel">
            <!-- 顶部标签栏 -->
            <div class="rs-live-carousel-hd">
              <span class="rs-live-badge">
                <span class="rs-live-dot"></span>
                {{ t(locale, "live") }} {{ activeLiveIndex + 1 }}/{{
                  totalLivePhotos
                }}
              </span>
              <button
                v-if="liveItems[activeLiveIndex]?.variants?.length > 1"
                class="rs-btn rs-btn--ghost rs-btn--sm"
                @click="
                  selection.selectItem(liveItems[activeLiveIndex].id);
                  activeTab = 'backup';
                "
              >
                {{ t(locale, "selectQuality") }}
              </button>
              <div class="rs-live-toggle">
                <button
                  @click="liveViewMode = 'video'"
                  class="rs-live-toggle-btn"
                  :class="{
                    'rs-live-toggle-btn--on': liveViewMode === 'video',
                  }"
                >
                  <AppIcon class="fas fa-video"></AppIcon>
                  {{ t(locale, "overlayVideo") }}
                </button>
                <button
                  @click="liveViewMode = 'cover'"
                  class="rs-live-toggle-btn"
                  :class="{
                    'rs-live-toggle-btn--on': liveViewMode === 'cover',
                  }"
                >
                  <AppIcon class="fas fa-image"></AppIcon>
                  {{ t(locale, "overlayCover") }}
                </button>
              </div>
            </div>

            <!-- 媒体内容区 -->
            <div class="rs-live-carousel-body">
              <!-- 导航箭头 -->
              <button
                @click="
                  activeLiveIndex =
                    activeLiveIndex === 0
                      ? totalLivePhotos - 1
                      : activeLiveIndex - 1
                "
                class="rs-live-nav rs-live-nav--prev"
                :disabled="totalLivePhotos <= 1"
              >
                <AppIcon class="fas fa-chevron-left"></AppIcon>
              </button>
              <button
                @click="
                  activeLiveIndex = (activeLiveIndex + 1) % totalLivePhotos
                "
                class="rs-live-nav rs-live-nav--next"
                :disabled="totalLivePhotos <= 1"
              >
                <AppIcon class="fas fa-chevron-right"></AppIcon>
              </button>

              <!-- 视频/图片展示 -->
              <div class="rs-live-carousel-content">
                <template v-if="liveViewMode === 'video'">
                  <video
                    :src="liveUrl(liveItems[activeLiveIndex], 'motion')"
                    :poster="liveUrl(liveItems[activeLiveIndex], 'image')"
                    controls
                    referrerpolicy="no-referrer"
                    class="rs-live-carousel-video"
                  >
                    <track kind="captions" :src="undefined" />
                  </video>
                </template>
                <template v-else>
                  <img
                    :src="liveUrl(liveItems[activeLiveIndex], 'image')"
                    class="rs-live-carousel-image"
                    :alt="`${t(locale, 'live')} ${activeLiveIndex + 1}`"
                  />
                </template>
              </div>
            </div>

            <!-- 当前项操作按钮 -->
            <div class="rs-live-carousel-actions">
              <button
                @click="emit('download-live-video', liveItems[activeLiveIndex])"
                class="rs-btn rs-btn--ghost rs-btn--sm"
              >
                <AppIcon class="fas fa-video"></AppIcon>
                {{ t(locale, "overlayVideo") }}
              </button>
              <button
                @click="emit('download-live-cover', liveItems[activeLiveIndex])"
                class="rs-btn rs-btn--ghost rs-btn--sm"
              >
                <AppIcon class="fas fa-image"></AppIcon>
                {{ t(locale, "overlayCover") }}
              </button>
            </div>
          </div>

          <!-- 底部批量下载 -->
          <div class="rs-live-bulk-bar">
            <button
              @click="emit('download-all-live')"
              class="rs-btn rs-btn--primary rs-btn--block"
            >
              <AppIcon class="fas fa-download"></AppIcon>
              {{ t(locale, "downloadAllLive") }} ({{ totalLivePhotos }}
              {{ t(locale, "live") }})
            </button>
            <button
              @click="emit('download-all-live-covers')"
              class="rs-btn rs-btn--ghost rs-btn--block"
            >
              <AppIcon class="fas fa-images"></AppIcon>
              {{ t(locale, "downloadAllCovers") }} ({{ totalLivePhotos }})
            </button>
          </div>
        </div>
      </div>
      <div ref="infoColumnEl" class="rs-info-col">
        <div v-if="author" ref="authorCardEl" class="rs-card rs-card--author">
          <div class="rs-author-inner">
            <div class="rs-author-avatar-wrap">
              <img
                v-if="author.avatar"
                :src="urlOf(author.avatar)"
                class="rs-author-avatar"
                :alt="author.name || 'Author'"
                referrerpolicy="no-referrer"
              />
              <div v-else class="rs-author-avatar-fb">
                <AppIcon class="fas fa-user" />
              </div>
            </div>
            <div class="rs-author-detail">
              <div
                class="rs-author-name rs-copyable"
                @click="copyToClipboard(author.name)"
              >
                {{ author.name || t(locale, "unknownAuthor") }}
              </div>
              <div
                v-if="author.id"
                class="rs-author-id rs-copyable"
                @click="copyToClipboard(author.id)"
              >
                @{{ author.id }}
              </div>
              <div
                v-if="
                  resultData.statistics?.likes?.state !== 'unverified' &&
                  resultData.statistics?.likes
                "
                class="rs-author-likes"
              >
                <AppIcon class="fas fa-heart" />{{
                  t(locale, "infoLikes").replace(
                    "{count}",
                    formatNumber(resultData.statistics.likes.value),
                  )
                }}
              </div>
            </div>
          </div>
        </div>
        <div ref="infoCardEl" class="rs-card rs-card--info">
          <nav ref="tabsEl" class="rs-tabs">
            <button
              v-for="tab in tabs"
              :key="tab.key"
              @click="activeTab = tab.key"
              class="rs-tab"
              :class="{ 'rs-tab--on': activeTab === tab.key }"
            >
              <AppIcon :class="['fas', tab.icon]" /><span>{{ tab.label }}</span>
            </button>
          </nav>
          <div
            v-if="activeTab === 'info' || canAlignDetails"
            ref="detailsPanelEl"
            class="rs-tab-panel rs-tab-panel--details"
            :class="{ 'rs-tab-panel--measure': activeTab !== 'info' }"
            :aria-hidden="activeTab !== 'info' ? 'true' : undefined"
            :inert="activeTab !== 'info' ? '' : undefined"
          >
            <div class="rs-info-list">
              <div
                v-for="card in infoCards"
                :key="card.label"
                class="rs-info-row rs-copyable"
                @click="copyToClipboard(card.value)"
              >
                <div class="rs-info-icon">
                  <AppIcon :class="['fas', card.icon]" />
                </div>
                <div class="rs-info-text">
                  <span class="rs-info-key">{{ card.label }}</span
                  ><span class="rs-info-val">{{ card.value }}</span>
                </div>
              </div>
            </div>
          </div>
          <div v-if="activeTab === 'stats'" class="rs-tab-panel">
            <div class="rs-stat-grid">
              <div v-for="stat in stats" :key="stat.key" class="rs-stat-cell">
                <div class="rs-stat-icon">
                  <AppIcon :class="['fas', stat.icon]" />
                </div>
                <div class="rs-stat-num">
                  {{ stat.unverified ? "—" : formatNumber(stat.value) }}
                </div>
                <div class="rs-stat-label">{{ stat.label }}</div>
              </div>
            </div>
            <p
              v-if="stats.some((stat) => stat.unverified)"
              class="rs-stat-note"
            >
              {{ playCountNote }}
            </p>
          </div>
          <div v-if="activeTab === 'backup'" class="rs-tab-panel">
            <div class="rs-backup-label-row">
              <span>{{ t(locale, "selectQuality") }}</span>
              <button
                v-if="hasHiddenQualityVariants"
                class="rs-quality-display-toggle"
                :aria-expanded="showAllQualityVariants"
                @click="showAllQualityVariants = !showAllQualityVariants"
              >
                {{
                  showAllQualityVariants
                    ? locale === "en" ? "Compact view" : "精简展示"
                    : locale === "en"
                      ? `All downloadable versions (${downloadableQualityOptions.length})`
                      : `全部可下载版本 (${downloadableQualityOptions.length})`
                }}
              </button>
            </div>
            <p v-if="unavailableQualityReason" class="rs-stat-note rs-quality-unavailable" role="status">
              {{ unavailableQualityReason }}
            </p>
            <p v-if="higherQualityNotice" class="rs-stat-note rs-higher-quality-note">
              {{ locale === "en" ? "Higher quality is available but requires processing that is not supported yet." : "存在更高画质，需要处理，当前暂不支持。" }}
            </p>
            <div class="rs-backup-list">
              <div
                v-for="option in qualityOptions"
                :key="option.variant.id"
                class="rs-backup-group"
              >
                <button
                  @click="selectQuality(option)"
                  class="rs-backup-item"
                  :class="{ 'rs-backup-item--cur': option.active }"
                >
                  <div class="rs-backup-hd">
                    <span class="rs-backup-name"
                      >{{
                        formatMediaSize(option.source?.size_bytes, locale)
                      }}
                      · {{ variantLabel(option.variant, locale) }}</span
                    ><span v-if="option.active" class="rs-backup-playing"
                      ><AppIcon class="fas fa-circle-check rs-backup-cur" />{{
                        t(locale, "currentPlaying")
                      }}</span
                    >
                  </div>
                  <div class="rs-backup-meta">
                    <span
                      v-if="option.variant.provenance === 'original_endpoint'"
                      class="rs-backup-meta-tag"
                      >原始入口</span
                    ><span
                      v-if="option.variant.delivery?.sources?.length > 1"
                      class="rs-backup-meta-tag"
                      >{{ option.variant.delivery.sources.length }} 条线路</span
                    ><span
                      v-for="rate in qualityBitrates(option.variant.metadata)"
                      :key="rate.key"
                      class="rs-backup-meta-tag"
                    ><AppIcon class="fas fa-wave-square" />
                      {{ rate.label }} {{ formatBitrate(rate.value) }}
                    </span
                    ><span
                      v-if="
                        option.variant.metadata?.width &&
                        option.variant.metadata?.height
                      "
                      class="rs-backup-meta-tag"
                      ><AppIcon class="fas fa-arrows-alt" />{{
                        option.variant.metadata.width
                      }}×{{ option.variant.metadata.height }}</span
                    ><span
                      v-if="option.variant.metadata?.video_codec"
                      class="rs-backup-meta-tag"
                      ><AppIcon class="fas fa-microchip" />{{
                        option.variant.metadata.video_codec
                      }}</span
                    ><span
                      v-if="option.capabilities.length"
                      class="rs-backup-meta-tag"
                      >{{ option.capabilities.join(" + ") }}</span
                    >
                  </div>
                </button>
              </div>
            </div>
            <details v-if="otherQualityOptions.length" :key="currentItem.id" class="rs-other-versions">
              <summary>{{ locale === "en" ? "Other versions" : "其他版本" }} ({{ otherQualityOptions.length }})</summary>
              <div v-for="option in otherQualityOptions" :key="option.variant.id" class="rs-other-version">
                <div class="rs-backup-name">{{ variantLabel(option.variant, locale) }}</div>
                <div class="rs-backup-meta">
                  <span v-for="specification in variantSpecifications(option.variant)" :key="specification" class="rs-backup-meta-tag">{{ specification }}</span>
                  <span v-if="option.capabilities.length" class="rs-backup-meta-tag">{{ option.capabilities.join(" + ") }}</span>
                </div>
                <p class="rs-stat-note">{{ option.downloadTarget.message }}</p>
              </div>
            </details>
          </div>
          <div v-if="activeTab === 'tags'" class="rs-tab-panel">
            <div class="rs-tags-wrap">
              <span
                v-for="tag in allTags"
                :key="tag"
                class="rs-tag rs-tag--hash rs-copyable-tag"
                @click="copyToClipboard(tag)"
                ># {{ tag }}</span
              >
            </div>
          </div>
          <div v-if="activeTab === 'music'" class="rs-tab-panel">
            <div
              v-for="attachment in attachments"
              :key="attachment.id"
              class="rs-music"
            >
              <div class="rs-music-title">
                {{ attachment.title || t(locale, "unknownMusic") }}
              </div>
              <div class="rs-music-artist">
                {{ attachment.author?.name || "" }}
              </div>
              <audio
                v-if="
                  attachment.role === 'background_audio' &&
                  attachmentUrl(attachment)
                "
                :src="attachmentUrl(attachment)"
                controls
                class="rs-music-player"
              />
              <p v-else-if="!attachmentUrl(attachment)" class="rs-stat-note">
                该附件暂不支持直接播放
              </p>
              <button
                @click="emit('download-music', attachment)"
                class="rs-btn rs-btn--primary rs-btn--sm rs-btn--block"
                :disabled="isYouTubeHelperBlocked"
              >
                <AppIcon class="fas fa-download" />{{
                  locale === "en" ? "Download" : "下载附件"
                }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
    <Teleport to="body"
      ><Transition name="rs-modal"
        ><div
          v-if="imageModalOpen && imageItems[activeImageIndex]"
          class="rs-modal"
          @click.self="closeImageModal"
          role="dialog"
          aria-modal="true"
          :aria-label="t(locale, 'preview')"
        >
          <button
            @click="closeImageModal"
            class="rs-modal-close"
            :aria-label="t(locale, 'close')"
          >
            <AppIcon class="fas fa-times" /></button
          ><button
            v-if="imageItems.length > 1"
            @click="prevImage"
            class="rs-modal-nav rs-modal-nav--prev"
          >
            <AppIcon class="fas fa-chevron-left" /></button
          ><button
            v-if="imageItems.length > 1"
            @click="nextImage"
            class="rs-modal-nav rs-modal-nav--next"
          >
            <AppIcon class="fas fa-chevron-right" />
          </button>
          <div class="rs-modal-body">
            <img
              :src="imageUrl(imageItems[activeImageIndex])"
              class="rs-modal-img"
              :alt="imageItems[activeImageIndex].title || 'Image'"
            />
          </div>
          <button
            class="rs-btn rs-btn--primary"
            @click="emit('download-image', imageItems[activeImageIndex])"
          >
            <AppIcon class="fas fa-download" />{{
              locale === "en" ? "Download image" : "下载图片"
            }}
          </button>
          <div class="rs-modal-num">
            {{ activeImageIndex + 1 }} / {{ imageItems.length }}
          </div>
        </div></Transition
      ></Teleport
    >
    <Teleport to="body"
      ><Transition name="rs-toast"
        ><div v-if="showCopyToast" class="rs-copy-toast">
          <div class="rs-copy-toast-icon"><AppIcon class="fas fa-check" /></div>
          <div class="rs-copy-toast-content">
            <div class="rs-copy-toast-title">{{ t(locale, "copied") }}</div>
            <div class="rs-copy-toast-text">{{ copyToastText }}</div>
          </div>
        </div></Transition
      ></Teleport
    >
  </section>
</template>
<style scoped>
/* ═══════════════════════════════════════════════════════════════
   DESIGN TOKENS — enterprise-grade design system
   ═══════════════════════════════════════════════════════════════ */
.rs {
  /* palette */
  --c-accent: #6c5ce7;
  --c-accent-light: #818cf8;
  --c-accent-dark: #4f46e5;
  --c-accent-2: #a855f7;
  --c-accent-3: #ec4899;
  --c-accent-4: #f472b6;
  --c-success: #10b981;
  --c-success-light: #34d399;
  --c-danger: #ef4444;
  --c-danger-light: #f87171;
  --c-warn: #f59e0b;
  --c-warn-light: #fbbf24;
  --c-info: #06b6d4;
  /* surfaces */
  --c-bg: #fff;
  --c-bg-2: #f8fafc;
  --c-bg-3: #f1f5f9;
  --c-bg-elevated: #ffffff;
  --c-fg: #0f172a;
  --c-fg-2: #475569;
  --c-fg-3: #94a3b8;
  --c-fg-4: #cbd5e1;
  --c-border: #e2e8f0;
  --c-border-2: #f1f5f9;
  --c-border-light: #f8fafc;
  /* shadows */
  --sh-sm: 0 1px 2px rgba(0,0,0,.03);
  --sh-md: 0 4px 12px rgba(0,0,0,.06);
  --sh-lg: 0 8px 28px rgba(0,0,0,.08);
  --sh-xl: 0 16px 48px rgba(0,0,0,.12);
  --sh-2xl: 0 24px 72px rgba(0,0,0,.16);
  --sh-glow: 0 0 40px rgba(108,92,231,.15);
  /* radii */
  --r-sm: .5rem;
  --r-md: .75rem;
  --r-lg: 1rem;
  --r-xl: 1.25rem;
  --r-2xl: 1.5rem;
  --r-3xl: 2rem;
  --r-full: 9999px;
  /* motion */
  --t-fast: 150ms cubic-bezier(0.4, 0, 0.2, 1);
  --t-base: 220ms cubic-bezier(0.4, 0, 0.2, 1);
  --t-slow: 350ms cubic-bezier(0.4, 0, 0.2, 1);
  /* layout */
  --content-max: 1320px;
  --sidebar-w: 380px;
  --sidebar-w-xl: 420px;
  --gap: 1.5rem;
  --gap-lg: 2rem;

  max-width: var(--content-max);
  margin: 0 auto 4rem;
  padding: 0 1rem;
  font-family: inherit;
  color: var(--c-fg);
  background: linear-gradient(180deg, rgba(108,92,231,.02) 0%, rgba(168,85,247,.01) 100%);
  border-radius: var(--r-2xl);
  padding: 1.5rem;
}

/* dark mode overrides（注意：不要用 :global(.dark) 前缀——编译器会丢弃 .rs 部分，
   导致 token 变成 html 全局变量被 .rs 的浅色定义覆盖；普通后代选择器才会编译为 .dark .rs[data-v] */
.dark .rs {
  --c-bg: #0f172a;
  --c-bg-2: #1e293b;
  --c-bg-3: #334155;
  --c-bg-elevated: #1e293b;
  --c-fg: #f1f5f9;
  --c-fg-2: #cbd5e1;
  --c-fg-3: #94a3b8;
  --c-fg-4: #64748b;
  --c-border: #334155;
  --c-border-2: #1e293b;
  --c-border-light: #334155;
  --sh-sm: 0 1px 2px rgba(0,0,0,.2);
  --sh-md: 0 4px 12px rgba(0,0,0,.25);
  --sh-lg: 0 8px 28px rgba(0,0,0,.35);
  --sh-xl: 0 16px 48px rgba(0,0,0,.45);
  --sh-2xl: 0 24px 72px rgba(0,0,0,.5);
  --sh-glow: 0 0 40px rgba(108,92,231,.25);
  background: linear-gradient(180deg, rgba(108,92,231,.08) 0%, rgba(168,85,247,.04) 100%);
}

/* ═══════════════════════════════════════════════════════════════
   HEADER BAR - Modern Style
   ═══════════════════════════════════════════════════════════════ */
.rs-header {
  position: relative;
  margin-bottom: 1.25rem;
  border-radius: var(--r-xl);
  overflow: hidden;
}
.rs-header::before {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(135deg, 
    rgba(108,92,231,.08) 0%, 
    rgba(168,85,247,.06) 50%, 
    rgba(236,72,153,.04) 100%
  );
  border: 1px solid rgba(108,92,231,.12);
}
.rs-header-inner {
  position: relative;
  z-index: 1;
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: .625rem;
  padding: 1rem 1.25rem;
  backdrop-filter: blur(12px);
}
.rs-header-left,
.rs-header-right {
  display: flex;
  align-items: center;
  gap: .75rem;
}

/* Status Dot with Pulse Animation */
.rs-status-dot-wrapper {
  position: relative;
  width: 1rem;
  height: 1rem;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.rs-status-dot {
  width: .55rem; 
  height: .55rem;
  border-radius: 50%;
  background: linear-gradient(135deg, #10b981, #34d399);
  box-shadow: 
    0 0 0 3px rgba(16,185,129,.2),
    0 0 10px rgba(16,185,129,.4);
  position: relative;
  z-index: 2;
}
.rs-status-dot-pulse {
  position: absolute;
  width: .55rem; 
  height: .55rem;
  border-radius: 50%;
  background: rgba(16,185,129,.5);
  animation: rsStatusPulse 2s ease-in-out infinite;
}
@keyframes rsStatusPulse {
  0%, 100% {
    transform: scale(1);
    opacity: .6;
  }
  50% {
    transform: scale(2.4);
    opacity: 0;
  }
}

/* Header Title */
.rs-header-title {
  font-size: .875rem;
  font-weight: 700;
  color: var(--c-fg);
  letter-spacing: .02em;
}

/* Header Badge */
.rs-header-badge {
  display: inline-flex;
  align-items: center;
  gap: .3rem;
  padding: .25rem .65rem;
  border-radius: 999px;
  font-size: .7rem;
  font-weight: 600;
  transition: all var(--t-fast);
}
.rs-header-badge--video { 
  background: linear-gradient(135deg, rgba(108,92,231,.2), rgba(108,92,231,.1)); 
  color: var(--c-accent);
  border: 1px solid rgba(108,92,231,.25);
}
.rs-header-badge--image { 
  background: linear-gradient(135deg, rgba(236,72,153,.2), rgba(236,72,153,.1)); 
  color: var(--c-accent-3);
  border: 1px solid rgba(236,72,153,.25);
}
.rs-header-badge--live  { 
  background: linear-gradient(135deg, rgba(239,68,68,.2), rgba(239,68,68,.1)); 
  color: var(--c-danger);
  border: 1px solid rgba(239,68,68,.25);
}
.rs-header-badge:hover {
  transform: translateY(-1px);
  box-shadow: 0 4px 14px rgba(0,0,0,.1);
}

/* Header Hint */
.rs-header-hint {
  display: flex;
  align-items: center;
  gap: .4rem;
  padding: .35rem .75rem;
  background: rgba(108,92,231,.06);
  border-radius: 999px;
  border: 1px solid rgba(108,92,231,.1);
  transition: all var(--t-fast);
}
.rs-header-hint:hover {
  background: rgba(108,92,231,.1);
  border-color: rgba(108,92,231,.18);
}
.rs-header-hint-icon {
  font-size: .65rem;
  color: var(--c-accent);
}
.rs-header-hint-text {
  font-size: .6875rem;
  color: var(--c-fg-3);
  display: flex;
  align-items: center;
  gap: .25rem;
}

/* ═══════════════════════════════════════════════════════════════
   PLAYBACK ERROR
   ═══════════════════════════════════════════════════════════════ */
.rs-playback-error {
  display: flex;
  flex-wrap: wrap;
  gap: .5rem 1rem;
  margin-bottom: 1.25rem;
  padding: .875rem 1rem;
  border: 1px solid #fbbf24;
  border-radius: .875rem;
  background: #fffbeb;
  color: #92400e;
  font-size: .875rem;
  line-height: 1.6;
}
.rs-playback-error a { color: inherit; font-weight: 600; text-decoration: underline; }
:global(.dark) .rs-playback-error { background: #3b2d14; color: #fde68a; border-color: #a16207; }

/* ═══════════════════════════════════════════════════════════════
   MAIN GRID — responsive: stack → side-by-side
   ═══════════════════════════════════════════════════════════════ */
.rs-main {
  display: grid;
  grid-template-columns: 1fr;
  gap: var(--gap);
  align-items: start;
}

/* Tablet: still stacked but wider inner columns for small cards */
@media (min-width: 768px) {
  .rs-main { gap: 1.75rem; }
}

/* Desktop: side-by-side */
@media (min-width: 1040px) {
  .rs-main {
    grid-template-columns: 1fr var(--sidebar-w);
  }
}
@media (min-width: 1300px) {
  .rs-main {
    grid-template-columns: 1fr var(--sidebar-w-xl);
  }
}

/* ═══════════════════════════════════════════════════════════════
   MEDIA COLUMN
   ═══════════════════════════════════════════════════════════════ */
.rs-media-col,
.rs-info-col {
  min-width: 0;
}
.rs-media-col {
  display: flex;
  flex-direction: column;
  gap: var(--gap);
}

/* ═══════════════════════════════════════════════════════════════
   INFO COLUMN — sticky on desktop
   ═══════════════════════════════════════════════════════════════ */
.rs-info-col {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
@media (min-width: 1040px) {
  .rs-info-col {
    position: sticky;
    top: 1rem;
  }
}

/* ═══════════════════════════════════════════════════════════════
   CARD base
   ═══════════════════════════════════════════════════════════════ */
.rs-card {
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-border);
  border-radius: var(--r-xl);
  box-shadow: var(--sh-md);
  overflow: hidden;
  transition: all var(--t-base);
  position: relative;
}
.rs-card::before {
  content: '';
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 2px;
  background: linear-gradient(90deg, 
    transparent 0%, 
    rgba(108,92,231,.4) 20%, 
    rgba(168,85,247,.3) 50%, 
    rgba(236,72,153,.3) 80%, 
    transparent 100%
  );
  opacity: 0;
  transition: opacity var(--t-base);
}
.rs-card:hover { 
  box-shadow: var(--sh-xl); 
  transform: translateY(-2px);
}
.rs-card:hover::before {
  opacity: 1;
}

/* 视频主体卡片：常驻渐变描边 + 柔光，突出当前播放内容 */
.rs-card--video {
  background:
    linear-gradient(var(--c-bg-elevated), var(--c-bg-elevated)) padding-box,
    linear-gradient(135deg, var(--c-accent), var(--c-accent-2), var(--c-accent-3)) border-box;
  border: 1.5px solid transparent;
}
.rs-card--video::before { opacity: 0; }
.rs-card--video:hover {
  transform: translateY(-2px);
  box-shadow: 0 18px 50px -22px rgba(108, 92, 231, 0.5);
}
/* 选集卡片入场动画 */
.rs-card--playlist { animation: rsFadeIn 0.35s ease; }

.rs-card-hd {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: .5rem;
  padding: .875rem 1.125rem;
  background: var(--c-bg-2);
  border-bottom: 1px solid var(--c-border);
  font-size: .8125rem;
  font-weight: 600;
  color: var(--c-fg-2);
}
.rs-card-hd-left {
  display: flex;
  align-items: center;
  gap: .5rem;
}
.rs-card-icon {
  font-size: .875rem;
  color: var(--c-accent);
}
.rs-card-icon--live { color: var(--c-danger); }

.rs-card-actions {
  display: flex;
  flex-wrap: wrap;
  gap: .5rem;
  justify-content: center;
  padding: .875rem 1.125rem;
  border-top: 1px solid var(--c-border-2);
}

/* ═══════════════════════════════════════════════════════════════
   TAGS (quality / count)
   ═══════════════════════════════════════════════════════════════ */
.rs-quality-tag {
  font-size: .6875rem;
  padding: .15rem .55rem;
  border-radius: 999px;
  background: rgba(108,92,231,.08);
  color: var(--c-accent);
  font-weight: 600;
}
.rs-count-tag {
  font-size: .6875rem;
  padding: .15rem .55rem;
  border-radius: 999px;
  background: rgba(236,72,153,.08);
  color: var(--c-accent-3);
  font-weight: 600;
}
.rs-count-tag--live {
  background: rgba(239,68,68,.08);
  color: var(--c-danger);
}

/* ═══════════════════════════════════════════════════════════════
   VIDEO
   ═══════════════════════════════════════════════════════════════ */
.rs-video-stage {
  background: #0a0a14;
  position: relative;
}
/* B 站多 P 解析中遮罩 */
.rs-stage-overlay {
  position: absolute;
  inset: 0;
  z-index: 3;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 0.75rem;
  color: rgba(255, 255, 255, 0.9);
  font-size: 0.875rem;
  background: rgba(10, 10, 20, 0.62);
  backdrop-filter: blur(2px);
}
.rs-stage-spinner {
  width: 2.25rem;
  height: 2.25rem;
  border-radius: 50%;
  border: 3px solid rgba(255, 255, 255, 0.25);
  border-top-color: var(--color-primary, #6366f1);
  animation: rs-spin 0.8s linear infinite;
}
@keyframes rs-spin {
  to { transform: rotate(360deg); }
}
.rs-playlist {
  padding: 1rem 1.125rem;
}
.rs-video {
  display: block;
  width: 100%;
  max-height: 520px;
  object-fit: contain;
  background: #000;
}
@media (max-width: 640px) {
  .rs-video { max-height: 260px; }
}
@media (min-width: 641px) and (max-width: 1039px) {
  .rs-video { max-height: 400px; }
}

/* The natural details height anchors both columns; tabs only change the right
   card's content height, with overflow inside that card. Mobile remains in flow. */
@media (min-width: 1040px) {
  .rs-main--align-details .rs-media-col {
    height: var(--rs-details-column-height);
    min-height: 0;
  }
  .rs-main--align-details .rs-card--video {
    display: flex;
    flex-direction: column;
    flex: 1;
  }
  .rs-main--align-details .rs-video-stage {
    flex: 1;
    min-height: 0;
  }
  .rs-main--align-details .rs-video {
    position: absolute;
    inset: 0;
    height: 100%;
    max-height: none;
  }
  .rs-main--align-details .rs-info-col {
    max-height: var(--rs-details-column-height);
  }
  .rs-main--align-details .rs-card--author,
  .rs-main--align-details .rs-tabs {
    flex-shrink: 0;
  }
  .rs-main--align-details .rs-card--info {
    display: flex;
    flex-direction: column;
    min-height: 0;
    max-height: var(--rs-details-card-height);
  }
  .rs-main--align-details .rs-tab-panel:not(.rs-tab-panel--details) {
    min-height: 0;
    overflow-y: auto;
    overscroll-behavior-y: contain;
  }
}

/* ═══════════════════════════════════════════════════════════════
   GALLERY — adaptive grid
   ═══════════════════════════════════════════════════════════════ */
.rs-gallery {
  display: grid;
  gap: .625rem;
  padding: 1rem 1.125rem;
}
/* 1 张 */
.rs-gallery--1 { grid-template-columns: 1fr; }
/* 2 张 */
.rs-gallery--2 { grid-template-columns: 1fr 1fr; }
/* 3~6 张: 3 columns desktop, 2 on mobile/tablet */
.rs-gallery--3 { grid-template-columns: repeat(3, 1fr); }
/* >6 张: 4 columns desktop, 3 tablet, 2 mobile */
.rs-gallery--4 { grid-template-columns: repeat(4, 1fr); }

@media (max-width: 767px) {
  .rs-gallery--3 { grid-template-columns: repeat(2, 1fr); }
  .rs-gallery--4 { grid-template-columns: repeat(2, 1fr); }
}
@media (min-width: 768px) and (max-width: 1039px) {
  .rs-gallery--4 { grid-template-columns: repeat(3, 1fr); }
}

.rs-gallery-item {
  position: relative;
  border-radius: var(--r-md);
  overflow: hidden;
  aspect-ratio: 4/3;
  cursor: pointer;
  border: none;
  padding: 0;
  background: var(--c-bg-3);
  transition: transform var(--t-base), box-shadow var(--t-base);
}
.rs-gallery-item:hover {
  transform: translateY(-3px);
  box-shadow: var(--sh-xl);
}
.rs-gallery-item:focus-visible {
  outline: 2px solid var(--c-accent);
  outline-offset: 2px;
}
.rs-gallery-img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  transition: transform var(--t-slow);
}
.rs-gallery-item:hover .rs-gallery-img {
  transform: scale(1.07);
}
.rs-gallery-mask {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0,0,0,0);
  color: #fff;
  font-size: 1.25rem;
  opacity: 0;
  transition: all var(--t-base);
}
.rs-gallery-item:hover .rs-gallery-mask {
  background: rgba(0,0,0,.35);
  opacity: 1;
}

/* ═══════════════════════════════════════════════════════════════
   LIVE PHOTOS - CAROUSEL MODE
   ═══════════════════════════════════════════════════════════════ */
.rs-live-dot {
  display: inline-block;
  width: .45rem; height: .45rem;
  border-radius: 50%;
  background: var(--c-danger);
  animation: rsPulse 1.8s ease infinite;
}

/* Carousel Container */
.rs-live-carousel {
  background: #0a0a14;
}

/* Header Bar */
.rs-live-carousel-hd {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: .625rem .875rem;
  background: rgba(0,0,0,.3);
}

/* Live Badge */
.rs-live-badge {
  display: inline-flex;
  align-items: center;
  gap: .35rem;
  padding: .25rem .65rem;
  background: linear-gradient(135deg, rgba(108,92,231,.9), rgba(168,85,247,.9));
  border-radius: 999px;
  font-size: .7rem;
  font-weight: 600;
  color: #fff;
}

/* Toggle Buttons */
.rs-live-toggle {
  display: flex;
  gap: .3rem;
  background: rgba(255,255,255,.08);
  border-radius: .45rem;
  padding: .15rem;
}
.rs-live-toggle-btn {
  display: inline-flex;
  align-items: center;
  gap: .25rem;
  padding: .25rem .55rem;
  border: none;
  background: transparent;
  border-radius: .35rem;
  color: rgba(255,255,255,.6);
  font-size: .65rem;
  font-weight: 500;
  cursor: pointer;
  transition: all var(--t-fast);
  font-family: inherit;
}
.rs-live-toggle-btn:hover { color: rgba(255,255,255,.9); }
.rs-live-toggle-btn--on {
  background: rgba(255,255,255,.15);
  color: #fff;
}
.rs-live-toggle-btn:disabled { opacity: .4; cursor: not-allowed; }

/* Carousel Body */
.rs-live-carousel-body {
  position: relative;
  aspect-ratio: 16/9;
}

/* Navigation Arrows */
.rs-live-nav {
  position: absolute;
  top: 50%;
  transform: translateY(-50%);
  z-index: 10;
  width: 2.5rem; height: 2.5rem;
  border-radius: 50%;
  border: 1px solid rgba(255,255,255,.2);
  background: rgba(0,0,0,.4);
  color: #fff;
  font-size: .9rem;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all var(--t-fast);
}
.rs-live-nav:hover:not(:disabled) {
  background: rgba(255,255,255,.15);
  border-color: rgba(255,255,255,.3);
}
.rs-live-nav--prev { left: .5rem; }
.rs-live-nav--next { right: .5rem; }
.rs-live-nav:disabled {
  opacity: .2;
  cursor: not-allowed;
}

/* Content Area */
.rs-live-carousel-content {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
}
.rs-live-carousel-video {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: contain;
}
.rs-live-carousel-image {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: contain;
  background: #000;
}

/* Item Actions */
.rs-live-carousel-actions {
  display: flex;
  justify-content: center;
  gap: .5rem;
  padding: .5rem .875rem;
  background: rgba(0,0,0,.2);
  border-top: 1px solid rgba(255,255,255,.05);
}

/* Bulk Download Bar */
.rs-live-bulk-bar {
  display: flex;
  flex-direction: column;
  gap: .5rem;
  padding: .875rem 1.125rem;
  border-top: 1px solid var(--c-border);
}

/* Responsive adjustments */
@media (max-width: 480px) {
  .rs-live-carousel-hd {
    padding: .5rem .625rem;
  }
  .rs-live-badge {
    font-size: .65rem;
    padding: .2rem .5rem;
  }
  .rs-live-toggle-btn {
    padding: .2rem .4rem;
    font-size: .6rem;
  }
  .rs-live-nav {
    width: 2rem; height: 2rem;
    font-size: .75rem;
  }
}

/* Old grid styles kept for compatibility */
.rs-live-bulk {
  display: flex;
  flex-wrap: wrap;
  gap: .5rem;
  padding: .75rem 1.125rem 0;
}

.rs-live-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: .75rem;
  padding: 1rem 1.125rem;
}
@media (min-width: 640px) {
  .rs-live-grid { grid-template-columns: repeat(2, 1fr); }
}
@media (min-width: 1040px) {
  .rs-live-grid { grid-template-columns: repeat(2, 1fr); }
}
@media (min-width: 1300px) {
  .rs-live-grid { grid-template-columns: repeat(3, 1fr); }
}

.rs-live-item {
  position: relative;
  border-radius: var(--r-lg);
  overflow: hidden;
  background: #000;
  transition: transform var(--t-base), box-shadow var(--t-base);
}
.rs-live-item:hover {
  transform: translateY(-2px);
  box-shadow: 0 12px 28px rgba(0,0,0,.35);
}
.rs-live-num {
  position: absolute;
  top: .5rem; left: .5rem; z-index: 2;
  min-width: 1.35rem; height: 1.35rem;
  border-radius: .35rem;
  background: rgba(0,0,0,.55);
  backdrop-filter: blur(6px);
  color: #fff;
  font-size: .625rem;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
}
.rs-live-video {
  display: block;
  width: 100%;
  aspect-ratio: 16/9;
  object-fit: contain;
}

/* ═══════════════════════════════════════════════════════════════
   AUTHOR CARD
   ═══════════════════════════════════════════════════════════════ */
.rs-card--author {
  background: linear-gradient(135deg, 
    rgba(108,92,231,.05) 0%, 
    rgba(168,85,247,.03) 50%, 
    rgba(236,72,153,.02) 100%
  );
  border: 1px solid rgba(108,92,231,.1);
}
.rs-author-inner {
  display: flex;
  align-items: center;
  gap: .875rem;
  padding: 1.125rem 1.25rem;
}
.rs-author-avatar-wrap { 
  position: relative; 
  flex-shrink: 0; 
}
.rs-author-avatar {
  width: 3rem; 
  height: 3rem;
  border-radius: var(--r-md);
  object-fit: cover;
  box-shadow: 
    0 0 0 2px var(--c-bg-elevated), 
    0 0 0 4px rgba(108,92,231,.15),
    0 4px 12px rgba(0,0,0,.08);
  transition: transform var(--t-fast);
}
.rs-author-avatar-fb {
  width: 3rem; 
  height: 3rem;
  border-radius: var(--r-md);
  background: linear-gradient(135deg, var(--c-bg-3), var(--c-bg-2));
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--c-fg-3);
  font-size: 1.125rem;
  box-shadow: 
    0 0 0 2px var(--c-bg-elevated), 
    0 0 0 4px rgba(108,92,231,.1),
    0 4px 12px rgba(0,0,0,.06);
}
.rs-author-inner:hover .rs-author-avatar {
  transform: scale(1.05);
}
.rs-author-detail { min-width: 0; }
.rs-author-name {
  font-size: .875rem;
  font-weight: 700;
  color: var(--c-fg);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.rs-author-id {
  font-size: .6875rem;
  color: var(--c-fg-3);
  margin-top: .1rem;
}
.rs-author-likes {
  display: flex;
  align-items: center;
  gap: .2rem;
  font-size: .6875rem;
  color: var(--c-accent-3);
  margin-top: .15rem;
}

/* Copyable Elements */
.rs-copyable {
  position: relative;
  cursor: pointer;
  transition: all var(--t-fast);
}
.rs-copyable:hover {
  color: var(--c-accent);
}
.rs-copy-success {
  position: absolute;
  right: -1.25rem;
  top: 50%;
  transform: translateY(-50%);
  font-size: .55rem;
  color: var(--c-success);
}

/* Info Row Copy Style */
.rs-info-row.rs-copyable {
  padding-right: 1.5rem;
}
.rs-info-row.rs-copyable:hover {
  background: rgba(108,92,231,.04);
  border-radius: var(--r-sm);
}

/* ═══════════════════════════════════════════════════════════════
   TABS
   ═══════════════════════════════════════════════════════════════ */
.rs-tabs {
  display: flex;
  overflow-x: auto;
  -webkit-overflow-scrolling: touch;
  scrollbar-width: none;
  border-bottom: 1px solid var(--c-border);
  padding: 0 .5rem;
  gap: 0;
}
.rs-tabs::-webkit-scrollbar { display: none; }
.rs-tab {
  display: flex;
  align-items: center;
  gap: .3rem;
  padding: .7rem .75rem;
  border: none;
  background: none;
  font-size: .75rem;
  font-weight: 500;
  color: var(--c-fg-3);
  cursor: pointer;
  white-space: nowrap;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  transition: all var(--t-fast);
  font-family: inherit;
}
.rs-tab:hover { color: var(--c-fg-2); }
.rs-tab--on {
  color: var(--c-accent);
  border-bottom-color: var(--c-accent);
  font-weight: 600;
}

.rs-tab-panel {
  padding: 1rem 1.125rem;
  animation: rsFadeIn .2s ease;
}
.rs-tab-panel--details {
  flex-shrink: 0;
}
.rs-tab-panel--measure {
  position: absolute;
  inset: 0 0 auto;
  visibility: hidden;
  pointer-events: none;
  animation: none;
}

/* ═══════════════════════════════════════════════════════════════
   INFO LIST
   ═══════════════════════════════════════════════════════════════ */
.rs-info-list {
  display: flex;
  flex-direction: column;
  gap: .5rem;
}
.rs-info-row {
  display: flex;
  gap: .625rem;
  padding: .625rem .75rem;
  background: var(--c-bg-2);
  border-radius: var(--r-md);
  border: 1px solid var(--c-border-2);
  transition: border-color var(--t-fast), background var(--t-fast);
}
.rs-info-row:hover {
  border-color: rgba(108,92,231,.2);
  background: linear-gradient(135deg, rgba(108,92,231,.02) 0%, rgba(168,85,247,.02) 100%);
}
.rs-info-icon {
  width: 1.75rem; height: 1.75rem;
  border-radius: .4rem;
  background: rgba(108,92,231,.08);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--c-accent);
  font-size: .7rem;
  flex-shrink: 0;
}
.rs-info-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.rs-info-key {
  font-size: .625rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: .04em;
  color: var(--c-fg-3);
}
.rs-info-val {
  font-size: .8125rem;
  font-weight: 600;
  color: var(--c-fg);
  line-height: 1.35;
  word-break: break-all;
}

/* ═══════════════════════════════════════════════════════════════
   STATS
   ═══════════════════════════════════════════════════════════════ */
.rs-stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(95px, 1fr));
  gap: .5rem;
}
.rs-stat-cell {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: .15rem;
  padding: .75rem .4rem;
  background: var(--c-bg-2);
  border-radius: var(--r-md);
  border: 1px solid var(--c-border-2);
  transition: all var(--t-fast);
}
.rs-stat-cell:hover {
  border-color: rgba(108,92,231,.18);
  transform: translateY(-1px);
  box-shadow: var(--sh-sm);
}
.rs-stat-icon {
  font-size: .95rem;
  color: var(--c-accent);
}
.rs-stat-num {
  font-size: .95rem;
  font-weight: 700;
  color: var(--c-fg);
  font-variant-numeric: tabular-nums;
}
.rs-stat-note {
  margin-top: .6rem;
  font-size: .6875rem;
  line-height: 1.5;
  color: var(--c-fg-3);
}
.rs-stat-label {
  font-size: .625rem;
  color: var(--c-fg-3);
  font-weight: 500;
}

/* ═══════════════════════════════════════════════════════════════
   BACKUP QUALITIES
   ═══════════════════════════════════════════════════════════════ */
.rs-backup-label-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: .5rem;
  font-size: .6875rem;
  color: var(--c-fg-3);
  margin-bottom: .5rem;
  font-weight: 500;
}
.rs-quality-display-toggle {
  border: 0;
  padding: 0;
  background: none;
  color: var(--c-accent);
  font: inherit;
  cursor: pointer;
}
.rs-backup-list {
  display: flex;
  flex-direction: column;
  gap: .4rem;
}
.rs-other-versions {
  margin-top: .65rem;
  color: var(--c-fg-3);
  font-size: .6875rem;
}
.rs-other-versions > summary {
  cursor: pointer;
}
.rs-other-version {
  display: grid;
  gap: .25rem;
  padding: .5rem 0;
  border-bottom: 1px solid var(--c-border-2);
}
.rs-backup-item {
  display: flex;
  flex-direction: column;
  gap: .25rem;
  padding: .5rem .75rem;
  background: var(--c-bg-2);
  border: 1px solid var(--c-border-2);
  border-radius: var(--r-md);
  cursor: pointer;
  text-align: left;
  font-family: inherit;
  width: 100%;
  transition: all var(--t-fast);
}
.rs-backup-item:hover {
  border-color: rgba(108,92,231,.25);
  background: linear-gradient(135deg, rgba(108,92,231,.03) 0%, rgba(168,85,247,.03) 100%);
}
.rs-backup-item--cur {
  border-color: var(--c-accent);
  background: rgba(108,92,231,.06);
  box-shadow: 0 0 0 1px var(--c-accent);
}
.rs-backup-hd {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: .5rem;
}
.rs-backup-name {
  font-size: .8125rem;
  font-weight: 600;
  color: var(--c-fg);
}
.rs-backup-cur {
  color: var(--c-accent);
  font-size: .8rem;
}
.rs-backup-meta {
  display: flex;
  flex-wrap: wrap;
  gap: .35rem;
}
.rs-backup-meta-tag {
  display: inline-flex;
  align-items: center;
  gap: .2rem;
  font-size: .625rem;
  color: var(--c-fg-3);
  background: var(--c-bg);
  padding: .1rem .45rem;
  border-radius: .25rem;
}
.rs-backup-playing {
  display: inline-flex;
  align-items: center;
  gap: .25rem;
  flex-shrink: 0;
  white-space: nowrap;
  font-size: .625rem;
  font-weight: 600;
  color: var(--c-accent);
}

/* empty */
.rs-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: .35rem;
  padding: 1.5rem 0;
  color: var(--c-fg-3);
  font-size: .8125rem;
}
.rs-empty i { font-size: 1.5rem; }

/* ═══════════════════════════════════════════════════════════════
   TAGS
   ═══════════════════════════════════════════════════════════════ */
.rs-tags-wrap {
  display: flex;
  flex-wrap: wrap;
  gap: .5rem;
}
.rs-tag {
  display: inline-flex;
  align-items: center;
  gap: .25rem;
  padding: .35rem .75rem;
  border-radius: 999px;
  font-size: .6875rem;
  font-weight: 600;
  transition: all var(--t-fast);
  position: relative;
  overflow: hidden;
}
.rs-tag::before {
  content: '';
  position: absolute;
  inset: 0;
  opacity: 0;
  transition: opacity var(--t-fast);
}
.rs-tag--hash {
  background: linear-gradient(135deg, rgba(108,92,231,.08) 0%, rgba(168,85,247,.06) 100%);
  color: var(--c-accent);
  border: 1px solid rgba(108,92,231,.15);
  box-shadow: 0 1px 2px rgba(108,92,231,.08);
}
.rs-tag--hash::before {
  background: linear-gradient(135deg, rgba(108,92,231,.1) 0%, rgba(168,85,247,.08) 100%);
}
.rs-tag--hash:hover {
  background: linear-gradient(135deg, rgba(108,92,231,.12) 0%, rgba(168,85,247,.1) 100%);
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(108,92,231,.15);
}
.rs-tag--vt {
  background: linear-gradient(135deg, rgba(236,72,153,.08) 0%, rgba(244,114,182,.06) 100%);
  color: var(--c-accent-3);
  border: 1px solid rgba(236,72,153,.15);
  box-shadow: 0 1px 2px rgba(236,72,153,.08);
}
.rs-tag--vt::before {
  background: linear-gradient(135deg, rgba(236,72,153,.1) 0%, rgba(244,114,182,.08) 100%);
}
.rs-tag--vt:hover {
  background: linear-gradient(135deg, rgba(236,72,153,.12) 0%, rgba(244,114,182,.1) 100%);
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(236,72,153,.15);
}

/* Tag Copy Styles */
.rs-copyable-tag {
  position: relative;
  cursor: pointer;
}
.rs-tag-copy-success {
  position: absolute;
  right: -.25rem;
  top: 50%;
  transform: translateY(-50%) translateX(100%);
  font-size: .5rem;
  color: var(--c-success);
}

/* ═══════════════════════════════════════════════════════════════
   MUSIC
   ═══════════════════════════════════════════════════════════════ */
.rs-music {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: .75rem;
}
.rs-music-cover-wrap {
  position: relative;
}
.rs-music-cover {
  width: 4.5rem; height: 4.5rem;
  border-radius: 50%;
  object-fit: cover;
  box-shadow: var(--sh-md);
  animation: rsSpin 14s linear infinite paused;
}
.rs-music:hover .rs-music-cover { animation-play-state: running; }
.rs-music-cover-fb {
  width: 4.5rem; height: 4.5rem;
  border-radius: 50%;
  background: rgba(108,92,231,.1);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--c-accent);
  font-size: 1.35rem;
}
.rs-music-disc {
  position: absolute;
  top: 50%; left: 50%;
  transform: translate(-50%, -50%);
  width: 1.25rem; height: 1.25rem;
  border-radius: 50%;
  background: var(--c-bg);
  box-shadow: 0 1px 4px rgba(0,0,0,.1);
}
.rs-music-info {
  text-align: center;
}
.rs-music-title {
  font-size: .8125rem;
  font-weight: 600;
  color: var(--c-fg);
}
.rs-music-artist {
  font-size: .6875rem;
  color: var(--c-fg-3);
  margin-top: .1rem;
}
.rs-music-player {
  width: 100%;
  height: 2.25rem;
  border-radius: 999px;
}

/* ═══════════════════════════════════════════════════════════════
   BUTTONS - Modern Style
   ═══════════════════════════════════════════════════════════════ */
.rs-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: .5rem;
  padding: .65rem 1.25rem;
  font-size: .7875rem;
  font-weight: 600;
  border-radius: var(--r-lg);
  border: none;
  cursor: pointer;
  font-family: inherit;
  transition: all var(--t-base);
  white-space: nowrap;
  line-height: 1.4;
  position: relative;
  overflow: hidden;
}
.rs-btn::before {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(135deg, rgba(255,255,255,.1) 0%, transparent 50%);
  opacity: 0;
  transition: opacity var(--t-base);
}
.rs-btn:hover::before { opacity: 1; }
.rs-btn:active { transform: scale(.96); }
.rs-btn--sm { 
  padding: .45rem .85rem; 
  font-size: .7rem; 
  border-radius: .55rem; 
  gap: .35rem;
}
.rs-btn--block { width: 100%; }
.rs-btn:disabled {
  opacity: .5;
  cursor: not-allowed;
  transform: none;
}

/* Primary Button - Purple Gradient */
.rs-btn--primary {
  background: linear-gradient(135deg, #6366f1 0%, #8b5cf6 50%, #a855f7 100%);
  color: #fff;
  box-shadow: 
    0 4px 16px rgba(108,92,231,.35),
    0 1px 3px rgba(0,0,0,.1);
}
.rs-btn--primary:hover {
  transform: translateY(-2px);
  box-shadow: 
    0 8px 28px rgba(108,92,231,.45),
    0 2px 6px rgba(0,0,0,.12);
}

/* Accent Button - Pink Gradient */
.rs-btn--accent {
  background: linear-gradient(135deg, #a855f7 0%, #ec4899 100%);
  color: #fff;
  box-shadow: 
    0 4px 16px rgba(168,85,247,.35),
    0 1px 3px rgba(0,0,0,.1);
}
.rs-btn--accent:hover {
  transform: translateY(-2px);
  box-shadow: 
    0 8px 28px rgba(168,85,247,.45),
    0 2px 6px rgba(0,0,0,.12);
}

/* Ghost Button - 跟随主题 token（浅色下为浅灰描边，深色下为暗底半透明） */
.rs-btn--ghost {
  background: var(--c-bg-3);
  color: var(--c-fg-2);
  border: 1px solid var(--c-border);
}
.rs-btn--ghost:hover {
  background: var(--c-bg-2);
  border-color: var(--c-accent);
  color: var(--c-fg);
  transform: translateY(-1px);
}

/* 深色实底区域（实况轮播操作条）内保持白色玻璃按钮 */
.rs-live-carousel-actions .rs-btn--ghost {
  background: rgba(255,255,255,.08);
  color: rgba(255,255,255,.85);
  border: 1px solid rgba(255,255,255,.15);
  backdrop-filter: blur(8px);
}
.rs-live-carousel-actions .rs-btn--ghost:hover {
  background: rgba(255,255,255,.15);
  border-color: rgba(255,255,255,.25);
  color: #fff;
  transform: translateY(-1px);
}

/* Ghost Button for Light Background */
.rs-btn--ghost-light {
  background: var(--c-bg-2);
  color: var(--c-fg-2);
  border: 1px solid var(--c-border);
}
.rs-btn--ghost-light:hover {
  background: var(--c-bg-3);
  border-color: var(--c-accent);
  color: var(--c-fg);
  transform: translateY(-1px);
}

/* Bulk Download Button */
.rs-btn--bulk {
  padding: .8rem 1.5rem;
  font-size: .825rem;
  font-weight: 700;
  border-radius: var(--r-xl);
  background: linear-gradient(135deg, #6366f1 0%, #8b5cf6 50%, #a855f7 100%);
  color: #fff;
  box-shadow: 
    0 6px 24px rgba(108,92,231,.4),
    0 2px 8px rgba(0,0,0,.15);
}
.rs-btn--bulk:hover {
  transform: translateY(-3px);
  box-shadow: 
    0 12px 36px rgba(108,92,231,.5),
    0 3px 10px rgba(0,0,0,.2);
}

/* ═══════════════════════════════════════════════════════════════
   IMAGE MODAL
   ═══════════════════════════════════════════════════════════════ */
.rs-modal {
  position: fixed;
  inset: 0;
  z-index: 10000;
  background: rgba(0,0,0,.96);
  backdrop-filter: blur(10px);
  display: flex;
  align-items: center;
  justify-content: center;
}
.rs-modal-close {
  position: absolute;
  top: 1rem; right: 1rem; z-index: 10;
  width: 2.5rem; height: 2.5rem;
  border-radius: 50%;
  background: rgba(255,255,255,.08);
  border: 1px solid rgba(255,255,255,.12);
  color: #fff;
  font-size: 1rem;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: background var(--t-fast);
}
.rs-modal-close:hover { background: rgba(255,255,255,.18); }

.rs-modal-nav {
  position: absolute;
  top: 50%;
  transform: translateY(-50%);
  z-index: 10;
  width: 2.75rem; height: 2.75rem;
  border-radius: 50%;
  background: rgba(255,255,255,.08);
  border: 1px solid rgba(255,255,255,.12);
  color: #fff;
  font-size: 1rem;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: background var(--t-fast);
}
.rs-modal-nav:hover { background: rgba(255,255,255,.2); }
.rs-modal-nav--prev { left: 1rem; }
.rs-modal-nav--next { right: 1rem; }
@media (max-width: 640px) {
  .rs-modal-nav { width: 2.25rem; height: 2.25rem; font-size: .8rem; }
  .rs-modal-nav--prev { left: .4rem; }
  .rs-modal-nav--next { right: .4rem; }
}

.rs-modal-body {
  display: flex;
  align-items: center;
  justify-content: center;
  max-width: 90vw;
  max-height: 85vh;
  padding: 2rem;
}
.rs-modal-img {
  max-width: 100%;
  max-height: 80vh;
  object-fit: contain;
  border-radius: .5rem;
  box-shadow: 0 24px 64px rgba(0,0,0,.5);
}

.rs-modal-pager {
  position: absolute;
  bottom: 1.25rem;
  left: 50%;
  transform: translateX(-50%);
  display: flex;
  gap: .4rem;
}
.rs-modal-dot {
  width: .4rem; height: .4rem;
  border-radius: 50%;
  background: rgba(255,255,255,.3);
  border: none;
  cursor: pointer;
  padding: 0;
  transition: all var(--t-fast);
}
.rs-modal-dot:hover { background: rgba(255,255,255,.55); }
.rs-modal-dot--on {
  background: #fff;
  width: 1.25rem;
  border-radius: 999px;
}
.rs-modal-num {
  position: absolute;
  bottom: 1.25rem;
  right: 1.25rem;
  color: rgba(255,255,255,.55);
  font-size: .75rem;
  font-variant-numeric: tabular-nums;
}

/* transition */
.rs-modal-enter-active { transition: opacity .25s ease; }
.rs-modal-leave-active { transition: opacity .18s ease; }
.rs-modal-enter-from,
.rs-modal-leave-to { opacity: 0; }

/* ═══════════════════════════════════════════════════════════════
   KEYFRAMES
   ═══════════════════════════════════════════════════════════════ */
@keyframes rsFadeIn {
  from { opacity: 0; transform: translateY(6px); }
  to   { opacity: 1; transform: translateY(0); }
}
@keyframes rsPulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50%      { opacity: .45; transform: scale(.75); }
}
@keyframes rsSpin {
  from { transform: rotate(0deg); }
  to   { transform: rotate(360deg); }
}

@media (prefers-reduced-motion: reduce) {
  .rs-tab-panel { animation: none; }
  .rs-music-cover { animation: none !important; }
  .rs-live-dot { animation: none; }
}

/* ═══════════════════════════════════════════════════════════════
   RESPONSIVE FINE-TUNING
   ═══════════════════════════════════════════════════════════════ */
@media (max-width: 480px) {
  .rs {
    padding: 0 .5rem;
    margin-bottom: 2.5rem;
    --gap: 1rem;
  }
  .rs-header {
    border-radius: var(--r-md);
    padding: .5rem .75rem;
  }
  .rs-card { border-radius: var(--r-lg); }
  .rs-card-hd { padding: .65rem .875rem; }
  .rs-card-actions { padding: .65rem .875rem; }
  .rs-video { max-height: 220px; }
  .rs-stat-grid { grid-template-columns: repeat(3, 1fr); }
  .rs-live-grid { grid-template-columns: 1fr; }
}

@media (min-width: 481px) and (max-width: 767px) {
  .rs-live-grid { grid-template-columns: repeat(2, 1fr); }
  .rs-video { max-height: 320px; }
}

/* ═══════════════════════════════════════════════════════════════
   COPY TOAST
   ═══════════════════════════════════════════════════════════════ */
.rs-copy-toast {
  position: fixed;
  right: 1.5rem;
  bottom: 1.5rem;
  display: flex;
  align-items: center;
  gap: .75rem;
  padding: .75rem 1rem;
  background: var(--c-bg-elevated);
  border-radius: var(--r-lg);
  box-shadow: var(--sh-lg);
  border: 1px solid var(--c-border);
  z-index: 9999;
  min-width: 180px;
}
.rs-copy-toast-icon {
  width: 2rem;
  height: 2rem;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, #10b981, #34d399);
  border-radius: 50%;
  flex-shrink: 0;
}
.rs-copy-toast-icon i {
  color: white;
  font-size: .75rem;
}
.rs-copy-toast-content {
  display: flex;
  flex-direction: column;
  gap: .125rem;
}
.rs-copy-toast-title {
  font-size: .75rem;
  font-weight: 600;
  color: var(--c-fg);
}
.rs-copy-toast-text {
  font-size: .65rem;
  color: var(--c-fg-3);
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Toast Animation */
.rs-toast-enter-active {
  animation: toast-in 0.3s cubic-bezier(0.16, 1, 0.3, 1);
}
.rs-toast-leave-active {
  animation: toast-out 0.2s cubic-bezier(0.16, 1, 0.3, 1);
}

@keyframes toast-in {
  from {
    opacity: 0;
    transform: translateX(100%) translateY(20%);
  }
  to {
    opacity: 1;
    transform: translateX(0) translateY(0);
  }
}

@keyframes toast-out {
  from {
    opacity: 1;
    transform: translateX(0) translateY(0);
  }
  to {
    opacity: 0;
    transform: translateX(100%) translateY(20%);
  }
}

/* Dark Mode：复制 Toast 随 .rs 内 token 自动翻转，无需单独覆盖 */
.rs-yt-helper-card,
.rs-yt-helper-alert,
.rs-yt-helper-notice {
  margin-bottom: 1.25rem;
  padding: 1rem 1.25rem;
  border-radius: var(--r-xl);
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-border);
  box-shadow: var(--sh-sm);
}
.rs-yt-helper-alert {
  border-color: rgba(239, 68, 68, 0.35);
  background: rgba(239, 68, 68, 0.06);
}
.rs-yt-helper-alert-title {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-weight: 700;
  color: var(--c-danger);
  margin-bottom: 0.375rem;
}
.rs-yt-helper-alert-desc {
  margin: 0;
  font-size: 0.875rem;
  color: var(--c-fg-2);
  line-height: 1.6;
}
.rs-yt-helper-card-hd {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 1rem;
  font-weight: 700;
  color: var(--c-fg);
  margin-bottom: 0.5rem;
}
.rs-yt-helper-card-intro {
  margin: 0 0 0.75rem;
  font-size: 0.875rem;
  color: var(--c-fg-2);
  line-height: 1.6;
}
.rs-yt-helper-steps {
  margin: 0 0 1rem;
  padding-left: 1.25rem;
  font-size: 0.875rem;
  color: var(--c-fg-2);
  line-height: 1.75;
}
.rs-yt-helper-steps code {
  padding: 0.125rem 0.375rem;
  border-radius: var(--r-sm);
  background: var(--c-bg-3);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 0.8125rem;
}
.rs-yt-helper-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
}
.rs-yt-helper-install-link {
  text-decoration: none;
}
.rs-yt-helper-notice {
  display: flex;
  align-items: center;
  gap: 0.625rem;
  font-size: 0.875rem;
  color: var(--c-fg-2);
  border-color: rgba(108, 92, 231, 0.25);
  background: rgba(108, 92, 231, 0.05);
}
</style>
