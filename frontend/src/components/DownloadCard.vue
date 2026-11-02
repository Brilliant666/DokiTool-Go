<script setup>
import AppIcon from './AppIcon.vue'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = defineProps({ downloads: { type: Array, default: () => [] } })
const emit = defineEmits(['cancel', 'retry', 'refresh', 'query', 'handoff', 'clear-completed'])
const formatBytes = (bytes) => {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  const index = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)))
  return parseFloat((bytes / Math.pow(1024, index)).toFixed(2)) + ' ' + units[index]
}
const formatSpeed = (speed) => formatBytes(speed) + '/s'
const formatElapsed = (elapsed) => {
  const rawSeconds = Math.max(0, elapsed || 0)
  if (rawSeconds < 10) return `${rawSeconds.toFixed(1)} 秒`
  const seconds = Math.floor(rawSeconds)
  return seconds < 60 ? `${seconds} 秒` : `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`
}
const getStatusColor = (status) => ({ downloading: 'text-blue-500', completed: 'text-green-500', failed: 'text-red-500', cancelled: 'text-gray-500', 'handed-off': 'text-blue-500' }[status] || 'text-slate-500')
const getStatusIcon = (status) => ({ downloading: 'fa-spinner fa-spin', completed: 'fa-check-circle', failed: 'fa-exclamation-circle', cancelled: 'fa-times-circle', 'handed-off': 'fa-external-link-alt' }[status] || 'fa-hourglass-half')
const observationText = (item) => item.recoveryExpired ? '结果未确认' : item.observation === 'unknown' ? '暂时无法获取进度' : item.observation === 'stalled' ? '传输暂无进展，可等待或取消' : ''
const isActive = (item) => !item.recoveryExpired && (['queued', 'preparing', 'downloading'].includes(item.status) || item.observation === 'unknown')
const isSettled = (item) => (item.transport === 'browser_streaming' && item.observation === 'unknown' && item.ownerGone === true && item.recoveryExpired === true)
  || (!item.observation && ['completed', 'failed', 'cancelled', 'interrupted', 'handed-off'].includes(item.status))
const orderedDownloads = computed(() => [...props.downloads.filter(isActive), ...props.downloads.filter(item => !isActive(item))])
const activeCount = computed(() => props.downloads.filter(isActive).length)
const settledCount = computed(() => props.downloads.filter(isSettled).length)
const outcomeSummary = computed(() => [['completed', '完成'], ['failed', '失败'], ['cancelled', '取消'], ['interrupted', '连接中断'], ['handed-off', '浏览器处理']]
  .map(([status, label]) => [props.downloads.filter(item => isSettled(item) && item.status === status).length, label])
  .filter(([count]) => count > 0).map(([count, label]) => `${label} ${count}`).join(' · '))
const summary = computed(() => activeCount.value
  ? `${activeCount.value} 个进行中${settledCount.value ? ` · 已结束 ${settledCount.value}` : ''}`
  : [outcomeSummary.value, props.downloads.some(item => item.recoveryExpired) ? `未确认 ${props.downloads.filter(item => item.recoveryExpired).length}` : ''].filter(Boolean).join(' · ') || '下载记录')
const statusText = (item) => observationText(item) || (item.status === 'completed' ? '传输完成' : item.statusText)
  || ({ queued: '排队中', preparing: '准备下载', downloading: '下载中…', failed: '下载失败', cancelled: '已取消', interrupted: '下载连接已中断', 'handed-off': '已交给浏览器' }[item.status] || item.status)

// The panel owns presentation only. Folding never changes a transfer or its history.
const preferenceKey = 'dokitool.download-panel'
const expanded = ref(activeCount.value > 0)
const manuallyCollapsed = ref(false)
const hovered = ref(false)
const focused = ref(false)
let collapseTimer
const stopCollapseTimer = () => clearTimeout(collapseTimer)
const togglePanel = () => {
  expanded.value = !expanded.value
  manuallyCollapsed.value = !expanded.value
  try { window.localStorage.setItem(preferenceKey, expanded.value ? 'expanded' : 'collapsed') } catch { /* Storage is optional. */ }
}
onMounted(() => {
  try {
    const saved = window.localStorage.getItem(preferenceKey)
    manuallyCollapsed.value = saved === 'collapsed'
    if (saved) expanded.value = saved === 'expanded'
  } catch { /* Private browsing must not disable the panel. */ }
})
watch(() => props.downloads.filter(isActive).map(item => item.id), (ids, previous) => {
  if (!manuallyCollapsed.value && ids.some(id => !previous.includes(id))) expanded.value = true
})
watch(() => props.downloads.length, (count) => {
  // Removing the last focused/hovered row need not dispatch leave events.
  if (!count) {
    hovered.value = false
    focused.value = false
    expanded.value = false
  }
})
watch([expanded, activeCount, hovered, focused], () => {
  stopCollapseTimer()
  if (expanded.value && !activeCount.value && !hovered.value && !focused.value) {
    collapseTimer = setTimeout(() => { expanded.value = false }, 5000)
  }
})
onBeforeUnmount(stopCollapseTimer)
</script>

