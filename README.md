# DokiTool-Go

本仓库是 `Brilliant666/DokiTool-Go` 的独立 Go 迁移工作区，按维护者决定在开发阶段公开协作。原 [DokiTool](https://github.com/Brilliant666/DokiTool) 的 main 继续维护 PHP；Go 提交和 Draft PR 进入本仓库，不整体反向合入 PHP 仓库。抖音与 [B 站迁移](docs/go-migration/bilibili.md) 已通过离线总门禁；公共合同的 fragment 与安全整数问题由 [PR #2](https://github.com/Brilliant666/DokiTool-Go/pull/2) 修复。尚有七个平台和真实在线验收，Go 不能替代九平台生产入口，没有部署或切流。

当前计划、版本化 PHP 对拍与证据见 [Go 迁移说明](docs/go-migration/README.md)，仓库、选择性同步及许可范围见 [维护边界](docs/go-migration/MAINTENANCE.md)。下方保留的功能与部署说明描述继承的 PHP/前端基线，不能据此认定 Go 已迁移全部平台。

DokiTool 是在同一仓库维护 Vue 3 前端与自建 PHP 解析接口的短视频工具，支持视频、图片、实况照片、画质与备用源选择，以及 B 站分 P 的展示、播放和下载。普通媒体优先由用户浏览器直接从媒体 CDN 获取。

本项目在两个指定上游快照上独立维护，采用选择性适配，并非 GitHub Fork 或上游仓库的完整镜像：

| 组件 | 上游 | 采用版本 |
| --- | --- | --- |
| PHP 解析器 | [jiuhunwl/short_videos](https://github.com/jiuhunwl/short_videos) | `72e5b6e72ab21e695d510e1c4c45213ce9a25f0f` |
| Vue 前端 | [jiuhunwl/shortvideo-html](https://github.com/jiuhunwl/shortvideo-html) | `cd16011960fe90b53dd87f57714d1a096e986e5a`，包版本 `4.1.0` |

原作者的许可证与版权声明保留在 [backend/LICENSE](backend/LICENSE)、[frontend/LICENSE](frontend/LICENSE) 和源码注释中；公开检查点 `1eb529f` 及继承的 DokiTool 适配代码继续采用 [MIT](LICENSE)。该检查点之后自有私有增量的范围见 [维护边界](docs/go-migration/MAINTENANCE.md#来源和许可边界)，不改变已公开或第三方部分的许可。依赖许可见 [NOTICE](NOTICE) 及 [frontend/public/licenses](frontend/public/licenses)。详细来源记录在 [UPSTREAM.json](UPSTREAM.json)，人工引入更新的方法见 [上游维护说明](docs/UPSTREAM-MAINTENANCE.md)。

[上游每日更新检查](docs/UPSTREAM-WATCH.md) 由 GitHub Actions 在北京时间 09:00 运行，新变化通过仓库 Issue 通知；不自动合并或部署。

## 当前目标

先完成已接入平台的实际使用，再扩展能力。当前范围是抖音、快手、B 站、小红书、今日头条、微博、皮皮虾、皮皮搞笑、最右这九个平台；前端入口与后端注册表保持一致。

一次完整使用包括：粘贴有效链接、识别平台、解析并展示对应媒体、切换或勾选内容、播放与下载，以及失败后能重试。接口返回成功但图片丢失、播放错分 P、下载空文件或页面仍显示旧结果，都属于本阶段要修复的缺陷。平台解析、界面交互与真实媒体下载分别记录验收结果，不能用固定样例通过代替真实平台可用。

相关缺陷按批修复，集中回归并提交一个 PR；本阶段不新增平台，也不以更换为作者公共 API 代替自建后端修复。

## 统一媒体接口

九平台统一使用 `/api/parse`、`/api/resolve` 和 `/api/platforms`，请求携带 `schema_version=1.0`。返回结构为 `items → variants → delivery`：每个条目的完整文件默认项由后端计算，前端校验后直接读取；用户选中的版本独立保留。旧公共字段与平台专属前端归一化已移除，前后端须成套切换。

单文件使用 `delivery.sources`，实况配对和音视频组合才展开 `resources`。后端保留完整的内部规范化候选；普通响应保留全部完整文件，并仅在同一个有效完整文件覆盖空间尺寸及受保护能力时，省略可替代的分离音视频组合。高帧率、HDR、已证明的备选语言和多声道是第一版保护范围，未知规格不会伪造能力。`parse/resolve` 携带 `debug=1` 可取得过滤前的规范化全量视图，仍遵守相同 Schema 和来源处理规则，不返回平台原始 JSON。

画质主列表展示当前具备下载能力的版本，保留下来的未支持版本默认收在“其他版本”。更高空间分辨率的处理候选由后端引用说明，前端不重新挑默认。DASH、音视频合流和需 remux 的分段目前明确提示暂不支持，视频裸轨不会被当成完整视频下载。待解析的原画 variant 可声明 `state:deferred`，不会伪造地址/规格或覆盖现有完整文件。

平台注册唯一来源是 `backend/config/platforms.json`，前端构建自动生成目录快照，运行时也读取公开目录。新增现有媒体能力的平台只需后端适配与注册，图标可以使用通用样式。详细说明见 [API 合同](docs/API.md)、[schema 与验证](docs/schema/unified-v1/README.md) 和 [成套切换要求](docs/DEPLOYMENT.md#统一媒体-v1-切换)。

小红书已保留 stream、原画候选及真实媒体探测能力，但当前 PHP 页面请求仍无法取得对照样本的原始作品数据。本 PR 不加入 Python/curl_cffi 运行依赖；已探测到的真实 4K 是隔离页面抓取后交给 PHP 探测的结果，不等于目标后端端到端或浏览器完整保存通过。脱敏证据与后续 Go 迁移验收条件见 [研究记录](docs/research/xhs-4k-transport-comparison-2026-10-30.md)。

## 下载与任务管理

紫色“下载视频”按钮按以下顺序选择完整文件的交付方式：

| 方式 | 采用条件 | 数据路径与体验 |
| --- | --- | --- |
| 原生直链 `browser_native` | 能读取到符合要求的附件响应头、媒体类型及文件名 | CDN → 浏览器下载管理器。文件名由来源决定；页面只确认已交接，不能跟踪其进度或保存结果。 |
| SW 流式 `browser_streaming` | 未满足原生附件条件，且浏览器支持 Service Worker、媒体允许跨域读取 | CDN → 浏览器 Worker → 下载管理器。按作品命名，边收边交付，显示进度，不在页面积攒整个文件。 |
| Blob `browser_blob` | 尚未启动 SW 任务，已确认浏览器缺少所需流式能力 | CDN → 浏览器完整接收 → 保存。没有固定文件大小上限，每页串行处理，仍受浏览器内存能力限制。 |

普通 CDN 下载不经本站服务器中转；微博仍使用本站限定来源的签名媒体服务。SW 注册、桥接加载或协议故障，以及源站错误、任务状态未知，都不会自动改走 Blob 再下载一份。播放器菜单中的“下载”由浏览器自行处理，不经过上述选择与命名流程。ZIP 打包、分段与 HLS 合并继续使用各自流程，不属于 SW 刷新恢复范围。

SW 需要 HTTPS 或 localhost，同时传输最多两项任务；等待名额时先保留任务，获准交付后再启动正式媒体传输。同源 IndexedDB 保存轻量任务身份和已确认结果，不保存媒体字节。同一标签页刷新后查询原任务及持久化终态，不能用新 Worker 实例的记录假装接管旧网络流。

通信中断或查询不到记录时显示状态待确认，从首次失联开始最多自动恢复 60 秒；截止后提示查看浏览器下载列表，并可手动“重新查询状态”。这个窗口不限制下载时长，不自动取消、重复 START 或换路线。存储不可用不阻断实时传输，可信终态仍可显示；没有足够证据时不把未知结果写成失败。这是恢复观察与结果，不是断点续传或浏览器落盘确认。

下载期间保留必要的受控桥接页，并继续联系各任务原来的 Worker；更新等待任务结束后自然接替，不强制切换。关闭全部页面、锁屏或浏览器被系统挂起后的持续下载尚无保证。实现、部署要求和验收记录见 [下载说明](docs/DOWNLOADS.md)。

任务面板将活动及恢复窗口内的待确认任务排在前面，超过窗口的未确认记录不再计为进行中。历史默认汇总，展开后可查看全部；手动折叠会记住偏好，新任务不会擅自展开，没有活动任务后面板可自动收起。“清除已结束”可在其他任务运行时使用；未确认记录仅在原实例已证实消失且恢复窗口结束时允许清除，不清除仍可能活跃的任务，也不删除已保存文件。折叠与展开只改变界面，不停止传输。

生产下载故障与排查严重过失见 [事故 INC-20261031-001](docs/incidents/INC-20261031-001-download-delivery.md)。目前进入发布收尾：用户已报告正常生产流程三次约 540 MB 文件完整保存、首尾有声且卡片完成；历史停流未确定唯一原因，旧地址 403 不能继续作为有效测速证据。bridge 配置已修复，PR #11 的探针/恢复补丁仍待批准发布；自动终态恢复尚待发布后浏览器验收。当前结论、历史证据和停止加测要求见 [验收记录](docs/UNIFIED-MEDIA-ACCEPTANCE.md#pr11-closeout)。

## 目录

- `frontend/`：Vue 3、TypeScript、Vite 界面与契约测试。
- `backend/`：PHP 控制器、明确启用的平台解析器及离线测试。
- `ops/examples/`：脱敏的 Caddy/PHP-FPM 配置示例。
- `docs/`：[API 合同](docs/API.md)、[下载说明](docs/DOWNLOADS.md)、[部署说明](docs/DEPLOYMENT.md)和上游维护说明。
- `scripts/`：依赖许可整理与公开文件清单检查工具。

`public-files.json` 明确列出公开文件，`.gitignore` 默认忽略未列出的本地资料。新增源码时先审查并加入清单，再运行 `python scripts/check-public-files.py --write-ignore`；提交前用 `python scripts/check-public-files.py --staged` 核对暂存内容。本地备份、凭据、运行日志、生产配置和构建产物不进入仓库。

## 本地构建

准备 Node.js（Vite 5 要求 Node 18+，建议使用受支持的 LTS）、npm，以及 Python 3.10+。生产环境只需前端静态文件和 PHP-FPM，不需要常驻 Vite 开发服务器。

```sh
cd frontend
npm ci --ignore-scripts --registry=https://registry.npmjs.org
npm test
npm run build
npm run check:load-budget
cd ..
python scripts/third-party-notices.py
```

构建产物在 `frontend/dist/`，不会提交到 Git。锁文件固定依赖版本和完整性信息；部分上游锁定下载地址使用 npm 镜像，命令行 registry 不会替换所有锁定地址。

首屏使用本地 SVG 图标和带 hash 的小尺寸品牌素材；原图保存在 `frontend/brand-originals/`，可用 `npm run assets:brand` 重新生成网页 PNG。结果区在解析期间加载，JSZip 在打包操作时加载。加载预算只检查构建体积与依赖拆分；[浏览器性能验收与缓存发布要求](docs/DEPLOYMENT.md#静态资源缓存与首次加载) 另行记录。

页眉、首页输入区与下载面板采用统一的明暗主题颜色过渡；平台卡片两侧以透明遮罩淡出，避免切换主题时出现底色断层。保留现有布局、渐变和粒子动画，并尊重系统的减少动态效果设置。

默认 `VITE_API_MODE=php`、`VITE_API_BASE_URL` 为空，浏览器请求同源 `/api/*`。可参考 `frontend/.env.example`。设置 API origin 不会自动启用加密协议；仅显式 `secure` 模式使用另行实现的 `/api/v1` 协议，基础 PHP 后端不提供该协议。

## 本地检查

PHP 8.1+，启用 cURL、OpenSSL、JSON 和标准扩展；本地已验证 PHP 8.4。用已安装的 PHP 可执行文件运行：

```sh
php backend/tests/contract.php
python backend/tests/validate.py --php /path/to/php --output .local/backend-validation.json
python -m pip install -r scripts/requirements-validation.txt
python scripts/test-unified-schema.py
python scripts/validate-unified-schema.py --php /path/to/php --output .local/unified-validation.json
python scripts/check-public-files.py
cd frontend
npm run test:media-integration
cd ..
```

Windows 的 `--php` 可使用 PHP 可执行文件的完整路径。`validate.py` 只运行离线契约、语法和 CLI 场景，不启动服务，也不请求真实媒体平台。两进程 HTTP 夹具的使用方式见 [部署说明中的本地 HTTP 验证](docs/DEPLOYMENT.md#本地-http-验证)。

`npm test` 仍是独立前端测试；`npm run test:media-integration` 另验证实际 PHP 输出进入前端下载 composable 后的续签、默认快照与显式选择。后者需要 PHP：优先读取 `DOKITOOL_PHP_BINARY` 环境变量，其次使用仓库已有的本地 PHP，再尝试 PATH 中的 `php`。上游输入为合成夹具，不请求真实媒体平台；平台与续签输出的独立 Schema 验证命令见 [schema README](docs/schema/unified-v1/README.md#实现与验证)。

该集成也覆盖最右真实抽取与适配输出进入前端 resolve：前项缺失不改变存活视频身份，目标缺失时不借用相邻条目。后端回归覆盖抖音同视频 URL 的不同明确音轨配对，仍仅识别 mux 能力，不执行合流。

自动化检查涵盖前端行为、PHP 网关契约、解析器与媒体传输回归、CLI 场景和独立 HTTP 夹具。前端测试覆盖元数据、混合静图与实况、分 P、选源、ZIP 取消重试，以及 SW 排队、刷新、取消竞态、任务面板和历史清理。仓库发布与线上服务发布分别进行，本仓库不会自动部署或定时覆盖上游。

PR #7 的历史下载验收使用本地生产构建与 Edge：两条超过六分钟的受控跨域文件下载经历刷新和 Worker 更新等待后完整保存；第三项排队超过五分钟，随后以原任务 ID 完成；更新自然激活后再次下载成功。另验证了新客户端搭配旧 Worker 的小文件下载。五个实际保存文件的大小与 SHA-256 均匹配源文件，详细结果见 [下载验收记录](docs/DOWNLOADS.md#state-and-admission-consolidation-2026-10-30)。

统一合同首次候选 `36bde3f` 的刷新终态误报继续保留为历史失败，不将内存记录缺口写成它的唯一根因。最新恢复与候选精简验收基于 `f1a6778`：实际生产构建通过 Edge 不刷新、中段刷新、临近完成刷新及旧/新 Worker 自然升级对照，文件大小与 SHA-256 一致；普通/debug 网关通过真实本地 HTTP 检查。媒体和上游 HTTP 输入受控，历史抖音样本另作离线回放，不计为真实平台重新验收。小红书 4K 仍为 deferred；各轮证据、未验证范围及复跑命令见 [统一媒体验收记录](docs/UNIFIED-MEDIA-ACCEPTANCE.md)。

这些受控样例验证任务生命周期与文件完整性，不能证明真实平台当前成功率、CDN 吞吐或所有浏览器的保存行为。PR #7 的升级样例只改变构建标记；最新一轮使用 main 原始 Worker 与当前 Worker，不代表未来不兼容协议也可直接升级。界面“传输完成”表示已将数据交给浏览器，最终保存仍以浏览器下载列表和实际文件为准；iOS Safari／微信、关闭全部页面、锁屏与系统挂起仍需单独验收。

## 适配与边界

默认 API 由本站 PHP 提供；后端适配器保留平台内部抽取细节，对外只发布统一合同。服务端执行结构与语义验证，独立 Python 验证器再检查实际适配器导出的响应；这些离线合成输入的通过不代表平台实时成功率。界面图标使用本地 SVG，平台展示图标与品牌图片随构建发布；不再加载图标字体和 Swiper。

除微博外，播放和下载直接使用解析结果的媒体地址，仍受媒体站点的 CORS、防盗链、地区限制和链接时效影响。微博通过本站限定来源的签名媒体服务播放与下载，需要配置共享密钥和独立媒体池，详见 [部署说明](docs/DEPLOYMENT.md#微博播放与下载)。当前公开入口不包含开放媒体代理、需要 Cookie 的抖音主页、网易云 Python 服务或其他可选上游脚本。

部署前必须选择访问控制、使用独立 PHP-FPM 池并限制解析服务出网。现有 TLS、域名和超时检查不构成完整的重定向/DNS 隔离；请完整阅读 [部署边界](docs/DEPLOYMENT.md)。
