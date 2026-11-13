# 生产发布、静态缓存与本地验证

本文档说明当前线上 Go + 静态前端站点的生产发布标准流程、Caddy 静态与流式转发要求，以及本地验证方法。所有启动参数与环境变量定义见 [CONFIGURATION.md](CONFIGURATION.md)，回滚操作细则见 [go-migration/rollback.md](go-migration/rollback.md)，PHP 时期与早期迁移部署归档见 [HISTORY.md](HISTORY.md)。

> **公开仓库安全声明**：本文档及 `ops/examples/` 仅描述通用架构与流程规范，不包含任何服务器地址、主机名、远程连接细节、入站限制规则或生产密钥。仓库代码合入 `main` 不会自动触发线上变更。

---

## 一、生产发布标准流程

线上严禁任何自动跟随 `main` 分支的轮询或 Webhook，也严禁在正在对外提供服务的目录上原地编译覆盖。所有生产环境变更一律遵循以下受控闭环：

**发布 Issue → 只读预检 → 服务器干净构建 → 清单对齐核验 → 追加静态资源 → systemd drop-in 与软链原子切换 → 冒烟门禁 → 回执归档 → 线上复测**。

### 1. 发布 Issue 与只读预检

1. 新建专用**发布 Issue**，写明目标完整 40 位提交 SHA、包含的已合并 PR、是否涉及配置或辅助服务变更、冒烟检查项以及回退预案。
2. 确认目标提交在 GitHub Actions 的代码门禁与发布构建工作流（`.github/workflows/release-build.yml`）均已通过，取得对应的 `release-manifest.json` 清单摘要。
3. 在目标环境执行只读预检：记录当前生效的 `dokitool-go.service` `ExecStart` 与 drop-in 列表、当前静态目录软链指向的目标版本目录，以及磁盘可用空间。

### 2. 服务器干净目录构建

在与线上运行目录隔离的干净源码检出目录执行确定性构建：

1. **精确检出源码树**：执行 `git fetch` 并 `git checkout --detach <40位目标SHA>`，强校验 `git rev-parse HEAD` 与 `git rev-parse HEAD^{tree}` 与目标提交完全一致，且工作区无未跟踪或修改文件。
2. **后端编译**：使用 `backend/go.mod` 锁定的 Go `1.27.3`（`GOTOOLCHAIN=local`），以 `CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=<40位目标SHA>"` 构建 `./cmd/dokitool`（涉及上海解析端变更时同步构建 `./cmd/shanghai-resolver`），并执行 `<binary> version` 核对输出版本一致。
3. **前端编译**：在 `frontend/` 执行 `npm ci --ignore-scripts` 与同源模式 `npm run build`（勿单独运行 `vite build`，完整 `npm run build` 会同时产出客户端资源、预渲染首页 HTML 及 SEO 静态文件）。

### 3. 构建清单与关键产物核验

将干净构建产出的二进制与 `frontend/dist/` 同 CI 生成的 `release-manifest.json` 逐项核对：

- 核对 `frontend_dist_files` 的文件集合无缺失或多余项；
- 核对 `index.html`、`downloads/worker.js`、`downloads/bridge.html` 以及公开用户脚本 `helper/dokitool-youtube.user.js` 的 SHA-256 摘要与预期完全一致；
- 执行 `./dokitool catalog` 离线自检，确认平台目录 JSON 结构合法。

### 4. 不可变目录落盘与追加哈希静态资源

1. 创建新版本独立不可变目录 `<releases_root>/<target_sha>/`，放入编译通过的二进制与 `frontend/dist/`，规范化目录（`0755`）、普通静态文件（`0644`）与可执行文件（`0755`）权限。
2. **不覆盖并入上一版带哈希静态资源**：执行 `cp -n <prev_release>/frontend/dist/assets/* <new_release>/frontend/dist/assets/`，将上一活动版本的 `/assets/*` 带哈希 chunk 追加到新版本目录中（绝不覆盖新版本同名文件），保证已打开旧页面的浏览器在切换后按需懒加载旧 chunk 时不会遇到 `404`。

