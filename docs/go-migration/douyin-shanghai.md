# 抖音原画取址

## 已验收生产基线（2026-11-04）

日本后端使用 `DOKITOOL_SHANGHAI_MODE=pull`。上海出站服务 `dokitool-sh-pull.service` 已启动并设为开机启用。上海先向日本领取任务，再用作品 detail 中的媒体 `video_id` 请求官方原画入口。日本校验候选并生成统一响应，浏览器直接从选中的 CDN 下载文件。

| 组件 | 验收时的版本 |
|---|---|
| 合并 #28 后的 main | `bc7ec7bfc69eaa64362b1cc6e0a2c81ab8afb3cd` |
| 日本 Go 后端、上海出站 worker 的实际构建源码 | `f68a9885b73b749371b07f955a6e04e63d162449` |
| 上述两个提交共有的 Git tree | `13915b27f2ef0d0b1907e05d42b319f71f3e3c65` |
| 生产前端 / 浏览器下载 Worker | `d2995fe` / `downloads-20261103.1` |

首笔生产解析在同一租约内收到 1 条非空上海候选，日本校验、采用各 1 次，兜底 0 次。最终默认来源 URL 的 SHA-256 与上海候选 URL 的 SHA-256 相同；响应中的原画为 93,733,891 字节、2160×3840、HEVC。文件本身的完整哈希比对来自更早的隔离下载。[生产回执与程序摘要](https://github.com/Brilliant666/DokiTool-Go/issues/9#issuecomment-6622576682) · [隔离文件验收](https://github.com/Brilliant666/DokiTool-Go/issues/9#issuecomment-6610285480)

维护者另一次使用日本代理完整下载了约 500 MB 的视频，报告速度约 5 MB/s。该笔没有独立的来源追踪或文件哈希。国内直连、晚高峰和长期可用性继续通过日常使用观察。[维护者验收记录](https://github.com/Brilliant666/DokiTool-Go/issues/9#issuecomment-6622990976) · [Issue #9 当前摘要](https://github.com/Brilliant666/DokiTool-Go/issues/9)

## 生产 pull 模式

日本 Caddy 仅允许上海指定出口 /32 对 `/internal/shanghai/claim`、`/internal/shanghai/result` 发起 POST，转发至日本后端 `127.0.0.1:19963`。代理覆盖边缘身份头，保留独立 bearer 认证，控制响应禁止缓存。

空闲领取最多等待 15 秒，返回 204 后上海重新等待。每个 worker 同时处理一个任务。上海忙碌或无人等待时，日本立即走原有取址路径；已领任务后失联则受原绝对截止约束（最多 8 秒，并服从用户请求更短的期限）。不重发已领任务，取消、过期或身份不匹配的回执不能覆盖结果。控制面最多允许 4 个等待领取、4 个未结任务和 4 个并发回交。上海遇暂时性通信故障按 1～10 秒退避，401/403 时退出。[两机空闲重领与日本回退验收](https://github.com/Brilliant666/DokiTool-Go/issues/9#issuecomment-6621546530)

上海从官方 play 入口（`ratio=default&line=0`）最多调度两次，共享 6 秒、1 MiB 和 12 次上游 HTTP 请求的探测预算。日本校验视频关联、媒体规格和作品已有转码，将同一原画的合格路线保存在 `delivery.sources`，由服务端决定默认项。显式选中的来源消失时需要重新选择。

## 关闭 pull 和恢复旧版 Go

按[发布操作单](https://github.com/Brilliant666/DokiTool-Go/issues/9#issuecomment-6621196023)及[实际生产回执](https://github.com/Brilliant666/DokiTool-Go/issues/9#issuecomment-6622576682)核对运行版本、当前配置和备份摘要，再分步执行：

1. 上海停止 `dokitool-sh-pull.service`。长期停用时同时 disable，避免重启后再次领取。
2. 日本确认现网配置仍与本次发布版本一致，用 `/root/dokitool-pull28-stage/snap.caddy.prepull` 恢复本站 Caddy 配置，验证并 reload，撤回两条控制路由。若现场已有后续修改，先核对差异。
3. 只关闭 pull 时，从 `/etc/systemd/system/dokitool-go.service.d/` 移走 `29-pull.conf`，保留 `28-binary.conf`。需要恢复上线前的 Go 程序时，同时移走 `28-binary.conf`，目标版本为 `22522d154a43300872c78603af0532ab44ff1d64`。
4. 日本执行 `systemctl daemon-reload`，排空 `dokitool-go` 后停止，确认进程退出和端口释放，再启动。核对实际程序摘要、上海开关和平台目录（9 项）。

旧 direct 入站 DNS、上海 443 白名单、resolver/socket、证书及凭据仍保留。其证书续期演练与重载待独立验证；撤回旧资产前须核对消费者并另行批准。

## 隔离构建与试运行

在 backend 目录，使用 go.mod 指定的工具链：

~~~sh
go build -trimpath -o shanghai-resolver ./cmd/shanghai-resolver
go build -trimpath -o dokitool ./cmd/dokitool
~~~

### 上海出站端（隔离 pull 示例）

使用上面构建的 shanghai-resolver。两个进程需使用相同的独立 pull token；在实际放行两个精确控制路由前，只使用获准的隔离 HTTPS 域名。JAPAN_HTTPS_ORIGIN 为日本端 HTTPS origin（例如 https://japan.example.org），不含路径或查询串。

~~~sh
export DOKITOOL_SHANGHAI_PULL_TOKEN="$(cat "$PULL_TOKEN_FILE")"
./shanghai-resolver -pull-origin "$JAPAN_HTTPS_ORIGIN"
~~~

pull 模式只主动连出；-listen、-tls-cert 与 -tls-key 仅适用于原入站模式。向前台进程发送 Ctrl+C 或向服务进程发送 SIGTERM，停止本地领取、探测与回交。日本端取消用户请求后立即释放等待；已经交给上海的探测最迟在任务截止时结束。过期或已经取消的回执会被拒绝。

### 日本端（隔离进程）

从 backend 按上节构建。在独立 shell 设置一份只供反向控制面使用的凭据。禁止复用微博签名密钥、Caddy 边缘密钥和原上海入站 bearer。以下示例只在本机回环运行。需要验证完整 API 时，沿用既有独立测试签名密钥和其他隔离运行参数。

~~~sh
unset DOKITOOL_SHANGHAI_ENDPOINT DOKITOOL_SHANGHAI_TOKEN
export DOKITOOL_SHANGHAI_MODE=pull
export DOKITOOL_SHANGHAI_PULL_TOKEN="$(cat "$PULL_TOKEN_FILE")"
./dokitool serve -listen 127.0.0.1:9082
~~~

前台 Ctrl+C 或向该测试进程发 SIGTERM 即停止。重启前设置 DOKITOOL_SHANGHAI_MODE=off 并取消 DOKITOOL_SHANGHAI_PULL_TOKEN，可恢复纯日本取址。要回到 #27 的调用方向，取消 pull token，设置 mode=direct 及原上海 endpoint/token 后重启。未设置 mode 和 endpoint 时默认为关闭；历史仅配置 endpoint/token 的部署继续按 #27 工作。pull 配置同时指定旧 endpoint 时启动报错。

## 历史 direct 模式（#27 隔离示例）

此模式由日本主动连接上海入站 resolver。以下示例仅用于独立测试进程。凭据使用两端专用的受限文件或服务管理器的 secret 注入。以下 Linux 示例假设已配置相应文件路径、匹配入口主机的 TLS 证书及获准的回环转发或受来源 IP 限制的 HTTPS 入口。不要开启 shell 跟踪。

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
