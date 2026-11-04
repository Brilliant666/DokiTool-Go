/* Small lifecycle records only. Storage failure never changes transfer outcome. */
export const HISTORY_MS = 24 * 60 * 60_000;
const LIMIT = 100;
const BUDGET_MS = 1500;
const terminal = record => ["completed", "failed", "cancelled", "manual"].includes(record.snapshot.status);

export function createTaskStore(database = globalThis.indexedDB) {
  let connection;
  let unavailable = false;
  function open() {
    if (!database) return Promise.reject(new Error("Storage unavailable"));
    connection ??= new Promise((resolve, reject) => {
      const request = database.open("dokitool-downloads", 1);
      request.onupgradeneeded = () => request.result.createObjectStore("tasks", { keyPath: "id" });
      request.onerror = () => reject(request.error);
      request.onblocked = () => reject(new Error("Storage blocked"));
      request.onsuccess = () => {
        const db = request.result;
        db.onversionchange = () => { db.close(); connection = undefined; };
        resolve(db);
      };
    });
    return connection;
  }
  async function run(mode, action) {
    if (unavailable) return undefined;
    let timer, transaction;
    let ended = false;
    try {
      return await Promise.race([
        (async () => {
          const db = await open();
          if (ended) { db.close(); return undefined; }
          return await new Promise((resolve, reject) => {
            transaction = db.transaction("tasks", mode);
            let result;
            transaction.oncomplete = () => resolve(result);
            transaction.onerror = transaction.onabort = () => reject(transaction.error);
            action(transaction.objectStore("tasks"), value => { result = value; });
          });
        })(),
        new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("Storage timed out")), BUDGET_MS); }),
      ]);
    } catch {
      try { transaction?.abort(); } catch { /* Already committed or unavailable. */ }
      connection = undefined;
      unavailable = true;
      return undefined;
    } finally { ended = true; clearTimeout(timer); }
  }
  function prune(store) {
    const request = store.getAll();
    request.onsuccess = () => {
      const records = request.result.filter(terminal).sort((a, b) => b.finishedAt - a.finishedAt);
      records.forEach((record, index) => {
        if (index >= LIMIT || Date.now() - record.finishedAt >= HISTORY_MS) store.delete(record.id);
      });
    };
  }
  return {
    put(record) { return run("readwrite", (store, done) => { store.put(record); prune(store); done(true); }); },
    get(id) { return run("readonly", (store, done) => {
      const request = store.get(id);
      request.onsuccess = () => {
        const record = request.result;
        done(record && (!terminal(record) || Date.now() - record.finishedAt < HISTORY_MS) ? record : null);
      };
    }); },
    remove(id) { return run("readwrite", (store, done) => { store.delete(id); prune(store); done(true); }); },
  };
}
