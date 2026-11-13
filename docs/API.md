# API 合同：统一媒体 v1

公开解析使用同一套 `items → variants → delivery` 合同。正式结构见 [JSON Schema](schema/unified-v1/unified-v1.schema.json)，选择规则、单位、示例及验证方法见 [schema README](schema/unified-v1/README.md)。本合同取代旧的 `url/images/video_backup/videos/live_photo` 公共返回格式；前端不再双读旧字段，也不按平台猜测字段含义。

## 固定入口

所有统一媒体 v1 JSON 接口均要求查询参数 **`schema_version=1.0`（必填）**。缺少、重复或版本不匹配时，服务端直接返回 HTTP `409`（`code: "INVALID_INPUT"`，提示刷新页面），不会回退旧格式。

| 方法与路径 | 参数与用途 |
| --- | --- |
| `GET /api/parse` | `schema_version=1.0`（必填）；`url` 为分享 URL 或包含 URL 的分享文本（必填）；可选 `platform` 为目录中的平台 ID（`all` 或省略时自动按域名识别）；可选 `debug=1` 返回规范化全量版本。 |
| `GET /api/resolve` | `schema_version=1.0`（必填）；使用后端给出的 `resolve_url`，携带原分享 `url`、`platform` 与不透明的 `item_id`（必填，长度 `<= 256`）解析指定分集；同样支持 `debug=1`。客户端不能从 ID 拼平台参数或借用其他分 P 的地址。 |
| `GET /api/platforms` | `schema_version=1.0`（必填）；返回当前已启用平台的公开目录。 |
| `GET /api/youtube/context` | `schema_version=1.0`（必填）；在启用 `DOKITOOL_YOUTUBE_HELPER` 时返回当前缓存的 YouTube `player_id`、`signature_timestamp` 与固定客户端参数，供同源用户脚本在浏览器端请求播放清单。未启用时返回 `404`。 |
| `POST /api/youtube/finalize` | `schema_version=1.0`（必填），可选 `debug=1`；接收浏览器端用户脚本提交的流清单载荷（`<= 512 KiB`），在无权限 Deno `ejs` 沙箱内完成签名解算并返回标准统一媒体 v1 结果。未启用时返回 `404`。 |

```sh
curl --get 'https://snap.example.com/api/parse' \
  --data-urlencode 'schema_version=1.0' \
  --data-urlencode 'url=https://www.douyin.com/video/EXAMPLE'
```

上面是格式示例，不是有效平台样本。分享 token、签名参数必须保持完整；不要把实际 token 或返回的签名 URL 写进公开日志、Issue 和夹具。

目录响应为 `{"schema_version":"1.0","code":"OK","data":{"platforms":[...]}}`。每个平台包含 `id/name/site_url/capabilities`，可选 `icon`；不会返回内部文件路径。唯一注册源是 `backend/config/platforms.json`，同时供后端路由、CLI 导出和前端构建快照使用。运行中的页面也会获取目录，失败时保留构建快照。没有专用展示图标的新平台使用通用图标，不影响解析。

旧 `/api/short_videos`、`/api/douyin`、`/api/ksjx`、`/api/bilibili`、`/api/xhsjx`、`/api/toutiao` 路径已退役并统一返回 `410` JSON 错误，不再返回旧合同。

## 最小成功响应

```json
{
  "schema_version": "1.0",
  "code": "OK",
  "data": {
    "platform": "douyin",
    "items": [{
      "id": "video-1",
      "type": "video",
      "variants": [{
        "id": "file-1",
        "delivery": {
          "kind": "file",
          "sources": [{"url": "https://media.example.test/video.mp4"}]
        }
      }],
      "default": {
        "variant_id": "file-1",
        "url": "https://media.example.test/video.mp4"
      }
    }]
  }
}
```

作品标题、作者、封面、发布时间、标签、统计、分组和附件均为可选，未知值不凭空补齐。多个 item 必须有 `default_item_id`；它选择初始展示条目，不替代每个 item 的默认文件。背景音乐和字幕属于 `attachments`，不能混成视频画质。图像和实况可同时存在，不能互相覆盖。

