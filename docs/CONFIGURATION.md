# 启动参数与环境变量参考

本文档列出 `dokitool` 主服务、`shanghai-resolver` 辅助进程以及前端构建链支持的全部命令行参数与环境变量。

> **说明**：为遵循公开仓库安全规范并避免文档与代码漂移，本文档**仅记录参数名称、作用与敏感属性，不记录任何线上实际取值或默认值**；允许的枚举范围、校验规则与默认值均以对应源码为准。

---

## 一、`dokitool` 子命令与启动参数

源码定义：`backend/cmd/dokitool/main.go`

### 1. 子命令

| 名称 | 作用 | 是否敏感 |
| --- | --- | --- |
| `serve` | 启动主后端 HTTP API 解析、内部控制面与准入限流服务 | 否 |
| `catalog` | 向标准输出打印当前内置平台目录的统一契约 JSON | 否 |
| `validate` | 从标准输入读取 JSON 载荷并按统一媒体契约结构执行校验 | 否 |
| `version` | 向标准输出打印编译阶段注入的程序版本标识 | 否 |

### 2. `dokitool serve` 命令行参数

| 名称 | 作用 | 是否敏感 |
| --- | --- | --- |
| `-listen` | 指定主后端本地回环 HTTP 监听地址与端口 | 否 |
| `-xhs-page-transport` | 选择小红书分享页与笔记页的 HTTPS 抓取传输实现（标准库传输或指定浏览器 TLS 指纹传输） | 否 |
| `-x-tombstone-fallback` | 控制 X 受限推文（墓碑页）的回退行为（直接返回受限错误，或向公开第三方元数据接口查询） | 否 |
| `-client-identity` | 指定准入控制识别客户端地址的方式（仅接受本地回环直连对端，或校验前置反向代理转发的身份头） | 否 |

---

## 二、`shanghai-resolver` 启动参数

源码定义：`backend/cmd/shanghai-resolver/main.go`

| 名称 | 作用 | 是否敏感 |
| --- | --- | --- |
| `-pull-origin` | 出站拉取模式下连接主后端领取抖音任务与回传结果的 HTTPS 源站地址（与入站监听类参数互斥） | 否 |
| `-listen` | 历史入站直连模式下的本地监听地址与端口 | 否 |
| `-tls-cert` | 历史入站直连 HTTPS 模式下的服务端证书链文件路径 | 否 |
| `-tls-key` | 历史入站直连 HTTPS 模式下的服务端私钥文件路径 | 是 |

---

## 三、后端环境变量

### 1. 密钥与鉴权凭据

源码定义：`backend/cmd/dokitool/main.go`、`backend/cmd/shanghai-resolver/main.go`、`backend/internal/api/admission.go`

| 名称 | 作用 | 是否敏感 |
| --- | --- | --- |
| `SNAP_MEDIA_SIGNING_KEY` | 微博视频同源中转接口签发与校验短期访问票据所用的共享 HMAC 密钥 | 是 |
| `SNAP_INGRESS_KEY` | 前置反向代理转发客户端真实地址时用于认证边缘请求头的共享密钥 | 是 |
| `DOKITOOL_SHANGHAI_PULL_TOKEN` | 上海拉取模式下任务认领与结果回传内部路由专用的独立 Bearer 鉴权令牌（不得与其他密钥相同） | 是 |
| `DOKITOOL_SHANGHAI_TOKEN` | 历史上海入站直连模式下的请求 Bearer 鉴权令牌 | 是 |

### 2. 抖音上海通道与平台启用开关

源码定义：`backend/cmd/dokitool/main.go`、`backend/internal/platform/ytgeneric/sites.go`

