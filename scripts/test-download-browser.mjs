// Real Chromium, two loopback origins, production download worker. Node 22.15+.
// 1. node scripts/test-download-browser.mjs serve <empty-run-dir>
// 2. Start a separate Chromium: --headless=new --remote-debugging-port=0
//    --user-data-dir=<new-test-profile>. Never reuse a personal profile.
// 3. node scripts/test-download-browser.mjs check <run-dir> <new-test-profile>
// Optional trailing case name runs a single fixture for regression diagnosis.
import assert from 'node:assert/strict'
import { createHash, randomUUID } from 'node:crypto'
import { mkdir, readFile, readdir, writeFile } from 'node:fs/promises'
import { createServer } from 'node:http'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { crc32, deflateSync, gzipSync } from 'node:zlib'
import { connect, until } from './download-browser-session.mjs'

const [command, directory, profile, only] = process.argv.slice(2)
const workerRoot = new URL('../frontend/public/downloads/', import.meta.url)
const fixtures = new URL('../frontend/tests/fixtures/download/', import.meta.url)
const runDir = path.resolve(directory)
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex')

// A real PNG with uncompressed scanlines makes encoded and decoded lengths
// very different. The MOV and MP4 fixtures each contain one 16x16 blue frame.
function pngChunk(name, data) {
  const payload = Buffer.concat([Buffer.from(name), data])
  const chunk = Buffer.alloc(payload.length + 8)
  chunk.writeUInt32BE(data.length)
  payload.copy(chunk, 4)
  chunk.writeUInt32BE(crc32(payload), payload.length + 4)
  return chunk
}
const ihdr = Buffer.from('00000100000001000802000000', 'hex')
const png = Buffer.concat([Buffer.from('89504e470d0a1a0a', 'hex'), pngChunk('IHDR', ihdr),
  pngChunk('IDAT', deflateSync(Buffer.alloc(256 * (1 + 256 * 3)), { level: 0 })), pngChunk('IEND', Buffer.alloc(0))])
const media = { png, mov: await readFile(new URL('one-frame.mov', fixtures)), mp4: await readFile(new URL('one-frame.mp4', fixtures)) }
const cases = [
  { name: 'gzip-hidden', gzip: true },
  { name: 'gzip-visible', gzip: true, expose: true },
  { name: 'identity-visible', identity: true, expose: true, known: true },
  { name: 'plain-cors' },
  { name: 'plain-basic', sameOrigin: true, known: true },
  { name: 'chunked', chunked: true },
  { name: 'truncated', broken: true, status: 'failed' },
  { name: 'overlong', sizeAdjustment: -1, chunked: true, status: 'failed' },
  { name: 'short-at-eof', sizeAdjustment: 1, chunked: true, status: 'failed' },
  { name: 'cancel', slow: true, status: 'cancelled' },
  { name: 'mov-conflict', file: 'mov', declared: 'video/mp4', format: 'video/quicktime' },
  { name: 'mp4-conflict', file: 'mp4', declared: 'video/quicktime', format: 'video/mp4' },
  { name: 'png-conflict', declared: 'image/jpeg', format: 'image/png' },
  { name: 'mov-no-mime', file: 'mov', declared: null, format: 'video/quicktime' },
  { name: 'media-family-conflict', file: 'mov', declared: 'image/jpeg', format: 'video/quicktime', status: 'failed' },
  { name: 'mime-fallback', file: 'mp4', declared: 'video/mp4', inputExt: 'mov' },
  { name: 'unsafe-no-mime', declared: null, inputExt: 'exe', outputExt: 'bin' },
]
const mimeFor = type => ({ png: 'image/png', mov: 'video/quicktime', mp4: 'video/mp4' })[type]
function specimen(test) {
  const file = test.file || 'png'
  const body = media[file]
  const wire = test.gzip ? gzipSync(body) : body
  const headers = { 'Access-Control-Allow-Origin': '*', 'Cache-Control': 'no-store' }
  const declared = test.declared === undefined ? mimeFor(file) : test.declared
  if (declared) headers['Content-Type'] = declared
  if (!test.chunked) headers['Content-Length'] = String(wire.length)
  if (test.gzip || test.identity) headers['Content-Encoding'] = test.gzip ? 'gzip' : 'identity'
  if (test.expose) headers['Access-Control-Expose-Headers'] = 'Content-Encoding'
  return { file, body, wire, headers }
}

