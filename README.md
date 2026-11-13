# DokiTool

DokiTool 是面向多平台的公开短视频、图集与实况照片无水印解析及浏览器直连保存工具，线上站点：<https://snap.dokitool.com>。

## 支持平台（15 个）

平台目录真值位于 `backend/config/platforms.json` 与 `backend/internal/catalog/catalog.go`（部分平台按服务端启动开关启用，详见 [docs/CONFIGURATION.md](docs/CONFIGURATION.md)）。前端粘贴文本时会自动提取首个 `http(s)` 链接并剥离尾随标点。

| 平台 | 支持媒体类型 | 接受的链接形式（含短链与域名归一化） | 已知限制 |
| --- | --- | --- | --- |
| 抖音（`douyin`） | 视频 / 图片 / 实况 | `douyin.com`（`www.douyin.com`、`v.douyin.com`）、`iesdouyin.com` | 不支持合集链接（`mix_id`） |
| 快手（`kuaishou`） | 视频 / 图片 | `kuaishou.com`（`www.kuaishou.com/short-video/...`、`/f/...`、`v.kuaishou.com`、`c.kuaishou.com`） | 仅公开分享作品；触发上游页面风控时返回 `UPSTREAM_BLOCKED` |
| 哔哩哔哩（`bilibili`） | 视频 | `bilibili.com`（`www.bilibili.com`、`m.bilibili.com`）、`b23.tv` | 多 P 分集按需逐集调用 `/api/resolve`；不支持番剧、付费或大会员专享稿件 |
| 小红书（`xiaohongshu`） | 视频 / 图片 / 实况 | `xiaohongshu.com`、`xhslink.com`、`rednote.com` | 网页直链须含 `xsec_token`，缺失时需从小红书 App 复制分享链接 |
| 今日头条（`toutiao`） | 视频 | `toutiao.com`（`www.toutiao.com`、`m.toutiao.com` 的 `/video/`、`/group/`、`/i<id>`） | `*.toutiaovod.com` 走浏览器原生附件下载，文件名由上游响应头决定，站内无法跟踪进度 |
| 微博（`weibo`） | 视频 | `weibo.com`、`www.weibo.com`、`video.weibo.com/show?fid=...`、`m.weibo.cn` | 视频经本站 `/api/media/weibo` 短期签名票据（30 分钟有效）中转，依赖服务端配置签名密钥 |
| 皮皮虾（`pipixia`） | 视频 / 图片 | `pipix.com`（`h5.pipix.com/item/...`、`/ppx/item/...`） | 仅支持公开分享帖子 |
| 皮皮搞笑（`pipigx`） | 视频 | `pipigx.com`（`h5.pipigx.com/pp/post/...`）、`ippzone.com` | 优先校验 `tbvideo.ixiaochuan.cn` 无水印 MP4 直下（不支持 ZIP 打包），失败回退手动打开源链接 |
| 最右（`zuiyou`） | 视频 / 图片 | `izuiyou.com`、`share.xiaochuankeji.cn` | 线上关闭视频 `tbvideo` 改写，视频通过「打开源链接」手动保存；图片支持直下与打包 |
| X（`x`） | 视频 / 图片 | `x.com`、`twitter.com`（含 `www.`、`mobile.`、`m.` 状态页及 `/photo/<N>`、`/video/<N>`） | 不支持 `t.co` 短链；纯文本推文返回 `NO_MEDIA`；受限推文（`TweetTombstone`）需开启 `-x-tombstone-fallback=fxtwitter` 才查询公开回退源 |
| YouTube（`youtube`） | 视频 | `youtube.com`（`www.`、`m.`、`music.` 的 `/watch`、`/shorts/`、`/live/`、`/embed/`）、`youtu.be` | 桌面端需安装并启用用户脚本 `DokiTool YouTube Helper`（`1.3.1`）取址与浏览器内无损合并；手机端无脚本管理器时暂不支持 |
| Instagram（`instagram`） | 视频 / 图片 | `instagram.com`（`www.`、`m.` 的 `/p/`、`/reel/`、`/reels/`、`/tv/`） | 不支持 `instagr.am` 短链（返回 `UNSUPPORTED_PLATFORM`）；不支持 Stories（`/stories/`）及私密或需登录帖（返回 `UPSTREAM_BLOCKED`） |
| Facebook（`facebook`） | 视频 | `facebook.com`、`www.facebook.com`、`m.facebook.com`、`fb.watch` | `fb.watch` 短链需登录展开，接口直接返回 `INVALID_INPUT` 提示先在浏览器打开并复制 `facebook.com` 完整链接；受限视频返回 `UPSTREAM_BLOCKED` |
| Tumblr（`tumblr`） | 视频 | `tumblr.com`、`www.tumblr.com`、`*.tumblr.com` | 仅支持公开视频帖（单帖最多提取 4 个视频条目） |
| Dailymotion（`dailymotion`） | 视频 | `dailymotion.com`、`www.dailymotion.com`、`dai.ly` | 仅支持单流 fMP4 HLS 清单并在浏览器按序拼接保存；仅含非 fMP4（如 TS 分片）HLS 的视频返回 `NO_MEDIA` |

## 架构概览

