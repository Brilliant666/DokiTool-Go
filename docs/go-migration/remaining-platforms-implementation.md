# 剩余五平台 Go 实施说明

九平台实现与完整离线门禁已在 **`1fdc7b73ac6e9eb54b212539fc21893e695608f5`** 通过：[CI #41262330047](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41262330047) 两个 job 均 completed/success。五平台实现最初提交为 `1545095`；下文描述其实际行为与独立证据，真实平台可用性和生产切换仍待验收。完整结果见 [acceptance.md](acceptance.md) 和 [nine-platform-checks.json](nine-platform-checks.json)。

## 共同合同与固定来源

PHP 对照源固定为 `25beaee107f8c1a208ced9deb3c637068445e316`，文件树 `e4b9d40f5e6c12a86fff629c105553886b434dcd`，运行时为 **PHP 8.3.6 / 64 位**，公开响应时钟固定为 `2026-10-30T12:00:00Z`（`1793361600`）。生产器执行归档中的原 PHP；仅替换受控网络 I/O、时钟或原解析器已支持的页面回调，不用 Go 输出制作 expected。每组基线记录来源、输入、生产器和期望的哈希。

Go 平台入口统一为 `New(fetch.Executor) *Parser` 与 `Parse(ctx, input, now)`。结果经过原入口 JSON 类型语义、统一适配、服务端默认选择与普通/debug 投影；生产 Go 无 PHP 运行依赖。保留 `items → variants → delivery`、稳定身份、显式选择及续签、已知零值和错误语义。候选覆盖与四项能力保护仍由公共层实现：高帧率、已确认 HDR 类型、已证明的备选语言、多声道。参见 [映射表](mapping.md) 与 [迁移范围](remaining-platforms.md)。

## 实际入口与媒体能力

公开入口先经过 [platform catalog](../../backend/config/platforms.json)，再进入平台解析器。下表域名遵守既有 catalog 子域匹配规则；它们不自动授权任意 CDN 或重定向目标。

| 平台 | 公开域名与既有能力 | 实际解析入口 |
| --- | --- | --- |
| 快手 | `kuaishou.com`；视频、图片 | [Go parser](../../backend/internal/platform/kuaishou/parser.go)：HEAD 取有效地址，再 GET 页面，解析 INIT_STATE/APOLLO；对应 [KuaishouSpider.php](../../backend/api/kuaishou/KuaishouSpider.php) 与 [ksjx.php](../../backend/api/kuaishou/ksjx.php) |
| 皮皮虾 | `pipix.com`；视频、图片 | [Go parser](../../backend/internal/platform/pipixia/parser.go)：作品 ID 或受控短链跳转，固定 `h5.pipix.com/ppx/item/<id>` 页面；对应 [ppxia.php](../../backend/api/ppxia.php) |
| 皮皮搞笑 | `pipigx.com`、`ippzone.com`；视频 | [Go parser](../../backend/internal/platform/pipigx/parser.go)：解析 `pid/mid`，POST `https://h5.pipigx.com/ppapi/share/fetch_content`；对应 [pipigx.php](../../backend/api/pipigx.php) |
| 最右 | `izuiyou.com`；视频、图片 | [Go parser](../../backend/internal/platform/zuiyou/parser.go)：`pid` 或一次短链 HEAD，POST `https://share.xiaochuankeji.cn/planck/share/post/detail_h5`；对应 [zuiyou.php](../../backend/api/zuiyou.php) |
| 小红书 | `xiaohongshu.com`、`xhslink.com`、`rednote.com`；视频、图片、实况 | [Go parser](../../backend/internal/platform/xiaohongshu/parser.go)：短链与笔记 ID，优先 rednote、再回退 xiaohongshu 页面；对应 [XiaohongshuParser.php](../../backend/api/xiaohongshu/XiaohongshuParser.php) |

**解析器兼容 alias 与网关授权分开。** 小红书原解析器接受 `xhs.com/www.xhs.com` 并转为 `xhslink.com`；冻结 catalog 没有 `xhs.com`，正式 Go 网关也应在发出请求前拒绝该入口。直接 parser 基线仍保留原行为，正式 [HTTP 测试](../../backend/internal/api/xiaohongshu_test.go) 单独验证前置拒绝，不改 PHP expected 来消除这层区别。

### 媒体与身份必须保留的行为

- **快手：** INIT_STATE 优先，按原 JSON 插入顺序选择 `tusjoh*`；仅在原条件下做有限字符串修复或 APOLLO 回退。保留图集、单图、`mainMvUrls`、manifest 各表示及备用地址；APOLLO 可读取 `manifestH265.json`。规格取真实字段，单位未知的 `avgBitrate/maxBitrate` 不推定为 bps。沿用公共 item/variant/resource/source 身份与默认规则，不新增 Live 能力。见 [formatter](../../backend/internal/platform/kuaishou/formatter.go)。
- **皮皮虾：** 只读取目标 `ppxItemDetail.item`，允许原有 raw/百分号编码 JSON，验证返回作品 ID 与请求一致，不捡取评论媒体。保留多档视频、原下载来源及 `note.multi_image`；视频表示使用原 URL 集合身份，图片按原顺序去重，默认仍由后端选择。见 [formatter](../../backend/internal/platform/pipixia/formatter.go)。
- **皮皮搞笑：** 保留 `private_videos` 多视频和 `private_video_id`；无上游视频键时沿用原列表位置，不能因中间无效 URL 重新编号。`pid/mid` 保留 PHP 查询与整数边界、POST 字节和失败语义。catalog 仍仅视频，不把上游图片擅自开放为新能力。见 [parser](../../backend/internal/platform/pipigx/parser.go)。
- **最右：** `imgs` 决定视频展示顺序，原 `videos` 映射键决定 item 身份；前项消失不改变后项身份，目标消失时 resolve 明确拒绝。图集原图/备用顺序保留；既有 HLS/DASH 候选按合同保留处理需求，不增加执行器。见 [formatter](../../backend/internal/platform/zuiyou/formatter.go) 与 [正式网关测试](../../backend/internal/api/zuiyou_http_test.go)。
- **小红书：** 仅提取目标笔记 SSR 数据，不执行页面 JavaScript；有限替换发生在引号外。保留所有 stream 编码/备用来源、`mediaV2` 投屏源、`originVideoKey` 的 qn/al/qc/bd 候选，以及图集与对应实况视频。视频笔记的图片只作封面，统计零值保留。原画容器/尺寸由有界 MP4/QuickTime 字节探测确认；未确认来源不能借用其他流的尺寸。可选探测失败保留已验证流及 PHP 原有的原画入口候选；未确认尺寸保持未知，不将所有原画统一改为 deferred，也不宣称已获取 4K。见 [formatter](../../backend/internal/platform/xiaohongshu/formatter.go) 与 [probe](../../backend/internal/platform/xiaohongshu/probe.go)。

## JSON 序列化边界

PHP parser 返回值与 JSON 序列化后再读回的值可能不同；Go 保留这一步后再进入公共适配。比较仍检查缺失/null/false/0/空数组、数组顺序、精确整数、错误与 URL 字节，不能先把所有数字转成浮点数。

| 平台 | 原入口 JSON flags | 对拍要点 |
| --- | --- | --- |
| 快手、皮皮搞笑、最右 | `480` | 含 `JSON_NUMERIC_CHECK`；数字字符串经过原数值转换后才适配，不把原字符串直接送入公共层 |
| 皮皮虾 | `320` | 不含 `JSON_NUMERIC_CHECK`；保留数字字符串、实际数字及大整数处理的区别 |
| 小红书 | `2097472` | 不含 `JSON_NUMERIC_CHECK`，含无效 UTF-8 替换；原 JSON encode/decode 对整数值浮点数、负零、空容器的影响仍须保留 |

小红书另有 **10 个独立 wire 场景 / 20 份普通/debug 响应**，没有覆盖或重写原 37 场景。真实 PHP 观察包括：`1e3/12.0` 输出后成为可读文本 `"1000"/"12"`；`-0.0` 经 wire `-0` 再读为零，标题与作者仍按各自原空值规则处理；小数/指数浮点不当作文本；数字字符串不转换；`9007199254740993` 不丢精度，超过 int64 的浮点值不冒充精确文本。另覆盖空数组/对象封面、空图片不生成实况、实况关联和数字键图片的插入顺序：封面取键 `0` 与图集遍历顺序是两个独立规则。

这组期望由 [独立 PHP wire 生产器](../../scripts/go-migration/produce-xiaohongshu-wire.php) 执行原 parser 的页面回调与真实 output 方法生成，只使用合成页面，没有媒体探测。冻结文件为 [php-wire-baseline.json](../../backend/internal/platform/xiaohongshu/testdata/php-wire-baseline.json)，SHA-256 为 `9b0204739889de490d933c4d3f4583571af0a25eb035069e98f08d220710afd1`；[来源验证器](../../scripts/go-migration/verify-xiaohongshu-wire.py) 与 [Go 对拍测试](../../backend/internal/platform/xiaohongshu/wire_test.go) 分别消费它。PHP 期望已独立生成；本轮 CI 的 20 份 Go 公开响应均逐字段相等，并进入独立校验和原前端消费。

## 请求与共享传输边界

| 平台 | 请求预算与跳转 | 凭据、URL 和返回内容 |
| --- | --- | --- |
| 快手 | HEAD/GET 各 20 秒、各最多 5 跳；解析总 40 秒，正文 8 MiB | 固定快手域名边界；保留 GET 空 Cookie，HEAD 不带 Cookie；GET 使用自动 Referer |
| 皮皮虾 | 短链最多 5 次显式 GET 后取详情；共享 20 秒，剩余时间按 PHP ceil，正文 2 MiB | 升级 HTTPS，固定页面主机与 Referer；不用 Cookie；只详情请求读取不跟随的 3xx 正文 |
| 皮皮搞笑 | 单次 POST 20 秒、8 MiB；不自动跟随 | 固定官方 endpoint、精确 JSON 与 Content-Length；不带凭据；允许读取原 PHP 可见的 3xx 正文 |
| 最右 | 可选 HEAD 12 秒；POST 20 秒、最多 5 跳；解析总 35 秒，正文 8 MiB | HEAD 的 Location 仅提取 pid，不请求该地址；随后只请求获准固定 API 路径 |
| 小红书 | 每段页面链共享 10 秒，初始请求加最多 5 跳、8 MiB；原画探测共享 10 秒，每次最多 6 秒 | 只替换页面 authority，保留签名查询原始字节与 `+`；空 Cookie；探测首读 64 KiB，每次总计最多 1 MiB/12 请求，并检查严格媒体头及容器 |

所有实际出站继续经 [fetch.Executor](../../backend/internal/fetch/fetch.go) 的每跳目标授权、DNS/公网 IP、连接绑定、TLS、取消、头部与读取预算。小红书页面与媒体探测分别授权，不因页面给出 CDN 就取消传输检查；有实际证明的最后来源才可保留为可信增强结果。

本轮公共执行器仅增加两个默认关闭的选项：

- **`AutoReferer`：** 仅快手 GET 开启。跟随重定向时取上一有效 URL，去掉 userinfo/fragment，保留签名查询；下一目标仍先通过授权。跨主机及开启此选项的 HTTPS→HTTP 跳转移除 Cookie/Authorization。HEAD 与其他未开启的平台维持原行为。受控回归见 [auto_referer_test.go](../../backend/internal/fetch/auto_referer_test.go)。
- **`ReadRedirectBody`：** 仅皮皮虾详情与皮皮搞笑 POST 开启，保留原 PHP 在不跟随 3xx 时仍能读取的正文。它不跟随 Location、不授权新目标，HEAD 仍不读正文；传输与解压大小限制、取消和严格媒体头校验继续生效。受控回归见 [redirect_body_test.go](../../backend/internal/fetch/redirect_body_test.go)。

请求对拍记录方法、URL、body、显式 headers、跟随规则及预算；只保留各 manifest 已列出的传输表示差异，如 libcurl 隐式 Accept、受限解码器协商和真实媒体探测时钟上界。公开响应字段没有为这些差异另加豁免。

## 独立基线与验证接入

下表为各独立基线库存；本轮已全部严格对拍通过。这些场景数与 Go 叶子测试数不可相加。每个平台目录内保留 `inputs.json`、`manifest.json` 与 `php-baseline.json`；`scripts/go-migration/produce-<平台>.py/.php` 负责原 PHP 生成，`verify-<平台>.py` 校验独立来源。

| 平台与冻结目录 | 场景 | 普通/debug 响应 | 主要回归 |
| --- | ---: | ---: | --- |
| [快手 testdata](../../backend/internal/platform/kuaishou/testdata/README.md) | 40 | 80 | HEAD 有效地址、INIT/APOLLO 优先级与有限修复、manifest/备用、图集顺序、实际规格、空 Cookie、wire 与失败 |
| [皮皮虾 testdata](../../backend/internal/platform/pipixia/testdata/README.md) | 49 | 98 | 短链/共享预算、ID/查询边界、目标匹配、编码脚本、视频/图集、评论排除、错误 |
| [皮皮搞笑 testdata](../../backend/internal/platform/pipigx/testdata/README.md) | 42 | 84 | 精确 pid/mid/POST、多个视频、无效项索引、wire、状态与无媒体 |
| [最右 testdata](../../backend/internal/platform/zuiyou/testdata/README.md) | 48 | 96 | 原映射键身份、imgs 顺序、缺失前项、目标消失、真实 parse/resolve、图片与处理需求 |
| [小红书主 testdata](../../backend/internal/platform/xiaohongshu/testdata/README.md) | 37 | 74 | rednote 回退、签名 URL、SSR、stream/origin/mediaV2、实际合成 MOV/MP4 探测、实况与零值 |
| [小红书独立 wire](../../backend/internal/platform/xiaohongshu/testdata/php-wire-baseline.json) | 10 | 20 | 数字类型、负零、精确整数、空值、实况关联、数字键顺序 |

五平台主组共 **216 场景 / 432 响应**；wire 组单列为 **10 / 20**。最右的 normal/debug 是原 PHP `snapHandle` 真正执行 parse 或 resolve 的最终响应，带 HTTP 语义；不能拿 parse 结果手改 default 来代替 resolve oracle。原画字节测试只使用合成容器，不能记为一次真实媒体下载。

[check.py](../../scripts/go-migration/check.py) 先核验独立基线，再逐字段比较 Go 导出，将实际产物送入独立 Schema/语义验证和 [原前端消费者](../../frontend/scripts/test-go-media-integration.mjs)。精确比较保留大整数，产物聚合直接读取 JSON，避免 Decimal 再序列化改变数据。既有 10 项公共投影差异仍按原批准范围处理，抖音 4 项与微博 4 项默认/显式续签检查保留；不通过放宽 schema、前端或 expected 来取得通过。

## 本轮验收状态与后续边界

本轮完整门禁已通过：Go **3835** 叶子场景（4309 个含父级通过事件），race/vet/gofmt/build 成功；独立 Schema＋语义 **994/994**；原前端实际消费 **918/918**（584 成功＋334 预期错误），抖音和微博各 **4/4** 续签通过。新增五平台的正式 HTTP 叶场景分别为快手 **202**、皮皮虾 **202**、皮皮搞笑 **170**、最右 **96**、小红书 **170**，已包含在 Go 总数中。新增公开响应声明差异为 0，原 10 项差异范围未扩大。执行提交与 CI 链接见本文顶部及 [验收记录](acceptance.md)。

初轮格式门禁未通过，随后使用固定 Go 工具链的实际输出整理源码。之后所有独立 PHP 对拍及正式 HTTP 场景已通过，但新增的两条小红书探测测试使用了含不合法扩展名的合成 origin key，未触发预期分支；仅将测试 key 修正为 PHP 允许的形式，完整重跑后通过。没有改产品 key 规则或任何冻结期望。

离线一致性证明受控输入下的行为，不能证明线上平台仍可访问、实际最高画质可取或文件完整保存。本轮未作九平台在线验收、小红书 4K 端到端专项、浏览器下载/刷新恢复、手机锁屏或生产切换。DASH/音视频合流执行器、新 TLS 指纹库继续不在范围；前端产品、Worker、bridge 与生产环境保持既有发布。最终切换仍须完成 [回滚门槛](rollback.md) 与另行获准的发布流程。
