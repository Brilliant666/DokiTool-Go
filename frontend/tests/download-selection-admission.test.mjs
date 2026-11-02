import test, { before, after, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'vite';
import { createPinia } from 'pinia';

// Exercise the real store, selection and download coordinator. Only transport
// and Worker acknowledgements are replaced; no browser/online claim is made.
let vite, useMediaDownload, useVideoSelection, useVideoStore;
let requests, toasts, runTransport, forget;
const originalWindow = globalThis.window;
before(async () => {
  globalThis.window = { setTimeout, clearTimeout };
  vite = await createServer({
    server: { middlewareMode: true, ws: false, hmr: false, watch: null }, appType: 'custom',
    plugins: [{ name: 'selection-admission-boundaries', enforce: 'pre',
      transform(code, id) {
        if (/[\\/]composables[\\/]useMediaDownload\.ts$/.test(id))
          return code.replaceAll('"../downloads/manager"', '"virtual:admission-manager"')
            .replaceAll('"../downloads/streaming"', '"virtual:admission-streaming"');
      },
      resolveId(id) { if (id.startsWith('virtual:admission-')) return '\0' + id; },
      load(id) {
        if (id === '\0virtual:admission-manager') return 'export const downloadManager = {run: (...args) => globalThis.__admissionRun(...args)}';
        if (id === '\0virtual:admission-streaming') return 'export const forgetStreaming = (...args) => globalThis.__admissionForget(...args); export const hasStreamingTask = () => false;';
      },
    }],
  });
  ({ useMediaDownload } = await vite.ssrLoadModule('/src/composables/useMediaDownload.ts'));
  ({ useVideoSelection } = await vite.ssrLoadModule('/src/composables/useVideoSelection.ts'));
  ({ useVideoStore } = await vite.ssrLoadModule('/src/stores/video.ts'));
});
after(async () => {
  globalThis.window = originalWindow;
  delete globalThis.__admissionRun; delete globalThis.__admissionForget;
  await vite?.close();
});
function terminal(request, observe, status) {
  const result = { id: request.id, filename: request.filename, url: request.url,
    transport: 'blob', status, loaded: status === 'completed' ? 4 : 0,
    total: 4, speed: 0, elapsed: 0, averageSpeed: 0,
    ...(status === 'failed' ? { error: 'HTTP 403' } : {}) };
  observe(result); return result;
}
beforeEach(() => {
  requests = []; toasts = []; forget = async () => {};
  runTransport = async (request, signal, observe) => {
    signal.throwIfAborted();
    return terminal(request, observe, request.url.endsWith('/old.mp4') ? 'failed' : 'completed');
  };
  globalThis.__admissionRun = (...args) => { requests.push(args[0]); return runTransport(...args); };
  globalThis.__admissionForget = (...args) => forget(...args);
});
function item() {
  const source = { id: 's-a', url: 'https://media.test/old.mp4' };
  return { id: 'video-a', type: 'video', resolve_url: '/api/resolve?item_id=video-a',
    variants: [{ id: 'v-a', metadata: { extension: 'mp4' }, delivery: { kind: 'file', sources: [source] } }],
    default: { variant_id: 'v-a', source_id: source.id, url: source.url } };
}
function fresh(old) {
  const value = structuredClone(old);
  value.variants[0].delivery.sources[0].url = 'https://media.test/fresh.mp4';
  value.default.url = 'https://media.test/fresh.mp4';
  return value;
}
function gate() { let release; const promise = new Promise(resolve => { release = resolve; }); return { promise, release }; }
async function until(predicate) {
  for (let i = 0; i < 1000; i++) { if (predicate()) return; await new Promise(resolve => setTimeout(resolve, 2)); }
  throw new Error('Expected coordinator state was not reached');
}
function harness(value = item(), resolver = async old => fresh(old)) {
  const store = useVideoStore(createPinia());
  store.setResult({ platform: 'toutiao', items: [value], default_item_id: value.id });
  const selection = useVideoSelection({ videoStore: store }); selection.syncResult();
  let resolves = 0;
  const instance = useMediaDownload({ videoStore: store, selection,
    resolveItem: (...args) => { resolves++; return resolver(...args); },
    showToast: (...args) => toasts.push(args) });
  const row = id => store.downloads.find(task => task.id === id);
  return { store, selection, instance, row, get resolves() { return resolves; } };
}
async function failed(h) {
  h.instance.downloadMainVideo();
  await until(() => h.store.downloads.length && h.store.downloads[0].status === 'failed');
  return h.store.downloads[0].id;
}
for (const mode of ['default', 'explicit', 'quality-only']) {
  test(`same selected file is not admitted from main while ${mode} refresh resolves`, async () => {
    const pending = gate(); const h = harness(item(), () => pending.promise);
    if (mode !== 'default') h.selection.choose({ variant_id: 'v-a', ...(mode === 'explicit' ? { source_id: 's-a' } : {}) });
    const id = await failed(h);
    const refresh = h.instance.handleRefreshDownload(id);
    await until(() => h.resolves === 1);
    h.instance.downloadMainVideo();
    try {
      assert.equal(h.store.downloads.length, 1, 'duplicate entry must not add a stale task');
      assert.equal(requests.length, 1, 'no second old media request during resolve');
      assert.ok(toasts.some(([message]) => message.includes('正在处理')));
    } finally { pending.release(fresh(item())); await refresh; }
    await until(() => h.row(id).status === 'completed');
    assert.deepEqual(requests.map(request => request.url), ['https://media.test/old.mp4', 'https://media.test/fresh.mp4']);
  });
}
test('admission remains reserved while waiting for the old Worker acknowledgement', async () => {
  const h = harness(); const id = await failed(h); const ack = gate(); let waiting = false;
  h.store.updateDownload(id, { transport: 'browser_streaming' });
  forget = async () => { waiting = true; await ack.promise; };
  const refresh = h.instance.handleRefreshDownload(id);
  await until(() => waiting);
  h.instance.downloadMainVideo();
  try { assert.equal(h.store.downloads.length, 1); assert.equal(h.resolves, 0); assert.equal(requests.length, 1); }
  finally { ack.release(); await refresh; }
  await until(() => h.row(id).status === 'completed');
});
test('same selection is blocked throughout the new transfer, not only resolve', async () => {
  const h = harness(); const id = await failed(h); const transfer = gate();
  runTransport = async (request, signal, observe) => {
    terminal(request, observe, 'downloading'); await transfer.promise;
    signal.throwIfAborted(); return terminal(request, observe, 'completed');
  };
  await h.instance.handleRefreshDownload(id);
  await until(() => requests.length === 2);
  h.instance.downloadMainVideo();
  try { assert.equal(h.store.downloads.length, 1); assert.equal(requests.length, 2); }
  finally { transfer.release(); }
  await until(() => h.row(id).status === 'completed');
});
for (const change of ['source', 'variant', 'item', 'result-version']) {
  test(`a genuinely different ${change} is not blocked by the selection reservation`, async () => {
    const old = item(); const pending = gate(); const h = harness(old, () => pending.promise);
    const id = await failed(h); const refresh = h.instance.handleRefreshDownload(id);
    await until(() => h.resolves === 1);
    const other = structuredClone(old);
    if (change === 'source') {
      other.variants[0].delivery.sources.push({ id: 's-b', url: 'https://media.test/other.mp4' });
      h.store.replaceItem(other); h.selection.choose({ variant_id: 'v-a', source_id: 's-b' });
    } else if (change === 'variant') {
      other.variants.push({ id: 'v-b', delivery: { kind: 'file', sources: [{ id: 's-a', url: 'https://media.test/other.mp4' }] } });
      h.store.replaceItem(other); h.selection.choose({ variant_id: 'v-b', source_id: 's-a' });
    } else {
      if (change === 'item') { other.id = 'video-b'; other.resolve_url = '/api/resolve?item_id=video-b'; }
      other.variants[0].delivery.sources[0].url = 'https://media.test/other.mp4'; other.default.url = 'https://media.test/other.mp4';
      if (change === 'item') { h.store.resultData = { ...h.store.resultData, items: [other], default_item_id: other.id }; }
      else h.store.setResult({ ...h.store.resultData, items: [other] });
      h.selection.syncResult();
    }
    h.instance.downloadMainVideo();
    try { await until(() => requests.length === 2); assert.equal(h.store.downloads.length, 2); assert.equal(requests[1].url, 'https://media.test/other.mp4'); }
    finally { pending.release(fresh(old)); await refresh; }
    await until(() => h.row(id).status === 'completed');
  });
}
test('cancelled preparation releases the selection and does not revive from a late resolve', async () => {
  const pending = gate(); const h = harness(item(), () => pending.promise); const id = await failed(h);
  const refresh = h.instance.handleRefreshDownload(id); await until(() => h.resolves === 1);
  await h.instance.handleCancelDownload(id); await refresh;
  assert.equal(h.row(id).status, 'cancelled'); h.instance.downloadMainVideo();
  await until(() => h.store.downloads.length === 2 && h.store.downloads[1].status === 'failed');
  pending.release(fresh(item())); await new Promise(resolve => setTimeout(resolve, 10));
  assert.equal(h.row(id).status, 'cancelled'); assert.equal(requests.length, 2);
});
test('failed refresh releases admission and clearing history cannot leave a stale reservation', async () => {
  const h = harness(item(), async () => { throw new Error('resolve unavailable'); }); const id = await failed(h);
  await h.instance.handleRefreshDownload(id); assert.equal(h.row(id).status, 'failed');
  await h.instance.handleClearCompleted(id); assert.equal(h.store.downloads.length, 0);
  h.instance.downloadMainVideo(); await until(() => h.store.downloads.length === 1 && h.store.downloads[0].status === 'failed');
  assert.equal(requests.length, 2);
});
test('a missing source ID is not given an inferred admission identity', async () => {
  const value = item(); delete value.variants[0].delivery.sources[0].id; delete value.default.source_id;
  const h = harness(value); const first = await failed(h);
  assert.equal(h.row(first).refreshAvailable, false);
  h.instance.downloadMainVideo(); await until(() => h.store.downloads.length === 2 && h.store.downloads[1].status === 'failed');
});

test('batch and per-item entries share the same in-flight selection guard', async () => {
  const pending = gate(); const h = harness(item(), () => pending.promise); const id = await failed(h);
  const refresh = h.instance.handleRefreshDownload(id); await until(() => h.resolves === 1);
  try {
    await h.instance.downloadAllVideos();
    h.instance.downloadVideoByIndex(0);
    assert.equal(h.store.downloads.length, 1); assert.equal(requests.length, 1);
  } finally { pending.release(fresh(item())); await refresh; }
  await until(() => h.row(id).status === 'completed');
});
test('retrying another failed card cannot compete with the same refreshing selection', async () => {
  const pending = gate(); const h = harness(item(), () => pending.promise);
  const first = await failed(h);
  h.instance.downloadMainVideo(); await until(() => h.store.downloads.length === 2 && h.store.downloads[1].status === 'failed');
  const second = h.store.downloads[1].id;
  const refresh = h.instance.handleRefreshDownload(second); await until(() => h.resolves === 1);
  try {
    await h.instance.handleRefreshDownload(first);
    assert.equal(h.resolves, 1); assert.equal(h.row(first).status, 'failed'); assert.equal(requests.length, 2);
  } finally { pending.release(fresh(item())); await refresh; }
  await until(() => h.row(second).status === 'completed');
});
test('unknown Worker observation retains admission until a terminal result is known', async () => {
  const h = harness(); const id = await failed(h);
  runTransport = async () => { const error = new Error('No START acknowledgement'); error.name = 'StreamingStateUnknownError'; throw error; };
  await h.instance.handleRefreshDownload(id); await until(() => h.row(id).observation === 'unknown');
  h.instance.downloadMainVideo();
  await new Promise(resolve => setTimeout(resolve, 10));
  assert.equal(h.store.downloads.length, 1); assert.equal(requests.length, 2);
});
test('a completed transfer does not prevent an intentional later download', async () => {
  const h = harness(fresh(item()));
  h.instance.downloadMainVideo(); await until(() => h.store.downloads[0]?.status === 'completed');
  h.instance.downloadMainVideo(); await until(() => h.store.downloads.length === 2 && h.store.downloads[1].status === 'completed');
  assert.equal(requests.length, 2);
});
test('a refused old-worker acknowledgement does not leave a permanent selection lock', async () => {
  const h = harness(); const id = await failed(h);
  h.store.updateDownload(id, { transport: 'browser_streaming' });
  forget = async () => { throw new Error('refused'); };
  await h.instance.handleRefreshDownload(id);
  assert.equal(h.resolves, 0); assert.equal(h.row(id).status, 'failed');
  h.instance.downloadMainVideo(); await until(() => h.store.downloads.length === 2 && h.store.downloads[1].status === 'failed');
  assert.equal(requests.length, 2);
});
