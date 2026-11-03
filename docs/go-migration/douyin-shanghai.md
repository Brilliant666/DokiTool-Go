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
