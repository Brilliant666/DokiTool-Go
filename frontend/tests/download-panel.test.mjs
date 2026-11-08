import test, { before } from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { transformWithEsbuild } from 'vite';
import { parse, compileScript } from '@vue/compiler-sfc';
import * as vue from 'vue';
import { renderToString } from '@vue/server-renderer';

const { createRenderer, createSSRApp, h, markRaw, nextTick, ref } = vue;
const require = createRequire(import.meta.url);
let DownloadCard;
before(async () => {
  const source = await readFile(new URL('../src/components/DownloadCard.vue', import.meta.url), 'utf8');
  const { descriptor } = parse(source);
  const script = compileScript(descriptor, { id: 'download-panel-test', inlineTemplate: true,
    templateOptions: { compilerOptions: { hoistStatic: false } } });
  const compiled = await transformWithEsbuild(script.content, 'DownloadCard.js', { loader: 'js', format: 'cjs' });
  const componentModule = { exports: {} };
  // Exercise the actual component state/events; CSS transition frames belong to
  // browser acceptance, not this renderer's deterministic interaction checks.
  const transition = { setup: (_, { slots }) => () => slots.default?.() };
  new Function('require', 'module', 'exports', compiled.code)(id => {
    if (id === './AppIcon.vue') return { __esModule: true, default: { render: () => h('i') } };
    if (id === 'vue') return { ...vue, Transition: transition, TransitionGroup: transition };
    return require(id);
  }, componentModule, componentModule.exports);
  DownloadCard = componentModule.exports.default;
});

const task = (id, status = 'downloading', extra = {}) => ({ id, status, filename: `${id}.mp4`,
  show: true, progressKind: 'bytes', loaded: 10, total: 100, percent: 10, ...extra });
const textOf = node => [node.text || '', ...(node.children || []).map(textOf)].join('');
const descendants = (node, predicate) => [...(predicate(node) ? [node] : []), ...(node.children || []).flatMap(child => descendants(child, predicate))];
const visible = node => !node || (![true, 'hidden', ''].includes(node.props?.hidden) && node.style?.display !== 'none' && visible(node.parent));

