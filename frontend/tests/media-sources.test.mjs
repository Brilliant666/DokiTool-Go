import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { transformWithEsbuild } from 'vite';
import { parse, compileScript } from '@vue/compiler-sfc';
import { createRenderer, h, markRaw, nextTick, reactive, ref } from 'vue';

const require = createRequire(import.meta.url);
let labels, contract, useVideoSelection, ResultSection;
const originals = { window: globalThis.window, document: globalThis.document };
before(async () => {
  const modules = {};
  async function load(relative, aliases = {}) {
    const content = await readFile(new URL(relative, import.meta.url), 'utf8');
    const compiled = await transformWithEsbuild(content, relative, { loader: 'ts', format: 'cjs' });
    const module = { exports: {} };
    new Function('require', 'module', 'exports', compiled.code)(id => aliases[id] || modules[id] || require(id), module, module.exports);
    return module.exports;
  }
  contract = modules['../utils/mediaContract'] = await load('../src/utils/mediaContract.ts');
  labels = modules['../utils/mediaSources'] = await load('../src/utils/mediaSources.ts');
  modules['../services/apiConfig'] = { apiConfig: { baseUrl: '' } };
  ({ useVideoSelection } = await load('../src/composables/useVideoSelection.ts'));
  const content = await readFile(new URL('../src/components/ResultSection.vue', import.meta.url), 'utf8');
  const { descriptor } = parse(content);
  const script = compileScript(descriptor, { id: 'media-sources', inlineTemplate: true,
    templateOptions: { compilerOptions: { hoistStatic: false } } });
  const compiled = await transformWithEsbuild(script.content, 'ResultSection.js', { loader: 'js', format: 'cjs' });
  const module = { exports: {} };
  new Function('require', 'module', 'exports', compiled.code)(id => {
    if (id === './VideoPlaylist.vue') return { __esModule: true, default: { render: () => null } };
    if (id === './AppIcon.vue') return { __esModule: true, default: { render: () => h('i') } };
    return modules[id] || require(id);
  }, module, module.exports);
  ResultSection = module.exports.default;
  globalThis.window = { addEventListener() {}, removeEventListener() {} };
  globalThis.document = { body: { style: {} } };
});
after(() => Object.assign(globalThis, originals));

const url = route => `https://media.example.test/${route}.mp4`;
const variant = (id, routes = [id], metadata = {}) => ({ id,
  ...(Object.keys(metadata).length ? { metadata: { ...metadata, evidence: 'probe' } } : {}),
  delivery: { kind: 'file', sources: routes.map(route => ({ id: route, url: url(route) })) } });
const item = (variants = [variant('hd', ['a', 'b', 'c', 'd'])], chosen = variants[0].id, sourceId) => {
  const selected = variants.find(v => v.id === chosen), source = selected.delivery.sources.find(s => s.id === sourceId) || selected.delivery.sources[0];
  return { id: 'video-1', type: 'video', title: '测试作品', variants,
    default: { variant_id: selected.id, source_id: source.id, url: source.url, ...(source.access ? { access: source.access } : {}), ...(source.expires_at ? { expires_at: source.expires_at } : {}) } };
};
const data = (mediaItem, extra = {}) => ({ platform: 'future_platform', items: [mediaItem], ...extra });

test('all design examples are readable without platform-specific normalization', async () => {
  const directory = new URL('../../docs/schema/unified-v1/examples/', import.meta.url);
  for (const file of await readdir(directory)) {
    const envelope = JSON.parse(await readFile(new URL(file, directory), 'utf8'));
    if (envelope.code !== 'OK') continue;
    const snapshot = JSON.stringify(envelope);
    assert.deepEqual(contract.validateMediaEnvelope(envelope), envelope.data, file);
    assert.equal(JSON.stringify(envelope), snapshot, file);
  }
});

test('default follows the exact server reference regardless of array order or client quality guesses', () => {
  const low = variant('720', ['low'], { width: 720, height: 1280 }), high = variant('4k', ['a', 'b'], { width: 2160, height: 3840 });
  const value = item([low, high], '4k', 'b'), snapshot = structuredClone(value);
  assert.equal(contract.getDefaultFileUrl(value), url('b'));
  value.variants.reverse(); high.delivery.sources.reverse();
  assert.equal(contract.getDefaultFileUrl(value), url('b'));
  // This client is deliberately not a second policy engine. Backend semantics
  // tests reject a low default; this reader must never silently replace it.
  const serverLow = item([high, low], '720');
  assert.equal(contract.getDefaultFileUrl(serverLow), url('low'));
  assert.deepEqual(snapshot.default, value.default);
});

