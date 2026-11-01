/* Download-only service worker. It never caches or intercepts the application/API. */
import { createTaskStore, HISTORY_MS } from "./task-store.js";
import { attachmentFormat, cleanFilename } from "./media-format.js";
import { decodedLength } from "./decoded-length.js";
const PROTOCOL = 1;
const BUILD = "downloads-20261101.1";
const INSTANCE = crypto.randomUUID();
const store = createTaskStore();
const MAX_ACTIVE = 2;
const IDLE_MS = 45_000;
// Bound attachment-response preparation below browser extended-event limits.
// Legacy clients also queue inside that response; admission-aware clients do not.
const RESPONSE_MS = 120_000;
const STALLED_MS = 15_000;
const SPEED_WINDOW_MS = 2000;
const CLAIM_MS = 60_000;
const RETAIN_MS = HISTORY_MS;
const tasks = new Map();
const history = new Map();
const queue = [];
let active = 0;
let historyTimer;
const terminal = status => ["completed", "failed", "cancelled"].includes(status);
const abortError = () => new DOMException("下载已取消", "AbortError");
const validKey = value => typeof value === "string" && /^[a-zA-Z0-9_-]{16,100}$/.test(value);
const sourceUrl = value => {
  const url = new URL(value, self.location.origin);
  if (!/^https?:$/.test(url.protocol) || url.username || url.password ||
      (url.origin === self.location.origin && url.pathname.startsWith("/downloads/")) || /\.m3u8(?:$|[?#])/i.test(url.href)) {
    throw new Error("资源地址不支持完整文件下载");
  }
  return url.href;
};

function snapshot(task) {
  if (task.result) return task.result;
  const now = Date.now(), end = task.finishedAt ?? now;
  const elapsed = Math.max(0, (end - task.startedAt) / 1000);
  const transferElapsed = task.transferStartedAt === undefined ? 0 : Math.max(0, (end - task.transferStartedAt) / 1000);
  // Lack of downstream demand may be a normal browser pause. Report it without
  // cancelling the transfer, starting another route, or releasing its slot.
  const observation = task.state.status === "downloading" && !task.pendingUpstream &&
    now - Math.max(task.lastPullAt ?? task.transferStartedAt, task.lastReceivedAt ?? 0) >= STALLED_MS ? "stalled" : undefined;
  const result = { ...task.state, workerBuild: BUILD, workerInstance: INSTANCE, ...(task.finishedAt === undefined ? {} : { finishedAt: task.finishedAt }), revision: ++task.revision, observation, elapsed, transferElapsed,
    speed: now - (task.lastReceivedAt || 0) > 3000 ? 0 : task.state.speed,
    averageSpeed: task.state.loaded / Math.max(0.001, transferElapsed) };
  if (terminal(task.state.status)) task.result = result;
  return result;
}

function persist(task) {
  const { url: _url, ...summary } = snapshot(task);
  const record = { id: task.state.id, token: task.token, createdAt: task.startedAt,
    updatedAt: Date.now(), finishedAt: task.finishedAt, snapshot: summary };
  // Preserve lifecycle ordering even when one transaction is delayed. No chunks
  // are written here; the small queue contains phase transitions and the result.
  task.persistence = (task.persistence || Promise.resolve()).then(() => store.put(record));
  return task.persistence;
}

function publish(task, force = false) {
  const now = Date.now();
  if (!force && now - task.publishedAt < 300) return;
  if (task.state.status === "downloading") {
    const samples = task.speedSamples;
    samples.push({ at: now, loaded: task.state.loaded });
    while (samples.length > 2 && samples[1].at <= now - SPEED_WINDOW_MS) samples.shift();
    task.state.speed = (task.state.loaded - samples[0].loaded) / Math.max(0.001, (now - samples[0].at) / 1000);
  } else task.state.speed = 0;
  task.publishedAt = now;
  const phase = `${task.state.status}:${task.state.delivery || ""}`;
  if (!terminal(task.state.status) && task.persistedPhase !== phase) {
    task.persistedPhase = phase;
    void persist(task);
  }
  const message = { channel: "dokitool-download", protocol: PROTOCOL, snapshot: snapshot(task) };
  for (const clientId of task.subscribers) {
    void self.clients.get(clientId).then(client => {
      if (client) client.postMessage(message);
      else task.subscribers.delete(clientId);
    }).catch(() => { /* A lookup failure does not prove a live page disappeared. */ });
  }
}

function awaitClaim(task) {
  task.claimTimer = setTimeout(() => settle(task, "failed", "浏览器未接收下载，请重试或手动打开源链接"), CLAIM_MS);
}

function drain() {
  while (active < MAX_ACTIVE && queue.length) {
    const task = queue.shift();
    if (terminal(task.state.status)) { task.rejectSlot?.(abortError()); continue; }
    active++;
    task.hasSlot = true;
    if (task.admission) {
      task.state.delivery = "ready";
      awaitClaim(task);
      publish(task, true);
    } else task.resolveSlot();
  }
}

function settle(task, status, error) {
  if (terminal(task.state.status)) return task.settling;
  clearTimeout(task.claimTimer);
  clearTimeout(task.responseTimer);
  task.state.status = status;
  task.state.error = error;
  task.finishedAt = Date.now();
  task.detachRequestAbort?.();
  task.detachRequestAbort = undefined;
  const queuedIndex = queue.indexOf(task);
  if (queuedIndex !== -1) queue.splice(queuedIndex, 1);
  if (status !== "completed") {
    // A consumer need not pull again after stopping. Error the output now rather
    // than waiting for a future pull to notice the aborted upstream request.
    task.outputController?.error(status === "cancelled" ? abortError() : new Error(error || "下载失败"));
  }
  task.outputController = undefined;
  task.controller.abort();
  const reader = task.reader;
  task.reader = undefined;
  void reader?.cancel().catch(() => {}).finally(() => { reader.releaseLock(); });
  task.rejectSlot?.(abortError());
  if (task.hasSlot) { task.hasSlot = false; active--; }
  // Close/abort the stream independently of storage. Retain the authoritative
  // result in memory until its bounded write and notification have finished.
  task.settling = persist(task).then(() => {
    publish(task, true);
    history.set(task.state.id, { token: task.token, finishedAt: task.finishedAt, result: task.result });
    tasks.delete(task.state.id);
    prune();
  });
  // A fetch event may already be inactive at EOF. This protects only the short
  // result write where the browser permits it, never the whole transfer.
  try { task.protect?.(task.settling); } catch { /* Stream/event lifetime is browser-owned. */ }
  drain();
  return task.settling;
}

function prune() {
  clearTimeout(historyTimer);
  historyTimer = undefined;
  const now = Date.now();
  for (const [id, entry] of history) if (now - entry.finishedAt >= RETAIN_MS) history.delete(id);
  while (history.size > 100) history.delete(history.keys().next().value);
  const oldest = history.values().next().value;
  if (oldest) historyTimer = setTimeout(prune, Math.max(1, oldest.finishedAt + RETAIN_MS - now));
}

async function timed(task, operation) {
  if (task.controller.signal.aborted) throw abortError();
  task.pendingUpstream = (task.pendingUpstream || 0) + 1;
  let timer;
  let onAbort;
  try {
    return await Promise.race([
      operation(),
      new Promise((_, reject) => {
        onAbort = () => reject(abortError());
        task.controller.signal.addEventListener("abort", onAbort, { once: true });
        timer = setTimeout(() => reject(new Error("来源超过 45 秒没有返回数据，请重新下载")), IDLE_MS);
      }),
    ]);
  } finally {
    task.pendingUpstream--;
    clearTimeout(timer);
    task.controller.signal.removeEventListener("abort", onAbort);
  }
}

function mediaHeaders(response, filename, formatMime, sourceSize) {
  if (response.status !== 200 || response.headers.has("content-range") || response.type === "opaque" || !response.body) throw new Error(`资源不是完整文件响应（HTTP ${response.status}）`);
  const format = attachmentFormat(filename, response.headers.get("content-type"), formatMime, { allowUnknownMedia: true });
  const total = decodedLength(response, sourceSize);
  return { ...format, total };
}

async function prepare(task) {
  let lastError;
  // Only before returning the attachment response can another route replace this one.
  for (const url of task.routes) {
    const attempt = new AbortController();
    const abortAttempt = () => attempt.abort();
    task.controller.signal.addEventListener("abort", abortAttempt, { once: true });
    let response;
    try {
      task.state.url = url;
      publish(task, true);
      response = await timed(task, () => fetch(url, {
        signal: attempt.signal, mode: "cors", credentials: new URL(url).origin === self.location.origin ? "same-origin" : "omit",
        referrerPolicy: "no-referrer", cache: "no-store",
      }));
      const metadata = mediaHeaders(response, task.state.filename, task.formatMime, task.sourceSizes?.[url]);
      task.reader = response.body.getReader();
      const first = await timed(task, () => task.reader.read());
      if (first.done || !first.value?.length) throw new Error("资源为空");
      const prefix = new TextDecoder().decode(first.value.subarray(0, 100)).trimStart();
      if (/^(?:<!doctype|<html|#EXTM3U|\{"|\[\{)/i.test(prefix)) throw new Error("资源返回了页面或播放清单，不能作为完整视频保存");
      if (metadata.total && first.value.length > metadata.total) throw new Error("下载长度与来源声明不一致");
      if (task.controller.signal.aborted) throw abortError();
      Object.assign(task.state, metadata);
      // Keep this attempt alive; cancelling the task aborts its source request as well.
      return { first: first.value, mime: metadata.mime };
    } catch (error) {
      lastError = error;
      attempt.abort();
      void task.reader?.cancel().catch(() => {});
      if (!task.reader) void response?.body?.cancel().catch(() => {});
      task.reader = undefined;
      task.controller.signal.removeEventListener("abort", abortAttempt);
      if (task.controller.signal.aborted) throw abortError();
    }
  }
  throw lastError || new Error("没有可用的下载线路");
}

async function attachment(task) {
  if (task.controller.signal.aborted) throw abortError();
  if (!task.hasSlot) {
    await new Promise((resolve, reject) => {
      task.resolveSlot = resolve; task.rejectSlot = reject;
      queue.push(task); drain();
    });
  }
  if (task.controller.signal.aborted) throw abortError();
  task.state.status = "preparing";
  publish(task, true);
  const prepared = await prepare(task);
  if (task.controller.signal.aborted) throw abortError();
  let first = prepared.first;
  task.state.status = "downloading";
  task.transferStartedAt = Date.now();
  task.speedSamples = [{ at: task.transferStartedAt, loaded: 0 }];
  publish(task, true);
  const stream = new ReadableStream({
    start(controller) { task.outputController = controller; },
    async pull(controller) {
      try {
        if (task.controller.signal.aborted) throw abortError();
        task.lastPullAt = Date.now();
        const item = first ? { value: first, done: false } : await timed(task, () => task.reader.read());
        first = undefined;
        if (item.done) {
          if (!task.state.loaded || (task.state.total && task.state.loaded !== task.state.total)) throw new Error("下载不完整，请重新下载");
          controller.close();
          task.outputController = undefined;
          settle(task, "completed");
          return;
        }
        if (task.state.total && task.state.loaded + item.value.length > task.state.total) throw new Error("下载长度与来源声明不一致");
        task.state.loaded += item.value.length;
        task.lastReceivedAt = Date.now();
        controller.enqueue(item.value);
        publish(task);
      } catch (error) {
        settle(task, task.controller.signal.aborted ? "cancelled" : "failed", error.message);
      }
    },
    cancel() { settle(task, "cancelled", "浏览器停止接收下载数据"); },
  }, { highWaterMark: 0 });
  const encoded = encodeURIComponent(task.state.filename).replace(/[!'()*]/g, c => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
  const headers = {
    "Content-Type": prepared.mime,
    "Content-Disposition": `attachment; filename="download.${task.state.filename.split(".").pop()}"; filename*=UTF-8''${encoded}`,
    "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff",
    "X-DokiTool-Worker-Build": BUILD,
  };
  if (task.state.total) headers["Content-Length"] = String(task.state.total);
  return new Response(stream, { headers });
}

async function authorizedClient(event) {
  if (!event.source?.id) return null;
  const client = await self.clients.get(event.source.id);
  return client && new URL(client.url).origin === self.location.origin ? client : null;
}

self.addEventListener("message", event => {
  const port = event.ports?.[0];
  if (!port) return;
  event.waitUntil((async () => {
    const client = await authorizedClient(event);
    if (!client || event.data?.protocol !== PROTOCOL) { port.postMessage({ error: "下载通信校验失败" }); return; }
    prune();
    const message = event.data;
    try {
      if (message.action === "ping") { port.postMessage({ protocol: PROTOCOL, workerBuild: BUILD, workerInstance: INSTANCE, capabilities: { admission: true, recovery: true } }); return; }
      if (message.action === "start") {
        const request = message.request;
        if (!validKey(request?.id) || !validKey(message.token)) throw new Error("无效下载任务");
        let task = tasks.get(request.id);
        const stored = !task && !history.has(request.id) ? await store.get(request.id) : null;
        // Another START may have admitted this ID while storage was pending.
        task = tasks.get(request.id);
        const previous = history.get(request.id) || (stored && { token: stored.token, result: stored.snapshot });
        if ((task || previous) && (task || previous).token !== message.token) throw new Error("下载任务无访问权限");
        if (previous) {
          if (!terminal(previous.result.status)) throw new Error("原执行实例已结束，不能重新启动同一任务");
          port.postMessage({ snapshot: { url: "", ...previous.result } }); return;
        }
        let created = false;
        if (!task) {
          if (tasks.size >= 100) throw new Error("下载任务过多，请清理后重试");
          const routes = [...new Set([request.url, ...(request.routes || [])].slice(0, 10).map(sourceUrl))].slice(0, 3);
          if (!routes.length) throw new Error("资源地址为空");
          const now = Date.now();
          const startedAt = Number.isFinite(request.startedAt) && request.startedAt > 0 && request.startedAt <= now ? request.startedAt : now;
          task = {
            token: message.token, routes, sourceSizes: request.sourceSizes, formatMime: request.formatMime,
            controller: new AbortController(), subscribers: new Set(),
            startedAt, publishedAt: now, revision: 0, admission: message.admission === true,
            state: { id: request.id, transport: "browser_streaming", status: "queued", filename: cleanFilename(request.filename), url: routes[0], loaded: 0, total: 0, speed: 0, elapsed: 0, averageSpeed: 0,
              ...(message.admission === true ? { delivery: "waiting" } : {}) },
          };
          tasks.set(request.id, task);
          created = true;
        }
        task.subscribers.add(client.id);
        if (created) {
          void persist(task);
          if (task.admission) { queue.push(task); drain(); }
          else awaitClaim(task);
        }
        port.postMessage({ snapshot: snapshot(task),
          ...(task.admission ? {} : { downloadUrl: `/downloads/file/${request.id}/${task.token}` }) });
        // Acknowledge setup only. Keeping this event open for the whole download
        // triggers Chromium's extended-event deadline even while bytes flow.
        return;
      }
      if (message.action === "query") {
        const result = [], unavailable = [];
        for (const key of (message.tasks || []).slice(0, 100)) {
          if (!validKey(key.id) || !validKey(key.token)) continue;
          const task = tasks.get(key.id);
          if (task && task.token === key.token) { await task.settling; task.subscribers.add(client.id); result.push(snapshot(task)); }
          else {
            const previous = history.get(key.id);
            if (previous?.token === key.token) result.push(previous.result);
            else {
              const stored = await store.get(key.id);
              if (stored?.token === key.token && terminal(stored.snapshot.status)) result.push({ url: "", ...stored.snapshot });
              else {
                const instance = stored?.token === key.token ? stored.snapshot.workerInstance : key.workerInstance;
                unavailable.push({ id: key.id, ownerGone: !!instance && instance !== INSTANCE });
              }
            }
          }
        }
        port.postMessage({ snapshots: result, unavailable, workerBuild: BUILD, workerInstance: INSTANCE }); return;
      }
      if (!validKey(message.id) || !validKey(message.token)) throw new Error("无效下载任务");
      const task = tasks.get(message.id);
      const stored = !task && !history.has(message.id) ? await store.get(message.id) : null;
      const previous = history.get(message.id) || (stored && terminal(stored.snapshot.status) ? { token: stored.token, result: { url: "", ...stored.snapshot } } : null);
      if (stored && stored.token !== message.token) throw new Error("下载任务无访问权限");
      if ((task || previous) && (task || previous).token !== message.token) throw new Error("下载任务无访问权限");
      if (message.action === "forget") {
        if (task) throw new Error("请先停止下载，再清除任务");
        history.delete(message.id); prune();
        await store.remove(message.id);
        port.postMessage({ forgotten: true }); return;
      }
      if (message.action !== "cancel" || (!task && !previous)) throw new Error("下载任务不存在或已失效");
      if (task) await settle(task, "cancelled", "页面请求取消下载");
      port.postMessage({ snapshot: task ? snapshot(task) : previous.result });
    } catch (error) { port.postMessage({ error: error.message }); }
  })());
});

self.addEventListener("fetch", event => {
  const url = new URL(event.request.url);
  if (url.origin !== self.location.origin || !url.pathname.startsWith("/downloads/file/")) return;
  const [, , , id, token] = url.pathname.split("/");
  const task = tasks.get(id);
  if (event.request.method !== "GET" || !task || task.token !== token || task.claimed || terminal(task.state.status)) {
    event.respondWith(new Response("Download expired", { status: 404, headers: { "Cache-Control": "no-store" } })); return;
  }
  if (task.admission && !task.hasSlot) {
    event.respondWith(new Response("Download not admitted", { status: 409, headers: { "Cache-Control": "no-store" } })); return;
  }
  task.claimed = true;
  task.protect = promise => event.waitUntil(promise);
  if (task.admission) task.state.delivery = "claimed";
  clearTimeout(task.claimTimer);
  task.responseTimer = setTimeout(() => settle(task, "failed", task.state.status === "queued"
    ? "等待下载空位超过 2 分钟，请在其他下载结束后重试"
    : task.admission ? "下载准备超过 2 分钟，请稍后重试" : "下载准备超过 2 分钟（含排队），请稍后重试"), RESPONSE_MS);
  const signal = event.request.signal;
  const abort = () => settle(task, "cancelled", "浏览器中止下载请求");
  task.detachRequestAbort = () => signal.removeEventListener("abort", abort);
  signal.addEventListener("abort", abort, { once: true });
  if (signal.aborted) abort();
  // Once returned, the response stream owns delivery. Do not bind the complete
  // transfer to waitUntil: long downloads must outlive the setup event.
  event.respondWith(attachment(task).catch(error => {
    settle(task, task.controller.signal.aborted ? "cancelled" : "failed", error.message);
    return new Response("Download failed", { status: 502, headers: { "Cache-Control": "no-store", "Content-Type": "text/plain; charset=utf-8" } });
  }).finally(() => { clearTimeout(task.responseTimer); }));
});
