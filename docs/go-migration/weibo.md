# 微博 Go 迁移：阶段性证据和剩余工作

## 范围

PHP 源码固定在 `25beaee107f8c1a208ced9deb3c637068445e316`。这次只移植微博二进制转发前置的单段 Range 解析与上游响应头验证，不开放公开的 `/api/media/weibo`，该路由仍明确返回 503；尚不能替代 PHP 的完整传输路径。

## 本轮固定输入对拍

- `scripts/go-migration/produce-weibo-media.php` 直接调用原 PHP 的 `snapWeiboMediaRange`、`snapWeiboMediaResponsePlan`，不调用 Go，不模拟媒体下载。
- 33 组原始输入/输出（14 Range、19 ResponsePlan）保存在 `backend/internal/media/weibo/testdata/php-media-baseline.json.gz`。可以 `gzip -dc` 读取原始 JSON；压缩只减少版本库体积，Go 测试完整比较所有 JSON 事实。
- CI 使用冻结 PHP 8.3.6 官方镜像 digest、核对产品源与固定提交相同，并复现 JSON 与入库原件逐字节对比；不允许把 PHP 8.4 预览结果作为正式 8.3.6 通过证据。
- Go 的 `TestPHPWeiboMediaProtocol` 从这个 PHP JSON 读取 expected，比较完整字段和错误文本；本地隔离预览不能代表仓库最终 CI。

## 仍须实现

微博 status / TV component 上游解析、匿名 session、主备来源和信息映射、统一 API/resolve、签名 URL 公开投影、完整受控媒体流式服务（GET/HEAD/单 Range、8 字节 ftyp、416、预算、取消、错误格式）。实现流式 transport 时不得复用只能读取 8 MiB 全 body 的普通元信息 `fetch.Executor`，需提供独立的受控流接口和严格原始响应头检查。现有 `main.go` 50 秒 `WriteTimeout` 与 PHP 300 秒媒体预算存在冲突，正式接入前必须解决并测试媒体服务超时/关停边界。无真实上游或浏览器保存证据。

## 独立请求准备对拍（本轮）

新增 `prepare.go`，在任何 DNS 请求之前依次验证方法、签名密钥、唯一 ticket、过期时间、GET 单 Range；HEAD 忽略 Range。DNS 经单次解析后逐条验证公网 IP，任何一个不合规就拒绝，合规集合 IPv4 优先；下一阶段的实际媒体传输必须消费选定 IP 并固定连接，不可重新解析。

另有 21 组固定 PHP `snapWeiboMediaPrepare` 原产品输入/期望，含无效/过期票据、DNS 混合公共/私网、IPv4/IPv6、HEAD、单 Range、405/403/416/502 等。输入在 `scripts/go-migration/produce-weibo-prepare.php`，独立预期在 `backend/internal/media/weibo/testdata/php-prepare-baseline.json`，固定 PHP 8.3.6 CI 会重新执行原函数并逐字节核对；Go 的 `TestPHPWeiboMediaPreparation` 完整对拍。无真实 DNS、无下载、无开放 relay。

这一阶段添加的是 **Prepare 前置验证**，不是完整的媒体传输。尚须完成固定来源的实际 HTTP stream、首 8 字节 ftyp 校验、完整 GET/HEAD/Range、上游关闭/取消和正式路由。PR CI 结果需以此后提交的真实运行记录为准。

## 新阶段：受控媒体二进制传输路径

新增 `internal/fetch/weibo_media.go` 专用、无重定向/无代理的流式连接，仅连接 Prepare 已审查的单个公网 IP，保留域名用于 TLS SNI/CA 校验；使用 HTTP/1.1 原始头部检查拒绝重复关键字段或未知/分块长度，避免普通元信息 fetch 的 8 MiB Body 限额干扰大型媒体。`internal/media/weibo/relay.go` 加入 GET/HEAD/单 Range 路由、200/206/416、首 8 字节 ftyp 审查、最大 1GiB、有限缓冲、并发槽位与响应后禁止附加 JSON。HEAD 使用内部 GET 0-7 Probe，客户端仍收到零字节的 200 响应。Go 验证服务保持普通 API WriteTimeout 50 秒，并为媒体请求单独设置 310 秒写期限；Shutdown 容忍 310 秒排空。微博媒体使用已有 `SNAP_MEDIA_SIGNING_KEY`，无效或缺少时返回原 PHP legacy 媒体 503，不触发网络访问。

