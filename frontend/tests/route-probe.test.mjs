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

function createVirtualClock() {
  let now = 0;
  const queue = [];
  let pumping = false;
  async function pump() {
    if (pumping) return;
    pumping = true;
    while (queue.length > 0) {
      for (let i = 0; i < 8; i++) await Promise.resolve();
      if (queue.length === 0) break;
      queue.sort((a, b) => a.at - b.at);
      const next = queue.shift();
      if (next.at > now) now = next.at;
      next.resolve();
    }
    pumping = false;
  }
  return {
    get now() {
      return now;
    },
    setNow(next) {
      if (next > now) now = next;
    },
    sleep(ms, signal) {
      return new Promise((resolve, reject) => {
        if (signal?.aborted) {
          reject(new DOMException('Aborted', 'AbortError'));
          return;
        }
        const entry = {
          at: now + ms,
          resolve: () => {
            cleanup();
            resolve();
          },
        };
        const onAbort = () => {
          const idx = queue.indexOf(entry);
          if (idx >= 0) queue.splice(idx, 1);
          cleanup();
          reject(new DOMException('Aborted', 'AbortError'));
        };
        const cleanup = () => signal?.removeEventListener('abort', onAbort);
        signal?.addEventListener('abort', onAbort, { once: true });
        queue.push(entry);
        void pump();
      });
    },
  };
}

function scriptedBodyResponse(chunks, signal, sleepFn, totalBytes = 484_077_446) {
  let index = 0;
  const bodySize = chunks.reduce((sum, c) => sum + c.bytes, 0);
  const body = new ReadableStream({
    async pull(controller) {
      if (index >= chunks.length) {
        controller.close();
        return;
      }
      const step = chunks[index++];
      if (step.delayMs > 0) {
        await sleepFn(step.delayMs, signal);
      }
      if (step.hangUntilAbort) {
        await new Promise((_, reject) => {
          if (signal?.aborted) {
            reject(new DOMException('Aborted', 'AbortError'));
            return;
          }
          signal?.addEventListener(
            'abort',
            () => reject(new DOMException('Aborted', 'AbortError')),
            { once: true },
          );
        });
      }
      const chunk = new Uint8Array(step.bytes);
      if (chunk.byteLength >= 4) chunk[3] = 0x18;
      controller.enqueue(chunk);
    },
  });
  return new Response(body, {
    status: 206,
    headers: {
      'content-type': 'video/mp4',
      'content-range': `bytes 0-${Math.max(0, bodySize - 1)}/${totalBytes}`,
      'content-length': String(bodySize),
    },
  });
}