### 5. systemd 编号 drop-in 与静态软链原子切换

1. **后端平滑切换**：在 `dokitool-go.service.d/` 下新增更高编号的 drop-in 配置文件（先以空 `ExecStart=` 清空旧定义，再指向 `<releases_root>/<target_sha>/bin/dokitool` 并保持原有启动参数不变；既有环境变量、密钥引用与运行时隔离 drop-in 保持原状不触碰）。执行 `systemctl daemon-reload`，排空在途请求后停止旧进程、确认本地回环监听端口释放，再启动新服务并确认处于 `active (running)`。
   - 若本次发布涉及上海端 `dokitool-sh-pull.service` 的二进制或守护策略更新（如 `Restart=on-failure` drop-in），同样以独立编号 drop-in 更新并执行 `daemon-reload` 与重启。
2. **前端软链原子切换**：在同一父目录下创建指向 `<new_release>/frontend/dist` 的临时符号链接，再通过 `ln -sfn <new_dist> <frontend_symlink>.tmp && mv -T <frontend_symlink>.tmp <frontend_symlink>` 原子替换 Caddy 静态根目录软链。整个切换过程不清空站点存储、不强制注销客户端已激活的下载 Service Worker。
3. 若任一环节异常，立即按 [go-migration/rollback.md](go-migration/rollback.md) 移除新增 drop-in 并原子切回上一版软链。

### 6. 冒烟门禁、发布回执与线上复测

1. **切换后即时冒烟**：
   - 本地回环与公网 `GET /api/platforms?schema_version=1.0` 均返回 HTTP `200`、`code: "OK"`，且启用平台数量与列表符合预期（当前为 15 个平台）；
   - 公网首页返回 HTTP `200`，首页引用的全部新 `/assets/*` chunk 及至少一个保留的上一版 `/assets/*` chunk 均返回 HTTP `200`，不存在的 `/assets/*` 路径返回 `404`（不回退 SPA HTML）；
   - `/downloads/worker.js` 返回正确 JavaScript MIME 与 `Cache-Control: no-cache`；精确 `/downloads/bridge.html` 返回唯一 `X-Frame-Options: SAMEORIGIN` 与 `Cache-Control: no-store`；`/helper/dokitool-youtube.user.js` 版本号与摘要正确。
2. **发布回执与线上复测**：
   - 在发布 Issue 中如实记录切换前后 SHA、二进制与关键静态资源摘要、服务状态与冒烟结果（不含任何敏感环境信息）；
   - 针对本次上线涉及的平台或前端交互在真实线上站点完成端到端复测，验证通过后关闭发布单与对应问题单。

---

## 静态资源缓存与首次加载

Caddy 站点配置（通用示例见 `ops/examples/snap.caddy`）需满足以下静态资源与下载 Worker 边界：

- **带哈希静态资源缓存**：现存的 `/assets/*` 文件设置一年 `immutable` 缓存；首页 HTML 与其他非哈希入口文件使用 `Cache-Control: no-cache` 校验更新。缺失的 `/assets/*`、`/downloads/*`、`robots.txt`、`sitemap.xml` 与 `social/dokitool-share-v1.png` 必须返回精确 `404`，严禁回退并返回 SPA 首页 HTML。
- **下载 Worker 与同源 Bridge 头策略**：
  - `/downloads/worker.js` 及其同目录模块仅注册 `/downloads/` 作用域，返回有效 JavaScript MIME 与 `Cache-Control: no-cache`；
  - 仅精确路径 `/downloads/bridge.html` 允许同源 iframe 嵌入（返回唯一 `X-Frame-Options: SAMEORIGIN` 与 `Cache-Control: no-store`，并在反向代理层剥离条件请求头 `If-None-Match` / `If-Modified-Since` 以避免 Chromium `304` 合并旧 `DENY` 头）；首页、Worker 与其他路径保持 `X-Frame-Options: DENY`。可用 `python3 scripts/check-download-headers.py --base-url https://snap.example.com --output /tmp/headers.json` 检查公开响应头契约。