async function mount(t, downloads = [], { storageBlocked = false, preference } = {}) {
  const saved = { window: Object.getOwnPropertyDescriptor(globalThis, 'window'), document: Object.getOwnPropertyDescriptor(globalThis, 'document') };
  const values = new Map();
  if (preference) values.set('dokitool.download-panel', preference);
  const storage = { getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, String(value)), removeItem: key => values.delete(key) };
  const browser = {};
  for (const key of ['sessionStorage', 'localStorage']) Object.defineProperty(browser, key, { get() {
    if (storageBlocked) throw new DOMException('Storage blocked', 'SecurityError');
    return storage;
  } });
  globalThis.window = browser;
  globalThis.document = { activeElement: null };
  let now = 0, nextTimer = 0;
  const timers = new Map();
  const schedule = (callback, delay = 0) => { timers.set(++nextTimer, { at: now + delay, callback }); return nextTimer; };
  const unschedule = id => timers.delete(id);
  t.mock.method(globalThis, 'setTimeout', schedule);
  t.mock.method(globalThis, 'clearTimeout', unschedule);
  browser.setTimeout = schedule; browser.clearTimeout = unschedule;

  const element = (type, text = '') => markRaw({ type, text, props: {}, children: [], parent: null, style: {},
    contains(node) { for (let current = node; current; current = current.parent) if (current === this) return true; return false; },
    setAttribute(name, value) { this.props[name] = value; }, removeAttribute(name) { delete this.props[name]; },
    getAttribute(name) { return this.props[name]; },
  });
  const detach = node => {
    if (node.parent) node.parent.children = node.parent.children.filter(child => child !== node);
    node.parent = null;
  };
  const renderer = createRenderer({
    createElement: element, createText: text => element('#text', text), createComment: text => element('#comment', text),
    setText(node, text) { node.text = text; }, setElementText(node, text) { node.text = text; node.children = []; },
    parentNode: node => node.parent, nextSibling: node => node.parent?.children[node.parent.children.indexOf(node) + 1] ?? null,
    insert(node, parent, anchor) { detach(node); node.parent = parent; const index = parent.children.indexOf(anchor); parent.children.splice(index < 0 ? parent.children.length : index, 0, node); },
    remove: detach,
    patchProp(node, key, _, value) { node.props[key] = value; if (key === 'style' && value) Object.assign(node.style, value); },
  });
  const events = [];
  const state = ref(downloads);
  const root = element('root');
  const app = renderer.createApp({ render: () => h(DownloadCard, { downloads: state.value,
    onCancel: id => events.push(['cancel', id]), onRetry: id => events.push(['retry', id]),
    onQuery: id => events.push(['query', id]),
    onHandoff: id => events.push(['handoff', id]), onClearCompleted: id => events.push(['clear-completed', id]),
  }) });
  let stopped = false;
  const stop = () => { if (!stopped) { app.unmount(); stopped = true; } };
  t.after(() => {
    stop();
    for (const [key, descriptor] of Object.entries(saved)) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key];
    }
  });
  const flush = async () => { await nextTick(); await nextTick(); };
  const nodes = predicate => descendants(root, node => predicate(node) && visible(node));
  const toggle = () => nodes(node => node.type === 'button' && node.props['aria-controls'] === 'download-panel-list')[0];
  const panel = () => nodes(node => node.props['data-download-panel'] !== undefined)[0];
  const rows = () => nodes(node => node.props['data-download-id'] !== undefined);
  const row = id => rows().find(node => String(node.props['data-download-id']) === String(id));
  const expanded = () => ['true', true].includes(toggle()?.props['aria-expanded']);
  const fire = async (node, event, extra = {}) => {
    assert.ok(node, `missing target for ${event}`);
    const handlers = node.props[event]; assert.ok(handlers, `missing handler ${event}`);
    for (const handler of Array.isArray(handlers) ? handlers : [handlers]) handler({ target: node, currentTarget: node, stopPropagation() {}, preventDefault() {}, ...extra });
    await flush();
  };
  const click = node => fire(node, 'onClick');
  const button = (label, within = root) => descendants(within, node => node.type === 'button' && visible(node)
    && (node.props.title === label || node.props['aria-label'] === label || textOf(node).trim() === label))[0];
  app.mount(root); await flush();
  return { root, state, events, timers, values, stop, toggle, panel, rows, row, expanded, button, click, fire, flush,
    async update(downloads) { state.value = downloads; await flush(); },
    async advance(milliseconds) {
      const end = now + milliseconds;
      while (true) {
        const due = [...timers].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
        if (!due) break;
        now = due[1].at; timers.delete(due[0]); due[1].callback(); await flush();
      }
      now = end; await flush();
    },
  };
}

test('completed stream timing exposes byte intervals and waits without source credentials', async t => {
  const finished = task('one', 'completed', {
    url: 'https://example.test/signed-private-source', workerBuild: 'worker-a',
    timing: { firstBlockMs: 200, windows: [
      { from: 0, to: 1000000, ms: 1000, readMs: 900, demandMs: 99 },
      { from: 1000000, to: 1000000, ms: 2000, readMs: 0, demandMs: 2000 },
    ] },
  })
  const view = await mount(t, [finished, task('legacy', 'completed')], { preference: 'expanded' })
  assert.equal(view.button('分段计时', view.row('legacy')), undefined)
  await view.click(view.button('分段计时', view.row('one')))
  const [{ props: { value: report } }] = descendants(view.row('one'), node => node.type === 'textarea')
  assert.match(report, /Worker worker-a · 传输完成\n首块等待 200 ms/)
  assert.match(report, /0\t1000000\t1000\t1.00\t900\t99\n1000000\t1000000\t2000\t0.00\t0\t2000$/)
  assert.doesNotMatch(report, /signed-private-source/)
})

test('thirteen restored cancellations start as one collapsed summary and expand into all history', async t => {
  const cancelled = Array.from({ length: 13 }, (_, index) => task(`cancel-${index}`, 'cancelled'));
  const view = await mount(t, cancelled);
  assert.equal(view.expanded(), false);
  assert.equal(view.rows().length, 0);
  assert.match(textOf(view.root), /取消\s*13/);
  await view.click(view.toggle());
  assert.equal(view.expanded(), true);
  assert.equal(view.rows().length, 13, 'history remains available rather than being deleted or truncated');
  assert.deepEqual(view.rows().map(node => node.props['data-download-id']), cancelled.map(item => item.id));
  assert.deepEqual(view.events, []);
});