test('#107: high-TTFB fast-transfer route beats low-TTFB initial-burst slow-transfer route because pre-header TTFB is excluded', async () => {
  const clock = createVirtualClock();
  const douyin4kSize = 484_077_446; // 461.65 MiB (#107 reproduction sample)
  const sources = [
    {
      id: 's-v11-cold',
      url: 'https://v11-cold.douyinvod.com/4k-slow.mp4',
      access: 'direct',
      browser_fetch: 'allowed',
      size_bytes: douyin4kSize,
    },
    {
      id: 's-v26-cold',
      url: 'https://v26-cold.douyinvod.com/4k-fast.mp4',
      access: 'direct',
      browser_fetch: 'allowed',
      size_bytes: douyin4kSize,
    },
  ];

  const seenRanges = [];
  const fetchImpl = async (url, init) => {
    seenRanges.push({ url, range: init.headers.Range });
    if (url.includes('v11-cold')) {
      // Fast 30ms handshake/TTFB + 512 KiB burst in 16ms, then throttled to 3 MiB/s (1.5 MiB in 500ms)
      await clock.sleep(30, init.signal);
      return scriptedBodyResponse(
        [
          { bytes: 512 * 1024, delayMs: 16 },
          { bytes: 1536 * 1024, delayMs: 500 },
        ],
        init.signal,
        (ms, sig) => clock.sleep(ms, sig),
        douyin4kSize,
      );
    }
    if (url.includes('v26-cold')) {
      // Slow 1200ms cold TTFB, then sustained 22 MiB/s (2 MiB in 2000/22 ~= 90.91ms)
      await clock.sleep(1200, init.signal);
      return scriptedBodyResponse(
        [{ bytes: 2 * 1024 * 1024, delayMs: 2000 / 22 }],
        init.signal,
        (ms, sig) => clock.sleep(ms, sig),
        douyin4kSize,
      );
    }
    throw new Error(`unexpected probe for ${url}`);
  };

  const result = await routeProbe.probeAndOrderSources(
    sources,
    {},
    new AbortController().signal,
    { fetchImpl, nowFn: () => clock.now },
  );

  assert.equal(result.didProbe, true);
  assert.deepEqual(
    seenRanges.map(r => r.range),
    ['bytes=0-2097151', 'bytes=0-2097151'],
  );

  const obsV11 = result.observations.find(o => o.source.id === 's-v11-cold');
  const obsV26 = result.observations.find(o => o.source.id === 's-v26-cold');
  assert.equal(obsV11.ok, true);
  assert.equal(obsV26.ok, true);
  assert.equal(obsV11.bytesRead, 2 * 1024 * 1024);
  assert.equal(obsV26.bytesRead, 2 * 1024 * 1024);
  // Pre-header TTFB (30ms and 1200ms) MUST NOT be counted in elapsedMs:
  assert.equal(obsV11.elapsedMs, 516);
  assert.ok(Math.abs(obsV26.elapsedMs - 2000 / 22) < 1e-6);
  assert.ok(
    Math.abs(obsV26.bytesPerSecond - 22 * 1024 * 1024) < 1,
    `expected ~22 MiB/s, got ${obsV26.bytesPerSecond}`,
  );
  assert.ok(
    obsV26.bytesPerSecond > obsV11.bytesPerSecond * 5,
    'v26-cold sustained throughput must beat v11-cold after excluding TTFB and diluting 512 KiB burst',
  );
  assert.equal(result.primary.id, 's-v26-cold');
  assert.deepEqual(result.orderedSources.map(s => s.id), ['s-v26-cold', 's-v11-cold']);
});

