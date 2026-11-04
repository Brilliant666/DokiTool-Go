# 抖音原画取址（可选）

日本后端先读取作品 detail，用其中的媒体 video_id 请求上海服务。上海从固定的官方 play 入口（ratio=default、line=0）探测音视频轨、大小、时长、像素和码率。最多调度两次，共享 6 秒、1 MiB 和 12 次上游 HTTP 请求的预算。首段指纹用于关联有限探测结果；完整文件相等须用实际文件核对。

日本端验证视频关联、公开 CDN 地址、探测数据和作品已有转码。在同一原画的 delivery.sources 保留合格路线，沿用服务端 default。成功时下载由浏览器直接访问 CDN。无合格上海候选或两机通信失败时，调用原日本探针。普通转码继续按实际规格展示；显式来源已消失须重新选择。

## 构建和试运行

在 backend 目录，使用 go.mod 指定的工具链：

~~~sh
go build -trimpath -o shanghai-resolver ./cmd/shanghai-resolver
go build -trimpath -o dokitool ./cmd/dokitool
~~~

凭据使用两端专用的受限文件或服务管理器的 secret 注入。以下 Linux 示例假设已配置相应文件路径、匹配入口主机的 TLS 证书及获准的回环转发或受来源 IP 限制的 HTTPS 入口。不要开启 shell 跟踪。

上海端：

~~~sh
export DOKITOOL_SHANGHAI_TOKEN="$(cat "$RESOLVER_TOKEN_FILE")"
./shanghai-resolver -listen 127.0.0.1:9083 -tls-cert "$RESOLVER_TLS_CERT" -tls-key "$RESOLVER_TLS_KEY"
~~~

日本端（独立进程）：

~~~sh
export DOKITOOL_SHANGHAI_TOKEN="$(cat "$RESOLVER_TOKEN_FILE")"
export DOKITOOL_SHANGHAI_ENDPOINT="https://127.0.0.1:9083/v1/douyin/original"
./dokitool serve -listen 127.0.0.1:9082
~~~

回环示例要求日本本机该端口已安全连接上海服务，证书含该 IP 的 SAN。公网上使用配置的 HTTPS 域名和标准 443 端口，限制日本来源 IP。证书必须受日本进程信任。测试可只对该进程设置 SSL_CERT_FILE，包含现有公共 CA 和临时测试 CA。控制面要求 Bearer token（32–256 个可打印非空格 ASCII 字符），只接受固定路径的 POST 和单个 video_id。监控访问只记录状态和数量，媒体 URL 与认证头需排除。

## 停用和回退

停止隔离进程使用 SIGTERM 或前台 Ctrl+C。日本端移除 DOKITOOL_SHANGHAI_ENDPOINT 和 DOKITOOL_SHANGHAI_TOKEN，按既有流程重新启动该后端即恢复原日本取址。浏览器已经发起的媒体下载直接连接 CDN。上海隔离端口和临时凭据可在测试结束后撤销。正式启用前需独立确认两机长期入口、证书、来源限制、凭据与晚高峰用户验收。

## 上海主动领取（开发验收）

日本端提供两个精确的 POST 路径。上海节点预先等待 /internal/shanghai/claim；没有等待者时，解析立即回到日本原路径。日本最多等待领取任务 15 秒，空闲结束返回 204；收到任务返回 200。任务绑定 video_id、随机 task_id、独立 attempt_id 和绝对 deadline_ms。控制面沿用 #27 的 8 秒总截止与用户更短的期限。已交付的任务不重新发放。上海用 /internal/shanghai/result 在原期限内回交 #27 的探测响应，探测失败回交 null。每类最多 4 个等待领取、4 个未结任务、4 个并发回交。已有回执、超时、取消及身份不匹配返回 409，不能替换本轮结果。进程重启后内存中的任务失效。

上海出站进程启动时就发起领取。服务端只接受固定路径的 HTTPS 请求；上海对日本域名使用系统信任库，无需给上海进程配置服务端证书。未接单时日本立即走原路径。上海控制请求遇到暂时性故障时按 1 到 10 秒退避；401/403 表示鉴权失败，须核对凭据。一次领取的客户端总超时为 25 秒，已领任务受原任务绝对截止时间约束。

### 上海运行与停止

使用上节构建的 shanghai-resolver。两个进程需使用相同的独立 pull token；在实际放行两个精确控制路由前，只使用获准的隔离 HTTPS 域名。JAPAN_HTTPS_ORIGIN 为日本端 HTTPS origin（例如 https://japan.example.org），不含路径或查询串。

~~~sh
export DOKITOOL_SHANGHAI_PULL_TOKEN="$(cat "$PULL_TOKEN_FILE")"
./shanghai-resolver -pull-origin "$JAPAN_HTTPS_ORIGIN"
~~~

pull 模式只主动连出；-listen、-tls-cert 与 -tls-key 仅适用于原入站模式。向前台进程发送 Ctrl+C 或向服务进程发送 SIGTERM，停止本地领取、探测与回交。日本端取消用户请求后立即释放等待；已经交给上海的探测最迟在任务截止时结束。过期或已经取消的回执会被拒绝。

### 日本隔离运行、停止和回退

从 backend 按上节构建。在独立 shell 设置一份只供反向控制面使用的凭据。禁止复用微博签名密钥、Caddy 边缘密钥和原上海入站 bearer。以下示例只在本机回环运行。需要验证完整 API 时，沿用既有独立测试签名密钥和其他隔离运行参数。

~~~sh
unset DOKITOOL_SHANGHAI_ENDPOINT DOKITOOL_SHANGHAI_TOKEN
export DOKITOOL_SHANGHAI_MODE=pull
export DOKITOOL_SHANGHAI_PULL_TOKEN="$(cat "$PULL_TOKEN_FILE")"
./dokitool serve -listen 127.0.0.1:9082
~~~

前台 Ctrl+C 或向该测试进程发 SIGTERM 即停止。重启前设置 DOKITOOL_SHANGHAI_MODE=off 并取消 DOKITOOL_SHANGHAI_PULL_TOKEN，可恢复纯日本取址。要回到 #27 的调用方向，取消 pull token，设置 mode=direct 及原上海 endpoint/token 后重启。未设置 mode 和 endpoint 时默认为关闭；历史仅配置 endpoint/token 的部署继续按 #27 工作。pull 配置同时指定旧 endpoint 时启动报错。

### 生产接入差异及原有资产

后续变更需单独审查日本业务域名下这两个控制路径、服务版本和独立凭据。前置代理须覆盖客户端传入的 X-DokiTool-Edge-Key / X-DokiTool-Client-IP，沿用现有可信边缘设置，传递独立 Authorization，限制上海允许来源；长轮询的代理响应超时须大于 15 秒，控制路径禁止缓存、凭据与结果正文不记日志。保持用户解析和微博流式准入的现有配置。上海须校验日本业务域名的 TLS，仅访问配置好的日本端；完整媒体继续由浏览器从 CDN 下载。生产路由和两机启用仍需单独批准。排查时区分上海到日本的静态路径连通、受控 TLS 闭环及真实生产启用。

旧 sh-original DNS、上海云安全组 443 / 日本出口白名单、主机 nft 与 systemd socket、入站 resolver、证书、续期 hook/timer、专用 DNS 凭据和旧入站 bearer 本轮全部保留。未来切换验证完成后，逐项确认消费者并单独批准撤回，保留 SSH 管理入口及其他证书的续期。原续期演练及证书重载仍待验。
