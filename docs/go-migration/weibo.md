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