test('#107: adaptive probe window uses 512 KiB for <= 50 MiB or unknown size, 2 MiB for > 50 MiB, and honors explicit options.probeBytes', async () => {
  assert.equal(routeProbe.ROUTE_PROBE_MAX_SOURCES, 3);
  assert.equal(routeProbe.ROUTE_PROBE_BYTES, 512 * 1024);
  assert.equal(routeProbe.ROUTE_PROBE_LARGE_FILE_THRESHOLD_BYTES, 50 * 1024 * 1024);
  assert.equal(routeProbe.ROUTE_PROBE_LARGE_BYTES, 2 * 1024 * 1024);
  assert.equal(routeProbe.ROUTE_PROBE_TIMEOUT_MS, 3000);

  const fiftyMiB = 50 * 1024 * 1024;
  const cases = [
    { name: 'unknown size', sizeBytes: undefined, expectedRange: 'bytes=0-524287' },
    { name: 'small 10 MiB', sizeBytes: 10 * 1024 * 1024, expectedRange: 'bytes=0-524287' },
    { name: 'exact 50 MiB boundary', sizeBytes: fiftyMiB, expectedRange: 'bytes=0-524287' },
    { name: '50 MiB + 1 byte', sizeBytes: fiftyMiB + 1, expectedRange: 'bytes=0-2097151' },
    { name: 'Douyin 4K 484,077,446 B', sizeBytes: 484_077_446, expectedRange: 'bytes=0-2097151' },
  ];

  for (const tc of cases) {
    const mkSources = () => [
      {
        id: 's-slow',
        url: 'https://v11-cold.douyinvod.com/a.mp4',
        browser_fetch: 'allowed',
        ...(tc.sizeBytes !== undefined ? { size_bytes: tc.sizeBytes } : {}),
      },
      {
        id: 's-fast',
        url: 'https://v26-cold.douyinvod.com/a.mp4',
        browser_fetch: 'allowed',
        ...(tc.sizeBytes !== undefined ? { size_bytes: tc.sizeBytes } : {}),
      },
    ];

    // 1) Direct probeAndOrderSources
    const directRanges = [];
    await routeProbe.probeAndOrderSources(
      mkSources(),
      {},
      new AbortController().signal,
      {
        fetchImpl: async (url, init) => {
          directRanges.push(init.headers.Range);
          return rangedStreamResponse(64 * 1024, url.includes('v26') ? 1 : 10);
        },
      },
    );
    assert.deepEqual(
      directRanges,
      [tc.expectedRange, tc.expectedRange],
      `probeAndOrderSources range mismatch for ${tc.name}`,
    );

    // 2) Default export probeFileTarget
    const targetSources = mkSources();
    const targetRanges = [];
    const statuses = [];
    const updatedTarget = await routeProbe.default(
      {
        source: targetSources[0],
        url: targetSources[0].url,
        routes: targetSources.map(s => s.url),
        variant: {
          delivery: { kind: 'file', sources: targetSources },
        },
      },
      { type: 'video' },
      'douyin',
      {},
      new AbortController().signal,
      text => statuses.push(text),
      {
        fetchImpl: async (url, init) => {
          targetRanges.push(init.headers.Range);
          return rangedStreamResponse(64 * 1024, url.includes('v26') ? 1 : 10);
        },
      },
    );
    assert.deepEqual(
      targetRanges,
      [tc.expectedRange, tc.expectedRange],
      `probeFileTarget range mismatch for ${tc.name}`,
    );
    assert.equal(updatedTarget.source.id, 's-fast');
    assert.deepEqual(statuses, ['正在优选下载线路...']);
  }

  // Size extraction fallbacks in probeFileTarget: target.source.size_bytes, variant.metadata.size_bytes, secondary source.size_bytes, and options.sizeBytes
  for (const mode of ['targetSource', 'variantMetadata', 'secondarySource', 'optionsSizeBytes']) {
    const s0 = {
      id: 's-0',
      url: 'https://v11-cold.douyinvod.com/fallback.mp4',
      browser_fetch: 'allowed',
      ...(mode === 'targetSource' ? { size_bytes: 484_077_446 } : {}),
    };
    const s1 = {
      id: 's-1',
      url: 'https://v26-cold.douyinvod.com/fallback.mp4',
      browser_fetch: 'allowed',
      ...(mode === 'secondarySource' ? { size_bytes: 484_077_446 } : {}),
    };
    const sourcesForDelivery =
      mode === 'targetSource'
        ? [{ id: 's-0', url: s0.url, browser_fetch: 'allowed' }, s1]
        : [s0, s1];
    const ranges = [];
    await routeProbe.default(
      {
        source: s0,
        url: s0.url,
        routes: [s0.url, s1.url],
        variant: {
          ...(mode === 'variantMetadata' ? { metadata: { size_bytes: 484_077_446 } } : {}),
          delivery: { kind: 'file', sources: sourcesForDelivery },
        },
      },
      { type: 'video' },
      'douyin',
      {},
      new AbortController().signal,
      undefined,
      {
        ...(mode === 'optionsSizeBytes' ? { sizeBytes: 484_077_446 } : {}),
        fetchImpl: async (_url, init) => {
          ranges.push(init.headers.Range);
          return rangedStreamResponse(64 * 1024, 1);
        },
      },
    );
    assert.deepEqual(ranges, ['bytes=0-2097151', 'bytes=0-2097151'], `failed size extraction via ${mode}`);
  }

  // Explicit options.probeBytes overrides both large (> 50 MiB) and small/unknown sizes
  const overrideLargeRanges = [];
  await routeProbe.default(
    {
      source: { id: 's-0', url: 'https://v11-cold.douyinvod.com/o.mp4', browser_fetch: 'allowed', size_bytes: 484_077_446 },
      url: 'https://v11-cold.douyinvod.com/o.mp4',
      routes: ['https://v11-cold.douyinvod.com/o.mp4', 'https://v26-cold.douyinvod.com/o.mp4'],
      variant: {
        delivery: {
          kind: 'file',
          sources: [
            { id: 's-0', url: 'https://v11-cold.douyinvod.com/o.mp4', browser_fetch: 'allowed', size_bytes: 484_077_446 },
            { id: 's-1', url: 'https://v26-cold.douyinvod.com/o.mp4', browser_fetch: 'allowed', size_bytes: 484_077_446 },
          ],
        },
      },
    },
    { type: 'video' },
    'douyin',
    {},
    new AbortController().signal,
    undefined,
    {
      probeBytes: 256 * 1024,
      fetchImpl: async (_url, init) => {
        overrideLargeRanges.push(init.headers.Range);
        return rangedStreamResponse(64 * 1024, 1);
      },
    },
  );
  assert.deepEqual(overrideLargeRanges, ['bytes=0-262143', 'bytes=0-262143']);

  const overrideSmallRanges = [];
  await routeProbe.probeAndOrderSources(
    [
      { id: 's-0', url: 'https://v11-cold.douyinvod.com/s.mp4', browser_fetch: 'allowed', size_bytes: 1024 },
      { id: 's-1', url: 'https://v26-cold.douyinvod.com/s.mp4', browser_fetch: 'allowed', size_bytes: 1024 },
    ],
    {},
    new AbortController().signal,
    {
      probeBytes: 1024 * 1024,
      fetchImpl: async (_url, init) => {
        overrideSmallRanges.push(init.headers.Range);
        return rangedStreamResponse(64 * 1024, 1);
      },
    },
  );
  assert.deepEqual(overrideSmallRanges, ['bytes=0-1048575', 'bytes=0-1048575']);
});

