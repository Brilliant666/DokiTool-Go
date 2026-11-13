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

test('#91 PR1 item 5: in-app browser UA detector matches WeChat/QQ/Douyin/Weibo only and keeps SSR/system browsers clean', async () => {
  assert.doesNotMatch(html, /data-in-app-browser-banner|请在系统浏览器中打开/);
  const { renderHome } = await import(new URL('../.ssg/entry-server.mjs', import.meta.url));
  const rendered = await renderHome();
  assert.doesNotMatch(rendered, /data-in-app-browser-banner|请在系统浏览器中打开/);

  for (const [label, ua] of [
    ['WeChat iOS', 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 MicroMessenger/8.0.50(0x18003237) NetType/WIFI'],
    ['WeChat Android', 'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/116.0.0.0 Mobile Safari/537.36 MicroMessenger/8.0.50.2701 WeChat/arm64'],
    ['QQ iOS', 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 QQ/9.0.70.631 V1_IPH_SQ_9.0.70_1_APP_A'],
    ['QQ Android', 'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 MQQBrowser/6.2 TBS/046900 Mobile Safari/537.36 V1_AND_SQ_9.0.70 PA QQ/9.0.70.16805'],
    ['Douyin iOS', 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 aweme_30.2.0 JsSdk/2.0'],
    ['Douyin Android', 'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Mobile Safari/537.36 AppName/aweme app_version/30.2.0 BytedanceWebview/d8a21c6'],
    ['Weibo iOS', 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Weibo (iPhone15,2__weibo__14.6.2__iphone__os17.5)'],
    ['Weibo Android', 'Mozilla/5.0 (Linux; Android 14; 23127PN0CC) AppleWebKit/537.36 Mobile Safari/537.36 Weibo (Xiaomi__weibo__14.6.2__android__android14)'],
  ]) {
    assert.equal(state.isInAppBrowser(ua), true, `${label} must be detected as in-app browser`);
  }

  for (const [label, ua] of [
    ['Desktop Chrome', 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36'],
    ['Android Chrome', 'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36'],
    ['iOS Mobile Safari', 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1'],
    ['Firefox', 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:128.0) Gecko/20100101 Firefox/128.0'],
    ['Edge', 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0'],
    ['Standalone Android MQQBrowser', 'Mozilla/5.0 (Linux; U; Android 13; zh-cn; Pixel 7) AppleWebKit/537.36 Version/4.0 Chrome/109.0.5414.86 MQQBrowser/15.3 Mobile Safari/537.36'],
    ['Desktop QQBrowser', 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/116.0.5845.97 Safari/537.36 QQBrowser/13.5.5653.400'],
    ['Empty/SSR UA', ''],
  ]) {
    assert.equal(state.isInAppBrowser(ua), false, `${label} must NOT be detected as in-app browser`);
  }
});