test('invalid default projections fail closed instead of borrowing the first file', () => {
  for (const patch of [{ variant_id: 'missing' }, { source_id: 'missing' }, { url: url('other') }, { access: 'relay' }, { expires_at: '2099-01-01T00:00:00Z' }]) {
    const value = item(); Object.assign(value.default, patch);
    assert.equal(contract.getDefaultDownloadTarget(value).kind, 'invalid');
  }
  const missingSource = item(); delete missingSource.default.source_id;
  assert.equal(contract.getDefaultDownloadTarget(missingSource).kind, 'invalid');
});

test('expired and unavailable sources never silently choose another variant', () => {
  const value = item(); value.variants[0].delivery.sources[0].expires_at = '2020-01-01T00:00:00Z'; value.default.expires_at = '2020-01-01T00:00:00Z';
  assert.equal(contract.getDefaultDownloadTarget(value).kind, 'needs_refresh');
  delete value.variants[0].delivery.sources[0].expires_at; delete value.default.expires_at;
  value.variants[0].delivery.sources[0].availability = 'unavailable';
  assert.equal(contract.getDefaultDownloadTarget(value).kind, 'unavailable');
  assert.equal(contract.getDownloadTarget(value, { variant_id: 'hd', source_id: 'b' }).url, url('b'));
});

test('source signatures survive relay expansion and unsafe origins or paths are rejected', () => {
  const signed = '/api/media?ticket=abc%2Bdef%2Fghi%3D&x=one+two';
  assert.equal(contract.sourceUrl({ url: signed, access: 'relay' }, { apiOrigin: 'https://api.example.test' }), 'https://api.example.test' + signed);
  assert.equal(contract.sourceUrl({ url: url('a') + '?sig=a%2Bb+Z' }), url('a') + '?sig=a%2Bb+Z');
  for (const path of ['/api/../private', '/api/%252e%252e/private', '//evil.test/api/a', '/api/a#secret', '/api/%5cprivate']) assert.throws(() => contract.sourceUrl({ url: path, access: 'relay' }));
  for (const origin of ['https://user:pass@api.example.test', 'https://api.example.test/path', 'https://api.example.test?x=1', 'https://api.example.test/prefix/..', 'https://api.example.test/%2e%2e', 'https://api.example.test:99999']) assert.throws(() => contract.sourceUrl({ url: signed, access: 'relay' }, { apiOrigin: origin }));
});

test('DASH, mux and remux cannot masquerade as a complete downloadable video', () => {
  const dash = { id: 'dash', delivery: { kind: 'dash', scope: 'adaptive', sources: [{ url: 'https://media.example.test/manifest.mpd' }] } };
  const video = { id: 'v', role: 'video', delivery: variant('track').delivery }, audio = { id: 'a', role: 'audio', delivery: variant('audio').delivery };
  const mux = { id: 'mux', delivery: { kind: 'mux', resources: [video, audio], output: { container: 'mp4', extension: 'mp4' } } };
  const remux = { id: 'remux', delivery: { kind: 'segments', join: 'remux', pieces: [{ index: 1, sources: [{ url: url('segment') }] }] } };
  for (const processing of [dash, mux, remux]) {
    const value = { id: 'one', type: 'video', variants: [processing], default: null, default_unavailable_reason: 'processing_required' };
    assert.equal(contract.getDefaultFileUrl(value), null);
    const selected = contract.getDownloadTarget(value, { variant_id: processing.id });
    assert.equal(selected.kind, 'unsupported'); assert.match(selected.message, /暂不支持/);
    assert.deepEqual(selected.requires, contract.requiredCapabilities(processing.delivery));
  }
});

