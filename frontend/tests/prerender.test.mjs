import test, { before } from 'node:test';
import assert from 'node:assert/strict';
import { readFile, access } from 'node:fs/promises';
import { createRequire } from 'node:module';
import vm from 'node:vm';
import { transformWithEsbuild } from 'vite';

const require = createRequire(import.meta.url);
let html, state;
before(async () => {
  html = await readFile(new URL('../dist/index.html', import.meta.url), 'utf8');
  const source = await readFile(new URL('../src/ssr/initialState.ts', import.meta.url), 'utf8');
  const code = await transformWithEsbuild(source, 'initialState.ts', { loader: 'ts', format: 'cjs' });
  const module = { exports: {} };
  new Function('require', 'module', 'exports', code.code)(require, module, module.exports);
  state = module.exports;
});

test('built homepage contains the actual content, local visuals and honest controls before JavaScript', async () => {
  assert.match(html, /<div id="app" data-prerendered="true"><div/);
  for (const content of ['轻松解析', '短视频', '支持的平台', '解析教程', 'DokiTool']) assert.ok(html.includes(content));
  assert.match(html, /<input[^>]*data-initial-url[^>]*>/);
  const input = html.match(/<input[^>]*data-initial-url[^>]*>/)[0];
  assert.doesNotMatch(input, /\sdisabled(?:[\s=>])/);
  assert.match(input, /data-allow-mismatch="attribute"/);
  assert.match(html, /<button[^>]*disabled[^>]*>[\s\S]*?正在准备/);
  assert.deepEqual(
    [...html.matchAll(/data-platform-tab="([^"]+)"/g)].map(match => match[1]),
    ['all', 'douyin', 'kuaishou', 'bilibili', 'xiaohongshu', 'weibo', 'x', 'youtube', 'instagram', 'facebook'],
  );
  assert.ok(!html.includes('更多平台'));
  assert.doesNotMatch(html, /data-more-platforms-toggle|data-more-platform-tab/);
  const supportedSection = html.slice(html.indexOf('id="Supported_Platforms"'));
  for (const name of ['抖音', '快手', 'B站', '小红书', '今日头条', '微博', '皮皮虾', '皮皮搞笑', '最右', 'X', 'YouTube', 'Instagram', 'Facebook', 'Tumblr', 'Dailymotion']) {
    assert.ok(supportedSection.includes(name), `prerendered Supported_Platforms must include ${name}`);
  }
  assert.match(html, /<noscript>[\s\S]*?解析和下载需要 JavaScript/);
  const logos = [...html.matchAll(/<img[^>]*alt="DokiTool"[^>]*>/g)].map(match => match[0]);
  assert.ok(logos.length >= 1);
  for (const logo of logos) assert.doesNotMatch(logo, /opacity-0|visibility:\s*hidden|display:\s*none/);
  const resources = new Set([...html.matchAll(/(?:src|href)="(\/assets\/[^"?#]+)"/g)].map(match => match[1]));
  assert.ok(resources.size >= 10);
  for (const resource of resources) await access(new URL(`../dist${resource}`, import.meta.url));
  assert.ok(html.indexOf('dokitool-theme-bootstrap') < html.indexOf('rel="stylesheet"'));
});

test('hydration starts with the emitted language while preserving pre-hydration live input', () => {
  const typed = '  https://example.com/video?text=用户提前填写  ';
  const root = { dataset: { prerendered: 'true' }, querySelector: () => ({ value: typed, getAttribute: () => '' }) };
  const initial = state.getClientInitialState(root);
  assert.deepEqual(initial, { prerendered: true, locale: 'zh-CN', isDark: false, inputUrl: typed });
  assert.equal(root.querySelector().value, typed, 'capture must not reset the live input');
  assert.equal(state.getClientInitialState({ ...root, querySelector: () => null }).inputUrl, '');
});

test('the real server-rendered homepage has no body teleports to hydrate', async () => {
  const { renderHome } = await import(new URL('../.ssg/entry-server.mjs', import.meta.url));
  const rendered = await renderHome();
  // Even a closed modal emits teleport anchors when its Teleport is mounted.
  // Without matching SSR target markup, hydrating it against body can consume
  // the application root. All body overlays must first mount on the client.
  assert.doesNotMatch(rendered, /<!--teleport (?:start|end|anchor)/);
  assert.doesNotMatch(html, /<!--teleport (?:start|end|anchor)/);
  assert.match(rendered, /data-initial-url/);
});

test('early theme code honors stored preference and works when storage is blocked', () => {
  const bootstrap = html.match(/<script id="dokitool-theme-bootstrap">([\s\S]*?)<\/script>/)?.[1];
  assert.ok(bootstrap);
  for (const { stored, systemDark, denied, expected } of [
    { stored: 'dark', systemDark: false, expected: true },
    { stored: 'light', systemDark: true, expected: false },
    { stored: null, systemDark: true, expected: true },
    { denied: true, systemDark: false, expected: false },
  ]) {
    const changes = [];
    const element = { classList: { toggle: (...args) => changes.push(args) }, style: {} };
    vm.runInNewContext(bootstrap, {
      localStorage: { getItem: () => { if (denied) throw new Error('blocked'); return stored; } },
      window: { matchMedia: () => ({ matches: systemDark }) },
      document: { documentElement: element },
    });
    assert.deepEqual(changes, [['dark', expected]]);
    assert.equal(element.style.colorScheme, expected ? 'dark' : 'light');
  }
});
