# 部署与本地验证

此文档与 `ops/examples/` 都是通用示例，不包含生产配置、账号或凭据。仓库提交不会触发线上更新。

## PR #11 发布收尾（2026-10-31，尚未执行）

当前范围和证据以[统一验收收尾](UNIFIED-MEDIA-ACCEPTANCE.md#pr11-closeout)及[严重事故复盘](incidents/INC-20261031-001-download-delivery.md)为准。生产已有bridge局部修复和诊断前端，PR内探针兼容/自动终态恢复尚未发布。正常生产下载已有用户三次约540 MB完整保存反馈，不再为发布重复测速；不把这些成功算作未发布修复生效。

获准合并后，以最终main同一提交构建前端、PHP、schema及目录；记录源码SHA、产物/Worker哈希和本站配置差异，保留发布前整套产物、指向与恢复命令。只做版本/精确bridge响应头/正常解析冒烟，以及新增自动终态恢复的浏览器验收；恢复优先复用已有完成任务，不重下媒体。不要求重跑历史PR #8测速、43 MB保存或无变化的全平台矩阵。

回滚目标是发布前**已经修好bridge**的生产状态，不能随旧配置恢复DENY。按实际变更回退应用指向及本站局部配置，保留旧哈希资源、任务数据和Worker自然生命周期；不清站点数据、不注销/强制升级Worker，不覆盖用户后来修改的网络配置。文件回退不代表已激活Worker立即切换。实际部署、验收和回滚回执另记；本次文档更新不执行这些操作，不包含Go切换。

## 统一媒体 v1 切换

这是九平台、网关和前端的合同切换，不能只替换前端。发布包必须包含同一提交的 `frontend/dist/`、`backend/` 以及 `docs/schema/unified-v1/unified-v1.schema.json`；PHP 从源码相对位置读取 schema，缺失文件不能用跳过验证兜底。后端注册表 `backend/config/platforms.json` 与前端生成的目录快照须来自同一版本。无数据库迁移，无 Go 重写，不增加 Python/curl_cffi 产品运行依赖。

本任务交付到 Draft PR，不授权或表示已合并、上线。后续发布时保留上一套完整应用产物，在受控窗口成套切换。Caddy 精确入口改为 `/api/parse`、`/api/resolve`、`/api/platforms`；旧解析路径保留可读的版本错误，不能继续返回旧 JSON，也不能落入 SPA 首页。内部 listener 使用固定 `/parse?platform=...&url=...`。微博签名媒体路径、共享密钥和独立媒体池继续沿用。

新客户端请求 `schema_version=1.0`；旧页面调用旧 API 时提示更新页面。不得为此清空任务存储、注销 Worker、强制 `skipWaiting` 或 `clients.claim`。PR #7 的下载模块、Worker 和 bridge 沿用原字节与生命周期；已有任务仍使用冻结的计划和原 Worker，新的解析合同只影响新建任务。刷新后的解析与已有下载恢复分别验收。

候选验证至少包含：独立 schema/语义验证、九平台实际适配器离线输出、两进程网关 HTTP 回归、前端构建与交互、选择/分 P/混合媒体，以及完整文件交给原下载链路后的实际保存。真实平台样本与受控夹具分别记录；小红书 4K 尚未完成的 PHP 页面端到端和浏览器完整保存不能写为通过。未来回退同样回退前后端和 schema 整套，保留旧 hash 静态资源与活动 Worker。

## PR #7：下载 Worker 发布与升级（既有机制）

PR #7 是前端更新，不修改后端解析 API、媒体池或数据库。下述内容保留其 Worker 发布机制，不表示本次统一合同也是纯前端更新。沿用现有访问控制、密钥、出网隔离和 PHP-FPM 配置；首次建站及微博服务配置要求不是每次发布都重新安装的步骤。候选代码、最终合并提交、构建清单和实际部署版本需分别记录。

必须整体发布同一次 `npm run build` 生成的 `frontend/dist/`，其中包括 `downloads/worker.js`（BUILD 为 `downloads-20261030.4`）和 `downloads/bridge.html`。本次 bridge 原文件为 198 字节，包含 `id="dokitool-download-bridge"`，响应不能是 SPA 首页。不能只更新主页面或 JS chunk，也不能漏掉 bridge。保留前一版产物和既有 hash 静态文件，按实际发布清单核对字节数及 SHA-256。

核对现有站点的 `/downloads/*` 静态文件规则是否与 `ops/examples/snap.caddy` 一致，并位于 SPA 回退规则之前。已正确配置的站点无需重复修改；缺失时才局部补齐：

- Worker 和 task-store 返回有效 JavaScript MIME，继续使用 `Cache-Control: no-cache` 校验更新；bridge 返回 `text/html`，单独使用 `Cache-Control: no-store`。这不强制立即替换正在工作的 Worker。
- **只有精确 `/downloads/bridge.html` 允许同源嵌入**：最终 GET 响应必须只有一个 `X-Frame-Options: SAMEORIGIN`；首页、Worker、其他下载路径仍保持 `DENY`。全站 `DENY` 会阻止这个同源 iframe，使页面在发送下载任务前报“无法确认下载连接的控制关系”。按示例使用互斥路径条件替换响应值，不用 `+X-Frame-Options` 追加第二个值，也不放开整个 `/downloads/*`。实际站点、公共 import 和上层代理可能另设响应头，应追查最终生效来源，不能用示例文件代替现场配置。
- **已有缓存也必须得到完整 bridge 响应**：Chromium 合并 304 时保留旧缓存的 `X-Frame-Options`，因此 304 本身显示 `SAMEORIGIN` 仍可能继续使用旧 `DENY`。仅对精确 bridge 删除请求的 `If-None-Match`、`If-Modified-Since`，让有效文档返回完整 200 和新安全头；响应用 `no-store`，避免再依赖这个文档的缓存头合并。缓存规则同样用互斥条件，不能被下载目录的 `no-cache` 覆盖。带旧 ETag、日期及两者组合的请求均须取得正确 200/HTML/标记、唯一 SAMEORIGIN 和 no-store。其他脚本的验证缓存机制不变。依据：[Chromium 的 304 非更新头列表](https://github.com/chromium/chromium/blob/main/net/http/http_response_headers.cc)、[Caddy 请求头删除规则](https://caddyserver.com/docs/caddyfile/directives/request_header)。
- 若设置了 CSP，还须核对首页的 `frame-src`（及其回退指令）允许本站 bridge，以及 bridge 的 `frame-ancestors` 允许同源父页面；保留其他 CSP 指令和外站嵌入限制。备份本站实际配置、记录 diff 和恢复命令，使用服务器当前 Caddy 校验完整配置后，仅做必要 reload；再从公网 GET 检查头部没有被代理覆盖。
- 缺失的 `/downloads/*` 文件返回 404，不能回退首页。`/downloads/file/*` 是浏览器内 Worker 生成的附件地址，服务器没有对应文件，直接请求服务器应返回 404。
- 正式环境使用 HTTPS。Worker 仅注册 `/downloads/` 作用域，不控制首页或 API，不将作用域扩大为 `/`，也不增加全站离线缓存。普通源媒体仍由 CDN 直接传到用户浏览器；既有微博签名媒体路径不变。

bridge 是受 Worker 控制的同源 HTML 文档，用于在下载期间保留任务所属 Worker；它不转发媒体字节。更新遵循浏览器正常的 waiting → active 流程，不调用 `skipWaiting`，不通过注销 Worker、清空站点数据或强制刷新来催促升级。多个任务共享 bridge；活动或状态未确定的任务保留它，最后一个任务确认结束四秒后才释放。已有任务继续查询和取消原 Worker；刷新恢复只查询已有任务，不重新开始下载。

服务端发布不会给已经打开的旧前端自动补上 bridge 保护。使用旧页面的用户应等待现有下载结束，再刷新页面使用新机制；不能承诺旧页面正在运行的任务立即获得本轮保护。

部署时先核对当前版本、保存本站发布指向及需要变更的 Caddy 局部配置，再校验新构建和配置、切换版本并检查响应。本轮不需要重启 PHP-FPM；只有实际修改 Caddy 配置时才进行必要 reload。失败时恢复本站上一个发布指向及本轮改动的局部配置，不修改其他应用。文件回退不会立即撤销浏览器中已激活的 Worker；同样保留其现有任务，按正常生命周期验收回退后的新任务，不强制终止下载。

上线验收分两层：

1. **部署响应**：确认首页、实际 hash chunk、Worker 和 bridge 的内容/哈希、MIME、缓存头正确；缺失 chunk、缺失下载文件和服务器端附件路径为 404；既有 API/微博入口及访问控制保持正常。服务端 Worker 已更新，不代表每个已打开浏览器立即切到 `.4`。
2. **真实浏览器**：通过紫色下载按钮检查实际保存文件名、大小和 SHA-256；下载中刷新后任务 ID 不变且不重复传输；有活动任务时新版保持 waiting，任务完成并释放 bridge 后自然升级，下一次下载使用新版。页面显示“传输完成”不能替代文件落盘验证。

可用 `python scripts/check-download-headers.py --base-url https://snap.example.com --output /path/to/headers.json` 检查公开 GET 响应。脚本拒绝重定向和重复/冲突的 X-Frame-Options，核对真实 bridge 标记、缓存策略及相邻路径保护，再发送 ETag、日期和两者组合的条件请求，要求仍返回相同完整正文。默认使用当前 GET 的验证值；复现旧缓存时用 `--previous-etag`、`--previous-last-modified` 传入原失败请求的值。脚本记录有关的 CSP 指令供人工复核；不解析作品、不读取任务凭据、不代替真实下载验收。纯响应头修复应保留原浏览器 profile、现有 Worker 和失败记录，从原首页重试，覆盖默认与显式选源的实际保存及一次中途刷新。不得通过注销 Worker、清站点数据、关闭控制关系校验或切换下载方式来替代修复。

本地 Edge 已用正式构建验证两条超过六分钟的下载、刷新、超过五分钟的排队、自然升级及五个文件的哈希；详情及历史检查点见 [下载说明](DOWNLOADS.md#state-and-admission-consolidation-2026-10-30)。这些是受控源的本地验收，不等于生产 CDN 吞吐测试。Safari/微信、锁屏、浏览器挂起和关闭所有页面仍需另行验证，不能保证 bridge 在这些条件下持续运行。

## 首页预渲染与 SEO 文件

生产必须使用 `cd frontend && npm ci && npm run build`，不要仅运行 `vite build`。构建会先生成客户端资源，再使用同一套 Vue 组件输出真实首页 HTML，并执行预渲染契约检查。仅发布 `frontend/dist/`；临时 `.ssg/` 不进入发布包，也不需要常驻 Node 渲染服务。

首页固定输出中文，客户端通过 hydration 接管后恢复语言偏好；英文偏好用户会在接管后切换文字。主题在样式加载前读取，输入框允许提前输入，解析及平台按钮在功能就绪前禁用并显示准备状态。粒子保持原视觉，在客户端挂载后启动。

`dist/robots.txt`、`dist/sitemap.xml` 和 `dist/social/dokitool-share-v1.png` 必须随包发布。Caddy 示例的精确文件路由让缺失文件返回 404，不能回退首页；部署维护方应将这些规则局部合入现有站点，校验配置后确认内容类型与实际内容。sitemap 当前仅包含真实首页，canonical、分享元数据和结构化数据使用 `https://snap.dokitool.com/`；部署到其他正式域名时需同步修改。

验收需确认 HTML 响应本身含主要内容、浏览器接管无 hydration 错误、提前输入未丢失、主题/语言偏好可恢复、解析正常。构建测试和本地延后加载脚本的功能验证不等同于真实弱网或 CPU 降速测试。

## 静态资源缓存与首次加载

`ops/examples/snap.caddy` 的前端路由为现存、文件名带 Vite hash 的 `/assets/` 文件设置一年 immutable 缓存；HTML 和其他文件使用 `no-cache` 校验更新。缺失的 `/assets/` 文件必须返回 404，不能返回并缓存 SPA HTML。API、微博签名媒体、认证与访问控制不纳入此缓存规则。已有 gzip 保留；不要对媒体响应添加压缩。

这是通用配置要求，实际生产状态以部署记录和响应核对为准。发布时由部署维护方先核对现有缓存规则，仅在缺失或不一致时局部补齐并执行 Caddy 配置适配/校验，再分别检查首页、实际 hash 文件、缺失 chunk、API 和媒体响应；保留旧版静态文件，避免已打开页面的延迟加载引用失效。不要用仓库示例覆盖整份生产配置。

开发时 `npm run build && npm run check:load-budget` 检查真实产物依赖图：结果区与 ZIP 不进入首屏静态依赖，界面不依赖 webfont，首屏 JS gzip 预算 100 KB、CSS 16 KB、保守总资源预算 240 KB。总预算包含本地图标及 touch icon，即使部分图片由浏览器延迟请求。此检查是体积回归门槛，不是浏览器性能测量。

集中验收必须分别记录冷缓存和热缓存、普通网络和弱网、普通 CPU 和降速 CPU 的条件，以及桌面/手机、首屏视觉完整、可输入解析、布局跳动和首次解析结果显示。图标不应出现方框，字标不应露出半截；普通模式保留 50 颗粒子的颜色、大小与原 60 Hz 运动速度，后台暂停且回到页面不瞬移。低动态效果设置仍被尊重。

如果验收环境无法控制浏览器缓存、网络或 CPU，应分别记录为未验证；正常浏览器回归与构建体积检查不能替代这些条件。iPhone Safari/微信也需要真机复核，不能以桌面浏览器的手机宽度模拟代替。

## 服务结构

使用 Caddy 提供 HTTPS 与 `frontend/dist/` 静态文件。三个统一 JSON API 路径及旧入口的版本错误交给 `backend/public/index.php`；另有固定的 `/api/media/weibo` 媒体入口，必须交给独立媒体 FPM 池。其他 `/api/*` 返回 JSON 404。不要公开整个源码目录，也不要把原始 `backend/api/` 作为静态根或任意 PHP 执行根。

聚合解析使用 HTTP 自请求到 loopback 内部 listener，再进入 `backend/internal/index.php`。采用独立服务账户和专用 PHP-FPM 实例，三个池分别服务公开解析、内部叶子解析及媒体传输。同一个池即使有多个 worker，也可能在聚合请求占满后互相等待。单进程 `php -S` 仅适合下文的两个独立测试服务，不适合生产。

PHP 使用受支持的 8.x 版本，代码最低要求 8.1，启用 cURL、OpenSSL、JSON、PCRE/filter/标准流支持，并安装有效 CA 证书。无需 Composer 或数据库。三个池的 worker 数量、内存、超时和日志限制需按机器容量验证，示例数值不代表性能验收。

## 配置示例

先复制示例到自己的配置位置，再替换域名、路径、系统账户、socket 和权限。`snap.example.com` 是保留的示例域名，不代表任何生产站点。

- `ops/examples/snap.caddy`：公开静态站点、精确 API 和仅 loopback 的内部监听。
- `ops/examples/snap-fpm.conf`：公开解析、内部解析和媒体传输三个池。
- `ops/examples/snap-access.caddy.example`：默认返回 503 的关闭入口，选择访问策略后才替换。

访问控制默认覆盖 HTML、静态资源与 API。Caddy 外层 `route` 保证访问门先执行；不要将访问门与 `handle` 并列后依赖书写顺序，因为 Caddy 会重新排序。发布路径使用版本软链时，示例的 `resolve_root_symlink` 可让 FastCGI 使用实际根目录。修改后对实际安装版本执行配置检查，保留局部配置及上一版本以便回退。

密码保护的测试站还必须验证模块 Worker 的实际注册，不能用带认证的静态 GET 通过代替。香港测试站曾因全站 Basic Auth 使 `/downloads/worker.js` 返回 401，注册在媒体传输前失败。仅对 `/downloads/worker.js` 和其实际导入的静态模块（`task-store.js`、`decoded-length.js`、`media-format.js`、`tbvideo.js`，均在 `/downloads/` 下）设置精确静态访问例外；这些文件须匿名返回 200、正确 JavaScript MIME、与构建一致的内容及 `no-cache`，不返回登录页或 SPA HTML。保留首页、API、bridge 和相邻路径的既有认证，不能放开整个 `/downloads/*`、源码或媒体接口。该例外只公开静态脚本，不公开用户浏览器中的任务数据，也不增加服务器媒体中转。

部署验收须分别记录匿名脚本响应、认证边界、浏览器注册/模块加载/激活、协议握手，以及认证后的 bridge 控制关系；后者继续遵守上文 SAMEORIGIN、no-store 和完整 200 要求。注册作用域仍为 `/downloads/`，无需扩大到 `/`。如 bridge 嵌入仍失败，按实际响应与浏览器错误另行定位，不默认扩大匿名例外。配置、下载服务启动和整文件保存是三层验收，任一层通过均不能代替后一层。

| 变量 | 用途 |
| --- | --- |
| `VITE_API_MODE=php` | 前端构建模式；基础 PHP 不实现旧加密会话协议 |
| `VITE_API_BASE_URL=` | 空表示同源 API；若跨域，另行配置明确的跨域策略 |
| `DEV_API_TARGET` | 仅本地 Vite 开发代理的 PHP 网关地址 |
| `SNAP_INTERNAL_ORIGIN=http://127.0.0.1:9081` | 显式 loopback HTTP origin，部署时核对端口 |
| `SNAP_INTERNAL_HOST=snap.example.com` | 必须与内部 Caddy host matcher 一致；显式配置，不依赖源码默认值 |
| `SNAP_MEDIA_SIGNING_KEY` | 公开解析池与媒体池共用的服务端随机密钥，至少 32 字节；内部解析池和前端不接触该值 |

PHP-FPM 保持 `clear_env=yes`，只传必要变量。不要继承生产密码、供应商凭据或其他应用的环境。源文件只读，临时目录独立授权；FastCGI socket 不开放公网。

## 出网与媒体边界

TLS 校验、入口域名检查、连接超时和跳转数量上限都已实施，但上游仍有跟随/手动重定向及 DNS 行为，不能据此认为已具备完整 SSRF 防护。解析服务需要独立出网限制，同时覆盖 IPv4/IPv6、loopback、私网、链路本地、云元数据及保留地址；只例外允许必要 DNS 和受控内部 HTTP 通道。使用网络命名空间或容器时也需验证等价边界。文件访问限制不能代替网络隔离。

除微博外，普通播放/下载直连返回的媒体 URL，仍受 CORS、防盗链、签名时效、Range 和浏览器策略影响。微博采用下述限定来源的签名传输服务，不启用原始 `svproxyurl.php` 开放代理。Cookie 主页、Python/Flask 网易云、其他可选模块不在本仓库维护范围。真实平台的匿名可用性和高级清晰度需单独验证；固定样例通过不是平台成功率证明。

抖音解析会额外探测程序生成的原始播放入口，以识别实际分辨率和视频码率，并返回最终 CDN 地址，避免浏览器必须经过缺少 CORS 头的原始 302 入口。探测由纯 PHP 完成，无需安装 ffprobe；只读取文件头和 MP4/QuickTime 元数据，不缓存或中转整段视频。限制为最多三次跳转、十二次请求、1 MiB 正文，cURL 共享六秒截止预算；系统 DNS 解析仍由操作系统的解析器超时控制。

探针先按平台规则授权初始入口：抖音原始入口由平台返回的视频 ID 生成，小红书原始候选由已提取的 `originVideoKey` 生成。随后允许该入口通过 HTTPS 重定向分配公共 CDN，不再枚举 CDN 域名。每跳仍只允许 HTTPS 443、禁止认证信息与 fragment，检查所有 DNS 地址均为公网并固定连接 IP，验证 TLS，且保持 Range、响应类型、容器结构和读取预算检查。旧入口返回的 HTTP Location 会先升级为 HTTPS，不发送 HTTP 请求；localhost、内网地址及混合公网/内网 DNS 答案均拒绝。现有部署出网隔离继续生效。

这不是供用户提交任意 URL 的通用探针或媒体代理，公共 API 路由和平台输入范围不变。探测失败时保留已知可用主源，不阻断整次解析；小红书页面获取与 4K deferred 的既定范围不变。

## 微博播放与下载

微博 CDN 对部分视频校验微博来源请求头，普通浏览器页面不能代替来源站发送该头。公开解析层对已成功解析的、属于固定 `f.video.weibocdn.com` HTTPS 主源及备用源签发 30 分钟票据；媒体层校验票据后设置固定微博来源，按流传输。没有任意 `url=` 转发入口，不允许重定向或继承系统代理，DNS 结果必须全部为公网地址并固定到本次 cURL 连接，TLS 校验保持开启。现有双栈出网隔离仍须覆盖媒体池账户。

首次启用微博签名媒体服务前，必须完成以下配置，不能只替换前端；已配置的站点继续沿用，本轮 PR #7 不重复创建媒体池或轮换密钥：

1. 在仓库和 Web 根之外生成随机服务端密钥（例如 `openssl rand -hex 32` 的结果），以仅管理员可读的服务环境文件提供给专用 FPM 主进程。通过示例中的 `env[...]` 仅向公开解析池和媒体池传递；两池必须一致。不要放入 `VITE_*`、Git、URL、浏览器或日志。缺少有效密钥时，微博公开解析明确返回 503，不伪装为可播放成功。
2. 新增独立 `snap-media` 池，示例最多 2 个 worker、64 MiB 内存、310 秒终止期限；媒体代码单次传输上限 300 秒、单资源 1 GiB。保持解析池的较短超时与独立容量。源文件只读，传输不会缓存完整视频到服务器磁盘或内存。
3. 将精确 `/api/media/weibo` 路由指向该池，保持所有既有访问控制和限流覆盖它。排除压缩和额外响应缓冲，保留准确 `Content-Length`、`Content-Range`、`Accept-Ranges`；示例 100 ms 刷新保留客户端断开时的取消行为。Caddy/FPM 超时需大于代码传输期限。
4. 不在访问日志中记录完整票据查询串；票据是短期下载凭证。响应使用 `no-store`，不要让 CDN 缓存这些地址。密钥轮换会立即使已有票据失效，重新解析即可取得新票据。超过 30 分钟或上游 URL 已过期时，同样需要重新解析。
5. 固定新版本提交后验收真实解析 → 主源播放 → 拖动 → 切换画质 → 完整下载，核对媒体尺寸、大小/哈希及失败反馈；另检查无票据、改票据、过期票据、非法 Range、HEAD、206/416和客户端取消。

Caddy 参数依据：[php_fastcgi](https://caddyserver.com/docs/caddyfile/directives/php_fastcgi)、[reverse_proxy streaming](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#streaming)。示例是待部署配置，仓库通过检查不代表生产已经启用。

## 本地 HTTP 验证

`backend/tests/mock-internal.php` 不访问真实媒体平台。使用两个独立的本机终端，保持 loopback 绑定，不将测试 router 部署到公网。

1. 先核对 19186/19187 空闲。
2. 第一个终端启动内部 mock：

```sh
php -S 127.0.0.1:19187 -t backend/tests backend/tests/mock-internal.php
```

3. 单独检查其健康响应：

```sh
curl -H 'Host: snap.dokitool.com' 'http://127.0.0.1:19187/parse?platform=douyin&url=https%3A%2F%2Fv.douyin.com%2Ffixture'
```

4. 第二个终端为公开网关设置测试环境，再启动：

```sh
export SNAP_INTERNAL_ORIGIN=http://127.0.0.1:19187
export SNAP_INTERNAL_HOST=snap.dokitool.com
export SNAP_MEDIA_SIGNING_KEY=local-fixture-only-at-least-32-bytes
php -S 127.0.0.1:19186 -t backend/public backend/public/index.php
```

以上 `export` 是 POSIX shell 语法；PowerShell 分别使用 `$env:SNAP_INTERNAL_ORIGIN='http://127.0.0.1:19187'`、`$env:SNAP_INTERNAL_HOST='snap.dokitool.com'` 和 `$env:SNAP_MEDIA_SIGNING_KEY='local-fixture-only-at-least-32-bytes'`。该 Host 只是固定样例参数，HTTP 流量仍只发送到 loopback，不请求该域名。

5. 单独检查公开入口并运行 `php backend/tests/http-smoke.php http://127.0.0.1:19186`。当前套件含九平台分发、微博票据签发、固定 Host/URL/路径、错误响应/重定向、真实无效输入业务拒绝和封闭路径；示例签名值仅用于假媒体固定样例，不得作为生产密钥。
6. 分别停止两个服务，确认退出。离线 CLI 套件包含连接失败场景，应在这两个服务停止后运行。

这些检查不验证生产 Caddy/FPM、真实平台或出网隔离。本地运行时、测试响应、原始日志与备份不提交到 Git。
