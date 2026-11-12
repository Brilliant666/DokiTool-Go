<script setup>
import AppIcon from './AppIcon.vue'
import { computed, ref } from 'vue'
import { groupedPlatformTabs } from '../utils/platforms'

const props = defineProps({
  inputUrl: String,
  isReady: { type: Boolean, default: true },
  isLoading: Boolean,
  isError: Boolean,
  parseError: String,
  locale: String,
  currentPlatform: String
})

const emit = defineEmits(['update:inputUrl', 'parse', 'select-platform'])

const content = {
  'zh-CN': {
    badge: 'DokiTool · 多平台解析',
    title1: '轻松解析',
    title2: '任何',
    title3: '短视频',
    desc: '支持抖音、快手、B 站、小红书等平台，一键提取无水印视频与图集，简单免费。',
    feature1: '无水印',
    feature2: '高速解析',
    feature3: '完全免费',
    inputLabel: '视频链接',
    placeholder: '粘贴分享链接或含链接的文本…',
    button: '开始解析',
    loading: '正在解析…',
    error: '解析失败',
    errorTitle: '解析没有完成',
    errorHint: '请检查作品是否公开可访问，或切换平台后再试一次。',
    features: [
      { icon: 'fa-check-circle', color: 'text-emerald-500', label: '无水印' },
      { icon: 'fa-bolt', color: 'text-amber-500', label: '高速解析' },
      { icon: 'fa-gift', color: 'text-indigo-500', label: '完全免费' }
    ]
  },
  'en': {
    badge: 'DokiTool · Multi-Platform',
    title1: 'Parse ',
    title2: 'Any',
    title3: ' Short Video',
    desc: 'Support Douyin, Kuaishou, Bilibili, Xiaohongshu and more. Extract videos & albums without watermark, simple and free.',
    feature1: 'No Watermark',
    feature2: 'Fast Parse',
    feature3: 'Free',
    inputLabel: 'Video URL',
    placeholder: 'Paste a share link or text containing a link…',
    button: 'Start Parsing',
    loading: 'Parsing…',
    error: 'Failed',
    errorTitle: 'Parsing did not finish',
    errorHint: 'Check that the post is public, or switch platform and try again.',
    features: [
      { icon: 'fa-check-circle', color: 'text-emerald-500', label: 'No Watermark' },
      { icon: 'fa-bolt', color: 'text-amber-500', label: 'Fast Parse' },
      { icon: 'fa-gift', color: 'text-indigo-500', label: 'Free' }
    ]
  }
}

const getContent = (locale) => content[locale] || content['zh-CN']

const hasParseError = computed(() => Boolean(props.parseError))

const showMorePlatforms = ref(false)
const groupedTabs = computed(() => groupedPlatformTabs(props.locale || 'zh-CN'))
const primaryPlatformTabs = computed(() => [
  groupedTabs.value.allTab,
  ...groupedTabs.value.popularTabs
])
const morePlatformTabs = computed(() => groupedTabs.value.moreTabs)
const morePlatformCount = computed(() => morePlatformTabs.value.length)
const isMorePlatformSelected = computed(() =>
  morePlatformTabs.value.some((tab) => tab.key === props.currentPlatform)
)

const selectPlatform = (key) => {
  emit('select-platform', key)
}
</script>

