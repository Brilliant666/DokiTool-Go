# 抖音上海解析通道（Pull v2）

本文档说明当前线上抖音国内 CDN 线路补充与原画取址的架构设计、并发与超时预算、回退机制及守护策略。早期 `v1` 单原画探针与历史直连模式归档于文末 [历史](#历史) 节。

---

## 一、架构与 `v2` 协议概览

线上主后端启用 `DOKITOOL_SHANGHAI_MODE=pull`（配置项说明见 [CONFIGURATION.md](../CONFIGURATION.md)），上海节点运行 `shanghai-resolver -pull-origin <主后端 HTTPS 源站>`，全程仅主动发起出站 HTTPS 连接，不开放任何入站监听端口：

1. **内部控制面路由**：
   - `POST /internal/shanghai/claim`：上海 worker 携带独立 `Authorization: Bearer <DOKITOOL_SHANGHAI_PULL_TOKEN>` 与协议头 `X-DokiTool-Shanghai-Protocol: 2` 长轮询认领任务。
   - `POST /internal/shanghai/result`：上海在同一租约（`lease_id`）截止前将解析结果回传主后端；取消、过期或租约不匹配的回传一律丢弃，不会污染其他请求。
2. **`v2` 任务载荷与返回内容**（源码：`backend/internal/shresolver/`、`backend/internal/platform/douyin/shanghai.go`）：
   - 主后端识别出抖音作品 `aweme_id` 后，在拉取本地详情的同时向 `PullBroker` 投递 `Protocol: 2` 任务（携带 `aweme_id`；若连接的是旧 `v1` worker 则返回 `ErrProtocolV1` 并自动回退 `v1` 仅原画探测）。
   - 上海端直接根据 `aweme_id` 请求作品详情，提取全部国内 `v*.douyinvod.com` 转码清晰度地址（`v26-web` 统一归一化为 `v26-luna`）并按存储路径（`DouyinStoragePath`）分组为 `renditions`，同时向 `aweme.snssdk.com/aweme/v1/play/` 探测 4K 原画候选（`candidates`）。
   - 主后端校验上海返回的 URL 域名、存储对象路径与媒体规格后，将合格的国内 `*.douyinvod.com` 地址前置并入对应清晰度的 `delivery.sources` 首位（海外源保留在同清晰度后续位置），由浏览器在下载前并发 Range 测速择优（详见 [DOWNLOADS.md](../DOWNLOADS.md)）。

---

## 二、Worker 并发与控制面容量

- **固定 3 个并发 Worker**：`shanghai-resolver` 进程内固定启动 **`3` 个独立 worker 协程**（`pullWorkers = 3`），每个 worker 同一时刻最多持有并处理 1 个租约任务。
- **控制面有界队列**：主后端 `PullBroker` 容量上限为 **`8`**（最多允许 8 个等待认领长轮询、8 个未结租约任务和 8 个并发结果回交），超出边界时快速拒绝并走本地回退，绝不无限堆积内存或请求协程。

---

## 三、超时预算与连接健康策略

源码定义：`backend/internal/shresolver/server.go`、`pull_broker.go`、`pull_worker.go`、`backend/internal/platform/douyin/shanghai.go`

| 阶段 | 预算 / 阈值 | 行为说明 |
| --- | --- | --- |
| 空闲认领长轮询（`PullWait`） | 服务端等待 `15s`（客户端请求超时 `25s`） | 无任务时服务端最多挂起 `15s` 后返回 `204 No Content`，worker 立即发起下一轮等待；若连续 **3 次**在 `< 1s` 内快速收到 `204`，自动关闭空闲复用连接，防止中间网络静默老化导致假死。 |
| 主后端任务总等待（`WorkTimeout`） | `9s`（受外层请求上下文约束） | 主后端单次 `ResolveWork` 最多等待上海结果 `9s`；若用户主动取消请求，主后端立即释放等待。 |
| 上海端回传预留（`workResultReserve`） | `1.5s`（或剩余租期一半的较小值） | 上海从租约剩余时间中扣除结果上传预留时间，确保在主后端超时前完成 `POST /internal/shanghai/result`。 |
| 上海端内部解析子预算（`WorkResolveTimeout`） | 上限 `7s`（详情 `4s` + 原画探测剩余预算） | 作品详情请求单独限制 **`4s`** 超时；拿到详情国内转码列表后，仅用剩余子预算探测原画入口。**若原画探测超时，上海仍立即回传已获取的国内转码 `renditions`（`candidates` 为空）**，保证主后端在极端慢原画场景下仍能在约 10 秒内拿到国内 CDN 线路并完成兜底。 |
| 认领后零退避续领 | `0s`（已认领任务结束后） | 只要本轮已成功领取有效租约（`claimed == true`），无论上游解析是否超时或回交是否失败，worker 都会重置退避计数并**立即认领下一笔任务**，不会因单条慢作品拖慢后续并发请求。 |
| 认领前通信异常退避 | `1s` 起指数退避至上限 `10s` | 仅当认领请求本身发生网络/5xx 暂时故障时执行 `1s → 10s` 退避；收到 `401` 或 `403` 鉴权错误时立即终止进程报错。 |

---

## 四、无结果或超时时的回退行为

- **零排队即时回退**：当上海当前无空闲等待 worker、全忙、租约超时，或返回空结果/未通过校验时，主后端**不让用户请求排队等待**，立即使用主后端自身获取的作品详情结果，并在需要时于本地剩余预算内执行原画兜底探测。
- **故障隔离**：上海节点离线、重启或网络抖动仅影响国内 `*.douyinvod.com` 补充线路的命中率，不会导致抖音 `/api/parse` 或 `/api/resolve` 整体不可用。

---

## 五、systemd 自动重启守护

上海端 `dokitool-sh-pull.service` 通过 systemd drop-in 启用故障自愈策略（上线记录见 Issue `#96`）：

- `Restart=on-failure`、`RestartSec=5`：进程因未捕获异常或瞬时故障非零退出时，5 秒后自动拉起恢复认领。
- `StartLimitIntervalSec=300`、`StartLimitBurst=10`：在 300 秒窗口内限制最多重启 10 次；若因令牌失效（`401`/`403`）等持续性配置错误反复退出，达到阈值后停止盲目拉起，便于运维排查。

---

## 六、本地隔离构建与试运行

在 `backend` 目录使用 `go.mod` 指定工具链构建：

```sh
go build -trimpath -o shanghai-resolver ./cmd/shanghai-resolver
go build -trimpath -o dokitool ./cmd/dokitool
```

1. **主后端本地回环启动（隔离示例）**：
   ```sh
   unset DOKITOOL_SHANGHAI_ENDPOINT DOKITOOL_SHANGHAI_TOKEN
   export DOKITOOL_SHANGHAI_MODE=pull
   export DOKITOOL_SHANGHAI_PULL_TOKEN="$(cat "$PULL_TOKEN_FILE")"
   ./dokitool serve -listen 127.0.0.1:9082
   ```
   > 注意：`DOKITOOL_SHANGHAI_PULL_TOKEN` 必须使用专用独立随机密钥，禁止与 `SNAP_MEDIA_SIGNING_KEY`、`SNAP_INGRESS_KEY` 或 `DOKITOOL_SHANGHAI_TOKEN` 复用，否则启动阶段直接报错拒绝。

2. **上海出站端启动（隔离示例）**：
   ```sh
   export DOKITOOL_SHANGHAI_PULL_TOKEN="$(cat "$PULL_TOKEN_FILE")"
   ./shanghai-resolver -pull-origin "$MAIN_HTTPS_ORIGIN"
   ```

---

## 历史

### 1. `#28` / `#30` 早期 `v1` Pull 原画验收基线（2026-11-04）

早期 `v1` 协议仅在主后端完成作品详情解析后向上海下发 `video_id` 请求官方原画入口（不拉取详情全清晰度国内线路），当时 `PullBroker` 容量为 4、单租约绝对截止上限为 8 秒、原画探测预算为 6 秒 / 1 MiB / 12 次上游 HTTP 请求：

- `#28` 上线时：主后端与上海出站 worker 构建源码为 `f68a9885b73b749371b07f955a6e04e63d162449`（合并后 `main` 为 `bc7ec7bfc69eaa64362b1cc6e0a2c81ab8afb3cd`，前端 `d2995fe` / 下载 Worker `downloads-20261103.1`）。首笔生产解析在同一租约内收到 1 条非空上海原画候选（`93,733,891` 字节、`2160×3840`、HEVC），主后端校验采用且兜底 0 次（详见 [生产回执](https://github.com/Brilliant666/DokiTool-Go/issues/9#issuecomment-6622576682)、[隔离文件验收](https://github.com/Brilliant666/DokiTool-Go/issues/9#issuecomment-6610285480) 与 [两机空闲重领验收](https://github.com/Brilliant666/DokiTool-Go/issues/9#issuecomment-6621546530)）。
- `#30` 增量更新时：仅更新主后端 Go 程序至 `dd5e66d`（对应合并后 `main` `5c8c2fd`），详见 [#30 发布回执](https://github.com/Brilliant666/DokiTool-Go/pull/30#issuecomment-6628340403)。
- 随后经 `#81` / `#83` 升级为 `v2` 协议与 3 worker 并发，经 `#84` / `#85` 拆分详情与原画超时预算并消除认领后退避睡眠，经 `#96` 补齐 systemd `Restart=on-failure` 自动重启守护。

### 2. `#27` 历史入站 Direct 模式（已停用）

早期 `#27` 曾设计由主后端主动通过 `DOKITOOL_SHANGHAI_ENDPOINT` 连接上海入站 HTTPS 监听服务（`shanghai-resolver -listen <addr> -tls-cert <cert> -tls-key <key>` + `DOKITOOL_SHANGHAI_TOKEN`）。线上自 `#28` 起全面采用无需上海开放入站端口的 `pull` 模式；历史直连参数仅保留代码兼容，线上拉取模式下 `DOKITOOL_SHANGHAI_ENDPOINT` 必须保持未设置。
