import test, {after, before, describe} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createServer} from 'vite';
import {reactive, ref} from 'vue';

// Real composables, synthetic network responses; no live platform requests.
describe('unified frontend request contract', () => {
let vite, parser, config, contract, selection;
const originalFetch=globalThis.fetch, originalWindow=globalThis.window;
const media='https://media.example.test/video.mp4';
const file=(id='one',url=media)=>({id,type:'video',variants:[{id:'main',delivery:{kind:'file',sources:[{url}]}}],default:{variant_id:'main',url}});
const good=(data={})=>({schema_version:'1.0',code:'OK',data:{platform:'future_platform',items:[file()],...data}});
before(async()=>{
  vite=await createServer({server:{middlewareMode:true,hmr:false,ws:false,watch:null},appType:'custom'});
  parser=await vite.ssrLoadModule('/src/composables/useVideoParser.ts');
  config=await vite.ssrLoadModule('/src/services/apiConfig.ts');
  contract=await vite.ssrLoadModule('/src/utils/mediaContract.ts');
  selection=await vite.ssrLoadModule('/src/composables/useVideoSelection.ts');
  (await vite.ssrLoadModule('/src/services/operationLogger.js')).operationLogger.enableConsole=false;
  globalThis.window={setTimeout,clearTimeout};
});
after(async()=>{globalThis.fetch=originalFetch;globalThis.window=originalWindow;await vite?.close();});
function harness(source='https://source.example.test/work?signature=a%2Bb%3D') {
  const store=reactive({resultData:null,resultVersion:0,
    clearResult(){this.resultData=null;this.resultVersion++;}, setResult(data){this.resultData=data;this.resultVersion++;},
    replaceItem(item,version){if(version!==this.resultVersion||!this.resultData)return false;this.resultData={...this.resultData,items:this.resultData.items.map(old=>old.id===item.id?item:old)};return true;},
  });
  const toasts=[];
  const instance=parser.useVideoParser({videoStore:store,locale:ref('zh-CN'),showToast:(...args)=>toasts.push(args)});
  instance.inputUrl.value=source;return {store,instance,toasts};
}
test('all existing and future platform IDs use one versioned parse endpoint',async()=>{
  assert.equal(config.resolveApiConfig({}).parseUrl,'/api/parse');
  assert.equal(config.resolveApiConfig({VITE_API_BASE_URL:'https://api.example.test/'}).parseUrl,'https://api.example.test/api/parse');
  assert.equal(config.resolveApiConfig({VITE_API_BASE_URL:'https://api.example.test/'}).mode,'php');
  assert.throws(()=>config.resolveApiConfig({VITE_API_MODE:'typo'}));
  assert.throws(()=>config.resolveApiConfig({VITE_API_BASE_URL:'https://api.example.test/api'}));
  for(const platform of ['all','douyin','kuaishou','bilibili','xiaohongshu','toutiao','weibo','pipixia','pipigx','zuiyou','x','instagram','facebook','tumblr','dailymotion','future_platform']) {
    const {instance,store}=harness();let requested;
    globalThis.fetch=async url=>{requested=new URL(url,'https://snap.example.test');return Response.json(good({platform}));};
    instance.selectPlatform(platform);await instance.parseVideo();
    assert.equal(requested.pathname,'/api/parse');assert.equal(requested.searchParams.get('schema_version'),'1.0');assert.equal(requested.searchParams.get('platform'),platform);
    assert.equal(requested.searchParams.get('url'),instance.inputUrl.value);assert.equal(store.resultData.items[0].default.url,media);
  }
  for(const [nonHeroPlatform, sampleUrl] of [
    ['toutiao', 'https://www.toutiao.com/video/7400000000000000000/'],
    ['pipixia', 'https://h5.pipix.com/s/abcdefg/'],
    ['pipigx', 'https://h5.pipigx.com/pp/post/123456'],
    ['zuiyou', 'https://share.izuiyou.com/detail/123456'],
    ['tumblr', 'https://www.tumblr.com/blog/123456789'],
    ['dailymotion', 'https://www.dailymotion.com/video/xbvrdzy'],
  ]) {
    const {instance,store}=harness(`分享链接 ${sampleUrl} 复制此消息`);let requested;
    globalThis.fetch=async url=>{requested=new URL(url,'https://snap.example.test');return Response.json(good({platform:nonHeroPlatform}));};
    assert.equal(instance.currentPlatform.value,'all');
    await instance.parseVideo();
    assert.equal(requested.pathname,'/api/parse');
    assert.equal(requested.searchParams.get('platform'),'all');
    assert.equal(requested.searchParams.get('url'),sampleUrl);
    assert.equal(store.resultData.platform,nonHeroPlatform);
  }
});
test('mixed items, metadata and attachments survive without platform normalization',async()=>{
  const raw=JSON.parse(await readFile(new URL('../../docs/schema/unified-v1/examples/10-mixed-live-photo-and-bgm.json',import.meta.url),'utf8'));
  const before=JSON.stringify(raw),data=contract.validateMediaEnvelope(raw);
  assert.equal(JSON.stringify(raw),before);assert.deepEqual(data,raw.data);
  assert.ok(data.items.some(i=>i.type==='image'));assert.ok(data.items.some(i=>i.type==='live_photo'));assert.equal(data.attachments[0].role,'background_audio');
});
test('old envelopes, aliases and absent default are rejected rather than guessed',()=>{
  for(const raw of [null,{}, {code:200,data:{url:media}},{status:'success',result:{video:media}},good({url:media}),good({video_backup:[media]}),good({images:[media]}),good({collections:[]})]) assert.throws(()=>contract.validateMediaEnvelope(raw));
  const noDefault=good();delete noDefault.data.items[0].default;assert.throws(()=>contract.validateMediaEnvelope(noDefault));
  assert.equal(parser.normalizeData,undefined);assert.equal(parser.normalizeResponse,undefined);
});
test('business failures clear stale media and retain the server explanation',async()=>{
  const {instance,store,toasts}=harness();store.setResult(good().data);
  globalThis.fetch=async()=>Response.json({schema_version:'1.0',code:'CONTENT_UNAVAILABLE',error:{code:'CONTENT_UNAVAILABLE',message:'作品不存在'}});
  await instance.parseVideo();assert.equal(store.resultData,null);assert.equal(instance.parseError.value,'作品不存在');assert.equal(toasts.at(-1)[1],'error');
});
test('malformed, HTTP and network failures do not claim success',async()=>{
  for(const fetch of [async()=>new Response('<html>failure</html>'),async()=>Response.json({error:{message:'上游不可用'}},{status:502}),async()=>{throw new TypeError('Failed to fetch');}]) {
    const {instance,store}=harness();store.setResult(good().data);globalThis.fetch=fetch;await instance.parseVideo();assert.equal(store.resultData,null);assert.ok(instance.parseError.value);assert.equal(instance.parseButton.isLoading.value,false);
  }
});
test('schema mismatch leaves independent in-flight download records untouched',async()=>{
  const {instance,store}=harness();store.downloads=[{id:17,status:'downloading',deliveryId:'original-task'}];
  globalThis.fetch=async()=>Response.json({...good(),schema_version:'2.0'});await instance.parseVideo();assert.match(instance.parseError.value,/刷新|更新/);assert.deepEqual(store.downloads,[{id:17,status:'downloading',deliveryId:'original-task'}]);
});
test('input invalidation aborts old parsing and duplicate clicks cannot start a second request',async()=>{
  const {instance,store}=harness();let calls=0,finish,signal;
  globalThis.fetch=async(_url,options)=>{calls++;signal=options.signal;return new Promise(resolve=>finish=resolve);};
  const first=instance.parseVideo();await instance.parseVideo();assert.equal(calls,1);
  instance.inputUrl.value='https://source.example.test/second';assert.equal(signal.aborted,true);
  globalThis.fetch=async()=>{calls++;return Response.json(good({title:'Second'}));};await instance.parseVideo();
  finish(Response.json(good({title:'First'})));await first;assert.equal(store.resultData.title,'Second');assert.equal(calls,2);assert.equal(instance.parseButton.isLoading.value,false);
});
test('platform change isolates a late error from the new parse result',async()=>{
  const {instance,store}=harness();let fail;
  globalThis.fetch=async()=>new Promise((_resolve,reject)=>fail=reject);const old=instance.parseVideo();
  instance.selectPlatform('future_platform');globalThis.fetch=async()=>Response.json(good({title:'Current'}));await instance.parseVideo();
  fail(new Error('late failure'));await old;assert.equal(store.resultData.title,'Current');assert.equal(instance.parseError.value,'');
});
const deferred={id:'opaque-part-3',type:'video',index:3,state:'deferred',default:null,default_unavailable_reason:'needs_resolve',resolve_url:'/api/resolve?schema_version=1.0&url=https%3A%2F%2Fsource.example.test%2Fwork%3Ftoken%3Da%252Bb&item_id=opaque-part-3'};
test('resolution uses the exact backend URL and opaque item ID',async()=>{
  const {instance,store}=harness();store.setResult(good({items:[deferred]}).data);let requested;
  const resolved=file(deferred.id,'https://media.example.test/p3.mp4');globalThis.fetch=async url=>{requested=url;return Response.json(good({items:[resolved]}));};
  const result=await instance.resolveItem(deferred);assert.equal(requested,deferred.resolve_url);assert.equal(result.id,deferred.id);assert.equal(result.default.url,resolved.default.url);assert.equal(store.resultData.items[0].default,null,'resolution is pure; the UI applies it using its captured result version');
  for(const url of ['https://evil.test/api/resolve?x=1','//evil.test/api/resolve?x=1','/api/../resolve?x=1','/api/resolve?x=1#part']) assert.throws(()=>parser.resolveApiUrl(url));
});
test('missing or unavailable resolved item never borrows another part URL',async()=>{
  const {instance,store}=harness();store.setResult(good({items:[deferred]}).data);
  globalThis.fetch=async()=>Response.json(good({items:[file('other-part')]}));await assert.rejects(instance.resolveItem(deferred),/条目/);assert.equal(store.resultData.items[0].default,null);
  const failed={id:deferred.id,type:'video',state:'unavailable',default:null,default_unavailable_reason:'unavailable',error:{code:'CONTENT_UNAVAILABLE',message:'当前分P不可用'}};
  globalThis.fetch=async()=>Response.json(good({items:[failed]}));const result=await instance.resolveItem(deferred);assert.equal(result.default,null);assert.equal(result.state,'unavailable');
});
test('resolution from an old result cannot mutate a newly parsed work',async()=>{
  const {instance,store}=harness();store.setResult(good({items:[deferred]}).data);let finish;
  globalThis.fetch=async()=>new Promise(resolve=>finish=resolve);const old=instance.resolveItem(deferred);
  store.setResult(good({title:'New work'}).data);finish(Response.json(good({items:[file(deferred.id)]})));await old;assert.equal(store.resultData.title,'New work');assert.equal(store.resultData.items[0].id,'one');
});
test('cancelling deferred resolution aborts the actual API request',async()=>{
  const {instance,store}=harness();store.setResult(good({items:[deferred]}).data);let observed;
  globalThis.fetch=async(_url,{signal})=>{observed=signal;return new Promise((_resolve,reject)=>signal.addEventListener('abort',()=>reject(new DOMException('Aborted','AbortError'))));};
  const controller=new AbortController(),task=instance.resolveItem(deferred,controller.signal);controller.abort();await assert.rejects(task);assert.equal(observed.aborted,true);
});
test('entry and group selection follow server IDs; failed parts stay unplayable',()=>{
  const {store}=harness();const failed={id:'failed',type:'video',state:'unavailable',default:null,default_unavailable_reason:'unavailable',error:{code:'CONTENT_UNAVAILABLE',message:'unavailable'}};
  store.setResult(good({items:[file('p1'),deferred,failed],default_item_id:deferred.id,groups:[{id:'parts',kind:'multipart',item_ids:['p1',deferred.id,'failed']}]}).data);
  const selected=selection.useVideoSelection({videoStore:store});selected.syncResult();assert.equal(selected.currentVideo.value.id,deferred.id);assert.equal(selected.currentVideoUrl.value,'');selected.selectIndex(2);assert.equal(selected.currentTarget.value.kind,'unavailable');assert.equal(selected.currentVideoUrl.value,'');selected.selectAll();assert.equal(selected.selectedCount.value,2);
});
test('API base accepts origins and rejects paths before URL normalization',()=>{
  for(const base of ['https://api.example.test','https://api.example.test/','http://localhost:8081','https://[::1]:8443']) assert.ok(config.resolveApiConfig({VITE_API_BASE_URL:base}).parseUrl.endsWith('/api/parse'));
  for(const base of ['https://user:secret@api.example.test','https://api.example.test/path','https://api.example.test/prefix/..','https://api.example.test/%2e%2e','https://api.example.test:99999','https://api.example.test?x=1','https://api.example.test/#hash']) assert.throws(()=>config.resolveApiConfig({VITE_API_BASE_URL:base}));
});

test('#91 PR1 item 3: fetchJSON surfaces retry_after_seconds and resolveItem retries 429 (<=15s, max 2 retries, abortable)',async(t)=>{
  t.mock.timers.enable({apis:['setTimeout']});
  globalThis.fetch=async()=>new Response(JSON.stringify({schema_version:'1.0',code:'RATE_LIMITED',error:{code:'RATE_LIMITED',message:'解析请求过于频繁，请稍后重试',retryable:true,retry_after_seconds:7}}),{status:429,headers:{'content-type':'application/json','Retry-After':'7'}});
  await assert.rejects(parser.fetchJSON('/api/resolve?item_id=1'),(err)=>{
    assert.equal(err.status,429);
    assert.equal(err.retry_after_seconds,7);
    return true;
  });

  // Header fallback when 429 body omits retry_after_seconds
  globalThis.fetch=async()=>new Response(JSON.stringify({error:{message:'Too Many Requests'}}),{status:429,headers:{'content-type':'application/json','Retry-After':'4'}});
  await assert.rejects(parser.fetchJSON('/api/resolve?item_id=1'),(err)=>{
    assert.equal(err.status,429);
    assert.equal(err.retry_after_seconds,4);
    return true;
  });

  // 2 consecutive 429s (<=15s) retry and succeed on attempt 3
  const {instance}=harness();
  const observedWaits=[];
  let calls=0;
  const resolved=file(deferred.id,'https://media.example.test/p3-after-429.mp4');
  globalThis.fetch=async()=>{
    calls++;
    if(calls<=2){
      return new Response(JSON.stringify({schema_version:'1.0',code:'RATE_LIMITED',error:{code:'RATE_LIMITED',message:'限流',retryable:true,retry_after_seconds:2}}),{status:429,headers:{'content-type':'application/json'}});
    }
    return Response.json(good({items:[resolved]}));
  };
  const flush=async()=>{await new Promise(r=>setImmediate(r));await new Promise(r=>setImmediate(r));};
  const promise=instance.resolveItem(deferred,undefined,{onRateLimitWait:(sec)=>observedWaits.push(sec)});
  for(let step=0;step<4;step++){
    await flush();
    t.mock.timers.tick(1000);
    await flush();
  }
  const item=await promise;
  assert.equal(calls,3);
  assert.equal(item.default.url,'https://media.example.test/p3-after-429.mp4');
  assert.deepEqual(observedWaits,[2,1,2,1]);

  // retry_after_seconds > 15 fails immediately without waiting
  let longCalls=0;
  const longWaits=[];
  globalThis.fetch=async()=>{
    longCalls++;
    return new Response(JSON.stringify({schema_version:'1.0',code:'RATE_LIMITED',error:{code:'RATE_LIMITED',message:'限流过久',retryable:true,retry_after_seconds:16}}),{status:429,headers:{'content-type':'application/json'}});
  };
  await assert.rejects(instance.resolveItem(deferred,undefined,{onRateLimitWait:(s)=>longWaits.push(s)}),/限流过久/);
  assert.equal(longCalls,1);
  assert.deepEqual(longWaits,[]);

  // Exhausting > 2 retries fails on 3rd 429
  let exhaustCalls=0;
  globalThis.fetch=async()=>{
    exhaustCalls++;
    return new Response(JSON.stringify({schema_version:'1.0',code:'RATE_LIMITED',error:{code:'RATE_LIMITED',message:'持续限流',retryable:true,retry_after_seconds:1}}),{status:429,headers:{'content-type':'application/json'}});
  };
  const exhaustHandled=assert.rejects(instance.resolveItem(deferred),/持续限流/);
  for(let step=0;step<3;step++){
    await flush();
    t.mock.timers.tick(1000);
    await flush();
  }
  await exhaustHandled;
  assert.equal(exhaustCalls,3);

  // User abort cancels during 429 wait immediately
  const abortController=new AbortController();
  globalThis.fetch=async()=>new Response(JSON.stringify({schema_version:'1.0',code:'RATE_LIMITED',error:{code:'RATE_LIMITED',message:'限流',retryable:true,retry_after_seconds:10}}),{status:429,headers:{'content-type':'application/json'}});
  const abortHandled=assert.rejects(instance.resolveItem(deferred,abortController.signal),{name:'AbortError'});
  await flush();
  abortController.abort();
  await abortHandled;
});

});