<template>
  <section class="hero-section relative text-center overflow-hidden">
    <div class="hero-glow absolute inset-0 -z-10" aria-hidden="true"></div>

    <div class="max-w-4xl mx-auto px-4 sm:px-6 lg:px-8 relative">
      <div class="theme-colors inline-flex items-center gap-2 px-4 py-1.5 rounded-full bg-indigo-50 dark:bg-indigo-950/50 border border-indigo-200 dark:border-indigo-800/50 text-indigo-600 dark:text-indigo-400 text-xs sm:text-sm font-medium mb-6 sm:mb-8">
        <AppIcon class="fas fa-wand-magic-sparkles"></AppIcon>
        <span>{{ getContent(locale).badge }}</span>
      </div>

      <h2 class="text-3xl sm:text-4xl md:text-5xl lg:text-6xl font-bold mb-6 sm:mb-8 tracking-tight">
        <span class="ux-gradient-text">{{ getContent(locale).title1 }}</span>
        <span class="theme-colors text-slate-700 dark:text-slate-200">{{ getContent(locale).title2 }}</span>
        <span class="ux-gradient-text">{{ getContent(locale).title3 }}</span>
      </h2>

      <p class="theme-colors text-base sm:text-lg text-slate-600 dark:text-slate-400 max-w-2xl mx-auto mb-8 sm:mb-10 leading-relaxed">
        {{ getContent(locale).desc }}
      </p>

      <div class="flex flex-wrap justify-center gap-4 sm:gap-6 mb-10 sm:mb-12">
        <div
          v-for="(feature, index) in getContent(locale).features"
          :key="index"
          class="theme-colors flex items-center gap-2 text-sm sm:text-base text-slate-600 dark:text-slate-300"
        >
          <AppIcon :class="['fas', feature.icon, feature.color]"></AppIcon>
          <span>{{ feature.label }}</span>
        </div>
      </div>

      <div class="max-w-xl sm:max-w-2xl mx-auto">
        <div class="flex flex-wrap justify-center gap-2 mb-4">
          <button
            v-for="tab in primaryPlatformTabs"
            :key="tab.key"
            type="button"
            :data-platform-tab="tab.key"
            @click="selectPlatform(tab.key)"
            :disabled="!isReady || isLoading"
            :aria-pressed="currentPlatform === tab.key"
            :class="[
              'theme-colors px-3 py-1.5 rounded-lg font-medium text-xs flex items-center gap-1.5',
              currentPlatform === tab.key
                ? 'bg-indigo-600 bg-gradient-to-r from-indigo-500 to-purple-500 text-white shadow-lg shadow-indigo-500/25'
                : 'bg-indigo-50 dark:bg-slate-800 text-indigo-600 dark:text-slate-300 hover:bg-indigo-100 dark:hover:bg-slate-700 border border-indigo-200 dark:border-slate-700'
            ]"
          >
            <AppIcon :class="['fas', tab.icon]"></AppIcon>
            <span>{{ tab.label }}</span>
          </button>
          <button
            v-for="tab in morePlatformTabs"
            v-show="showMorePlatforms"
            :key="tab.key"
            type="button"
            :data-platform-tab="tab.key"
            data-more-platform-tab
            @click="selectPlatform(tab.key)"
            :disabled="!isReady || isLoading"
            :aria-pressed="currentPlatform === tab.key"
            :class="[
              'theme-colors px-3 py-1.5 rounded-lg font-medium text-xs flex items-center gap-1.5',
              currentPlatform === tab.key
                ? 'bg-indigo-600 bg-gradient-to-r from-indigo-500 to-purple-500 text-white shadow-lg shadow-indigo-500/25'
                : 'bg-indigo-50 dark:bg-slate-800 text-indigo-600 dark:text-slate-300 hover:bg-indigo-100 dark:hover:bg-slate-700 border border-indigo-200 dark:border-slate-700'
            ]"
          >
            <AppIcon :class="['fas', tab.icon]"></AppIcon>
            <span>{{ tab.label }}</span>
          </button>
          <button
            v-if="morePlatformCount > 0"
            type="button"
            data-more-platforms-toggle
            :disabled="!isReady || isLoading"
            :aria-expanded="showMorePlatforms"
            @click="showMorePlatforms = !showMorePlatforms"
            :class="[
              'theme-colors px-3 py-1.5 rounded-lg font-medium text-xs flex items-center gap-1.5',
              !showMorePlatforms && isMorePlatformSelected
                ? 'bg-indigo-600 bg-gradient-to-r from-indigo-500 to-purple-500 text-white shadow-lg shadow-indigo-500/25'
                : 'bg-slate-100 dark:bg-slate-800/90 text-slate-600 dark:text-slate-300 hover:bg-slate-200/80 dark:hover:bg-slate-700 border border-slate-200 dark:border-slate-700'
            ]"
          >
            <span>{{
              showMorePlatforms
                ? locale === 'en' ? 'Less' : '收起'
                : locale === 'en' ? `More (${morePlatformCount})` : `更多平台 (${morePlatformCount})`
            }}</span>
            <AppIcon :class="['fas text-[10px]', showMorePlatforms ? 'fa-chevron-up' : 'fa-chevron-down']"></AppIcon>
          </button>
        </div>

        <div class="relative group">
          <div class="absolute -inset-0.5 bg-gradient-to-r from-indigo-500 via-purple-500 to-pink-500 rounded-2xl blur opacity-20 group-hover:opacity-40 transition-opacity duration-300 -z-10"></div>
          <div class="theme-colors relative flex items-center bg-white dark:bg-slate-800 rounded-2xl border border-slate-200/80 dark:border-slate-700/80 shadow-xl shadow-slate-200/50 dark:shadow-slate-950/50 overflow-hidden focus-within:border-indigo-500 dark:focus-within:border-indigo-400 focus-within:ring-4 focus-within:ring-indigo-500/10">
            <input
              data-initial-url
              data-allow-mismatch="attribute"
              :value="inputUrl"
              @input="emit('update:inputUrl', $event.target.value)"
              @keyup.enter="isReady && emit('parse')"
              type="text"
              :placeholder="getContent(locale).placeholder"
              :aria-label="getContent(locale).inputLabel"
              class="theme-colors flex-1 px-4 sm:px-6 py-4 sm:py-5 bg-transparent text-slate-800 dark:text-slate-100 text-sm sm:text-base placeholder:text-slate-400 dark:placeholder:text-slate-500 outline-none"
              :disabled="isLoading"
            />
            <button
              @click="emit('parse')"
              :disabled="!isReady || isLoading"
              :aria-label="!isReady ? (locale === 'en' ? 'Preparing…' : '正在准备…') : getContent(locale).button"
              class="m-1 sm:m-1.5 px-5 sm:px-7 py-2.5 sm:py-3 rounded-xl font-semibold flex items-center gap-2 whitespace-nowrap enterprise-btn"
              :class="isLoading
                ? 'bg-slate-400 dark:bg-slate-500 cursor-not-allowed opacity-60'
                : isError
                  ? 'bg-rose-600 hover:bg-rose-700 dark:bg-rose-700 dark:hover:bg-rose-800 text-white shadow-lg shadow-rose-500/20 dark:shadow-rose-900/30 border border-rose-500/30 dark:border-rose-600/30 error-state-btn'
                  : 'bg-indigo-600 hover:bg-indigo-700 dark:bg-indigo-600 dark:hover:bg-indigo-700 text-white shadow-lg shadow-indigo-500/25 dark:shadow-indigo-900/40 border border-indigo-500/40 dark:border-indigo-500/30 parse-state-btn'"
            >
              <AppIcon :class="['fas text-xs sm:text-sm transition-transform duration-300', isLoading ? 'fa-spinner fa-spin' : isError ? 'fa-exclamation-circle' : 'fa-bolt']"></AppIcon>
              <span class="hidden sm:inline text-sm">{{ !isReady ? (locale === 'en' ? 'Preparing…' : '正在准备…') : isLoading ? getContent(locale).loading : isError ? getContent(locale).error : getContent(locale).button }}</span>
              <span class="sm:hidden text-sm">{{ !isReady || isLoading ? '...' : isError ? '!' : '>' }}</span>
            </button>
          </div>
        </div>

        <Transition name="parse-error">
          <div
            v-if="hasParseError"
            class="theme-colors parse-error-panel"
            role="alert"
            aria-live="assertive"
          >
            <div class="theme-colors parse-error-icon">
              <AppIcon class="fas fa-circle-exclamation"></AppIcon>
            </div>
            <div class="parse-error-copy">
              <p class="parse-error-title">{{ getContent(locale).errorTitle }}</p>
              <p class="parse-error-message">{{ parseError }}</p>
              <p class="theme-colors parse-error-hint">{{ getContent(locale).errorHint }}</p>
            </div>
          </div>
        </Transition>

        <p class="theme-colors text-xs text-slate-400 dark:text-slate-500 mt-3 px-2">
          <AppIcon class="fas fa-info-circle mr-1"></AppIcon>
          {{ locale === 'zh-CN' ? '支持抖音、快手、B站、小红书等平台' : 'Support Douyin, Kuaishou, Bilibili, Xiaohongshu & more' }}
        </p>
      </div>
    </div>
  </section>
