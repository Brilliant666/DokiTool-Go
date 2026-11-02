<script setup>
import {
  computed,
  defineAsyncComponent,
  h,
  inject,
  ref,
  onMounted,
  watch,
} from "vue";
import { useVideoStore } from "./stores/video";
import HeaderNav from "./components/HeaderNav.vue";
import HeroSection from "./components/HeroSection.vue";
import PlatformGrid from "./components/PlatformGrid.vue";
import TutorialSection from "./components/TutorialSection.vue";
import FaqSection from "./components/FaqSection.vue";
import FooterSection from "./components/FooterSection.vue";
import ToastContainer from "./components/ToastContainer.vue";
import ProgressModal from "./components/ProgressModal.vue";
import ParticlesCanvas from "./components/ParticlesCanvas.vue";
import DownloadCard from "./components/DownloadCard.vue";
import { useVideoParser } from "./composables/useVideoParser";
import { useMediaDownload } from "./composables/useMediaDownload";
import { useVideoSelection } from "./composables/useVideoSelection";
import { refreshPlatformCatalog } from "./utils/platforms";
import {
  homeInitialStateKey,
  readBrowserPreferences,
  persistPreference,
} from "./ssr/initialState";

const initialState = inject(homeInitialStateKey, {
  prerendered: false,
  locale: "zh-CN",
  isDark: false,
  inputUrl: "",
});
const isReady = ref(!initialState.prerendered);

// Share the preload with the async component, but allow a later parse to retry
// after a transient chunk-loading failure.
let resultSectionModule;
const loadResultSection = () =>
  (resultSectionModule ??= import("./components/ResultSection.vue").catch(
    (error) => {
      resultSectionModule = undefined;
      throw error;
    },
  ));
const ResultSection = defineAsyncComponent({
  loader: loadResultSection,
  loadingComponent: {
    setup: () => () =>
      h(
        "p",
        {
          role: "status",
          class: "py-6 text-center text-sm text-slate-500 dark:text-slate-400",
        },
        locale.value === "en" ? "Preparing the result…" : "正在显示解析结果…",
      ),
  },
  errorComponent: {
    setup: () => () =>
      h(
        "p",
        {
          role: "alert",
          class: "py-6 text-center text-sm text-rose-600 dark:text-rose-400",
        },
        locale.value === "en"
          ? "The result could not be displayed. Please parse again."
          : "结果暂时无法显示，请重新解析。",
      ),
  },
});

const videoStore = useVideoStore();
const isDark = ref(initialState.isDark);

const isDownloading = computed(() =>
  videoStore.downloads.some((task) =>
    ["queued", "preparing", "downloading"].includes(task.status),
  ),
);
const locale = ref(initialState.locale);
const {
  inputUrl,
  parseError,
  currentPlatform,
  parseButton,
  parseVideo,
  selectPlatform,
  resolveItem,
} = useVideoParser({
  videoStore,
  locale,
  showToast: (message, type, duration) =>
    videoStore.addToast(message, type, duration),
});
inputUrl.value = initialState.inputUrl;

// Start fetching the result UI alongside a valid parse request, not on the
// initial visit and not only after the API has finished.
watch(
  () => parseButton.isLoading.value,
  (loading) => {
    if (loading) void loadResultSection().catch(() => {});
  },
  { flush: "sync" },
);

const selection = useVideoSelection({ videoStore });

watch(
  () => videoStore.resultVersion,
  () => selection.syncResult(),
  { flush: "sync" },
);

const showToast = (message, type = "success", duration = 3000) => {
  videoStore.addToast(message, type, duration);
};

const {
  restoreDownloads,
  downloadMainVideo,
  downloadVideoByIndex,
  downloadSelected,
  downloadAllVideos,
  downloadVideosAsZip,
  downloadAllMedia,
  downloadMusic,
  downloadLiveVideo,
  downloadLiveCover,
  downloadImage,
  handleCancelDownload,
  handleRetryDownload,
  handleRefreshDownload,
  handleQueryDownload,
  handleBrowserDownload,
  handleClearCompleted,
} = useMediaDownload({
  videoStore,
  showToast,
  selection,
  resolveItem,
});

const resolveSelectedItem = async (item) => {
  if (!item?.resolve_url || videoStore.resolvingItems.has(item.id)) return;
  const version = videoStore.resultVersion;
  videoStore.setResolving(item.id, true);
  try {
    const resolved = await resolveItem(item);
    videoStore.replaceItem(resolved, version);
  } catch (error) {
    if (videoStore.resultVersion === version)
      showToast(error.message || "解析失败", "error");
  } finally {
    if (videoStore.resultVersion === version)
      videoStore.setResolving(item.id, false);
  }
};
watch(
  () => selection.currentVideo.value,
  (item) => {
    if (item?.state === "deferred") void resolveSelectedItem(item);
  },
);

const copyUrl = async (url) => {
  if (!url) {
    showToast("复制失败", "error");
    return;
  }
  try {
    await navigator.clipboard.writeText(String(url));
    showToast("已复制到剪贴板");
  } catch (e) {
    showToast("复制失败", "error");
  }
};

