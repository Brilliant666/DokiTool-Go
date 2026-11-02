# DokiTool：PHP → Go 迁移对照与验收方案

当前方向：**公开 PHP main 持续保留，Go 在独立仓库维护，现已由维护者公开用于开发协作。** 维护和许可边界以 [MAINTENANCE.md](MAINTENANCE.md) 为准。本文保留第一阶段调研时的固定 PHP 源码证据；首个抖音实现、公共合同修复及 B 站迁移另记在 [验收记录](acceptance.md)。下文私库建立阶段的描述保留历史语境，不代表当前可见性。

入库位置：`docs/go-migration/mapping.md`。历史阶段顺序是先骨架、unified 层和稳定 ID 对拍，再迁抖音；未来 Go 发布属于私库计划且需另行授权，不以取代公开 PHP 分支为目标。

## 1. 基线与证据范围

- 调研日期：2026-10-31（Asia/Shanghai）。
- 仓库：[Brilliant666/DokiTool](https://github.com/Brilliant666/DokiTool)。
- 调研时 PR #9 已合并；原冻结 main：`665bd55134982b9fe107aa1cd00373ba8da8ff3c`。本节函数与行号均对应此历史基线，不宣称是当前生产版本。
- 文件树：`93744d4d718c3299f82640d2888e93252106db70`，与已审查 PR #9 Head `6349cdc4e609b673c63101ea0b791a2deea6b395` 的文件树一致。
- 已核对范围：27 个生产 PHP 文件，5,372 行，220 个命名函数/方法；9 个平台注册项。统计不含 PHP 测试、文档、前端及匿名函数，不沿用 brief 中未说明口径的“约 7.6K 行”。完整清单见附录 A。
- 已阅读：统一 Schema、适配/选择/聚合/筛选、网关、九平台请求链路、共享媒体探测、微博媒体服务、主要测试入口、前端消费/构建关联和部署示例。
- 参考输入：用户附件 `dokitool_go_migration_brief.md`；其意见作为待审建议，不替代源码事实。

第一阶段没有执行真实平台请求、浏览器下载、生产探测或重新运行全套测试。文中 960/294/13/252/24 等通过数属于已完成的 PHP/PR #9 基线，不代表 Go 验收。部署文件是仓库示例，尚未核实服务器实际 OS、架构、Caddy、服务账户或出网规则。

## 2. 对 brief 的修订意见

| 原建议 | 判断与推荐写法 |
| --- | --- |
| unified-v1 就是全部验收标准 | 保留它作为 JSON 结构基准，同时验收 HTTP 路由/状态/错误、语义规则、稳定身份、正式前端消费，以及微博二进制媒体协议。Schema 合法不等于行为等价。 |
| 输出逐字段与 PHP 一致 | 对相同上游输入、固定时钟和配置做确定性逐字段对拍；真实链接的两次独立请求允许已证实的动态差异，但不能忽略身份、候选、默认或媒体事实。 |
| 画质及编码不低于 PHP | 不丢既有完整可下载候选、已确认规格和受保护能力；默认规则不变。H.264/HEVC/AV1 不定义统一高低顺序，不用裸视频替代完整音画文件。 |
| 前端一行不改 | 页面、产品消费、下载核心和 Worker 保持不改；允许迁移直接调用 PHP 的测试生产器/脚本。保留平台 JSON 的路径，可避免改前端目录生成器。 |
| 一个平台一个 adapter，新增平台只注册 | 同意。平台原始字段解释、身份规则、请求策略归平台包；共享 default/过滤/验证只保留一份。新增同类平台还需对应测试与受控出站配置，不需要前端特判。 |
| HTTP 先抽接口，预留 TLS 库 | 同意最小注入点，首版标准库；小红书页面可替换传输。暂不选定或引入 TLS 指纹库，不做通用插件框架。接口不能绕过主机、重定向、DNS、预算和取消策略。 |
| 全部出站沿用 media-probe 白名单 | 不能直接照搬。页面、详情 API、匿名会话、媒体探测、微博媒体有不同主机与限制；当前普通解析器并没有完整的统一 DNS/重定向沙箱。 |
| 每平台三条，含图集/Live/视频 | 改为按已声明能力和真实分支采样。B 站应覆盖分 P/合集；微博、头条、皮皮搞笑不要求不存在的 Live 能力。 |
| 一次切换并删除 PHP | 已被仓库拆分决定取代。公开 PHP main 持续保留；Go 私库未来发布或目标实例切流另行确认，不将私有迁移分支反合公开仓库。冻结 PHP 基准和回滚材料继续保留。 |
| git revert 就是回滚 | git revert 只处理源码。部署回滚还需上一版本应用产物、PHP/FPM 可恢复环境、Caddy 局部配置和同一签名密钥；切流与源码删除不能混成一个动作。 |

### 2.1 小红书范围的精确定义

当前 `XiaohongshuParser::videoCandidates` 已抽取普通 stream、mediaV2 与 originVideoKey，构造 qn/al/qc/bd 来源并做有界探测；公共 adapter 没有“所有 origin 一律 deferred”的分支。Schema 中的 deferred 是能力预留，不能在没有页面数据时虚构“已发现 4K”。

建议 PR #10 的约束写为：**保留已有候选提取与测量行为；不新增 TLS 指纹传输方案，不承诺小红书 4K 页面到浏览器完整保存的端到端成功。** 如果标准 Go 传输自然取得真实 SSR，原有算法得到可信原始候选，应正常保留，不人为降级为 deferred。4K 专项和合流执行器另行验收。

现有研究记录说明了页面传输差异和媒体探测事实，尚未证明唯一原因就是 TLS 指纹。不得把“预留可插拔客户端”写成“已经解决小红书 4K”。

证据：[小红书候选提取](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/xiaohongshu/XiaohongshuParser.php#L148)、[公共适配器](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-adapters.php#L383)、[研究记录](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/docs/research/xhs-4k-transport-comparison-2026-10-30.md)。

## 3. 建议架构：一个 Go 后端单体，保留现有仓库布局

已确认 Go module 位于 `backend/`，不在仓库根另建平行后端。以下为完整迁移的职责分工，是否已经实现以验收记录为准。

| 建议位置 | 职责 |
| --- | --- |
| `backend/cmd/dokitool/` | 服务启动、配置检查、退出处理、版本信息和需要保留的 catalog 导出能力 |
| `backend/internal/api/` | 公开路由、输入/版本校验、错误/HTTP 映射、请求级编排；不含平台字段判断 |
| `backend/internal/catalog/` | 从唯一目录绑定平台 ID、输入域与 adapter；校验注册完整性 |
| `backend/config/platforms.json` | 保留唯一语言无关目录与原路径，公开字段保持一致；私库未来若调整 Go 内部注册信息，不能反向删除公开 PHP 所需字段 |
| `backend/config/` 的资源嵌入入口 | 将同一目录嵌入二进制，避免工作目录依赖和第二份目录副本 |
| `backend/internal/unified/` | 类型、结构/语义验证、来源与身份、事实聚合、选择、普通/debug 视图；按文件划分，不为每个 helper 建独立包 |
| `backend/internal/platform/<id>/` | 九个平台的请求编排、上游结构解码、身份规则及规范化；允许少量平台专用请求策略 |
| `backend/internal/fetch/` | 可替换 HTTP 执行器、请求目的策略、逐跳检查、DNS/连接约束、有限读取、取消；不暴露公共任意 URL 代理 |
| `backend/internal/probe/` | 已有有界 ISO BMFF/MP4/MOV 元数据读取，保留尺寸/编码/时长/码率事实 |
| `backend/internal/media/weibo/` | 既有票据、受限媒体请求、HEAD/Range、流式传输与取消；有独立并发容量/时间预算 |
| `backend/testdata/` | 脱敏上游输入、HTTP 应答序列、PHP 期望输出和基准元数据；无用户 Cookie/生产密钥 |

Schema 继续以 `docs/schema/unified-v1/unified-v1.schema.json` 为唯一规范。需要内嵌时由构建流程从该文件产生受校验资源，不能手工维护第二份。Go 的结构校验实现须支持现用 Schema 语义；外部 Python 验证器继续独立验证 Go 输出。具体 Schema 库和 Go 工具链版本在第二阶段按受支持版本选定、固定，不在只读阶段提前引入依赖。

### 3.1 Go 运行时无需复制的 PHP 结构

公开 PHP 网关 → loopback HTTP → 内部 PHP → include 脚本/output buffer，是现有 PHP 脚本隔离的实现方式。Go 推荐直接在请求 context 内调用注册 adapter，保留公开行为即可；无需复制内部 HTTP 自调用、全局 `$_GET`、`chdir`、`echo/exit` 或旧 3.0 数值码中间 JSON。

部署可由 Caddy 把原公开 API 转到仅监听 loopback 的 Go 服务；静态前端仍由 Caddy 提供。`SNAP_INTERNAL_ORIGIN/HOST` 在退出 PHP 内部链路后不再是 Go 产品必需变量。`SNAP_MEDIA_SIGNING_KEY` 保持既有含义与私密来源。

### 3.2 “前端零改动”的精确范围

- 保持 `frontend/src` 的解析/选择/下载产品逻辑及 `frontend/public/downloads/` 的 Worker、bridge、task-store 不变。
- `VITE_API_MODE=php` 当前只是同源 API 模式的历史名称，可以继续指向 Go；无需为了名称改成 go 而修改消费者。
- 保留 `backend/config/platforms.json`，现有前端 dev/build 直接读取它，继续自动产生同样的目录快照。
- 允许 `frontend/scripts/test-media-integration.mjs` 和测试文件中后端 fixture producer 从 PHP 改为 Go；保留原断言，并确保至少一组集成数据来自正式 Go 产品函数，不能全改为静态假输出。
- 新媒体类型/新执行能力仍是独立演进；本次不引入新的前端下载能力。

证据：[前端目录生成器](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/frontend/scripts/generate-platform-catalog.mjs)、[API 模式](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/frontend/src/services/apiConfig.ts)、[集成生产器](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/frontend/scripts/test-media-integration.mjs)。

## 4. 对外合同：路由、JSON、媒体三个层面

| 面向调用者的行为 | Go 验收要求 |
| --- | --- |
| `/api/parse` | GET、`schema_version=1.0`、分享文本取 URL、platform/all、普通/debug；响应结构和状态映射保持 |
| `/api/resolve` | 使用服务端给出的 URL；精确 item_id；当前实现会重解析作品并验证目标，将其设为 default_item_id，不是随意裁成一个 item；目标缺失不能借邻项 |
| `/api/platforms` | 同一公开目录、字段与顺序；仍需要版本参数；内部实现路径不能暴露 |
| `/api/media/weibo` | 保留 GET/HEAD/单段 Range、票据、200/206/416、错误反馈和流式行为；它不使用媒体解析 JSON Schema 作为二进制成功响应 |
| 旧六个解析路径 | 保留 410 和可读版本错误；未知 API 为 JSON 404，不能落 SPA |
| 方法/版本错误 | JSON API 非 GET 为 405 + Allow；版本缺失/不符为 409；注意不能让 Go 路由默认 HEAD/自动重定向改变这些行为 |
| JSON 响应头 | 保留 Content-Type、no-store、nosniff 和既有同源边界；不自动添加通配 CORS |
| 输入限制 | URL/分享文本长度、userinfo、控制字符、非默认端口、平台域匹配和数组参数拒绝需保留；重复 query 的解释需用现有行为夹具明确 |
| 旧内部 `/parse`、原始 PHP、`/api/v1/*` | 不作为新公开入口；Go 内部编排不必保持 PHP 网络拓扑 |

### 4.1 必须保持的处理顺序

1. 平台提取完整候选、明确媒体/音轨身份与技术事实。
2. 按既有规则完成规范化、聚合和矛盾隔离后，校验完整统一响应；不能借普通视图的 mux 过滤掩盖非法 metadata 或错误引用。原始坏 URL 的剔除、冲突候选转 deferred 仍按现有规则执行。
3. 应用公开来源资格；微博按固定提供方签发票据，不能延长上游已知到期时间。
4. 普通视图过滤确可由一个有效完整文件覆盖的 mux；debug 保留规范化全量候选。
5. 重新计算每个 item 的 default、不可用原因和 higher_quality_variants，并验证引用/投影。

平台 adapter 提供事实和身份；四项能力策略仍只有一个公共实现。其四项闭合类别继续为高帧率、确认的 HDR 类型、已证明的备选语言、多声道，不新增 codec/文件大小/码率保护。保持 1% FPS 容差、过期来源的语言证据和多音轨父级摘要的保守处理。

证据：[公开处理顺序](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/gateway.php#L35)、[默认与语义](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-contract.php)、[统一筛选](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-policy.php)。

## 5. 字段等价与差异判定

| 内容 | 固定上游输入/时钟/配置 | 实时双实现对照 |
| --- | --- | --- |
| item/variant/resource/source ID；default 引用；分 P/分组 | 严格相同，不能删掉 ID 后比较 | 相同媒体身份必须保持；动态签名不应造成身份漂移 |
| 类型、state、delivery、资源角色、能力与完整性 | 严格相同 | 不得丢完整文件、错误配对或把裸视频当默认 |
| 尺寸、编码、时长、音轨事实、证据/冲突 | 严格相同；包括缺失、零、false | 核对实际事实；不同上游表示单独解释，不用“动态”掩盖信息丢失 |
| 普通过滤、debug 全量、default/null 原因/higher | 严格相同 | 解释来源可用性变化，仍执行同一规则 |
| items、variants、resources、groups、sources、pieces 的顺序 | 保持现有语义及稳定顺序 | 同一次上游输入的顺序不能被 Go map 打乱 |
| CDN URL、签名、过期时间、generated_at | 固定输入与时钟优先精确相同 | 仅规范化已证实的动态部分；保留资源路径、未识别参数、重复 query 和有效期判断 |
| 微博 ticket | 固定 clock/key 时可逐字比较；至少双向验签兼容 | 可随签发时间变化，但有效期、来源、密钥与签名必须有效 |
| 点赞等统计、标题等可变化内容 | 同输入严格相同，已知零不能丢 | 允许真实请求时间差，保留原记录用于解释 |
| JSON 对象键序、空白、数值 30/30.0 | 外部结构比较可规范化 | 不构成业务差异；身份哈希的输入字节不能按此规则随意改 |

**禁止的对拍捷径：**忽略全部 URL、全部 metadata、全部 ID；把 missing/null/false/0 视为相同；只比较 default.url 是否存在；只统计版本数量；只让 Go 新写的校验器自证。

### 5.1 迁语言高风险点

- 身份是跨版本契约。现有 SHA-256 输入有原字符串，也有 PHP `json_encode` 字节；转义、字段顺序和数字表示不同会改变 ID。将来源、variant、B 站配对、最右 item 身份保存为固定期望样例。
- 用字符串或保留精度的数字类型接收长作品 ID；不能先转浮点再转回字符串。文件大小/时长/码率用明确整数范围；PHP round 与 Go 舍入需对照。
- 必须区分未知、省略、显式 null、false、0；`default:null` 是有效状态，`has_audio:false` 和统计零都不能被省略，数组不能误输出为 null。
- 媒体原 URL 保留传输字节；仅身份算法中移除明确允许忽略的签名参数。自生成 API URL 的编码与上游 URL 处理分开。
- 使用有序切片承载候选和来源，map 只作索引；最右以 imgs 为展示顺序、video 原键为身份。
- 正则不能机械复制 PHP PCRE；小红书图片处理和部分 URL 校验含断言语法。SSR 使用有限扫描/JSON 解码，不能执行网页 JavaScript。
- 共享连接池可以复用；微博匿名 jar、头条 ttwid、probe 计数、deadline、guestAttempted 均保持请求/实例隔离。不得把 PHP 每请求状态改成可变全局单例。

Go 官方文档说明 JSON 的空值、省略和数字解码均有明确规则，HTTP Client 的总超时也包含响应体读取。因此这些项目要通过固定样例和请求级预算明确验证，不能依靠语言默认行为。

## 6. HTTP 与探测迁移

### 6.1 一个最小执行器，按请求目的施加策略

首版采用标准 `net/http`，以可注入请求执行器支撑 fake transport 和将来小红书页面传输替换。平台只选择受控目的，不允许公开参数指定任意代理、TLS 配置或请求头。

| 请求目的 | 已发现的主机/路径例子 | 主要约束 |
| --- | --- | --- |
| 分享页/短链 | v.douyin.com、b23.tv、快手分享页、rednote/xhslink | URL 验证、逐跳目标检查、原 query、明确跳转次数和总预算 |
| 详情/播放 API | api.amemv.com、api.bilibili.com、weibo.com、h5.pipigx.com、share.xiaochuankeji.cn | 固定 endpoint、平台专用 headers、请求/响应限额、严格内容身份 |
| 匿名会话 | passport.weibo.com、ttwid.bytedance.com | 每次 parse 局部 Cookie 状态、有界初始化、不回传给前端 |
| 媒体头探测 | aweme.snssdk.com/aweme/v1/play/、受限抖音 CDN、sns-video-{qn,al,qc,bd}.xhscdn.com | HTTPS、固定 IP、全部 DNS 答案公网、严格 Range、有限字节/请求/时间 |
| 微博媒体 | f.video.weibocdn.com | 既有签名验证、固定 Referer、无代理/跳转、identity、流式和独立容量 |
| 仅返回浏览器的媒体 URL | 各平台普通 CDN/图片/音频地址 | 仍走浏览器直连；不会因为出现在 JSON 中就自动获得服务器抓取权限 |

上表是静态代码确定的用途，不是已在线验证的完整域名名单。后续移植按实际链路和脱敏请求录制补齐精确域/点分后缀规则，不使用任意主机兜底。入口识别域、服务端出站域与浏览器媒体域不能混用。

策略执行不随传输库更换而失效：probe 和微博媒体既有的逐跳/禁止跳转、DNS 公网检查及连接绑定、TLS Host/SNI/证书校验、禁代理、总截止和限额原样保留。普通解析请求新增的统一出站限制按已确认链路验证，不能用任意主机或任意跳转兜底，也不能声称这些机制已全部存在于 PHP。真实部署还要把现有服务账户出网隔离迁到 Go 服务，源码有白名单不等于服务器隔离已验证。

不能对全部平台统一套浏览器 UA。B 站详情/播放 API 当前刻意使用普通服务器请求；小红书页面、快手页面及移动分享页各有现存请求模板。请求头“清理优化”需要对拍证据。

### 6.2 已有媒体探测不是空接口

共享 `SnapMediaProbe` 必须迁移现有实际能力：1 MiB 总读取、12 次请求、6 秒、3 跳、4 GiB 文件上限，校验 Range/长度/MIME/编码，并从 ISO BMFF 的 moov/stsd/mdhd/stsz 等结构读取真实视频轨信息。大 mdat 可跳过，尾部 moov 和截断布局都要覆盖；失败保留已知候选，不伪造规格。小红书扩展还识别 MOV/QuickTime。无需新增 ffprobe 或完整视频缓存。

这些是当前代码参数，不是本次新性能测量；Go 的 deadline 还应覆盖可取消 DNS/连接，不能每次子请求重新获得完整预算。

证据：[通用 transport 当前限制](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/runtime.php)、[共享 probe](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-probe.php)、[部署出网要求](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/docs/DEPLOYMENT.md#L91)。

## 7. 九平台迁移总表

| 平台 | 当前主链路 | 必须保留 | 真实样本应覆盖 |
| --- | --- | --- | --- |
| 抖音 | 分享/短链 → aweme ID → api.amemv detail → 表示/音轨 → 原始入口 probe | 完整文件与裸轨区别；同 URL 的明确配对；后续 FPS/HDR 观察；图集/Live 对应；稳定续签身份 | 多清晰度完整视频、图集、混合 Live、短链；有实际原始入口的作品 |
| 小红书 | 短链 → rednote 优先/xhs 备用 → SSR → stream/mediaV2/origin → probe | 原始 query、笔记 ID、视频封面非图集、MOV、已知零统计；已有 origin 行为 | 普通视频、图集、混合 Live、有效/失效 token 与验证页；4K 专项另做 |
| B 站 | BV/p → view → 选定 cid 的 playurl → parts/collection | p 不丢、不借 P1；其余条目 lazy；真实播放尺寸；file/segments/mux；缺音轨明确原因 | 单 P、多 P 的 P2、失败 P、合集、b23 带 p；按可获得性记录 DASH/segments |
| 快手 | HEAD 重定向 → 页面 → INIT_STATE/APOLLO → manifest | 两条 SSR 路线、空 Cookie 现状、图集/单图、备源和未知码率单位 | 视频、图集/单图、两类页面；受限失败 |
| 微博 | status 或 TV component → 有界 guest → 可选补充 → 签名媒体 | 可选补充失败保主结果、真实画质、匿名 jar 隔离、票据与媒体 HTTP | 正文、TV、受限；HEAD/seek/完整保存/取消单独验收 |
| 头条 | 同域短链 → ttwid → 规范页面 RENDER_DATA | 精确作品 ID、原百分号/加号、base64 主备、author.description、35 秒共享预算 | 规范链接、短链、多画质、匿名会话/失败 |
| 皮皮虾 | 同域短链 → /ppx/item/ID → 目标 item JSON | 超大 item_id_str、无评论串媒体、原始媒体 URL、图集和明确错误 | 视频、图集、短链、目标错配/不可用 |
| 最右 | pid/必要 HEAD → 固定 post/detail_h5 | imgs 顺序、videos 原 map 键身份、缺前项不改后项 ID、图片原图 | 多视频、图集、短链、目标缺失 |
| 皮皮搞笑 | pid/mid → 固定 fetch_content POST | 整数参数、完整 private_videos、无媒体不空成功；保持当前身份规则 | 视频、多视频、非法输入/无媒体 |

九平台详细请求链路见附录 B；逐函数迁移定位见附录 A。正式迁移仍以抖音作为首个平台；建议把微博和 B 站提前到紧随其后的批次，尽早验证媒体流与 resolve 两类架构风险。其余平台顺序可按现成样本调整，无需成为硬门禁。

## 8. 微博媒体的专项兼容

该路径是既有受限功能，不属于新增通用代理。迁移必须保留：

1. v1 HMAC-SHA256 票据，最长 1,800 秒且不超过上游到期时间；正常切换保留同一私密密钥。
2. 固定 HTTPS `f.video.weibocdn.com`、全部 DNS 答案校验并绑定 IP、不跟随跳转和系统代理。
3. GET/HEAD/单段 Range，200/206/416，准确 Content-Length/Content-Range/Accept-Ranges；HEAD 用有界 GET bytes=0-7 验证，不能读整文件。
4. 1 GiB 上限、300 秒传输；小 Range、seek、截断、非法关键头与取消；媒体响应不压缩、不整文件缓冲，已发送媒体后不追加 JSON。
5. PHP 签发→Go 验证、Go 签发→PHP 验证的固定样例，覆盖切换前票据和回滚兼容。
6. 与解析不同的容量和时间预算。一个二进制可以做到，但不能用统一 45 秒客户端/服务器超时截断 300 秒媒体，也不能让长流占满解析容量。

Caddy 的媒体路由继续独立，保留断开取消语义；不要随意将 `flush_interval` 改为负数低延迟模式。官方文档指出该设置会改变客户端断开时是否取消上游请求。

证据：[票据与媒体实现](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/weibo-media.php)、[媒体部署要求](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/docs/DEPLOYMENT.md#L99)、[Caddy streaming](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#streaming)。

## 9. 验收迁移矩阵

| 既有入口/证据 | 后续 Go 阶段怎么使用 |
| --- | --- |
| Schema + 29 设计样例 + 13 组独立检查 | 原样保留规范和独立验证；Go 输出喂给现有 Python 校验器 |
| contract.php：91 项；unified-contract.php：41 项 | 移植路由、结构、引用、default、null 原因及错误行为；PHP 内部 HTTP/扩展特征按职责退役或替换 |
| parser-contract.php：抖音 14、皮皮搞笑 6、B 站 21、最右 5、微博 4 | 相同原始结构输入对应平台 Go 提取器；保留非法输入/无媒体分支 |
| weibo 33、toutiao 41、pipixia 24 | 迁移有状态请求序列、解码和明确失败，增加常驻进程所需的并发会话隔离检查 |
| weibo-media 109、douyin-media 51 | 媒体协议/票据/流/取消，以及有界 MP4 探测；不能只迁 JSON 样例 |
| test_unified_xhs.php：29 | SSR 解析、多编码/备源、原始入口测量、Live、URL 安全；不凭 fixture 声称 4K 在线成功。deferred 合同由 unified-contract.php 及 Schema 示例 27 覆盖 |
| unified-review-identity：51；platforms：65 | 保留续签身份、显式选择、最右顺序/缺失目标和作者/统计映射 |
| media-policy：83；media-merge：159；douyin-observation：124 | 完整迁移最近数轮修复场景，尤其反序、后补事实、冲突隔离、主语言来源及多音轨归属 |
| 后端 960/960、48 PHP lint | 是 PHP 基线，不要求 Go 凑相同条数；按场景追踪映射，以 Go 测试/静态检查替换 PHP 专属机制 |
| 252 份独立产品输出 | 9 平台 + 策略 74 + 聚合 65 + formatter 52 + 平台专项 12 + 身份 20 + HTTP 20；先保存固定输入/期望，再由真实 Go 路径导出并独立验证 |
| 本地 HTTP 24；http-smoke.php | Go 正式 HTTP handler + 受控上游应答；保留公开路径、debug、resolve、refresh、票据边界，拓扑无需还是两个 PHP 进程 |
| 前端 294；跨语言 13 | 前端原有断言保持；把 fixture producer 改为实际 Go 导出，验证默认、显式选择、任务快照和续签 |
| 类型/构建/SSG/预算/公共清单 | 保证目录生成、资源、契约和产物一致；新增 Go 源码须登记 public-files.json 并更新对应 ignore |
| 历史真实平台与浏览器记录 | 只作为样本线索；不能替代 Go 新实现的线上/媒体保存验收 |

### 9.1 确定性对拍先于在线对拍

先保留冻结 PHP main、脱敏输入、预期输出、时钟/签名密钥测试值与 fixture 哈希。使用相同上游应答序列分别进入 PHP 和 Go 的正式产品路径；输出差异按第 5 节处理。

再在同一环境、相近时间、相同匿名/凭据条件下运行真实链接，保存请求条件、返回差异和媒体事实。两边都被风控不能算迁移通过；PHP 可得而 Go 不可得应作为待解释差异；PHP 已失效的链接不能替代有效成功样本。合成输入与在线样本分别记账。

真实验收按能力矩阵覆盖，不要求凑整齐的“9×3”。媒体 JSON 对拍之外，需用 Go API 结果进入未修改的前端完成代表性直接下载、resolve/续签和微博媒体保存；若 Worker 字节未变且无相关风险，不机械重做全部历史升级实验。

### 9.2 单进程特有检查

后续实现应定向验证并发会话隔离、请求取消、响应体关闭、有限内存、probe 预算不串用、媒体与解析容量隔离。对新 Go 并发路径运行 race 检查；这属于 PHP→常驻服务的实质风险，不是单纯补测试数量。

## 10. 私库未来发布与可恢复切流

### 10.1 部署目标

私库未来发布方向为一个 Go 二进制 + 既有 Caddy 静态前端 + systemd 管理。具体站点、是否替换某个 PHP 运行实例及发布时间另行确认，公开 PHP main 不随之删除。本轮仅离线同步，不部署或改变现有生产。系统 CA、DNS、私密环境文件、访问控制和出网规则仍是运行条件；“静态二进制”不代表不需要这些条件。

未来若获准在既有实例切流，应保持原 API origin/路径与配套前端产物，Caddy 只局部替换 PHP API 转发为 Go reverse_proxy；保留 `/downloads/*`、缺失 hash 资源、SEO 文件与 SPA 的精确路由顺序。不扩大 Worker scope、不强制升级或清空任务，不轮换微博密钥。

systemd 模板需明确非特权账户、只读发布目录、密钥来源、loopback 监听、停止/排空预算及资源限制；部署示例更新不等于已经获准生产上线。解析和媒体容量分别配置，不把旧三个 FPM 池逐一翻译成三项 Go 微服务。

### 10.2 未来接替既有实例的验收条件

1. 九平台已有能力与失败行为完成迁移；无静默 PHP fallback。
2. 确定性对拍、独立 Schema/语义、HTTP 和未改前端消费通过；真实样本与未验证项清楚记录。
3. 微博媒体协议、旧票据兼容、直接媒体交付和可取消流通过；小红书 4K 新路径与 DASH 合流仍另列范围。
4. Go 运行时不依赖 PHP；开发验证仍可调用已固定 PHP producer，原始输入、期望、生产器、哈希、语言无关 JSON、Schema、来源说明和许可证保留。
5. 切换前将固定 PHP 基线的输入、实际期望输出、时钟和来源哈希作为 fixture 入库；不得在比较失败时直接按 Go 输出刷新期望。
6. 保存可恢复的 PHP 旧发布包，必须包含同一版本的 PHP 应用、前端构建产物、Caddy 站点配置和 FPM 配置，以及对应运行环境说明。密钥和其他凭据只记录受保护来源引用，不复制值。
7. 切换前实际演练一次恢复上一发布包及 Caddy/FPM 路由的过程，并验证 API、代表性下载和旧票据。演练证据未完成时不能宣布可切换；Git revert 不替代服务回滚。详见 [回滚门槛](rollback.md)。

公开 `Brilliant666/DokiTool` 的 `main` 继续保留 PHP 实现。私库自身的运行目录与发布包可按 Go 职责组织，但不因此删除冻结 PHP 证据或反向清理公开仓库。是否停止某个已部署 PHP 实例属于另行确认的运维动作。不得用“当前无用户”推断没有活动下载或无需可恢复产物。

### 10.3 仓库配套

- 保留 `backend/config/platforms.json` 与 Schema；语言变化不改变公开字段。
- 私库沿用 `public-files.json` 和生成的 `.gitignore` 作为显式版本文件清单；名称不表示私库内容已获公开授权。只登记本阶段必要 Go/fixture/文档，不提交原始签名 URL、Cookie 或临时私密证据。
- 私库 README、API/部署说明按 Go 接续更新；公开 PHP 运行文档保留，不能整体覆盖回公开 main。历史验收记录保留其历史语境。
- 保留 NOTICE、backend/LICENSE 与 UPSTREAM.json 的来源记录；迁语言不删除既有来源声明。上游 PHP 仍可作为平台变更线索，后续采用流程变为审查并移植到 Go。
- 第一阶段只有 upstream-watch 定时/手动工作流；后续 PR #2 已增加 `Go migration contract`，执行固定 PHP 来源核验、Go race/vet、严格对拍、独立 Schema 和原前端消费。B 站增量另加固定 PHP 运行时的离线 oracle 重放，不把这些检查解释为真实平台或部署验收。

证据：[当前部署说明](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/docs/DEPLOYMENT.md)、[Caddy 示例](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/ops/examples/snap.caddy)、[公共清单检查](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/scripts/check-public-files.py)、[来源声明](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/NOTICE)。

## 11. 已确认的阶段顺序与边界

| 阶段 | 交付 | 进入下一阶段的条件 |
| --- | --- | --- |
| 1：只读调研（已确认） | 本对照表、请求链路、语义/测试/部署矩阵、争议点 | 四项边界已获用户确认 |
| 2：骨架与公共契约 | Go module、catalog、API/错误、unified、fetch 最小接口、冻结 PHP fixture 与 ID/ticket 对拍 | 公共合同与可重复验证路径建立；尚不删除 PHP |
| 3：平台迁移 | 抖音首先，微博/B 站尽早验证，再覆盖其余平台；每平台正式产品路径和对应测试 | 九平台既有能力全部有对应实现，不以“返回合法空结果”占位完成 |
| 4：对拍与交付验证 | 固定输入逐字段报告 + 同条件真实链接差异 + 前端消费/媒体保存 | 差异已解释或修正；未验证项与迁移阻断分别列明 |
| 5：私库发布准备 | Go 发布包、systemd/Caddy 局部配置；涉及既有实例切流时保留完整 PHP 旧发布包并实际演练回滚；公开 PHP main 持续保留 | 冻结 fixture 已入库、所需验收与演练完成；用户另行确认目标与发布时间，不自动部署 |

原阶段边界继续保留，其中旧“一次切换、退出 PHP”目标由本轮仓库拆分决定取代：

1. 等价要求采用“固定输入严格比较，真实动态字段有证据地解释”。
2. 前端产品与 Worker 零改动，同时允许测试生产器迁移；保留平台 JSON 原路径。
3. 保留小红书已有 origin 提取/测量，不人为降级；4K 新传输及端到端专项仍在后续 PR。
4. 公开 PHP main 持续保留；后续 Go 开发和 Draft PR 仅进入私库，采用 PHP → Go 选择性同步。未来发布或实例切流另行授权，并保留能实际恢复的上一发布产物。

## 12. 外部技术依据

以下是语言/服务器行为参考，不是平台可用性证据：

- [Go encoding/json](https://pkg.go.dev/encoding/json)：数字解码、空值、省略、编码语义。
- [Go net/http](https://pkg.go.dev/net/http)：请求 context、客户端总超时、连接池和压缩行为。
- [Go regexp](https://pkg.go.dev/regexp)：标准正则语法与匹配规则；迁移 PCRE 时需逐条核对。
- [Caddy reverse_proxy](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)：媒体流、缓冲与客户端取消。


## 附录 A：完整生产 PHP 文件/函数 → Go 定位

以下目标包相对 `backend/` Go module。名称用于评审和任务定位，可在实现时合并纯 helper；不要求为 220 个 PHP 函数创建 220 个公共 Go API。标为退役的是 PHP 机制/无调用辅助，公开行为仍须由指定位置承接。

| PHP 文件 | 行数 | 命名函数/方法 | 建议主要归属 |
| --- | ---: | ---: | --- |
| [backend/api/bilibili/bilibili.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/bilibili/bilibili.php) | 287 | 10 | `internal/platform/bilibili` |
| [backend/api/douyin/DouyinParser.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/douyin/DouyinParser.php) | 1096 | 33 | `internal/platform/douyin` |
| [backend/api/douyin/douyin.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/douyin/douyin.php) | 34 | 0 | `internal/platform/douyin` |
| [backend/api/kuaishou/KuaishouSpider.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/kuaishou/KuaishouSpider.php) | 353 | 9 | `internal/platform/kuaishou` |
| [backend/api/kuaishou/ksjx.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/kuaishou/ksjx.php) | 31 | 0 | `internal/platform/kuaishou` |
| [backend/api/pipigx.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/pipigx.php) | 164 | 4 | `internal/platform/pipigx` |
| [backend/api/ppxia.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/ppxia.php) | 201 | 10 | `internal/platform/pipixia` |
| [backend/api/sv.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/sv.php) | 5 | 0 | `internal/api` |
| [backend/api/toutiao.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/toutiao.php) | 253 | 8 | `internal/platform/toutiao` |
| [backend/api/weibo.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/weibo.php) | 293 | 20 | `internal/platform/weibo` |
| [backend/api/xiaohongshu/XiaohongshuMediaProbe.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/xiaohongshu/XiaohongshuMediaProbe.php) | 59 | 4 | `internal/platform/xiaohongshu` |
| [backend/api/xiaohongshu/XiaohongshuParser.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/xiaohongshu/XiaohongshuParser.php) | 280 | 13 | `internal/platform/xiaohongshu` |
| [backend/api/xiaohongshu/xhsjx.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/xiaohongshu/xhsjx.php) | 38 | 0 | `internal/platform/xiaohongshu` |
| [backend/api/zuiyou.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/zuiyou.php) | 229 | 7 | `internal/platform/zuiyou` |
| [backend/bin/catalog.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/bin/catalog.php) | 4 | 0 | `cmd/dokitool` |
| [backend/internal/index.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/internal/index.php) | 8 | 0 | `internal/api` |
| [backend/lib/douyin-media-probe.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/douyin-media-probe.php) | 22 | 1 | `internal/platform/douyin` |
| [backend/lib/gateway.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/gateway.php) | 294 | 14 | `internal/api` |
| [backend/lib/media-adapters.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-adapters.php) | 529 | 26 | `internal/unified` |
| [backend/lib/media-contract.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-contract.php) | 357 | 17 | `internal/unified` |
| [backend/lib/media-metadata.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-metadata.php) | 51 | 4 | `internal/unified` |
| [backend/lib/media-policy.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-policy.php) | 125 | 7 | `internal/unified` |
| [backend/lib/media-probe.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-probe.php) | 261 | 13 | `internal/probe` |
| [backend/lib/platforms.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/platforms.php) | 26 | 2 | `internal/catalog` |
| [backend/lib/runtime.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/runtime.php) | 46 | 3 | `internal/fetch` |
| [backend/lib/weibo-media.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/weibo-media.php) | 318 | 15 | `internal/media/weibo` |
| [backend/public/index.php](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/public/index.php) | 8 | 0 | `internal/api` |

### backend/api/bilibili/bilibili.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/bilibili/bilibili.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `parseBilibili`（12） | `internal/platform/bilibili.Parse` | 以 context/input 返回结构化结果，不修改进程全局状态 |
| `bilibiliRedirectTarget`（62） | `internal/platform/bilibili.RedirectTarget` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `buildBilibiliResponse`（75） | `internal/platform/bilibili.BuildBilibiliResponse` | 上游解码/事实提取归平台；可直接规范化，不必生产旧中间 JSON |
| `getFinalHtmlContent`（148） | `internal/platform/bilibili.GetFinalHtmlContent` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `extractBilibiliBVId`（206） | `internal/platform/bilibili.ExtractBilibiliBVId` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `bilibiliApiRequest`（232） | `internal/platform/bilibili.ApiRequest` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `bilibiliDecodeResponse`（250） | `internal/platform/bilibili.DecodeResponse` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `bilibiliFailure`（260） | `internal/platform/bilibili.Failure` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `bilibiliQuality`（276） | `internal/platform/bilibili.Quality` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `cleanUrlParameters`（283） | `internal/platform/bilibili.CleanUrlParameters` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |

### backend/api/douyin/DouyinParser.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/douyin/DouyinParser.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `__construct`（20） | `internal/platform/douyin.New` | 注入请求执行器、时钟/探测，保持每请求状态隔离 |
| `parse`（46） | `internal/platform/douyin.Parse` | 以 context/input 返回结构化结果，不修改进程全局状态 |
| `fetchDetail`（79） | `internal/platform/douyin.FetchDetail` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `extractShareUrl`（124） | `internal/platform/douyin.ExtractShareUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `resolveRedirect`（136） | `internal/platform/douyin.ResolveRedirect` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `extractAwemeId`（175） | `internal/platform/douyin.ExtractAwemeId` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `output`（205） | `internal/platform/douyin 返回值 + internal/api.WriteJSON` | 退役脚本 echo/header/exit 壳，保留公开错误语义 |
| `formatData`（217） | `internal/platform/douyin.FormatData` | 上游解码/事实提取归平台；可直接规范化，不必生产旧中间 JSON |
| `detailMetadata`（389） | `internal/platform/douyin.DetailMetadata` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `pickImageListItemUrl`（421） | `internal/platform/douyin.PickImageListItemUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `extractLivePhotoVideoUrl`（441） | `internal/platform/douyin.ExtractLivePhotoVideoUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `collectPlayUrlCandidates`（512） | `internal/platform/douyin.CollectPlayUrlCandidates` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `extractHighestQualityVideo`（538） | `internal/platform/douyin.ExtractHighestQualityVideo` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `candidateDeliveryKey`（664） | `internal/platform/douyin.CandidateDeliveryKey` | 区分媒体身份/配对/观察去重，只有既定稳定身份进入公开 ID |
| `candidateObservationKey`（674） | `internal/platform/douyin.CandidateObservationKey` | 区分媒体身份/配对/观察去重，只有既定稳定身份进入公开 ID |
| `videoMetadata`（683） | `internal/platform/douyin.VideoMetadata` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `representationIdentity`（716） | `internal/platform/douyin.RepresentationIdentity` | 区分媒体身份/配对/观察去重，只有既定稳定身份进入公开 ID |
| `audioCandidates`（727） | `internal/platform/douyin.AudioCandidates` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `pickCover`（754） | `internal/platform/douyin.PickCover` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `extractVidFromVideoInfo`（808） | `internal/platform/douyin.ExtractVidFromVideoInfo` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `buildOriginalPlayUrl`（828） | `internal/platform/douyin.BuildOriginalPlayUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `resolveOriginalVideoUrl`（840） | `internal/platform/douyin.ResolveOriginalVideoUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `followLocation`（854） | `internal/platform/douyin.FollowLocation` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `resolveVidBatch`（906） | `internal/platform/douyin.ResolveVidBatch` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `httpGet`（922） | `internal/platform/douyin.HttpGet` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `probeVideoSize`（945） | `退役候选（当前无调用点）` | 实际 size/probe 路线已另有实现；不为逐函数翻译搬死代码 |
| `replacePlayHost`（980） | `internal/platform/douyin.ReplacePlayHost` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `formatSizeLabel`（988） | `internal/platform/douyin.FormatSizeLabel` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `normalizeCreateTime`（1010） | `internal/platform/douyin.NormalizeCreateTime` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `formatPublishTime`（1031） | `internal/platform/douyin.FormatPublishTime` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `toHttps`（1048） | `internal/platform/douyin.ToHttps` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `strv`（1059） | `internal/platform/douyin.Strv` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `ensureVideoMimeType`（1074） | `internal/platform/douyin.EnsureVideoMimeType` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |

### backend/api/douyin/douyin.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/douyin/douyin.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| 脚本顶层入口 | `internal/catalog → platform/douyin.Parse` | 脚本入口退役，保留参数/错误语义 |

### backend/api/kuaishou/KuaishouSpider.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/kuaishou/KuaishouSpider.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `__construct`（17） | `internal/platform/kuaishou.New` | 注入请求执行器、时钟/探测，保持每请求状态隔离 |
| `analyze`（27） | `internal/platform/kuaishou.Parse` | 以 context/input 返回结构化结果，不修改进程全局状态 |
| `extractFromInitState`（56） | `internal/platform/kuaishou.ExtractFromInitState` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `extractFromApolloState`（184） | `internal/platform/kuaishou.ExtractFromApolloState` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `mediaCandidates`（249） | `internal/platform/kuaishou.MediaCandidates` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `filterMediaData`（271） | `internal/platform/kuaishou.FilterMediaData` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `getRedirectedUrl`（282） | `internal/platform/kuaishou.GetRedirectedUrl` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `curlRequest`（304） | `internal/platform/kuaishou.CurlRequest` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `extractContentIdAndType`（339） | `internal/platform/kuaishou.ExtractContentIdAndType` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |

### backend/api/kuaishou/ksjx.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/kuaishou/ksjx.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| 脚本顶层入口 | `internal/catalog → platform/kuaishou.Parse` | 空 Cookie 现状与平台请求头由 adapter 承接 |

### backend/api/pipigx.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/pipigx.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `formatResponse`（21） | `internal/platform/pipigx 返回值 + internal/api.WriteJSON` | 退役脚本 echo/header/exit 壳，保留公开错误语义 |
| `extractParamsFromUrl`（35） | `internal/platform/pipigx.ExtractParamsFromUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `sendPostRequest`（58） | `internal/platform/pipigx.SendPostRequest` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `processApiResponse`（89） | `internal/platform/pipigx.ProcessApiResponse` | 上游解码/事实提取归平台；可直接规范化，不必生产旧中间 JSON |

### backend/api/ppxia.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/ppxia.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `pipixiaFailure`（12） | `internal/platform/pipixia.Failure` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `pipixiaShareUrl`（17） | `internal/platform/pipixia.ShareUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `pipixiaItemId`（27） | `internal/platform/pipixia.ItemId` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `pipixiaRequest`（38） | `internal/platform/pipixia.Request` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `pipixiaRedirect`（61） | `internal/platform/pipixia.Redirect` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `pipixiaTransportFailure`（69） | `internal/platform/pipixia.TransportFailure` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `pipixiaPageItem`（86） | `internal/platform/pipixia.PageItem` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `pipixiaUrls`（98） | `internal/platform/pipixia.Urls` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `pipixiaItemResponse`（111） | `internal/platform/pipixia.ItemResponse` | 上游解码/事实提取归平台；可直接规范化，不必生产旧中间 JSON |
| `pipixia`（167） | `internal/platform/pipixia.Parse` | 以 context/input 返回结构化结果，不修改进程全局状态 |

### backend/api/sv.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/sv.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| 脚本顶层入口 | `internal/api.Handler` | 现有 gateway 转接壳合并，不另公开 legacy 路由 |

### backend/api/toutiao.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/toutiao.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `toutiaoAllowedUrl`（12） | `internal/platform/toutiao.AllowedUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `toutiaoVideoId`（22） | `internal/platform/toutiao.VideoId` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `toutiaoRedirectUrl`（40） | `internal/platform/toutiao.RedirectUrl` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `toutiaoRequest`（51） | `internal/platform/toutiao.Request` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `toutiao`（83） | `internal/platform/toutiao.Parse` | 以 context/input 返回结构化结果，不修改进程全局状态 |
| `toutiaoMediaUrl`（142） | `internal/platform/toutiao.MediaUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `toutiaoCandidates`（152） | `internal/platform/toutiao.Candidates` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `toutiaoResponseFromPage`（193） | `internal/platform/toutiao.ResponseFromPage` | 上游解码/事实提取归平台；可直接规范化，不必生产旧中间 JSON |

### backend/api/weibo.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/weibo.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `main`（14） | `internal/platform/weibo 返回值 + internal/api.WriteJSON` | 退役脚本 echo/header/exit 壳，保留公开错误语义 |
| `weiboTarget`（23） | `internal/platform/weibo.Target` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `extractVideoId`（47） | `internal/platform/weibo.ExtractVideoId` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `getRequestHeaders`（53） | `internal/platform/weibo.GetRequestHeaders` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `fetchVideoInfo`（59） | `internal/platform/weibo.FetchVideoInfo` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `weiboFetchTarget`（64） | `internal/platform/weibo.FetchTarget` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `weiboResolve`（85） | `internal/platform/weibo.Resolve` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `weiboTransportFailure`（107） | `internal/platform/weibo.TransportFailure` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `weiboIsVisitor`（116） | `internal/platform/weibo.IsVisitor` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `weiboJsonp`（123） | `internal/platform/weibo.Jsonp` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `weiboStartGuest`（132） | `internal/platform/weibo.StartGuest` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `weiboJsonRequest`（152） | `internal/platform/weibo.JsonRequest` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `weiboCount`（168） | `internal/platform/weibo.Count` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `weiboCandidate`（174） | `internal/platform/weibo.Candidate` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `weiboCandidates`（195） | `internal/platform/weibo.Candidates` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `formatWeiboStatusResponse`（225） | `internal/platform/weibo.FormatWeiboStatusResponse` | 上游解码/事实提取归平台；可直接规范化，不必生产旧中间 JSON |
| `formatWeiboResponse`（233） | `internal/platform/weibo.FormatWeiboResponse` | 上游解码/事实提取归平台；可直接规范化，不必生产旧中间 JSON |
| `weiboVideoResult`（241） | `internal/platform/weibo.VideoResult` | 上游解码/事实提取归平台；可直接规范化，不必生产旧中间 JSON |
| `outputSuccess`（281） | `internal/platform/weibo 返回值 + internal/api.WriteJSON` | 退役脚本 echo/header/exit 壳，保留公开错误语义 |
| `outputError`（287） | `internal/platform/weibo 返回值 + internal/api.WriteJSON` | 退役脚本 echo/header/exit 壳，保留公开错误语义 |

### backend/api/xiaohongshu/XiaohongshuMediaProbe.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/xiaohongshu/XiaohongshuMediaProbe.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `__construct`（13） | `internal/platform/xiaohongshu.NewOriginInspector` | 在共用 probe 上记录 HTTP/字节/容器，不共享可变观察 |
| `allowedUrl`（29） | `internal/platform/xiaohongshu.ProbePolicy` | 四 CDN、受限 key 路径、QuickTime |
| `mediaTypes`（42） | `internal/platform/xiaohongshu.ProbePolicy` | 四 CDN、受限 key 路径、QuickTime |
| `inspect`（47） | `internal/platform/xiaohongshu.InspectOrigin` | MOV 容器与状态/已读取字节 |

### backend/api/xiaohongshu/XiaohongshuParser.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/xiaohongshu/XiaohongshuParser.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `__construct`（20） | `internal/platform/xiaohongshu.New` | 注入请求执行器、时钟/探测，保持每请求状态隔离 |
| `output`（26） | `internal/platform/xiaohongshu 返回值 + internal/api.WriteJSON` | 退役脚本 echo/header/exit 壳，保留公开错误语义 |
| `setCookie`（31） | `internal/platform/xiaohongshu.SetCookie` | 仅内部受控配置注入，默认空；不新增公开 Cookie 参数 |
| `pageAllowed`（33） | `internal/platform/xiaohongshu.PageAllowed` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `request`（44） | `internal/platform/xiaohongshu.Request` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `extractId`（79） | `internal/platform/xiaohongshu.ExtractId` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `desktopUrl`（85） | `internal/platform/xiaohongshu.DesktopUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `parse`（90） | `internal/platform/xiaohongshu.Parse` | 以 context/input 返回结构化结果，不修改进程全局状态 |
| `extractNote`（115） | `internal/platform/xiaohongshu.ExtractNote` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `streamCandidate`（128） | `internal/platform/xiaohongshu.StreamCandidate` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `videoCandidates`（148） | `internal/platform/xiaohongshu.VideoCandidates` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `formatNoteData`（201） | `internal/platform/xiaohongshu.FormatNoteData` | 上游解码/事实提取归平台；可直接规范化，不必生产旧中间 JSON |
| `processImageUrl`（232） | `internal/platform/xiaohongshu.ProcessImageUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |

### backend/api/xiaohongshu/xhsjx.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/xiaohongshu/xhsjx.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| 脚本顶层入口 | `internal/catalog → platform/xiaohongshu.Parse` | 脚本入口退役，页面/probe 依赖注入 |

### backend/api/zuiyou.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/api/zuiyou.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `outputJson`（21） | `internal/platform/zuiyou 返回值 + internal/api.WriteJSON` | 退役脚本 echo/header/exit 壳，保留公开错误语义 |
| `getParamFromUrl`（36） | `internal/platform/zuiyou.GetParamFromUrl` | 迁移现有平台行为；详细输入/请求/规范化边界见附录 B |
| `getRedirectUrl`（52） | `internal/platform/zuiyou.GetRedirectUrl` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `getParamFromUrlWithRedirect`（97） | `internal/platform/zuiyou.GetParamFromUrlWithRedirect` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `curlRequest`（124） | `internal/platform/zuiyou.CurlRequest` | 平台请求编排/策略保留；执行统一走 internal/fetch |
| `zuiyouResponse`（164） | `internal/platform/zuiyou.ZuiyouResponse` | 上游解码/事实提取归平台；可直接规范化，不必生产旧中间 JSON |
| `parseZuiyou`（209） | `internal/platform/zuiyou.Parse` | 以 context/input 返回结构化结果，不修改进程全局状态 |

### backend/bin/catalog.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/bin/catalog.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| 脚本顶层入口 | `cmd/dokitool catalog → internal/catalog.PublicCatalog` | 需要导出时使用 Go CLI；前端已有生成器直接读 JSON |

### backend/internal/index.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/internal/index.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| 脚本顶层入口 | `退役；internal/catalog.Parse` | 不保留 PHP 内部 HTTP 自请求；由进程内调用替代 |

### backend/lib/douyin-media-probe.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/douyin-media-probe.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `allowedUrl`（9） | `internal/platform/douyin.ProbePolicy` | 受限 aweme path、douyinvod/zjcdn 域 |

### backend/lib/gateway.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/gateway.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `snapPublicRoutes`（9） | `internal/api.Routes` | 保留公开 JSON、媒体与关闭路径；不暴露 PHP 路径 |
| `snapError`（14） | `internal/api.MapHTTPError` | 保留 HTTP/业务枚举与 retryable 映射 |
| `snapEnvelopeStatus`（24） | `internal/api.StatusForEnvelope` | 明确 HTTP 状态；不能所有业务失败返回 200 |
| `snapPreparePublicMedia`（35） | `internal/api.PreparePublicMedia` | 完整校验 → 提供方公开来源处理 → 统一视图 → 重算与校验 |
| `snapMapDeliverySources`（73） | `internal/unified.MapDeliverySources` | 递归 simple/init/pieces/resources，保留 ID |
| `snapSend`（82） | `internal/api.WriteJSON` | 统一响应头、状态、错误与 UTF-8 编码 |
| `snapPlatformForUrl`（93） | `internal/catalog.MatchURL` | 平台目录域/点分子域与入口 URL 限制 |
| `snapInputUrl`（117） | `internal/api.ParseInputURL` | 分享文本、长度、尾标点，保留原 query |
| `snapInternalSettings`（129） | `退役；cmd/dokitool 配置与监听边界承接` | Go 不需要 PHP loopback 自调用；保持服务不直接公开内部执行入口 |
| `snapInternalRequestAllowed`（145） | `退役；cmd/dokitool 配置与监听边界承接` | Go 不需要 PHP loopback 自调用；保持服务不直接公开内部执行入口 |
| `snapDecodeResponse`（153） | `internal/api.ValidateAdapterResult` | 退役内部 HTTP JSON 解码壳；保留统一结果/状态一致性验证 |
| `snapForward`（165） | `internal/catalog.Parse` | 改进程内注册调用，不复制内部 HTTP |
| `snapRunParser`（204） | `internal/api 调用 Adapter.Parse` | 退役 include/output buffer/全局变量/exit；异常转明确错误 |
| `snapHandle`（249） | `internal/api.Handler` | 路由、方法、schema_version、debug、resolve 目标校验 |

### backend/lib/media-adapters.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-adapters.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `snapAdaptParser`（9） | `internal/catalog.Parse + internal/unified.FinalizeData` | 平台私有原始壳可退出；公开错误和结果行为保持 |
| `snapText`（26） | `internal/unified.Text` | 空值与字符串/整数身份语义 |
| `snapSource`（31） | `internal/unified.SourceFromCandidate` | URL/大小/有效期/可用性与访问方式 |
| `snapSourceIdentity`（46） | `平台 identity helper + internal/unified.SourceIdentity` | 仅剔除已确认签名部分，不泛化修改媒体 URL |
| `snapSources`（70） | `internal/unified.BuildSources` | 保留来源顺序和稳定哈希 |
| `snapMergeSources`（85） | `internal/unified.MergeSources` | 同来源凭据整体择优，不拼接不同 URL 与有效期 |
| `snapResourceIdentity`（99） | `internal/unified.ResourceIdentity` | role/kind/来源 ID 的既有哈希输入 |
| `snapCandidateMetadata`（106） | `internal/unified.MetadataFromCandidate` | 单位、证据、总码率与视频轨码率区别 |
| `snapMergeMetadata`（132） | `internal/unified.MergeMetadata` | 后补事实、冲突集合、证据与反序稳定 |
| `snapDeferAudioContradiction`（170） | `internal/unified.DeferAudioContradiction` | 明确跨字段矛盾只隔离对应身份 |
| `snapFileVariants`（190） | `internal/unified.BuildFileVariants` | 两层身份聚合、明确音轨、所有有效组合 |
| `snapResolveUrl`（270） | `internal/unified.ResolveURL` | 自生成 API URL 的精确编码与 item_id |
| `snapWorkMetadata`（275） | `internal/unified.BuildWorkMetadata + 平台事实提取` | 保留 title/author/stats/tags/attachment；抖音零播放特例归平台 |
| `snapSimpleAdapter`（316） | `internal/unified.BuildSimpleItems` | 视频、图片、pair 与 group 的共用构造 |
| `snapAdaptDouyin`（370） | `internal/platform/douyin.Normalize` | 平台字段与特殊身份归平台；公共 default/策略仍由 unified 执行 |
| `snapAdaptKuaishou`（382） | `internal/platform/kuaishou.Normalize` | 平台字段与特殊身份归平台；公共 default/策略仍由 unified 执行 |
| `snapAdaptXiaohongshu`（383） | `internal/platform/xiaohongshu.Normalize` | 平台字段与特殊身份归平台；公共 default/策略仍由 unified 执行 |
| `snapAdaptToutiao`（389） | `internal/platform/toutiao.Normalize` | 平台字段与特殊身份归平台；公共 default/策略仍由 unified 执行 |
| `snapAdaptWeibo`（394） | `internal/platform/weibo.Normalize` | 平台字段与特殊身份归平台；公共 default/策略仍由 unified 执行 |
| `snapAdaptPipixia`（399） | `internal/platform/pipixia.Normalize` | 平台字段与特殊身份归平台；公共 default/策略仍由 unified 执行 |
| `snapAdaptPipigx`（400） | `internal/platform/pipigx.Normalize` | 平台字段与特殊身份归平台；公共 default/策略仍由 unified 执行 |
| `snapAdaptZuiyou`（401） | `internal/platform/zuiyou.Normalize` | 平台字段与特殊身份归平台；公共 default/策略仍由 unified 执行 |
| `snapAdaptMultiVideo`（403） | `internal/unified.BuildMultiVideoItems` | 按平台给定顺序和稳定身份构造；不按 Go map 遍历 |
| `snapAdaptBilibili`（420） | `internal/platform/bilibili.Normalize` | 平台字段与特殊身份归平台；公共 default/策略仍由 unified 执行 |
| `snapBilibiliVariants`（467） | `internal/platform/bilibili.NormalizePlay` | file/segments/DASH 配对、缺音轨 deferred |
| `snapBilibiliTrack`（509） | `internal/platform/bilibili.NormalizeTrack` | 实际表示规格、明确语言/HDR、资源身份 |

### backend/lib/media-contract.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-contract.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `snapContractError`（6） | `internal/unified.ErrorEnvelope` | 统一 schema_version/code/error |
| `snapAssert`（13） | `internal/unified.ValidationError` | 用结构化 Go error 替代异常断言 |
| `snapRfc3339`（18） | `internal/unified.ValidateRFC3339` | 格式、有效时间和有效期比较 |
| `snapValidateStructure`（29） | `internal/unified.ValidateStructure` | 同一 Schema 的结构规则 |
| `snapSourceUsable`（88） | `internal/unified.SourceUsable` | 过期/不可用与 browser_fetch 的区别 |
| `snapDeliverySources`（94） | `internal/unified.SourceGroups` | 依赖资源 AND；同资源多来源 OR |
| `snapDeliveryUsable`（103） | `internal/unified.DeliveryUsable` | 递归资源可用性 |
| `snapDeliveryRequires`（111） | `internal/unified.DeliveryRequirements` | 能力集合与递归资源 |
| `snapVariantVideoMetadata`（122） | `internal/unified.VideoGeometry` | 视频 resource 与父级事实/冲突 |
| `snapQualityCompare`（139） | `internal/unified.CompareSpatialQuality` | 横竖尺寸和画幅可比性；不是编码排序 |
| `snapVariantTieKey`（155） | `internal/unified.VariantTieKey` | 原入口/稳定 ID 等既有平局规则 |
| `snapSelectDefault`（162） | `internal/unified.SelectDefault` | 完整 file/default/null reason/higher 投影 |
| `snapFinalizeData`（202） | `internal/unified.FinalizeData` | 请求时钟和 envelope |
| `snapValidateSource`（210） | `internal/unified.ValidateSource` | direct/relay URL、身份与有效期 |
| `snapValidateMetadata`（223） | `internal/unified.ValidateMetadata` | 字段证据、角色、单值/冲突、音轨关系 |
| `snapValidateDelivery`（259） | `internal/unified.ValidateDelivery` | sources/pair/mux/segments/hls/dash |
| `snapValidateEnvelope`（291） | `internal/unified.ValidateEnvelope` | 作用域/引用/顺序/default/higher/分组与错误 |

### backend/lib/media-metadata.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-metadata.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `snapFrameRate`（5） | `internal/unified.ParseFrameRate` | 只接受已定义技术证据，未知不推断；保留原字段优先次序 |
| `snapHdrType`（14） | `internal/unified.NormalizeHDRType` | 只接受已定义技术证据，未知不推断；保留原字段优先次序 |
| `snapVideoFacts`（24） | `internal/unified.ReadVideoFacts` | 只接受已定义技术证据，未知不推断；保留原字段优先次序 |
| `snapAudioFacts`（35） | `internal/unified.ReadAudioFacts` | 只接受已定义技术证据，未知不推断；保留原字段优先次序 |

### backend/lib/media-policy.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-policy.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `snapSameFrameRate`（5） | `internal/unified.SameFrameRate` | 1% 容差 |
| `snapPolicyMetadata`（11） | `internal/unified.EffectiveMetadata` | 父/资源同字段冲突 |
| `snapPolicyFactValues`（25） | `internal/unified.FactValues` | 确认的单值与冲突集合 |
| `snapPolicyAudioMetadata`（33） | `internal/unified.AssociatedAudioFacts` | 共用关联读取、多音轨保守 |
| `snapFilterItemVariants`（54） | `internal/unified.FilterCoveredMux` | 一个有效完整文件覆盖尺寸与四项能力 |
| `snapViewUrl`（106） | `internal/unified.WithDebugView` | 只改自生成 API 链接 |
| `snapPublicMediaView`（114） | `internal/unified.PublicView` | 公开来源处理后筛选，再重算默认与引用 |

### backend/lib/media-probe.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/media-probe.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `__construct`（22） | `internal/probe.New` | 注入 transport/clock；计数不是共享全局 |
| `allowedUrl`（27） | `internal/probe.Policy` | 由抖音/小红书提供策略，无需继承 PHP 类 |
| `mediaTypes`（29） | `internal/probe.Policy` | 由抖音/小红书提供策略，无需继承 PHP 类 |
| `resolve`（35） | `internal/fetch.ResolvePublic` | 全 DNS 答案与地址绑定；保持 Host/SNI |
| `request`（56） | `internal/fetch.ReadRange` | 请求 context、禁代理/跳转、严格头和有限正文 |
| `read`（102） | `internal/probe.ReadRange` | 逐跳/Range/长度/预算与相同文件大小 |
| `probe`（143） | `internal/probe.Inspect` | 首块、跳过 mdat、定位 moov、失败保守 |
| `u32`（171） | `internal/probe ISO BMFF 读取 helpers` | 字节序、整数上限、嵌套/重复/截断边界 |
| `u64`（176） | `internal/probe ISO BMFF 读取 helpers` | 字节序、整数上限、嵌套/重复/截断边界 |
| `atom`（182） | `internal/probe ISO BMFF 读取 helpers` | 字节序、整数上限、嵌套/重复/截断边界 |
| `children`（194） | `internal/probe ISO BMFF 读取 helpers` | 字节序、整数上限、嵌套/重复/截断边界 |
| `child`（206） | `internal/probe ISO BMFF 读取 helpers` | 字节序、整数上限、嵌套/重复/截断边界 |
| `metadata`（219） | `internal/probe.ReadVideoMetadata` | 真实视频轨尺寸、编码、时长、视频码率 |

### backend/lib/platforms.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/platforms.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `snapPlatforms`（5） | `internal/catalog.Registry` | 唯一 JSON 与 Go adapter ID 对齐，缺失配置启动失败 |
| `snapPlatformCatalog`（22） | `internal/catalog.PublicCatalog` | 公开字段、目录顺序和无内部路径 |

### backend/lib/runtime.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/runtime.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `snapResponseWriter`（7） | `internal/fetch.ReadBounded` | 读取前限制容量；页面与媒体使用不同策略 |
| `snapCurlExec`（18） | `internal/fetch.Do` | 标准实现＋可替换执行器；上下文与策略不绑定 cURL |
| `snapMediaUrl`（34） | `internal/unified.ValidMediaURL` | 返回媒体地址的结构检查，不等于服务器出站许可 |

### backend/lib/weibo-media.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/lib/weibo-media.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| `snapWeiboMediaKey`（8） | `internal/media/weibo.SigningKey` | 私密配置与长度 |
| `snapWeiboMediaAllowedUrl`（14） | `internal/media/weibo.AllowedURL` | 固定 HTTPS 微博媒体域与路径约束 |
| `snapWeiboMediaBase64`（26） | `internal/media/weibo.Base64URL` | 无填充 URL-safe 编码 |
| `snapWeiboMediaUrl`（32） | `internal/media/weibo.SignTicket` | v1 payload/HMAC/TTL/上游到期 |
| `snapWeiboMediaVerifyTicket`（45） | `internal/media/weibo.VerifyTicket` | 双向兼容、恒定时间签名比较和时钟 |
| `snapWeiboMediaRange`（64） | `internal/media/weibo.ParseRange` | 单段 Range 语义与边界 |
| `snapWeiboMediaInCidr`（86） | `internal/fetch.IsAllowedPublicIP` | 保留当前 IPv4/IPv6 禁止范围，勿只检查 RFC1918 |
| `snapWeiboMediaPublicIp`（95） | `internal/fetch.IsAllowedPublicIP` | 保留当前 IPv4/IPv6 禁止范围，勿只检查 RFC1918 |
| `snapWeiboMediaResolve`（116） | `internal/fetch.ResolvePublic + weibo.Policy` | 固定提供方、全 DNS 答案验证和 IP pin |
| `snapWeiboMediaPrepare`（138） | `internal/media/weibo.PrepareRequest` | GET/HEAD/ticket/配置/Range 检查 |
| `snapWeiboMediaResponsePlan`（158） | `internal/media/weibo.ResponsePlan` | 200/206/416/关键头与长度验证 |
| `snapWeiboMediaCurl`（198） | `internal/media/weibo.FetchStream` | 受限传输、固定 Referer/identity、取消 |
| `snapWeiboMediaStream`（232） | `internal/media/weibo.Stream` | HEAD 小 Range、媒体前校验、有限缓冲 |
| `snapWeiboMediaError`（284） | `internal/media/weibo.WriteError` | 保留现有媒体 HTTP 错误，不套解析成功 Schema |
| `snapHandleWeiboMedia`（294） | `internal/media/weibo.Handler` | 不得在已发媒体后追加 JSON |

### backend/public/index.php

[固定基线源码](https://github.com/Brilliant666/DokiTool/blob/665bd55134982b9fe107aa1cd00373ba8da8ff3c/backend/public/index.php)

| PHP 符号（定义行） | 建议 Go 符号/职责 | 处理要求 |
| --- | --- | --- |
| 脚本顶层入口 | `cmd/dokitool + internal/api.Handler` | 公共服务启动与恢复错误处理 |

## 附录 B：九平台详细请求链路

以下为固定源码的请求行为说明，未在本阶段访问真实平台。各段中的路径/行号以附录 A 的固定基线链接为准。

### 抖音

- 公共输入域：`douyin.com`、`iesdouyin.com`（含点分子域，目录+gateway）。解析器 `parse` 在短链/无可识别ID时走 `get_headers` GET 优先、cURL HEAD跟随兜底，提取常见query字段或路径作品ID。ID限定16–20位且不能0开头；字符串处理，不能经float64。
- 详情：GET `https://api.amemv.com/aweme/v1/aweme/detail/`，`aweme_id/aid=6383/device_platform=web/version_code=190500/version_name=19.5.0`；Android Chrome123 UA与JSON Accept，20秒，gzip/deflate，无Cookie/签名。必须校验status_code和返回aweme_id，空detail不能擅自断言作品已删除。`DouyinParser.php:46–199`。
- 单视频：`bitRateList`或`bit_rate` → 每表示地址/尺寸/编码/音轨事实 → 保留所有明确配对及不同观察；main仅为旧格式中间结果，最终 default 由统一层选择。DASH看上游format/is_dash与audio_file_id，不靠URL拼写。`538–749`。
- 原始入口：`https://aweme.snssdk.com/aweme/v1/play/?video_id=...&ratio=default&line=0`，共享有界ISO BMFF探测读取真实尺寸/编码/视频码率；成功更高规格可替代中间主源，失败保留已知主源与unknown备选，不能把“原始入口”本身当4K。入口/来源身份不受跳转签名续期影响。`317–379,828–848`。
- 图集/Live：保留图片顺序及图片-motion配对、独立背景音乐；每批最多30个唯一vid尝试原始入口HEAD跳转，失败回退原地址，不能凭另一图的motion补齐。`271–316,421–506,906–916`。
- 视频URL既有处理：playwm→play、HTTP→HTTPS、v26-web播放主机改v26-luna.douyinvod.com、补mime_type=video_mp4（不重编码原query、fragment前追加）；不要误用到图片/背景音频。`980–982,1048–1094`。
- 易漏：原始入口probe读到的bit_rate是视频轨码率；普通上游bit_rate可能是总码率，适配层依据来源映射不同字段。has_audio缺失≠无音轨；同URL不同音频配对不得合并；FPS/HDR后续观察要完整进入聚合。`media-adapters.php:106–126,370–380`。

### 小红书

- 公共目录接受 `xiaohongshu.com/xhslink.com/rednote.com`；解析器内部另外有`xhs.com`别名，但目前gateway不会接受它，迁移不应无意扩大公开输入范围。
- 页面链：需要时解析短链 → 保留原path/query字节仅换authority → 首先`www.rednote.com`，再`www.xiaohongshu.com`。页面请求手动重定向最多5跳、10秒、Chrome131 UA/Accept/Accept-Language；Cookie默认空，opt-in，不采集响应Cookie或维护自动Cookie jar。`XiaohongshuParser.php:31–113`。
- SSR：只提取`window.__INITIAL_STATE__`，字符串外有限转换`undefined/new Set([])/new Map([])`后JSON解析，不执行网页JS；noteDetailMap必须匹配请求ID。`115–124`。
- 候选：`video.media.stream`各编码、mediaV2.opaque1两类screencast（可能多层JSON字符串）、originVideoKey。尺寸只能来自各stream或实际探测，不能借上传source_dimension。相同stream合并备源，保留多编码。`128–198`。
- origin：key字符/路径校验后构造`https://sns-video-{qn,al,qc,bd}.xhscdn.com/<key>`；共享10秒循环预算，首次测量成功后停止其余探测但保留全部候选；明确403/404/410标当前source unavailable。可能为MOV/QuickTime，不可把内部metadata_source=mp4误当容器MP4。`164–186`及`XiaohongshuMediaProbe.php:13–57`。
- 图片/Live：视频note的imageList只是cover，不能变相多生成图集；图文按原顺序保留图片和Live motion variants；保留昵称/简介/统计含已知0。图片去水印URL替换沿用既有各路径规则。`201–277`。
- 研究事实：历史无Cookie rednote+有效token有4K证据；该次PHP页面未取得SSR。真实SSR回放加PHP有界探测读到3840×2160/H264/MOV/3,425,556,989B，累计只读672,088B，没有完整文件保存或端到端成功。研究文档`7–30,63–107`。

### B站

- 输入`bilibili.com`系列与`b23.tv`。短链HEAD跟随，显式原链接p优先；p只接受1起、最多6位数字，不能让数组/错误值回退P1。BV可从路径取，路径缺时用HTML meta兜底。总deadline35秒。`bilibili.php:12–73`。
- 详情GET `https://api.bilibili.com/x/web-interface/view?bvid=...`，只选定一个cid调用`/x/player/playurl`，参数`otype=json/fnver=0/fnval=3/player=3/qn=112/bvid/cid/platform=html5/high_quality=1`。普通服务器请求不加个人Cookie/代理，不能根据请求qn认定实际画质。`47–59`。
- 每P生成稳定`<BV>-pN`，只当前P解析；其他P与合集条目deferred+resolve_url。某P失败不能借其他P完成，合集group.complete=false。`75–130; media-adapters.php:420–464`。
- 一个durl完整file，多个durl是segments/remux；DASH资源是每video与每合法audio备选的独立配对，缺音频deferred；仅结构识别，当前不执行合流。元数据来自play/track，不借view上传尺寸。`media-adapters.php:467–529`。
- 错误保持层次：401/403/412/429及上游部分负码→受限，网络/无效JSON→上游异常，明确删除/不可见→404，当前P无源→明确失败。`bilibili.php:232–280`。

### 快手

- 公共目录只`kuaishou.com`系列；先HEAD自动跟随取最终页，再GET该页；从`short-video/long-video/photo`路径提ID。`KuaishouSpider.php:27–50,282–352`。
- 当前入口传空Cookie（`ksjx.php:25–29`），尽管旧注释写“务必填cookie”；代码支持注入Cookie但不能据此宣称Cookie是现有运行必需项。
- 数据路线：`window.INIT_STATE`优先，`tusjoh*`有效photo对象；JSON损坏有有限stripslashes/两种错误字符串修整。先图集，再单图，再video；其次`window.__APOLLO_STATE__`页面分支，定位`VisionVideoDetailPhoto:<id>`并提作者。`56–246`。
- 视频多表示：mainMvUrls + manifest.adaptationSet.representation；每表示保存URL/backupUrl、尺寸与已知codec，不能把avgBitrate/maxBitrate猜成bps。APOLLO采用manifestH265.json。`249–268`。
- 图集媒体地址构造`http://tx2.a.yximgs.com/<path>`、背景音`http://txmov2.a.kwimgs.com...`；这些是返回浏览器的URL，不意味着必须由Go后端代取。平台名判断域与出站媒体域不能混为一谈。
- 当前支持video/image，不承诺Live或所有作品都可无Cookie访问；没有本轮在线样本佐证，迁移线上验收需分别覆盖INIT与APOLLO路径，不能只测通用适配器fixture。
### 微博

入口识别 `weiboTarget` 接受微博 TV 的 fid、桌面正文链接与 `m.weibo.cn/status|detail`；提取身份后请求固定官方接口，不直接将输入页面当成 API（`api/weibo.php:23–44`）。

- 正文：`https://weibo.com/ajax/statuses/show?id=...`。
- TV：POST `https://weibo.com/tv/api/component?page=...`，表单 `data=...Component_Play_Playinfo...`、对应 TV Referer；成功后若有 mid，再 GET statuses/show 补充实际画质/作者/统计。
- 若响应是访客页面：POST `https://passport.weibo.com/visitor/genvisitor` → GET `.../visitor/visitor` → 重试原接口。整次解析最多启动一次匿名访客初始化，component/status 两端共享此限制；JSONP 仅作 JSON 解析，不执行 JavaScript（`85–165`）。
- 单次解析新建 cURL handle 和内存 Cookie jar；35 秒总预算，每次不超过 20 秒，单响应最多 2 MiB；结束即释放，不落 Cookie 文件（`64–81`）。UA Chrome131，固定 Accept，Referer 按 endpoint 提供。当前自动跟随跳转，runtime 限制最多 5 次，但并非完整出站白名单实现。
- 画质优先取正文 playback_list 中 MIME 为 video 的元数据，component urls 补充，必要时用旧字段；避免把 scrubber JPEG 当 8K 视频。保留尺寸、码率、codec、size、fps；仅 label 为1080P时不会捏造宽高；1080P优先于高码率720P；正文补充失败不能抹掉已经可播放的 component。统计的已知零值、秒级小数时长、作者ID字符串与 Asia/Shanghai 发布时间要保留（`174–278`）。
- 网络/访问限制、非法响应和确实无视频区别处理（503/502/404），再交公共 gateway 映射；这不是统一输出一律200的薄壳。

### 今日头条

- 输入允许 toutiao.com 与子域，指定路径/video、/group、/i 或 group_id/item_id 抽取字符串ID；不误用 wid、host中的数字，拒绝数组查询、用户信息、非默认端口（`api/toutiao.php:12–47`）。
- 无ID时最多3次显式重定向，每跳保持平台域；随后 POST `https://ttwid.bytedance.com/ttwid/union/register/` 匿名初始化；读 Set-Cookie ttwid，再 GET `https://www.toutiao.com/video/<id>/`（`83–139`）。
- 共享35秒单调时钟预算，单请求最多20秒；注册响应64 KiB，页面2 MiB；匿名ttwid仅本解析内存有效且不出现在业务输出。Chrome123 UA。注册JSON含 aid24、needFid false、region cn、service www.toutiao.com、union true（`50–99,120–126`）。
- 页面 `script id=RENDER_DATA` 内原始或百分号编码JSON；使用 rawurldecode 语义保留 `+`；大ID不经浮点。严格匹配作品ID（`193–210`）。
- main/backup/base64媒体URL以及video_meta保留；按真实像素/码率/size排序；同表示主备以排序后URL集合哈希关联；未知保持未知。作者简介必须最终落 `author.description`，统计含零，时长是秒。验证码/限流、404、格式错误分开（`142–244`、`media-adapters.php:389–392`）。

### 皮皮虾

- pipix.com及子域输入，旧 `/item/<id>`、新 `/ppx/item/<id>` 或 item_id；无ID则最多5跳显式同平台重定向；最终只请求 `https://h5.pipix.com/ppx/item/<id>`（`api/ppxia.php:17–67,167–190`）。
- 20秒总体截止，页面2 MiB，iPhone/Safari UA，Referer h5.pipix.com；无会话Cookie流程。当前循环的余时行为应以已有用例为基线，Go建议Context总截止，不能每步重新分配20秒。
- script内原始/百分号编码的惰性JSON `ppxItemDetail.item`；不执行JS，不从评论区借用其他作品素材；优先精确 `item_id_str`，防止超大数四舍五入导致串作品（`85–117`）。
- 取 origin_video_download 与 video_download/high/mid/low/fallback 的 URL+download列表，保留不同尺寸及CDN；保留图集note.multi_image、avatar、时长及零统计；内容为空/不可用/ID错配/上游受限/超时均有明确机器错误原因（`111–164`）。

### 最右

- `pid`查询或 `/detail/<id>`；没有ID时对分享页做一次HEAD读取Location再抽pid；固定POST `https://share.xiaochuankeji.cn/planck/share/post/detail_h5`，JSON `{pid:<整数>,h_av:"5.2.13.011"}`（`api/zuiyou.php:36–115,209–223`）。
- HEAD分享解析12秒，普通metadata请求20秒，Chrome91 UA，IPv4优先，低速限制；runtime会补充共同大小/连接/重定向限制。API目前允许自动跳转，没有Cookie会话。
- **imgs是顺序来源，videos只是ID查表**。按imgs顺序排视频并补余项，在每项保存原map键 `private_video_id`；后续item ID由该键哈希，前项缺失不会使后项换身份；视频不存在不能被空成功覆盖（`164–206`；`media-adapters.php:403–417`）。
- 图片原图优先origin，再large，再首个可用；视频主源是排序后第一个有效视频。Go不能直接遍历无序map生成items，也不能只排序map键代替上游imgs顺序。

### 皮皮搞笑

- 分享URL只抽pid/mid，须为合法范围的十进制数，保留内层查询，拒绝数组参数；固定POST `https://h5.pipigx.com/ppapi/share/fetch_content`，JSON整数pid/mid与type=post（`api/pipigx.php:35–81,128–163`）。
- 单次20秒，JSON Content-Type，公共runtime默认8MiB响应限额，无匿名Cookie步骤。
- `data.post.videos` 转序列；第一个视频作旧主源，但 `private_videos` 保留供统一层生成多item；封面来自 `https://file.ippzone.com/img/frame/id/<thumb>`（返回URL，并非本文件发起请求）。非法JSON、缺data.post、无视频不能变空成功（`89–123`）。
- 这里当前没有最右那种上游map键身份字段：统一层在无private_video_id时用序号。迁移先保持已批准行为，不因“Go map无序”意外进一步漂移；新增更稳身份属于须明确评审的行为改变。
