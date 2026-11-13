# 历史演进、PHP 迁移与早期验收记录

本文档归档原 `README.md` 与 `docs/DEPLOYMENT.md` 中的 PHP 时期说明、Go 迁移过程及早期验收记录，供追溯参考。当前线上架构、15 平台支持矩阵、配置项与发布流程请以根 [README.md](../README.md)、[CONFIGURATION.md](CONFIGURATION.md) 和 [DEPLOYMENT.md](DEPLOYMENT.md) 为准。

---

## 一、原 README 历史说明与迁移记录

### 早期 Go 迁移基线与九平台阶段状态

DokiTool 最初为基于 PHP 后端与 Vue 3 + Vite 前端的短视频/图文去水印解析工具，早期聚焦以下 9 个国内平台：

- 抖音（视频 / 图集 / 实况）
- 快手（视频 / 图集）
- 哔哩哔哩（单视频 / 多 P 选集）
- 小红书 / rednote（视频 / 图集 / 实况）
- 今日头条（视频）
- 微博（视频）
- 皮皮虾（视频 / 图集）
- 皮皮搞笑（视频）
- 最右（视频 / 图集）

Go 后端替代完成后，初期线上以「日本 Go 主服务 + 上海 `pull` worker」承载九平台解析与下载（参见 [抖音上海通道说明](go-migration/douyin-shanghai.md) 与 [下载说明](DOWNLOADS.md#production-download-capabilities)），随后在 Go 单栈基础上陆续接入 X、YouTube、Instagram、Facebook、Tumblr、Dailymotion 六个海外平台，扩展至 15 个平台。

Go 构建、同源启动与早期迁移细节见 [Go 迁移总览](go-migration/README.md#当前运行方式)。仓库根目录历史 PHP `api/` 在迁移过渡期仅保留用于离线基线对照、语法门禁与历史参考，不再作为线上新部署入口。

### 来源与许可历史记录

项目最初参考并整合以下 MIT 开源项目：

- 后端解析逻辑来源：[jiuhunwl/short_videos](https://github.com/jiuhunwl/short_videos)
- 前端界面基础来源：[jiuhunwl/shortvideo-html](https://github.com/jiuhunwl/shortvideo-html)

在此基础上完成了前后端工程化整理、统一返回结构、前端构建链收敛、安全加固、Go 后端重写、下载可靠性改进与自动化测试建设。仓库根目录许可证见 [LICENSE](../LICENSE)，完整第三方许可说明见 [NOTICE](../NOTICE)；历史迁移维护边界见 [维护边界](go-migration/MAINTENANCE.md#来源和许可边界)。

### 迁移过渡期目录结构

```text
.
├── api/                 # 历史 PHP 解析与兼容辅助实现（仅供离线基线对照）
├── backend/             # Go 后端与离线契约测试
├── frontend/            # Vue 3 + Vite 前端源码、静态资源构建与回归测试
├── scripts/             # 本地与 CI 校验脚本
├── docs/                # API、部署、下载、历史与故障复盘文档
├── index.html           # 历史兼容跳转页
└── router.php           # PHP 内置开发服务器路由（仅供历史对照）
```

### 统一媒体 v1 合同（PR #7 / PR #11 验收归档）

新前端默认使用统一媒体 v1 接口，必须显式传入 `schema_version=1.0`；兼容路由仍保留给旧调用方。接口说明见 [API 合同](API.md)，JSON Schema 与校验命令见 [schema 与验证](schema/unified-v1/README.md)，前后端成套切换要求见 [成套切换要求](DEPLOYMENT.md#统一媒体-v1-切换)。

- `GET /api/platforms?schema_version=1.0`
- `GET /api/parse?schema_version=1.0&url=<编码后的分享链接>&platform=all`
- `GET /api/resolve?schema_version=1.0&url=<编码后的分享链接>&platform=<平台>&item_id=<条目标识>`
- `GET /api/media/weibo?ticket=<短期签名票据>&intent=play|download`

默认下载只依赖统一响应里的标准 `delivery`、来源 `access` / `browser_fetch` 事实和所需能力，不在前端写死具体平台 CDN 域名；仅在平台直链没有跨域读取权限且上游未声明附件下载时，才启用针对该平台的附加校验或中继入口（例如皮皮搞笑 `tbvideo` 同路径候选校验、今日头条附件直下、微博短期签名票据中转）。详见 [下载说明](DOWNLOADS.md)。

PR #11 已按当时维护者拍板收尾：在 `fe405f8` 基线上完成真实单图回归与下载传输整理，补入小红书笔记页独立 TLS ClientHello 传输并默认保持 `stdlib`；未继续处理当时本地代理出口在快手 `txmov2` 上的环境差异。详见 [统一媒体验收记录](UNIFIED-MEDIA-ACCEPTANCE.md#pr11-closeout)。

### 历史 PHP 环境要求与本地校验命令

迁移过渡期曾同时维护的前后端与离线基线环境：

- Node.js `20+`
- Go `1.27.3`（Go 后端开发、构建与离线契约测试）
- PHP `8.1+`（仅用于历史 PHP 对照与语法检查，启用 `curl`、`json`、`mbstring`）
- Python `3.10+`（运行仓库门禁与统一媒体 Schema 校验脚本）

过渡期曾记载的补充开发与门禁命令：

- 跨域调试构建说明：`frontend` 默认使用同源 PHP 路由模式（`VITE_API_MODE=php` 且 `VITE_API_BASE_URL` 为空，Go 后端复用同一套同源 `/api/*` 路径）；`VITE_API_MODE=secure` 为保留的独立安全接口模式，构建时必须显式提供完整 `http(s)` `VITE_API_BASE_URL`。
- 静态预渲染资源检查：`frontend/dist` 产出首页静态 HTML、`dist/robots.txt`、`dist/sitemap.xml` 和 `dist/social/dokitool-share-v1.png`；缓存与预加载预算见 [静态资源缓存与首次加载](DEPLOYMENT.md#静态资源缓存与首次加载)。
- 历史 PHP 语法与迁移门禁：

```bash
find api -name '*.php' -print0 | xargs -0 -n1 php -l
php -l router.php
python3 -B scripts/check-public-files.py
python3 scripts/validate-unified-schema.py
```

- 历史 PHP 本地 HTTP 冒烟、微博签名密钥配置与状态收敛说明分别见 [本地 HTTP 验证](DEPLOYMENT.md#本地-http-验证)、[微博播放与下载](DEPLOYMENT.md#微博播放与下载) 与 [下载验收记录](DOWNLOADS.md#state-and-admission-consolidation-2026-10-30)。
- 上游变更自动观察工作流位于 [`.github/workflows/upstream-watch.yml`](../.github/workflows/upstream-watch.yml)，仅提交观察报告或提醒 Issue，不自动合入上游代码（详见 [UPSTREAM-WATCH.md](UPSTREAM-WATCH.md)）。

---

## 二、PHP 时期与早期方式 B 部署切换归档

1. **早期 GitHub Release + `gh attestation verify` 方式 B 方案**：
   在切换为服务器本地干净检出直编（`git checkout --detach <40-char SHA>` + `git rev-parse HEAD^{tree}` 校验）前，文档曾规划由 `.github/workflows/release.yml` 产出带构建来源证明（Build Provenance Attestation）的二进制与前端压缩包，并由主机侧脚本验证签名后解压至 `/srv/dokitool-go/releases/<sha>/`。现网实际采用并稳定运行的流程是**发布 Issue 驱动 + 服务器干净目录直编 + CI `release-manifest.json` 哈希对齐 + systemd 编号 drop-in 覆盖 `ExecStart` + 前端软链原子切换**（详见 [DEPLOYMENT.md](DEPLOYMENT.md)）。
2. **PHP 时期 Nginx / Apache / PHP-FPM 部署说明**：
   PHP 时期由 `frontend/dist` 承载静态首页，`/api/*` 交由 PHP-FPM 执行 `api/*.php`（如 `api/platforms.php`、`api/parse.php`、`api/resolve.php`、`api/media/weibo.php`），并通过 FastCGI 参数或进程环境注入 `SNAP_MEDIA_SIGNING_KEY`。自 Go 后端全量接管后，线上统一由 Caddy 托管静态资源并将 `/api/*` 反代至本机回环 Go 服务，不再运行 PHP-FPM。
