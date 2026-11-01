# 小红书页面传输专项（PR #5）

## 2026-11-01 正式在线验收

北京时间 12:15–12:24，使用 Go 1.27.3 构建的正式服务，经 loopback 请求 `/api/parse` 和 `/api/resolve`。初测版本为 `115049540348afc197e30516fa886f8489e61b76`，修后实际运行的版本为 `c3be6503c7ea14ce14aa1a03d317b72815e436ec`。两种页面模式使用同一构建，保留输入的原始查询串。13 份 HTTP 响应均通过独立 Schema 与语义验证，其中包含修前的 3 份 `404 NO_MEDIA`。每份响应的版本、时刻、状态和条目数见[脱敏机器报告](xhs-page-online-checks.json)。

| 样本 | 正式接口 | 原前端和 Edge |
| --- | --- | --- |
| 普通四图 | 修前两种模式均为 `NO_MEDIA`。修后均返回 4 张图，封面对应首图，resolve 第四项成功。每张有界读取 2,048 字节，均为 206 JPEG。图片顺序与身份保留，重新签发的 URL 有变化。 | 4 张均已解码并按序显示。首图点击一次，Worker 收到 133,563 B，任务声明总长 156,177 B，报“下载长度与来源声明不一致”，没有完整落盘。该次实际响应的线上 Content-Encoding 未捕获。 |
| 视频兼容流 | 初测 standard 取得 720×1280 H.264。chrome131 同时保留该流和原画，默认原画。修后 parse 与 resolve 的 2 个候选、7 个来源及本次来源 URL 保持一致。 | 显式切换至兼容流后，实际播放器时间推进 2.155 秒，无错误。 |
| 视频原画 | rednote 的目标视频页面包含 origin key。原标准媒体执行器对 qn 做 3 次有界读取，共 86,520 B，识别 QuickTime `qt  `、2160×3840 HEVC、60 fps 和音轨，总长 70,759,716 B。CDN 声明 `video/mp4`，API 为 `video/quicktime`、`mov`。 | 初测原画短播推进 0.606 秒。修后原前端经 `browser_streaming` 从所选 qn 来源一次完整保存，页面与 Worker 均完成。文件为 70,759,716 B，全片解码通过，13.00 秒，HEVC、AAC LC 双声道。实际落盘后缀为 `.mp4`，Worker 按 CDN 的 MIME 重新命名。 |
| 独立 Live 样本 | 修前为 `NO_MEDIA`。修后 6 组静图和动态资源保留配对，resolve 成功。组合项返回 `default=null`、`processing_required`。 | 第一组 1080×1920 动态短播推进 0.781 秒，无错误，同组静态封面显示正常。未执行 Live 文件保存。 |

原画文件的本地 SHA-256 为 `d8a6bc32058cea5687092d45770a729c22aab5ed847fb0a4599623aba94e837b`。Grok 只提供历史汇总，本机未找到该探针原件或同一文件的参考哈希。两项短视频播放来自初测构建，原画完整保存、图集与 Live 展示来自修后构建。正式 API、单独的页面阶段观察、同字节重放和浏览器任务都有独立回执。

**修复依据。** 图集和 Live 的目标页面均已取得。其 `imageList` 明确给出空字符串 `url` 及可用的 `urlDefault` 或 `urlPre`，原 formatter 采用空值后丢弃条目。沿用旧地址改写的四个图集来源均返回 403，使用页面声明的地址、仅将 HTTP 升为 HTTPS 后均取得 JPEG。修复仅处理主地址为空字符串的这种输入，按声明优先级保留地址、查询串和顺序。未调整媒体探测边界、来源优先级或公共身份规则。

相同页面字节、相同时钟和 Range 观察下，冻结 PHP 8.3.6（`25beaee`）与修后 Go 的视频提取、普通及 debug 输出逐字段相同。图集和 Live 在未改的 PHP 中仍为 `NO_MEDIA`。另以页面声明的图片来源作为独立输入，经过冻结 PHP 的适配、默认选择和公开策略后，与修后 Go 三层结果的差异均为零。两个对比的输入和用途分别记录。原 PHP fixture 及 10 项既有投影差异保持原样。

