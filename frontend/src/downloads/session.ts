import { StreamingUnavailableError, type DownloadSnapshot } from "./types";
import { connectBridge } from "./bridge";

export const PROTOCOL = 1;
const SCOPE = "/downloads/";
export interface WorkerReply {
  protocol?: number;
  workerBuild?: string;
  workerInstance?: string;
  capabilities?: { admission?: boolean; recovery?: boolean; tbvideo?: boolean };
  snapshot?: DownloadSnapshot;
  snapshots?: DownloadSnapshot[];
  unavailable?: { id: string; ownerGone: boolean }[];
  downloadUrl?: string;
}
export interface WorkerSession { worker: ServiceWorker; build: string; instance?: string; admission: boolean; tbvideo?: boolean }
/** An explicit refusal is distinct from a lost reply after START. */
export class WorkerRequestError extends Error {}
const sessions = new WeakMap<ServiceWorker, WorkerSession>();
const checkedUpdates = new Set<string>();
let connection: Promise<WorkerSession | null> | undefined;
let discovery: Promise<ServiceWorkerRegistration | undefined> | undefined;

export function rpc(worker: ServiceWorker, message: object): Promise<WorkerReply> {
  return new Promise((resolve, reject) => {
    const channel = new MessageChannel();
    const finish = () => { clearTimeout(timer); channel.port1.close(); };
    const timer = setTimeout(() => {
      finish(); reject(new Error("下载服务应答超时，状态未知；请勿重复启动，刷新后检查任务"));
    }, 8000);
    channel.port1.onmessage = event => {
      finish();
      event.data?.error ? reject(new WorkerRequestError(event.data.error)) : resolve(event.data);
    };
    try { worker.postMessage({ protocol: PROTOCOL, ...message }, [channel.port2]); }
    catch (error) { finish(); reject(error); }
  });
}

function supported(): boolean {
  return typeof navigator !== "undefined" && globalThis.isSecureContext && "serviceWorker" in navigator
    && typeof MessageChannel !== "undefined" && !!globalThis.crypto?.randomUUID;
}

function bounded<T>(request: Promise<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("下载服务查询或注册超时")), 8000);
    request.then(value => { clearTimeout(timer); resolve(value); }, error => { clearTimeout(timer); reject(error); });
  });
}

function registration(): Promise<ServiceWorkerRegistration | undefined> {
  discovery ??= bounded(navigator.serviceWorker.getRegistration(SCOPE))
    .then(value => value && new URL(value.scope).pathname === SCOPE ? value : undefined)
    .finally(() => { discovery = undefined; });
  return discovery;
}

/** Terminal-history cleanup does not create a worker, bind a task, or retain a bridge. */
export async function existingWorker(): Promise<ServiceWorker | null> {
  if (!supported()) return null;
  return (await registration())?.active || null;
}

function activated(worker: ServiceWorker): Promise<ServiceWorker> {
  if (worker.state === "activated") return Promise.resolve(worker);
  return new Promise((resolve, reject) => {
    const finish = () => { clearTimeout(timer); worker.removeEventListener("statechange", changed); };
    const changed = () => {
      if (worker.state === "activated") { finish(); resolve(worker); }
      else if (worker.state === "redundant") { finish(); reject(new Error("下载服务安装失败")); }
    };
    const timer = setTimeout(() => { finish(); reject(new Error("下载服务激活超时")); }, 8000);
    worker.addEventListener("statechange", changed); changed();
  });
}

async function connect(create: boolean): Promise<WorkerSession | null> {
  let reg = await registration();
  if (!reg && create) reg = await bounded(navigator.serviceWorker.register("/downloads/worker.js", { scope: SCOPE, type: "module", updateViaCache: "none" }));
  if (!reg) return null;
  const pending = reg.active || reg.installing || reg.waiting;
  if (!pending) throw new Error("下载服务未激活");
  await activated(pending);
  for (let attempt = 0; ; attempt++) {
    let worker = reg.active;
    try {
      const controller = await connectBridge();
      // The frame protects lifetime; RPC uses the parent realm so replies and
      // subscriptions belong to the page. Cross-realm worker wrappers differ.
      worker = reg.active;
      if (!worker || worker.state !== "activated" || controller.state !== "activated") throw new Error("下载服务正在更新，请稍后重试");
      let session = sessions.get(worker);
      if (!session) {
        const reply = await rpc(worker, { action: "ping" });
        if (reply.protocol !== PROTOCOL) throw new Error("下载服务版本不兼容，请刷新页面后重试");
        if (worker.state !== "activated" || reg.active !== worker) throw new Error("下载服务正在更新，请稍后重试");
        session = { worker, build: reply.workerBuild || "legacy", instance: reply.workerInstance, admission: reply.capabilities?.admission === true, tbvideo: reply.capabilities?.tbvideo === true };
        sessions.set(worker, session);
      }
      if (!checkedUpdates.has(reg.scope)) {
        checkedUpdates.add(reg.scope);
        // Check only once a controlled document protects existing transfers.
        try { void reg.update?.().catch(() => {}); } catch { /* An offline update does not break a download. */ }
      }
      return session;
    } catch (error) {
      // Before START there is no task owner yet. Complete a natural activation
      // race once; never switch an established task to another worker.
      if (attempt || worker?.state !== "redundant" || reg.active?.state !== "activated") throw error;
    }
  }
}

/** Concurrent starts and restored tasks share discovery and the capability handshake. */
export async function workerSession(create: boolean): Promise<WorkerSession | null> {
  if (!supported()) {
    if (create) throw new StreamingUnavailableError("当前浏览器不支持流式下载");
    return null;
  }
  if (connection) {
    const session = await connection;
    if (session || !create) return session;
    if (connection) return connection;
  }
  connection = connect(create).finally(() => { connection = undefined; });
  return connection;
}