有界合成 HTTP 测试覆盖媒体请求与返回格式、拒绝恶意票据/目标、无效 ftyp、上游部分内容、HEAD、416、传输截断后的“禁止追加 JSON”。这些是 Go 受控连接/流式协议测试，不是 PHP 完整 `snapWeiboMediaStream` 回调的逐字节 oracle，更不是平台在线、真实浏览器保存或可上线证明。真实微博解析、签名来源公开投影、Parse/Resolve 仍未移植，媒体回传必须等待后续联调和进一步传输条件核对。

## 微博 URL 身份解析：47 组独立 PHP oracle

从已发布 `25beaee` 的 `weiboTarget` 原产品函数新增固定输入生成器（包含 TV fid、status、parse_str 重复键与数组、主机与端口拒绝、非字符串 URL 等）。Go `internal/platform/weibo/target.go` 只识别身份，不执行任意 URL 请求。实际已由 [固定 PHP 8.3.6 CI](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41230458238) 原函数生成 47 组 expected；原始 JSON 已逐字节冻结入库，Go 测试逐条比较完整 kind/id/null，CI 每轮从同一 PHP 产品路径重新生成并严格 cmp。尚未注册微博 adapter，也不据此宣称解析链路完成。

## 2026-11-01：微博访客/接口错误/JSONP 严格语义（本次增量）

新增 Go `internal/platform/weibo/session_protocol.go`，用于微博解析器的上游传输失败分类、Sina Visitor System 识别及安全的 JSONP 数据解码。使用独立的 51 例输入/输出集合，直接由固定 PHP `weiboTransportFailure()`、`weiboIsVisitor()`、`weiboJsonp()` 产品函数产生；独立 PHP 8.3.6 容器每次重新生成并与冻结输出逐字节比较，Go 单测对原输出逐项比较。输入和基线分别压缩入库，便于审查与继续扩展。

这一阶段尚未注册完整微博平台 adapter，也未发起匿名游客会话或实现 status/TV component 的请求与 formatter；此前二进制 relay 不变。当前提交的 CI 结果须单独核实，不能沿用此前绿色结果。无真实平台调用、浏览器下载或生产切换。

## 2026-11-01：微博访客请求编排（PHP/Go 固定对拍阶段）

- 新增 Go `internal/platform/weibo/guest_request.go`：实现可注入的 `weiboJsonRequest` / `weiboStartGuest` 控制流程，包括首次挑战、单次访客初始化、同一目标重试、HTTP 失败与异常 JSON 不额外重试、上下文取消不调用请求器。访客调用仅使用原 PHP 两个固定 passport 端点；这仍是 parser 的内部组件，并未接入真实 HTTP/Cookie jar 或正式 Parse/Resolve。
- 28 组独立输入覆盖 status/TV 目标、成功 JSON（包括空对象/数组和大整数 ID）、不可解析数据、403/429/500、首次/重复 challenge、visitor gen/incarnate 失败与成功、一次重试、原始 POST/referrer/URL 请求序列。生成器 `scripts/go-migration/produce-weibo-request.php` 真正调用已发布 PHP `25beaee` 的 `weiboJsonRequest` 与 `weiboStartGuest`；仅将随机的 `_rand` cache-buster 值统一为 `<nonce>`，保留请求其余字段和顺序。输入、预期分别压缩入库，Go `TestPHPWeiboGuestRequestFlow` 完整字段比较；取消执行另有 Go 测试。
- CI 需要固定 PHP 8.3.6，在无网络只读容器中验证 PHP 源哈希并重新生成 28 组预期，解压后逐字节 `cmp`；新增 Go 测试纳入原本的 race/全包/格式、Schema、前端消费和构建门禁。
- 尚未实现真正的微博 cookie jar、受控上游 status/TV parser/adapter、媒体候选及签名公开投影。真实微博/CDN、浏览器保存与生产测试仍待验收。当前阶段结果必须以对应最终 Head CI 为准，不能把创建 PR 或触发 CI 算作成功。
