import test, { before, after, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'vite';

let vite, segments;
const originalFetch = globalThis.fetch;
before(async () => {
  vite = await createServer({ server: { middlewareMode: true, ws: false, hmr: false, watch: null }, appType: 'custom' });
  segments = await vite.ssrLoadModule('/src/utils/segmentDownload.ts');
});
after(async () => { globalThis.fetch = originalFetch; await vite?.close(); });
beforeEach(() => { globalThis.fetch = async () => { throw new Error('unexpected request'); }; });

test('segment metadata probes never exceed four concurrent requests', async () => {
  let active = 0, peak = 0, count = 0;
  globalThis.fetch = async (_url, options) => {
    if (options.method === 'HEAD') {
      count++; active++; peak = Math.max(peak, active);
      await new Promise(resolve => setTimeout(resolve, 5));
      active--;
      return new Response(null, { headers: { 'Content-Length': '1' } });
    }
    return new Response(new Uint8Array([1]), { headers: { 'Content-Length': '1' } });
  };
  await segments.mergeSegments(Array.from({ length: 16 }, (_, i) => `https://cdn.test/segment-${i}`));
  assert.equal(count, 16);
  assert.ok(peak <= 4, `unbounded HEAD fan-out: ${peak}`);
});

test('segment output is an ordered Blob, without allocating a complete contiguous Uint8Array', async () => {
  globalThis.fetch = async url => new Response(new Uint8Array(url.endsWith('/a') ? [1, 2] : [3, 4]), {
    headers: { 'Content-Length': '2', 'Content-Type': 'video/mp4' },
  });
  const result = await segments.mergeSegments(['https://cdn.test/a', 'https://cdn.test/b']);
  assert.ok(result instanceof Blob, 'mergeSegments still returns a full contiguous array');
  assert.deepEqual([...new Uint8Array(await result.arrayBuffer())], [1, 2, 3, 4]);
});

const buffered = () => vite.ssrLoadModule('/src/downloads/buffered.ts');

test('combined cancellation works without AbortSignal.any and disposes every listener', async t => {
  const { linkAbortSignals } = await buffered();
  const descriptor = Object.getOwnPropertyDescriptor(AbortSignal, 'any');
  Object.defineProperty(AbortSignal, 'any', { configurable: true, value: undefined });
  t.after(() => Object.defineProperty(AbortSignal, 'any', descriptor));
  const a = new AbortController(), b = new AbortController();
  let removes = 0;
  for (const signal of [a.signal, b.signal]) {
    const original = signal.removeEventListener.bind(signal);
    t.mock.method(signal, 'removeEventListener', (...args) => { removes++; return original(...args); });
  }
  const linked = linkAbortSignals([a.signal, b.signal]);
  const reason = new Error('user cancelled');
  a.abort(reason); b.abort(new Error('too late'));
  assert.equal(linked.signal.reason, reason);
  linked.dispose(); linked.dispose();
  assert.equal(removes, 2);
  const already = linkAbortSignals([a.signal, b.signal]);
  assert.equal(already.signal.reason, reason);
});

test('normal completion detaches combined-signal listeners', async () => {
  const { linkAbortSignals } = await buffered();
  const original = new AbortController();
  const linked = linkAbortSignals([original.signal]);
  linked.dispose(); original.abort();
  assert.equal(linked.signal.aborted, false);
});

test('ZIP and segment header waits abort the request instead of hanging', async () => {
  const { fetchMediaBlob } = await buffered();
  let signal;
  globalThis.fetch = (_url, options) => { signal = options.signal; return new Promise(() => {}); };
  await assert.rejects(fetchMediaBlob('https://cdn.test/object', { timeoutMs: 25 }), /没有返回数据/);
  assert.equal(signal.aborted, true);
});

test('stalled body cancels its reader, aborts fetch and produces no Blob', async () => {
  const { fetchMediaBlob } = await buffered();
  let cancelled = 0, signal;
  globalThis.fetch = async (_url, options) => {
    signal = options.signal;
    return new Response(new ReadableStream({
      start(controller) { controller.enqueue(new Uint8Array([1, 2])); },
      cancel() { cancelled++; },
    }), { headers: { 'Content-Length': '4' } });
  };
  await assert.rejects(fetchMediaBlob('https://cdn.test/object', { timeoutMs: 25 }), /没有返回数据/);
  assert.equal(signal.aborted, true); assert.equal(cancelled, 1);
});

test('progress renews read waits rather than imposing a whole-file deadline', async () => {
  const { fetchMediaBlob } = await buffered();
  let timer;
  globalThis.fetch = async () => new Response(new ReadableStream({
    start(controller) {
      let n = 0;
      timer = setInterval(() => {
        controller.enqueue(new Uint8Array([++n]));
        if (n === 12) { clearInterval(timer); controller.close(); }
      }, 10);
    },
    cancel() { clearInterval(timer); },
  }), { headers: { 'Content-Length': '12' } });
  try {
    const result = await fetchMediaBlob('https://cdn.test/object', { timeoutMs: 80 });
    assert.equal(result.size, 12);
  } finally { clearInterval(timer); }
});

test('caller cancellation preserves its reason and releases the body reader', async () => {
  const { fetchMediaBlob } = await buffered();
  const parent = new AbortController(); let entered, cancelled = false;
  const reading = new Promise(resolve => { entered = resolve; });
  globalThis.fetch = async () => new Response(new ReadableStream({
    pull() { entered(); }, cancel() { cancelled = true; },
  }));
  const pending = fetchMediaBlob('https://cdn.test/object', { signal: parent.signal });
  const reason = new Error('stop this job');
  const rejected = assert.rejects(pending, error => error === reason);
  await reading; parent.abort(reason); await rejected;
  assert.equal(cancelled, true);
});

test('incomplete, excessive and error bodies cannot be packed into a ZIP member', async () => {
  const { fetchMediaBlob } = await buffered();
  const cases = [
    [new Uint8Array([1, 2]), { 'Content-Length': '3' }, {}, /不完整/],
    [new Uint8Array([1, 2, 3]), { 'Content-Length': '2' }, {}, /长度不符/],
    [new Uint8Array([1, 2]), {}, { expectedSize: 3 }, /不完整/],
    ['<html>bad</html>', { 'Content-Type': 'text/html' }, {}, /错误页面/],
    [new Uint8Array([1, 2, 3]), {}, { maxBytes: 2 }, /长度不符/],
  ];
  for (const [body, headers, options, pattern] of cases) {
    globalThis.fetch = async () => new Response(body, { headers });
    await assert.rejects(fetchMediaBlob('https://cdn.test/object', options), pattern);
  }
  globalThis.fetch = async () => new Response(new Uint8Array([1]), { status: 206 });
  await assert.rejects(fetchMediaBlob('https://cdn.test/object'), /完整文件/);
});

test('metadata failure leaves the total unknown without blocking the actual GET', async () => {
  const progress = [];
  globalThis.fetch = async (_url, options) => options.method === 'HEAD'
    ? new Response(null, { status: 403 })
    : new Response(new Uint8Array([1, 2]));
  const output = await segments.mergeSegments(['https://cdn.test/a', 'https://cdn.test/b'], { onProgress: row => progress.push(row) });
  assert.equal(output.size, 4);
  assert.ok(progress.slice(0, -1).every(row => row.total === 0));
  assert.equal(progress.at(-1).loaded, 4); assert.equal(progress.at(-1).total, 4);
});

test('cancelling metadata probes never starts segment GETs or queued probes', async () => {
  const parent = new AbortController(); let count = 0, entered;
  const started = new Promise(resolve => { entered = resolve; });
  globalThis.fetch = (_url, options) => {
    assert.equal(options.method, 'HEAD'); count++; if (count === 4) entered();
    return new Promise((_resolve, reject) => options.signal.addEventListener('abort', () => reject(options.signal.reason), { once: true }));
  };
  const reason = new Error('cancel probes');
  const pending = segments.mergeSegments(Array.from({ length: 20 }, (_, i) => `https://cdn.test/${i}`), { signal: parent.signal });
  const rejected = assert.rejects(pending, error => error === reason);
  await started; parent.abort(reason); await rejected;
  assert.equal(count, 4);
});

test('one buffered job runs; queued cancellation and double release cannot create extra slots', async () => {
  const { createBufferedQueue } = await buffered();
  const acquire = createBufferedQueue();
  const first = new AbortController(), second = new AbortController(), third = new AbortController();
  const release1 = await acquire(first.signal);
  const cancelled = acquire(second.signal);
  const rejected = assert.rejects(cancelled, { name: 'AbortError' });
  let running3 = false;
  const waiting3 = acquire(third.signal).then(release => { running3 = true; return release; });
  second.abort(); await rejected; assert.equal(running3, false);
  release1(); const release3 = await waiting3; release1();
  let running4 = false;
  const waiting4 = acquire(new AbortController().signal).then(release => { running4 = true; return release; });
  await Promise.resolve(); assert.equal(running4, false);
  release3(); (await waiting4)();
});

test('loading the button controller no longer starts the unused AntiReplay interval', async t => {
  const originalWindow = globalThis.window, realInterval = globalThis.setInterval;
  let antiReplayIntervals = 0;
  t.mock.method(globalThis, 'setInterval', (fn, delay, ...args) => {
    if (delay === 60000) { antiReplayIntervals++; return 1; }
    return realInterval(fn, delay, ...args);
  });
  globalThis.window = {};
  try { await vite.ssrLoadModule('/src/composables/useButtonControl.js'); }
  finally { globalThis.window = originalWindow; }
  assert.equal(antiReplayIntervals, 0);
});

test('platform duplicates are hidden from keyboard/assistive navigation and reduced-motion stays disabled on mobile', async () => {
  const { readFile } = await import('node:fs/promises');
  const source = await readFile(new URL('../src/components/PlatformGrid.vue', import.meta.url), 'utf8');
  assert.match(source, /:aria-hidden="idx >= allPlatforms.length \? true : undefined"/);
  assert.match(source, /:tabindex="idx >= allPlatforms.length \? -1 : undefined"/);
  assert.match(source, /x:\s*\{\s*img:\s*xArtwork/);
  assert.match(source, /youtube:\s*\{\s*img:\s*youtubeArtwork/);
  assert.match(source, /instagram:\s*\{[\s\S]*?img:\s*instagramArtwork/);
  assert.match(source, /facebook:\s*\{[\s\S]*?img:\s*facebookArtwork/);
  assert.match(source, /tumblr:\s*\{[\s\S]*?img:\s*tumblrArtwork/);
  assert.match(source, /dailymotion:\s*\{[\s\S]*?img:\s*dailymotionArtwork/);
  const platformsSource = await readFile(new URL('../src/utils/platforms.ts', import.meta.url), 'utf8');
  assert.match(platformsSource, /facebook:\s*"fa-facebook"/);
  assert.match(platformsSource, /tumblr:\s*"fa-tumblr"/);
  assert.match(platformsSource, /dailymotion:\s*"fa-dailymotion"/);
  const reduced = source.slice(source.indexOf('@media (prefers-reduced-motion: reduce)'));
  assert.match(reduced, /animation: none/);
  assert.doesNotMatch(reduced, /animation:\s*marquee/);
});


test('buffered helper preserves existing request policies; only HLS manifest explicitly omits referrer', async () => {
  const { fetchMediaBlob, probeMediaLength } = await buffered();
  const policies = [];
  globalThis.fetch = async (_url, options) => {
    policies.push(options.referrerPolicy);
    if (options.method === 'HEAD') return new Response(null, { headers: { 'Content-Length': '2' } });
    return new Response(new Uint8Array([1, 2]));
  };
  await fetchMediaBlob('https://cdn.test/file');
  await probeMediaLength('https://cdn.test/file');
  assert.deepEqual(policies, [undefined, undefined]);
  globalThis.fetch = async (_url, options) => {
    assert.equal(options.referrerPolicy, 'no-referrer');
    return new Response('#EXTM3U\n#EXTINF:1,\nclip.ts\n#EXT-X-ENDLIST');
  };
  assert.deepEqual(await segments.resolveHlsSegments('https://cdn.test/list'), [{ url: 'https://cdn.test/clip.ts' }]);
});
