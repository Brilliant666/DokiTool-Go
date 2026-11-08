import { defineStore } from "pinia";
import { ref, shallowRef } from "vue";
import type {
  DownloadInput,
  DownloadTask,
  UnifiedMedia,
  MediaItem,
  ToastMessage,
  ToastType,
} from "../types/media";

export const useVideoStore = defineStore("video", () => {
  const resultVersion = ref(0);
  const resultData = shallowRef<UnifiedMedia | null>(null),
    toasts = ref<ToastMessage[]>([]),
    showProgress = shallowRef(false),
    progressText = shallowRef(""),
    progressStatus = shallowRef(""),
    progressPercent = shallowRef(0),
    downloads = ref<DownloadTask[]>([]),
    activeDownloads = shallowRef(new Map<number, DownloadTask>());
  const setResult = (data: UnifiedMedia) => {
    resultData.value = data;
    resolvingItems.value = new Set();
    resultVersion.value++;
  };
  const clearResult = () => {
    resultData.value = null;
    resolvingItems.value = new Set();
    resultVersion.value++;
  };
  const addToast = (
    message: string,
    type: ToastType = "success",
    duration = 3000,
  ) => {
    if (!message) return null;
    const id = `${Date.now()}-${Math.random()}`,
      safeDuration =
        Number.isFinite(duration) && duration > 0 ? duration : 3000;
    toasts.value = toasts.value.filter(
      (item) => !(item.message === message && item.type === type),
    );
    toasts.value.push({ id, message, type });
    window.setTimeout(() => {
      toasts.value = toasts.value.filter((item) => item.id !== id);
    }, safeDuration);
    return id;
  };
  const removeToast = (id: string) => {
    toasts.value = toasts.value.filter((item) => item.id !== id);
  };
  const setProgress = ({
    text,
    status,
    percent = 0,
  }: {
    text: string;
    status: string;
    percent?: number;
  }) => {
    showProgress.value = true;
    progressText.value = text;
    progressStatus.value = status;
    progressPercent.value = percent;
  };
  const hideProgress = () => {
    showProgress.value = false;
    progressText.value = "";
    progressStatus.value = "";
    progressPercent.value = 0;
  };
  const addDownload = (input: DownloadInput) => {
    const id = Date.now() + Math.random();
    downloads.value.push({
      id,
      status: "preparing",
      percent: 0,
      loaded: 0,
      total: 0,
      speed: 0,
      elapsed: 0,
      averageSpeed: 0,
      transferred: 0,
      error: null,
      statusText: "准备中...",
      show: true,
      abortController: null,
      ...input,
    } as DownloadTask);
    return id;
  };
  const updateDownload = (id: number, updates: Partial<DownloadTask>) => {
    const index = downloads.value.findIndex((item) => item.id === id);
    if (index !== -1)
      downloads.value[index] = { ...downloads.value[index], ...updates };
  };
  const removeDownload = (id: number) => {
    const index = downloads.value.findIndex((item) => item.id === id);
    if (index !== -1) {
      downloads.value[index].abortController?.abort();
      downloads.value.splice(index, 1);
    }
  };
  const cancelDownload = (id: number) => {
    const download = downloads.value.find((item) => item.id === id);
    if (download) {
      download.abortController?.abort();
      updateDownload(id, {
        status: "cancelled",
        statusText: "已取消",
        speed: 0,
        observation: undefined,
        abortController: null,
      });
    }
  };
  const clearCompletedDownloads = () => {
    downloads.value = downloads.value.filter(
      (item) =>
        ![
          "completed",
          "cancelled",
          "failed",
          "handed-off",
          "manual",
          "interrupted",
        ].includes(item.status),
    );
  };
  const resolvingItems = ref(new Set<string>());
  const replaceItem = (item: MediaItem, version = resultVersion.value) => {
    const data = resultData.value;
    if (
      !data ||
      version !== resultVersion.value ||
      !data.items.some((existing) => existing.id === item.id)
    )
      return;
    resultData.value = {
      ...data,
      items: data.items.map((existing) =>
        existing.id === item.id ? item : existing,
      ),
    };
  };
  const setResolving = (id: string, pending: boolean) => {
    const next = new Set(resolvingItems.value);
    if (pending) next.add(id);
    else next.delete(id);
    resolvingItems.value = next;
  };
  return {
    resultVersion,
    resultData,
    toasts,
    showProgress,
    progressText,
    progressStatus,
    progressPercent,
    downloads,
    activeDownloads,
    setResult,
    clearResult,
    addToast,
    removeToast,
    setProgress,
    hideProgress,
    addDownload,
    updateDownload,
    removeDownload,
    cancelDownload,
    clearCompletedDownloads,
    replaceItem,
    resolvingItems,
    setResolving,
  };
});
