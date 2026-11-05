<script setup>
import AppIcon from "./AppIcon.vue";
import { ref, computed } from "vue";
import douyinArtwork from "../assets/platforms/douyin.png";
import kuaishouArtwork from "../assets/platforms/kuaishou.ico";
import bilibiliArtwork from "../assets/platforms/bilibili.ico";
import xhsArtwork from "../assets/platforms/xhs.ico";
import toutiaoArtwork from "../assets/platforms/toutiao.ico";
import weiboArtwork from "../assets/platforms/weibo.svg";
import pipixiaArtwork from "../assets/platforms/pipixia.png";
import pipigxArtwork from "../assets/platforms/pipigx.ico";
import zuiyouArtwork from "../assets/platforms/zuiyou.png";
import { supportedPlatforms } from "../utils/platforms";

defineProps({
  locale: String,
});

defineEmits(["platform-click"]);

const isPaused = ref(false);
const failedArtwork = ref({});

const handleTouchStart = () => {
  isPaused.value = true;
};

const handleTouchEnd = () => {
  isPaused.value = false;
};

const content = {
  "zh-CN": {
    badge: "支持的平台",
    title: "",
  },
  en: {
    badge: "Supported Platforms",
    title: "",
  },
};

const getContent = (locale) => content[locale] || content["zh-CN"];

// The shared list controls capabilities; the showcase retains each brand's artwork.
const platformArtwork = {
  douyin: { img: douyinArtwork, desc: "解析抖音无水印视频", color: "#fe2c55" },
  kuaishou: {
    img: kuaishouArtwork,
    desc: "解析快手无水印视频",
    color: "#ff4906",
  },
  bilibili: { img: bilibiliArtwork, desc: "解析B站视频", color: "#00a1d6" },
  xiaohongshu: { img: xhsArtwork, desc: "解析小红书视频", color: "#fe2c55" },
  toutiao: { img: toutiaoArtwork, desc: "解析今日头条视频", color: "#e6162d" },
  weibo: { img: weiboArtwork, desc: "解析微博短视频", color: "#e6162d" },
  pipixia: { img: pipixiaArtwork, desc: "解析皮皮虾视频", color: "#ffc700" },
  pipigx: { img: pipigxArtwork, desc: "解析搞笑短视频", color: "#25d68a" },
  zuiyou: { img: zuiyouArtwork, desc: "解析最右短视频", color: "#8854d0" },
};
const allPlatforms = computed(() =>
  supportedPlatforms.value.map((platform) => ({
    ...platform,
    key: platform.id,
    url: platform.site_url,
    english: platform.id,
    desc: `解析${platform.name}媒体`,
    color: "#8854d0",
    ...platformArtwork[platform.icon],
  })),
);
const getDesc = (locale, idx) =>
  locale === "en"
    ? `Parse ${allPlatforms.value[idx].english} media`
    : allPlatforms.value[idx].desc;
const duplicatedPlatforms = computed(() => [
  ...allPlatforms.value,
  ...allPlatforms.value,
]);
</script>

<template>
  <section
    id="Supported_Platforms"
    class="mb-20 overflow-hidden platform-section scroll-mt-16 sm:scroll-mt-18"
  >
    <div class="flex items-center justify-center py-4 platform-header">
      <h3
        class="theme-colors inline-flex items-center gap-2 px-4 py-1.5 rounded-full bg-gradient-to-r from-indigo-500/10 to-purple-500/10 dark:from-indigo-400/10 dark:to-purple-400/10 border border-indigo-200 dark:border-indigo-800/50 text-indigo-600 dark:text-indigo-400 text-sm font-medium"
      >
        <AppIcon class="fas fa-table-cells-large text-xs"></AppIcon>
        {{ getContent(locale).badge }}
      </h3>
    </div>

    <div class="relative">
      <div
        class="marquee-container"
        @touchstart="handleTouchStart"
        @touchend="handleTouchEnd"
        @touchcancel="handleTouchEnd"
      >
        <div class="marquee-content" :class="{ 'marquee-paused': isPaused }">
          <a
            v-for="(platform, idx) in duplicatedPlatforms"
            :key="`platform-${idx}`"
            :href="platform.url"
            target="_blank"
            rel="noopener noreferrer"
            class="platform-card"
            :aria-hidden="idx >= allPlatforms.length ? true : undefined"
            :tabindex="idx >= allPlatforms.length ? -1 : undefined"
            :aria-label="`${platform.name}: ${getDesc(locale, idx % allPlatforms.length)}`"
          >
            <div class="platform-icon-container">
              <div class="platform-icon-wrapper">
                <img
                  v-if="platform.img && !failedArtwork[platform.key]"
                  :src="platform.img"
                  :alt="platform.name"
                  class="platform-icon-img"
                  loading="lazy"
                  decoding="async"
                  @error="failedArtwork[platform.key] = true"
                />
                <span
                  v-else
                  class="font-bold text-xl"
                  :style="{ color: platform.color }"
                  aria-hidden="true"
                  >{{ platform.name.slice(0, 1) }}</span
                >
              </div>
              <div
                class="platform-glow"
                :style="{ background: platform.color }"
              ></div>
            </div>

            <h4 class="platform-name">{{ platform.name }}</h4>
            <p class="platform-desc">
              {{ getDesc(locale, idx % allPlatforms.length) }}
            </p>

            <div class="platform-hover-indicator">
              <AppIcon class="fas fa-external-link-alt text-xs"></AppIcon>
            </div>
          </a>
        </div>
      </div>
    </div>

    <div
      class="theme-colors flex justify-center mt-8 gap-6 text-xs text-slate-400 dark:text-slate-500"
    >
      <span class="flex items-center gap-1">
        <AppIcon class="fas fa-mouse-pointer text-indigo-400"></AppIcon>
        {{ locale === "zh-CN" ? "悬停暂停" : "Hover to pause" }}
      </span>
      <span class="hidden sm:flex items-center gap-1">
        <AppIcon class="fas fa-arrows-alt-h text-purple-400"></AppIcon>
        {{ locale === "zh-CN" ? "自动滚动" : "Auto scroll" }}
      </span>
    </div>
  </section>