## 默认选择与完整媒体

默认选择由后端统一计算。`default` 只能指向当前 item 中最高合格的完整 `file` variant；多个来源时必须带 `source_id`。投影 URL、访问方式和已知过期时间必须与被引用的 source 一致。默认优先级、未知规格及同档处理详见 [服务端选择规则](schema/unified-v1/README.md#服务端选择与客户端职责)。

普通文件直接使用 `delivery.sources`，不套一层只有一个成员的 `resources`。只有音视频组合 `mux` 和实况 `pair` 使用 `resources`。无完整文件时 `default:null` 并说明 `processing_required`、`needs_refresh`、`needs_resolve` 或 `unavailable`；不能把视频裸轨当成完整视频。

后端先完整规范化所有版本及明确音轨配对，再生成普通产品视图：只过滤被同一条当前有效完整音画 `file` 在空间尺寸和已知受保护能力上覆盖的 `mux`。四项保护为高于 30 fps 的帧率、已确认 HDR 类型、已证明备选语言及多声道。未知值不新增保护；保护已经触发时，替代文件的未知值不能证明覆盖。具体 1% 帧率容差、缺失和冲突规则见 [产品视图规则](schema/unified-v1/README.md#普通产品视图与诊断视图)。

`debug=1` 保留完整规范化 `variants`，不是平台原始响应，也不跳过公开来源资格和微博签名处理。两种视图的存活 item/variant/source ID 相同；debug 响应的 `refresh_url`、`resolve_url` 延续 `debug=1`。筛选后重新计算 default、不可用原因与更高画质引用。已知空间尺寸更高、可用但需要处理的版本由后端放入 `higher_quality_variants`；HDR、帧率、语言本身不冒充“空间分辨率更高”。当前支持范围：

| delivery | 行为说明 |
| --- | --- |
| `file` | 完整视频、音频或图片；读取服务端默认或用户显式选择，接入下载管理器（YouTube 分离轨道经浏览器端用户脚本并发拉取并使用 Mediabunny 无损合并）。 |
| `hls` | 保留显式清单及作用域（如 Dailymotion 单流 fMP4 HLS 清单），由浏览器按序下载初始化段与媒体分片拼接保存；不作为完整 `file` 默认。 |
| `segments` | 只有具备布局验证证据的 `byte_concat` 才允许拼接；`remux` 明确暂不支持。 |
| `pair` | 静图与动态片段有明确角色，按实况操作处理。 |
| `dash`、`mux` | 结构保留、能力识别与“暂不支持”提示；服务端不执行音视频合流。 |

variant 的 `state:"deferred"` 是待解析能力预留，只有 ID、可选标签/来源说明和 `reason`，没有媒体地址、规格或交付结构；不能进入默认选择或更高画质可交付引用。它也不代表已经拿到 4K。当前预留没有隐含的抓取器或自动重试入口。

前端通用函数校验并读取后端选择，不按尺寸、码率或数组位置重新挑默认。用户显式选中的 variant/source 在条目更新后仍保留；引用失效则明确提示重新选择。新解析结果不能修改已经建立的下载任务快照。

## 规格、统计与来源

- 尺寸是所选文件/轨道的尺寸，时长为 `duration_ms`，码率为 `*_bps`，大小为 source 的 `size_bytes`。`metadata.evidence` 区分 `probe/upstream/mixed/unknown`；混合证据逐字段说明。上传原片尺寸不是当前直链尺寸。
- `hdr_type` 可选值为 `sdr/hdr10/hdr10_plus/hlg/dolby_vision/hdr_unknown`，缺失表示未知；`hdr_unknown` 只代表已确认 HDR、类型未确认。`audio_language_role` 可选 `main/alternate`，必须同时有已知 `audio_language`。这些字段也必须有技术证据；不从 HEVC、音频 ID 或非默认音轨推断 HDR/备选语言。冲突的 variant/resource 证据保留各层事实，不用冲突尺寸作默认或更高画质排序。
- `provenance:"original_endpoint"` 仅说明来源。它不能独自证明更高画质；未验证规格的原始入口不能覆盖已知高规格完整文件。UI 直接展示后端下发的 `variant.label`（如抖音验证通过的原画为 `原画`）与实际元数据，不自行将“原始入口”升级成“原画”。
- 多 CDN 来源属于同一个已确认的文件版本；编码、原始码率、尺寸或明确规格冲突时不能仅按相同分辨率合并。缺少可靠同档证据的候选单独保留。
- 统计项使用 `state:"known"` 与数值，或 `state:"unverified"`；真实零和无法确认的零不同。平台特有的原值解释由后端处理，前端只显示状态，不再特判抖音。
- `access` 省略表示 `direct`；`relay` 必须是 API-relative 地址，前端按配置的 API origin 展开。source 不能携带 Cookie、Authorization 或任意请求头。`browser_fetch` 描述可读取性，不等同于默认文件资格，也不是服务器中转指令。
- 已知有效期写 `expires_at`，同时响应提供 `generated_at`。过期来源不能作为默认；既有选择过期不能静默换成另一版本。

## YouTube 浏览器助手接口

当服务端启用 `DOKITOOL_YOUTUBE_HELPER` 且 Deno 与 `yt-dlp-ejs` 脚本通过启动期 SHA-256 校验时，开放以下同源协作接口（服务端全程只访问 `https://www.youtube.com` 获取播放器脚本，绝不请求 `*.googlevideo.com`）：

1. **`GET /api/youtube/context?schema_version=1.0`**
   - 内存缓存命中未过期播放器上下文时走 `CATALOG` 准入池，缓存未命中触发刷新时走 `PARSE` 准入池（播放器缓存默认有效期 6 小时，刷新失败时允许最多 1 小时陈旧回退）。
   - 成功返回 HTTP `200`：
     ```json
     {
       "schema_version": "1.0",
       "code": "OK",
       "data": {
         "player_id": "1a2b3c4d",
         "signature_timestamp": 20000,
         "client": { "name": "TVHTML5", "version": "5.20260707" },
         "expires_at": "2026-11-13T12:00:00Z"
       }
     }
     ```
2. **`POST /api/youtube/finalize?schema_version=1.0`**（可选 `&debug=1`）
   - 走 `PARSE` 准入池；请求体上限为 **`512 KiB`**，`formats` 与 `adaptiveFormats` 条目总数上限为 **`64`**。
   - 请求 JSON 字段：`video_id`（11 位视频 ID）、`player_id`（8 位十六进制播放器 ID）、可选元数据（`title`、`author`、`duration_seconds`、`thumbnail`）以及客户端从 `www.youtube.com` 取回的 `formats` / `adaptiveFormats` 数组。
   - 服务端在零权限 Deno 沙箱中批量解算 `s` / `n` 挑战参数，严格校验生成的直链必须为 `https://*.googlevideo.com/videoplayback`（无凭据、无自定义端口、无片段锚点），最终返回标准统一媒体 v1 响应（`extra.youtube_helper.status = "ready"`）。

## 错误码、限流退避与分 P 语义

条目状态分为可用（`ready`）、`deferred` 和 `unavailable`。分 P/合集顺序由 `groups[].item_ids` 表达；未解析条目带明确 `resolve_url`，失效条目保留身份与错误。切换、下载时解析对应条目，不能用 P1 直链填补失败的 P2。部分失败可以随其他成功项返回，整体无可用媒体则返回业务错误。

错误响应固定包含 `schema_version`、`code` 和 `error`（`error.code` 与顶层 `code` 一致，不含 `data`），客户端同时检查 HTTP 状态码与业务码：

| 业务错误码 | HTTP 状态码 | 触发场景与契约语义 |
| --- | --- | --- |
| `INVALID_INPUT` | `400` / `405` / `409` / `410` | 缺少或非法的 `url` / `item_id`；链接与显式指定的 `platform` 不匹配；缺少或不匹配的 `schema_version`（`409`）；不支持的 HTTP 方法（`405`）；已退役旧路径（`410`）；以及特定需先展开的短链提示（如 Facebook `fb.watch` 短链需登录展开，接口本地直接返回 `400 INVALID_INPUT` 提示先在浏览器打开后复制 `facebook.com` 完整视频链接，抖音 `mix_id` 合集链接、小红书缺失 `xsec_token` 同属此类）。 |
| `UNSUPPORTED_PLATFORM` | `400` | 输入包含合法的 `http(s)` 公开域名链接，但该域名不属于任何当前已启用的平台（例如未接入站点，或 X 的 `t.co` 短链），或查询参数显式传入了未注册的 `platform` ID。 |
| `UPSTREAM_BLOCKED` | `403` | 上游平台拒绝匿名公开读取或要求登录验证：<br>• **X 受限推文（`TweetTombstone`）**：年龄限制、敏感内容或需登录查看的推文在默认配置（`-x-tombstone-fallback=off`）下返回 `403 UPSTREAM_BLOCKED`；若服务端显式启用 `-x-tombstone-fallback=fxtwitter`，则在遇到 `TweetTombstone` 时向 `api.fxtwitter.com` 查询公开元数据，仍被拒绝或不可读时保持失败关闭。<br>• **Facebook / Instagram 登录与权限限制**：Facebook 私密/年龄受限视频、Instagram Stories（`/stories/`）、私密帖及跳转登录页的 `instagr.am` 短链均返回 `403 UPSTREAM_BLOCKED`。 |
| `NO_MEDIA` | `404` | 目标内容公开可访问，但作品本身不包含任何受支持的媒体资源：例如 **X 纯文本推文**（无视频也无图片）、**Dailymotion 仅提供非 fMP4（如 TS 分片）HLS 流**的视频，或空图文帖。 |
| `CONTENT_UNAVAILABLE` | `404` | 目标作品不存在、已被作者或平台删除，或 `/api/resolve` 请求的 `item_id` 在作品中不存在或不可用（例如不存在的 Dailymotion 视频 ID）。 |
| `RATE_LIMITED` | `429` | 触发单客户端或全局令牌桶/并发上限。响应同时携带 `Retry-After` HTTP 头以及 `error.retryable: true` 和正整数 `error.retry_after_seconds`。<br>**前端自动等待重试策略**：前端在请求收到 `429` 且 `0 < retry_after_seconds <= 15` 时，自动按秒倒计时等待并重试（单次操作最多重试 **2 次**；若 `retry_after_seconds > 15` 或重试耗尽仍受限，则立即向用户展示限流提示）。 |
| `UPSTREAM_TIMEOUT` / `UPSTREAM_UNAVAILABLE` / `UPSTREAM_INVALID_RESPONSE` | `504` / `502` | 上游抓取超时（`504`）、上游暂不可用或提取失败（`502`）、上游响应不符合预期结构（`502`）。 |
| `SERVICE_NOT_CONFIGURED` / `INTERNAL_ERROR` | `503` / `500` | 平台适配器或媒体签名密钥未配置完成（`503`），或内部响应编码异常（`500`）。 |

## 微博签名媒体服务与下载边界

微博仍通过固定 `/api/media/weibo?ticket=...` 签名媒体服务，GET/HEAD/Range 行为不变。公开层先处理简单及组合交付的来源资格和签名，再筛选产品视图并重新计算 default，确保投影一致。签名不能延长已知 CDN 有效期；被公开层拒绝的完整文件不能导致有效 mux 被过滤。不再向前端发送 `playback_url/original_url/media_delivery` 旧别名。未配置共享签名密钥明确报错。它不是通用 URL 代理；入口隔离与池配置见 [部署说明](DEPLOYMENT.md#微博播放与下载)。

紫色下载按钮按原生附件、同源 Service Worker 流式下载、YouTube 助手并发拉取 + 浏览器内封装、以及分片拼接/ZIP 打包处理媒体交付。选源、命名和任务快照在交付前固定；具体下载策略、多线路测速、打包上限与调试开关见 [下载说明](DOWNLOADS.md)。

`/api/v1/session`、`/api/v1/parse`、原始 PHP 路径、开放媒体代理、Cookie 主页和网易云服务不属于此公开合同。小红书 4K 的传输对照及历史研究见 [研究记录](research/xhs-4k-transport-comparison-2026-10-30.md)。