test('explicit choice survives refreshed server defaults and never falls back if its identity disappears', () => {
  const first = item([variant('large'), variant('small')]);
  const store = reactive({ resultData: data(first) }), selection = useVideoSelection({ videoStore: store }); selection.syncResult();
  selection.choose({ variant_id: 'small', source_id: 'small' });
  assert.equal(selection.currentVideoUrl.value, url('small'));
  store.resultData = data(item([variant('new'), variant('small')], 'new'));
  assert.equal(selection.currentVideoUrl.value, url('small'));
  store.resultData = data(item([variant('new')]));
  assert.equal(selection.currentTarget.value.kind, 'invalid'); assert.equal(selection.currentVideoUrl.value, '');
  selection.resetDefault(); assert.equal(selection.currentVideoUrl.value, url('new'));
});

test('display labels preserve server intent without promoting original endpoint provenance', () => {
  assert.equal(labels.variantLabel({ ...variant('raw'), provenance: 'original_endpoint' }), '画质未知');
  assert.equal(labels.variantLabel({ ...variant('raw'), provenance: 'original_endpoint', metadata: { width: 720, height: 1280 } }), '720p');
  assert.equal(labels.variantLabel({ ...variant('raw'), label: '原画' }), '原画');
  assert.equal(labels.formatMediaSize(60342212), '60.3 MB'); assert.equal(labels.formatMediaSize(0), '大小未知');
});

async function mount(mediaData) {
  const element = (type, text = '') => markRaw({ type, text, props: {}, children: [], parent: null, style: {}, dataset: {}, removeAttribute() {}, getAttribute(name) { return this.props[name]; } });
  const detach = node => { if (node.parent) node.parent.children = node.parent.children.filter(item => item !== node); node.parent = null; };
  const portal = element('portal');
  const renderer = createRenderer({
    querySelector: () => portal, createElement: element, createText: text => element('#text', text), createComment: text => element('#comment', text),
    setText(node, text) { node.text = text; }, setElementText(node, text) { node.text = text; node.children = []; },
    parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
    insert(node, parent, anchor) { detach(node); node.parent = parent; const index = parent.children.indexOf(anchor); parent.children.splice(index < 0 ? parent.children.length : index, 0, node); }, remove: detach,
    patchProp(node, key, _, value) { node.props[key] = value; if (key === 'src') node.src = value; if (key === 'data-playback-key') node.dataset.playbackKey = value; },
  });
  const store = reactive({ resultData: mediaData }), selection = useVideoSelection({ videoStore: store }); selection.syncResult();
  const switched = [], copied = [], downloaded = [];
  const state = ref({ resultVersion: 1, locale: 'zh-CN', formatNumber: String, formatDuration: String,
    onSwitchVideo(choice) { switched.push(choice); selection.choose(choice); }, onCopyUrl(value) { copied.push(value); },
    onDownloadMain() { downloaded.push(selection.currentTarget.value); },
  });
  const root = element('root'), app = renderer.createApp({ render: () => h(ResultSection, { ...state.value, resultData: store.resultData, selection: markRaw(selection) }) });
  app.mount(root); await nextTick();
  const findAll = (node, match) => [...(match(node) ? [node] : []), ...node.children.flatMap(child => findAll(child, match))];
  const text = node => [node.text, ...node.children.map(text)].join('');
  const buttons = () => findAll(root, node => node.type === 'button');
  const button = label => buttons().find(node => text(node).includes(label));
  return { app, store, selection, state, switched, copied, downloaded,
    player: () => findAll(root, node => node.type === 'video')[0], alert: () => findAll(root, node => node.props.role === 'alert')[0],
    choices: () => buttons().filter(node => String(node.props.class).split(' ').includes('rs-backup-item')),
    elements: className => findAll(root, node => String(node.props.class).split(' ').includes(className)), textOf: text,
    async click(label) { await button(label).props.onClick(); await nextTick(); },
    async fail(player = this.player()) { player.currentSrc = player.src; player.props.onError({ currentTarget: player }); await nextTick(); },
    text: () => text(root),
  };
}

const muxVariant = (id = 'mux') => ({ id, delivery: { kind: 'mux', output: { container: 'mp4', extension: 'mp4' }, resources: [
  { id: 'video', role: 'video', metadata: { width: 3840, height: 2160, evidence: 'upstream' }, delivery: variant('video-track').delivery },
  { id: 'audio', role: 'audio', delivery: variant('audio-track').delivery },
] } });