| 名称 | 作用 | 是否敏感 |
| --- | --- | --- |
| `DOKITOOL_SHANGHAI_MODE` | 控制主后端抖音上海解析通道的工作模式（禁用、历史直连模式或出站拉取模式） | 否 |
| `DOKITOOL_SHANGHAI_ENDPOINT` | 历史上海直连模式下主后端请求的远端解析端点地址（拉取模式下必须留空） | 否 |
| `DOKITOOL_TBVIDEO_REWRITE_PIPIGX` | 控制是否为皮皮搞笑视频生成同路径跨域可读无水印候选附件信息 | 否 |
| `DOKITOOL_TBVIDEO_REWRITE_ZUIYOU` | 控制是否为最右视频生成同路径跨域可读无水印候选附件信息 | 否 |
| `DOKITOOL_YTDLP_X_ENABLED` | 控制是否注册并启用 X 平台解析适配器（启用时在启动阶段强校验提取器可用性） | 否 |
| `DOKITOOL_YTDLP_YOUTUBE_ENABLED` | 控制是否注册并启用 YouTube 解析路由适配器（启用时在启动阶段强校验提取器可用性） | 否 |
| `DOKITOOL_YTDLP_INSTAGRAM_ENABLED` | 控制是否注册并启用 Instagram 平台解析适配器（启用时在启动阶段强校验提取器可用性） | 否 |
| `DOKITOOL_YTDLP_SITES` | 按逗号分隔指定启用的长尾海外平台标识集合（含未知标识或空项时拒绝启动） | 否 |
| `DOKITOOL_YOUTUBE_HELPER` | 控制是否启用 YouTube 浏览器助手上下文与签名结算接口（启用时在启动阶段校验运行时与脚本文件摘要） | 否 |

### 3. 外部提取器、任务网关与签名解算沙箱

源码定义：`backend/internal/ytengine/engine.go`、`backend/internal/ytengine/gate_ports.go`、`backend/internal/platform/youtube/ejs_sandbox.go`

| 名称 | 作用 | 是否敏感 |
| --- | --- | --- |
| `DOKITOOL_YTDLP_BINARY` | 指定受官方发布摘要锁定校验的外部提取器可执行文件路径 | 否 |
| `DOKITOOL_YTDLP_CONCURRENCY` | 指定所有依赖外部提取器的平台共享的最大并发子进程数 | 否 |
| `DOKITOOL_YTDLP_TIMEOUT` | 指定单次外部提取器子进程调用的超时时长 | 否 |
| `DOKITOOL_YTDLP_GATE_PORTS` | 按逗号分隔指定本地回环任务网关监听端口池（端口数量须与提取器并发上限一致） | 否 |
| `DOKITOOL_YTDLP_PROXY` | 外部提取器代理配置项（启用出站目标白名单策略时禁止设置；若携带代理凭据则属敏感信息） | 是 |
| `DOKITOOL_DENO_BINARY` | 指定用于无权限沙箱解算 YouTube 签名挑战且受摘要校验的 Deno 可执行文件路径 | 否 |
| `DOKITOOL_EJS_DIR` | 指定存放受摘要校验的 YouTube 签名解算脚本文件的目录路径 | 否 |
| `DOKITOOL_EJS_CONCURRENCY` | 指定 Deno 签名解算沙箱允许的最大并发子进程数 | 否 |
| `DOKITOOL_EJS_TIMEOUT` | 指定单次 Deno 签名解算子进程执行的超时时长 | 否 |
| `PATH` | 系统可执行文件搜索路径，用于定位提取器或启动自检所需的运行时 | 否 |
| `SYSTEMROOT` | Windows 环境下向净化后的提取器子进程透传的系统根目录路径 | 否 |

### 4. 准入控制限流与并发池

源码定义：`backend/internal/api/admission_config.go`