- **浏览器直下，服务端仅解析**：Go 后端负责输入校验、上游页面/接口解析与统一媒体 v1（`schema_version=1.0`）契约输出；媒体文件由用户浏览器直接连接平台 CDN 下载（通过同源 Service Worker 流式落盘、缓冲拼接或原生附件移交）。**微博视频（`/api/media/weibo`）是唯一的服务端签名媒体流中转例外**。
- **日本主解析 + 上海抖音国内线路补充**：主后端承担全站路由与准入限流；针对抖音作品，上海 `shanghai-resolver` 通过出站 `pull` 模式（`v2` 协议）认领 `aweme_id`，提取国内 `*.douyinvod.com` 各档清晰度与 4K 原画候选交回主后端合并，由前端并发测速择优下载；上海忙碌或超时时主后端无阻塞回退本地解析。
- **YouTube 浏览器端取址与无损合并**：桌面浏览器安装同源用户脚本 `dokitool-youtube.user.js` 后，在客户端直接向 `www.youtube.com` 请求播放清单，交由服务端无权限 Deno `ejs` 沙箱解算签名挑战；`1080p` / `4K` 等分离音视频由脚本并发拉取 `googlevideo.com` 并在浏览器内用 Mediabunny 无损封装保存。

## 安全边界

- **无状态零 Cookie 出站与逐跳目标白名单**：服务端解析请求不携带任何用户或平台账号 Cookie；所有出站请求按平台白名单限制域名、协议与标准端口，并在逐跳重定向与 DNS 解析时拦截回环、私网、链路本地与云元数据地址。`yt-dlp` 子进程经本地回环任务网关强制执行同一出站域名白名单。
- **服务端不中转媒体**：除微博视频使用短期 HMAC-SHA256 签名票据（严格校验 `*.weibocdn.com` / `*.miaopai.com` 与上游响应类型）外，服务端不代理或落盘任何平台音视频与图片字节。
- **用户脚本最小权限**：`frontend/public/helper/dokitool-youtube.user.js` 仅响应固定页面事件，`@connect` 严格限定 `www.youtube.com` 与 `googlevideo.com`（覆盖 `*.googlevideo.com` 媒体子域），且全部跨域请求强制设置 `anonymous: true`（不附带浏览器 Cookie）。
- **唯一第三方解析回退依赖**：仅当启动参数显式设为 `-x-tombstone-fallback=fxtwitter` 且 X 官方 Syndication 接口返回 `TweetTombstone` 时，服务端才向公开接口 `api.fxtwitter.com` 查询推文元数据，且媒体域名仍强制限制为 `pbs.twimg.com` 与 `video.twimg.com`。

## 本地开发与测试

环境要求：**Go `1.27.3`**（见 `backend/go.mod`）、**Node.js `20+`**。

```bash
# 1. 前端依赖安装、单测与生产构建
cd frontend
npm ci
npm test
npm run build

# 2. 后端全量离线契约与单元测试
cd ../backend
go test ./...

# 3. 本地启动 Go API 服务（默认同源路由，可配合 Vite 开发代理联调）
go run ./cmd/dokitool serve
```

## 文档索引

- [docs/API.md](docs/API.md)：统一媒体 v1 接口契约、错误码（含 `UNSUPPORTED_PLATFORM`、`UPSTREAM_BLOCKED`、`NO_MEDIA`、`429` 退避）与 YouTube Helper 接口
- [docs/CONFIGURATION.md](docs/CONFIGURATION.md)：`dokitool` 与 `shanghai-resolver` 全部命令行启动参数及环境变量说明
- [docs/DOWNLOADS.md](docs/DOWNLOADS.md)：浏览器流式下载、多线路测速、ZIP 打包上限、调试开关与应用内置浏览器提示说明
- [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)：生产发布流程（发布 Issue → 干净构建 → 清单核验 → systemd drop-in 与软链原子切换 → 冒烟与复测）
- [docs/go-migration/rollback.md](docs/go-migration/rollback.md)：生产回滚操作流程与核验门禁
- [docs/go-migration/douyin-shanghai.md](docs/go-migration/douyin-shanghai.md)：抖音上海 `pull` 模式（`v2` 协议、3 worker、超时预算、回退与自动重启）设计说明
- [docs/schema/unified-v1/README.md](docs/schema/unified-v1/README.md)：统一媒体 v1 JSON Schema 定义与校验命令
- [docs/HISTORY.md](docs/HISTORY.md)：PHP 时期架构、Go 迁移历程与早期验收归档

## 许可证与上游致谢

本仓库原创与衍生代码采用 [MIT License](LICENSE)，完整第三方组件与许可证声明见 [NOTICE](NOTICE)：

- **早期架构与解析参考**：[jiuhunwl/short_videos](https://github.com/jiuhunwl/short_videos)（MIT）与 [jiuhunwl/shortvideo-html](https://github.com/jiuhunwl/shortvideo-html)（MIT）— 详见 [NOTICE](NOTICE) 与 [docs/HISTORY.md](docs/HISTORY.md)。
- **Mediabunny**（`1.65.2`，`MPL-2.0`，Vanilagy）：内嵌于公开用户脚本 `frontend/public/helper/dokitool-youtube.user.js`，用于在浏览器端对 YouTube 分离视频轨与音轨执行无损 MP4/WebM 封装合并；文件级源码获取说明保留在用户脚本头部与 [NOTICE](NOTICE)。
- **FixTweet**（`api.fxtwitter.com`）：在服务端显式启用 `-x-tombstone-fallback=fxtwitter` 时，作为 X 受限推文（`TweetTombstone`）的可选公开 HTTP 元数据查询源；本仓库未复制或分发 FixTweet 源代码（详见 [NOTICE](NOTICE)）。
