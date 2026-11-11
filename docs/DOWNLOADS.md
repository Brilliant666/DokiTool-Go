# Browser download delivery

The existing download button resolves the selected work, episode and source before
handing a complete file to `frontend/src/downloads/manager.ts`. Parser APIs now use
the [unified media v1 contract](API.md); the confirmed layout and existing download
transport routes are retained. Ordinary
CDN files travel directly to the user's browser.
Weibo's signed `/api/media/weibo` route remains a server relay when required.

## Production download capabilities

下表区分生产按钮验收、浏览器直接打开附件的实测，以及需要手动保存的入口。结论仅限所测作品、版本和网络；截至 [#33 发布回执](https://github.com/Brilliant666/DokiTool-Go/pull/33#issuecomment-6649301507)，日本 Go/Vue 为 `d82ff77`，生产下载 Worker 为 `downloads-20261104.1`；该轮因下述实况 ZIP 缺口尚未接收为新稳定基线。

| 平台 | 已确认的能力和边界 |
| --- | --- |
| 抖音 | 上海主动领取取址已启用，4K 原画有[生产基线及维护者保存记录](go-migration/douyin-shanghai.md)。上海提供候选地址，文件由浏览器从 CDN 下载。 |
| 小红书实况 | **生产 d82ff77 仍有新增 HTTP v28 导致 ZIP 失败的缺口。** 本次修复候选对适配器已提取的实况 motion 直连来源做同址 HTTPS 准备，不再枚举 CDN 节点或域名；尚未部署，也不等同于声明每个来源均可跨域读取。历史 #30 只覆盖 v11/v27：一笔生产样本的两个地址均已升级，原按钮保存了视频、对应封面及同组 ZIP；单视频和 ZIP 中的视频实取 v11，封面沿用自己的来源。视频和封面完整解码，ZIP 通过 CRC，成员与单项的长度及 SHA-256 一致。v27 未单独整存；这不是小红书 4K 全量验收。[发布回执](https://github.com/Brilliant666/DokiTool-Go/pull/30#issuecomment-6628340403) · [Chat 接收记录（非独立生产复测）](https://github.com/Brilliant666/DokiTool-Go/pull/30#issuecomment-6629159922) |
| 今日头条 | 作品 `7501991960077500979` 原选 1080p：上游缺少允许网页读取的跨域响应头，带 `Content-Disposition: attachment` 且不校验 Referer；下载按钮已将 `*.toutiaovod.com` 接入隔离的浏览器原生附件路线（`noopener`、`no-referrer`，任务状态记为 `handed-off`「已交给浏览器」，不读取跨域字节、不经服务端中转；本地验证通过，生产保存结果待上线后补）。独立 Edge 此前直接接收该附件时，以系统命名 `download.mp4` 实际保存 25,912,280 字节，H.264 视频和 AAC 音轨全片解码通过。进度由浏览器下载管理器管理，页面只能确认交接。此前一次仅收到 15,662 字节，未证实慢的原因。 |
| 皮皮搞笑、最右 | 已测作品（分别为 `860538173891`、`163566379`）的 HTTP 媒体被浏览器拦截。同址 HTTPS 可预览，但不允许网页跨域读取，直接打开也未触发附件下载。保留「打开原视频，手动保存」入口；该轮未发现原域名上可一键保存的候选；未启动中继。 |

后三个平台引自 2026-11-04 本地验收原件《三平台无中继保存能力：最终结果记录》。其中只有头条新增一次原生附件保存；皮皮搞笑与最右复用已有记录。非浏览器客户端下载成功不计为网页一键保存。

### 皮皮搞笑 / 最右：tbvideo 候选（已随 d82ff77 发布，分平台开关）

按 #33 回执，生产已显式开启皮皮搞笑、关闭最右，相关 Worker 模块与路由已核对；本轮没有新增这两平台的真实保存验收。下述仍是候选交付能力，不是全平台成功率保证。

新版本保留原播放器地址。单视频下载按钮只尝试所选无水印视频在 `https://tbvideo.ixiaochuan.cn` 的同路径对象，不带签名、查询参数或凭据。每个视频按自己的来源 ID 绑定；没有候选、浏览器不能读取或文件校验不通过，就保留「打开源链接」手动保存，不向原地址再次请求下载字节。媒体不经本站服务器。多视频可逐项保存；该候选不提供 ZIP 打包。

日本 Go 的 `DOKITOOL_TBVIDEO_REWRITE_PIPIGX` 和 `DOKITOOL_TBVIDEO_REWRITE_ZUIYOU` 默认开启。只撤回一个平台时，将对应环境变量设为 `false` 并重启日本 Go，无需改代码、重新构建或更新上海服务；重新解析后生效，已生成的结果和在途任务不被改写。新客户端遇到不支持该能力的旧 Worker 时直接回落，不要求清缓存。

上游是否允许跨域读取以**实际 GET** 为准。最右的不同对象可能返回不同的跨域头；OPTIONS 通过不代表 GET 可读。不给同一失败对象自动重试或加随机查询串。下载管理器只在收到完整、已核验的 MP4 后接收附件；客户端的 `saved` 指已验证文件交给浏览器，不代表站点读过用户磁盘。

日本后端接收同源 `POST /api/downloads/tbvideo-result`，只记录 `tbvideo_save` 事件的 platform、pid、vid、used_candidate、result（saved/fallback）、total_bytes、elapsed_ms，不记录媒体 URL。服务端对上报限流；用户取消、页面关闭、连接中断、旧版数据缺少视频身份等都可能没有结果记录。若获准上线，观察一周：最右以实际尝试候选的 saved / (saved + fallback) 统计**已上报**成功率，开关关闭或旧 Worker 的手动回落另计。不要用这个比例代表所有对象或网络。

## Historical download evidence

The [production incident record](incidents/INC-20261031-001-download-delivery.md) and
[PR #11 closeout](UNIFIED-MEDIA-ACCEPTANCE.md#pr11-closeout) preserve earlier failures
and their scope. Controlled refresh and upgrade results are in the
[unified-media acceptance record](UNIFIED-MEDIA-ACCEPTANCE.md#下载恢复与候选精简验收2026-10-31).

As of the 2026-10-31 closeout, the owner reports three normal production purple-button
saves of approximately 540 MB, with playable beginning/end, audio and completed
cards. This updates the earlier normal-parser acceptance gap. Re-parsing occurred
between attempts: these are user functional results, not a matched-URL benchmark
or exact hash verification of the historical 523,069,472-byte original. No new
throughput patch was deployed to cause these successes. Historical partial-transfer
failures remain documented without a proven single cause.

The frozen a8 URL was reused after a time encoded in its path without checking its
validity window. The resulting 403/zero-media attempts cannot support SW or route
performance conclusions. The incident report explicitly records this serious
investigation error, excessive repeated manual steps and the required stop rules.
Do not repeat the old URL or the completed download matrix. At that checkpoint, probe compatibility
and automatic expired-result recovery awaited release; the latter required
post-release browser verification, preferably using an existing trusted terminal
record without another transfer. The historical sections below do not prescribe
new tests. A temporary source replacement alone still is not normal-parser acceptance.

## Unified media input boundary

The backend selects each item's complete-file `default`. The frontend validates
its local variant/source reference and reads it without ranking alternatives.
An explicit user selection remains separate from that default; invalid/expired
references require an explicit refresh or selection instead of silently using a
different quality. Multi-part resolution uses the backend's opaque item ID.

New download plans freeze the selected item, version, source, filename and routes
before entering the existing manager. Later parsing and resolution cannot rewrite
an active plan. File delivery continues to use native attachment eligibility,
Service Worker streaming and capability-only Blob fallback. The unified schema
switch retained PR #7 lifecycle behavior; the current recovery change adds durable
outcome records and bounded observation, without changing frozen media plans,
admission, transport selection or natural Worker upgrades.

Explicit HLS, validated byte-concatenable segments and live-photo pairs use their
corresponding media preparation paths. DASH/mux/remux are preserved and explained
as unsupported, never downloaded as naked video tracks. A deferred variant has no
delivery URL and cannot be passed to the manager. The historical browser records
below prove PR #7 lifecycle behavior; they are not new end-to-end acceptance of
the unified parser or of any real platform's current availability.

## Policy and modules

1. `native.ts`: visible attachment headers and a safe filename with the correct
   media extension establish eligibility. Video format detection shares its
   complete HTTP 200 GET response with this check: it reads the first chunk, then
   cancels. Partial, failed, opaque or non-media responses cannot supply a cached
   decision. Other entry points use a three-second GET probe. Hidden or unavailable
   headers mean unknown eligibility.
   Qualified attachments use a hidden, sandboxed iframe, avoiding an asynchronous
   popup. The server chooses the actual filename. Status is **handed off**, never
   verified completion; the iframe remains until the document is destroyed because
   native completion cannot be observed safely.
2. `streaming.ts`, `session.ts`, `bridge.ts`, `public/downloads/worker.js` and `task-store.js`: other readable complete files
   use a download-only Service Worker. It supplies an attachment filename and
   forwards bytes with backpressure; it never buffers the entire file. Only the
   `/downloads/` scope is controlled, not the application or API. Sources still
   require CORS; no new proxy is introduced.
   A separate hidden `public/downloads/bridge.html` is a real controlled document,
   retained while tasks are preparing, active or uncertain. It carries no media
   bytes and uses no heartbeat or whole-transfer `waitUntil`.
3. `blob.ts`: a compatibility path only when streaming capability is definitely
   unavailable before START. There is no fixed file-size cutoff; unknown or encoded
   lengths can download without fabricated percentages. Known decoded lengths are
   still checked. One Blob transfer runs per page, and object URLs retain the
   existing four-second release delay. Chunks are released after success/failure;
   allocation or delivery errors do not retry the whole file on other routes.
   A missing browser capability permits fallback; registration, bridge-loading or
   protocol failures do not. CORS errors, source failures and ambiguous START
   acknowledgements never silently fall back. Blob still buffers the complete file.

`file.ts` handles URLs, format evidence and network waits. Page transports and the Worker
share the filename rules in `media-format.js` and decoded-byte accounting in `decoded-length.js`.
The manager
limits preparation/transfers to two concurrent tasks per page, the Worker also
limits transfers across tabs to two, and Blob has its stricter memory queue.
Native transfers leave application control and cannot count toward an observable
browser-wide concurrency budget.

Worker `downloads-20261031.1` advertises admission and recovery support in its protocol-1 ping.
New clients queue START before opening an attachment request. Waiting has no total
deadline and sends no media GET. A ready task reserves a slot and has 60 seconds
for the browser to claim the attachment; only then do the 120-second preparation
deadline and source attempts begin. Returning attachment headers clears that
deadline; it never limits the streaming body. A maximum of 100 unfinished tasks is
independent of the last 100 terminal summaries, which expire after 24 hours.

Compatibility is negotiated with each task's actual owner. A new client with an
old Worker uses the original attachment URL. An old client with the current Worker retains its
original URL and 120-second combined queue/preparation deadline. Both generations
share the same two transfer slots. START messages finish after acknowledgement;
neither message nor fetch events wait for the whole file through `waitUntil`.

## Ownership, recovery and failure

- Every attempt receives a distinct task ID. The Worker requires a random ownership
  token and a same-origin client. Small summaries in sessionStorage omit signed
  CDN URLs. All unfinished IDs/tokens survive history trimming; at most 100 terminal
  summaries remain for 24 hours after confirmation. Worker terminal history does
  not consume the active-task quota, and authenticated cleanup rejects live tasks.
- Same-origin IndexedDB stores only IDs/access tokens, original build/execution ID,
  lifecycle timestamps/revisions and small filename/byte summaries. It stores no
  media bytes or source URLs. Every Worker execution generates a new instance ID,
  including restarts of the same build. A new execution can report an old durable
  terminal, but cannot adopt its old active stream. This is outcome recovery, not
  network resume. Writes occur at lifecycle transitions, not on each media chunk.
  Storage operations have a 1.5-second bound; a storage failure disables further
  persistence in that execution and never turns a successful transfer into failure.
  Terminal persistence is attempted before notification and in-memory cleanup;
  trusted live outcomes remain valid when persistence fails. Only short result
  writes may receive event lifetime protection, never the complete transfer.
- Refresh queries the existing Worker, re-subscribes and permits cancellation. It
  never resends START or replaces an already claimed transfer. A task still waiting
  for admission can later become ready; an unclaimed ready task can be claimed once
  after refresh. Missing records, RPC timeouts or an ended owner without a confirmed
  terminal mean **unconfirmed**, not failed. Temporary communication failure
  preserves the latest progress and can recover on a later reply. Saved progress omits
  signed URLs and is throttled to at most one sessionStorage write per five seconds.
- `streaming.ts` owns one record per task, with a single reducer for push/query/ACK
  snapshots. Revision ordering is scoped to the execution identity. Authoritative
  completed/failed/cancelled outcomes are absorbing; observation failure is not a
  terminal. Late evidence from the same execution can correct an unconfirmed card,
  but a new execution or stale revision cannot overwrite it. Legacy Workers use
  monotonic bytes/stages instead. Old client-inferred `interrupted` summaries are
  rechecked; confirmed terminal summaries never downgrade because a query is empty.
  Cancellation intent is persisted immediately and prevents unclaimed delivery;
  only a Worker outcome confirms cancellation. A refresh retries that idempotent
  cancel on the same owner, never starts another download.
- Automatic observation recovery lasts a fixed 60 seconds from the first lost
  observation. Empty queries, retries and page refresh do not extend that deadline.
  Trusted activity or a terminal ends recovery early. At expiry, polling stops and
  the compact history row says “无法确认下载结果，请查看浏览器下载列表。” with
  “重新查询状态”. That explicit action starts another bounded query window, never
  START, cancellation, transport fallback or a second media request. Sixty seconds
  is not a transfer duration limit. Restoring an already expired record makes one
  authenticated result-only query per task and page, without renewing the deadline
  or restarting polling. A trusted terminal can correct the card; active replies,
  subscribed progress pushes and missing results keep it unconfirmed. The explicit
  query button remains available. A possible live owner keeps its bridge; confirmed
  termination or confirmed execution loss permits release. Unconfirmed history is
  excluded from the panel's active count.
- Legacy summaries without an execution ID use their known build as fallback
  ownership evidence: a different known build from the verified active-worker
  handshake proves that execution has retired. Missing/`legacy` build markers or
  query misses alone do not. A retired task still queries for a trusted outcome;
  without one it stays unconfirmed, releases its bridge and can be cleared after
  the recovery window, without restarting or cancelling a download.
- `session.ts` coalesces discovery and the capability handshake. It distinguishes
  explicit refusals from lost replies; a lost START acknowledgement keeps an
  uncertain task rather than permitting Blob or another START. Dead page subscribers
  are removed from the Worker during progress delivery.
- A source can change to one of up to three frozen, equivalent alternatives only
  before the attachment response starts. After delivery starts, a stream error
  fails that attempt. There is no automatic cross-source Range resume or byte
  splicing. Retrying creates a new task; matching resolution is not proof of byte
  identity.
- The application-defined Worker network timeout applies to an outstanding
  upstream read or header wait (45 seconds), not downstream backpressure. Blob
  uses 30-second network waits. Neither defines a total-duration limit; browser
  event-lifetime limits are separate (see the historical investigation below).
- The attachment Request abort signal and response-stream cancellation both stop
  the task and release its queue slot. Fifteen seconds without downstream demand
  reports stalled observation without cancelling a normal browser pause. If the
  browser exposes neither cancellation event, the page cannot confirm a stop;
  explicit cancellation remains available. Two simultaneous transfers remain the
  limit; no speculative replacement transfer is started.
- HTTP 206/Content-Range are not accepted as complete files. Known length, empty
  bodies and media/error-page responses are checked. Unknown source length remains
  unknown in the UI. `loaded === total` alone never establishes completion: real
  upstream EOF, integrity checks and normal response stream close are required.
  Stream completion means bytes were handed to the browser,
  not independently verified disk persistence.
- A source's declared file size applies only to that source URL. Fetch can decode
  a CDN response while CORS hides `Content-Encoding`. In that case the wire
  `Content-Length` cannot bound the decoded stream. When both the source size
  and a trustworthy identity length are absent, the attachment omits its length
  and counts delivered bytes until EOF. A reader error still fails the task.
- A supported format established by the selected variant can supply the safe
  filename suffix and attachment MIME. A probed QuickTime file keeps its MOV
  name if the CDN serves it as `video/mp4`. Without format evidence the
  transport falls back to the observed media type.
- Manual opening of a source is available only after confirmed failure,
  cancellation or loss of the original task. It is absent from active/uncertain
  cards; a stale click cannot cancel the running transfer or create a second one.
- Total elapsed time starts at the user's click. Transfer duration and average
  speed start at byte delivery, excluding preparation/queue waits. Instantaneous
  speed is smoothed; metrics describe stream delivery, not verified disk writes.
- Bilibili lazy episode selection, segmented/HLS merging and ZIP assembly keep
  their existing pipelines. ZIP and segmented downloads are not claimed to be
  streaming or refresh-resumable by this change.

### Cancellation diagnostics

Worker build `downloads-20261031.2` preserves a fixed cancellation cause in the
existing terminal snapshot `error` field. The first terminal event wins:

| Detail | Observed event |
| --- | --- |
| 页面请求取消下载 | The Worker received the page's cancel request. |
| 浏览器停止接收下载数据 | The attachment response stream was cancelled by its consumer. |
| 浏览器中止下载请求 | The attachment request's abort signal fired. |

These messages identify an event, not the underlying browser policy or the user's
intent. Raw cancellation reasons, task tokens and signed URLs are not recorded.
The manager and download card preserve the confirmed cause, including after
refresh recovery; older records without a cause remain unchanged. Unconfirmed
cancellation still remains unknown. This diagnostic change does not change
timeouts, source selection, fallbacks, cancellation semantics or Worker activation.
It does not by itself establish that the production download failure is fixed.

## Download panel

The summary bar expands a bounded, internally scrolling task list. Active and
uncertain tasks come first; ended records keep compact status rows with optional
details. Completion means stream delivery, not verified disk persistence.
Clearing ended records is available while other tasks run. An unconfirmed record
can be cleared only after its recovery window expires and its original execution
is confirmed gone; possibly live unknown tasks remain protected. Clearing history
does not send cancellation, establish a result or delete saved files.

Folding only changes presentation. A new active task opens the panel unless the
user has manually folded it; that preference is stored locally when available.
After all tasks end, the panel folds after five seconds without pointer hover or
keyboard focus. Automatic folding does not overwrite the manual preference.

### Explicit link refresh

Owned, settled single-file failures can offer **刷新链接并重试** in the details.
Unlike ordinary retry, this user action calls the original item's resolver once,
even when the old URL has no expiry timestamp. It retains the pinned item,
variant and source IDs; a missing selection fails rather than borrowing a new
default. The existing 30-second preparation deadline, cancellation and Worker
acknowledgement protect the old/new attempt boundary. This does not diagnose all
403 responses as expired and never starts an automatic refresh loop.

Only in-memory tasks with an identified file source and resolver are eligible.
Restored URL-only history, archives, Live pairs, active/unknown, completed or
cancelled tasks do not gain a guessed recovery path. Fresh source size/format
metadata replaces the prior attempt's data; this is a fresh download, not a byte
resume. Existing transport fallback among the same variant's sources is unchanged.
See [implementation evidence and remaining identity limitation](https://github.com/Brilliant666/DokiTool-Go/blob/7a44c02fdc19fa85e7b959d9146e5e9ae322b188/docs/go-migration/refresh-recovery.md).

## Deployment

Publish the entire `frontend/dist/downloads/` directory along with its frontend build. The module Worker imports its task store, decoded-length and media-format helpers from that directory.
Verify the existing `/downloads/*` static-file rules against
`ops/examples/snap.caddy`, adding the block only if missing: use revalidation
(`Cache-Control: no-cache`), correct JavaScript MIME, and **404 for missing files**.
Do not route a missing Worker or `/downloads/file/*` to the SPA. The latter URL is
answered by an active Worker only. HTTPS (or localhost) is required.

The client waits for activation even when registration already exposes an active
Worker object whose state is still `activating`. It checks `registration.update()`
once per page, only after the bridge is loaded and its controller is verified.
There is no forced `skipWaiting`. Merely
omitting `skipWaiting` is insufficient: Chromium can retire an old Worker without
controlled clients after a waiting version has existed for five minutes, even
while it streams an attachment. An attachment iframe does not commit a controlled
HTML document. The separate bridge preserves that control relationship instead.

Each task pins its owner Worker for progress queries and cancellation. Installing
an update must not redirect those operations to the new active Worker. Multiple
tasks share a bridge; uncertainty keeps it alive, and the last confirmed terminal
task releases it after the existing four-second handoff grace period. A new task
cancels pending release. Refresh rebuilds the bridge before querying unfinished
IDs/tokens, without START or a replacement GET for a claimed transfer. Completed history alone
does not hold a bridge. Closing all pages or suspending the browser remains outside
this guarantee; the bridge is not a native background download service.

Worker build `downloads-20261031.1` and a fresh per-execution instance are returned
by ping/query/progress and exposed as `data-worker-build` on download cards; an old
Worker without a build marker is labelled `legacy` in this diagnostic attribute.
`data-worker-instance` and `data-task-id` allow inspection of execution/task
continuity without exposing an access token or signed source URL.
Bump BUILD when changing Worker behavior. Legacy statistics keep their original
meaning until that Worker retires. Future protocol-breaking updates need a
migration plan. The download modules load on demand; an empty-history homepage
does not register a Worker or load its client.

## Acceptance

The recovery regression suite exercises the real Worker and page modules with
deterministic IndexedDB, client and timer fault injection. It covers a lost terminal
notification followed by a same-build restart, stale active records, bounded storage
failure, 60-second observation expiry/manual query, legacy inferred interruption,
execution-scoped revisions, history pruning and real EOF. Run from `frontend/`:
`node --test --test-concurrency=1 tests/download-worker.test.mjs tests/download-streaming.test.mjs tests/download-panel.test.mjs`.
These are deterministic regressions, not browser disk-save acceptance. See the
current [unified media acceptance record](UNIFIED-MEDIA-ACCEPTANCE.md) for final-head
browser outcomes. The historical full-file-save/page-interruption observation below
does not establish a unique cause; the proven in-memory-history gap and the new
durable recovery behavior must remain distinct from that historical attribution.

Automated tests cover native eligibility, serial Blob, delivery selection,
concurrency, cancellation, Worker streams, first-byte failover, truncation, token
ownership, missing tasks, uncertain acknowledgements, late completion and refresh
queries. Existing UI tests retain selection, Bilibili, Weibo and ZIP regressions.
These tests are not a substitute for browser-managed file saving.

The browser acceptance matrix includes saved bytes/name/playability in Chrome,
Edge and iOS Safari/WeChat; first registration, refresh during transfer,
cancellation after refresh, slow sources, disconnects, old/new Worker versions
and both fallback paths. Record untested cells explicitly: one browser's passing
result does not validate another. Closing a browser or OS suspension is not
guaranteed to preserve a transfer.

### Initial PR acceptance, before revision, 2026-10-30

- 126 frontend tests pass, including transport selection, bounded Blob, Worker
  recovery, duplicate prevention while status is unknown, and cancellation racing
  completion. Type checking, production build and four prerender checks pass.
- Chrome: a controlled cross-origin 32 MiB MP4 was saved with the requested
  filename and SHA-256 matching the fixture. A refresh during transfer continued
  the same upstream GET; the completed file still matched. Refresh then cancel
  stopped that request before completion.
- Chrome: the production API response for the original Douyin sample was loaded
  into the local production preview. The browser fetched the real CDN directly,
  saving a 60,342,212-byte MP4 under its work title. File inspection found HEVC
  2160 x 3840, AAC, duration 9.15 seconds. This checks local delivery against a real
  CDN response; it is not an acceptance run of a deployed new frontend.
- Edge: first registration, streaming, refresh recovery and transfer completion
  were observed with the controlled source. The saved file location was not
  confirmed, so disk persistence is still unverified there.
- Native attachment qualification and handoff pass automated checks. Actual
  native saving remains unverified: the browser test produced
  `ERR_BLOCKED_BY_CLIENT`, and the attachment GET ended before receiving bytes.
  The source of the client block is undetermined. Browser settings were not
  changed, and uncertain handoff does not trigger another SW/Blob download.
- Still pending: native disk saving in an unblocked browser, real-browser Blob
  fallback, iOS Safari/WeChat, closing the page/browser, OS suspension, and a
  deployed old/new Worker upgrade with active downloads. Slow-source testing used
  a controlled HTTP server, not CDP network/CPU throttling.

### Seven-issue revision acceptance, 2026-10-30

- 154 frontend tests pass. Type checking, production build, all four prerender
  checks, initial-load budgets and the 156-file public inventory pass. Regression
  coverage includes request-abort cancellation, queue release, stalled readers,
  uncertain queries retaining progress, late cancellation replies, Worker update
  races, probe reuse, unknown-size Blob and a real 65 MiB Blob in the test runtime.
- Chrome production preview, controlled cross-origin 32 MiB MP4: the active card
  reports `browser_streaming` and Worker `downloads-20261030.2`. Refresh retained
  progress and the same source GET. Page cancellation closed that GET at
  17,301,504 bytes; a manual retry transferred all 33,554,432 bytes. No source-link
  shortcut was visible while active; it appeared after confirmed cancellation.
- Chrome production preview, separate origin with the Worker script deliberately
  returning 404: the purple button selected `browser_blob` before any SW START.
  It received all 68,157,440 bytes (65 MiB) and handed off the named file. The media
  log shows one early-closed inspection GET and one full delivery GET, with no
  extra native qualification fetch. This revision verified browser delivery and
  UI completion; the resulting saved file location/hash was not independently
  verified. No browser security or permission settings were changed.
- The local source uses controlled pacing. These observations are not a CDN
  bandwidth comparison. SW concurrency, buffering, single-GET transfer and Range
  behavior are unchanged; throughput tuning waits for matched-source evidence.
- Still unverified for this revision: stopping from the browser download manager
  (page cancellation is a different action), native saving, final Blob disk
  persistence, Edge/iOS Safari/WeChat, page/browser closure, OS suspension, and a
  deployed Worker upgrade with active downloads. Request-abort/stream-cancel and
  update-race tests establish code behavior, not every browser's signal delivery.

### Matched-source investigation, 2026-10-30

The implementation under test is `bc8b448`, Worker `downloads-20261030.2`.
The baseline preview Worker is byte-identical to the tracked source. A private localhost
harness imports the unchanged transport modules; its raw and Worker-response
paths use the same browser, exact source URL, credentials, no-referrer policy and
`cache: no-store`. Both paths consume the response with a page reader and discard
the bytes. Runs are serial Raw / SW / SW / Raw. This isolates the SW data channel;
it does not measure the purple button's format probe, native downloads or disk IO.

| Source and consumer | Raw total seconds (two runs) | SW total seconds (two runs) | Raw read MiB/s | SW read MiB/s |
| --- | --- | --- | --- | --- |
| Edge, localhost 64 MiB fixture | 0.207 / 0.117 | 0.265 / 0.198 | 325.87 / 581.29 | 262.30 / 391.68 |
| In-app browser, one fresh Douyin CDN URL, 60,342,212 bytes | 7.138 / 9.968 | 7.018 / 5.698 | 9.37 / 5.90 | 8.48 / 10.50 |

Total seconds include preparation and first-byte wait. Read throughput starts at
the first actual reader chunk and ends at EOF. All four CDN transfers returned
HTTP 200 with the same URL fingerprint and length; this is not a content-hash
verification. Worker-reported CDN mean speed differs from reader timing by less
than 0.02%. The earlier saved CDN URL returned 403 and was excluded; one fresh
parse supplied the URL used for all four successful runs.

There is no stable SW slowdown in this small CDN sample. Two runs per path cannot
establish a performance ranking. The much faster localhost test shows possible
bridge/chunking overhead, but still transfers hundreds of MiB/s and cannot explain
the reported 447 KB/s on another CDN/video. No production buffering, concurrency,
cache policy or Range strategy was changed on this evidence.

Additional acceptance observations:

- Edge and the in-app browser both ended the native attachment connection before
  the complete fixture was sent. A direct user-clicked link to the same attachment
  in the in-app browser also ended early. The responsible browser/environment
  component is unconfirmed; this does not isolate the production iframe as the
  cause. Server write counters include socket buffering, not proven browser reads.
- In-app browser SW attachment delivery received a cancellation after 2 MiB of a
  64 MiB fixture, without a page cancel action. The Worker reported cancelled and
  released the source. A separate observation-only Worker run recorded
  `response-stream-cancel` at 1,179,648 bytes, rather than an RPC cancellation or
  request-signal abort. The reason the response consumer cancelled is unresolved.
  This verifies handling of an observed cancellation, not the untested browser
  download-manager Stop action.
- The actual purple button in the in-app browser selected Blob when the isolated
  Worker script returned 404, received all 65 MiB, and reported transfer completion.
  No matching file was found in the user's specified Downloads directory. Browser
  download-event collection also timed out; no pending Save As dialog was found.
  Blob final saving, as well as native/SW saving in this environment, remains
  unverified. Failed/cancelled delivery rows are excluded from throughput results.
- Earlier Chrome acceptance runs remain separate historical evidence; they do
  not turn these current in-app/browser saving outcomes into passes.

#### Historical blocker: approximately five-minute SW interruption

A 32 MiB fixture sends 24 KiB every 250 ms, continuously, with the same source
URL and no-store policy. It takes about 356 seconds to finish. The following
long-running controls distinguish source availability from Worker lifetime:

| Path | Total seconds | Received bytes | Result |
| --- | ---: | ---: | --- |
| Original Worker, in-app browser | 300.074 | 28,237,824 | Network error |
| Original Worker, Edge | 305.368 | 28,729,344 | Network error |
| Direct Fetch, in-app browser | 356.551 | 33,554,432 | Complete |
| Private lifetime experiment, in-app browser | 356.505 | 33,554,432 | Complete |

Both original Worker runs were still receiving data near 94 KB/s when they
failed. This is not the application's 45-second idle-read timeout. Direct Fetch
completing the same source rules out a five-minute source cutoff in this fixture.

The original Worker waits for `task.settled` inside the START message event and
also adds it to the attachment fetch event's `waitUntil`. Chromium has a
[five-minute extended-event deadline](https://raw.githubusercontent.com/chromium/chromium/main/third_party/blink/renderer/modules/service_worker/service_worker_event_queue.h).
Its [response stream holds a separate stay-awake token](https://raw.githubusercontent.com/chromium/chromium/main/third_party/blink/renderer/modules/service_worker/fetch_respond_with_observer.cc)
while streaming. These implementation details and the matched controls strongly
identify the long-lived event binding as the cause of this reproduced failure.
Edge's slightly later failure does not establish its precise internal timing.

The private experiment removes only those two whole-transfer waits, adds stop
origin observations and a distinct build marker, and retains the same response
stream, cancellation, source reader, concurrency and buffering. It uses no new
keep-alive heartbeat. Its complete 356.505-second run validates that direction
for this fixture; it is not a production patch or completed download-manager
acceptance. Both waits were removed together, so their individual contributions
have not been isolated.

Before release, implement and review the lifetime change, then repeat actual
file saving, refresh recovery and cancellation. Also handle or explicitly bound
cross-tab tasks waiting over five minutes for a concurrency slot: `attachment()`
currently waits for that slot before returning its Response. The experiment does
not resolve this separate queued-fetch deadline. Closing pages, mobile lock
screens and Safari remain outside this result. This SW finding cannot explain
interruptions from a player's native download command, which bypasses this SW.

At this investigation checkpoint, PR #6 stayed draft, unmerged and undeployed.
The approximately five-minute interruption was a confirmed acceptance blocker;
final saving and browser-specific behaviors above remained unverified. This
investigation changed documentation only; the subsequent implementation is below.

### Lifetime correction, 2026-10-30

Worker `downloads-20261030.3` removes both whole-transfer event waits and the
unused settled promise. A single 120-second deadline bounds the attachment's
cross-tab queue plus all source preparation attempts. Returning the response
clears this deadline. Expiry reports failure, aborts the source and releases its
slot without changing transports or affecting other transfers. Streaming
concurrency, buffering, Range behavior and the confirmed UI remain unchanged.

- 157 frontend tests pass, including three new lifetime/deadline cases. Type
  checking, production build, four prerender checks and initial-load budgets pass.
  An independent review found no blocking implementation issue. A final wording
  correction clarifies that source preparation includes time already queued;
  all 29 Worker/client tests pass again after that correction.
- The actual `.3` candidate in the in-app browser completed the paced 32 MiB
  response in 356.3621 seconds, receiving all 33,554,432 bytes. Its source log
  independently records a complete response. This crosses the former failure
  point while another task times out in the queue and a second stream is cancelled.
  The run preceded only the final preparation-error wording correction; that
  correction changes no transfer or lifetime logic. This is response-stream
  acceptance, not a saved-file hash check.
- In-app browser, two active responses occupying both slots: a third cross-tab
  attachment failed after 120.0195 seconds with zero bytes and no source GET.
  Both existing streams continued. Cancelling the second stream closed its source
  connection; a deliberate retry of the third task then received its complete
  3,001-byte fixture. This is page/response cancellation, not a download-manager
  Stop test. No automatic retry or transport fallback occurred.
- A separate 3,001-byte static attachment baseline bypasses all DokiTool download
  modules. In the in-app browser, the local server wrote the complete response,
  but no matching file appeared in the specified Downloads directory. Chrome's
  direct link visibly returned `ERR_BLOCKED_BY_CLIENT`. No browser permission or
  security setting was changed. This confirms a Chrome client block for that
  baseline, without identifying the responsible component or proving the cause
  of the in-app browser's missing file. Final saving remains unverified.

The reproduced long-response lifetime defect and queue boundary are fixed and
verified at the stream layer. At this implementation checkpoint, PR #6 remained
draft, unmerged and undeployed. The owner subsequently authorized review, merge
and production deployment with these limits disclosed. Actual saving,
download-manager Stop, refresh recovery on this final version, active Worker
upgrades and mobile/browser-lifecycle acceptance remain unverified; deployment
does not turn them into passing results.

### Release review, 2026-10-30

The final code review identified an asynchronous retry/clear race. A delayed
Worker acknowledgement could launch duplicate retries or remove a newer attempt
reusing the same download card. Retry and clear now share per-card ownership;
clearing all settled cards claims the whole batch before awaiting any reply.
Captured attempt fields are checked again after asynchronous operations, and the
retry retains ownership until its new call settles. This also covers legacy ZIP
and segmented downloads without a Worker delivery ID. These changes preserve
the layout, transport selection and backend API.

Seven new regression cases reproduced the races before the fix. The final
candidate passes all 164 frontend tests, type checking, the production build,
four prerender checks and initial-load budgets. These race tests use the same
object-replacement update behavior as the Pinia store. The streaming Worker is
unchanged from the long-response validation above; file-saving and browser
lifecycle limits remain as documented.

### Controlled-document upgrade fix, 2026-10-30

Post-release Edge acceptance isolated a second lifetime defect in `.3`: installing
a waiting Worker interrupted a paced download after 300.007 seconds of waiting.
The same source completed without an update. The attachment iframe remained an
empty document, so it did not keep the old Worker controlled. This is separate
from the whole-transfer event deadline corrected above.

At this checkpoint, the client retained the controlled bridge, pinned each task's original Worker,
and restores that relationship before querying after refresh. A completed task
can be cleared even if its old Worker has already retired. There are no Worker
algorithm, parser, native/Blob selection or layout changes; that checkpoint used
Worker `downloads-20261030.3`. The new HTML asset must accompany deployment.

- All 193 frontend tests, type checking, the production build, four prerender
  checks, initial-load budgets and the public-file inventory pass. An independent
  implementation review found no blocking issue. Coverage includes activating
  Workers, shared bridge ownership, refresh, uncertain states and owner retirement.
- Edge, fresh local origin and final production build: the normal purple button
  selected SW on its first download. A paced 32 MiB file completed in 390.641
  seconds while an update waited. A concurrent short task completed and the page
  refreshed at about 48 seconds; the long task retained its ID, build and single
  source GET. Both the update wait and the post-refresh interval exceeded five
  minutes without interruption. Player preloading and the short eligibility
  probe were distinguished from the actual download GET.
- The 33,554,432-byte saved file matched the fixture SHA-256
  `7babd0092a944517a76175f42c0fbdcdfec539b6a389063fe9f22b96c68e74ab`.
  After the last task finished, the bridge was released and the waiting Worker
  activated naturally. The next purple-button download used that new build.
  Both 3,001-byte short files also matched their source SHA-256 and retained the
  requested filenames (with the browser's duplicate-name suffix where needed).
- The private next Worker differed only in its BUILD marker. The observer stayed
  outside `/downloads/`, created no controlled helper document and sent no keep-
  alive messages. This checks the application's own bridge, not a test-page pin.

This is local Edge acceptance of an unchanged production bundle with controlled
cross-origin media. It is not a production deployment or a throughput benchmark.
Closing all pages, lock screens, browser suspension and Safari/WeChat remain
unverified. The already demonstrated native-download naming limitations remain;
this fix does not change native eligibility or claim browser-manager progress.

### State and admission consolidation, 2026-10-30

The `.4` revision separates session discovery, task state and Worker scheduling.
One client task record replaces the parallel owner/progress/watched/uncertain
collections. Pushes, queries and acknowledgements share revision ordering and
terminal-state protection. Cancellation intent prevents an unclaimed attachment
from starting even when ready arrives before the cancel reply; it survives refresh.
Connection failures are operational errors, not evidence of missing capability.

The Worker queues admission before opening an attachment fetch, removes vanished
page subscribers, and keeps terminal summaries outside unfinished-task capacity.
Client persistence keeps every unfinished task plus bounded completed history.
Ten simultaneously restored tasks share one handshake and one grouped query.
These changes leave the byte pump, source-read timeout, two-transfer budget,
cache/Range policy, native eligibility, Blob size policy, parser API and layout
unchanged. No throughput increase is claimed from this lifecycle work.

- All 227 frontend regressions pass, along with type checking, production build,
  four prerender checks, initial-load budgets and the 160-file public inventory.
  Independent review found and verified corrections for history trimming and
  cancellation arriving before admission; no blocking issue remained.
- Edge on a fresh local origin, final production bundle: two separate 32 MiB
  fixtures started through the normal purple buttons. Refresh retained both IDs
  and their original source requests. They completed in 391.125 and 390.965 seconds
  according to the independent source logs. Both saved files contained 33,554,432
  bytes and matched SHA-256
  `7babd0092a944517a76175f42c0fbdcdfec539b6a389063fe9f22b96c68e74ab`.
- A third task submitted after refresh remained queued across an explicitly
  observed 328.761-second interval, with no attachment iframe or source GET during
  that interval. It automatically delivered when the first slot became free; the
  same task completed after 363.460 seconds total. Its saved 3,001-byte file matched
  `9feddfc912e985422c47e5f0e43816c0b6426973f7d01d51f841d712ac8fac00`.
- An update waited throughout those transfers and activated naturally only after
  their completion and bridge release. A subsequent purple-button download used
  the new build and saved another matching short file. Refreshing the four terminal
  records and clearing them removed storage entries and cards without creating a
  bridge. The next Worker differed only in BUILD; this does not test a future
  protocol-breaking update.
- A separate origin served the unchanged `.3` Worker to the new client. Its normal
  purple-button download used the legacy delivery path and saved the correctly
  named, hash-matching 3,001-byte file. Automated tests also cover an old client
  talking to `.4`; that direction was not repeated in a real browser here.

Five actual Edge files were independently hashed. The controlled, paced fixtures
exercise lifetime, queueing, refresh and integrity; they are not a CDN speed test.
The inspection page stayed outside the Worker scope and issued no keep-alive or
forced-activation messages. This is local acceptance, not production deployment;
Safari/WeChat, lock screens, suspension and closing all pages remain unverified.

### Task panel and theme acceptance, 2026-10-30

The final panel revision passes 244 frontend regressions, including 16 panel
interaction cases; type checking, production build and four prerender checks pass.
Edge and the in-app browser verified collapsed histories of 13 cancelled or
completed tasks, bounded internal scrolling, keyboard details, progress while
folded, and cleanup that preserves active tasks. A 390px viewport checked the
compact layout; it is not an iPhone device test. A removed last row resets hover
and focus state so that a later completed batch can still fold automatically.

The page, header, hero and download panel share theme-color timing. Platform
cards use a fixed alpha mask instead of switching white/dark edge overlays;
theme changes preserve the mask, the 50-second scrolling animation and card
dimensions. Browser inspection and a fresh production build verified this
CSS-only follow-up. Particle animation and the established page layout remain.

These presentation changes do not modify the SW implementation validated above,
its data path, or the backend API. "Transfer complete" continues to mean bytes
handed to the browser; the browser's download list and actual file determine
whether saving succeeded.

### Production incident: fixed-source observations, 2026-10-31

PR #11 keeps the production download incident open. The bridge header/cache
repair and Worker `.2` cancellation diagnostics are distinct from a throughput
fix. Two user-created production `.2` tasks failed with `network error` after
25 and 115 seconds, at displayed averages of approximately 180 and 253 KB/s.
These failures occurred after media transfer started; an old Worker or a
pre-START bridge failure does not explain them. The historical selected URLs
were not captured, so the current player selection cannot establish their source.

A separate, fixed CDN URL returned a 43,074,063-byte HEVC/AAC file. The following
observations use that exact URL and retain failures as well as successes:

| Path | Observation | What it establishes |
| --- | --- | --- |
| In-app browser, native video-menu download | Actual file saved; filesystem write interval about 116 s | Complete file, not a precise network throughput measurement |
| In-app browser, localhost ordinary-page SW download | Output stream cancelled by the browser after the first 14,725 bytes | Consumer-side cancellation in this tool/browser path, not a proven universal SW defect |
| Same browser, localhost raw Fetch | 43,074,063 bytes read in 3.460 s | Full readable response; bytes discarded, no disk-save claim |
| Same browser, localhost controlled-page SW read | Full response in 2.288 s, Worker completed | Byte pump can read the file; the extra controlled document makes this unsuitable for lifecycle acceptance |
| Chrome, localhost ordinary-page SW download | Worker completed in 6.183 s; actual 43,074,063-byte saved file independently verified | Real SW delivery and saved-file integrity on localhost; task timing is not exact disk-save timing |

The two actual saved files have SHA-256
`518abc4327148cd6ed5a0f05542eb89349e42ce070728463b01e137aaffb6115`,
matching the previously saved local file. The native file also passed a complete
packet scan: HEVC 2160x3840 and AAC, approximately 123.7 seconds. This is not an
acceptance result for the 523 MB original or a production SW save.

The fixture uses the exact `.2` Worker and official streaming modules, one URL
and no backup routes. It skips the purple button's native-eligibility GET.
Consequently its throughput cannot alone establish the production button's
behavior. These direct-CDN downloads do not relay media bytes through the
application server; the existing Weibo media relay is a separate path.

A subsequent sequential HTTP comparison changed only the Origin header, using
the same URL with no application-level proxy, cookies or Referer. Production
Origin completed in 4.641 s (9,281,203 B/s); localhost Origin completed in
18.844 s (2,285,824 B/s). Both were HTTP 200, ACAO `*`, and matched the full-file
hash. This is an independent HTTP-client observation, not a browser test or a
proof that the underlying OS/network path was identical to the browser. It does
not reproduce a fixed production-Origin rate cap and does not invalidate the
user's observed browser slowdown.

The required production-Origin browser comparison, default/explicit-source full
saves, and refreshed-page/save-state acceptance remain incomplete. The browser
tool's saved CDP permission denied network diagnostics. Automated Chrome bridge
timeouts also coincided with a paused-document-response tool condition; they
must not be reported as media throughput evidence. No cache clearing, forced
Worker activation, proxy change, timeout increase or transport fallback was
used to turn these failures into passes. Signed URLs and detailed private
observations remain outside the public repository.

### Handoff evidence: baseline review and expired-result recovery, 2026-10-31

This subsection records the incoming recovery-patch handoff environment's
observations and experiments. Local receipt checks and the subsequent matched
browser comparison are recorded separately in `UNIFIED-MEDIA-ACCEPTANCE.md`.

The preceding incident section is a historical checkpoint. The owner subsequently
reported two complete production Chrome SW saves of the 43,074,063-byte compressed
4K file, one with a page refresh, and resolved CDP access. The 523,069,472-byte
original still slowed down in SW, raw Fetch and a command-line client. Those new
file/hash observations are owner-reported here, not independent reruns of the
private Windows acceptance log.

An independent read of the existing production page found one completed card and
one unconfirmed card. Clicking only the latter's existing query button immediately
recovered its completed outcome and `.mp4` filename. No parse or download button
was used. This proves that this task's trusted terminal remained available; it
does not identify the original communication failure or independently verify disk
contents. The old client deliberately skipped queries for an expired saved
recovery window, so a refresh could leave an available terminal unread. The
result-only restoration query documented above repairs that gap without a new
transfer, a renewed recovery deadline or automatic retries.

Comparison of PR #8 (`f1a6778`), PR #9 (`665bd55`) and the reviewed PR #11
(`d8cc86c`) found identical native/Blob/bridge modules, upstream Fetch parameters,
read timeout and byte-pump behavior. Blocking every lifecycle persistence write in
a deterministic three-version Worker test did not block any of the 64 MiB media
response: each version performed one media Fetch and 1,025 upstream reads and
produced the same bytes. This is a dependency test, not a browser speed benchmark.
The backend default/source ordering functions also match. Four identical parser
inputs per version produced the same complete outputs: unmeasured original uses
compressed 4K; measured 4K original becomes the default; a measured 720p original
does not replace compressed 4K. The two actual 4K files differ in size by about
12.14 times; a work URL or resolution label alone is not a matched-source test.

At that historical checkpoint, the proposed throughput comparison was to freeze one still-valid media URL/file
and the same browser/network path for PR #8 and the current client. A historical
fast save of a different representation cannot locate a regression. Conversely,
matching application code does not prove every browser/CDN/network interaction
healthy. Later valid version comparisons, the rejected a8 experiment and the owner's
successful normal production saves are superseding evidence in the closeout summary;
do not repeat that comparison. This recovery patch does not
change timeouts, media routing, quality defaults, proxy settings or the Worker.

## PR31 后稳定性整改（2026-11-05，候选代码未发布）

本节对应维护者提供的《DokiTool-Go 生产端与代码仓库全面审查报告》，不是新的媒体验收或现网发布记录。

| 原报告范围 | 本次处理及边界 |
| --- | --- |
| B-01 / B-02 / B-04 | 独立 [PR #32](https://github.com/Brilliant666/DokiTool-Go/pull/32) 处理可配置准入、目录隔离、慢请求体和客户表回收；本前端变更不重复修改后端。 |
| F-01 分片与 ZIP | 分片元数据 HEAD 最多四路，并共享五秒预探测预算。不可读取的长度保持未知，实际 GET 决定字节完整性。分片按序组成 Blob，不再额外分配整件连续 Uint8Array。ZIP 仍由 JSZip 缓冲生成，不宣称零内存或流式 ZIP；转换成员后及时释放原 Blob 引用。 |
| F-01 请求与任务生命周期 | 分片、HLS 清单和 ZIP 资源的响应头及每次 body.read 最多等待三十秒；正常持续传输没有总下载时长限制。超时/取消关闭对应请求和 reader。ZIP 与分片成品在页面内共用一个可取消、最多一项执行的缓冲队列；普通 Worker 流式下载的两并发与原生交付不改。已有分片最多两次重试保留，取消不重试。 |
| F-02 | 删除当前产品没有使用的前端自签自验 AntiReplay 及其导入时周期定时器；不触碰 tbvideo 文件完整性 MD5、服务器认证或签票。 |
| 下载取消兼容性（报告 5.3.5） | resolve 准备阶段不再依赖 AbortSignal.any；组合信号保留首个取消原因，完成/取消后移除监听。原有三十秒准备期限、选择身份与取消语义不变。 |
| F-05 可访问性的小修 | 跑马灯第二份链接退出键盘与辅助技术导航；移动端不再重新开启 reduced-motion 下的动画。未执行全仓死代码/依赖清理。 |
| #31 候选暂存边界 | 新增受控回归证明：tbvideo 整件校验若超过原有两分钟附件准备期限，取消读取、无附件返回并回落手动，释放 Worker 名额。该期限与算法未修改；不借此声称大文件内存问题已全部解决。 |

本批没有更改原画/实况来源选择、HTTP/HTTPS 修正、媒体中继、原生 iframe 生命周期、ObjectURL 回收时长或 IndexedDB 清理规则。报告中 Pull 重派、未启用 secure 协议、旧 direct 服务、网关/CSP/HSTS 和全量 CI/依赖治理建议不在本批内，不能记为已修。缓存/singleflight、连接复用与探测并行属于另一组容量工作，也未由本批实现。

发布时本批只需更新配套正式 Vue；下载 Worker 产品脚本与其 build 未变。上线与否以维护者批准和实际发布回执为准。核验优先复用受控小文件、ZIP 配对与取消/恢复回归，不重新下载抖音原画或重复 CDN 测速。


### 小红书上游 CDN 轮换：协议准备不再依赖节点名单

#32/#33 的实际发布源码为 `d82ff77`。其生产验收中，新增首来源
`http://sns-v28.rednotecdn.com/...` 未被 #30 的两域名规则升级；单视频
按备用顺序保存了 v11，ZIP 首来源请求被 mixed-content 拦截。旧、新版
均受该规则影响，因此该回执未宣布新稳定基线，也未因这项旧限制回退。
参见 [发布与缺口记录](https://github.com/Brilliant666/DokiTool-Go/pull/33#issuecomment-6649301507)。

本次产品规则在 ID 生成后的 `xiaohongshu.PreparePublic` 实施：对已解析
`live_photo → pair → motion → file` 的直接来源，以及同源凭据备用 URL，
只将规范 HTTP 协议改为 HTTPS。主机、路径、签名参数、编码、来源 ID、
主备顺序、可用性和到期信息原样保留；既有 HTTPS 不改。不再只认某几个
`sns-vNN`，也不将范围替换成另一份 CDN 后缀名单。普通视频/4K、封面、
其他平台、冻结 PHP 提取基线不变。

该步骤只处理适配器已经返回的结构化媒体来源，不从用户自由文本猜测
CDN，不拼主机、不扩展服务器抓取权限、不读取完整媒体。含凭据、显式
端口或格式异常的地址不猜测修正，原来源仍保留。服务器逐跳 DNS/IP、
TLS 和出站限制不变。

HTTP 升级不是可用性证明：实际浏览器仍须通过 TLS、CORS、完整性检查。
单项与 ZIP 读取同一份已准备来源；本次不改变 ZIP 的首来源选取策略，
不新增自动换源，也不把跨来源内容视为同一完整文件。受控回归覆盖新节点
首项、多来源原序及陌生 CDN 域名，**不等同于生产 v28 完整保存通过**。
后续获准发布只更新日本 Go；应复用现有生产响应，一次核对同一所选实况
的视频、封面、ZIP 完整保存/解码、CRC/配对及同源成员一致性。未取得
该证据前保持“修复候选”，不重新运行大文件下载或 CDN 排名。

### YouTube 第一阶段：用户脚本 `GM_download` 下载模式与内存边界

YouTube 第一阶段不走服务器媒体中转或页面 Service Worker 流式下载：
- `360p (含音频, itag=18)` 通过浏览器原生标签页打开包含 `&title=<标题>_360p` 的直连地址交由浏览器直接保存；
- 仅视频轨（如 `1080p`、`4K` MP4）与 `M4A` 音轨（`itag=140`）由同源页面通过 `CustomEvent` 委托 `dokitool-youtube.user.js` 调用 Tampermonkey `GM_download` 从 `*.googlevideo.com` 直接下载。

Tampermonkey 下载模式与内存边界（同一条 `tO01J-M3g0U` `1440p60` `itag=700` 仅视频轨，`267,476,589` 字节 ≈ 255.09 MiB，有界面 Chrome 147 + Tampermonkey 5.5.1 实测对比）：
- **Tampermonkey「默认」（`default`）下载模式**：`GM_download` 会在扩展后台通过 XHR 将完整响应读入内存（`responseType: "blob"`）后再触发保存。实测耗时 `83.84s`，保存期间 Chrome 进程总 RSS 从基线 `1106.39 MiB` 升至峰值 `1808.09 MiB`（峰值增量 `+701.70 MiB`，页面 JS Heap 峰值 `13.17 MiB`）；超大体积（如数 GiB 的 4K 60fps 长视频）在该模式下受扩展单进程 Blob 内存上限约束。
- **Tampermonkey「浏览器 API」（`chrome`）下载模式**：对 `.mp4` 仅视频轨直接调用 `chrome.downloads.download` 流式落盘，无需在扩展内存中缓冲整段视频。实测耗时 `83.92s`，保存期间 Chrome 进程总 RSS 从基线 `1280.42 MiB` 到峰值 `1312.44 MiB`（峰值增量仅 `+32.02 MiB`，下载完成后 `1234.42 MiB`，页面 JS Heap 峰值 `7.67 MiB`）。
- **`.m4a` 扩展名保留与 `mode: "native"`**：Chromium 的 `chrome.downloads.download` 会根据上游 `Content-Type: audio/mp4` 将 `.m4a` 文件名改写为 `.mp4`。因此用户脚本对 `.m4a` 音轨显式传入 `mode: "native"`，由扩展走 Blob 锚点保存以保留 `<标题>_audio.m4a` 后缀；音轨体积通常较小（数 MiB 至数十 MiB），在内存边界之内。