| 名称 | 作用 | 是否敏感 |
| --- | --- | --- |
| `DOKITOOL_ADMISSION_MAX_CLIENTS` | 指定每个准入限流池内跟踪的独立客户端状态表容量上限 | 否 |
| `DOKITOOL_PARSE_CLIENT_BURST` | 解析池（覆盖解析、按需单集解析及未命中缓存的 YouTube 助手接口）单客户端令牌桶突发容量 | 否 |
| `DOKITOOL_PARSE_CLIENT_RPS` | 解析池单客户端每秒补充的请求令牌速率 | 否 |
| `DOKITOOL_PARSE_CLIENT_CONCURRENCY` | 解析池单客户端允许同时在途的最大请求数 | 否 |
| `DOKITOOL_PARSE_GLOBAL_BURST` | 解析池全局令牌桶突发容量 | 否 |
| `DOKITOOL_PARSE_GLOBAL_RPS` | 解析池全局每秒补充的请求令牌速率 | 否 |
| `DOKITOOL_PARSE_GLOBAL_CONCURRENCY` | 解析池全局允许同时在途的最大请求数 | 否 |
| `DOKITOOL_CATALOG_CLIENT_BURST` | 目录池（覆盖平台目录及命中缓存的 YouTube 上下文接口）单客户端令牌桶突发容量 | 否 |
| `DOKITOOL_CATALOG_CLIENT_RPS` | 目录池单客户端每秒补充的请求令牌速率 | 否 |
| `DOKITOOL_CATALOG_CLIENT_CONCURRENCY` | 目录池单客户端允许同时在途的最大请求数 | 否 |
| `DOKITOOL_CATALOG_GLOBAL_BURST` | 目录池全局令牌桶突发容量 | 否 |
| `DOKITOOL_CATALOG_GLOBAL_RPS` | 目录池全局每秒补充的请求令牌速率 | 否 |
| `DOKITOOL_CATALOG_GLOBAL_CONCURRENCY` | 目录池全局允许同时在途的最大请求数 | 否 |
| `DOKITOOL_WEIBO_CLIENT_BURST` | 微博媒体中转池单客户端令牌桶突发容量 | 否 |
| `DOKITOOL_WEIBO_CLIENT_RPS` | 微博媒体中转池单客户端每秒补充的请求令牌速率 | 否 |
| `DOKITOOL_WEIBO_CLIENT_CONCURRENCY` | 微博媒体中转池单客户端允许同时在途的最大请求数 | 否 |
| `DOKITOOL_WEIBO_GLOBAL_BURST` | 微博媒体中转池全局令牌桶突发容量 | 否 |
| `DOKITOOL_WEIBO_GLOBAL_RPS` | 微博媒体中转池全局每秒补充的请求令牌速率 | 否 |
| `DOKITOOL_WEIBO_GLOBAL_CONCURRENCY` | 微博媒体中转池全局允许同时在途的最大请求数 | 否 |
| `DOKITOOL_REPORT_CLIENT_BURST` | 下载校验上报池单客户端令牌桶突发容量 | 否 |
| `DOKITOOL_REPORT_CLIENT_RPS` | 下载校验上报池单客户端每秒补充的请求令牌速率 | 否 |
| `DOKITOOL_REPORT_CLIENT_CONCURRENCY` | 下载校验上报池单客户端允许同时在途的最大请求数 | 否 |
| `DOKITOOL_REPORT_GLOBAL_BURST` | 下载校验上报池全局令牌桶突发容量 | 否 |
| `DOKITOOL_REPORT_GLOBAL_RPS` | 下载校验上报池全局每秒补充的请求令牌速率 | 否 |
| `DOKITOOL_REPORT_GLOBAL_CONCURRENCY` | 下载校验上报池全局允许同时在途的最大请求数 | 否 |

---

## 四、前端构建期环境变量

源码定义：`frontend/src/services/apiConfig.ts`

| 名称 | 作用 | 是否敏感 |
| --- | --- | --- |
| `VITE_API_MODE` | 前端构建期选择接口路由模式（同源兼容路由模式或独立跨域安全路由模式） | 否 |
| `VITE_API_BASE_URL` | 前端构建期指定后端 API 基础源站地址（同源部署时留空） | 否 |
