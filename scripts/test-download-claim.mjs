// Original Vue -> admission/ready -> attachment iframe, on two controlled loopback origins.
// node scripts/test-download-browser.mjs serve-ui <runDir> <production-dist>
// Start Chrome with a NEW profile and --remote-debugging-port=0, then run this probe
// for each mode, using separate clean profiles: <runDir> <profile> passive|legacy.
import {mkdir, readFile, readdir, writeFile} from 'node:fs/promises'
import {createHash} from 'node:crypto'
import path from 'node:path'
import {setTimeout as sleep} from 'node:timers/promises'
import {connect, until} from './download-browser-session.mjs'

function checkClaim(report) {
  const {events, source, terminal} = report
  const task = terminal?.id
  const instance = terminal?.instance
  const sameTask = e => task && instance && e.id === task && e.instance === instance && e.build === terminal.build
  const isFileURL = e => e.url?.endsWith('/downloads/file/' + task + '/[redacted]')
  const pick = (kind, test) => events.find(e => e.kind === kind && test(e))
  const begin = pick('browser-begin', isFileURL)
  const response = pick('response', e => isFileURL(e) && e.type === 'Document')
  const header = key => Object.entries(response?.headers || {}).find(([k]) => k.toLowerCase() === key)?.[1]
  const encodedName = /(?:^|;)\s*filename\*=UTF-8''([^;]+)/i.exec(header('content-disposition') || '')?.[1]
  const states = events.filter(e => e.kind === 'page-snapshot' && sameTask(e))
  const ready = states.findIndex(e => e.delivery === 'ready')
  const claimed = states.findIndex((e,i) => i > ready && e.delivery === 'claimed'
    && e.status === 'downloading' && e.loaded > 0 && e.loaded < source.bytes)
  const stored = report.stored?.find(s => sameTask(s) && s.delivery === 'claimed' && s.status === 'completed')
  const completedFile = report.files.find(f => f.name === begin?.name && !f.name.endsWith('.crdownload'))
  return {
    bridge: !!pick('response', e => e.type === 'Document' && e.url?.endsWith('/downloads/bridge.html') && e.status === 200),
    ready: ready !== -1,
    appended: !!pick('iframe-append', e => isFileURL(e) && !!e.stack),
    requested: !!pick('request', e => isFileURL(e) && e.method === 'GET' && e.type === 'Document'),
    attachment: response?.status === 200 && response.fromWorker === true && header('content-type') === source.mime
      && /^attachment\s*;/i.test(header('content-disposition') || '')
      && !!encodedName && decodeURIComponent(encodedName) === begin?.name,
    claimed: claimed > ready && ready !== -1,
    completed: claimed !== -1 && states.slice(claimed+1).some(s => s.status === 'completed' && s.loaded === source.bytes)
      && terminal?.state === 'completed' && stored?.loaded === source.bytes,
    file: completedFile?.bytes === source.bytes && completedFile.sha256 === source.sha256,
    browserCompleted: !!begin && !!pick('browser-progress', e => e.guid === begin.guid && e.state === 'completed'
      && e.loaded === source.bytes && e.total === source.bytes),
  }
}


// Offline recheck of an immutable receipt; this mode performs no browser I/O.
if (process.argv[2] === 'verify') {
  const receipt = JSON.parse(await readFile(process.argv[3], 'utf8'))
  const evidence = checkClaim(receipt)
  console.log(JSON.stringify(evidence, null, 2))
  process.exit(Object.values(evidence).every(Boolean) ? 0 : 1)
}

