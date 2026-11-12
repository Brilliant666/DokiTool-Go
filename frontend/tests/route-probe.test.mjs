import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import { build, stop } from 'esbuild';

let routeProbe;

before(async () => {
  const bundled = await build({
    entryPoints: ['src/downloads/routeProbe.ts'],
    bundle: true,
    write: false,
    outdir: 'test-output',
    platform: 'node',
    format: 'esm',
  });
  const code = bundled.outputFiles.find(file => file.path.endsWith('routeProbe.js')).text;
  routeProbe = await import(`data:text/javascript;base64,${Buffer.from(code).toString('base64')}`);
});

after(() => {
  stop();
});

function rangedStreamResponse(size, delayPerChunkMs = 0, chunkSize = 64 * 1024) {
  let sent = 0;
  const body = new ReadableStream({
    async pull(controller) {
      if (sent >= size) {
        controller.close();
        return;
      }
      if (delayPerChunkMs > 0) {
        await new Promise(resolve => setTimeout(resolve, delayPerChunkMs));
      }
      const n = Math.min(chunkSize, size - sent);
      const bytes = new Uint8Array(n);
      bytes[0] = 0x00;
      bytes[1] = 0x00;
      bytes[2] = 0x00;
      bytes[3] = 0x18;
      sent += n;
      controller.enqueue(bytes);
    },
  });
  return new Response(body, {
    status: 206,
    headers: {
      'content-type': 'video/mp4',
      'content-range': `bytes 0-${size - 1}/${size * 8}`,
      'content-length': String(size),
    },
  });
}

test('route probe picks the fastest CORS-readable source among up to 3 allowed candidates and preserves backups', async () => {
  const sources = [
    { id: 's-slow', url: 'https://v13-cold.douyinvod.com/slow.mp4', browser_fetch: 'allowed' },
    { id: 's-fast', url: 'https://v26-cold.douyinvod.com/fast.mp4', browser_fetch: 'allowed' },
    { id: 's-mid', url: 'https://v3-dy-o.zjcdn.com/mid.mp4', browser_fetch: 'allowed' },
    { id: 's-fourth', url: 'https://v9-cold.douyinvod.com/fourth.mp4', browser_fetch: 'allowed' },
    { id: 's-dispatch', url: 'https://www.douyin.com/aweme/v1/play/?video_id=1' },
  ];

  const seenRequests = [];
  const fetchImpl = async (url, init) => {
    seenRequests.push({ url, init });
    if (url.includes('v26-cold')) return rangedStreamResponse(512 * 1024, 1);
    if (url.includes('v3-dy-o')) return rangedStreamResponse(512 * 1024, 8);
    if (url.includes('v13-cold')) return rangedStreamResponse(512 * 1024, 22);
    throw new Error(`unexpected probe for ${url}`);
  };

  const result = await routeProbe.probeAndOrderSources(
    sources,
    {},
    new AbortController().signal,
    { fetchImpl },
  );

  assert.equal(result.didProbe, true);
  assert.equal(seenRequests.length, 3, 'probes at most the first 3 browser_fetch=allowed sources');
  for (const req of seenRequests) {
    assert.equal(req.init.headers.Range, 'bytes=0-524287');
    assert.equal(req.init.mode, 'cors');
    assert.equal(req.init.credentials, 'omit');
    assert.equal(req.init.referrerPolicy, 'no-referrer');
  }
  assert.equal(result.primary.id, 's-fast');
  assert.equal(result.url, 'https://v26-cold.douyinvod.com/fast.mp4');
  assert.deepEqual(
    result.orderedSources.map(s => s.id),
    ['s-fast', 's-mid', 's-slow', 's-fourth', 's-dispatch'],
  );
  assert.deepEqual(result.routes, [
    'https://v26-cold.douyinvod.com/fast.mp4',
    'https://v3-dy-o.zjcdn.com/mid.mp4',
    'https://v13-cold.douyinvod.com/slow.mp4',
    'https://v9-cold.douyinvod.com/fourth.mp4',
    'https://www.douyin.com/aweme/v1/play/?video_id=1',
  ]);
});