另一次有界页面观察中，chrome131 对三条样本的 rednote 路径均取得 301 → 200 且找到目标笔记。standard 对视频取得 rednote 302 → 200，后者无目标笔记，随后从主站取得普通流。这次观察不足以归因于唯一 TLS 参数。浏览器交付仍有两个独立问题，分别为单图保存长度不一致，以及 MOV 落盘后缀跟随 CDN MIME。

## 历史检查点：受控传输与完整回归

产品代码与依赖锁检查点 **`50fa63ca15d220154ec5a536e8f2aa9d4cb1b524`**，[Actions #41270778447](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41270778447) 三个 job 均 **completed/success**：`bilibili-php-oracle`、`offline-contract`、`xhs-dependency-review`。下面的结果是固定运行环境本轮执行，不沿用历史通过数；不是 XHS 真实在线或浏览器验收。

| 检查 | 本轮结果 |
| --- | --- |
| 固定 Go 全包 / race / vet / gofmt / build | **4116** 叶场景、4598 含父级通过事件；全部通过，计数不相加 |
| 原 PHP 页面 oracle 经真实 TLS、HTTP/1.1 与 HTTP/2、正式 Go API | **25** 旧输入、**236** parse/resolve/缺失目标响应逐字段通过 |
| 独立 Schema＋语义 | **1230/1230**，包括全部原输出和新增传输输出 |
| 原前端完整消费 | 原九平台 **918/918**；以实际 H2 页面 Parse 替换其中 50 份后再跑 **918/918**，非 1836 个独立样本 |
| 原默认／显式续签 | 两轮各保留抖音 **4/4**、微博 **4/4** |
| 既有公共投影差异 | 原 **10** 项继续精确消费，不扩宽 |
| 固定 PHP / 来源 / 入库清单 / 模块锁 | 全部通过；513 个文件，`go.mod/go.sum` 与 Go 1.27.3 重新 tidy 输出一致 |

原始完整机器报告位于该运行 artifact **13068714670**，内容 SHA-256 **`ef4f99ac43804f570b09e29197f89f6492a248269762283ac36c709f23e2c679`**。下方机器回执从其实际字段和 job 结果抽取，不冒充原报告逐字节副本；原始 JSON 另作为交付附件提供。原九平台 `nine-platform-checks.json` 未覆盖。

首次实现 `9a9f102` 的 [CI #41270532789](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41270532789) 已通过 PHP、真实 TLS/API 和完整 Go/前端门禁，但独立模块锁 `cmp` 失败：`go get` 的锁未包含 tidy 增加的依赖测试摘要，且 require 分组不同。后续只采用同一固定 Go 1.27.3 在临时副本生成的 `go.mod/go.sum`，没有升级依赖版本、改产品或减弱检查。`50fa63c` 三个 job 完整重跑成功，机器报告与首次代码运行逐字节一致。

静态范围复核确认运行时代码只修改 CLI 接线并新增 `fetch/xhs_pages.go`。PHP、前端产品、schema、平台 formatter、旧基线输入/expected/生产器全部不变。本文收尾提交只归档已执行证据；最终分支 Head 的状态另以 PR 和相应 CI 为准。

## 授权、分支与证据层次

维护者在提供 Grok bot 的 `RESULT.md` 后授权继续实施。本专项从九平台迁移 PR #4 的 `f3650a07d289a90dda44c0ebc36f3032bda58256` 接出 `codex/xhs-page-transport`，PR #5 以原迁移分支为 base。原任务对新增 TLS 传输的排除仍适用于原 PR #4；这里是独立增量授权，不追溯改写旧验收。未合并、未部署，不修改公开 PHP 仓库与生产。

用户材料是第三方探针的汇总，不是本 PR 的执行证据。报告记载图集和视频页面可由 Chrome_131 配合有效分享 token 取得；同一视频在 rednote 页面提供 origin key，而主站页面仅提供普通转码流。原画文件据报告为 70,759,716 字节、2160×3840 HEVC/60 fps、QuickTime，并列出 bd/qn 下载成功。报告没有附探针源码、完整请求头、原始 HTML、ffprobe JSON 或具体 SHA-256，因此不宣称本实现逐项复制了探针，不宣称独立复核下载。报告中的真实 token、笔记 ID、origin key 与媒体不进入公开库或 CI。

这些观察支持页面传输兼容的实施方向，但不证明 TLS 是唯一变量，也不证明所有出口/笔记有效。未改 IP/代理，没有登录 Cookie、验证码自动化、动态 profile 搜索或循环换源。遇到登录/验证/限流仍返回原业务失败，不升级成通用浏览器自动化。

## 实现与使用

默认维持标准传输。测试环境显式开启：

```sh
cd backend
go build -o /tmp/dokitool-go ./cmd/dokitool
/tmp/dokitool-go serve -listen 127.0.0.1:9082 -xhs-page-transport chrome131
```

`standard` 与 `chrome131` 是仅有选项，未知选项在启动 listener 前拒绝；不允许公开 API 参数提供任意 profile、代理、Cookie、CA 或请求头。

`fetch.NewXiaohongshuPages()` 保留原 `Client.Do`，只为用途 `xiaohongshu-page` 的获准 HTTPS GET 更换单跳传输。原明文短链 GET 仍使用标准受控执行器。每一跳继续由 parser 检查 Location，再由共享层检查目标、DNS、公网地址和预算。原 parser 已 rednote 优先，原画/多 CDN/Live/MOV 提取无需重写。

XHS 媒体探测与所有非页面用途仍为 `standardRoundTrip`；严格媒体头、Range、连接绑定与探测预算不经 fhttp。其他八个平台不变，普通媒体仍由浏览器直连 CDN，微博仍走其专用 relay。未改变 CDN 顺序 qn/al/qc/bd、身份算法、default、四项能力保护、普通/debug、PHP oracle、前端产品、Worker、bridge 或 Schema。

## 依赖和传输审查

固定直接依赖 `tls-client v1.18.0`、`fhttp v0.7.3`；完整模块及校验值在 `backend/go.mod`、`backend/go.sum`，由固定 Go 1.27.3 的独立运行取得，CI 在临时目录重新 tidy 并逐字节比较，不由人工编造和静默回写。工具链仍是原 Go 1.27.3。

- 明确选择 `Chrome_131`，不用库的默认 profile；匹配 Chrome 131 UA 与有限导航头。未取得探针原始请求头，因此这些是经审查的产品配置，不声称与 Grok 探针完全相同。
- 无 Cookie jar、无自动重定向、无环境代理、无 HTTP/3/QUIC、无调试响应日志；压缩协商仅 gzip/deflate，由原共享层限制 wire 与解压大小。
- 自定义 dial 只接受原始主机的 443 连接请求，然后只连接已经验证的数值 IP。TLS SNI/证书名仍为原主机；不关闭校验，不把 URL 替换成 IP，不二次域名解析。
- 原请求 context 和 deadline 绑定真实 socket，包括库中可能脱离请求 context 的 HTTP/2 重连。每跳独立 client，取消/错误/正文关闭时回收连接；迟到的 dial 不能逃过已经完成的清理。
- **HTTP/2 上限单独处理**：库的 HTTP/1 `MaxResponseHeaderBytes` 不能替代 H2 profile 设置。Chrome_131 原 profile 的 header-list 设置为 256 KiB；本实现复制 profile 后收紧到本请求上限（至多 64 KiB），不改全局 profile。因此 ClientHello 选 Chrome131，但 H2 头限制有明确的安全差异，不能称完整浏览器指纹完全相同。须以目标环境复验决定兼容性。
- 返回的库 `url.Error` 去除含签名 URL 的外层，只保留根因；页面原始正文不写日志。

依赖不是本仓库 MIT 文本的自动再许可。tls-client 和其 utls 模块自带 BSD 许可；本次 module archive 的 fhttp 没有根 LICENSE，Go 派生源文件仍有原 BSD 声明。本 PR 不 vendor 第三方源码、不发布二进制；未来分发前需要完成完整传递依赖许可/NOTICE 收集，不把当前模块来源审查等同于发布许可证审计。

## 执行验证

新的测试只在 test binary 中创建受控 CA 和 loopback 映射，产品不暴露这两个配置口。它们运行真实 tls-client/fhttp 与 TLS、HTTP/1.1、HTTP/2，而不是用 mock executor 假装握手成功。覆盖 token query 原字节、SNI/证书拒绝、代理环境隔离、无 Cookie/自动跳转、用途分离、混合私网 DNS、wire/解压/响应头限额、取消和连接回收、全局 profile 不变。

页面型旧 PHP 场景经真实受控 TLS 服务器、现有 XHS parser 与正式 `/api/parse`/`/api/resolve` 返回，逐字段对拍**既有未改 PHP 8.3.6 oracle**。主门禁额外导出这些实际 HTTP 响应，用独立 Python 比较和原 Schema/语义校验器检查；保留原完整前端门禁，再用真实 H2 页面响应替换相应测试输入重跑同一个未修改的前端消费者。不是 Go 生成 expected，也没有减少原九平台覆盖。

实际通过数、固定工具链执行、失败及修复以本 PR 的最终 Head CI 与机器报告为准。没有真实 XHS 页面请求、原画重新下载、浏览器保存或生产发布的本轮声明。

## 历史检查点的在线验收计划

复用 Grok 已保存的源代码、依赖、成功 HTML、媒体探测结果与哈希；不要为补摘要再次下载 bd/qn 两份原画。对同一保存 HTML先检验原 parser 兼容，再在获准环境用正式 Go 服务的显式开关检验页面获取。图集、视频、Live 分开记录；截图/图片/视频成功不替代 Live。

如果真实入口仍受阻，记录精确 HTTP/页面阶段及构建/profile，不归因 token 过期，不循环 profile 或换网络。新传输工作不能关闭已有跨调用显式 source ID 漂移、其他平台稳定性、旧生产恢复和回滚门槛。代表性原前端播放/文件保存另列，不能以本地合成 TLS 或 Grok 独立下载替代。

## 机器回执（本轮摘要）

```json
{
  "scope": "XHS page-only Chrome131 implementation and controlled offline acceptance; not online acceptance",
  "code_head": "50fa63ca15d220154ec5a536e8f2aa9d4cb1b524",
  "base_head": "f3650a07d289a90dda44c0ebc36f3032bda58256",
  "ci_run": 41270778447,
  "ci_jobs": {
    "bilibili-php-oracle": {
      "id": 124742926015,
      "conclusion": "success"
    },
    "offline-contract": {
      "id": 124742926171,
      "conclusion": "success"
    },
    "xhs-dependency-review": {
      "id": 124742926190,
      "conclusion": "success"
    }
  },
  "source_report_artifact": 13068714670,
  "source_report_sha256": "ef4f99ac43804f570b09e29197f89f6492a248269762283ac36c709f23e2c679",
  "passed": true,
  "toolchain": "go version go1.27.3 linux/amd64",
  "php_runtime": "8.3.6",
  "php_baseline": "25beaee107f8c1a208ced9deb3c637068445e316",
  "dependencies": {
    "tls-client": "v1.18.0",
    "fhttp": "v0.7.3",
    "module_lock_tidy_exact": true
  },
  "xhs_page_transport": {
    "passed": true,
    "frozen_page_cases": 25,
    "http_responses": 236,
    "protocols": [
      "HTTP/1.1 over TLS",
      "HTTP/2 over TLS"
    ],
    "parse_envelopes_for_frontend": 50,
    "evidence": "Controlled local TLS servers with original PHP response tapes, not online platform access"
  },
  "go": {
    "leaf_scenarios": 4116,
    "passed_test_events": 4598,
    "race": true,
    "vet": true
  },
  "independent_responses": {
    "total": 1230,
    "passed": 1230,
    "failures": []
  },
  "frontend_original": {
    "passed": true,
    "responses": 918,
    "valid_successes": 584,
    "expected_errors": 334,
    "unchanged_renewal_tests": 4,
    "weibo_signed_renewal_tests": 4,
    "rejected_successes": 0
  },
  "frontend_with_actual_h2_pages": {
    "passed": true,
    "responses": 918,
    "valid_successes": 584,
    "expected_errors": 334,
    "unchanged_renewal_tests": 4,
    "weibo_signed_renewal_tests": 4,
    "rejected_successes": 0
  },
  "consumed_existing_public_differences": 10,
  "source_inventory_files": 513,
  "product_php_frontend_schema_oracles_unchanged": true,
  "online_platform_requests": false,
  "browser_save": false,
  "production_changed": false,
  "default_mode": "standard",
  "opt_in_mode": "chrome131"
}
```