</template>

<style scoped>
.platform-section {
  position: relative;
}

.platform-header {
  animation: platformHeaderFade 0.6s ease forwards;
}

@keyframes platformHeaderFade {
  from {
    opacity: 0;
    transform: translateY(20px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

.marquee-container {
  overflow: hidden;
  padding: 1rem 0;
  position: relative;
  /* Fade the cards themselves so the actual page background always shows through. */
  --edge-mask: linear-gradient(to right, rgba(0, 0, 0, 0.05), #000 120px, #000 calc(100% - 120px), rgba(0, 0, 0, 0.05));
  -webkit-mask-image: var(--edge-mask);
  mask-image: var(--edge-mask);
}

.marquee-content {
  display: flex;
  gap: 1.25rem;
  animation: marquee 50s linear infinite;
  -webkit-animation: marquee 50s linear infinite;
  width: max-content;
  will-change: transform;
  backface-visibility: hidden;
}

.marquee-content:hover,
.marquee-content.marquee-paused {
  animation-play-state: paused;
  -webkit-animation-play-state: paused;
}

@keyframes marquee {
  0% {
    transform: translateX(0);
    -webkit-transform: translateX(0);
  }
  100% {
    transform: translateX(-50%);
    -webkit-transform: translateX(-50%);
  }
}

@-webkit-keyframes marquee {
  0% {
    -webkit-transform: translateX(0);
  }
  100% {
    -webkit-transform: translateX(-50%);
  }
}

.platform-card {
  position: relative;
  width: 160px;
  padding: 1.5rem 1rem;
  background: white;
  border-radius: 1.25rem;
  border: 1px solid #e2e8f0;
  text-decoration: none;
  text-align: center;
  transition: var(--theme-color-transition), transform 0.35s cubic-bezier(0.4, 0, 0.2, 1);
  overflow: hidden;
}

.dark .platform-card {
  background: #1e293b;
  border-color: #334155;
}

.platform-card::before {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(135deg, rgba(99, 102, 241, 0.05) 0%, rgba(139, 92, 246, 0.05) 100%);
  opacity: 0;
  transition: opacity 0.3s ease;
}

.platform-card:hover {
  transform: translateY(-8px) scale(1.02);
  border-color: rgba(99, 102, 241, 0.4);
  box-shadow:
    0 20px 40px rgba(99, 102, 241, 0.15),
    0 0 0 1px rgba(99, 102, 241, 0.1);
}

.platform-card:hover::before {
  opacity: 1;
}

.platform-icon-container {
  position: relative;
  display: inline-block;
  margin-bottom: 0.75rem;
}

.platform-icon-wrapper {
  background: white;
  width: 56px;
  height: 56px;
  margin: 0 auto;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 0.875rem;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  transition: all 0.35s cubic-bezier(0.4, 0, 0.2, 1);
  position: relative;
  z-index: 1;
}

.platform-card:hover .platform-icon-wrapper {
  transform: scale(1.1);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
}

.platform-icon-img {
  width: 32px;
  height: 32px;
  object-fit: contain;
  border-radius: 0.25rem;
}

.platform-glow {
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  width: 40px;
  height: 40px;
  border-radius: 50%;
  opacity: 0;
  filter: blur(20px);
  transition: all 0.35s ease;
  z-index: 0;
}

.platform-card:hover .platform-glow {
  opacity: 0.3;
  width: 80px;
  height: 80px;
}

.platform-name {
  font-weight: 700;
  font-size: 0.9375rem;
  color: #1e293b;
  margin-bottom: 0.25rem;
  transition: color var(--theme-duration) var(--theme-ease);
}

.dark .platform-name {
  color: #f1f5f9;
}

.platform-card:hover .platform-name {
  color: #6366f1;
}

.platform-desc {
  font-size: 0.75rem;
  color: #64748b;
  line-height: 1.4;
  transition: color var(--theme-duration) var(--theme-ease);
}

.dark .platform-desc {
  color: #94a3b8;
}

.platform-hover-indicator {
  position: absolute;
  top: 0.75rem;
  right: 0.75rem;
  width: 24px;
  height: 24px;
  border-radius: 50%;
  background: rgba(99, 102, 241, 0.1);
  display: flex;
  align-items: center;
  justify-content: center;
  opacity: 0;
  transform: scale(0.8);
  transition: all 0.3s ease;
  color: #6366f1;
}

.platform-card:hover .platform-hover-indicator {
  opacity: 1;
  transform: scale(1);
}

@media (prefers-reduced-motion: reduce) {
  .marquee-content {
    animation: none;
    flex-wrap: wrap;
    justify-content: center;
  }

  .platform-card {
    transition: none;
  }

  .platform-card[aria-hidden="true"] {
    display: none;
  }
}

@media (max-width: 640px) {
  .platform-card {
    width: 140px;
    padding: 1.25rem 0.75rem;
  }

  .platform-icon-wrapper {
    width: 48px;
    height: 48px;
  }

  .platform-icon-img {
    width: 28px;
    height: 28px;
  }
}
</style>