test('route probe skips uncountable/CORS-blocked/HTML/timeout sources and falls back cleanly if none succeed', async () => {
  const sources = [
    { id: 's-cors-fail', url: 'https://a.example.test/fail.mp4', browser_fetch: 'allowed' },
    { id: 's-html', url: 'https://b.example.test/error.mp4', browser_fetch: 'allowed' },
    { id: 's-good', url: 'https://c.example.test/good.mp4', browser_fetch: 'allowed' },
  ];

  const fetchImpl = async (url) => {
    if (url.includes('a.example.test')) throw new TypeError('Failed to fetch');
    if (url.includes('b.example.test')) {
      return new Response('<!doctype html><html>blocked</html>', {
        status: 200,
        headers: { 'content-type': 'text/html' },
      });
    }
    return rangedStreamResponse(128 * 1024, 1);
  };

  const result = await routeProbe.probeAndOrderSources(
    sources,
    {},
    new AbortController().signal,
    { fetchImpl, timeoutMs: 200 },
  );
  assert.equal(result.primary.id, 's-good');
  assert.deepEqual(result.orderedSources.map(s => s.id), ['s-good', 's-cors-fail', 's-html']);

  // When every probe fails or times out with 0 bytes, original order is preserved.
  const allDead = await routeProbe.probeAndOrderSources(
    sources,
    {},
    new AbortController().signal,
    {
      timeoutMs: 40,
      fetchImpl: async (_url, init) =>
        new Promise((_, reject) => {
          init.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
        }),
    },
  );
  assert.equal(allDead.didProbe, true);
  assert.equal(allDead.primary.id, 's-cors-fail');
  assert.deepEqual(allDead.orderedSources.map(s => s.id), ['s-cors-fail', 's-html', 's-good']);
});

test('route probe issues zero network requests for single-source or non-allowed sources and honors user cancellation', async () => {
  let calls = 0;
  const fetchImpl = async () => {
    calls++;
    return rangedStreamResponse(1024);
  };

  // Single allowed source -> no probe needed.
  const single = await routeProbe.probeAndOrderSources(
    [{ id: 's-only', url: 'https://v26-cold.douyinvod.com/only.mp4', browser_fetch: 'allowed' }],
    {},
    new AbortController().signal,
    { fetchImpl },
  );
  assert.equal(single.didProbe, false);
  assert.equal(calls, 0);

  // Multiple unknown sources -> no probe sent.
  const unknown = await routeProbe.probeAndOrderSources(
    [
      { id: 's-1', url: 'https://cdn1.example.test/a.mp4' },
      { id: 's-2', url: 'https://cdn2.example.test/a.mp4', browser_fetch: 'unknown' },
      { id: 's-3', url: 'https://cdn3.example.test/a.mp4', browser_fetch: 'blocked' },
    ],
    {},
    new AbortController().signal,
    { fetchImpl },
  );
  assert.equal(unknown.didProbe, false);
  assert.equal(calls, 0);

  // Sole allowed source already at index 0 + unmeasurable fallback -> no redundant probe.
  const alreadyFirst = await routeProbe.probeAndOrderSources(
    [
      { id: 's-allowed', url: 'https://v5-hl-mly-ov.zjcdn.com/a.mp4', browser_fetch: 'allowed' },
      { id: 's-dispatch', url: 'https://www.douyin.com/aweme/v1/play/?video_id=1' },
    ],
    {},
    new AbortController().signal,
    { fetchImpl },
  );
  assert.equal(alreadyFirst.didProbe, false);
  assert.equal(calls, 0);

  // User AbortController cancels in-flight probes immediately.
  const userController = new AbortController();
  const pending = routeProbe.probeAndOrderSources(
    [
      { id: 's-a', url: 'https://v13-cold.douyinvod.com/a.mp4', browser_fetch: 'allowed' },
      { id: 's-b', url: 'https://v26-cold.douyinvod.com/b.mp4', browser_fetch: 'allowed' },
    ],
    {},
    userController.signal,
    {
      timeoutMs: 3000,
      fetchImpl: async (_url, init) =>
        new Promise((_, reject) => {
          init.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
        }),
    },
  );
  setTimeout(() => userController.abort(), 15);
  await assert.rejects(pending, err => err?.name === 'AbortError');
});
