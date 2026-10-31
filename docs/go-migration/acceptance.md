# Go 迁移验收记录

## B 站迁移：PR #3

分支 `codex/bilibili-go-migration` 从公共合同修复 `0bd08e03e47248b601ac4fc1afd6682c435d6013` 接续；[Draft PR #3](https://github.com/Brilliant666/DokiTool-Go/pull/3) 以 PR #2 分支为 base，避免混淆合同修复与平台迁移。没有合并 PR #1/#2/#3，没有部署或修改公开 PHP main。

本阶段新增 Go B 站 parser/adapter 并注册正式 API。保留当前 P 的精确 cid、其他 P/合集待解析、完整作品响应、file/segments/mux、缺音轨 deferred、稳定 ID、共享 default 和能力保护。迁移修正包含原始短链 p 优先、特殊 PHP 参数名、实际 HTTP 默认 UA 和重定向 fragment 的兼容问题；详细范围与限制见 [B 站迁移说明](bilibili.md)。

独立 oracle 使用已发布 PHP `25beaee`、固定时钟及 PHP **8.3.6**，真实执行原 parser、入口 JSON 480 数值转换、adapter/public 与原 PHP 合同检查。原 PHP 文件不修改，原生网络函数禁用，容器也无网络。55 个场景、81 个请求步骤生成 **110** 份 normal/debug 公开响应，原始期望和 manifest 已入库。现有三个旧基线组及 PHP `25beaee` 探针对照保持原样。

首次完整 Go 验证提交：`46a1b15e225dd9e8574abd2e721e8216fcec2774`。实际执行见 [CI 41218589863](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41218589863)，完整机器报告保留为 [bilibili-checks.json](bilibili-checks.json)。

| 检查 | 本次实际结果 |
| --- | --- |
| 固定 PHP 重放 | **55/55** 场景，**110** 份公开响应；与入库 oracle、manifest 逐字节一致 |
| B 站完整公开响应对拍 | **110/110**，声明差异 **0**；另比较 legacy wire 和请求序列 |
| Go 1.27.3 全包测试 | **1337/1337** 叶子场景，1529 个含父级通过事件；计数不可相加 |
| Race / vet / CLI 构建 | 通过 |
| 独立 Schema＋语义 | **376/376**，失败 0 |
| 原前端实际消费 | **300/300** 符合预期：232 成功＋68 预期错误，被拒成功响应 0 |
| 原前端默认/显式续签 | **4/4** |
| B 站正式 HTTP | **53** 场景（52 个表驱动子场景＋1 个专项），包含 parse/resolve/错误目标拒绝 |
| 原公共差异比较单测 | **11/11**；原 10 项精确差异继续严格消费 |
| 固定 PHP 来源与基线 | 旧组、新 PHP 探针组、新 B 站组均核验通过 |

本地无 Go/PHP，实际运行来自对应 GitHub runner。收尾使用同一 Go 1.27.3 的 `gofmt` 输出规范化六个新增 Go 文件，并加入格式门禁、验收文档和机器报告；最终 Head 的两个 CI job 仍须分别通过，不能用上一提交的结果代替当前检查。

初次 oracle 启动时安装步骤实际给出 PHP 8.3.36，生产器拒绝并停止，没有生成错误版本的期望。随后改用已核验的官方 PHP 8.3.6 镜像，以不可变 digest 固定；没有更改版本断言或反写 Go 期望。第一轮完整 Go 对拍通过，未出现需改 oracle 放行的响应差异。

所有平台输入为合成或冻结材料。没有新增真实 B 站/抖音请求、媒体下载、浏览器保存、生产发布或回滚演练；没有将解析候选声明为可执行合流。剩余七个平台、真实链接双实现对照、必要浏览器交付与切流回滚仍未完成。PR #11 的历史真实浏览器自动恢复验收保持独立。

## 公开响应合同修复：`09622a0` 后续

修复分支 `codex/fix-public-media-projection` 从 `09622a05b69bcb4f1da435295fdafd59aba80455` 接续，面向独立 Go 仓库的迁移分支审查。维护者已将该仓库公开用于开发协作；下方私库建立与原阶段验证数字均保留为历史记录。

产品变化限于 Go 的公开 Source 投影：删除 direct 媒体 URL 的字面 fragment，并省略无法由原前端精确表示的超限正整数文件大小。完整身份、默认选择、候选顺序和签名 query 保留；PHP、前端产品、Worker、bridge 与 schema 不变。详细边界及历史复现见 [合同问题与修复](known-issues.md)。

冻结 PHP 输入、期望、生产器和来源摘要不改。测试使用独立的精确差异清单，对 10 份已知公开响应逐字段说明修正后继续比较完整输出；内部抽取和 formatter 仍对拍原始 PHP 结果。独立验证器和前端直接消费实际 Go 输出，没有后处理 actual 或放宽规则。

本次完整检查命令：

```sh
python -B -X utf8 scripts/go-migration/check.py \
  --race --frontend --check-php-source \
  --report /tmp/dokitool-go-checks.json
```

修复分支增加单个 `Go migration contract` PR 检查，在 Ubuntu runner 上使用 `go.mod` 的工具链、Node 24 和现有 Python 校验依赖。它读取两版完整 Git 历史核对冻结源码，执行 Go 测试、race、vet、严格对拍、独立 Schema/语义、原前端消费与续签，并构建 CLI。报告作为短期 Actions artifact 保留，实际结果以对应提交的检查为准，不复用下方历史数字。

当前会话本地缺少 Go，官方工具链下载被执行环境网络阻断，因此没有本地 Go 编译或运行通过声明。新代码须由上述远端检查提供执行证据。所有验证使用冻结合成输入或受控本地连接，不新增真实平台、媒体下载、浏览器落盘、生产发布或回滚演练。

## 当前接续与历史记录范围

后续 Go 开发已转入独立私有仓库 [Brilliant666/DokiTool-Go](https://github.com/Brilliant666/DokiTool-Go)，公开 PHP main 持续保留。维护和许可边界见 [MAINTENANCE.md](MAINTENANCE.md)。下文第二/三阶段数字、当时推送失败与部署限制保留为交接环境的历史证据，不代表本轮在本机新执行的结果。

公开同步检查点：`1eb529f32b45898875008acde9e8b91fc69c5ad9`，由原 Go Head `a6c333016dadae63d78946affa0068f2488b40b2` 合入已发布 PHP main `25beaee107f8c1a208ced9deb3c637068445e316` 得到。私库 main 以该 PHP main 为初始基础，私库迁移分支从此公开检查点接续；不会将 Go 分支合入公开 PHP main。

本轮验证应分别记录：旧 `665bd55` 证据未改、`25beaee` 新 PHP 基线与差异、受影响 Go 回归/对拍、独立合同和现有前端消费。各项本轮执行结果以追加的实际回执为准，不能沿用下方历史数字代替。fragment 与超范围 `size_bytes` 两类旧合同失败继续保留；PR #11 自动恢复真实浏览器待验收项单列，不阻断离线接续。本轮不部署 Go、不改变 PHP 生产、不重新下载媒体。

### 私库本轮实测：PHP `25beaee` → Go

新版[生产器、来源哈希与差异](../../backend/testdata/php-25beaee/README.md)使用固定时钟、合成输入和 PHP **8.3.6** 执行两个 Git 归档。旧基线的 208 个公共场景、21 个票据、目录、70 个 Parse、23 个探针、72 个舍入输入，以及 13 个整数 Parse / 12 个私有调用，均在新归档重放后与原期望一致，引用原哈希。新基线独立生成两遍，三份 JSON 逐字节一致。不同运行时（8.4.27 与 8.3.6）在 72 个舍入输入中有 6 个结果不同；这是单独的运行时观察，不归因于 PR #11，也不改写旧期望。

- **行为变化：** 初始入口保留平台授权；后续 CDN 和每次 Range 使用 HTTPS/443、公网 DNS 和连接绑定、TLS 及原有请求数、字节、跳数和时间预算。QuickTime 需要实际 `ftyp/moov` 证据；只在成功读取后提交新来源，后续失败保留最后可信来源。新增的根相对跳转回归覆盖公网 IPv6 方括号。未修改 Live Photo 独立解析器的主机策略。
- **编译与对拍：** Go 1.27.3，私库 module 编译成功，编译产物输出的目录与 PHP 完全相同。全包 **969** 个叶子场景通过，`go vet ./...` 通过；本轮没有重新执行 `-race`。12 个新场景中 **6 变、6 不变**，24 份 Go normal/debug 响应与 PHP 完整对拍一致，全部通过独立校验和原前端消费。
- **总门禁 `passed:false`、退出 1：** 独立校验 **264/266**，仅旧 fragment 的 normal/debug 两份失败。前端 **180/190** 符合预期（164 个正常成功、16 个后端错误），10 份拒绝仍为原 fragment 2 份和超范围 `size_bytes` 8 份；失败 ID 与原检查点一一相同，PHP 与 Go 同样被拒。4/4 默认与显式续签用例通过。**新增失败 0**，未删除或放宽任何失败项。[机器报告](php25-checks.json)
- **证据范围：** 固定源代码、离线输入、受控传输、独立校验和实际前端测试模块；无真实 CDN 请求、浏览器落盘、生产切换、其他平台迁移或新媒体下载。PR #11 自动恢复仍待合适的真实浏览器样本，另案保留。

## 基线与范围

- PHP main：`665bd55134982b9fe107aa1cd00373ba8da8ff3c`。
- PHP tree：`93744d4d718c3299f82640d2888e93252106db70`。
- 工具链：Go 1.27.3；参考执行器 PHP 8.3.6。工具链从 Go 官方发布清单下载并核验 SHA-256。
- 第二阶段检查点已通过：骨架、unified、稳定 ID 和票据对拍。随后进入抖音迁移。
- 现有 PHP 生产实现保留；本轮没有核查运行服务器、生产切流、配置 reload 或生产回滚。

## 第二阶段的固定证据

| 检查 | 结果与覆盖 | 证据位置 |
| --- | --- | --- |
| 冻结 PHP 纯函数/合同 | 208 场景逐字段比较通过；涵盖 29 设计样例、拒绝合同、默认、普通/debug、身份、来源、事实合并、音轨配对 | `backend/testdata/php-baseline/cases.json`，`internal/unified/baseline_test.go` |
| 票据 | 21 场景通过；有效签发字节和验证结果与 PHP 相同，保留 TTL/上游到期/非法输入拒绝 | `tickets.json`，`internal/media/weibo/ticket_test.go` |
| 平台目录 | 与 PHP 实际 CLI 输出逐字段一致，继续读取唯一平台注册 JSON | `catalog.json`，`internal/catalog/catalog_test.go` |
| 基线完整性 | 40 个固定 PHP/Schema 输入文件摘要通过；两次由同一固定 Git archive 生成的数据与 manifest 逐字节一致 | `manifest.json`，`scripts/go-migration/verify.py` |
| 独立 Schema/语义 | 76/76 份实际 Go 公共层输出通过既有 Python Draft 2020-12 及独立语义验证 | `DOKITOOL_GO_BASELINE_OUTPUT` 导出，`scripts/validate-unified-schema.py` |
| HTTP 骨架 | 旧路由 410、未知路径 404、GET/版本/输入边界、缺 adapter 503、resolve 目标及完整工作保留、过滤前拒绝非法合同 | `internal/api/handler_test.go` |
| 请求边界 | 混合公网/私网 DNS 拒绝、批准 IP 传递、跨主机凭据移除、逐跳限制、原 query、压缩前后读取限额、DNS 取消 | `internal/fetch/fetch_test.go` |
| Go 检查 | 全包 345 个叶子场景通过，race 与 vet 通过；Schema 扩展门禁及 URL/HTTP/manifest 差异已修复 | Go 测试和静态检查命令见 README |

阶段 2 机器可读结果见 [phase2-checks.json](phase2-checks.json)。该检查点尚无抖音 Go 平台实现，证明先公共层、后平台的执行顺序。

第二阶段本地检查点提交：`988fc95cf4ea96087f465eb4aa49b6d7b8121037`。

这些均为离线或受控测试，不证明当前真实平台可访问，也不证明浏览器已保存媒体。票据兼容不等于微博媒体 relay 已迁移。

## 第三阶段：首个抖音实现

状态：**已实现并完成本轮检查；严格 PHP 对拍通过，但总验收未通过。** 两类原 PHP 与原前端之间的合同冲突保留为失败项，详见 [known-issues.md](known-issues.md)。没有通过修改基线期望、放宽前端或跳过数据将结果改绿。

### 已实现

- CLI/API 实际绑定抖音 Go adapter。请求在进程内执行，不调用 PHP；其余八个平台仍明确返回未配置，尚未迁移。
- 详情请求、分享短链及 HEAD 兜底、作品身份校验、完整文件与独立音轨、A/B 配对、后续 FPS/HDR 观察、图片/Live、背景音频、原始候选探测与失败回退均有对应实现。
- 使用共享 `unified` 聚合事实、稳定身份、服务端默认和普通/debug 筛选；保留 PR #9 四项能力保护及明确的合流不可执行状态。
- 有界 MP4 探测保留 1 MiB/12 请求/6 秒预算与已确认 URL，取消会释放实际连接；严格媒体传输保留原始重复头与 trailer 证据，不让标准库归并掩盖错误。
- 修复迁语言差异：PHP 半数舍入、直接整数转换、数组形状、JSON 数字及哈希字节。整数 oracle 由冻结 PHP 实际运行生成，不由 Go 结果计算期望。

### 固定输入与现有前端检查

| 检查 | 本轮结果 | 证据位置 |
| --- | --- | --- |
| 全包 Go | **931/931 叶子场景**通过；`-race` 与 `go vet ./...` 通过 | [phase3-checks.json](phase3-checks.json) |
| 公共层 PHP 基线 | 原 **208 场景、21 票据、1 目录**继续通过；固定来源字节校验通过 | `php-baseline/`；`verify.py --source-root .` |
| 抖音实际 Parse | **70 场景／140 normal/debug 响应**与 PHP 逐字段相同；同时比较上游方法、URL、headers、顺序和 probe 请求 | `php-douyin/cases.json` |
| 探测与舍入 | **13 metadata + 10 实际 bounded reader**；**72 PHP round** 观察通过 | `php-douyin/probe.json`、`numeric.json` |
| PHP 整数边界 | **13 完整 Parse／26 公共响应 + 12 private 调用**对拍通过，新回归 **49/49** | `internal/platform/douyin/testdata/`；固定 producer/source/fixture hashes |
| 实际 HTTP 网关到抖音 | 新增 **28/28**：18 parse、8 resolve 命中、2 resolve 缺失；包内合计 **50/50** | `internal/api/douyin_test.go` |
| 原前端续签 | **4/4**；actual Go CDN/play 输出分别走默认与显式选源，保持任务快照及原界面选择 | `frontend/scripts/test-go-media-integration.mjs` 调用既有测试 |
| 原前端回归 | **294/294**，类型检查、生产构建、SSG **4/4**、加载预算均通过 | [frontend-checks.json](frontend-checks.json) |
| 产品未改 | `frontend/src/`、`frontend/public/` 相对固定 PHP main 无差异；PHP 生产文件和原平台 JSON 保留 | Git diff；基线摘要校验 |
| 可执行产物 | Go CLI 构建成功；编译后 catalog 输出与冻结 PHP 目录完全一致 | CLI build/catalog 对照 |

测试数量有包含关系：平台、HTTP、探测及公共层测试都已包含在 931 个叶子场景中，不能把上表简单相加。

### 必须保留的失败

**独立 Schema 与语义：240/242 通过，2 份失败。** 242 份由 76 公共层实际输出、140 抖音输出、26 整数边界输出组成。失败为同一 Live Photo 输入的 normal/debug：媒体交付 URL 含 `#fragment`。原 PHP 与 Go 完全一致，原前端及原 Python 独立校验均拒绝。

**原前端消费：166 份中 156 份符合预期，10 份成功响应被拒绝。** 其中 140 份成功响应正常消费、16 份后端错误按原消息正确拒绝；其余 2 份为 fragment，8 份为极限 `size_bytes` 超过 `Number.MAX_SAFE_INTEGER`。每个失败都已将相同的冻结 PHP 响应交给原前端复验，结果相同。它们是已有合同不一致，不是本轮 Go 凭空产生的值；仍然阻断“全部可由原前端消费”的结论。

总门禁 `scripts/go-migration/check.py --race --check-php-source --frontend` 因这些真实失败返回 **exit 1**，机器报告的 `passed` 为 **false**。源码兼容和浏览器合同修正的边界尚未获确认，因此本轮保持冻结对拍，不自行更改公开输出规则。

### 证据限制与交付状态

本轮平台输入均为合成的固定上游数据；HTTP 与 TLS/连接取消使用受控响应或本地连接，前端下载使用模拟字节。没有新增真实平台请求、浏览器文件落盘/哈希、手机锁屏、吞吐、部署或回滚证据。

首次 Git 推送因本机没有凭据失败；GitHub connector 创建分支返回 `403 Resource not accessible by integration`。因此本地开发分支和提交保留，但**未推送、未创建远端 Draft PR、未合并、未部署**。此处是连接的仓库写入权限不足，不是自动审批拒绝。交接时提供可导入提交包与 PR 正文，待恢复权限或由有权限的本地 Codex 发布为 Draft。

## 尚未关闭的切换门槛

上述两类公开合同冲突、其他平台迁移、真实链接双实现对照、代表性浏览器保存、微博流式协议、目标部署配置与运行条件、完整旧发布包以及实际回滚演练，均未完成。后续结果按阶段追加，保留证据类别，不用新一轮合成通过数覆盖历史未验证项。

旧发布包仍必须包含实际 Caddy/FPM 配置与配套前端产物，密钥仅记录受保护来源引用。当前源码/提交交接包不是这个旧生产发布包；本轮没有执行用户要求的最终切换前回滚演练。


## 2026-11-01 微博媒体 Range 与上游响应计划（分阶段）

- 本轮代码：`backend/internal/media/weibo/media_protocol.go`、`media_protocol_test.go`；PHP oracle 生产器 `scripts/go-migration/produce-weibo-media.php`；压缩原始 JSON fixture `testdata/php-media-baseline.json.gz`。
- 14 个 Range + 19 个响应计划固定样例；本地 Go 1.23.2 **隔离 stub** 与 PHP 8.4.24 本地预览的这些样例可逐字段一致。此本地预览不能充当固定 PHP 8.3.6 / Go 1.27.3 的正式验收。
- GitHub CI 通过原先已验证的固定 PHP 8.3.6 镜像、源 SHA 校验和逐字节基线比较；完整 Go 1.27.3/race 等门禁仍需以本提交产生的 Actions 结果为准。
- 未改现有 PHP 产品、前端、Worker、bridge、schema、生产路由与部署；微博 parse/resolve、签名改写、流式 relay、独立预算及真实平台/浏览器均 **未完成**。本阶段不得标记微博迁移通过。

- CI 首次运行 [812b95e](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41220825246)：`offline-contract` 成功；PHP oracle job 因 GitHub Actions 各 step 不继承 `DOKITOOL_ORACLE_TMP` 导出变量而在新脚本启动前失败（非 PHP/Go 对拍差异）。下一提交为独立步骤显式设置固定镜像与临时挂载环境；需以重跑结果确认。

## 2026-11-01：微博请求准备阶段（本轮新增）

在此前单 Range/响应计划的 33 例之外，追加从冻结 `25beaee` PHP `snapWeiboMediaPrepare` 实际函数抽取的 21 例正常与异常输入。签名使用既有 PHP ticket/sign/canonical 基线，原始私钥仅为已公开的合成测试值；冻结 PHP 8.3.6 镜像的 CI 必须独立重放并与版本化 JSON 逐字节比较。Go 中覆盖方法/参数优先级、票据时效、HEAD 忽略 Range、DNS 结果逐项审查与 IPv4 优先。PHP 预期不能由 Go 输出反写。

本阶段仅提交接口准备与离线测试，不代表已具备视频流式 relay、微博解析或在线验收。最终 Go/PHP CI 状态须按对应实际 Head 更新；不复用上次成功状态。

## 2026-11-01：微博受控流式路径首次实现（尚待最终验收）

Go 增加 `internal/fetch/weibo_media.go` 单目标公网固定地址 TLS 流式访问及 `internal/media/weibo/relay.go` 原 PHP legacy GET/HEAD/Range/200/206/416 错误/响应格式；接口注册到现有验证二进制服务但保持生产 PHP 不变。HTTP/1.1 原始关键头检查，八字节 ftyp 前缀审查、1 GiB 限额、300 秒请求预算、310 秒媒体写/关停、有限内存/并发和源中断后的无 JSON 污染均纳入受控测试。原 33+21 组 PHP 来源函数预期仍严格保留，不能以 Go 输出覆盖。最终 Head 的 CI 结果另记。

本轮**没有**真实微博解析器、签名媒体来源投影、受控真实 CDN 请求、完整 PHP 传输事件流对拍或浏览器保存。因此这只是二进制迁移的可执行骨架，不是微博整体完成，也不能宣称生产可切流。


## 2026-11-01 微博 URL 身份解析阶段（Draft PR #4）

独立生成器 \`scripts/go-migration/produce-weibo-target.php\` 在 PHP 8.3.6 固定镜像中调用冻结 \`25beaee\` 的未修改 \`weiboTarget()\`，禁用实际网络。47 个固定 synthetic 输入覆盖视频 fid、正文 ID、短键覆盖、数组替换、URL 编码、主机端口验证、非字符串及越界输入。**原 PHP 完整 expected 已入库** \`backend/internal/platform/weibo/testdata/php-target-baseline.json\`，不以 Go 输出生成。CI 重播 PHP 并逐字节对比入库文件，再运行 Go 对拍和原有完整离线门禁。正式 Go 微博网络 parser/adapter、匿名会话及媒体签名公开投影尚未完成；这项身份阶段并非微博整平台通过或线上验收。

## 2026-11-01：微博访客协议前置增量（CI 待核实）

由 `25beaee107f8c1a208ced9deb3c637068445e316` 的原 `backend/api/weibo.php`，在无网络、只读 PHP 8.3.6 环境中独立运行 51 个合成输入，冻结 transport failure、visitor HTML/passport 识别与 gen_callback/cross_domain JSONP 解析的完整结果，并逐字节 `cmp`。Go `internal/platform/weibo/session_protocol.go` 复刻纯协议层，`session_protocol_test.go` 读取不可变 PHP oracle 逐项比对；没有 Go 生成 expected。前期 PHP8.4 本地复制函数预览仅供建立输入，不作为最终 PHP8.3.6 运行证据；最终以同一 Head 的 CI 重放结果为准。

本增量不包含真实上游请求、Cookie jar/游客二次请求、微博媒体候选 formatter、签名来源公开投影、正式微博 Parse/Resolve 对拍及在线浏览器保存。既有 relay 与 PHP 生产保持不变。未合并、未部署。

### 本轮首次 CI 差异（已定位并修复测试桥接，待重跑）

[Actions #41235263262](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41235263262)：固定 PHP 8.3.6 无网络重放与压缩基线逐字节匹配，`bilibili-php-oracle` 成功；`offline-contract` 中 Go 客户端校验测试首次有 7 个 `nil map` 与 JSON `null` 的 Go interface 表达差异。问题只在测试桥接层的 typed-nil，不在 PHP 产品输出或 Go 业务判断。保留严格期望，测试改为在 `legacyTransportFailure` 返回 nil 时向比较器交付真正的 nil；后续完整 CI 结果另行核实。

## 2026-11-01：微博访客请求编排（PHP/Go 固定对拍阶段）

- 新增 Go `internal/platform/weibo/guest_request.go`：实现可注入的 `weiboJsonRequest` / `weiboStartGuest` 控制流程，包括首次挑战、单次访客初始化、同一目标重试、HTTP 失败与异常 JSON 不额外重试、上下文取消不调用请求器。访客调用仅使用原 PHP 两个固定 passport 端点；这仍是 parser 的内部组件，并未接入真实 HTTP/Cookie jar 或正式 Parse/Resolve。
- 28 组独立输入覆盖 status/TV 目标、成功 JSON（包括空对象/数组和大整数 ID）、不可解析数据、403/429/500、首次/重复 challenge、visitor gen/incarnate 失败与成功、一次重试、原始 POST/referrer/URL 请求序列。生成器 `scripts/go-migration/produce-weibo-request.php` 真正调用已发布 PHP `25beaee` 的 `weiboJsonRequest` 与 `weiboStartGuest`；仅将随机的 `_rand` cache-buster 值统一为 `<nonce>`，保留请求其余字段和顺序。输入、预期分别压缩入库，Go `TestPHPWeiboGuestRequestFlow` 完整字段比较；取消执行另有 Go 测试。
- CI 需要固定 PHP 8.3.6，在无网络只读容器中验证 PHP 源哈希并重新生成 28 组预期，解压后逐字节 `cmp`；新增 Go 测试纳入原本的 race/全包/格式、Schema、前端消费和构建门禁。
- 尚未实现真正的微博 cookie jar、受控上游 status/TV parser/adapter、媒体候选及签名公开投影。真实微博/CDN、浏览器保存与生产测试仍待验收。当前阶段结果必须以对应最终 Head CI 为准，不能把创建 PR 或触发 CI 算作成功。

## 2026-11-01：微博正式上游请求执行边界（待最终 Head CI 核验）

新 Go 单次请求 Cookie jar 通过已审查的 `fetch.Executor` 发起固定域 metadata 请求，逐跳保留会话 Cookie；阻止恶意跨域重定向、无关域 Cookie、非 HTTPS/未知入口以及取消后请求。新增 4 组冻结 PHP `getRequestHeaders` 独立预期并由固定 PHP 8.3.6 CI 复现；Go 模拟上游测试不等于真实微博可用性。实际 Head、失败/成功和工作流链接以本阶段提交后的结果单独登记；status/TV 产品级 Parse/Resolve 未完成。
