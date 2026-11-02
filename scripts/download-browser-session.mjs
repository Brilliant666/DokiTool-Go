import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

// Use the browser's debugging endpoint only for this dedicated test profile.
// Native download naming remains browser-owned; the test never renames files.
export async function connect(profileDir, onEvent = () => {}) {
  const [port, socketPath] = (await readFile(path.join(profileDir, 'DevToolsActivePort'), 'utf8')).trim().split(/\r?\n/)
  const ws = new WebSocket('ws://127.0.0.1:' + Number(port) + socketPath)
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => { ws.close(); reject(new Error('Browser debug socket did not open')) }, 10_000)
    ws.onopen = () => { clearTimeout(timer); resolve() }
    ws.onerror = () => { clearTimeout(timer); reject(new Error('Browser debug socket failed to open')) }
  })
  let sequence = 0
  const pending = new Map()
  const disconnected = () => {
    for (const reply of pending.values()) {
      clearTimeout(reply.timer)
      reply.reject(new Error('Browser disconnected during ' + reply.method))
    }
    pending.clear()
  }
  ws.onclose = disconnected
  ws.onerror = disconnected
  ws.onmessage = message => {
    const packet = JSON.parse(message.data)
    const reply = pending.get(packet.id)
    if (!packet.id) { onEvent(packet); return }
    if (!reply) return
    clearTimeout(reply.timer)
    pending.delete(packet.id)
    if (packet.error) reply.reject(new Error(reply.method + ': ' + packet.error.message))
    else reply.resolve(packet.result)
  }
  const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    if (ws.readyState !== WebSocket.OPEN) return reject(new Error('Browser is not connected for ' + method))
    const id = ++sequence
    const timer = setTimeout(() => {
      pending.delete(id)
      reject(new Error('Browser RPC timed out: ' + method))
    }, 15_000)
    pending.set(id, { resolve, reject, timer, method })
    ws.send(JSON.stringify({ id, method, params, sessionId }))
  })
  return { send, close: () => ws.close() }
}

export async function until(read, label, budgetMs = 20_000) {
  const deadline = Date.now() + budgetMs
  do {
    const result = await read()
    if (result) return result
    await delay(75)
  } while (Date.now() < deadline)
  throw new Error('Timed out waiting for ' + label)
}