const [directory, profile, mode] = process.argv.slice(2)
if (!profile || !['passive', 'legacy'].includes(mode)) throw Error('Expected <run-dir> <new-profile> passive|legacy')
const root = path.resolve(directory), out = path.join(root, mode), files = path.join(out, 'files')
await mkdir(out)
await mkdir(files)
const {app, cdn, claimFixture} = JSON.parse(await readFile(path.join(root, 'servers.json')))
if (!claimFixture || ![app, cdn].every(u => /^http:\/\/127\.0\.0\.1:\d+$/.test(u))) throw Error('Expected the serve-ui loopback fixture')
const events = [], initErrors = [], pending = new Set(), sessions = new Map(), requests = new Map()
const report = { mode, source: claimFixture, start: new Date().toISOString(), events, initErrors }
const sanitize = text => String(text).replace(/(\/downloads\/file\/[^/?\s]+\/)[^/?#\s)]+/g, '$1[redacted]')
const log = (kind, value={}) => events.push({ at: Date.now(), kind, ...value })
function watched(task, label) {
  const job = task.catch(error => { const problem = {label, error:sanitize(error.message)}; initErrors.push(problem); log('cdp-error',problem) })
  pending.add(job)
  job.finally(() => pending.delete(job))
}
function headers(h={}) {
  const pick = ['content-type','content-disposition','content-length','cache-control']
  return Object.fromEntries(Object.entries(h).filter(([k])=>pick.includes(k.toLowerCase()))
    .map(([k,v])=>[k.toLowerCase(),v]))
}
function onEvent({method,params:p,sessionId:s}) {
  if (!p) return
  if (method === 'Target.attachedToTarget') {
    sessions.set(p.sessionId,p.targetInfo.type)
    if (p.targetInfo.type==='service_worker') {
      log('worker-attachment',{waiting:p.waitingForDebugger})
      if (mode==='legacy') watched((async()=>{
        try {
          if (!p.targetInfo.url) throw new Error('Attached service worker has no initial URL')
          if (!p.targetInfo.url.startsWith(app+'/downloads/')) {
            log('non-project-worker-skipped',{waiting:p.waitingForDebugger})
            return
          }
          await send('Network.enable',{},p.sessionId)
          await enableInterception(p.sessionId)
          log('worker-init-finished')
        } finally {
          // The original observer resumed only after successful setup. Always
          // release here, even if setup failed or the initial URL was empty.
          if (p.waitingForDebugger) {
            try {
              await send('Runtime.runIfWaitingForDebugger',{},p.sessionId)
              log('worker-released')
            } catch (error) {
              const problem={label:'resume worker',error:sanitize(error.message)}
              initErrors.push(problem)
              log('cdp-error',problem)
            }
          }
        }
      })(),'legacy worker initialization')
    }
  }
  if (method==='Target.targetCreated' || method==='Target.targetDestroyed') {
    if(p.targetInfo?.type==='service_worker')log('worker-target',{method,id:p.targetInfo.targetId})
  }
  if (method==='ServiceWorker.workerVersionUpdated') for(const v of p.versions) log('worker-version',{id:v.versionId,status:v.status,running:v.runningStatus})
  if (method==='ServiceWorker.workerErrorReported') log('worker-error',{message:sanitize(p.errorMessage.errorMessage)})
  if (method==='Fetch.requestPaused') {
    log('legacy-interception',{target:sessions.get(s),url:sanitize(p.request.url),type:p.resourceType})
    const preload=p.resourceType==='Media'
    watched(send(preload?'Fetch.failRequest':'Fetch.continueRequest',
      {requestId:p.requestId,...(preload?{errorReason:'Aborted'}:{})},s),'legacy request continuation')
  }
  if(method==='Runtime.exceptionThrown')log('runtime-exception',{target:sessions.get(s),error:sanitize(p.exceptionDetails.exception?.description||p.exceptionDetails.text)})
  if(method==='Runtime.bindingCalled' && p.name==='claimEvidence'){const event=JSON.parse(p.payload);log(event.kind,event.data)}
  if(method==='Browser.downloadWillBegin')log('browser-begin',{url:sanitize(p.url),guid:p.guid,name:p.suggestedFilename})
  if(method==='Browser.downloadProgress')log('browser-progress',{state:p.state,loaded:p.receivedBytes,total:p.totalBytes,guid:p.guid})
  if(method==='Page.frameRequestedNavigation'||method==='Page.frameStartedNavigating')log('frame-navigation',{method,url:sanitize(p.url),frame:p.frameId})
  const k=s+'/'+p.requestId
  if(method==='Network.requestWillBeSent') {
    const u=p.request.url
    if(u.includes('/downloads/')||u.startsWith(cdn+'/')) {
      requests.set(k,u);log('request',{id:k,url:sanitize(u),type:p.type,method:p.request.method,target:sessions.get(s)})
    }
  }
  if(!requests.has(k)) return
  if(method==='Network.responseReceived')log('response',{id:k,url:sanitize(p.response.url),type:p.type,status:p.response.status,fromWorker:p.response.fromServiceWorker,headers:headers(p.response.headers)})
  if(method==='Network.responseReceivedExtraInfo')log('response-extra',{id:k,status:p.statusCode,headers:headers(p.headers)})
  if(method==='Network.loadingFailed')log('network-failure',{id:k,error:p.errorText,cancelled:p.canceled,blocked:p.blockedReason})
}

// Identical passive page instrumentation in both modes. It neither starts a Worker
// nor requests a snapshot; it observes the application's existing channel messages.
function observePage() {
  const record=(kind,data)=>window.claimEvidence(JSON.stringify({kind,data}))
  const safe=s=>String(s||'').replace(/(\/downloads\/file\/[^/?\s]+\/)[^/?#\s)]+/g,'$1[redacted]')
  const snap=s=>({id:s.id,delivery:s.delivery,status:s.status,loaded:s.loaded,build:s.workerBuild,instance:s.workerInstance,error:safe(s.error)})
  const response=m=>{for(const s of [m?.snapshot,...(m?.snapshots||[])])if(s)record('page-snapshot',snap(s))}
  const Channel=window.MessageChannel
  window.MessageChannel=class extends Channel {constructor(){super();this.port1.addEventListener('message',e=>response(e.data))}}
  navigator.serviceWorker.addEventListener('message',e=>response(e.data))
  const post=ServiceWorker.prototype.postMessage
  ServiceWorker.prototype.postMessage=function(m,...rest){if(m?.action)record('page-rpc',{action:m.action,id:m.id||m.request?.id,keys:m.tasks?.map(t=>t.id)});return post.call(this,m,...rest)}
  const append=Node.prototype.appendChild
  Node.prototype.appendChild=function(el){
    const frame=el instanceof HTMLIFrameElement && el.src.includes('/downloads/')
    if(frame)record('iframe-append',{url:safe(el.src),title:el.title,stack:safe(Error().stack).slice(0,900)})
    try{return append.call(this,el)}catch(error){if(frame)record('iframe-error',{message:safe(error.message)});throw error}
  }
  window.addEventListener('error',e=>record('page-error',{message:safe(e.message)}))
  window.addEventListener('unhandledrejection',e=>record('page-rejection',{message:safe(e.reason?.message||e.reason)}))
}
const browser=await connect(profile,onEvent), send=browser.send
const enableInterception=s=>send('Fetch.enable',{patterns:['Media','Fetch','XHR'].map(resourceType=>({urlPattern:'*',resourceType,requestStage:'Request'}))},s)
let page, closedTarget
async function evaluate(expression) {
  const r=await send('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true,userGesture:true},page)
  if(r.exceptionDetails)throw Error(sanitize(r.exceptionDetails.exception?.description||r.exceptionDetails.text))
  return r.result.value
}
async function currentRows() {
  return evaluate("Array.from(document.querySelectorAll('[data-download-state]')).map(e=>({state:e.dataset.downloadState,id:e.dataset.taskId,build:e.dataset.workerBuild,instance:e.dataset.workerInstance}))")
}
// Evaluated in the page's realm. Task records also contain a token, so select
// only the public state fields when reading the existing store.
async function storedSnapshots() {
  const name = 'dokitool-downloads'
  if (!(await indexedDB.databases()).some(database => database.name === name)) return []
  const read = request => new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
  const db = await read(indexedDB.open(name))
  try {
    if (!db.objectStoreNames.contains('tasks')) return []
    const records = await read(db.transaction('tasks').objectStore('tasks').getAll())
    return records.map(({snapshot}) => ({
      id: snapshot.id,
      status: snapshot.status,
      delivery: snapshot.delivery,
      loaded: snapshot.loaded,
      build: snapshot.workerBuild,
      instance: snapshot.workerInstance,
      error: snapshot.error,
    }))
  } finally {
    db.close()
  }
}

try{
  report.browser=(await send('Browser.getVersion')).product
  await send('Browser.setDownloadBehavior',{behavior:'allow',downloadPath:files,eventsEnabled:true})
  if(mode==='legacy')await send('Target.setAutoAttach',{autoAttach:true,waitForDebuggerOnStart:true,flatten:true,filter:[{type:'service_worker'},{exclude:true}]})
  closedTarget=(await send('Target.createTarget',{url:'about:blank'})).targetId
  page=(await send('Target.attachToTarget',{targetId:closedTarget,flatten:true})).sessionId; sessions.set(page,'page')
  for(const name of ['Page','Network','Runtime','ServiceWorker'])await send(name+'.enable',{},page)
  await send('Runtime.addBinding',{name:'claimEvidence'},page)
  await send('Page.addScriptToEvaluateOnNewDocument',{source:'('+observePage+')()'},page)
  if(mode==='legacy')await enableInterception(page)
  const nav=await send('Page.navigate',{url:app+'/'},page)
  if(nav.errorText)throw Error(nav.errorText)
  await until(()=>evaluate("!!document.querySelector('input[data-initial-url]')"),'original form')
  await evaluate("(()=>{const i=document.querySelector('input[data-initial-url]');i.value='https://www.douyin.com/video/1234567890123456789';i.dispatchEvent(new Event('input',{bubbles:true}))})()")
  await sleep(100);await evaluate("document.querySelector('input[data-initial-url]').parentElement.querySelector('button').click()")
  await until(()=>evaluate("!!document.querySelector('video.rs-video')"),'video rendered')
  const point=await evaluate(`(()=>{
    const b=[...document.querySelectorAll('button.rs-btn--primary')].find(x=>/Download Video|下载视频/i.test(x.innerText))
    if(!b || b.disabled) throw Error('The normal video action is unavailable')
    b.scrollIntoView({block:'center',behavior:'instant'})
    const rect=b.getBoundingClientRect(), x=rect.x+rect.width/2, y=rect.y+rect.height/2
    if(!b.contains(document.elementFromPoint(x,y))) throw Error('The normal video action is covered')
    return {x,y}
  })()`)
  await send('Input.dispatchMouseEvent',{type:'mousePressed',...point,button:'left',clickCount:1},page)
  await send('Input.dispatchMouseEvent',{type:'mouseReleased',...point,button:'left',clickCount:1},page)
  report.downloadClicks=1
  try {
    report.terminal=await until(async()=>{const rows=await currentRows();return rows.find(r=>['failed','cancelled','completed','handed-off'].includes(r.state))},'terminal page state',65_000)
  } catch(error){report.observationError=error.message}
  report.rows=await currentRows()
  // Read existing records only; a missing store is not proof of a failed download.
  report.stored = await evaluate("(" + storedSnapshots.toString() + ")()")
  if (report.terminal?.state === 'completed') {
    const expected = await until(() => events.find(e => e.kind === 'browser-begin'
      && e.url.endsWith('/downloads/file/' + report.terminal.id + '/[redacted]')), 'browser download handoff', 6000)
    await until(async () => (await readdir(files)).includes(expected.name), 'named file saved', 6000)
    try {
      await until(() => events.find(e => e.kind === 'browser-progress' && e.guid === expected.guid
        && ['completed', 'canceled'].includes(e.state)), 'browser download terminal event', 10000)
    } catch (error) {
      report.browserTerminalObservationError = sanitize(error.message)
    }
  }
} catch(error){report.error=sanitize(error.message);log('probe-error',{error:report.error})}
finally{
  await Promise.allSettled([...pending])
  report.files = []
  const names = await readdir(files)
  report.temporaryFileCount = names.filter(name => name.endsWith('.crdownload')).length
  const handoffs = new Set(events.filter(e => e.kind === 'browser-begin').map(e => e.name))
  for (const name of names) {
    if (name.endsWith('.crdownload') || !handoffs.has(name)) continue
    try {
      const bytes = await readFile(path.join(files, name))
      report.files.push({name, bytes: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex')})
    } catch (error) {
      if (error.code !== 'ENOENT') throw error
      log('file-no-longer-present', {name})
    }
  }
  report.end=new Date().toISOString()
  report.claimEvidence = checkClaim(report)
  await writeFile(path.join(out,'receipt.json'),JSON.stringify(report,null,2)+'\n')
  if(closedTarget)await send('Target.closeTarget',{targetId:closedTarget})
  browser.close()
}
console.log(JSON.stringify({mode,terminal:report.terminal?.state||null,evidence:report.claimEvidence,errors:initErrors.length,receipt:path.join(out,'receipt.json')}))
if(report.error||report.observationError||initErrors.length||report.terminal?.state!=='completed'
    ||!Object.values(report.claimEvidence).every(Boolean))process.exitCode=1