test('expanded tasks put every active or uncertain item before finished history', async t => {
  const tasks = [task('failed', 'failed'), task('active'), task('done', 'completed', { show: false }),
    task('unknown', 'cancelled', { observation: 'unknown' }), task('queued', 'queued')];
  const view = await mount(t, tasks);
  if (!view.expanded()) await view.click(view.toggle());
  const ids = view.rows().map(node => node.props['data-download-id']);
  assert.equal(ids.length, tasks.length, 'completed history remains inspectable even after its old toast show flag expires');
  const activePositions = ['active', 'unknown', 'queued'].map(id => ids.indexOf(id));
  const historyPositions = ['failed', 'done'].map(id => ids.indexOf(id));
  assert.ok(Math.max(...activePositions) < Math.min(...historyPositions));
});

test('the first new download opens the panel, but manual collapse survives progress and later downloads', async t => {
  const view = await mount(t);
  await view.update([task('first')]);
  assert.equal(view.expanded(), true);
  await view.click(view.toggle());
  assert.equal(view.expanded(), false);
  await view.update([task('first', 'downloading', { loaded: 75, percent: 75 })]);
  await view.update([task('first'), task('second', 'queued')]);
  await view.advance(10_000);
  assert.equal(view.expanded(), false);
  assert.deepEqual(view.events, [], 'presentation changes cannot cancel or restart transfers');
});

test('finishing all transfers collapses after five seconds without deleting any history', async t => {
  const view = await mount(t);
  await view.update([task('first')]);
  await view.update([task('first', 'completed')]);
  await view.advance(4_999);
  assert.equal(view.expanded(), true);
  await view.advance(1);
  assert.equal(view.expanded(), false);
  await view.click(view.toggle());
  assert.ok(view.row('first'));
  assert.deepEqual(view.events, []);
});

test('hovering over finished tasks postpones automatic collapse until five seconds after leaving', async t => {
  const view = await mount(t);
  await view.update([task('hover')]);
  await view.update([task('hover', 'completed')]);
  await view.advance(2_000);
  await view.fire(view.panel(), 'onMouseenter');
  await view.advance(20_000);
  assert.equal(view.expanded(), true);
  await view.fire(view.panel(), 'onMouseleave');
  await view.advance(4_999);
  assert.equal(view.expanded(), true);
  await view.advance(1);
  assert.equal(view.expanded(), false);
});

test('keyboard focus inside the panel prevents automatic collapse, including focus moving between its buttons', async t => {
  const view = await mount(t);
  await view.update([task('focus')]);
  await view.update([task('focus', 'completed')]);
  await view.fire(view.panel(), 'onFocusin');
  await view.advance(10_000);
  assert.equal(view.expanded(), true);
  await view.fire(view.panel(), 'onFocusout', { relatedTarget: view.toggle() });
  await view.advance(10_000);
  assert.equal(view.expanded(), true);
  await view.fire(view.panel(), 'onFocusout', { relatedTarget: null });
  await view.advance(5_000);
  assert.equal(view.expanded(), false);
});

test('a new active task cancels the pending automatic collapse without mutating task state', async t => {
  const view = await mount(t);
  await view.update([task('first')]);
  await view.update([task('first', 'completed')]);
  await view.advance(4_000);
  await view.update([task('first', 'completed'), task('new')]);
  await view.advance(10_000);
  assert.equal(view.expanded(), true);
  assert.equal(view.state.value[1].status, 'downloading');
  assert.deepEqual(view.events, []);
});