<template>
  <div v-if="downloads.length" data-download-panel class="download-panel fixed bottom-4 right-4 z-50 w-80 max-w-[calc(100vw-2rem)]"
    @mouseenter="hovered = true" @mouseleave="hovered = false"
    @focusin="focused = true" @focusout="focused = $event.currentTarget.contains($event.relatedTarget)">
    <section id="download-panel-list" :hidden="!expanded" class="download-panel-list theme-colors bg-white dark:bg-slate-800 border border-slate-100 dark:border-slate-700 rounded-xl shadow-2xl mb-2" aria-label="下载任务">
      <div
        v-for="item in orderedDownloads"
        :key="item.id"
        :data-download-id="item.id"
        :data-worker-build="item.workerBuild"
        :data-worker-instance="item.workerInstance"
        :data-task-id="item.deliveryId"
        :data-recovery-expired="item.recoveryExpired || undefined"
        :data-download-state="item.observation || item.status"
        :data-download-transport="item.transport"
        class="download-panel-item theme-colors border-b border-slate-100 dark:border-slate-700 last:border-b-0"
      >
        <div :class="isActive(item) ? 'p-4' : 'px-4 py-3'">
          <div class="flex items-start justify-between gap-2">
            <div class="flex-1 min-w-0 mr-3">
              <div class="theme-colors font-medium text-slate-800 dark:text-slate-200 truncate text-sm" :title="item.filename">
                {{ item.filename || '下载文件' }}
              </div>
              <div class="flex items-center gap-2 mt-1">
                <AppIcon :class="['fas', item.observation ? 'fa-hourglass-half' : getStatusIcon(item.status), getStatusColor(item.status)]"></AppIcon>
                <span class="text-xs" :class="getStatusColor(item.status)">
                  {{ statusText(item) }}
                </span>
                <span v-if="item.progressKind === 'bytes' && item.status === 'downloading' && !item.observation" class="text-xs text-slate-400">
                  {{ formatSpeed(item.speed) }}
                </span>
              </div>
            </div>

            <div class="flex items-center gap-1.5">
              <button
                v-if="!item.observation && ['failed', 'cancelled', 'handed-off', 'interrupted'].includes(item.status) && (!item.deliveryId || item.url)"
                @click="emit('retry', item.id)"
                class="p-1.5 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-700 text-slate-500 hover:text-blue-500 transition-colors"
                title="重试"
              >
                <AppIcon class="fas fa-redo text-sm"></AppIcon>
              </button>

              <button
                v-if="isActive(item)"
                @click="emit('cancel', item.id)"
                class="p-1.5 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-700 text-slate-500 hover:text-red-500 transition-colors"
                title="取消"
              >
                <AppIcon class="fas fa-times text-sm"></AppIcon>
              </button>

              <button
                v-if="isSettled(item)"
                @click="emit('clear-completed', item.id)"
                class="p-1.5 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-700 text-slate-500 hover:text-slate-700 dark:hover:text-slate-300 transition-colors"
                title="清除"
              >
                <AppIcon class="fas fa-times text-sm"></AppIcon>
              </button>
            </div>
          </div>

          <div v-if="item.recoveryExpired" class="mt-2 text-xs text-slate-500 dark:text-slate-400">
            无法确认下载结果，请查看浏览器下载列表。
            <button type="button" @click="emit('query', item.id)" class="block mt-2 text-indigo-600 dark:text-indigo-400 hover:underline">重新查询状态</button>
          </div>

          <div v-if="item.status === 'downloading' && !item.recoveryExpired" class="mt-3 mb-2">
            <div class="flex justify-between text-xs text-slate-500 mb-1">
              <span v-if="item.progressKind === 'bytes'">{{ formatBytes(item.loaded || 0) }}<template v-if="item.total"> / {{ formatBytes(item.total) }}</template><template v-else> / 大小未知</template></span>
              <span v-else>{{ item.progressKind === 'archive' ? '打包进度' : '分片进度' }}</span>
              <span v-if="item.progressKind !== 'bytes' || item.total">{{ item.percent || 0 }}%</span>
            </div>
            <div class="h-2 bg-slate-100 dark:bg-slate-700 rounded-full overflow-hidden">
              <div
                class="h-full transition-[width] duration-300 rounded-full bg-blue-500"
                :style="{ width: (item.percent || 0) + '%' }"
              ></div>
            </div>
          </div>

          <details :open="isActive(item)" class="download-details">
            <summary :hidden="isActive(item)" class="mt-1 text-xs text-slate-500 dark:text-slate-400 cursor-pointer">查看详情</summary>
            <div v-if="item.status === 'completed'" class="text-xs text-slate-500 mt-2">传输完成，请在浏览器确认保存。</div>
            <div v-if="item.elapsed !== undefined && item.status !== 'handed-off'" class="text-xs text-slate-500 mt-2"
              title="总用时包含准备与排队；传输均速统计交付的数据，不代表文件已保存到磁盘，也不是宽带测速">
              总用时 {{ formatElapsed(item.elapsed) }}
              <template v-if="item.progressKind === 'bytes' && item.transferred"> · {{ item.transferElapsed === undefined ? '平均' : '传输均速' }} {{ formatSpeed(item.averageSpeed) }}</template>
            </div>
            <div v-if="item.status === 'handed-off'" class="text-xs text-slate-500 mt-2">
              请在浏览器下载列表确认进度和结果，本站无法追踪。
            </div>
            <button v-if="item.nativeAvailable && !item.observation && ['failed', 'cancelled', 'interrupted'].includes(item.status)"
              @click="emit('handoff', item.id)"
              class="mt-2 text-xs text-indigo-600 dark:text-indigo-400 hover:underline"
              title="打开源文件，由浏览器处理；本站不再显示该次下载速度">
              打开源链接
            </button>
            <button v-if="item.refreshAvailable && !item.observation && ['failed', 'interrupted'].includes(item.status)"
              type="button" @click="emit('refresh', item.id)"
              class="block mt-2 text-xs text-indigo-600 dark:text-indigo-400 hover:underline"
              title="重新解析一次并核对原版本与来源；不会改用新的默认版本">
              刷新链接并重试
            </button>
            <div v-if="['failed', 'interrupted', 'cancelled'].includes(item.status) && item.error" class="text-xs mt-1"
              :class="item.status === 'cancelled' ? 'text-slate-500 dark:text-slate-400' : 'text-red-500'">
              {{ item.error }}
            </div>
          </details>
        </div>
      </div>
    </section>

    <div
      class="theme-colors bg-white dark:bg-slate-800 rounded-xl shadow-2xl border border-slate-100 dark:border-slate-700 px-3 py-2 flex items-center justify-between gap-2"
    >
      <button type="button" @click="togglePanel" :aria-expanded="expanded" aria-controls="download-panel-list"
        :aria-label="expanded ? '收起下载任务' : '展开下载任务'"
        class="theme-colors min-w-0 flex-1 flex items-center gap-2 py-1 text-sm text-slate-600 dark:text-slate-300 text-left" :title="summary">
        <AppIcon class="fas fa-cloud-download-alt text-indigo-500" />
        <span class="truncate flex-1" aria-live="polite">{{ summary }}</span>
        <AppIcon :class="['fas text-xs', expanded ? 'fa-chevron-down' : 'fa-chevron-up']" />
      </button>

      <button
        v-if="settledCount"
        @click="emit('clear-completed', 'all')"
        class="text-xs shrink-0 text-slate-500 hover:text-slate-700 dark:hover:text-slate-300"
      >
        清除已结束
      </button>
    </div>
  </div>
</template>

<style scoped>
.download-panel-list {
  max-height: min(60vh, 480px);
  max-height: min(60dvh, 480px);
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-gutter: stable;
}
.download-panel-list[hidden] { display: none; }
.download-panel button:focus-visible,
.download-panel summary:focus-visible {
  outline: 2px solid #6366f1;
  outline-offset: 2px;
}
@media (max-width: 639px) {
  .download-panel-list { max-height: 50vh; max-height: 50dvh; }
}
@supports (bottom: env(safe-area-inset-bottom)) {
  .download-panel { bottom: max(1rem, env(safe-area-inset-bottom)); }
}
</style>