test('#107: probeFileTarget caps probing at first 3 available allowed routes and preserves available[0] on all-fail', async () => {
  const now = Date.parse('2026-11-14T12:00:00Z');
  const sources = [
    {
      id: 's-expired',
      url: 'https://sns-video-exp.xhscdn.com/v.mp4',
      browser_fetch: 'allowed',
      expires_at: '2026-11-14T11:00:00Z',
      size_bytes: 80 * 1024 * 1024,
    },
    {
      id: 's-qn',
      url: 'https://sns-video-qn.xhscdn.com/v.mp4',
      browser_fetch: 'allowed',
      size_bytes: 80 * 1024 * 1024,
    },
    {
      id: 's-qc-403',
      url: 'https://sns-video-qc.xhscdn.com/v.mp4',
      browser_fetch: 'allowed',
      size_bytes: 80 * 1024 * 1024,
    },
    {
      id: 's-al',
      url: 'https://sns-video-al.xhscdn.com/v.mp4',
      browser_fetch: 'allowed',
      size_bytes: 80 * 1024 * 1024,
    },
    {
      id: 's-bd-fourth',
      url: 'https://sns-video-bd.xhscdn.com/v.mp4',
      browser_fetch: 'allowed',
      size_bytes: 80 * 1024 * 1024,
    },
  ];

  // When all 3 probed routes fail (403 / network / HTML), fallback picks available[0] ('s-qn') and keeps available order
  const probedUrls = [];
  const target = {
    source: sources[1],
    url: sources[1].url,
    routes: sources.slice(1).map(s => s.url),
    variant: { delivery: { kind: 'file', sources } },
  };
  const fallbackTarget = await routeProbe.default(
    target,
    { type: 'video' },
    'xiaohongshu',
    { now },
    new AbortController().signal,
    undefined,
    {
      fetchImpl: async url => {
        probedUrls.push(url);
        return new Response('Forbidden', { status: 403 });
      },
    },
  );

  assert.deepEqual(probedUrls, [sources[1].url, sources[2].url, sources[3].url]);
  assert.equal(fallbackTarget.source.id, 's-qn');
  assert.equal(fallbackTarget.url, sources[1].url);
  assert.deepEqual(fallbackTarget.routes, [
    sources[1].url,
    sources[2].url,
    sources[3].url,
    sources[4].url,
  ]);
});