- **首屏体积与预渲染检查**：生产前端构建会自动输出中文预渲染首页 HTML 与 SEO 资源；可通过 `cd frontend && npm run build && npm run check:load-budget` 校验首屏 JS/CSS 体积预算（结果区与 ZIP 打包模块保持按需动态加载，不进入首屏静态依赖）。详细历史下载收尾背景见 [下载说明](DOWNLOADS.md#state-and-admission-consolidation-2026-10-30) 与 [统一验收收尾](UNIFIED-MEDIA-ACCEPTANCE.md#pr11-closeout)。

---

## 微博播放与下载

微博部分视频 CDN 校验来源请求头，浏览器直连无法自行附带，因此 `/api/media/weibo` 是全站唯一的短期签名媒体中转入口：

1. 主后端解析层与微博中转层共享服务端环境变量 `SNAP_MEDIA_SIGNING_KEY`（高熵随机密钥，仅保存在受保护的服务环境配置中，严禁写入仓库、前端构建变量或日志；参数定义见 [CONFIGURATION.md](CONFIGURATION.md)）。未配置密钥时微博解析明确返回 `503 SERVICE_NOT_CONFIGURED`。
2. 解析成功后仅对通过域名校验的微博官方视频源签发 30 分钟有效的 HMAC 签名票据；`/api/media/weibo?ticket=...&intent=play|download` 验签后固定上游来源请求头并按流转发（支持 `GET`、`HEAD` 与 `Range`），不接受任意 `url=` 参数、不跟随跨域重定向、连接前校验全部 DNS 结果均为公网地址。
3. 反向代理转发 `/api/media/weibo` 时须关闭响应压缩与额外缓冲（参考 Caddy [reverse_proxy streaming](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#streaming)），透传准确的 `Content-Length`、`Content-Range` 与 `Accept-Ranges`，使用 `Cache-Control: no-store`，且不在访问日志中记录完整 `ticket` 查询串。该路由受独立的 `WEIBO` 准入限流池保护。

---

## 统一媒体 v1 切换

前后端统一围绕 `schema_version=1.0` 契约协同发布：

- 每次发布或回滚必须同时对齐同一源码提交的后端二进制（内嵌 `backend/config/platforms.json` 目录）与 `frontend/dist/`；公开路由固定为 `/api/platforms`、`/api/parse`、`/api/resolve`、`/api/media/weibo`、`/api/downloads/tbvideo-result` 及可选启用的 `/api/youtube/context`、`/api/youtube/finalize`。
- 已退役的旧 `/api/short_videos`、`/api/douyin` 等兼容路径固定返回 JSON 版本提示错误（`410`），不得回退为 SPA HTML。接口契约与 Schema 校验规则详见 [API.md](API.md) 与 [schema/unified-v1/README.md](schema/unified-v1/README.md)。

---

## 本地 HTTP 验证

本地开发或验证不访问公网生产配置，全部绑定本机回环地址运行：

```sh
# 1. 运行后端全量离线契约与 HTTP 路由单测
cd backend
go test ./...

# 2. 在本机回环端口启动隔离测试实例（仅作本地验证示例）
./dokitool serve -listen 127.0.0.1:19186

# 3. 另一终端验证平台目录契约响应
curl --get 'http://127.0.0.1:19186/api/platforms' --data-urlencode 'schema_version=1.0'
```

验证完毕后终止本地测试进程即可。

---

## 历史

- **早期 GitHub Release 附件拉取脚本方案（方式 B 草案）**：早期文档曾记录通过 `ops/release/deploy.sh` 在主机侧调用 `gh release download` 与 `gh attestation verify` 拉取预编译包的方案；现网实际运行流程已统一收敛为上文「发布 Issue + 服务器干净检出直编 + `release-manifest.json` 校验 + systemd 编号 drop-in 与软链原子切换」。
- **PHP-FPM 多池部署与本地 PHP Mock 验证**：Go 全量接管前的三池 PHP-FPM（公开解析池、内部叶子池、媒体池）配置说明及 `php -S` 本地网关验证记录已归档至 [HISTORY.md](HISTORY.md)。