test('clearing the last viewed history resets hover and focus even when removing the panel emits neither leave nor blur', async t => {
  const view = await mount(t);
  await view.update([task('old')]);
  await view.update([task('old', 'completed')]);
  await view.fire(view.panel(), 'onMouseenter');
  await view.fire(view.panel(), 'onFocusin');
  await view.click(view.button('清除已结束'));
  await view.update([]);
  assert.equal(view.panel(), undefined);
  // Removing a focused/hovered DOM subtree need not send focusout/mouseleave.
  // The next panel must not inherit those interaction flags from the old one.
  await view.update([task('next')]);
  assert.equal(view.expanded(), true);
  await view.update([task('next', 'completed')]);
  await view.advance(4_999);
  assert.equal(view.expanded(), true);
  await view.advance(1);
  assert.equal(view.expanded(), false);
  assert.deepEqual(view.events, [['clear-completed', 'all']]);
});

test('cancel, retry and individual clear emit the original task ID after sorting and folding', async t => {
  const view = await mount(t, [task('old-failure', 'failed'), task(73), task('old-done', 'completed')]);
  if (!view.expanded()) await view.click(view.toggle());
  await view.click(view.button('取消', view.row(73)));
  await view.click(view.button('重试', view.row('old-failure')));
  await view.click(view.button('清除', view.row('old-done')));
  assert.deepEqual(view.events, [['cancel', 73], ['retry', 'old-failure'], ['clear-completed', 'old-done']]);
  await view.click(view.toggle()); await view.click(view.toggle());
  assert.equal(view.events.length, 3, 'fold/unfold is purely presentation');
});

test('finished history can be cleared while live and uncertain tasks remain protected', async t => {
  const view = await mount(t, [task('done', 'completed'), task('live'), task('uncertain', 'cancelled', { observation: 'unknown' })]);
  if (!view.expanded()) await view.click(view.toggle());
  assert.ok(view.button('清除已结束'));
  assert.equal(view.button('清除', view.row('uncertain')), undefined);
  assert.equal(view.button('重试', view.row('uncertain')), undefined);
  assert.ok(view.button('取消', view.row('uncertain')));
  await view.click(view.button('清除已结束'));
  assert.deepEqual(view.events, [['clear-completed', 'all']]);
  await view.update(view.state.value.filter(item => item.id !== 'done'));
  assert.equal(view.button('清除已结束'), undefined);
  assert.equal(view.state.value.find(item => item.id === 'uncertain').observation, 'unknown');
});

test('expanded native handoff and lost-worker history remain honest about completion and retry eligibility', async t => {
  const view = await mount(t, [task('native', 'handed-off', { transport: 'browser_native' }),
    task('lost', 'interrupted', { deliveryId: 'old-worker', url: '', error: '原任务失效，请重新解析' })]);
  await view.click(view.toggle());
  assert.match(textOf(view.row('native')), /本站无法追踪/);
  assert.doesNotMatch(textOf(view.row('native')), /100%|传输完成/);
  assert.match(textOf(view.row('lost')), /原任务失效，请重新解析/);
  assert.equal(view.button('重试', view.row('lost')), undefined);
  assert.equal(view.button('取消', view.row('lost')), undefined);
  await view.update([task('lost', 'interrupted', { deliveryId: 'old-worker', url: 'https://media.test/video.mp4' })]);
  assert.ok(view.button('重试', view.row('lost')));
});

test('cancelled history exposes the reported cause without changing its status or starting another action', async t => {
  const causes = ['页面请求取消下载', '浏览器停止接收下载数据', '浏览器中止下载请求'];
  const view = await mount(t, causes.map((error, index) => task(`cancel-${index}`, 'cancelled', { error })));
  await view.click(view.toggle());
  causes.forEach((cause, index) => {
    const row = view.row(`cancel-${index}`);
    assert.ok(textOf(row).includes(cause));
    assert.match(textOf(row), /已取消/);
    assert.doesNotMatch(textOf(row), /下载失败|传输完成/);
  });
  assert.deepEqual(view.events, []);
  await view.update([task('legacy', 'cancelled')]);
  assert.match(textOf(view.row('legacy')), /已取消/);
  assert.equal(causes.some(cause => textOf(view.row('legacy')).includes(cause)), false);
});

test('unmount clears the automatic-collapse timer', async t => {
  const view = await mount(t);
  await view.update([task('done')]);
  await view.update([task('done', 'completed')]);
  assert.ok(view.timers.size > 0);
  view.stop();
  assert.equal(view.timers.size, 0);
  await view.advance(10_000);
  assert.deepEqual(view.events, []);
});

