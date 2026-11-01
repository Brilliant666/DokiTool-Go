import test, { before, after, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { build, stop } from 'esbuild';

let native, blob, segments, clicks, saved, revoked, frames;
const original = { fetch: globalThis.fetch, document: globalThis.document, location: globalThis.location, createObjectURL: URL.createObjectURL, revokeObjectURL: URL.revokeObjectURL };
const request = (changes = {}) => ({ id: 'test-task', url: 'https://cdn.test/video', routes: [], filename: '作品_P1.mp4', ...changes });
const media = (bytes = [1, 2, 3, 4], headers = {}) => new Response(new Uint8Array(bytes), { headers: { 'content-type': 'video/mp4', 'content-length': String(bytes.length), ...headers } });
const signal = () => new AbortController().signal;

before(async () => {
  const bundled = await build({ entryPoints: ['src/downloads/native.ts', 'src/downloads/blob.ts', 'src/utils/segmentDownload.ts'], bundle: true, write: false, outdir: 'test-output', platform: 'node', format: 'esm' });
  const load = name => import(`data:text/javascript;base64,${Buffer.from(bundled.outputFiles.find(file => file.path.endsWith(name + '.js')).text).toString('base64')}`);
  native = await load('native');
  blob = await load('blob');
  segments = await load('segmentDownload');
  globalThis.document = { body: { appendChild(element) { if (element.tag === 'iframe') frames.push(element); } }, createElement: tag => ({ tag, setAttribute(name, value) { this[name] = value; }, click() { clicks.push({ url: this.href, filename: this.download, target: this.target }); }, remove() { this.removed = true; } }) };
  globalThis.location = { href: 'https://snap.dokitool.com/' };
  URL.createObjectURL = value => { saved.push(value); return `blob:test-${saved.length}`; };
  URL.revokeObjectURL = value => revoked.push(value);
});
beforeEach(() => { clicks = []; saved = []; revoked = []; frames = []; globalThis.fetch = async () => media(); });
after(async () => {
  stop();
  Object.assign(globalThis, { fetch: original.fetch, document: original.document, location: original.location });
  URL.createObjectURL = original.createObjectURL; URL.revokeObjectURL = original.revokeObjectURL;
});

test('native probe uses the real GET attachment headers, cancels body and accepts UTF-8 filenames', async () => {
  let options, cancelled = false, calls = 0;
  globalThis.fetch = async (_url, init) => {
    calls++; options = init;
    return new Response(new ReadableStream({ cancel() { cancelled = true; } }), { headers: {
      'content-type': 'video/mp4', 'content-disposition': "attachment; filename=video.mp4; filename*=UTF-8''%E4%BD%9C%E5%93%81.mp4",
    } });
  };
  assert.deepEqual(await native.probeNative(request(), signal()), { url: request().url, filename: '作品.mp4' });
  assert.equal(options.method, 'GET'); assert.equal(options.credentials, 'omit');
  assert.equal(calls, 1); assert.equal(cancelled, true); assert.equal(options.signal.aborted, true);
  assert.equal(clicks.length, 0);
});

test('unexposed attachment, inline playback and mismatched filename/type are not native eligible', async () => {
  for (const headers of [
    {},
    { 'content-disposition': 'inline; filename=video.mp4' },
    { 'content-disposition': 'attachment' },
    { 'content-disposition': 'attachment; filename=video.webm' },
    { 'content-disposition': 'attachment; filename=video.mp4', 'content-type': 'application/octet-stream' },
    { 'content-disposition': 'attachment; filename=video.mp4', 'content-type': 'text/html' },
  ]) {
    globalThis.fetch = async () => media([1, 2], headers);
    assert.equal(await native.probeNative(request(), signal()), null, JSON.stringify(headers));
  }
});

test('unsafe and malformed attachment names are rejected instead of silently renamed', async () => {
  for (const disposition of [
    'attachment; filename="../video.mp4"',
    'attachment; filename="CON.mp4"',
    'attachment; filename=".video.mp4"',
    'attachment; filename="video.mp4 "',
    'attachment; filename="safe.mp4"extra.exe',
    'attachment; filename="safe.mp4"; filename="different.mp4"',
    "attachment; filename*=UTF-8''%GG.mp4",
    "attachment; filename*=UTF-8''%E2%80%AEgpj.mp4",
  ]) {
    globalThis.fetch = async () => media([1], { 'content-disposition': disposition });
    assert.equal(await native.probeNative(request(), signal()), null, disposition);
  }
});

test('CORS probe failure is unknown eligibility, while user cancellation remains cancellation', async () => {
  globalThis.fetch = async () => { throw new TypeError('Failed to fetch'); };
  assert.equal(await native.probeNative(request(), signal()), null);
  const controller = new AbortController(); controller.abort();
  await assert.rejects(native.probeNative(request(), controller.signal), { name: 'AbortError' });
});

test('automatic native handoff isolates the source; manual sources use a new tab', () => {
  native.handoffNative('https://cdn.test/file.mp4', 'upstream.mp4');
  assert.equal(clicks.length, 0); assert.equal(frames.length, 1);
  assert.equal(frames[0].src, 'https://cdn.test/file.mp4'); assert.equal(frames[0].hidden, true);
  assert.equal(frames[0].sandbox, 'allow-downloads');
  assert.equal(frames[0].referrerPolicy, 'no-referrer'); assert.equal(frames[0].removed, undefined);
  native.openSource('https://cdn.test/file.mp4', 'upstream.mp4');
  assert.deepEqual(clicks[0], { url: 'https://cdn.test/file.mp4', filename: 'upstream.mp4', target: '_blank' });
  for (const url of ['javascript:alert(1)', '//other.test/file.mp4', 'https://cdn.test/list.m3u8', 'https://u:p@cdn.test/file.mp4']) {
    assert.throws(() => native.openSource(url, 'test.mp4'));
  }
});

test('same-origin native qualification and Blob retain access credentials; CDN requests omit them', async () => {
  const calls = [];
  globalThis.fetch = async (url, options) => { calls.push({ url, credentials: options.credentials }); return media(); };
  await native.probeNative(request({ url: '/api/media/weibo?ticket=signed' }), signal());
  await native.probeNative(request(), signal());
  await blob.downloadBlob(request({ url: '/api/media/weibo?ticket=signed' }), signal(), () => {});
  await blob.downloadBlob(request(), signal(), () => {});
  assert.deepEqual(calls.map(call => call.credentials), ['same-origin', 'omit', 'same-origin', 'omit']);
  assert.equal(calls[0].url, 'https://snap.dokitool.com/api/media/weibo?ticket=signed');
});

test('Blob receives the complete file before saving and uses the response media extension', async () => {
  const updates = [];
  globalThis.fetch = async () => media([5, 6, 7], { 'content-type': 'video/webm' });
  const result = await blob.downloadBlob(request(), signal(), value => updates.push(value));
  assert.equal(result.transport, 'browser_blob'); assert.equal(result.status, 'completed');
  assert.equal(result.filename, '作品_P1.webm'); assert.equal(result.total, 3);
  assert.deepEqual(new Uint8Array(await saved[0].arrayBuffer()), new Uint8Array([5, 6, 7]));
  assert.equal(clicks.length, 1); assert.equal(clicks[0].target, undefined);
  assert.equal(updates[0].status, 'queued'); assert.equal(updates.at(-1).loaded, 3);
  assert.equal(revoked.length, 0, 'Blob URL remains valid immediately after browser handoff');
});

test('Blob retains its type checks for unsupported media, unsafe generic files and mismatched media roles', async () => {
  for (const [filename, formatMime, type] of [
    ['image.png', undefined, 'image/svg+xml'],
    ['unsafe.exe', undefined, 'application/octet-stream'],
    ['original.mov', 'video/quicktime', 'image/jpeg'],
  ]) {
    globalThis.fetch = async () => media([1, 2], { 'content-type': type });
    await assert.rejects(blob.downloadBlob(request({ filename, formatMime }), signal(), () => {}), /媒体类型|音视频或图片/);
    assert.equal(clicks.length, 0);
  }
});

test('Blob accepts readable unknown-length media and resolves its size on completion', async () => {
  const updates = [];
  globalThis.fetch = async () => new Response(new Uint8Array([1, 2, 3]), { headers: { 'content-type': 'video/mp4' } });
  const result = await blob.downloadBlob(request(), signal(), value => updates.push(value));
  assert.equal(updates.find(update => update.status === 'downloading').total, 0);
  assert.equal(result.total, 3); assert.equal(result.loaded, 3); assert.equal(clicks.length, 1);
  assert.deepEqual(new Uint8Array(await saved[0].arrayBuffer()), new Uint8Array([1, 2, 3]));
});

test('Blob has no fixed 64 MiB cutoff while preserving exact saved length', async () => {
  const part = new Uint8Array(1024 * 1024), count = 65;
  let sent = 0;
  globalThis.fetch = async () => new Response(new ReadableStream({
    pull(controller) { sent++ < count ? controller.enqueue(part) : controller.close(); },
  }), { headers: { 'content-type': 'video/mp4', 'content-length': String(part.length * count) } });
  const result = await blob.downloadBlob(request(), signal(), () => {});
  assert.equal(result.status, 'completed'); assert.equal(saved[0].size, part.length * count);
  assert.equal(result.loaded, part.length * count); assert.equal(clicks.length, 1);
});

test('Blob still rejects bytes exceeding a trustworthy Content-Length', async () => {
  globalThis.fetch = async () => media([1, 2, 3, 4], { 'content-length': '1' });
  await assert.rejects(blob.downloadBlob(request(), signal(), () => {}), /超过声明大小/);
  assert.equal(saved.length, 0); assert.equal(clicks.length, 0);
});

test('Blob treats encoded lengths as unknown rather than comparing decoded bytes to wire size', async () => {
  globalThis.fetch = async () => media([1, 2, 3, 4], { 'content-length': '2', 'content-encoding': 'gzip' });
  const result = await blob.downloadBlob(request(), signal(), () => {});
  assert.equal(result.loaded, 4); assert.equal(result.total, 4);
});

test('Blob allocation failure is reported once without retrying and re-allocating every route', async () => {
  const RealBlob = globalThis.Blob;
  let calls = 0;
  globalThis.fetch = async () => { calls++; return media(); };
  globalThis.Blob = class { constructor() { throw new RangeError('Blob allocation failed'); } };
  try {
    await assert.rejects(blob.downloadBlob(request({ routes: ['https://cdn.test/backup'] }), signal(), () => {}), /Blob allocation failed/);
    assert.equal(calls, 1); assert.equal(clicks.length, 0);
  } finally { globalThis.Blob = RealBlob; }
  assert.equal((await blob.downloadBlob(request(), signal(), () => {})).status, 'completed', 'allocation failure releases the serial queue');
});

test('failed Blob routes restart cleanly and never append bytes from another file', async () => {
  const calls = [];
  globalThis.fetch = async url => {
    calls.push(url);
    return url.endsWith('/video') ? media([1, 2], { 'content-length': '4' }) : media([9, 8, 7]);
  };
  const result = await blob.downloadBlob(request({ routes: ['https://cdn.test/backup'] }), signal(), () => {});
  assert.equal(result.url, 'https://cdn.test/backup'); assert.equal(calls.length, 2);
  assert.deepEqual(new Uint8Array(await saved[0].arrayBuffer()), new Uint8Array([9, 8, 7]));
  assert.equal(clicks.length, 1);
});

test('Blob rejects error pages and incomplete files without saving', async () => {
  for (const response of [media([1], { 'content-type': 'text/html' }), media([...new TextEncoder().encode('<html>denied</html>')]), media([1], { 'content-length': '2' }), media([], { 'content-length': '1' }), media([1], { 'content-range': 'bytes 0-0/2' })]) {
    globalThis.fetch = async () => response;
    await assert.rejects(blob.downloadBlob(request(), signal(), () => {}));
    assert.equal(clicks.length, 0);
  }
});

test('declared complete files use native qualification without an extra format-reading probe', async () => {
  let calls = 0, cancelled = false, options;
  globalThis.fetch = async (_url, init) => {
    calls++; options = init;
    return new Response(new ReadableStream({
      pull(controller) { controller.enqueue(new Uint8Array([0, 0, 0, 8, 102, 116, 121, 112])); },
      cancel() { cancelled = true; },
    }, { highWaterMark: 0 }), { headers: { 'content-type': 'video/mp4', 'content-disposition': 'attachment; filename=video.mp4' } });
  };
  assert.deepEqual(await native.probeNative(request(),signal()),{url:request().url,filename:'video.mp4'});
  assert.equal(calls, 1); assert.equal(cancelled, true);
  assert.equal(options.headers?.Range, undefined); assert.equal(options.credentials, 'omit');
});

test('partial, failed and disguised error responses cannot supply reusable native eligibility', async () => {
  for (const response of [
    new Response(new Uint8Array([1]), { status: 206, headers: { 'content-type': 'video/mp4', 'content-range': 'bytes 0-0/2', 'content-disposition': 'attachment; filename=video.mp4' } }),
    media([1], { 'content-range': 'bytes 0-0/2' }),
    media([...new TextEncoder().encode('<html>denied</html>')]),
    new Response('denied', { status: 403 }),
  ]) {
    globalThis.fetch = async () => response;
    assert.equal(await native.probeNative(request(),signal()),null);
  }
});

test('explicit extensionless HLS stays in its manifest pipeline', async () => {
  globalThis.fetch = async () => new Response('#EXTM3U\n#EXTINF:1,\nfirst.ts\n#EXTINF:1,\nsecond.ts\n#EXT-X-ENDLIST', { headers: { 'content-type': 'application/octet-stream' } });
  assert.deepEqual(await segments.resolveHlsSegments(request().url,signal()), [
    { url: 'https://cdn.test/first.ts' }, { url: 'https://cdn.test/second.ts' },
  ]);
  assert.equal(clicks.length,0);
});

test('cancelling a body read aborts the transfer and never triggers a save', async () => {
  let reading;
  const started = new Promise(resolve => { reading = resolve; });
  let cancelled = false;
  globalThis.fetch = async () => new Response(new ReadableStream({ pull() { reading(); }, cancel() { cancelled = true; } }), { headers: { 'content-type': 'video/mp4', 'content-length': '4' } });
  const controller = new AbortController(); const updates = [];
  const done = blob.downloadBlob(request(), controller.signal, update => updates.push(update));
  await started; controller.abort();
  await assert.rejects(done, { name: 'AbortError' });
  assert.equal(cancelled, true); assert.equal(updates.at(-1).status, 'cancelled'); assert.equal(clicks.length, 0);
});

test('Blob downloads serialize, a cancelled waiter does not fetch or block the next task', async () => {
  let finishFirst, calls = 0;
  globalThis.fetch = async () => {
    calls++;
    return calls === 1 ? new Response(new ReadableStream({ start(controller) { finishFirst = () => { controller.enqueue(new Uint8Array([1])); controller.close(); }; } }), { headers: { 'content-type': 'video/mp4', 'content-length': '1' } }) : media();
  };
  const first = blob.downloadBlob(request({ id: 'first' }), signal(), () => {});
  const middleController = new AbortController();
  const middle = blob.downloadBlob(request({ id: 'middle' }), middleController.signal, () => {});
  const last = blob.downloadBlob(request({ id: 'last' }), signal(), () => {});
  await new Promise(resolve => setTimeout(resolve, 10));
  assert.equal(calls, 1);
  middleController.abort(); await assert.rejects(middle, { name: 'AbortError' });
  finishFirst(); await first; await last;
  assert.equal(calls, 2); assert.equal(clicks.length, 2);
});

test('header and reader stalls time out independently, with at most three distinct routes', async () => {
  const realSetTimeout = globalThis.setTimeout;
  globalThis.setTimeout = (callback, ms, ...args) => realSetTimeout(callback, ms === 30000 ? 5 : ms, ...args);
  try {
    for (const stall of ['headers', 'body']) {
      const calls = [];
      globalThis.fetch = async url => {
        calls.push(url);
        return stall === 'headers' ? new Promise(() => {}) : new Response(new ReadableStream(), { headers: { 'content-type': 'video/mp4', 'content-length': '4' } });
      };
      await assert.rejects(blob.downloadBlob(request({ routes: [request().url, 'https://cdn.test/b', 'https://cdn.test/c', 'https://cdn.test/d'] }), signal(), () => {}), /30 秒没有返回数据/);
      assert.deepEqual(calls, [request().url, 'https://cdn.test/b', 'https://cdn.test/c']);
    }
    assert.equal(clicks.length, 0);
  } finally { globalThis.setTimeout = realSetTimeout; }
});