</template>

<style scoped>
.hero-section {
  isolation: isolate;
  padding: 1rem 0;
}
.hero-glow {
  pointer-events: none;
  background: radial-gradient(ellipse closest-side at 50% 45%, rgba(99, 102, 241, .10), rgba(139, 92, 246, .04) 65%, transparent);
}
@media (min-width: 640px) {
  .hero-section { padding-top: 1.5rem; }
}

.ux-gradient-text {
  background: linear-gradient(135deg, #6366f1, #8b5cf6, #ec4899);
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
}

.enterprise-btn {
  position: relative;
  overflow: hidden;
  font-family: inherit;
  letter-spacing: 0.02em;
  transition: var(--theme-color-transition),
    transform var(--button-motion-duration, 300ms) var(--theme-ease),
    opacity 300ms var(--theme-ease);
}

.enterprise-btn::before {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(
    135deg,
    rgba(255, 255, 255, 0.15) 0%,
    rgba(255, 255, 255, 0) 50%
  );
  opacity: 0;
  transition: opacity 0.3s ease;
}

.enterprise-btn:hover::before {
  opacity: 1;
}

.parse-state-btn {
  background: linear-gradient(
    180deg,
    #6366f1 0%,
    #4f46e5 50%,
    #4338ca 100%
  );
}

.parse-state-btn:hover {
  background: linear-gradient(
    180deg,
    #818cf8 0%,
    #6366f1 50%,
    #4f46e5 100%
  );
  transform: translateY(-2px);
  box-shadow:
    0 8px 20px rgba(99, 102, 241, 0.35),
    0 0 0 1px rgba(255, 255, 255, 0.1) inset;
}

.parse-state-btn:active {
  transform: translateY(0);
  background: linear-gradient(
    180deg,
    #4f46e5 0%,
    #4338ca 50%,
    #3730a3 100%
  );
  --button-motion-duration: 100ms;
}

.parse-state-btn .fa-bolt {
  animation: subtlePulse 2s ease-in-out infinite;
}

@keyframes subtlePulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50% { opacity: 0.8; transform: scale(0.95); }
}