test('#107: post-header timeout partial read uses post-header elapsedMs while pre-header, 0-byte timeout, and mid-stream reset return ok=false', async () => {
  const clock1 = createVirtualClock();
  const sources1 = [
    {
      id: 's-preheader-timeout',
      url: 'https://cdn-pre.example.test/v.mp4',
      browser_fetch: 'allowed',
      size_bytes: 80 * 1024 * 1024,
    },
    {
      id: 's-partial-body-timeout',
      url: 'https://cdn-partial.example.test/v.mp4',
      browser_fetch: 'allowed',
      size_bytes: 80 * 1024 * 1024,
    },
  ];

  const result1 = await routeProbe.probeAndOrderSources(
    sources1,
    {},
    new AbortController().signal,
    {
      timeoutMs: 80,
      nowFn: () => clock1.now,
      fetchImpl: async (url, init) => {
        if (url.includes('cdn-pre')) {
          return new Promise((_, reject) => {
            init.signal.addEventListener(
              'abort',
              () => {
                clock1.setNow(1500);
                reject(new DOMException('Aborted', 'AbortError'));
              },
              { once: true },
            );
          });
        }
        await Promise.resolve();
        clock1.setNow(1200);
        let chunkSent = false;
        const body = new ReadableStream({
          async pull(controller) {
            if (!chunkSent) {
              chunkSent = true;
              const chunk = new Uint8Array(1024 * 1024);
              chunk[3] = 0x18;
              controller.enqueue(chunk);
              return;
            }
            await new Promise((_, reject) => {
              init.signal.addEventListener(
                'abort',
                () => {
                  clock1.setNow(1500);
                  reject(new DOMException('Aborted', 'AbortError'));
                },
                { once: true },
              );
            });
          },
        });
        return new Response(body, {
          status: 206,
          headers: {
            'content-type': 'video/mp4',
            'content-range': 'bytes 0-2097151/83886080',
          },
        });
      },
    },
  );

  const obsPre = result1.observations.find(o => o.source.id === 's-preheader-timeout');
  const obsPartial = result1.observations.find(o => o.source.id === 's-partial-body-timeout');

  // Pre-header timeout: transferStartedAt === undefined -> ok: false, bytesRead: 0, elapsedMs: 0, bytesPerSecond: 0
  assert.equal(obsPre.ok, false);
  assert.equal(obsPre.bytesRead, 0);
  assert.equal(obsPre.elapsedMs, 0);
  assert.equal(obsPre.bytesPerSecond, 0);

  // Post-header partial-read timeout: 1 MiB over 300ms post-header (1500 - 1200, excluding 1200ms TTFB)
  assert.equal(obsPartial.ok, true);
  assert.equal(obsPartial.bytesRead, 1024 * 1024);
  assert.equal(obsPartial.elapsedMs, 300);
  assert.equal(obsPartial.bytesPerSecond, (1024 * 1024 * 1000) / 300);
  assert.equal(result1.primary.id, 's-partial-body-timeout');

  // Post-header 0-byte timeout and mid-stream non-timeout network reset both return ok: false
  const clock2 = createVirtualClock();
  const sources2 = [
    {
      id: 's-zero-body-timeout',
      url: 'https://cdn-zero.example.test/v.mp4',
      browser_fetch: 'allowed',
      size_bytes: 80 * 1024 * 1024,
    },
    {
      id: 's-midstream-reset',
      url: 'https://cdn-reset.example.test/v.mp4',
      browser_fetch: 'allowed',
      size_bytes: 80 * 1024 * 1024,
    },
  ];

  const result2 = await routeProbe.probeAndOrderSources(
    sources2,
    {},
    new AbortController().signal,
    {
      timeoutMs: 60,
      nowFn: () => clock2.now,
      fetchImpl: async (url, init) => {
        clock2.setNow(400);
        if (url.includes('cdn-zero')) {
          return scriptedBodyResponse(
            [{ bytes: 64 * 1024, delayMs: 0, hangUntilAbort: true }],
            init.signal,
            async () => {},
          );
        }
        let sent = false;
        const body = new ReadableStream({
          pull(controller) {
            if (!sent) {
              sent = true;
              const chunk = new Uint8Array(256 * 1024);
              chunk[3] = 0x18;
              controller.enqueue(chunk);
              return;
            }
            clock2.setNow(500);
            throw new TypeError('network reset');
          },
        });
        return new Response(body, {
          status: 206,
          headers: {
            'content-type': 'video/mp4',
            'content-range': 'bytes 0-2097151/83886080',
          },
        });
      },
    },
  );

  const obsZero = result2.observations.find(o => o.source.id === 's-zero-body-timeout');
  const obsReset = result2.observations.find(o => o.source.id === 's-midstream-reset');
  assert.equal(obsZero.ok, false);
  assert.equal(obsZero.bytesRead, 0);
  assert.equal(obsZero.bytesPerSecond, 0);
  assert.equal(obsReset.ok, false);
  assert.equal(obsReset.bytesRead, 0);
  assert.equal(obsReset.bytesPerSecond, 0);
  assert.equal(result2.primary.id, 's-zero-body-timeout');
});