test('candidate policy UI: unsupported versions start collapsed and cannot replace a downloadable selection', async () => {
  const file = variant('file', ['primary', 'backup'], { width: 1920, height: 1080 });
  const mux = muxVariant(), dash = { id: 'dash', delivery: { kind: 'dash', scope: 'adaptive', sources: [{ url: 'https://media.example.test/a.mpd' }] } };
  const pending = { id: 'pending', state: 'deferred', reason: { code: 'PENDING', message: '等待后端验证' } };
  const value = item([file, mux, dash, pending], 'file', 'backup'), before = structuredClone(value), view = await mount(data(value));
  try {
    await view.click('画质选项');
    assert.equal(view.choices().length, 1, 'only currently downloadable options belong in the main list');
    const folded = view.elements('rs-other-versions');
    assert.equal(folded.length, 1); assert.equal(folded[0].type, 'details'); assert.ok(!folded[0].props.open);
    assert.equal(view.elements('rs-other-version').length, 3);
    assert.ok(view.elements('rs-other-version').every(row => row.type !== 'button' && !row.props.onClick));
    assert.equal(view.selection.currentSelection.value, null);
    assert.equal(view.selection.currentTarget.value.url, url('backup'));
    assert.deepEqual(value, before, 'ready mux resources and every other variant remain unchanged');
  } finally { view.app.unmount(); }
});

test('candidate policy UI: no complete file has a visible reason outside the collapsed versions', async () => {
  const value = { id: 'video-1', type: 'video', variants: [muxVariant()], default: null, default_unavailable_reason: 'processing_required' };
  const view = await mount(data(value));
  try {
    await view.click('画质选项');
    const notice = view.elements('rs-quality-unavailable');
    assert.equal(notice.length, 1); assert.match(view.textOf(notice[0]), /完整文件|暂不支持/);
    assert.notEqual(notice[0].parent.type, 'details');
    assert.equal(view.choices().length, 0); assert.equal(view.selection.currentTarget.value.kind, 'unsupported');
  } finally { view.app.unmount(); }
});

test('candidate policy UI: higher quality notice reads backend references once without inferring from dimensions', async () => {
  const value = item([variant('file'), muxVariant('higher-a'), muxVariant('higher-b')]);
  const view = await mount(data(value));
  try {
    await view.click('画质选项'); assert.equal(view.elements('rs-higher-quality-note').length, 0);
    value.higher_quality_variants = [{ variant_id: 'higher-a', requires: ['mux'] }, { variant_id: 'higher-b', requires: ['mux'] }];
    view.store.resultData = data({ ...value }); await nextTick();
    assert.equal(view.elements('rs-higher-quality-note').length, 1);
    assert.match(view.textOf(view.elements('rs-higher-quality-note')[0]), /更高画质.*暂不支持/);
    assert.equal(view.selection.currentTarget.value.url, url('file'));
  } finally { view.app.unmount(); }
});

test('candidate policy contract: HDR and alternate language facts require valid enums and evidence', () => {
  const envelope = metadata => ({ schema_version: '1.0', code: 'OK', data: data(item([variant('file', ['file'], metadata)])) });
  for (const metadata of [{ hdr_type: 'hevc' }, { audio_language_role: 'other', audio_language: 'en' }, { audio_language_role: 'alternate' }]) {
    assert.throws(() => contract.validateMediaEnvelope(envelope(metadata)), /媒体合同无效/);
  }
  const missingEvidence = envelope({ hdr_type: 'hdr10' }); delete missingEvidence.data.items[0].variants[0].metadata.evidence;
  assert.throws(() => contract.validateMediaEnvelope(missingEvidence), /证据/);
});

