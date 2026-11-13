import test, {after, before, describe} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {createServer} from 'vite';
import {reactive, ref} from 'vue';

// Real composables, synthetic network responses; no live platform requests.
describe('unified frontend request contract', () => {
let vite, parser, config, contract, selection, parseHelpers, ytHelperCore;
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
  parseHelpers=await vite.ssrLoadModule('/src/utils/parseHelpers.ts');
  ytHelperCore=await vite.ssrLoadModule('/src/utils/youtubeHelperCore.js');
  (await vite.ssrLoadModule('/src/services/operationLogger.js')).operationLogger.enableConsole=false;
  globalThis.window={setTimeout,clearTimeout};
});
after(async()=>{globalThis.fetch=originalFetch;globalThis.window=originalWindow;await vite?.close();});
function harness(source='https://source.example.test/work?signature=a%2Bb%3D', lang='zh-CN') {
  const store=reactive({resultData:null,resultVersion:0,
    clearResult(){this.resultData=null;this.resultVersion++;}, setResult(data){this.resultData=data;this.resultVersion++;},
    replaceItem(item,version){if(version!==this.resultVersion||!this.resultData)return false;this.resultData={...this.resultData,items:this.resultData.items.map(old=>old.id===item.id?item:old)};return true;},
  });
  const toasts=[];
  const localeRef=ref(lang);
  const instance=parser.useVideoParser({videoStore:store,locale:localeRef,showToast:(...args)=>toasts.push(args)});
  instance.inputUrl.value=source;return {store,instance,toasts,locale:localeRef};
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
test('share link extraction truncates at adjacent Hanzi or fullwidth punctuation while keeping ASCII query intact',async()=>{
  for(const [shareText, wantUrl, platform] of [
    ['79 小红书发布了一篇笔记 http://xhslink.com/o/abc123XYZ，复制本条信息，打开【小红书】App查看精彩内容！', 'http://xhslink.com/o/abc123XYZ', 'xiaohongshu'],
    ['https://www.bilibili.com/video/BV1xx411c7mD【官方MV】高清完整版', 'https://www.bilibili.com/video/BV1xx411c7mD', 'bilibili'],
    ['https://www.bilibili.com/video/BV1xx411c7mD?p=2&vd_source=abc_123【第2集】精彩片段', 'https://www.bilibili.com/video/BV1xx411c7mD?p=2&vd_source=abc_123', 'bilibili'],
    ['https://www.xiaohongshu.com/explore/66a1b2c3d4e5f6a7b8c9d0e1?xsec_token=ABc+123%2B_=-x&xsec_source=pc_feed，复制本条信息打开App', 'https://www.xiaohongshu.com/explore/66a1b2c3d4e5f6a7b8c9d0e1?xsec_token=ABc+123%2B_=-x&xsec_source=pc_feed', 'xiaohongshu'],
    ['看这个视频https://v.douyin.com/iYabc123/复制此链接打开抖音', 'https://v.douyin.com/iYabc123/', 'douyin'],
  ]) {
    const {instance,store}=harness(shareText);let requested;
    globalThis.fetch=async url=>{requested=new URL(url,'https://snap.example.test');return Response.json(good({platform}));};
    await instance.parseVideo();
    assert.equal(instance.inputUrl.value,wantUrl);
    assert.equal(requested.searchParams.get('url'),wantUrl);
    assert.equal(store.resultData.platform,platform);
  }
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

test('pasting another platform link while a specific platform is selected prompts switching back to 所有平台, while unsupported fb.com returns null',async()=>{
  assert.equal(parseHelpers.detectUrlPlatform('https://fb.watch/abc123/'),'facebook');
  assert.equal(parseHelpers.detectUrlPlatform('https://www.facebook.com/watch/?v=123'),'facebook');
  assert.equal(parseHelpers.detectUrlPlatform('https://fb.com/watch/?v=123'),null);
  assert.equal(parseHelpers.detectUrlPlatform('https://www.fb.com/watch/?v=123'),null);

  let fetchCount=0;
  globalThis.fetch=async()=>{fetchCount++;return Response.json(good());};

  const {instance,toasts}=harness('https://www.bilibili.com/video/BV1xx411c7mD');
  instance.selectPlatform('douyin');
  await instance.parseVideo();
  assert.equal(fetchCount,0);
  assert.match(instance.parseError.value,/所有平台/);
  assert.equal(toasts.at(-1)?.[1],'error');

  const ytMismatch=harness('https://youtu.be/I9JvDaciaFk');
  ytMismatch.instance.selectPlatform('douyin');
  await ytMismatch.instance.parseVideo();
  assert.equal(fetchCount,0);
  assert.match(ytMismatch.instance.parseError.value,/所有平台/);

  const enMismatch=harness('https://xhslink.com/a/abc123','en');
  enMismatch.instance.selectPlatform('douyin');
  await enMismatch.instance.parseVideo();
  assert.equal(fetchCount,0);
  assert.match(enMismatch.instance.parseError.value,/\bAll\b/);

  // Unsupported fb.com must NOT falsely trigger platform mismatch; backend handles it as unsupported link
  const fbDotCom=harness('https://fb.com/watch/?v=123');
  fbDotCom.instance.selectPlatform('douyin');
  globalThis.fetch=async()=>{fetchCount++;return Response.json({schema_version:'1.0',code:'UNSUPPORTED_PLATFORM',error:{code:'UNSUPPORTED_PLATFORM',message:'暂不支持该平台链接'}},{status:400});};
  await fbDotCom.instance.parseVideo();
  assert.equal(fetchCount,1);
  assert.equal(fbDotCom.instance.parseError.value,'暂不支持该平台链接');
  assert.doesNotMatch(fbDotCom.instance.parseError.value,/不匹配|所有平台/);

  // Backend mismatch fallback for opaque domains when a specific platform is selected
  const backendMismatch=harness('https://short.example.test/opaque');
  backendMismatch.instance.selectPlatform('douyin');
  globalThis.fetch=async()=>Response.json({schema_version:'1.0',code:'PLATFORM_MISMATCH',error:{code:'PLATFORM_MISMATCH',message:'平台不匹配'}},{status:400});
  await backendMismatch.instance.parseVideo();
  assert.match(backendMismatch.instance.parseError.value,/所有平台/);

  const legacyMismatch=harness('https://short.example.test/opaque2');
  legacyMismatch.instance.selectPlatform('douyin');
  globalThis.fetch=async()=>Response.json({schema_version:'1.0',code:'INVALID_INPUT',error:{code:'INVALID_INPUT',message:'请输入对应平台的有效分享链接'}},{status:400});
  await legacyMismatch.instance.parseVideo();
  assert.match(legacyMismatch.instance.parseError.value,/所有平台/);
});

test('cancelParse aborts in-flight request cleanly without error banner or toast',async()=>{
  const {instance,store,toasts}=harness('https://source.example.test/cancel-target');
  let capturedSignal;
  globalThis.fetch=async(_url,{signal})=>{
    capturedSignal=signal;
    return new Promise((_resolve,reject)=>signal.addEventListener('abort',()=>reject(new DOMException('Aborted','AbortError')),{once:true}));
  };
  const pending=instance.parseVideo();
  assert.equal(instance.parseButton.isLoading.value,true);
  instance.cancelParse();
  await pending;
  assert.equal(capturedSignal.aborted,true);
  assert.equal(instance.parseButton.isLoading.value,false);
  assert.equal(instance.parseError.value,'');
  assert.equal(toasts.length,0);
  assert.equal(instance.inputUrl.value,'https://source.example.test/cancel-target');

  globalThis.fetch=async()=>Response.json(good({title:'After Cancel'}));
  await instance.parseVideo();
  assert.equal(store.resultData?.title,'After Cancel');

  // Specific platform (currentPlatform !== 'all'): isLoading becomes true synchronously while lazy parseHelpers loads, and cancelParse + subsequent retry never deadlock active
  const spec=harness('https://www.douyin.com/video/7400000000000000000');
  spec.instance.selectPlatform('douyin');
  let specFetchCalls=0;
  globalThis.fetch=async()=>{specFetchCalls++;return Response.json(good({platform:'douyin',title:'Douyin After Cancel'}));};
  const specPending=spec.instance.parseVideo();
  assert.equal(spec.instance.parseButton.isLoading.value,true);
  spec.instance.cancelParse();
  await specPending;
  assert.equal(specFetchCalls,0);
  assert.equal(spec.instance.parseButton.isLoading.value,false);
  assert.equal(spec.instance.parseError.value,'');
  await spec.instance.parseVideo();
  assert.equal(specFetchCalls,1);
  assert.equal(spec.store.resultData?.title,'Douyin After Cancel');
});

test('YouTube mobile & desktop helper detection: mobile UA + helper succeeds identically to desktop; mobile UA without helper shows userscript/desktop prompt; desktop UA without helper keeps install guide',async()=>{
  const origNavDesc=Object.getOwnPropertyDescriptor(globalThis,'navigator');
  const prevWin=globalThis.window;
  try {
    // 1. 手机 UA + 已检测到助手时不被拦截（与桌面走完全相同的 context -> player -> finalize 路径）
    for (const mobileUa of [
      'Mozilla/5.0 (Android 14; Mobile; rv:131.0) Gecko/131.0 Firefox/131.0',
      'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36 EdgA/131.0.0.0',
    ]) {
      Object.defineProperty(globalThis,'navigator',{configurable:true,value:{userAgent:mobileUa}});
      const bus=new EventTarget();
      bus.setTimeout=setTimeout;
      bus.clearTimeout=clearTimeout;
      globalThis.window=bus;
      const unmount=ytHelperCore.createUserscriptController({
        eventTarget:bus,
        version:ytHelperCore.YOUTUBE_HELPER_VERSION,
        gmXmlhttpRequest(details){
          if(details.url.includes('sw.js_data')){
            details.onload({status:200,responseText:'["Cgt0ZXN0VmlzaXRvckRhdGFTdHJpbmc%3D"]'});
            return;
          }
          details.onload({
            status:200,
            responseText:JSON.stringify({
              playabilityStatus:{status:'OK'},
              videoDetails:{title:'Mobile Firefox Helper Video',author:'Creator',lengthSeconds:'42'},
              streamingData:{formats:[{itag:18,url:'https://rr1---sn-test.googlevideo.com/videoplayback?itag=18',mimeType:'video/mp4; codecs="avc1.42001E, mp4a.40.2"',width:640,height:360}],adaptiveFormats:[]},
            }),
          });
        },
      }).mount();
      try {
        const apiPaths=[];
        globalThis.fetch=async(url,init)=>{
          const u=new URL(url,'https://snap.example.test');
          apiPaths.push(u.pathname);
          if(u.pathname==='/api/youtube/context'){
            return Response.json({schema_version:'1.0',code:'OK',data:{player_id:'22f02d3d',signature_timestamp:20760,client:{name:'TVHTML5',version:'7.20260728.13.00'}}});
          }
          if(u.pathname==='/api/youtube/finalize'){
            const body=JSON.parse(init.body);
            assert.equal(body.video_id,'I9JvDaciaFk');
            return Response.json(good({platform:'youtube',title:body.title,items:[file('yt-1','https://rr1---sn-test.googlevideo.com/videoplayback?itag=18')]}));
          }
          throw new Error(`unexpected request: ${u.pathname}`);
        };
        const mobileReady=harness('https://www.youtube.com/watch?v=I9JvDaciaFk');
        await mobileReady.instance.parseVideo();
        assert.deepEqual(apiPaths,['/api/youtube/context','/api/youtube/finalize']);
        assert.equal(mobileReady.instance.parseError.value,'');
        assert.equal(mobileReady.store.resultData?.title,'Mobile Firefox Helper Video');
        assert.equal(mobileReady.store.resultData?.extra?.youtube_helper?.status,'ready');
      } finally {
        unmount();
      }
    }

    // 2. 手机 UA + 未检测到助手时把安装说明换成提示（不发 /api/parse，不弹桌面安装说明）
    globalThis.window={setTimeout,clearTimeout};
    let fetchCount=0;
    globalThis.fetch=async()=>{fetchCount++;return Response.json(good());};
    for(const ua of [
      'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36',
      'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1',
    ]) {
      Object.defineProperty(globalThis,'navigator',{configurable:true,value:{userAgent:ua}});
      let pingCalls=0;
      const zhStore=reactive({resultData:null,resultVersion:0,clearResult(){this.resultData=null;this.resultVersion++;},setResult(d){this.resultData=d;this.resultVersion++;}});
      const zhToasts=[];
      const zhInst=parser.useVideoParser({
        videoStore:zhStore,
        locale:ref('zh-CN'),
        showToast:(...a)=>zhToasts.push(a),
        pingHelper:async()=>{pingCalls++;return {installed:false,version:null,supported:false};},
      });
      zhInst.inputUrl.value='https://www.youtube.com/watch?v=I9JvDaciaFk';
      await zhInst.parseVideo();
      assert.equal(pingCalls,1,'must enter parseYouTubeWithHelper and ping helper before deciding');
      assert.equal(fetchCount,0);
      assert.equal(zhStore.resultData,null);
      assert.match(zhInst.parseError.value,/用户脚本管理器/);
      assert.match(zhInst.parseError.value,/电脑/);
      assert.equal(zhToasts.at(-1)?.[1],'error');

      const en=harness('https://youtu.be/I9JvDaciaFk','en');
      await en.instance.parseVideo();
      assert.equal(fetchCount,0);
      assert.equal(en.store.resultData,null);
      assert.match(en.instance.parseError.value,/userscript manager/i);
    }

    // 3. 桌面 UA + 未检测到助手时仍显示原安装说明
    Object.defineProperty(globalThis,'navigator',{configurable:true,value:{userAgent:'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36'}});
    globalThis.fetch=async()=>Response.json(good({platform:'youtube',items:[file('yt-1','https://rr1---sn-test.googlevideo.com/videoplayback?itag=18')]}));
    const desktopMissing=harness('https://www.youtube.com/watch?v=I9JvDaciaFk');
    await desktopMissing.instance.parseVideo();
    assert.equal(desktopMissing.instance.parseError.value,'');
    assert.equal(desktopMissing.store.resultData?.extra?.youtube_helper?.status,'missing');
    assert.match(desktopMissing.store.resultData?.extra?.youtube_helper?.message || '',/请按说明安装并启用后重试/);
    assert.doesNotMatch(JSON.stringify(desktopMissing.store.resultData),/googlevideo\.com/);

    // 4. Non-YouTube links on mobile still parse normally
    fetchCount=0;
    Object.defineProperty(globalThis,'navigator',{configurable:true,value:{userAgent:'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) Mobile/15E148'}});
    globalThis.fetch=async()=>{fetchCount++;return Response.json(good({platform:'douyin',title:'Mobile Douyin'}));};
    const dy=harness('https://www.douyin.com/video/7400000000000000000');
    await dy.instance.parseVideo();
    assert.equal(fetchCount,1);
    assert.equal(dy.store.resultData?.title,'Mobile Douyin');
  } finally {
    globalThis.window=prevWin;
    if(origNavDesc) Object.defineProperty(globalThis,'navigator',origNavDesc);
    else delete globalThis.navigator;
  }
});

});