async function serve() {
  await mkdir(runDir, { recursive: true })
  const listen = async handler => {
    const server = createServer(handler)
    await new Promise((resolve, reject) => server.once('error', reject).listen(0, '127.0.0.1', resolve))
    return 'http://127.0.0.1:' + server.address().port
  }
  function sendMedia(req, res) {
    const test = command === 'serve-ui' && req.url === '/fixture/claim-video'
      ? {name:'claim-video', file:'mp4', slow:true}
      : cases.find(test => req.url === '/fixture/' + test.name)
    if (!test) { res.writeHead(404).end(); return }
    const { wire, headers } = specimen(test)
    res.writeHead(200, headers)
    if (test.broken || test.slow || test.sizeAdjustment === -1) {
      const first = Math.min(4096, Math.ceil(wire.length / 2))
      res.write(wire.subarray(0, first))
      const end = setTimeout(() => test.broken ? res.destroy() : res.end(wire.subarray(first)), test.slow ? 6000 : 250)
      res.on('close', () => clearTimeout(end))
    } else if (test.chunked) {
      res.write(wire.subarray(0, 4096))
      res.end(wire.subarray(4096))
    } else res.end(wire)
  }
  const cdn = await listen(sendMedia)
  const app = await listen(async (req, res) => {
    if (command === 'serve-ui') {
      const pathname = new URL(req.url, 'http://localhost').pathname;
      if (pathname === '/api/parse') {
        const mediaUrl = cdn + '/fixture/claim-video';
        const item = { id: 'video', type: 'video', variants: [{ id: 'mp4', metadata: { extension: 'mp4', mime_type: 'video/mp4', evidence: 'upstream' }, delivery: { kind: 'file', sources: [{ url: mediaUrl, size_bytes: media.mp4.length }] } }], default: { variant_id: 'mp4', url: mediaUrl } };
        return res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ schema_version: '1.0', code: 'OK', data: { platform: 'douyin', title: 'Local claim check', items: [item] } }));
      }
      if (pathname === '/api/platforms') return res.writeHead(200, { 'Content-Type': 'application/json' })
        .end(await readFile(new URL('../frontend/src/generated/platform-catalog.json', import.meta.url)));
      const root = path.resolve(profile);
      const file = path.resolve(root, '.' + (pathname === '/' ? '/index.html' : pathname));
      if (!file.startsWith(root + path.sep)) return res.writeHead(404).end();
      const type = { '.html': 'text/html; charset=utf-8', '.js': 'application/javascript', '.css': 'text/css', '.png': 'image/png', '.svg': 'image/svg+xml', '.woff2': 'font/woff2' }[path.extname(file)] || 'application/octet-stream';
      try { const body = await readFile(file); return res.writeHead(200, { 'Content-Type': type, 'Cache-Control': 'no-store' }).end(body) }
      catch (error) { if (error.code === 'ENOENT') return res.writeHead(404).end(); throw error }
    }
    if (req.url?.startsWith('/fixture/')) return sendMedia(req, res)
    if (req.url === '/downloads/check') return res.writeHead(200, { 'Content-Type': 'text/html' }).end('<!doctype html><title>Download test</title>')
    if (/^\/downloads\/[a-z][a-z-]*\.js$/.test(req.url)) {
      const source = await readFile(new URL(path.basename(req.url), workerRoot))
      return res.writeHead(200, { 'Content-Type': 'text/javascript', 'Cache-Control': 'no-store' }).end(source)
    }
    res.writeHead(404).end()
  })
  await writeFile(path.join(runDir, 'servers.json'), JSON.stringify({ app, cdn, ...(command === 'serve-ui' ? { claimFixture: { mime:'video/mp4', bytes: media.mp4.length, sha256: sha256(media.mp4) } } : {}) }))
  console.log('Loopback fixtures ready', app, cdn)
}

