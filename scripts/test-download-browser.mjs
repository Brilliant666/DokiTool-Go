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
    const test = cases.find(test => req.url === '/fixture/' + test.name)
    if (!test) { res.writeHead(404).end(); return }
    const { wire, headers } = specimen(test)
    res.writeHead(200, headers)
    if (test.broken || test.slow || test.sizeAdjustment === -1) {
      res.write(wire.subarray(0, 4096))
      const end = setTimeout(() => test.broken ? res.destroy() : res.end(wire.subarray(4096)), test.slow ? 6000 : 250)
      res.on('close', () => clearTimeout(end))
    } else if (test.chunked) {
      res.write(wire.subarray(0, 4096))
      res.end(wire.subarray(4096))
    } else res.end(wire)
  }
  const cdn = await listen(sendMedia)
  const app = await listen(async (req, res) => {
    if (req.url?.startsWith('/fixture/')) return sendMedia(req, res)
    if (req.url === '/downloads/check') return res.writeHead(200, { 'Content-Type': 'text/html' }).end('<!doctype html><title>Download test</title>')
    if (/^\/downloads\/[a-z][a-z-]*\.js$/.test(req.url)) {
      const source = await readFile(new URL(path.basename(req.url), workerRoot))
      return res.writeHead(200, { 'Content-Type': 'text/javascript', 'Cache-Control': 'no-store' }).end(source)
    }
    res.writeHead(404).end()
  })
  await writeFile(path.join(runDir, 'servers.json'), JSON.stringify({ app, cdn }))
  console.log('Loopback fixtures ready', app, cdn)
}

// Use the browser's debugging endpoint only for this dedicated test profile.
// Native download naming remains browser-owned; the test never renames files.
async function connect(profileDir) {
  const [port, socketPath] = (await readFile(path.join(profileDir, 'DevToolsActivePort'), 'utf8')).trim().split(/\r?\n/)
  const ws = new WebSocket('ws://127.0.0.1:' + Number(port) + socketPath)
  await new Promise((resolve, reject) => { ws.onopen = resolve; ws.onerror = reject })
  let sequence = 0
  const pending = new Map()
  ws.onmessage = message => {
    const packet = JSON.parse(message.data)
    const reply = pending.get(packet.id)
    if (reply) {
      pending.delete(packet.id)
      if (packet.error) reply.reject(new Error(packet.error.message))
      else reply.resolve(packet.result)
    }
  }
  const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    const id = ++sequence
    pending.set(id, { resolve, reject })
    ws.send(JSON.stringify({ id, method, params, sessionId }))
  })
  return { send, close: () => ws.close() }
}

async function until(read, label) {
  const deadline = Date.now() + 20_000
  do {
    const result = await read()
    if (result) return result
    await delay(75)
  } while (Date.now() < deadline)
  throw new Error('Timed out waiting for ' + label)
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

if (command === 'serve') await serve()
else if (command === 'check' && profile) await check()
else throw new Error('Expected serve <run-dir> or check <run-dir> <browser-profile> [case]')
