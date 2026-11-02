// Real Go envelopes -> real Vue/Pinia/resolve/download manager. HTTP and DOM
// are controlled boundaries; this is not browser-native or online acceptance.
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { createServer } from 'vite';
import { createPinia, setActivePinia } from 'pinia';
import { ref, markRaw } from 'vue';
import { webcrypto } from 'node:crypto';

export async function verifyCredentialRecovery(cases) {
  const root = fileURLToPath(new URL('../', import.meta.url));
  const vite = await createServer({root, optimizeDeps:{noDiscovery:true,include:[]}, server:{middlewareMode:true, ws:false, hmr:false, watch:null}, appType:'custom'});
  const names = ['fetch', 'window', 'location', 'document', 'crypto'];
  const descriptors = Object.fromEntries(names.map(name => [name,Object.getOwnPropertyDescriptor(globalThis,name)]));
  const oldCreate = URL.createObjectURL, oldRevoke = URL.revokeObjectURL;
  const body = Buffer.from([0,0,0,20,102,116,121,112,105,115,111,109,0,0,0,0,105,115,111,109,1,2,3,4]);
  let phase, payload, behavior, saves, wire, source, dl, store, anti;
  const observations = [];
  Object.defineProperty(globalThis,'crypto',{configurable:true,value:webcrypto});
  globalThis.window = {setTimeout,clearTimeout,addEventListener(){},removeEventListener(){}};
  globalThis.location = {href:'http://localhost/',origin:'http://localhost'};
  globalThis.document = {body:{appendChild(){},style:{}},createElement(){return {setAttribute(){},click(){saves++},remove(){}}}};
  URL.createObjectURL = () => 'blob:controlled'; URL.revokeObjectURL = () => {};
  globalThis.fetch = async (url) => {
    url = String(url);
    wire.push({phase,url});
    if (url.includes('/api/resolve?')) return new Response(JSON.stringify(payload),{headers:{'content-type':'application/json'}});
    if (phase === 'fresh' && behavior === 'cancel') {
      dl.handleCancelDownload(store.downloads[0].id);
      throw new DOMException('Aborted','AbortError');
    }
    if (phase === 'old' || behavior === 'all-failed' || url === source.url) return new Response(null,{status:403});
    return new Response(body,{headers:{'content-type':'video/mp4','content-length':String(body.length)}});
  };
  async function settled() {
    for (let i=0; i<1000; i++) {
      if (['failed','completed','cancelled'].includes(store.downloads[0]?.status)) return store.downloads[0];
      await new Promise(resolve => setTimeout(resolve,2));
    }
    throw new Error('download did not settle');
  }
  try {
    const {useVideoStore} = await vite.ssrLoadModule('/src/stores/video.ts');
    const {useVideoSelection} = await vite.ssrLoadModule('/src/composables/useVideoSelection.ts');
    const {useVideoParser} = await vite.ssrLoadModule('/src/composables/useVideoParser.ts');
    const {useMediaDownload} = await vite.ssrLoadModule('/src/composables/useMediaDownload.ts');
    anti = (await vite.ssrLoadModule('/src/utils/antiReplay.js')).antiReplayInstance;
    for (const name of ['stale-first','fresh-first','host-changed','object-changed','content-changed']) {
      for (const mode of ['default','explicit']) {
        for (const transport of (name.endsWith('first') ? ['working','all-failed','cancel'] : ['working'])) {
          setActivePinia(createPinia()); store = useVideoStore();
          store.setResult(structuredClone(cases['credential-before'].data));
          const selection = markRaw(useVideoSelection({videoStore:store})); selection.syncResult();
          const variant = store.resultData.items[0].variants[0]; source = variant.delivery.sources[0];
          if (mode === 'explicit') selection.choose({variant_id:variant.id,source_id:source.id});
          const parser = useVideoParser({videoStore:store,locale:ref('zh-CN'),showToast(){}});
          dl = useMediaDownload({videoStore:store,selection,resolveItem:parser.resolveItem,showToast(){}});
          phase='old'; behavior=transport; payload=cases['credential-'+name]; saves=0; wire=[];
          await dl.downloadMainVideo(); assert.equal((await settled()).status,'failed');
          const id=store.downloads[0].id, choice=JSON.stringify(selection.currentSelection.value);
          phase='fresh'; await dl.handleRefreshDownload(id); const task=await settled();
          const expected = !name.endsWith('first') || transport==='all-failed' ? 'failed' : transport==='cancel' ? 'cancelled' : 'completed';
          assert.equal(task.status,expected,`${name}/${mode}/${transport}: ${task.error}`);
          assert.equal(store.downloads.length,1); assert.equal(task.id,id);
          assert.equal(saves,expected==='completed'?1:0);
          assert.equal(JSON.stringify(selection.currentSelection.value),choice);
          assert.equal(wire.filter(r=>r.url.includes('/api/resolve?')).length,1);
          const media=wire.filter(r=>r.phase==='fresh'&&!r.url.includes('/api/resolve?')).map(r=>r.url);
          if (!name.endsWith('first')) assert.deepEqual(media,[],'lost identity must not borrow default');
          else {
            const renewed=payload.data.items[0].variants[0].delivery.sources[0];
            const routes=[renewed.url,...(renewed.fallback_urls??[])];
            assert.ok(media.every(url=>routes.includes(url)),'must stay within selected source credentials');
            assert.ok(media.length<=4,'bounded two-credential attempts');
            if (transport==='cancel') assert.equal(new Set(media).size,1,'cancel must not try next credential');
            if (expected==='completed') {
              assert.notEqual(media.at(-1),source.url);
              assert.equal(task.loaded,body.length);
              if(name==='stale-first') assert.equal(media[0],source.url,'old request must fail before fallback');
            }
          }
          observations.push({name,mode,transport,status:task.status,saves,mediaRequests:media.length});
          console.log('CREDENTIAL_RECOVERY_PASS',name,mode,transport,task.status);
        }
      }
    }
    assert.equal(observations.length,18);
    return observations;
  } finally {
    anti?.destroy(); await vite.close();
    URL.createObjectURL=oldCreate; URL.revokeObjectURL=oldRevoke;
    for(const name of names) { if(descriptors[name]) Object.defineProperty(globalThis,name,descriptors[name]); else delete globalThis[name]; }
  }
}
