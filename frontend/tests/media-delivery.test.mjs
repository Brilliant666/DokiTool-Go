import test, { before, after, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'vite';
let vite, delivery, segments;
const originalFetch = globalThis.fetch;
before(async () => {
  vite = await createServer({ server:{middlewareMode:true,ws:false,hmr:false,watch:null},appType:'custom' });
  delivery=await vite.ssrLoadModule('/src/utils/mediaDelivery.ts');
  segments=await vite.ssrLoadModule('/src/utils/segmentDownload.ts');
});
after(async()=>{globalThis.fetch=originalFetch;await vite?.close();});
beforeEach(()=>{globalThis.fetch=async()=>{throw new Error('unexpected request')};});
test('explicit segments preserve initialization, order and exact byte ranges without URL guesses',async()=>{
  const value={kind:'segments',join:'byte_concat',join_evidence:'validated_fragment_layout',output:{container:'mp4',extension:'mp4'},init:{sources:[{url:'https://cdn.test/object?sig=a%2Bb'}],byte_range:{start:0,end_inclusive:99}},pieces:[{index:1,sources:[{url:'https://cdn.test/object?sig=a%2Bb'}],byte_range:{start:100,end_inclusive:199}},{index:2,sources:[{url:'https://cdn.test/object?sig=a%2Bb'}],byte_range:{start:200,end_inclusive:499}}]};
  assert.deepEqual(await delivery.declaredSegments(value,{}),[{url:'https://cdn.test/object?sig=a%2Bb',start:0,length:100},{url:'https://cdn.test/object?sig=a%2Bb',start:100,length:100},{url:'https://cdn.test/object?sig=a%2Bb',start:200,length:300}]);
  await assert.rejects(delivery.declaredSegments({...value,join:'remux'},{}),/重封装/);
});
test('single HLS is fetched once, preserves a one-segment complete VOD, and does not infer URL types',async()=>{
  const calls=[];globalThis.fetch=async url=>{calls.push(url);return new Response('#EXTM3U\n#EXTINF:1,\nopaque-object?signature=a%2Bb\n#EXT-X-ENDLIST\n')};
  const value={kind:'hls',scope:'single',sources:[{url:'https://cdn.test/opaque'}],output:{container:'ts',extension:'ts'}};
  assert.deepEqual(await delivery.declaredSegments(value,{}),[{url:'https://cdn.test/opaque-object?signature=a%2Bb'}]);assert.equal(calls.length,1);
  assert.equal(delivery.mediaExtension(undefined,value),'ts');
});
test('adaptive masters, external audio and encrypted streams cannot become silent full-video downloads',async()=>{
  const source={url:'https://cdn.test/manifest'};
  await assert.rejects(delivery.declaredSegments({kind:'hls',scope:'adaptive',sources:[source]},{}),/暂不支持/);
  for(const body of ['#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=200\nlow.m3u8\n#EXT-X-ENDLIST','#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,URI="audio.m3u8"\nclip.ts\n#EXT-X-ENDLIST','#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI="key"\nclip.ts\n#EXT-X-ENDLIST']) {
    globalThis.fetch=async()=>new Response(body);assert.equal(await segments.resolveHlsSegments(source.url),null);
  }
});
test('ranged pieces require a matching 206 response; a complete object is never concatenated accidentally',async t=>{
  const realTimeout = globalThis.setTimeout;
  t.mock.method(globalThis,'setTimeout',(callback, delay, ...args)=>realTimeout(callback, delay < 1000 ? 0 : delay, ...args));
  const piece={url:'https://cdn.test/fragment',start:2,length:2};
  globalThis.fetch=async()=>new Response(new Uint8Array([1,2,3,4]),{headers:{'content-length':'4'}});
  await assert.rejects(segments.mergeSegments([piece]),/字节范围/);
  globalThis.fetch=async()=>new Response(new Uint8Array([3,4]),{status:206,headers:{'content-length':'2','content-range':'bytes 2-3/4'}});
  assert.deepEqual([...new Uint8Array(await (await segments.mergeSegments([piece])).arrayBuffer())],[3,4]);
});
test('extensions follow metadata and delivery output rather than opaque signed filenames',()=>{
  assert.equal(delivery.mediaExtension({mime_type:'image/webp'}),'webp');
  assert.equal(delivery.mediaExtension({extension:'mov'}),'mov');
  assert.equal(delivery.mediaExtension(undefined),'bin');
  assert.equal(delivery.mediaExtension(undefined,undefined,'Video/MP4; codecs=avc1'),'mp4');
  assert.equal(delivery.mediaExtension({extension:'mov'},undefined,'video/mp4'),'mov');
  assert.equal(delivery.mediaExtension({container:'mp4'}),'mp4');
});
test('dailymotion single-stream fMP4 HLS resolves #EXT-X-MAP init + m4s fragments, rejects MPEG-TS when output is mp4, and builds readable mp4 filename',async()=>{
  const { buildMediaFilename } = await vite.ssrLoadModule('/src/utils/videoFilename.ts');
  const m3u8Url = 'https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/718475466/h264_aac_fhd/1/manifest.m3u8';
  const manifest = [
    '#EXTM3U',
    '#EXT-X-VERSION:7',
    '#EXT-X-TARGETDURATION:6',
    '#EXT-X-MAP:URI="init.mp4"',
    '#EXTINF:5.000,',
    'seg-0.m4s',
    '#EXTINF:4.200,',
    'seg-1.m4s',
    '#EXT-X-ENDLIST',
  ].join('\n');
  const bodies = new Map([
    [m3u8Url, new TextEncoder().encode(manifest)],
    ['https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/718475466/h264_aac_fhd/1/init.mp4', new Uint8Array([0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70])],
    ['https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/718475466/h264_aac_fhd/1/seg-0.m4s', new Uint8Array([0x01, 0x02, 0x03])],
    ['https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/718475466/h264_aac_fhd/1/seg-1.m4s', new Uint8Array([0x04, 0x05])],
  ]);
  globalThis.fetch = async (url) => {
    const payload = bodies.get(String(url));
    if (!payload) throw new Error(`unexpected fetch: ${url}`);
    return new Response(payload, { status: 200, headers: { 'content-length': String(payload.byteLength) } });
  };
  const value = { kind: 'hls', scope: 'single', sources: [{ url: m3u8Url }], output: { container: 'mp4', extension: 'mp4', mime_type: 'video/mp4' } };
  const resolved = await delivery.declaredSegments(value, {});
  assert.deepEqual(resolved, [
    { url: 'https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/718475466/h264_aac_fhd/1/init.mp4' },
    { url: 'https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/718475466/h264_aac_fhd/1/seg-0.m4s' },
    { url: 'https://vod2.cf.dmcdn.net/sec2(tok)/cloud/fmp4/718475466/h264_aac_fhd/1/seg-1.m4s' },
  ]);
  const merged = await segments.mergeSegments(resolved);
  assert.deepEqual(
    [...new Uint8Array(await merged.arrayBuffer())],
    [0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70, 0x01, 0x02, 0x03, 0x04, 0x05],
  );
  const ext = delivery.mediaExtension(undefined, value);
  assert.equal(ext, 'mp4');
  assert.equal(
    buildMediaFilename({ title: 'Big Buck Bunny Clip', index: 1, total: 1, fallback: m3u8Url, extension: ext }),
    'Big Buck Bunny Clip.mp4',
  );

  // Defense-in-depth: MPEG-TS (.ts) segments must be rejected when output.container === "mp4"
  const legacyM3u8 = 'https://vod3.cf.dmcdn.net/sec2(tok)/video/790/233/154332097_mp4_h264_aac_hd.m3u8';
  globalThis.fetch = async () => new Response('#EXTM3U\n#EXTINF:10.0,\n../../../frag(1)/video/790/233/154332097_mp4_h264_aac_hd.ts?sig=1\n#EXT-X-ENDLIST\n');
  await assert.rejects(
    delivery.declaredSegments({ kind: 'hls', scope: 'single', sources: [{ url: legacyM3u8 }], output: { container: 'mp4', extension: 'mp4', mime_type: 'video/mp4' } }, {}),
    /HLS 清单不可读取，或需要当前不支持的处理能力/,
  );
});
