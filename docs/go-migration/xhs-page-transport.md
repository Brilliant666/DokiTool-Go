# 小红书页面传输专项（PR #5）

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

## 后续真实环境验收

复用 Grok 已保存的源代码、依赖、成功 HTML、媒体探测结果与哈希；不要为补摘要再次下载 bd/qn 两份原画。对同一保存 HTML先检验原 parser 兼容，再在获准环境用正式 Go 服务的显式开关检验页面获取。图集、视频、Live 分开记录；截图/图片/视频成功不替代 Live。

如果真实入口仍受阻，记录精确 HTTP/页面阶段及构建/profile，不归因 token 过期，不循环 profile 或换网络。新传输工作不能关闭已有跨调用显式 source ID 漂移、其他平台稳定性、旧生产恢复和回滚门槛。代表性原前端播放/文件保存另列，不能以本地合成 TLS 或 Grok 独立下载替代。
