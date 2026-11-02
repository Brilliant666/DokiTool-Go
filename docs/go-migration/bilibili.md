# B 站 Go 迁移

本阶段从公共合同修复 PR #2 接续，在独立 Go 仓库实现第二个平台。只替换 B 站后端解析与适配，继续使用既有 unified-v1、服务端 default、候选过滤和前端消费者。前端产品、Worker、bridge、PHP 产品和 Schema 未改；不实现 DASH、mux 或分段 remux 执行器。

## 实际路径与职责

| PHP 参考 | Go 实现 | 保留的行为 |
| --- | --- | --- |
| `parseBilibili`、短链、HTML 回退、API 请求 | `internal/platform/bilibili/parser.go`、`request.go` | 35 秒总预算、原始 p 优先、只请求当前 cid、受控网络执行器 |
| `buildBilibiliResponse`、入口 JSON 编码 | `parser.go`、`helpers.go` | 当前分 P 和旧入口 `json_encode(..., 480)` 的数值转换边界 |
| `snapAdaptBilibili` | `adapter.go` | `<BV>-pN`、其他 P/合集待解析、完整作品和 group |
| `snapBilibiliVariants`、`snapBilibiliTrack` | `adapter.go` | file、segments/remux、逐视频×音轨配对、缺音轨 deferred |
| 公共 default、能力保护、来源身份 | 既有 `internal/unified` | 不在平台包重复选择、排序或过滤规则 |
| 公开 HTTP 路由 | 既有 `internal/api`，CLI 注册 B 站 adapter | parse/resolve 整体作品响应、精确目标检查、错误/HTTP 映射 |

### 分 P 与失败隔离

`p` 使用 PHP 的最后赋值语义与正整数一至六位语法。数组、零、前导零和非法值不能悄悄回到 P1；NUL 参数名和未闭合括号按真实 PHP 行为对照。短链携带的原始 `p` 优先于目标页，HTML 回退后仍只取被选中的 cid。

当前 P 不存在时返回顶层 `CONTENT_UNAVAILABLE`，不请求其他 P 的播放源。已取得详情但当前 P 的播放接口失败时，顶层保留作品信息，仅当前 item 标记为 `unavailable`；其他 P 和合集仍是 `deferred`。resolve 不借用别的条目，也不把这些未就绪目标变成成功下载。

### 质量、候选和身份

单个 durl 是完整 file；多个 durl 保持有序 segments，声明需要 remux。DASH 每条有效视频与每条有效音轨各成一组 mux，缺音轨保留为 `deferred`；这些结果供识别和展示，当前不能执行合并。

规格来自实际播放响应和资源。请求 `qn=112`、view 中的上传尺寸都不能证明实际取得相应画质。保留 part 优先的元数据、分数帧率、HDR 编号 125/126、已知语言和声道，沿用四项能力保护与普通/debug 过滤。稳定 ID 继续使用 PHP 兼容身份编码；续签和主备来源反序由固定样本对拍。

### 请求政策

页面/短链仅允许 `b23.tv`、`bilibili.com`、`www.bilibili.com`、`m.bilibili.com`；API 仅为 HTTPS `api.bilibili.com` 的两个既有路径。分享页安全 fragment 可以经过标准 HTTP 跳转，解析前按 PHP 删除；它不能改变允许主机。实际执行器继续约束每一跳、公网 DNS/连接、TLS、超时和读取预算。

总预算 35 秒，短链和 HTML 请求至多 10 秒，详情/播放请求至多 20 秒，并受同一剩余总预算约束。页面和 API 保留 8 MiB 读取上限，重定向至多五跳。不会抓取整段媒体。

API 与短链显式禁用 Go 自动添加的 User-Agent，HTML 回退沿用原桌面 UA。PHP 的 `CURLOPT_ENCODING=""` 表示其编译支持的编码集合；Go 明确请求 `gzip,deflate`，这是已显式对照的请求头差异，原 PHP 请求记录不被改写。请求 tape 记录执行器输入；真实 fetch 对未指定编码的短链 HEAD 会补 `identity`，本次不承诺两种 HTTP 实现自动生成的全部请求头逐字节相同。出站目标限制是既有迁移设计中的收紧，真实平台适配仍需在线阶段验证。

短链覆盖 p 时，普通跟踪参数和简单数组保留值及顺序；复杂嵌套跟踪参数的全部 PHP 重编码边界尚未覆盖，不能把本次 55 个场景解释为所有 query 语法都已穷尽验收。

## 独立 PHP oracle

- 固定生产 PHP：`25beaee107f8c1a208ced9deb3c637068445e316`。
- 固定时钟：`2026-10-30T12:00:00Z`；PHP **8.3.6**。
- 官方 CLI 镜像：`php@sha256:9ed77294c3332080ebcd2f6b9a2d390565eaa4a05fc26aefff62aa9b0af9b5e9`，Linux amd64。容器无网络、只读挂载；PHP 只读取归档和合成输入。
- **55 个场景、81 个精确请求步骤、110 份 normal/debug 响应**。29 个场景为成功响应，26 个为预期错误；所有期望由原 PHP 产品路径生成。
- 保存 `parser_response`、真实入口数值转换后的 `legacy_response`、公开结果、实际请求和指定警告；完整性清单包含源码、输入、生产器和结果摘要。

首次成功生成见 [CI 41218276929](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41218276929)。前一轮环境实际提供了 PHP 8.3.36，生产器正确拒绝，未产出期望；没有放宽版本门槛。旧 PHP `665bd55`、`25beaee` 与抖音基线均保持原样。

文件位于 `backend/internal/platform/bilibili/testdata/`。后续 CI 每次在临时目录重新执行 PHP，再与已入库的两份 JSON 逐字节比较；不会按实际 Go 输出更新 PHP 期望。

```sh
python3 -B scripts/go-migration/verify-bilibili.py --check-git-source
python3 -B -X utf8 scripts/go-migration/check.py \
  --race --frontend --check-php-source --report /tmp/dokitool-go-checks.json
```

## 验收范围

Go 对拍执行真实 parser、适配、公共投影、服务端选择与过滤，并检查完整 HTTP 请求序列。正式 HTTP 集成验证 parse/resolve 和错误目标拒绝；实际 Go 输出还进入独立 Python Schema/语义验证器和原前端消费者。具体通过数和对应提交以 [验收记录](acceptance.md) 及当前 Head 的 CI 为准。

这些是合成上游输入、冻结 PHP 和实际 Go 产品路径的离线证据。尚未执行 B 站真实链接双实现对照、浏览器保存或线上画质比较，不能据此宣称 Go 版本已在线可用、画质更高或可替代九平台生产。