test('a saved collapsed preference wins over restored active tasks and new arrivals', async t => {
  const view = await mount(t, [task('restored')], { preference: 'collapsed' });
  assert.equal(view.expanded(), false);
  await view.update([task('restored'), task('new')]);
  assert.equal(view.expanded(), false);
  await view.click(view.toggle());
  assert.equal(view.values.get('dokitool.download-panel'), 'expanded');
  await view.click(view.toggle());
  assert.equal(view.values.get('dokitool.download-panel'), 'collapsed');
});

test('automatic collapse does not overwrite a manually expanded preference', async t => {
  const view = await mount(t, [task('active')], { preference: 'expanded' });
  assert.equal(view.expanded(), true);
  await view.update([task('active', 'completed')]);
  await view.advance(5_000);
  assert.equal(view.expanded(), false);
  assert.equal(view.values.get('dokitool.download-panel'), 'expanded');
});

test('blocked browser storage cannot prevent expanding, collapsing or operating a download', async t => {
  const view = await mount(t, [], { storageBlocked: true });
  await view.update([task('blocked')]);
  assert.equal(view.expanded(), true);
  await view.click(view.toggle()); await view.click(view.toggle());
  await view.click(view.button('取消', view.row('blocked')));
  assert.deepEqual(view.events, [['cancel', 'blocked']]);
});

test('server rendering is safe without window, document or browser storage', async t => {
  const saved = ['window', 'document', 'sessionStorage', 'localStorage'].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]);
  for (const [key] of saved) delete globalThis[key];
  t.after(() => { for (const [key, descriptor] of saved) if (descriptor) Object.defineProperty(globalThis, key, descriptor); });
  const html = await renderToString(createSSRApp({ render: () => h(DownloadCard, {
    downloads: Array.from({ length: 13 }, (_, index) => task(index, 'cancelled')),
  }) }));
  assert.match(html, /取消\s*13/);
  assert.match(html, /aria-expanded="false"/);
  assert.match(html, /hidden/);
});

test('expired observation becomes compact history with query only, never an active transfer or retry', async t => {
  const view = await mount(t, [task('uncertain', 'downloading', {
    observation: 'unknown', recoveryExpired: true, deliveryId: 'task-opaque', workerBuild: 'build', workerInstance: 'execution',
    url: 'https://cdn.test/private', nativeAvailable: true,
  })]);
  assert.equal(view.expanded(), false); assert.match(textOf(view.root), /未确认 1/);
  assert.doesNotMatch(textOf(view.root), /个进行中/);
  await view.click(view.toggle());
  const row = view.row('uncertain');
  assert.match(textOf(row), /无法确认下载结果，请查看浏览器下载列表。/);
  assert.equal(view.button('取消', row), undefined); assert.equal(view.button('重试', row), undefined);
  assert.equal(view.button('打开源链接', row), undefined);
  assert.equal(row.props['data-task-id'], 'task-opaque'); assert.equal(row.props['data-worker-instance'], 'execution');
  await view.click(view.button('重新查询状态', row)); assert.deepEqual(view.events, [['query', 'uncertain']]);
});

test('only expired unknown history with a confirmed gone owner can be cleared', async t => {
  const view = await mount(t, [task('lost', 'downloading', { observation: 'unknown', recoveryExpired: true, ownerGone: true, transport: 'browser_streaming', deliveryId: 'old-task' }),
    task('possible', 'downloading', { observation: 'unknown', recoveryExpired: true, transport: 'browser_streaming' }),
    task('recovering', 'downloading', { observation: 'unknown', ownerGone: true, transport: 'browser_streaming' })]);
  if (!view.expanded()) await view.click(view.toggle());
  assert.ok(view.button('清除', view.row('lost')));
  assert.equal(view.button('清除', view.row('possible')), undefined);
  assert.equal(view.button('清除', view.row('recovering')), undefined);
  assert.match(textOf(view.row('lost')), /结果未确认/);
  await view.click(view.button('清除', view.row('lost')));
  assert.deepEqual(view.events, [['clear-completed', 'lost']]);
});