test('candidate policy contract: capability facts validate every metadata layer without rejecting evidenced mux conflicts', () => {
  const mux = muxVariant(), file = variant('file', ['file'], { hdr_type: 'sdr' });
  mux.metadata = { width: 1920, height: 1080, hdr_type: 'hdr10', evidence: 'upstream' };
  mux.delivery.resources[0].metadata.hdr_type = 'hlg';
  mux.delivery.resources[1].metadata = { audio_language: 'en', audio_language_role: 'alternate', audio_channels: 6, evidence: 'upstream' };
  const envelope = { schema_version: '1.0', code: 'OK', data: data(item([file, mux])) }, before = structuredClone(envelope);
  assert.equal(contract.validateMediaEnvelope(envelope), envelope.data);
  assert.deepEqual(envelope, before, 'conflicting facts are retained, not merged into a new default');
  for (const hdr of ['sdr', 'hdr10', 'hdr10_plus', 'hlg', 'dolby_vision', 'hdr_unknown']) {
    file.metadata.hdr_type = hdr; assert.doesNotThrow(() => contract.validateMediaEnvelope(envelope));
  }
  file.metadata = { hdr_type: 'hdr10', fps: 60, evidence: 'mixed', field_evidence: { hdr_type: 'probe' } };
  assert.throws(() => contract.validateMediaEnvelope(envelope), /证据/);
  file.metadata.field_evidence.fps = 'upstream'; assert.doesNotThrow(() => contract.validateMediaEnvelope(envelope));
  mux.delivery.resources[1].metadata.hdr_type = 'hdr10';
  assert.throws(() => contract.validateMediaEnvelope(envelope), /音频资源不能声明 HDR/);
  delete mux.delivery.resources[1].metadata.hdr_type;
  mux.delivery.resources[0].metadata.audio_language = 'en';
  assert.throws(() => contract.validateMediaEnvelope(envelope), /无音频资源/);
});

test('candidate policy UI: failed explicit source stays failed until a user selects an available option', async () => {
  const selected = variant('file', ['old', 'backup']), value = item([selected]), view = await mount(data(value));
  try {
    view.selection.choose({ variant_id: 'file', source_id: 'old' });
    selected.delivery.sources.shift();
    view.store.resultData = data(item([selected])); await nextTick();
    await view.click('画质选项');
    assert.equal(view.selection.currentTarget.value.kind, 'invalid');
    assert.deepEqual(view.selection.currentSelection.value, { variant_id: 'file', source_id: 'old' });
    assert.equal(view.choices().length, 1); assert.ok(!String(view.choices()[0].props.class).includes('rs-backup-item--cur'));
    view.choices()[0].props.onClick(); await nextTick();
    assert.equal(view.selection.currentTarget.value.url, url('backup'));
  } finally { view.app.unmount(); }
});

test('merged identity conflicts retain evidence without changing server or explicit selection', () => {
  const observations = (...values) => values.map(value => ({ value, evidence: 'upstream' }));
  const file = variant('file', ['file']), mux = muxVariant();
  mux.metadata = { field_conflicts: { fps: observations(30, 60), hdr_type: observations('sdr', 'hdr10') } };
  mux.delivery.resources[0].metadata = structuredClone(mux.metadata);
  mux.delivery.resources[1].metadata = { audio_language_role: 'main', evidence: 'upstream', field_conflicts: { audio_language: observations('en', 'fr') } };
  const envelope = { schema_version: '1.0', code: 'OK', data: data(item([file, mux])) }, before = structuredClone(envelope);
  assert.equal(contract.validateMediaEnvelope(envelope), envelope.data);
  assert.deepEqual(envelope, before);
  for (const bad of [
    { fps: observations(60, 60) }, { fps: observations('30', '60') }, { fps: observations(30) },
    { fps: [{ value: 30, evidence: 'unknown' }, { value: 60, evidence: 'upstream' }] },
    { hdr_type: observations('sdr', 'HEVC') }, { private_url: observations('a', 'b') },
    { audio_language: observations('   ', 'en') },
    { fps: [{ value: 30, evidence: 'upstream' }, { value: 30.0, evidence: 'probe' }] },
  ]) {
    const changed = structuredClone(before); changed.data.items[0].variants[1].metadata.field_conflicts = bad;
    assert.throws(() => contract.validateMediaEnvelope(changed), /媒体合同无效/);
  }
  let changed = structuredClone(before); changed.data.items[0].variants[1].metadata.fps = 60;
  assert.throws(() => contract.validateMediaEnvelope(changed), /确定值/);
  changed = structuredClone(before); changed.data.items[0].variants[1].delivery.resources[1].metadata.field_conflicts.fps = observations(30, 60);
  assert.throws(() => contract.validateMediaEnvelope(changed), /音频资源/);
  changed = structuredClone(before); changed.data.items[0].variants[1].delivery.resources[0].metadata.field_conflicts.has_audio = observations(false, true);
  assert.throws(() => contract.validateMediaEnvelope(changed), /无音频资源/);
});