const downloadAll = () => downloadAllMedia("all");
const downloadAllImages = () => downloadAllMedia("images");
const downloadAllLivePhotos = () => downloadAllMedia("live");
const downloadAllLiveCovers = () => downloadAllMedia("covers");

const switchVideo = (choice) => selection.choose(choice);

const formatNumber = (num) => {
  if (!num && num !== 0) return "0";
  const n = Number(num);
  if (n >= 1e8) return (n / 1e8).toFixed(1) + "亿";
  if (n >= 1e4) return (n / 1e4).toFixed(1) + "万";
  return String(n);
};

const formatDuration = (seconds) => {
  const duration = Number(seconds);
  if (!Number.isFinite(duration) || duration <= 0) return "0:00";
  const tenths = Math.round(duration * 10);
  const m = Math.floor(tenths / 600);
  const s = Math.floor((tenths % 600) / 10);
  const fraction = tenths % 10;
  return `${m}:${String(s).padStart(2, "0")}${fraction ? `.${fraction}` : ""}`;
};

const toggleTheme = () => {
  isDark.value = !isDark.value;
  persistPreference("theme", isDark.value ? "dark" : "light");
  document.documentElement.classList.toggle("dark", isDark.value);
  document.documentElement.style.colorScheme = isDark.value ? "dark" : "light";
};

const setLocale = (lang) => {
  locale.value = lang;
  persistPreference("lang", lang);
  document.documentElement.lang = lang;
};

onMounted(() => {
  void refreshPlatformCatalog();
  void restoreDownloads();
  // Static HTML and the first client render use the same language and controls.
  // Restore preferences only after hydration; the head bootstrap already applied
  // the background theme before CSS, without mutating the hydrated subtree.
  const preferences = readBrowserPreferences();
  isDark.value = preferences.isDark;
  locale.value = preferences.locale;
  document.documentElement.classList.toggle("dark", isDark.value);
  document.documentElement.style.colorScheme = isDark.value ? "dark" : "light";
  document.documentElement.lang = locale.value;
  isReady.value = true;
});
</script>

<template>
  <div class="min-h-screen">
    <ParticlesCanvas />
    <ToastContainer />
    <!-- Body teleports are created after hydration; the static page has no
         teleport targets, so Vue must not hydrate them against body content. -->
    <ProgressModal v-if="isReady" />
    <DownloadCard
      :downloads="videoStore.downloads"
      @cancel="handleCancelDownload"
      @retry="handleRetryDownload"
      @refresh="handleRefreshDownload"
      @query="handleQueryDownload"
      @handoff="handleBrowserDownload"
      @clear-completed="handleClearCompleted"
    />

    <HeaderNav
      :is-dark="isDark"
      :locale="locale"
      @toggle-theme="toggleTheme"
      @set-locale="setLocale"
    />

    <main class="max-w-7xl mx-auto px-4 pb-8 pt-0 relative z-10">
      <HeroSection
        v-model:input-url="inputUrl"
        :is-ready="isReady"
        :is-loading="parseButton.isLoading.value"
        :is-error="!!parseError || parseButton.state.value === 'error'"
        :parse-error="parseError"
        :locale="locale"
        :current-platform="currentPlatform"
        @parse="parseVideo"
        @select-platform="selectPlatform"
      />

      <ResultSection
        v-if="videoStore.resultData"
        :result-data="videoStore.resultData"
        :result-version="videoStore.resultVersion"
        :is-downloading="isDownloading"
        :locale="locale"
        :selection="selection"
        :format-number="formatNumber"
        :format-duration="formatDuration"
        :resolving-ids="videoStore.resolvingItems"
        @refresh-item="resolveSelectedItem"
        @download-main="downloadMainVideo"
        @download-all="downloadAll"
        @download-images="downloadAllImages"
        @download-image="downloadImage"
        @download-music="downloadMusic"
        @download-live-video="downloadLiveVideo"
        @download-live-cover="downloadLiveCover"
        @download-all-live="downloadAllLivePhotos"
        @download-all-live-covers="downloadAllLiveCovers"
        @copy-url="copyUrl"
        @switch-video="switchVideo"
        @select-video="selection.selectIndex"
        @toggle-video="selection.toggleSelect"
        @select-all-videos="selection.selectAll"
        @clear-selection="selection.clearSelection"
        @select-collection="selection.setSection"
        @download-index="downloadVideoByIndex"
        @download-selected="downloadSelected"
        @download-all-videos="downloadAllVideos"
        @download-selected-zip="
          (videos) => downloadVideosAsZip(videos, undefined)
        "
        @download-all-zip="(videos) => downloadVideosAsZip(videos, undefined)"
      />

      <PlatformGrid :locale="locale" />
      <TutorialSection :locale="locale" />
      <FaqSection :locale="locale" />
    </main>

    <FooterSection :locale="locale" />
  </div>
</template>