// Observe the native response at the SW fetch boundary and the synthetic
// attachment headers. Do not substitute a Response, read the stream, or
// change any request, header, chunk or Content-Disposition.
function observe() {
  self.observations = {}
  const originalFetch = self.fetch
  self.fetch = async function (...args) {
    const response = await originalFetch.apply(this, args)
    self.observations.source = { type: response.type, status: response.status, headers: Object.fromEntries(response.headers) }
    return response
  }
  const respondWith = FetchEvent.prototype.respondWith
  FetchEvent.prototype.respondWith = function (promise) {
    Promise.resolve(promise).then(response => { self.observations.attachment = Object.fromEntries(response.headers) })
    return respondWith.call(this, promise)
  }
}
function client() {
  window.rpc = data => new Promise(resolve => {
    const channel = new MessageChannel()
    channel.port1.onmessage = message => { channel.port1.close(); resolve(message.data) }
    navigator.serviceWorker.controller.postMessage({ protocol: 1, ...data }, [channel.port2])
  })
  window.claim = url => {
    const frame = Object.assign(document.createElement('iframe'), { src: url, hidden: true, referrerPolicy: 'no-referrer' })
    document.body.append(frame)
  }
}

async function check() {
  const { app, cdn } = JSON.parse(await readFile(path.join(runDir, 'servers.json')))
  const build = (await readFile(new URL('worker.js', workerRoot), 'utf8')).match(/const BUILD = "([^"]+)"/)[1]
  const selected = cases.filter(test => !only || test.name === only)
  assert.ok(selected.length, 'unknown fixture')
  const downloads = path.join(runDir, 'saved')
  await mkdir(downloads, { recursive: true })
  assert.equal((await readdir(downloads)).length, 0, 'use an empty download directory')
  const browser = await connect(profile)
  const { send } = browser
  const report = { browser: await send('Browser.getVersion'), build, cases: [] }
  try {
    await send('Browser.setDownloadBehavior', { behavior: 'allow', downloadPath: downloads })
    const { targetId } = await send('Target.createTarget', { url: app + '/downloads/check' })
    const attach = async id => (await send('Target.attachToTarget', { targetId: id, flatten: true })).sessionId
    const page = await attach(targetId)
    async function evaluate(session, expression) {
      const reply = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true, userGesture: true }, session)
      if (reply.exceptionDetails) throw new Error(reply.exceptionDetails.exception?.description || reply.exceptionDetails.text)
      return reply.result.value
    }
    await until(() => evaluate(page, 'location.href === ' + JSON.stringify(app + '/downloads/check')), 'initial page')
    await evaluate(page, `(async () => {await navigator.serviceWorker.register('/downloads/worker.js',{type:'module'}); await navigator.serviceWorker.ready; return true})()`)
    await send('Page.reload', {}, page)
    await until(() => evaluate(page, '!!navigator.serviceWorker?.controller').catch(() => false), 'controlled page')
    await evaluate(page, '(' + client + ')()')
    const workerTarget = (await send('Target.getTargets')).targetInfos.find(target => target.type === 'service_worker' && target.url === app + '/downloads/worker.js')
    assert.ok(workerTarget, 'active download worker target')
    const worker = await attach(workerTarget.targetId)
    await evaluate(worker, '(' + observe + ')()')
    const rpc = data => evaluate(page, 'rpc(' + JSON.stringify(data) + ')')
    const ping = await rpc({ action: 'ping' })
    assert.equal(ping.workerBuild, build)
    report.worker = ping

    for (const test of selected) {
      const { file, body, headers } = specimen(test)
      const url = (test.sameOrigin ? app : cdn) + '/fixture/' + test.name
      const id = randomUUID(), token = randomUUID()
      const request = { id, url, filename: test.name + '.' + (test.inputExt || file) }
      if (test.format) request.formatMime = test.format
      if (test.sizeAdjustment !== undefined) request.sourceSizes = { [url]: body.length + test.sizeAdjustment }
      await evaluate(worker, 'self.observations = {}')
      const start = await rpc({ action: 'start', token, request, admission: true })
      assert.equal(start.snapshot.delivery, 'ready')
      await evaluate(page, 'claim(' + JSON.stringify('/downloads/file/' + id + '/' + token) + ')')
      const query = async () => (await rpc({ action: 'query', tasks: [{ id, token }] })).snapshots[0]
      if (test.slow) {
        await until(async () => (await query()).status === 'downloading', 'active transfer to cancel')
        await rpc({ action: 'cancel', id, token })
      }
      const row = { name: test.name, wire: headers }
      report.cases.push(row)
      const terminal = await until(async () => {
        const snapshot = row.lastObserved = await query()
        return ['completed', 'failed', 'cancelled'].includes(snapshot?.status) && snapshot
      }, test.name + ' terminal')
      const observed = await evaluate(worker, 'self.observations')
      delete row.lastObserved
      Object.assign(row, { source: observed.source, attachment: observed.attachment, terminal })
      assert.equal(observed.source.status, 200)
      assert.equal(observed.source.type, test.sameOrigin ? 'basic' : 'cors')
      assert.equal(observed.source.headers['content-encoding'], test.expose ? headers['Content-Encoding'] : undefined)
      assert.equal(terminal.status, test.status || 'completed', test.name + ': ' + (terminal.error || terminal.status))
      assert.equal(terminal.workerBuild, build)
      if (test.status) {
        if (test.name === 'truncated') {
          assert.ok(terminal.loaded < body.length)
          assert.match(terminal.error, /network|fetch|stream|load|connection|连接|网络/i)
        } else if (test.name === 'overlong') {
          assert.ok(terminal.loaded > 0 && terminal.loaded < body.length)
          assert.match(terminal.error, /长度与来源声明不一致/)
        } else if (test.name === 'short-at-eof') {
          assert.equal(terminal.loaded, body.length)
          assert.match(terminal.error, /下载不完整/)
        } else if (test.name === 'media-family-conflict') {
          assert.match(terminal.error, /媒体类型与已确认的文件格式不一致/)
        }
        continue
      }
      assert.equal(terminal.loaded, body.length)
      assert.equal(terminal.total, test.known ? body.length : 0, test.name + ' trusted total')
      assert.equal(observed.attachment['content-length'], test.known ? String(body.length) : undefined)
      const filename = test.name + '.' + (test.outputExt || file)
      assert.equal(terminal.filename, filename)
      assert.ok(observed.attachment['content-disposition'].includes("filename*=UTF-8''" + filename))
      const expectedMime = test.format || headers['Content-Type'] || 'application/octet-stream'
      assert.equal(observed.attachment['content-type'], expectedMime)
      const saved = await until(async () => {
        const data = await readFile(path.join(downloads, filename)).catch(() => null)
        return data?.length === body.length && data
      }, 'browser-named on-disk file ' + filename)
      assert.equal(sha256(saved), sha256(body))
      row.file = { filename, bytes: saved.length, sha256: sha256(saved) }
    }
    report.passed = true
  } finally {
    await writeFile(path.join(runDir, 'browser-results.json'), JSON.stringify(report, null, 2) + '\n')
    browser.close()
  }
  console.log('Browser download fixtures passed:', report.cases.length, report.build)
}

