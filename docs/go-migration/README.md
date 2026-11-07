# Go 迁移工作区

后续 Go 开发在独立仓库 [Brilliant666/DokiTool-Go](https://github.com/Brilliant666/DokiTool-Go) 进行，维护者已将其公开用于开发协作。原 [Brilliant666/DokiTool](https://github.com/Brilliant666/DokiTool) 的 `main` 继续保留 PHP 开源版本，不整体合入 Go 迁移分支，也不以删除公开 PHP 为迁移目标。

维护、选择性同步及许可边界见 [MAINTENANCE.md](MAINTENANCE.md)。行为对照见 [mapping.md](mapping.md)，未来发布或切流前的回滚门槛见 [rollback.md](rollback.md)，各阶段证据见 [acceptance.md](acceptance.md)。抖音和 B 站的实现之后，维护者已授权[持续完成剩余七个平台](remaining-platforms.md)。该迁移阶段只做受控上游测试，不代表真实平台或浏览器保存验收。

## 当前交付与历史证据

snap 的生产解析入口已是日本 Go。抖音原画的上海 pull 架构、#28 基线和 #30 后的运行组合见[生产版本与恢复入口](douyin-shanghai.md)；小红书实况及三平台无中继保存的范围见[下载说明](../DOWNLOADS.md#production-download-capabilities)。

早期 Go 集成基线经 [PR #20](https://github.com/Brilliant666/DokiTool-Go/pull/20) 合入 `main`：`7641d65d9bdb2016a0425e189b9b35f9567d6813`，tree `ed2d4314c57cdee2631fef8ee2d87691ce85762d`。合并 tree 与已验 `e92cfdf` 相同，但后者 CI 不冒称为 merge SHA 的新 CI。后续抖音基线见 [#9](https://github.com/Brilliant666/DokiTool-Go/issues/9)，平台和网络待验边界见 [#13](https://github.com/Brilliant666/DokiTool-Go/issues/13)。PR #20 当时只合并源码，未切换生产。

[验收记录](acceptance.md)、[机器报告](nine-platform-checks.json)、[B 站说明](bilibili.md) 和[五平台实施说明](remaining-platforms-implementation.md)中的 3835/994/918 等数字属于对应历史提交，不是每次构建的新门禁结果；不要为启动交接重复整套九平台矩阵。旧迁移自动调度不恢复；当前协作方式见 [#11 顶部说明](https://github.com/Brilliant666/DokiTool-Go/issues/11)。

[小红书专项](xhs-page-transport.md)和[旧机器报告](xhs-page-online-checks.json)保留当时的真实原画与限制。旧单图长度/MOV 后缀观察不是当前版本必然仍失败的结论，亦不能直接改写为通过。后续下载/身份修正及限定真实验收见 PR #20/#9 的准确 SHA 记录；不外推所有平台、媒体与浏览器均通过。

## 当前运行方式

Go module 为 `github.com/Brilliant666/DokiTool-Go/backend`，位于 `backend/`；内部 import 使用该独立仓库路径。固定工具链为 Go **1.27.3**。基础解析和标准媒体传输沿用标准库；本专项另引入锁定的 `tls-client` / `fhttp` 页面传输依赖，版本及完整性以 `go.mod/go.sum` 为准。PHP 应用保留为冻结参考。下列本地 Go 示例使用独立的回环端口，没有 PHP 回退调用。

### 1. 固定版本构建

以下为 Bash，从干净的目标提交根目录执行；`go version` 必须实际显示 `go1.27.3`，不能仅凭命令名认定工具可用。Node/npm 使用可运行 Vite 5 的受支持版本，依赖以 `frontend/package-lock.json` 为准。默认构建不需要 PHP。

```sh
set -eu
ROOT="$PWD"
SOURCE_SHA="$(git rev-parse HEAD)"
OUT="$(mktemp -d "${TMPDIR:-/tmp}/dokitool-go.XXXXXX")"
go version
(cd backend && GOTOOLCHAIN=local go build -trimpath \
  -ldflags "-X main.version=$SOURCE_SHA" -o "$OUT/dokitool" ./cmd/dokitool)
(cd frontend && npm ci && VITE_API_MODE=php VITE_API_BASE_URL='' npm run build)
"$OUT/dokitool" version
"$OUT/dokitool" catalog
(cd backend && GOTOOLCHAIN=local go test ./cmd/dokitool)
# 保存这次真实构建的来源/架构和摘要；不要复制旧二进制后改版本名。
printf 'source_sha=%s\n' "$SOURCE_SHA" > "$OUT/build.txt"
go version >> "$OUT/build.txt"
go env GOOS GOARCH >> "$OUT/build.txt"
sha256sum "$OUT/dokitool" >> "$OUT/build.txt"
(cd frontend/dist && find . -type f -print0 | sort -z | xargs -0 sha256sum) \
  > "$OUT/frontend.sha256"
printf 'artifact_directory=%s\n' "$OUT"
```

有完整、获准缓存时可用 `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local` 和 `npm ci --offline` 复用缓存（本轮前端使用 `--ignore-scripts` 后正式 build 成功）；缓存缺项会失败，不能称构建通过。依赖下载被安全策略拒绝时停止该请求，不换镜像绕行。构建产物、日志、真实密钥不提交。`version` 未注入 `-X main.version=...` 时仅输出 `development`；没有 HTTP `/version` 路由。

### 2. 配置与 CLI

| 配置/入口 | 实际语义 |
| --- | --- |
| `dokitool serve -listen 127.0.0.1:9082` | 默认 `serve`；只接受明确回环 IP，端口 1024–65535，不接受 `0.0.0.0` 或主机名。 |
| `-xhs-page-transport standard` | 默认标准传输；`chrome131` 仅显式影响小红书 HTTPS 页面，不改变媒体策略。 |
| `dokitool version` / `catalog` | 构建版本 / 内嵌目录，均无需外网。 |
| `dokitool validate < response.json` | stdin 读取统一响应，最多 8 MiB；不是 parse 命令或配置加载器。 |
| `SNAP_MEDIA_SIGNING_KEY` | Go 服务启动前注入的服务端秘密，32–512 字节；微博公开视频签发和 relay 必需。未配置时服务/目录仍可启动，但微博相关服务返回未配置，不能算微博通过。不放进 `VITE_*`、命令日志或仓库。 |
| `VITE_API_MODE=php` | 兼容统一 `/api/*` 的模式名，Go 同样使用；不是 PHP 进程依赖。`go` 无此模式，`secure` 需要本项目未实现的另一协议。 |
| `VITE_API_BASE_URL=''` | 构建期留空，使浏览器请求同源；不要填 `/api` 路径。 |
| `DEV_API_TARGET=http://127.0.0.1:9082` | Vite dev/preview 的 `/api` 代理目标，运行时给预览进程；默认值为旧 PHP 网关 8081，Go 必须显式覆盖。 |
| `DOKITOOL_YTDLP_X_ENABLED` / `DOKITOOL_YTDLP_YOUTUBE_ENABLED` / `DOKITOOL_YTDLP_INSTAGRAM_ENABLED` | 可选启用 X / Twitter、YouTube、Instagram 适配器（均默认 `false`，生产默认不启用；不含 `t.co`、`instagr.am` 短链主机）。仅当共享 `internal/ytengine` 二进制预检（SHA-256 校验）通过后，才向运行时内存目录注册对应已启用平台；预检失败时不写入运行时目录，不影响原有九个平台。 |
| `DOKITOOL_YTDLP_GATE_PORTS` | 显式逗号分隔的有限回环端口集合，与共享并发匹配；各平台共享租约，每次提取独立认证。日本部署必须配置经审查的固定集合，不能用未配置的动态回环模式。配置、最小 nft 差异及撤回边界见 [任务网关说明](ytdlp-gateway.md)。 |
| `DOKITOOL_YTDLP_BINARY` | 指定已固定的 `yt-dlp 2026.10.26` 发行产物路径；未设置时在 `PATH` 中查找 `yt-dlp` / `yt-dlp.exe`。无论显式路径还是 `PATH` 发现，启动时均强制核对官方 `SHA2-256SUMS`。 |
| `DOKITOOL_YTDLP_CONCURRENCY` / `DOKITOOL_YTDLP_TIMEOUT` / `DOKITOOL_YTDLP_PROXY` | 跨平台共享 `yt-dlp` 子进程总并发上限（默认 `2`，范围 `1–8`，X/YouTube/Instagram 共用同一信号量池；满额时新 `yt-dlp` 请求非阻塞立即返回 `429 RATE_LIMITED` 与 `Retry-After: 1` 的本地解析引擎繁忙提示，不挂起等待或占满公共 `/api/parse` 准入池）与单次解析超时（默认 `25s`，范围 `2s–40s`）。当前受控出站边界不支持 `DOKITOOL_YTDLP_PROXY` 外部代理：配置非空值时 `ytengine.NewFromEnv` 与 `NewHTTPSizeProber` 均会拒绝启用（`main` 不注册对应新平台，且不会自动改为绕过保护的直连）；保持空配置时，Go 大小探测复用 `internal/fetch.Executor`，子进程通过进程内回环安全网关强制执行平台域名白名单、`80`/`443` 端口限制及逐跳公网 DNS 校验与 IP 绑定。仅在新 `yt-dlp` 平台（`x` / `youtube` / `instagram`）启用“完整有声文件优先于拆轨无声/纯音频轨”的默认选择资格规则（原有九个平台默认选择与 `main` 完全一致）：YouTube 默认选中完整音画直链（`360p (含音频)`，`itag=18` 附件交付），4K/2K/1080p 仅视频无声裸轨（`has_audio: false`，保留真实宽高元数据）与独立 M4A 音频保留在 `variants` 供显式选择并通过“打开源链接”手动保存；Instagram 无编解码器声明的直链保持 `has_audio` 未知（省略该字段）。 |

公网入口应运行单个 Go 实例，显式设置 `-client-identity caddy` 和独立服务端凭据 `SNAP_INGRESS_KEY`（32–512 字节、不含空格的可打印 ASCII）。Caddy 每次向 Go 转发时覆盖 `X-DokiTool-Client-IP` 为真实连接方 IP、`X-DokiTool-Edge-Key` 为双方约定的秘密。Go 仅接受来自回环、已认证且各只出现一次的头；身份配置错误时拒绝请求。直接回环调试默认根据 TCP 对端计数。签名媒体与入口认证使用不同密钥，密钥只从权限受限的运行配置注入。反代和应用日志均不得记录这些头、完整签名 URL、票据或 Referer。

| 入口 | 单客户 / 全局突发 | 单客户 / 全局补充 | 单客户 / 全局在途 |
|---|---|---|---|
| parse、resolve 及六个旧接口 | 4 / 32 | 每秒 0.2 / 10 | 1 / 16 |
| platforms 目录 | 20 / 128 | 每秒 5 / 100 | 4 / 64 |
| 精确微博 relay | 12 / 24 | 每秒 1 / 2 | 2 / 12 |
| tbvideo 结果上报 | 10 / 60 | 每秒 1 / 5 | 2 / 16 |

四池独立，按可信 IPv4 地址或 IPv6 /64 分组；目录请求不再占用解析名额。并发租约持有至处理返回，含流式传输、错误和取消。过载返回 429 和 `Retry-After`。每池默认最多记录 4096 个客户。表满时仅回收无在途且令牌已经补满的条目；活动请求和仍欠额度的客户不被驱逐。限额在单进程内共享；重启会重置短期令牌，不能并行运行多份面向同一站点的实例。在入口代理前还有 CDN 时，须另行验证可信客户端地址链；直接采用客户端提交的 XFF 不满足上述要求。

### 可配置的生产容量

以上是本版程序默认值，不是已经测出的生产吞吐量，也不表示现网已改限额。
每个池分别配置单客户防刷和全局容量；不使用一个全站固定的两并发上限。
环境变量在日本 Go 启动时读取，修改后按现有流程重启，无需重新编译。

前缀为 `DOKITOOL_PARSE_`、`DOKITOOL_CATALOG_`、`DOKITOOL_WEIBO_`、
`DOKITOOL_REPORT_`，均支持 `CLIENT_BURST`、`CLIENT_RPS`、
`CLIENT_CONCURRENCY`、`GLOBAL_BURST`、`GLOBAL_RPS`、`GLOBAL_CONCURRENCY`。
RPS 为每秒补充令牌数（允许小数）；并发和令牌速率分别生效。
例如 `DOKITOOL_PARSE_GLOBAL_CONCURRENCY=32`、`DOKITOOL_PARSE_GLOBAL_RPS=20`
可单独调整服务容量，而不放宽单 IP 限额。不能把它当作实测每秒完成 20 次解析的保证。
`DOKITOOL_ADMISSION_MAX_CLIENTS` 设置各池客户表上限（默认 4096，最大 65536）。
所有值必须为有限正数；并发必须为整数且单客户不超过全局。
微博全局并发最大 12，与内部现有流名额一致；非法值启动失败，不静默关闭保护。
启动日志记录实际生效的非敏感限额，便于区分程序默认与部署覆盖值。

保留旧生产容量只需显式设置解析 `GLOBAL_CONCURRENCY=2`、`GLOBAL_BURST=8`、
`GLOBAL_RPS=1` 和微博 `GLOBAL_CONCURRENCY=2`；不必回退媒体处理代码。
本次源代码交付不修改线上环境变量，提升生产容量仍按维护者批准发布。
结果上报只接受最多 768 字节且读取期限为 3 秒；其慢请求不能占住解析或目录池。
上海长轮询、媒体流超时和浏览器下载并发均不改变。

无通用 YAML 配置加载器、PHP 回退、内置前端静态服务器或公开任意 URL 代理。普通媒体仍由浏览器访问 CDN，API 同源代理不能保证 CDN 可访问或浏览器保存成功。

### 3. 正式 Vue 产物同源启动、无外网冒烟、停止

延续上面的 `ROOT` / `OUT`；先确认 9082/4173 未被占用（若改端口同时改代理和检查）。这使用 `npm run build` 的正式 `dist`，不是另一套演示 UI。**Vite preview 只做隔离本地验收，不是生产发布方案。** 不启动 PHP、不访问真实平台。

```sh
cd "$ROOT"
"$OUT/dokitool" serve -listen 127.0.0.1:9082 > "$OUT/go.log" 2>&1 &
GO_PID=$!
(cd frontend && exec env DEV_API_TARGET=http://127.0.0.1:9082 \
  node node_modules/vite/bin/vite.js preview --host 127.0.0.1 --port 4173 --strictPort) \
  > "$OUT/vue.log" 2>&1 &
VUE_PID=$!
stop_local() {
  kill "$VUE_PID" "$GO_PID" 2>/dev/null || true
  wait "$VUE_PID" 2>/dev/null || true
  wait "$GO_PID" 2>/dev/null || true
}
trap stop_local EXIT INT TERM
# 等监听就绪；连接失败只在本地短暂重试，不触及上游。
for i in $(seq 1 50); do
  if curl -fsS 'http://127.0.0.1:4173/api/platforms?schema_version=1.0' \
    > "$OUT/platforms.json"; then break; fi
  sleep 0.1
done
curl -fsS 'http://127.0.0.1:9082/api/platforms?schema_version=1.0' > "$OUT/direct.json"
cmp "$OUT/platforms.json" "$OUT/direct.json"
curl -fsS http://127.0.0.1:4173/ > "$OUT/home.html"
curl -fsS http://127.0.0.1:4173/downloads/worker.js > "$OUT/worker.js"
curl -fsS http://127.0.0.1:4173/downloads/bridge.html > "$OUT/bridge.html"
cmp "$OUT/worker.js" frontend/dist/downloads/worker.js
cmp "$OUT/bridge.html" frontend/dist/downloads/bridge.html
# 无 url 的错误应来自 Go JSON，不应误返回 SPA HTML；预期分别 400、400、409。
curl -sS -o "$OUT/parse-error.json" -w '%{http_code}\n' 'http://127.0.0.1:4173/api/parse?schema_version=1.0'
curl -sS -o "$OUT/resolve-error.json" -w '%{http_code}\n' 'http://127.0.0.1:4173/api/resolve?schema_version=1.0'
curl -sS -o "$OUT/schema-error.json" -w '%{http_code}\n' 'http://127.0.0.1:4173/api/platforms'
stop_local
trap - EXIT INT TERM
# 两个监听应均连接失败；还需结合进程退出码检查，不用 killall 影响其他服务。
if curl -sS http://127.0.0.1:9082/ >/dev/null 2>&1; then echo 'Go listener still open'; exit 1; fi
if curl -sS http://127.0.0.1:4173/ >/dev/null 2>&1; then echo 'Vue listener still open'; exit 1; fi
```

Go 接收 SIGTERM/SIGINT 后关闭监听并等待在途请求，shutdown 上限 310 秒；不要以一次命令退出 0 代替检查响应和停止。Worker/bridge 资源存在且 MIME 正确只证明资源交付，不能证明浏览器 Service Worker 注册、流式保存或真实媒体下载。HTTPS/生产代理、密钥注入、生产回滚与切流均需单独验收/授权。

### 本轮隔离执行记录

2026-11-02，task `go9-go-launch-handoff-7641d65`，dot Linux x86_64 云工作区，对上述 `7641d65` 的完整 tree 构建：Go 1.27.3、Node v24.22.1、npm 11.9.0、Python 3.12.14。离线 Go build、锁文件 `npm ci --offline --ignore-scripts`、正式前端 build（含 4 项 SSG 检查）、Go CLI tests 均 exit 0。既有 Vite preview 已继承 `server.proxy`，无需修改入口代码。

实际使用独占回环端口 19082/14173：首页、Worker、bridge 均 200，Worker/bridge 与 dist 字节一致；catalog CLI 与 `/api/platforms?schema_version=1.0` JSON 一致；同源代理的 catalog 200、缺 schema 409、缺 URL 的 parse/resolve 400、无密钥微博 relay 503 均与直连 Go 响应逐字节一致。Go/preview 都正常退出 0，两个端口已释放。没有启动 PHP、访问上游或下载媒体，没有重跑九平台门禁。精确产物摘要、交付 SHA、CI 与日志由 #9 的同 task_key DOT_REPORT 保留；文档更新不能把这次 base 构建冒称为之后 SHA 的新产物。

catalog 从原路径 `backend/config/platforms.json` 内嵌，公开字段与九平台 PHP 目录保持一致。目录表示项目的平台注册事实；某平台尚未绑定 Go adapter 时，其解析请求明确返回 `503 SERVICE_NOT_CONFIGURED`，不会把空媒体结果当迁移成功。本分支已绑定抖音、B站、微博、头条、快手、皮皮虾、皮皮搞笑、最右、小红书全部九个平台。两类 [历史基线合同问题](known-issues.md) 的公共修正已通过 PR #2 的完整离线门禁，Go 服务仍不能替代九平台生产入口。

公开 JSON 仍为 `/api/parse`、`/api/resolve`、`/api/platforms`，需要 `schema_version=1.0`。例如检查目录：

```sh
curl 'http://127.0.0.1:9082/api/platforms?schema_version=1.0'
```

不接受公开任意 URL 代理、客户端 Cookie/传输配置或 TLS 参数。平台经 `internal/fetch.Executor` 请求上游，实际执行器统一约束目标、DNS、socket 绑定、逐跳校验、读取限额和 context。首次实现采用标准 `net/http`；替换握手/传输实现时须保留同一策略和对应测试。媒体探测及微博流式媒体各自有更严格/独立的预算，不应直接复用普通 JSON 请求预算。

抖音有界 MP4 探测每次最多 1 MiB、12 个请求、6 秒，限制重定向与文件总大小，不下载整片。严格媒体请求使用标准库 HTTP/1.1 传输，并在标准解析器前观察原始头部和 trailer，防止同值重复 `Content-Length` 被合并而丢失原有拒绝证据；正文仍由 `net/http` 解码。默认模式的普通页面/API 请求保留标准库 HTTP/2 协商；显式 Chrome131 模式只改变小红书 HTTPS 页面请求，媒体探测仍走原标准传输。所有路径都将请求取消绑定到底层连接。

Go 对普通请求新增了逐跳目的域、DNS 与连接约束；Live HEAD 重定向只访问已声明媒体主机。这是已批准的出站边界收紧，与旧 PHP 接受任意重定向的行为不同，真实平台兼容性仍须在后续阶段验证。

## 合同与身份

`internal/unified` 使用保留精度的 JSON tree 表示公开对象：`json.Number` 不先转浮点，缺字段、null、false、0 和空数组分别保留。所有公开媒体结果在完整候选阶段校验，应用来源资格和公共候选策略后再次校验。平台包不能自行实现第二套默认规则或把未经验证的 tree 直接发给前端。

同一规范仍是 `docs/schema/unified-v1/unified-v1.schema.json`。Go 只内嵌自动生成且有字节一致性测试的副本：

```sh
cd backend
go generate ./internal/unified
go test ./internal/unified
```

身份哈希与票据使用显式的 PHP JSON 兼容编码，不能改成普通 Go map 的 JSON 编码。公共 JSON 对象键序不影响比较，但哈希/签名的输入字节有意义。变更身份规则必须单独审查，而不是更新 fixture 以通过测试。

完整离线门禁可一次执行（需要 Go 和既有 Python 校验依赖，无需 PHP）：

```sh
python scripts/go-migration/check.py --race --report /tmp/dokitool-go-checks.json
# 已安装 frontend/package-lock.json 中的依赖时，再加入原前端消费、原有四项续签及微博四项签名续签检查：
python scripts/go-migration/check.py --race --frontend --report /tmp/dokitool-go-checks.json
```

门禁会运行各项独立检查并保存失败明细，任何未通过的公共响应使总结果为 `passed: false`、进程退出 1。fragment 与极限文件大小保留原 PHP 期望，在测试专用清单中精确声明 10 项公共响应修正；清单应用后仍逐字段比较整个实际输出，不跳过场景或放宽消费者。

旧 `--check-php-source` 检查针对 `665bd55` 的来源字节；同步新 PHP 后，旧来源应从固定提交单独导出验证，不以当前工作区的合法产品差异作为旧 fixture 损坏。

## 冻结 PHP 对拍

已发布 PHP `25beaee` 的版本化基线见 [新基线与复现方法](../../backend/testdata/php-25beaee/README.md)。新旧归档分别用 **PHP 8.3.6** 和同一固定时钟执行。旧样本重放一致后按原哈希引用；12 个新场景记录旧/新 PHP 输出及逐字段差异，Go 在真实探针、formatter、adapter、服务端选择和公开投影路径上逐项对拍。

统一门禁会把 24 份新增实际 Go 响应交给既有独立 Schema 和前端消费者；`--check-php-source` 从各自的固定 Git 对象检查源字节，不用新 PHP 工作区核对旧来源哈希。在安装前端锁定依赖后执行：

```sh
python -X utf8 scripts/go-migration/check.py --check-php-source --frontend --report /tmp/dokitool-go-checks.json
```

使用 UTF-8 运行含中文样本的 Python 工具。[PHP 同步阶段结果](php25-checks.json)保留当时的非零退出及两个合同问题；后续修复和平台迁移的当前结果见 [验收记录](acceptance.md)。不把 Go 单测通过或目录包含九个平台解释为可部署。

旧 `665bd55` 冻结目录：`backend/testdata/php-baseline/`（公共层）与 `backend/testdata/php-douyin/`（实际抖音 Parse、请求序列、探测及舍入）。这些原始输入、期望、生产器和哈希保持不变。已发布 PHP `25beaee107f8c1a208ced9deb3c637068445e316` 作为新增版本基线，变化须由该提交的 PHP 产品路径生成并逐项说明，不覆盖旧期望，也不使用 Go 输出反写期望。正常 Go 测试只读固定基线，不安装或运行 PHP。

```sh
python scripts/go-migration/verify.py
python scripts/go-migration/verify.py --douyin
python scripts/go-migration/verify.py --integers
```

需要复核 PHP 生成来源时，按该目录 README 从固定 Git commit 重新导出至临时目录，核对差异；旧版 `--source-root` 必须指向 `665bd55` 的归档，不能直接指向已同步新 PHP 的当前工作区。生产代码和普通 Go 验收不依赖这个生成器。测试密钥均明确标为公开合成材料，真实密钥只记录受保护来源引用。

可以将 **实际 Go 输出** 导出给既有独立 Python 检查：

```sh
cd backend
DOKITOOL_GO_BASELINE_OUTPUT=/tmp/dokitool-go-baseline.json \
  go test ./internal/unified -run TestPHPBaseline -count=1
cd ..
python scripts/validate-unified-schema.py /tmp/dokitool-go-baseline.json
```

Python 校验依赖仍由 `scripts/requirements-validation.txt` 声明。导出不覆盖 PHP 期望输出，也不把未知动态字段整批删掉。

单个平台仍可通过对应 `DOKITOOL_GO_*_OUTPUT` 导出实际响应，用于单独审查。完整前端脚本要求九平台的冻结库存齐全，请使用上面的 `check.py --frontend` 统一生成和消费；不要把只有抖音的导出文件传给全平台脚本。该脚本只扩展测试输入，未改变前端产品代码。

## 阶段顺序

骨架、公共合同、稳定身份、票据、抖音与 B 站实现保留为阶段成果。其余七平台的实现、审查和可执行离线验证已经完成，见 [任务结果](remaining-platforms.md)；当时的实施授权不含生产切流。公开 PHP `main` 持续保留。未来需要切流时，先完成相关行为、客户端消费与可恢复发布产物验收，再执行 [回滚门槛](rollback.md)。DASH 合流执行器仍在范围外；小红书新增页面传输已在独立 PR #5 获得实施授权，不追溯改变原迁移排除项，真实原画的专项结果见上文。此处保留迁移阶段记录，现网状态见本文开头。

## 匿名增强管线（吸收 OmniGet 借鉴点 1–3）

在不引入用户登录态/Cookie 托管、不新增服务端媒体中继或转码合流的前提下，后端已落地三项匿名解析与运行稳定增强：

1. **借鉴点 1｜Instagram 免登录三级解析管线（`yt-dlp` -> Embed Captioned -> Anonymous GraphQL）**
   - 当 `yt-dlp` 因匿名登录墙或上游变更未产出有效视频/图集格式时，`internal/platform/instagram` 在同一请求内按序回退到：
     - **Tier 1（Embed Captioned）**：通过受控 `fetch.Executor` 请求 `https://www.instagram.com/p/{shortcode}/embed/captioned/`，提取 `contextJSON`（含反义 JSON 字符串）或 `<video>` / `<img class="EmbeddedMediaImage">` 直链。
     - **Tier 2（Anonymous GraphQL）**：通过受控 `fetch.Executor` 发送匿名 `POST https://www.instagram.com/api/graphql`（`fb_api_req_friendly_name=PolarisPostActionLoadPostQueryQuery`，`doc_id=8845758582119845`，公开 Web App ID `936619743392459`），解析 `xdt_shortcode_media` / `shortcode_media`。
   - 原生支持单视频（`GraphVideo` / `XDTGraphVideo`）、单图（`GraphImage`）与多图/视频混排轮播图集（`GraphSidecar` -> `media.type=album`），全部走 SSRF 防护与 DNS 绑定的 `fetch.Executor`，并保持确定性 `private_source_identity` 以支持签名续期（`/api/resolve`）。本地信号量饱和（`LocalBusy`）时仍立即返回 `RATE_LIMITED`，不触发回退风暴。

2. **借鉴点 2｜`yt-dlp` Python Zipapp 零解压启动、启动信号量与陈旧临时目录清理**
   - `internal/ytengine` 自动识别 SHA-256 校验通过的 `yt-dlp` 文件是否为官方 Python Zipapp（头部 `#!...python` + 内嵌 ZIP 归档），若主机 `PATH` 存在可执行 `python3`，则通过 `python3 <zipapp>` 直接运行，避免 PyInstaller 单文件二进制每次调用向 `/tmp/_MEIxxxxxx` 解压约 30MB Python 运行时，同时兼容 `0644` 只读权限文件。
   - 若部署环境使用 PyInstaller 单文件二进制，新增 `bootSem`（默认并发上限 3）限制同时解压启动的子进程峰值，并在引擎初始化时执行 `CleanupStaleTempDirs` 自动清理超过 `maxAge` 的残留 `dokitool-ytdlp-*` 与 `_MEI*` 临时目录。

3. **借鉴点 3｜YouTube `--extractor-args` 单参数合并与匿名 `player_client` 劣化/错误级联恢复**
   - `MergeYouTubeExtractorArgs` / `MergeExtractorArgs` 将同一命名空间的多个子项（如 `youtube:player_client=...` 与 `youtube:player_skip=...`）合并为单个 `;` 分隔的 `--extractor-args` 参数，防止 `yt-dlp` 后置同名参数覆盖前置配置。
   - `ExtractYouTubeWithCascade` 在同一并发槽位（`e.sem`）与同一本地出站代理门禁（`outboundGate`）内执行匿名客户端级联：
     - 首轮使用默认参数；
     - 若遇到可恢复的 YouTube 播放器/签名/SABR 报错（`nsig`、`Signature extraction failed`、`sabr`、`Requested format is not available` 等），或返回结果劣化为仅 `<=360p` 的 `googlevideo.com` 渐进流（`IsDegradedYouTube360p`），自动依次尝试匿名备选客户端组合 `tv_downgraded,ios,android,web_safari` 与 `ios,mweb`；
     - 若备选客户端成功产出更高清格式则自动升级返回；若备选客户端失败但首轮已拿到合法 360p 结果，则保底返回首轮 360p 结果，绝不把已成功的解析降级为报错。

---

## 浏览器插件 / 拓展 / 用户脚本辅助前端零服务器流量合流方案（RFC 草案，仅供审查）

> **状态说明**：本节仅作为架构设计提案（RFC）提交审查，**当前 PR 不包含任何前端/后端合流代码或浏览器插件代码实现**。DokiTool 核心服务继续严格保持“纯匿名解析、零服务器媒体中继、零服务端 ffmpeg 转码/合流”。

### 1. 背景与核心痛点

YouTube 的 1080p / 2K / 4K 高清画质均采用 DASH 音画分离流（Video-only `avc1`/`vp9`/`av01` + Audio-only `mp4a`/`opus`），单文件直链（`itag=18`）最高仅 360p。要在**不消耗 DokiTool 服务器带宽、不在服务器运行 `ffmpeg`** 的前提下让用户下载合流后的 1080p/4K MP4，纯网页前端存在两道浏览器安全与平台风控边界：

1. **跨域读取拦截（CORS）**：`*.googlevideo.com` 对普通网页 `fetch()` 请求不返回允许 `https://snap.dokitool.com` 读取字节流的 CORS 响应头，纯网页 Worker 无法直接拉取音视频分片字节。
2. **出口 IP 与客户端指纹绑定（`sparams=ip`）**：当服务端 `yt-dlp` 提取出的 `googlevideo.com` URL 带有 `ip=<server_ip>` 签名绑定时，用户浏览器（异网 IP）直接请求该 URL 会触发 `HTTP 403 Forbidden`。

通过引入**可选的轻量级浏览器伴侣插件（Manifest V3 Extension）或用户脚本（Tampermonkey / Violentmonkey Userscript）**，可以在完全不经过 DokiTool 服务器中继流量的前提下，同时打通上述两道边界，并在浏览器本地完成零转码容器重封装（Remux）。

### 2. 总体三层架构（零服务器流量）

```text
+-----------------------------------------------------------------------------------+
| 用户浏览器本地环境 (User Browser - 100% 消耗用户本地网络与本地算力)               |
|                                                                                   |
|  +---------------------------+      postMessage /       +----------------------+  |
|  | DokiTool 前端主页面       | <----------------------> | 浏览器拓展 / 用户脚本|  |
|  | (snap.dokitool.com)       |   MessageChannel Port    | (Bridge Companion)   |  |
|  +-------------+-------------+                          +----------+-----------+  |
|                |                                                   |              |
|                | Transferable ReadableStream / ArrayBuffer Chunks  |              |
|                v                                                   |              |
|  +---------------------------+                                     |              |
|  | 专用合流 Web Worker       |                                     |              |
|  | - OPFS 临时分片缓存       |                                     |              |
|  | - mp4box.js / WebCodecs   |                                     |              |
|  |   fMP4 零转码容器重封装   |                                     |              |
|  +-------------+-------------+                                     |              |
|                |                                                   |              |
|                v                                                   v              |
|       触发本地 Blob 下载                             直接以用户本地出口 IP 拉取   |
|   (1080p/4K 有声完整 MP4)                            *.googlevideo.com 音视频分片 |
+-----------------------------------------------------------------------------------+
```

### 3. 两种伴侣工作模式（解决 IP 绑定与 CORS）

伴侣插件/用户脚本具备宿主机浏览器的特权网络 API（Extension `host_permissions` 或 Userscript `@connect googlevideo.com` + `@connect www.youtube.com`），支持按以下两级模式获取音视频流：

- **模式 A（优先｜客户端本地同 IP 提取 + 跨域分片拉流，彻底规避异网 `403`）**
  - 当用户在 DokiTool 解析 YouTube 链接且检测到已安装伴侣插件/脚本时，前端可指令伴侣脚本直接在用户浏览器侧请求 YouTube Player API（使用用户本机出口 IP，不上传任何数据到 DokiTool 服务器），或对服务端返回但触发 `403` 的条目在本地刷新签名直链。
  - 因为提取与下载完全共享用户同一个家庭/办公宽带出口 IP，`sparams=ip` 校验天然 100% 匹配，从根源消除跨国/异网 `403`。
- **模式 B（回退｜仅特权跨域分片桥接）**
  - 对于未绑定严格异网 IP 或同区域可访问的音视频分离直链（`video_url` + `audio_url`），伴侣插件/脚本利用 `GM_xmlhttpRequest({ responseType: "arraybuffer", headers: { Range: "bytes=..." } })` 或 MV3 Service Worker `fetch()` 绕过网页 CORS 限制，按固定分片（例如 `4 MiB` Range 请求）拉取字节并通过 `MessagePort` 零拷贝（`Transferable` `ArrayBuffer`）流式传输给 DokiTool 前端合流 Worker。

### 4. 前端流式无损合流管线（复用现有 Worker + OPFS 基建）

DokiTool 前端目前已具备成熟的 `download.worker.ts` 与 Origin Private File System（OPFS）流式写入管线。合流扩展无需引入体积庞大（~25MB）的 `ffmpeg.wasm` 软编解码器，而是采用**纯容器重封装（Container Remuxing，等价于 `ffmpeg -c copy`）**：

1. **编码筛选**：优先选择 MP4 容器原生兼容轨：
   - 视频轨：`avc1`（H.264，最高 1080p）或 `vp09` / `av01`（2K/4K，现代 MP4 标准与主流播放器均原生支持 MP4 封装的 VP9/AV1）；
   - 音频轨：`mp4a.40.2`（AAC，`itag=140`）。
2. **双流并发分片拉取与 OPFS 落盘**：
   - Worker 通过伴侣 `MessagePort` 并行拉取音频流（通常 3–15 MB）和视频流（按 `Range: bytes=start-end` 分块），实时汇报双轨总字节进度（`loadedBytes / (videoClen + audioClen)`），避免大文件撑爆 JS 堆内存。
3. **流式 fMP4 / Faststart MP4 重封装**：
   - 使用轻量纯 JS MP4 解析/封装库（如 `mp4box.js` 或定制 ISO BMFF box 拼接器，仅 ~50KB gzip），解析两路 DASH Fragmented MP4（`init` 段 `ftyp+moov` 与媒体段 `moof+mdat`），将 Video `trak`（`track_id=1`）与 Audio `trak`（`track_id=2`）的时间戳对齐交织写入 OPFS 目标文件句柄。
   - 全程**零视频重编码、零 CPU 算力瓶颈**，100MB 1080p 视频在普通笔记本/手机浏览器中合流仅需数百毫秒的磁盘 I/O 时间。
4. **完整性校验与交付**：
   - 严格校验拉取字节数与上游 `Content-Length` / `clen` 一致、MP4 `moov`/`mdat` 结构完整后，才从 OPFS 生成 `File` / `Blob` 触发浏览器原生保存；任一分片中断、403 或校验不通过均立即中止并清理 OPFS 临时文件，绝不向用户交付残缺或静音文件。

### 5. 安全边界与产品降级契约

1. **零服务器负担与零隐私收集**：
   - DokiTool 服务端不新增任何媒体代理接口，服务器带宽消耗恒为 0；
   - 插件/脚本不读取、不上传用户任何平台 Cookie 到 DokiTool 服务器。
2. **严格的插件通信白名单**：
   - 插件/用户脚本仅在匹配白名单域名（`https://snap.dokitool.com/*` 与本地开发域名）时注入通信桥，且允许请求的目标域名严格锁定为白名单媒体 CDN（如 `*.googlevideo.com`），拒绝任意 URL 代理请求，防止插件被第三方网页滥用为通用跨域跳板。
3. **未安装插件时的平滑体验（渐进增强）**：
   - **未安装插件/脚本的普通匿名用户**：维持现有稳定行为（默认选中带声单文件直链，或在隔离新标签页打开原生流，同时在 1080p/4K 仅视频选项旁清晰提示“高清音画合流需启用浏览器伴侣脚本”）；
   - **已安装插件/脚本的用户**：前端通过握手事件（`DOKITOOL_COMPANION_PING` -> `DOKITOOL_COMPANION_PONG`）自动解锁“1080p / 4K 音画合流直下（本地零损封装）”按钮，一键完成高清有声 MP4 下载。