.error-state-btn {
  background: linear-gradient(
    180deg,
    #f43f5e 0%,
    #e11d48 50%,
    #be123c 100%
  );
}

.error-state-btn:hover {
  background: linear-gradient(
    180deg,
    #fb7185 0%,
    #f43f5e 50%,
    #e11d48 100%
  );
  transform: translateY(-2px);
  box-shadow:
    0 8px 20px rgba(244, 63, 94, 0.35),
    0 0 0 1px rgba(255, 255, 255, 0.1) inset;
}

.error-state-btn:active {
  transform: translateY(0);
  background: linear-gradient(
    180deg,
    #e11d48 0%,
    #be123c 50%,
    #9f1239 100%
  );
  --button-motion-duration: 100ms;
}

.parse-error-panel {
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
  margin-top: 0.875rem;
  padding: 0.875rem 1rem;
  border: 1px solid rgba(244, 63, 94, 0.24);
  border-radius: 1rem;
  background: rgba(255, 241, 242, 0.92);
  color: #881337;
  text-align: left;
  box-shadow: 0 14px 32px rgba(225, 29, 72, 0.1);
}

:global(.dark) .parse-error-panel {
  border-color: rgba(251, 113, 133, 0.26);
  background: rgba(76, 5, 25, 0.56);
  color: #fecdd3;
  box-shadow: 0 14px 32px rgba(0, 0, 0, 0.22);
}

.parse-error-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2rem;
  height: 2rem;
  flex: 0 0 2rem;
  border-radius: 999px;
  background: rgba(244, 63, 94, 0.12);
  color: #e11d48;
}

:global(.dark) .parse-error-icon {
  background: rgba(251, 113, 133, 0.16);
  color: #fb7185;
}

.parse-error-copy {
  min-width: 0;
}

.parse-error-title {
  margin: 0;
  font-size: 0.875rem;
  font-weight: 700;
  line-height: 1.35;
}

.parse-error-message {
  margin: 0.25rem 0 0;
  font-size: 0.8125rem;
  line-height: 1.5;
  word-break: break-word;
}

.parse-error-hint {
  margin: 0.375rem 0 0;
  font-size: 0.75rem;
  line-height: 1.45;
  color: rgba(136, 19, 55, 0.72);
}

:global(.dark) .parse-error-hint {
  color: rgba(254, 205, 211, 0.72);
}

.parse-error-enter-active,
.parse-error-leave-active {
  transition: var(--theme-color-transition), opacity 0.22s ease, transform 0.22s ease;
}

.parse-error-enter-from,
.parse-error-leave-to {
  opacity: 0;
  transform: translateY(-6px);
}

@media (max-width: 480px) {
  .enterprise-btn {
    padding-left: 0.875rem;
    padding-right: 0.875rem;
  }

  .parse-error-panel {
    border-radius: 0.875rem;
    padding: 0.75rem;
  }
}
</style>