test('candidate policy capabilities: adaptive HLS remains unsupported even inside a live pair', () => {
  const adaptive = { kind: 'hls', scope: 'adaptive', sources: [{ url: 'https://media.example.test/adaptive.m3u8' }] };
  const pair = { id: 'pair', delivery: { kind: 'pair', resources: [
    { id: 'still', role: 'image', delivery: variant('still').delivery }, { id: 'motion', role: 'motion', delivery: adaptive },
  ] } };
  for (const unsupported of [{ id: 'hls', delivery: adaptive }, pair]) {
    const value = { id: 'one', type: 'video', variants: [unsupported], default: null, default_unavailable_reason: 'processing_required' };
    assert.equal(contract.getDownloadTarget(value, { variant_id: unsupported.id }).kind, 'unsupported');
  }
});

test('quality UI retains complete variants and truthful sizes while processing entries remain read-only', async () => {
  const original = variant('original', ['raw'], { width: 2160, height: 3840, bitrate_bps: 52619915, video_bitrate_bps: 52000000, audio_bitrate_bps: 128000 }); original.label = '原画'; original.provenance = 'original_endpoint'; original.delivery.sources[0].size_bytes = 60342212;
  const small = variant('compressed', ['small'], { width: 2160, height: 3840 }); small.delivery.sources[0].size_bytes = 2051199;
  const dash = { id: 'dash', label: '更高画质', delivery: { kind: 'dash', scope: 'adaptive', sources: [{ url: 'https://media.example.test/manifest.mpd' }] } };
  const value = item([original, small, dash], 'original'), snapshot = structuredClone(value), view = await mount(data(value));
  try {
    await view.click('画质选项'); assert.equal(view.choices().length, 2);
    assert.deepEqual(view.elements('rs-backup-name').slice(0, 2).map(view.textOf), ['60.3 MB · 原画', '2.1 MB · 2160p']);
    assert.match(view.text(),/总码率\s+52\.6 Mbps/);assert.match(view.text(),/视频码率\s+52\.0 Mbps/);assert.match(view.text(),/音频码率\s+128 Kbps/);
    assert.equal(view.elements('rs-other-version').length, 1);
    assert.equal(view.selection.currentTarget.value.url, url('raw'));
    assert.equal(view.player().src, url('raw')); assert.match(view.text(), /暂不支持/); assert.deepEqual(value, snapshot);
  } finally { view.app.unmount(); }
});

test('compact quality cards preserve the server source, explicit versions and access to every variant', async () => {
  const raw = variant('raw', ['raw-source'], { width: 2160, height: 3840 });
  raw.provenance = 'original_endpoint'; raw.delivery.sources[0].size_bytes = 60342212;
  const compressed = variant('4k', ['compressed'], { width: 2160, height: 3840 });
  const h264 = variant('1080-h264', ['h264-a', 'h264-b'], { width: 1080, height: 1920, video_codec: 'h264' });
  const hevc = variant('1080-hevc', ['hevc-a', 'hevc-b'], { width: 1080, height: 1920, video_codec: 'hevc' });
  const low = variant('720', ['low'], { width: 720, height: 1280 });
  const dash = { id: 'dash', metadata: { width: 1080, height: 1920 }, delivery: { kind: 'dash', scope: 'adaptive', sources: [{ url: 'https://media.example.test/manifest.mpd' }] } };
  const pending = { id: 'pending', state: 'deferred', reason: { code: 'PENDING_CAPABILITY', message: '原始资源待解析' } };
  const value = item([raw, compressed, h264, hevc, low, dash, pending], '1080-hevc', 'hevc-b');
  const snapshot = structuredClone(value), view = await mount(data(value));
  try {
    await view.click('画质选项');
    assert.equal(view.choices().length, 4, 'only downloadable files participate in compact display cards');
    assert.equal(view.selection.currentSelection.value, null);
    assert.equal(view.selection.currentTarget.value.url, url('hevc-b'));
    assert.match(view.text(), /原始入口/); assert.match(view.text(), /原始资源待解析/); assert.match(view.text(), /暂不支持/);
    assert.equal(view.elements('rs-other-version').length, 2);
    await view.click('全部可下载版本 (5)');
    assert.equal(view.choices().length, 5);
    assert.equal(view.selection.currentTarget.value.url, url('hevc-b'), 'expansion never selects a different source');
    view.choices()[2].props.onClick(); await nextTick();
    assert.deepEqual(view.selection.currentSelection.value, { variant_id: '1080-h264', source_id: 'h264-a' });
    view.selection.choose({ variant_id: '1080-h264', source_id: 'h264-b' }); await nextTick();
    await view.click('精简展示');
    assert.equal(view.choices().length, 4);
    assert.equal(view.selection.currentTarget.value.url, url('h264-b'), 'collapsing keeps the exact explicit route');
    assert.match(view.textOf(view.choices()[2]), /h264/);
    assert.deepEqual(value, snapshot, 'display grouping never mutates or removes domain variants');
  } finally { view.app.unmount(); }
});

