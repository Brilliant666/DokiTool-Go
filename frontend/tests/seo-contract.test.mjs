import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import sharp from 'sharp';

const homepage = 'https://snap.dokitool.com/';
const html = await readFile(new URL('../index.html', import.meta.url), 'utf8');
const meta = [...html.matchAll(/<meta\s+(?:name|property)="([^"]+)"\s+content="([^"]*)"\s*\/?>/g)];
const metadata = new Map(meta.map(([, name, content]) => [name, content]));

test('canonical and social metadata identify the same real homepage and image', async () => {
  assert.deepEqual([...html.matchAll(/<link\s+rel="canonical"\s+href="([^"]+)"/g)].map(match => match[1]), [homepage]);
  for (const name of ['og:type', 'og:locale', 'og:site_name', 'og:title', 'og:description', 'og:url', 'og:image', 'og:image:alt', 'twitter:card', 'twitter:title', 'twitter:description', 'twitter:image', 'twitter:image:alt']) {
    assert.equal(meta.filter(match => match[1] === name).length, 1, `${name} must appear once`);
    assert.ok(metadata.get(name)?.trim(), `${name} must not be empty`);
  }
  assert.equal(metadata.get('og:url'), homepage);
  assert.equal(metadata.get('og:type'), 'website');
  assert.equal(metadata.get('twitter:card'), 'summary_large_image');
  assert.equal(metadata.get('og:title'), html.match(/<title>(.*?)<\/title>/s)[1]);
  assert.equal(metadata.get('twitter:title'), metadata.get('og:title'));
  assert.equal(metadata.get('twitter:image'), metadata.get('og:image'));
  const imageUrl = new URL(metadata.get('og:image'));
  assert.equal(imageUrl.origin, new URL(homepage).origin);
  assert.equal(imageUrl.search + imageUrl.hash, '');
  const image = await sharp(fileURLToPath(new URL(`../public${imageUrl.pathname}`, import.meta.url))).metadata();
  assert.equal(image.format, 'png');
  assert.equal(image.width, Number(metadata.get('og:image:width')));
  assert.equal(image.height, Number(metadata.get('og:image:height')));
  assert.equal(image.width, 1200);
  assert.equal(image.height, 630);
});

test('structured data declares the application without invented reviews or search endpoints', () => {
  const blocks = [...html.matchAll(/<script\s+type="application\/ld\+json">([\s\S]*?)<\/script>/g)];
  assert.equal(blocks.length, 1);
  const graph = JSON.parse(blocks[0][1]);
  assert.equal(graph['@context'], 'https://schema.org');
  assert.deepEqual(graph['@graph'].map(item => item['@type']).sort(), ['WebApplication', 'WebSite']);
  const website = graph['@graph'].find(item => item['@type'] === 'WebSite');
  const app = graph['@graph'].find(item => item['@type'] === 'WebApplication');
  for (const item of graph['@graph']) {
    assert.equal(item.url, homepage);
    assert.equal(item.name, 'DokiTool');
    assert.equal(item.inLanguage, 'zh-CN');
    assert.ok(item.description.length > 10);
    for (const inventedField of ['aggregateRating', 'review', 'potentialAction']) assert.equal(item[inventedField], undefined);
  }
  assert.equal(app.isPartOf['@id'], website['@id']);
  assert.equal(app.applicationCategory, 'MultimediaApplication');
  assert.equal(app.isAccessibleForFree, true);
  for (const name of ['抖音', '快手', 'B站', '小红书', '今日头条', '微博', '皮皮虾', '皮皮搞笑', '最右', 'X', 'YouTube', 'Instagram', 'Facebook', 'Tumblr', 'Dailymotion']) {
    assert.ok(metadata.get('description')?.includes(name), `meta description must include ${name}`);
    assert.ok(metadata.get('keywords')?.includes(name), `meta keywords must include ${name}`);
    assert.ok(app.description.includes(name), `WebApplication description must include ${name}`);
  }
});

test('crawler files expose only the homepage and keep APIs out of the crawl plan', async () => {
  const robots = await readFile(new URL('../public/robots.txt', import.meta.url), 'utf8');
  assert.match(robots, /^User-agent: \*$/m);
  assert.match(robots, /^Allow: \/$/m);
  assert.match(robots, /^Disallow: \/api\/$/m);
  assert.match(robots, /^Sitemap: https:\/\/snap\.dokitool\.com\/sitemap\.xml$/m);
  assert.doesNotMatch(robots, /^Disallow:\s*\/$/m);
  const sitemap = await readFile(new URL('../public/sitemap.xml', import.meta.url), 'utf8');
  assert.match(sitemap, /<urlset xmlns="http:\/\/www\.sitemaps\.org\/schemas\/sitemap\/0\.9">/);
  assert.deepEqual([...sitemap.matchAll(/<loc>(.*?)<\/loc>/g)].map(match => match[1]), [homepage]);
  assert.doesNotMatch(sitemap, /<lastmod>|<changefreq>|<priority>/);
});

test('Caddy routes crawler and share files directly, before the SPA fallback', async () => {
  const caddy = await readFile(new URL('../../ops/examples/snap.caddy', import.meta.url), 'utf8');
  for (const [path, contentType] of [
    ['/robots.txt', 'text/plain; charset=utf-8'],
    ['/sitemap.xml', 'application/xml; charset=utf-8'],
    ['/social/dokitool-share-v1.png', 'image/png'],
  ]) {
    const escaped = path.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const blocks = [...caddy.matchAll(new RegExp(`handle ${escaped} \\{([^{}]+)\\}`, 'g'))];
    assert.equal(blocks.length, 1, `${path} needs one exact, non-wildcard handle`);
    assert.match(blocks[0][1], /root \* \/srv\/dokitool\/current\/frontend\/dist/);
    assert.match(blocks[0][1], /\bfile_server\b/);
    assert.ok(blocks[0][1].includes(`header Content-Type "${contentType}"`));
    assert.match(blocks[0][1], /header Cache-Control "no-cache"/);
    assert.doesNotMatch(blocks[0][1], /try_files|rewrite|pass_thru|index\.html/);
    assert.ok(blocks[0].index < caddy.indexOf('try_files {path} /index.html'));
  }
});

test('public favicon.ico exists with a valid ICO header and non-empty icon directory', async () => {
  const ico = await readFile(new URL('../public/favicon.ico', import.meta.url));
  assert.ok(ico.byteLength >= 70, 'favicon.ico must contain header, directory entry, and image payload');
  assert.equal(ico.readUInt16LE(0), 0, 'ICO reserved field must be 0');
  assert.equal(ico.readUInt16LE(2), 1, 'ICO type field must be 1');
  assert.ok(ico.readUInt16LE(4) >= 1, 'ICO must declare at least one icon image');
});