async function checkNative() {
  await mkdir(runDir, { recursive: true })
  const downloads = path.join(runDir, 'saved-native')
  await mkdir(downloads, { recursive: true })
  assert.equal((await readdir(downloads)).length, 0, 'use an empty download directory')
  const distRoot = path.resolve(only || new URL('../frontend/dist', import.meta.url).pathname)
  const received = []
  let cdnPort = 0

  const cdnServer = createServer((req, res) => {
    const u = new URL(req.url, `http://127.0.0.1:${cdnPort}`)
    if (u.pathname === '/favicon.ico') return res.writeHead(204).end()
    const id = u.searchParams.get('id') || ''
    received.push({
      id,
      path: u.pathname,
      query: u.search,
      referer: req.headers.referer || null,
      range: req.headers.range || null,
    })
    if (id === 'attachment') {
      res.writeHead(200, {
        'Content-Type': 'video/mp4',
        'Content-Disposition': 'attachment; filename="r1-direct.mp4"',
        'Content-Length': String(media.mp4.length),
        'Cache-Control': 'no-store',
      })
      res.end(media.mp4)
    } else if (id === 'redirect-attachment') {
      res.writeHead(302, { Location: `/videoplayback?id=redirect-attachment-hop2&itag=18&source=youtube&sparams=id,itag,source&title=R1_redirect-attachment` })
      res.end()
    } else if (id === 'redirect-attachment-hop2') {
      res.writeHead(200, {
        'Content-Type': 'video/mp4',
        'Content-Disposition': 'attachment; filename="r1-redirected.mp4"',
        'Content-Length': String(media.mp4.length),
        'Cache-Control': 'no-store',
      })
      res.end(media.mp4)
    } else if (id === 'redirect-403') {
      res.writeHead(302, { Location: `/videoplayback?id=redirect-403-hop2&itag=18` })
      res.end()
    } else if (id === 'redirect-403-hop2') {
      res.writeHead(403, { 'Content-Type': 'text/plain; charset=utf-8', 'Cache-Control': 'no-store' })
      res.end('403 Forbidden (simulated cross-network rejection)')
    } else if (id === 'inline') {
      res.writeHead(200, {
        'Content-Type': 'video/mp4',
        'Content-Disposition': 'inline',
        'Content-Length': String(media.mp4.length),
        'Cache-Control': 'no-store',
      })
      res.end(media.mp4)
    } else if (id === 'network-failure') {
      req.socket.destroy()
    } else if (id === 'popup-restricted') {
      res.writeHead(200, {
        'Content-Type': 'video/mp4',
        'Content-Disposition': 'attachment; filename="r1-popup-recovered.mp4"',
        'Content-Length': String(media.mp4.length),
        'Cache-Control': 'no-store',
      })
      res.end(media.mp4)
    } else {
      res.writeHead(404).end()
    }
  })
  await new Promise((resolve, reject) => cdnServer.once('error', reject).listen(0, '127.0.0.1', resolve))
  cdnPort = cdnServer.address().port
  const cdnHost = `http://rr2---sn-a5mekn6s.googlevideo.com:${cdnPort}`

  const appServer = createServer(async (req, res) => {
    const u = new URL(req.url, 'http://localhost')
    if (u.pathname === '/api/parse') {
      const inputUrl = u.searchParams.get('url') || ''
      const caseMatch = /r1_([a-z0-9-]+)/i.exec(inputUrl)
      const caseName = caseMatch ? caseMatch[1] : 'attachment'
      const primaryUrl = `${cdnHost}/videoplayback?id=${caseName}&itag=18&source=youtube&sparams=id%2Citag%2Csource&title=R1_${caseName}`
      const silent4kUrl = `${cdnHost}/videoplayback?id=${caseName}-4k&itag=401&source=youtube&sparams=id%2Citag%2Csource`
      const item = {
        id: `video-${caseName}`,
        type: 'video',
        state: 'ready',
        title: `Title_${caseName}`,
        default: { variant_id: 'v-360p', source_id: `s-360p-${caseName}`, url: primaryUrl },
        variants: [
          {
            id: 'v-4k-silent',
            label: '2160p60 (4K · 仅视频无声)',
            metadata: { width: 3840, height: 2160, has_audio: false, container: 'mp4', extension: 'mp4', mime_type: 'video/mp4' },
            delivery: { kind: 'file', sources: [{ id: `s-4k-${caseName}`, url: silent4kUrl, size_bytes: media.mp4.length }] },
          },
          {
            id: 'v-360p',
            label: '360p (含音频)',
            metadata: { width: 640, height: 360, has_audio: true, container: 'mp4', extension: 'mp4', mime_type: 'video/mp4' },
            delivery: { kind: 'file', sources: [{ id: `s-360p-${caseName}`, url: primaryUrl, size_bytes: media.mp4.length }] },
          },
        ],
      }
      return res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({
        schema_version: '1.0',
        code: 'OK',
        data: { platform: 'youtube', title: `Title_${caseName}`, source_url: inputUrl, default_item_id: item.id, items: [item] },
      }))
    }
    if (u.pathname === '/api/platforms') {
      return res.writeHead(200, { 'Content-Type': 'application/json' })
        .end(await readFile(new URL('../frontend/src/generated/platform-catalog.json', import.meta.url)))
    }
    const file = path.resolve(distRoot, '.' + (u.pathname === '/' ? '/index.html' : u.pathname))
    if (!file.startsWith(distRoot + path.sep)) return res.writeHead(404).end()
    const type = { '.html': 'text/html; charset=utf-8', '.js': 'application/javascript', '.css': 'text/css', '.png': 'image/png', '.svg': 'image/svg+xml', '.woff2': 'font/woff2', '.ico': 'image/x-icon' }[path.extname(file)] || 'application/octet-stream'
    try {
      const body = await readFile(file)
      return res.writeHead(200, { 'Content-Type': type, 'Cache-Control': 'no-store' }).end(body)
    } catch (error) {
      if (error.code === 'ENOENT') return res.writeHead(404).end()
      throw error
    }
  })
  await new Promise((resolve, reject) => appServer.once('error', reject).listen(0, '127.0.0.1', resolve))
  const appOrigin = `http://127.0.0.1:${appServer.address().port}`

  const browser = await connect(profile)
  const { send } = browser
  const report = { browser: await send('Browser.getVersion'), appOrigin, cdnHost, cases: [] }
  try {
    await send('Browser.setDownloadBehavior', { behavior: 'allow', downloadPath: downloads })
    const { targetId } = await send('Target.createTarget', { url: appOrigin + '/' })
    const attach = async id => (await send('Target.attachToTarget', { targetId: id, flatten: true })).sessionId
    const page = await attach(targetId)
    async function evaluate(session, expression, userGesture = false) {
      const reply = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true, userGesture }, session)
      if (reply.exceptionDetails) throw new Error(reply.exceptionDetails.exception?.description || reply.exceptionDetails.text)
      return reply.result.value
    }

    await until(() => evaluate(page, "!!document.querySelector('input[data-initial-url]')"), 'initial SPA form')

    const readPageState = () => evaluate(page, `(() => ({
      href: location.href,
      title: document.querySelector('.rs-info-val')?.textContent?.trim() || '',
      activeQuality: document.querySelector('.rs-quality-tag')?.textContent?.trim() || '',
      videoSrc: document.querySelector('video.rs-video')?.getAttribute('src') || '',
      tasks: [...document.querySelectorAll('[data-download-state]')].map(el => ({
        id: el.dataset.downloadId,
        state: el.dataset.downloadState,
        transport: el.dataset.downloadTransport || null,
        hasManualLink: [...el.querySelectorAll('button')].some(b => b.textContent.includes('打开源链接')),
      })),
    }))()`, false)

    const nativeCases = [
      { name: 'popup-restricted', file: 'r1-popup-recovered.mp4', restrictFirst: true },
      { name: 'attachment', file: 'r1-direct.mp4' },
      { name: 'redirect-attachment', file: 'r1-redirected.mp4' },
      { name: 'redirect-403', inspectNewTab: true },
      { name: 'inline', inspectNewTab: true },
      { name: 'network-failure' },
    ]

    for (const tc of nativeCases) {
      await evaluate(page, `(() => {
        const clearBtn = [...document.querySelectorAll('[data-download-panel] button')].find(b => /清空|清除/.test(b.textContent));
        if (clearBtn) clearBtn.click();
        const input = document.querySelector('input[data-initial-url]');
        input.value = ${JSON.stringify('https://www.youtube.com/watch?v=r1_' + tc.name)};
        input.dispatchEvent(new Event('input', { bubbles: true }));
      })()`, false)
      await delay(50)
      await evaluate(page, "document.querySelector('input[data-initial-url]').parentElement.querySelector('button').click()", false)
      await until(async () => {
        const st = await readPageState()
        return st.title === `Title_${tc.name}` && st.videoSrc.includes(`id=${tc.name}`) && st
      }, `parsed ${tc.name}`)

      const before = await readPageState()
      const beforeReqCount = received.length

      if (tc.restrictFirst) {
        // Trigger primary download WITHOUT transient user activation (popup restricted)
        await evaluate(page, `(() => {
          const btn = [...document.querySelectorAll('button.rs-btn--primary')].find(x => /Download Video|下载视频/i.test(x.innerText));
          if (!btn) throw new Error('missing primary download button');
          btn.click();
        })()`, false)
        await delay(300)
        const mid = await readPageState()
        assert.equal(mid.href, before.href, 'popup-restricted attempt must not navigate current page')
        assert.equal(mid.title, before.title, 'popup-restricted attempt must preserve parsed result')
        assert.equal(mid.activeQuality, before.activeQuality, 'popup-restricted attempt must preserve active quality label')
        assert.equal(mid.videoSrc, before.videoSrc, 'popup-restricted attempt must preserve selected source')
        const midNavReqs = received.slice(beforeReqCount).filter(r => !r.range)
        assert.equal(midNavReqs.length, 0, 'popup-restricted attempt must not issue any document navigation request')
        const manualTask = mid.tasks.find(t => t.state === 'manual' && t.hasManualLink)
        assert.ok(manualTask, 'popup-restricted attempt must settle in manual state with explicit 打开源链接 button')

        // Now click explicit 打开源链接 button WITH user gesture
        await evaluate(page, `(() => {
          const btn = [...document.querySelectorAll('[data-download-state="manual"] button')].find(b => b.textContent.includes('打开源链接'));
          if (!btn) throw new Error('missing 打开源链接 button');
          btn.click();
        })()`, true)
      } else {
        await evaluate(page, `(() => {
          const btn = [...document.querySelectorAll('button.rs-btn--primary')].find(x => /Download Video|下载视频/i.test(x.innerText));
          if (!btn) throw new Error('missing primary download button');
          btn.click();
        })()`, true)
      }

      await delay(500)
      const after = await readPageState()
      assert.equal(after.href, before.href, `${tc.name}: current DokiTool page URL must be preserved`)
      assert.equal(after.title, before.title, `${tc.name}: parsed result title must be preserved`)
      assert.equal(after.activeQuality, before.activeQuality, `${tc.name}: active quality selection must be preserved`)
      assert.equal(after.videoSrc, before.videoSrc, `${tc.name}: selected variant/source must be preserved`)
      assert.ok(after.tasks.some(t => t.state === 'handed-off'), `${tc.name}: download card state must be handed-off`)

      const caseReqs = received.slice(beforeReqCount)
      const navReqs = caseReqs.filter(r => !r.range)
      assert.ok(navReqs.length >= 1, `${tc.name}: expected CDN document navigation request`)
      for (const r of navReqs) {
        assert.equal(r.referer, null, `${tc.name}: navigation Referer header must be omitted (no-referrer)`)
      }
      for (const r of caseReqs) {
        assert.equal(Boolean(r.referer && r.referer.includes(appOrigin)), false, `${tc.name}: Referer must never leak DokiTool origin`)
      }

      let isolatedOpenerNull = null
      if (tc.inspectNewTab) {
        const targets = (await send('Target.getTargets')).targetInfos
        const aux = targets.find(t => t.type === 'page' && t.targetId !== targetId && t.url.includes('googlevideo.com'))
        assert.ok(aux, `${tc.name}: expected isolated auxiliary tab for non-attachment response`)
        const auxSession = await attach(aux.targetId)
        isolatedOpenerNull = await evaluate(auxSession, 'window.opener === null', false)
        assert.equal(isolatedOpenerNull, true, `${tc.name}: auxiliary tab must have window.opener === null`)
        await send('Target.closeTarget', { targetId: aux.targetId })
      }

      let savedFile = null
      if (tc.file) {
        const data = await until(async () => {
          const bytes = await readFile(path.join(downloads, tc.file)).catch(() => null)
          return bytes?.length === media.mp4.length && bytes
        }, `saved file ${tc.file}`)
        assert.equal(sha256(data), sha256(media.mp4))
        savedFile = { filename: tc.file, bytes: data.length, sha256: sha256(data) }
      }

      report.cases.push({
        name: tc.name,
        preservedHref: after.href === before.href,
        preservedResult: after.title === before.title,
        preservedQuality: after.activeQuality,
        preservedSelection: after.videoSrc === before.videoSrc,
        taskState: after.tasks[0]?.state,
        requests: caseReqs,
        isolatedOpenerNull,
        savedFile,
      })
    }
    report.passed = true
  } finally {
    await writeFile(path.join(runDir, 'native-isolation-results.json'), JSON.stringify(report, null, 2) + '\n')
    browser.close()
    cdnServer.close()
    appServer.close()
  }
  console.log('Cross-origin native isolation browser fixtures passed:', report.cases.length)
}

if (command === 'serve' || (command === 'serve-ui' && profile)) await serve()
else if (command === 'check' && profile) await check()
else if (command === 'check-native' && profile) await checkNative()
else throw new Error('Expected serve <run-dir>, check <run-dir> <browser-profile> [case], or check-native <run-dir> <browser-profile> [dist-dir]')