test('player fallback stays in the selected variant, stops at three routes and ignores stale DOM events', async () => {
  const view = await mount(data(item([variant('hd', ['a', 'b', 'c', 'd']), variant('lower', ['low'])])));
  try {
    const old = view.player(); await view.fail(old); assert.equal(view.player().src, url('b'));
    await view.fail(old); assert.equal(view.player().src, url('b')); assert.equal(view.switched.length, 1);
    await view.fail(); assert.equal(view.player().src, url('c')); await view.fail(); assert.ok(view.alert());
    assert.equal(view.switched.length, 2); assert.equal(view.player().src, url('c'));
    await view.click('复制链接'); await view.click('下载视频'); assert.equal(view.copied.at(-1), url('c')); assert.equal(view.downloaded.at(-1).url, url('c'));
    await view.click('画质选项'); view.choices()[1].props.onClick(); await nextTick(); assert.equal(view.player().src, url('low'));
    await view.fail(); assert.ok(view.alert()); assert.equal(view.player().src, url('low'));
    view.choices()[0].props.onClick(); await nextTick(); await view.fail(); assert.equal(view.player().src, url('b'), 'manual selection resets only this player retry budget');
  } finally { view.app.unmount(); }
});

test('new result resets playback failures while stale events cannot alter the new item', async () => {
  const view = await mount(data(item([variant('single')])));
  try {
    await view.fail(); assert.ok(view.alert()); const stale = view.player();
    view.store.resultData = data(item([variant('new', ['next', 'next-backup'])])); view.selection.syncResult(); view.state.value = { ...view.state.value, resultVersion: 2 }; await nextTick();
    assert.equal(view.alert(), undefined); await view.fail(stale); assert.equal(view.player().src, url('next'));
    await view.fail(); assert.equal(view.player().src, url('next-backup'));
  } finally { view.app.unmount(); }
});

test('statistics distinguish a verified zero from an unverified count without platform checks', async () => {
  const view = await mount(data(item(), { statistics: { plays: { state: 'unverified' }, likes: { state: 'known', value: 0 } } }));
  try { await view.click('数据'); assert.deepEqual(view.elements('rs-stat-num').map(view.textOf), ['—', '0']); assert.match(view.text(), /未将缺失值当作 0/); }
  finally { view.app.unmount(); }
});

test('credential alternatives stay within the chosen source and precede mirror routes', () => {
  const value = item();
  value.variants[0].delivery.sources[1].fallback_urls = [url('b-renewed'), url('b-other')];
  const target = contract.getDownloadTarget(value, {variant_id:'hd',source_id:'b'});
  assert.equal(target.url, url('b'));
  assert.equal(target.source.id, 'b');
  assert.deepEqual(target.routes.slice(0,3), [url('b'),url('b-renewed'),url('b-other')]);
  for (const fallback of [[], [url('b')], [url('b-renewed'),url('b-renewed')], [url('1'),url('2'),url('3')], ['https://other.test/b.mp4'], ['http://media.example.test/b.mp4'], ['https://user:pass@media.example.test/b.mp4']]) {
    value.variants[0].delivery.sources[1].fallback_urls = fallback;
    assert.equal(contract.getDownloadTarget(value, {variant_id:'hd',source_id:'b'}).kind, 'invalid');
  }
});
