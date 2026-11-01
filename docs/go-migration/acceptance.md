# Go 迁移验收记录

## 2026-11-02 JST：dot第二次复验与两项边界修复

人工报告确认17ae147的原replacement来源污染已修复，但release生命周期和严格clean仍FAIL（CHAT_ACK6561169369）。本轮以共享短临界区串行化整个领取/释放，以原始HEAD树逐项对照index，保留旧失败与新反例。Windows开发互斥14/14、预检31 PASS/1 POSIX-only SKIP；完整旧Head探针独立严格拒绝断言失败且候选仍来自原HEAD。新Head完整CI/严格探针需另核，详细范围见 [生命周期与index](lifecycle-index-hardening.md) 和 [开发回执](lifecycle-index-checks.json)。未改产品后端、前端、PHP基线和安全停止项，不放行整体迁移。

## 2026-11-02 JST：串行交接与原子占用

原小时运行已确认结束并暂时暂停新触发；两批未提交改动的 14 个文件完整保存，旧工作树不动。以新的 serial-review 工作树选择性整合 Git replacement，单独提交 d996541；回滚工具仍原样保留待审，没有混合提交。已纠正旧记录中 PID 289296 的归属：它属于其他生图项目，不是 DokiTool 写入者，未作改动。

新增 execution_guard.py 原子目录占用与 owner/nonce 检查，不再把先读后写 JSON 当成互斥。九项测试含六个真实进程同时竞争、只允许一个成功，Windows 和隔离 Linux 的开发运行均 9/9；原预检 Windows 28 PASS / 1 POSIX-only SKIP。来源、日志摘要和过渡失败见 [串行机器回执](serial-integration-checks.json)，使用方式见 [串行协议](serial-execution.md)。这些是开发结果，最终提交的严格 Windows 对照和 Linux 完整 CI 须另行核对。未改 backend、frontend、旧 PHP 基线或停止项目，父 variant、真实验收与完整回滚不因此关闭。

## 2026-11-01 UTC / 11-02 JST：人工接收 dot 报告，关闭 Git replacement 读取路径

收到并核验 dot 对 `b44a21a` 的独立报告（ZIP 与 18 项载荷哈希一致，CHAT_ACK `6560540222`）：来源完整性 FAIL，Linux 落盘执行 PASS，dot 未测 Windows。该反例推翻 b44a21a 的来源完整性放行含义，不推翻其 CI 确实成功或 Linux 权限确实修复的事实。详细根因与边界见 [发布预检](release-preflight.md)。

Chat 在独立 Windows clone 实际复现旧预检误报 clean，合成嵌入资源改变最终候选。随后统一七处 Git 来源调用强制 `--no-replace-objects`，增加七项反例与完整构建对照脚本。有效修前单测为 29 项中 21 PASS、6 FAIL、1 ERROR、1 SKIP；修后为 28 PASS、1 POSIX-only SKIP。开发完整对照在 baseline/replacement 下均实际构建、执行 9 项本地 HTTP、2 项拒绝配置及发布 CLI，修后哈希一致且无替换标记；两份开发回执明确 git_clean:false，源码基线仍为 b44a21a，不冒充最终提交。

原始失败、开发摘要及附件核验见 [git-replacement-checks.json](git-replacement-checks.json)。CI 增加相同完整反例及报告留存，原 Go/PHP/Schema/消费/浏览器门禁不删减；最终准确 SHA 的严格 Windows 和 Linux CI 须以实际运行结果为准。本轮未修改 backend、frontend、固定 PHP 输入/期望/生产器/哈希及停止的断言/计数维护。父 variant 身份与真实平台、完整回滚仍未关闭；保持 Draft，未合并、未部署。

## 2026-11-01　接续修复：发布候选来源与落盘权限

在 `ce56768` 的独立审查中复现了被忽略 Go 文件参与工作目录构建，以及 Linux copyfile 发布后执行位丢失；反例和边界见 [发布预检](release-preflight.md)。本轮将后端构建改为固定 HEAD 的逐 blob 校验快照，隔离工作区/构建覆盖参数，保留只读模块与依赖校验；发布保留 mode 并实际执行落盘后的 version/catalog。没有改后端产品身份或原 PHP 基线。

新增 16 项单测，本机 22 项中 21 PASS / 1 POSIX-only SKIP；开发模式实际编译和 loopback 9 路由、2 拒绝配置、落盘 CLI 通过，266 个后端文件逐项校验，Linux 交叉产物仅 build-only。中间两次拒绝及开发 `git_clean:false` 保留于 [机器回执](preflight-integrity-checks.json)。最终干净 Head 的 CI 与本机回执需独立核对，不能用这里的开发运行替代。父 variant 身份、动态 PHP 额外入口及真实线上待验均未因此关闭。

## 2026-11-01　隔离 Go 候选预检与父版本身份审查

本轮未改变解析器或统一身份算法。针对头条/皮皮虾已确认的“备用来源集合影响父 variant ID”检查了两平台 formatter、FileVariants 和公开冻结输入：当前父身份由来源集合或其哈希派生；已有元数据/槽位名不足以证明存在独立、永久且能区分不同媒体编码的表示标识。因此**仍未修复，不跨 variant 猜配，不更新固定 PHP 期望**。审查和未完成反例见 [隔离发布预检](release-preflight.md)。

新增仅在开发机/CI 运行的 Go 构建和本地 HTTP 预检脚本及 6 项单元测试。首轮 Windows Go 1.27.3 在旧 Head ac0d1e7 源码树上编译，并真实启动 loopback 服务：九平台目录、9 个 HTTP 路由、两个不安全启动配置的拒绝均通过；另交叉构建 Linux amd64。Windows 二进制 SHA-256 为 4fe3008f97f166c0a3b16a2225d7ccd0d114d2811083fbc6188254046829a154，Linux 交叉构建物 SHA-256 为 2f27cc77dce69fb25b25f8863e8f35ddf0219856f183d5dfd129cbd62d8f2332。Linux **仅编译、未在 Linux 运行**。脚本修正后，因新脚本尚未提交，二次运行正确标注 git_clean:false；必须提交后对最终 Head 重新执行且核验对应 CI，不能拿旧构建冒充新构建。

已将此独立预检接入 offline-contract CI，保留短期脱敏 manifest；其最终执行结果必须依据实际 Actions。真实九平台回归、跨浏览器落盘、PHP→Go→PHP 回滚、生产配置及服务集成均没有因这次本机烟测而放行。未部署、未合并、未触碰公开 PHP。

## 2026-11-01　PR #8 跨入口刷新恢复补强

dot 对 `7a44c02` 的 27 项独立受控检查已通过接收核验（PR #8 评论 `6558863745`），不包含真实浏览器落盘或平台在线。其额外观察“刷新时再次点主下载另建旧链接失败任务”已转为本轮修复：按同页作品/版本/来源的明确 ID，在确认旧 Worker、resolve 和传输阶段共享占用检查。不同来源/版本/作品或新的解析结果不由此合并；无 ID 历史不猜测身份。

新增测试先在原产品上得到 12 项中 5 项失败、7 项通过；修后扩展为 17/17，前端完整 358/358（包含该 17 项），类型/构建/SSG 4/4/预算通过。实际使用 Pinia、选择和下载协调器，仅传输与 Worker 确认使用替身；不是新一轮真实下载。准确最终 SHA 和 CI 以 PR 的本次运行记录为准，不沿用 `7a44c02` 的 CI。机器摘要见 [cross-entry-recovery-checks.json](cross-entry-recovery-checks.json)。

父版本身份与备源集合耦合仍未修复，动态 PHP 额外入口的两条断言适配失败及维护停止边界仍保留。本轮恢复原小时开发任务、从当前仓库接续，状态与未完成项见 [无人值守接续](unattended-closeout.md)。未合并、未部署、不改变生产或系统网络。

## 2026-11-01　小红书 PR #5 专项复验

在修后构建 `c3be6503c7ea14ce14aa1a03d317b72815e436ec` 的正式接口中，普通四图、普通视频、自带音轨的 2160×3840 原画及 6 组 Live 均取得可识别资源，parse 与所请求 item 的 resolve 通过。初测的 standard 视频只取得 720p，chrome131 同时取得原画。真实 Edge 从原前端按默认来源完整保存原画一次，70,759,716 字节，全片解码通过。四图均显示，首图保存因长度不一致失败。MOV 原画实际落盘为 `.mp4`。这项专项复验保留了下方 PR #4 当时的失败观察。详见[专项验收](xhs-page-transport.md)和[脱敏机器报告](xhs-page-online-checks.json)。

## 2026-11-01　九平台真实在线验收（开发环境）

**19 条真实分享输入中，冻结 PHP 的首轮解析成功 13 条，Go 成功 12 条。8 个平台在原前端实际呈现了媒体，1 个抖音小文件完整保存。** 快手同一视频有成功和失败记录，小红书匿名图集、实况、视频和 rednote 来源均受阻。不能把这些结果合并为九平台全部通过。

冻结 PHP 为 `25beaee107f8c1a208ced9deb3c637068445e316`，使用 PHP 8.3.6。Go 初测为 `eee118d82b9b92af68da8ecc7edb069e621caf7d`，修后实际二进制为 `99286313c956a1fe4ef251b25112622b11164980`，均用 Go 1.27.3。两个网关分别绑定 loopback，微博共用独立测试密钥。原前端通过同源开发代理访问 Go。前端产品、Worker、bridge 和 Schema 与冻结 PHP 相同。以下时间均为北京时间，同一行内各动作在所示时段内完成。

| 平台、样本与时间 | 同输入 PHP / Go 结果 | Edge 原界面及差异 |
| --- | --- | --- |
| **抖音**：两视频、两图集，10:26–10:38 | 两端 4/4 成功。两个图集分别 11、3 个 item。修前两条视频 Go 有 20、24 个候选，PHP 为 11、18 个；修后分别同为 11、18，默认 variant 相同。最高返回档位分别为 2160×3840 HEVC 和 1920×1080 HEVC，规格来自上游。 | 初测构建实测从 2160p 切到 576p，播放器为 576×1024，播放推进 5.13 秒。该选项完整保存、可解码，见下。独立、有界的默认 4K CDN 请求返回 403；没有完整保存 4K 原画。 |
| **B 站**：普通、10P 的 P2、两作品合集，10:27–10:31 | 两端 3/3 成功，10P 顺序及选中 P2 相同。合集返回 2 个作品项。分别 resolve 另一分 P、另一合集项成功；不存在的 item 均为 HTTP 404 / `CONTENT_UNAVAILABLE`。 | 原前端由 P2 切至 P1，实际 resolve 200，播放器身份随之更新，1280×720 播放约 5 秒；B 站下载未执行。CDN 的 `trid/uparams/upsig` 等发生变化，部分主机也变，引起 variant/source ID 差异。 |
| **微博**：TV 和对应正文入口，10:27–10:32 | TV 两端各返回 6 个 file、6 个来源，最高 1920×1080。正文两端均为 `UPSTREAM_INVALID_RESPONSE`，未取得完整正文。两份响应各自的票据均用测试密钥验签通过。 | TV 在同源 `/api/media/weibo` 播放 3.33 秒，1920×1080。两端各取首 4096 字节，均为 206、总长 135,861,723 字节，`ftyp` 有效且首段哈希相同。未完整保存。 |
| **今日头条**：视频，10:27–10:35 | 两端均成功，4 个完整候选、8 个来源，默认 1920×1080 H.264。域名、路径和 `l` 查询值发生变化，resolve 返回请求的 item，两端旧显式来源均未匹配。 | 1920×1080，媒体 206，播放推进；独立 Range 客户端的 403 与 Edge 的 206 分别保留。 |
| **快手**：视频和旧报告标为图集的入口，10:27–10:38 | PHP 视频成功，图集无内容。修前 Go 两者均为 `INVALID_INPUT`。修后配对解析两者均报 `CONTENT_UNAVAILABLE`；同一视频的修后 resolve 两端均成功、保留显式来源，Edge 新解析也成功。图集未取得实际图片。 | 修后原前端返回视频，720×1280、206，播放推进。已关闭首跳误拒。短链内容仍有波动，不能记为稳定通过。 |
| **皮皮虾**：视频，10:27–10:35 | 两端 1 file / 2 来源，默认 1024×576。主机、路径或 `ft/l/x-signature` 变化，resolve item 保留，两端原显式来源均未匹配。图集缺少可追溯样本。 | 1024×576、206，短播放推进。独立 Range 客户端返回 403；未完整保存。 |
| **皮皮搞笑**：视频，10:26–10:35 | 两端 1 file / 1 来源，作品标题一致。默认 URL 仅主机不同，variant/source ID 不同。响应未声明可核实的最高规格。 | 960×540、206，播放器取得首帧。短观察窗播放时间未推进，连续播放待验。 |
| **最右**：视频，10:27–10:35 | 此样本首轮两端公开 envelope 完全相同，1 file / 1 来源，resolve item 成功。多视频和图集缺少可追溯分享样本。 | 540×960、206，播放器取得首帧。短观察窗播放时间未推进，连续播放待验。 |
| **小红书**：图集、实况、视频、rednote 原画候选，10:28–10:29 | 两端 0/4 可交付。首个图集 PHP 为 `CONTENT_UNAVAILABLE`、Go 为 `NO_MEDIA`；另三条均为 `CONTENT_UNAVAILABLE`。分享页面 HTTP 200 不能证明匿名取到了作品，token 过期、风控或上游页面差异的具体原因未确认。 | 用户确认按现有证据记受阻。真实播放、保存、4K 交付均待验。 |

### 两项定向修复与证据

1. **抖音音轨**。同一真实详情中，`audio_meta.url_list` 是 JSON 对象。PHP 根据字段次序读取了 3 个 URL；原 Go 只接受数组，丢失明确配对的音轨。只取一次详情，在相同输入、相同探针观察下重放，修前 Go 比 PHP 多出 9 个 `MISSING_AUDIO_TRACK`。修后保留有序 URL 和重复键语义，完整 formatter、normal 与 debug 均逐字段等于冻结 PHP。普通视图有 11 个 file，debug 有 11 个 file 和 9 个 mux，默认 ID 不变。两份视频在真实入口的修后复验均保留与 PHP 相同的候选数。没有新增 DASH 执行能力。
2. **快手分享跳转**。原 Go 在真实 302 跳转到快手移动作品页时拒绝请求。修复仅允许已获准分享后续访问匹配的 HTTPS 移动作品页路径，保持入口域名、逐跳 DNS/公网绑定、TLS 和读取预算。受控完整路径修前失败、修后通过。内容阶段留存两份真实页面，各自交给 PHP、Go 重放，四组均为 1 个视频、3 个候选，同一页面仅生成时间不同。两份页面自身的 CDN 主机、查询参数和来源数量不同。那次短链失败的页面正文未留存，不能把它归因于已证实的 formatter 错误。

### 身份、时效与实际保存

共 34 组 PHP/Go 配对、68 份 HTTP 响应（包括业务错误），均通过既有独立 Schema 和语义验证。19 组初测 parse、4 组修后 parse、10 组有效 item resolve、1 组缺失 item resolve。每次保留原签名地址、完整字段差异、来源顺序、默认引用及到期值。比较时保留对象字段和数组顺序；错误的空数据没有计入媒体成功。数据未提供通用 `content_id` 的场景分别核对输入、返回作品定位、标题和 item 身份。修后 Go 构建成功，抖音、快手及实际 API 网关三个包 `go test -count=1` 通过；506 个入库文件通过清单检查。

- 抖音图集的 item、variant、source 身份相同，URL 查询或主机可变。修后两条视频的 variant 身份及默认 ID 在两端一致；CDN 签名、来源 ID 和部分到期时间不同。PHP 的独立原画入口没有测得规格，Go 同一轮的原画入口实际探测到 720p；该入口与上游声明的默认 2160p/1080p 分别记录。两端重新 resolve 都可能找不到原显式来源 ID，此问题不能算作跨调用选择保持通过。
- 微博两份 TV 响应的 6 个同槽候选规格相同。3 个槽的上游 `ab/uid` 值不同，来源和 variant ID 随之变化。两个 1080p 候选同档时以 variant ID 打破平局；将两份输入分别交给两套选择器，四次重算均复现各自的默认引用。普通 API 成功、同源 Range 成功与首段哈希是三个不同层次的证据。
- B 站、今日头条和皮皮系样本保留了主机、路径、原始 query、到期值及身份变化。上述已有 URL 漂移同时出现在 PHP 和 Go；不能将同规格候选等同于同一个 source。声明了到期时间的来源，在这些配对请求时均未到其报告的期限；不能据此反推单次 CDN 403 的原因。
- **本轮唯一完整保存**是 Edge 紫色按钮的 `browser_streaming` 下载。Worker `downloads-20261031.2` 报 `completed`，卡片显示完成；落盘 **10,045,502 B**，与选中候选大小一致。计算 SHA-256 `5d0d57aabc2392e89d2c6a8409b0450f169e774623d8c2634e544e8170bd4c66`，全片 H.264 + AAC 双声道解码通过。没有外部参考哈希。该保存属于初测构建，修后验证采用同输入重放、真实解析与 resolve，没有重下同一文件。

原始输入、HTTP 响应、逐字段差异、签名 URL、浏览器截图和文件路径仅保存在本地受限证据目录。开发环境验收保留以上未通过及待验项目。未修改生产、未部署 Go、未执行生产切流或回滚演练。

## 2026-11-01：九平台原前端选源门禁补强并完整重跑

实际代码提交 **`99d7a3f186884588772e6fcc67b15124f9ad223c`**，接续 `37c853bce54dfd0950afabbb2d26758931da1dfd`，没有覆盖此前九平台实现和文档提交。[CI #41263011500](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41263011500) 的 `bilibili-php-oracle`（job `124719943846`）与 `offline-contract`（job `124719943992`）均 **completed/success**。

本次只扩展既有 `frontend/scripts/test-go-media-integration.mjs` 测试生产器，实际调用未修改的 `mediaContract` 与 `useVideoSelection`。全部 **584** 份成功 Go 响应进入原选源逻辑；另外 **334** 份预期错误继续验证原错误消息。九个平台均有非零覆盖，不能只让某个平台代替其余平台通过。

| 新增原消费者检查 | 实际执行次数 |
| --- | ---: |
| 服务端默认完整文件及其身份/URL | 618 |
| 空默认不擅自推导可下载文件 | 98 |
| 显式来源、原始签名 query、首选路由及状态 | 906 |
| 缺失 variant/source 明确拒绝，不回退到其他身份 | 1424 |
| 替换响应对象后保留当前条目和显式选择 | 1622 |

上述计数按成功响应及条目/来源统计，包含 normal/debug，不是新增真实链接数量。对象替换使用同一响应的深拷贝，**不等于平台网络续签或浏览器保存**；原抖音 4 项和微博 4 项真正受控续签回归仍单独运行并全部通过。

本次完整结果仍为 Go **3835/3835** 叶场景、race/vet/gofmt/build 通过，独立 Schema＋语义 **994/994**，原前端消费 **918/918**，原 **10** 项精确公共投影差异未扩大。固定 PHP 8.3.6 job 重新执行 workflow 列出的产品生产器并逐字节比较原 oracle；本次没有新增或改写 PHP 输入、expected、生产器或来源哈希，没有修改前端产品、Worker、bridge、Schema、平台实现或 PHP 生产。

最新机器报告 [nine-platform-checks.json](nine-platform-checks.json) 原样来自该运行 artifact `13065594701`，SHA-256 为 `de2b4f602b005e7bb4699056d598122813e40a6decbde240809dbb9c7938f054`；新增 `selection_coverage` 按平台保留明细，其余原报告段落与前一里程碑一致。下节旧报告使用固定提交链接保留。当前结论仍仅为**九平台实现与离线验收完成**；真实平台、浏览器交付、发布包和回滚门槛未关闭，Draft 未合并、未部署，原小时任务保持暂停。

## 2026-11-01：当前聊天完成九平台实现与完整离线验收

实际执行提交 **`1fdc7b73ac6e9eb54b212539fc21893e695608f5`**，对应 [CI #41262330047](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41262330047)。`bilibili-php-oracle`（job `124717949590`）和 `offline-contract`（job `124717949406`）均 **completed/success**。机器报告由该运行原样保存为 [nine-platform-checks.json](https://github.com/Brilliant666/DokiTool-Go/blob/37c853bce54dfd0950afabbb2d26758931da1dfd/docs/go-migration/nine-platform-checks.json)，保留精确整数，未通过 JavaScript 数值解析后再写回。

原小时自动任务已经暂停，最后一次自动执行正常结束；本轮由当前聊天统一接管、并行实现/独立复核、串行集成。接管前已正式注册抖音、B站和微博；本轮补齐微博签发后/前端续签验证，完成头条及余下五个平台。现在正式 Go CLI/API 注册 **9/9**；这表示实现和离线门禁完成，不代表真实平台或生产切换验收完成。

| 本轮完整检查 | 实际结果 |
| --- | --- |
| 固定 PHP 重放 | PHP **8.3.6**、`25beaee` 归档、只读无网络容器；workflow 列出的 B站、微博、头条及新增五平台/wire 重新生成并 `cmp` 一致。旧 `665bd55` 公共/抖音及 `25beaee` 增量另做来源核验和 Go 对拍，本轮未重新执行这些旧组的 PHP producer |
| Go 1.27.3 全包测试 | **3835/3835** 叶子场景；4309 个含父级通过事件，不能相加 |
| Race / vet / gofmt / CLI 构建 | 通过 |
| 独立 Schema＋语义 | **994/994**，失败 0 |
| 原前端实际消费 | **918/918**：584 成功＋334 预期后端错误；被拒成功响应 0 |
| 原抖音默认/显式续签 | **4/4** |
| 微博实际 Go 签名输出、真正 `resolveItem` 和下载快照 | **4/4** |
| 原 10 项公共投影差异 | 继续精确消费，未扩大允许范围 |
| 来源/输入/生产器/产物摘要与公开文件清单 | 通过 |

本轮新增五平台的主组共 **216** 场景、**432** normal/debug 响应；小红书 JSON wire 另列 **10** 场景、**20** 响应，未改原 37 组。所有新公开响应与原 PHP 逐字段相等，声明差异为 **0**。正式 API 使用真实 catalog、parser、adapter、handler，包含成功、错误、逐 item resolve 和目标缺失拒绝；最右期望直接来自真实 PHP `snapHandle` 的 parse/resolve，未用手改 default 代替原网关执行。

| 平台 | 独立 PHP 场景 / normal-debug 响应 | 正式 Go HTTP 叶场景 |
| --- | ---: | ---: |
| 快手 | 40 / 80 | 202 |
| 皮皮虾 | 49 / 98 | 202 |
| 皮皮搞笑 | 42 / 84 | 170 |
| 最右 | 48 / 96 | 96 |
| 小红书 | 主组 37 / 74，独立 wire 10 / 20 | 170 |

以上 HTTP 场景已包含在 Go 总数中，响应也已包含在独立/前端总门禁中。微博 35/70、头条 48/96、B站 55/110 和抖音各旧版本基线同轮重新验证，不将旧通过记录代替本次执行。实现详情、预算与文件位置见 [五平台实施说明](remaining-platforms-implementation.md)。

### 本轮修复与兼容边界

- 快手使用可选 `AutoReferer`，仅其 GET 开启，默认关闭；每跳仍检查平台授权、DNS、公网地址、绑定连接和 TLS，跨主机/指定降级场景移除认证信息。
- 真正的 fetch 原先会丢弃不跟随 3xx 的正文，而皮皮虾详情和皮皮搞笑会解析它。新增默认关闭的 `ReadRedirectBody` 仅为这两处开启，复用原有有界读取/解压/strict trailer，HEAD 不读，不访问 Location。12 个受控真实传输场景验证正文、默认/HEAD、限额、trailer 和取消。
- 小红书保留原画、多 CDN、stream/mediaV2、Live、统计零值和真实 MP4/MOV 容器读取。`xhs.com` 只在原 parser 内兼容；公开 catalog 仍预检拒绝。新增真实 PHP wire 观察修复整数形式浮点/负零、数字键 0 封面和空图片处理，没有新增数值豁免。
- 原画标签或 originVideoKey 本身不是 4K 证据；未测量尺寸保持未知，原候选按 PHP 规则保留，不统一压成 deferred，不伪造更高画质。合成 4K/MOV 容器只验证代码读取路径。
- 前端产品、Worker、bridge、schema、四项能力保护和 PHP 产品未修改。Go 生产路径不运行 PHP，不新增 DASH/合流执行器或 TLS 指纹库。

### 中间失败与修复保留

`1545095` 的 [CI #41262088458](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41262088458) 完成固定 PHP bootstrap，Go 格式门禁未通过，报告 17 个新文件；采用同一 Go 1.27.3 的实际 gofmt 输出整理，未以自制格式器替代。

`7948fff` 的 [CI #41262207908](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41262207908) 固定 PHP、新平台对拍和正式 HTTP 全部通过；两条新小红书边界测试失败，因为合成原画 key 含 PHP 规则不允许的 `.mp4` 扩展名，实际没有进入探测。仅修正该合成输入为合法 key，未改产品规则、37/10 组冻结输入或 PHP 期望。`1fdc7b7` 完整重跑后两 job 成功。

### 真实验收与交付仍待完成

本轮全部为冻结/合成上游、受控 HTTP/连接、合成容器和原前端测试模块；没有新增真实平台请求、CDN 大文件下载、浏览器文件保存、生产部署或回滚演练。尚需同一批有效链接的 PHP/Go 对照、必要的真实浏览器交付，以及含 Caddy/FPM 配置和前端产物的旧发布包与一次回滚演练，密钥只记录引用。真实 4K 新传输、合流执行器保持独立范围；PR #11 自动恢复的真实浏览器待验收项也没有在本任务中冒充关闭。

当前 Draft PR #4 继续以 `codex/bilibili-go-migration` 为 base；PR #1 → #2 → #3 → #4 依赖链未合并。公开 PHP main 和生产保持原样，Go 没有发布。原小时调度继续暂停，避免在已完成离线实现后空转或与当前聊天同时更新分支。

## 2026-11-01：聊天接管，头条与微博完整离线门禁通过

自动调度已暂停，最后一次自动执行正常结束；后续由当前聊天统一集成，避免并发更新同一分支。接管远端 Head 为 `30312d27ec983a34b173672ef2e4284506ac36ac`。本阶段验证提交 `2dbad5da563b32b664ae35741ca5df1661e6d0f8`，[CI #41260973668](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41260973668) 两个 job 均 completed/success；未合并或部署。

- **头条**：新增正式 Go parser、CLI/API 注册；固定 PHP 8.3.6 执行原 `toutiao/toutiaoRequest`、入口 JSON 和原 adapter/public，48 场景、86 请求生成 96 份独立公开预期。所有字段、来源顺序、身份、HTTP 请求和共享 35 秒预算严格对拍，声明差异为 0。正式 HTTP 另有 176 场景，包含解析、指定条目 resolve 与缺失目标拒绝。
- **微博**：追加真实旧 PHP gateway 签发后的 35 场景/70 份 normal/debug 响应，与实际 Go HTTP 逐字段相等，声明差异为 0。修复可选 TV 补充超时丢失已取得的视频、计数大整数舍入、业务错误优先级/数组 urls，以及 Range 首字节前错误提交成功媒体头；35 个新增精确整数 PHP oracle 与受控 EOF/截断回归保持冻结预期。
- **总门禁**：Go **2239** 个叶子场景、race/vet/gofmt/构建通过；独立 Schema＋语义 **542/542**；原前端实际消费 **466/466**（310 成功＋156 预期错误），原有续签 **4/4**，微博默认/显式选择及真正 `resolveItem` 续签 **4/4**。原 10 项精确公共投影差异未扩宽。机器报告见 [takeover-toutiao-weibo-checks.json](takeover-toutiao-weibo-checks.json)。

首轮 `7487ef6` 的 Go 和固定 PHP 对拍通过，但新增前端测试误把单媒体可省略的 `default_item_id` 当必填，4 项失败；仅修正测试识别单条目，未向响应增加字段、未改产品前端。最终提交已重新完整验证。固定 PHP 数字/请求来源不能被 Go 期望反写；旧 fixture/producer/hash 未改。

该阶段结束时正式注册 **4/9**：抖音、B站、微博、头条；其余五平台随后完成，见本文件顶部最新结果。证据全部为冻结或受控输入，没有新真实平台请求、媒体下载、浏览器保存、生产部署或回滚演练。小红书新增 TLS 指纹/4K 端到端专项及合流执行器仍不在范围。

## 2026-11-01：微博正式 Go Parse/Resolve、来源签发与 PHP 公开投影对拍

代码验证点 `aca70af8e21c29415e36e2ded23e4ef1c8772743`，对应 [GitHub Actions #41257671113](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41257671113)：`bilibili-php-oracle`、`offline-contract` **均 completed/success**。首次接入正式微博 `Parser.Parse`、`cmd/dokitool` 注册、`/api/parse` 与 `/api/resolve`、统一 `PreparePublic` 安全签发及已实现的 `/api/media/weibo` GET/HEAD/Range relay。新签名源触发服务端 default 重新选择，修正中间合同校验中的旧直链引用，未改变公开 schema、前端、Worker、bridge 或原 PHP 生产。

**独立对拍**：固定 PHP 8.3.6 无网络容器从冻结的 29 组原 PHP formatter 结果，重新执行原 `snapAdaptWeibo`、`snapFinalizeData` 和 `snapPublicMediaView`，生成 29 个完整产品场景 × `internal/normal/debug` = **87 份原 PHP 公开适配预期**；原始基线 `php-public-baseline.json`（含旧输入）只由 PHP 生产，正式 CI 每次逐字节 `cmp`，Go 独立逐字段对拍三种输出。此前的 16 组 status/TV 请求序列、29 组 formatter 及微博签名/媒体票据原 PHP 基线继续原样保留。

正式 Go HTTP 合成上游测试覆盖 status 的正常/debug `parse` 和 `resolve`、固定官方 GET、请求策略/截止、媒体票据签发验证、稳定 variant/source ID、默认选择、缺失签发密钥和非法链接拒绝。测试直接经过原 API handler、独立 Schema/语义；全包 Go race/vet/gofmt/build、来源清单和既有未改动前端通用门禁在最终 CI 通过。早期中间提交曾因 gofmt 和签名后的旧默认 URL 而失败，已修正并在最终代码点重新验证，没有放宽原 PHP 预期。

**仍待验收**：上述公开投影 oracle 是**签名前**、固定时钟的完整产品适配对拍；完整旧 PHP gateway 签发后的线上公开响应，还需要同源有效样本对照。当前未做真实微博/受限匿名会话/CDN 下载、浏览器保存、Go 输出专属前端端到端消费或生产切流。微博代码集成达到离线阶段性门槛，**不能据此标记整个平台真实验收完成**；其余六个平台不受影响，继续逐一迁移。机器摘要见 [weibo-product-checks.json](weibo-product-checks.json)。


## 2026-11-01 微博 status/TV formatter 与完整请求编排的独立 PHP/Go 对拍

实际代码 Head `c2c574fd3a38d5070fe76bb3a49f903595828c1e`，[最终 CI #41253090716](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41253090716) 的 `bilibili-php-oracle`、`offline-contract` **均 completed/success**。冻结 PHP 来源 `25beaee107f8c1a208ced9deb3c637068445e316`，固定 PHP **8.3.6**，无网络只读容器实际调用原产品函数并重新 `cmp` 入库预期，未修改旧基线。

新增 **29 组完整私有 formatter**（`formatWeiboStatusResponse`、`formatWeiboResponse`、`weiboVideoResult`）与 **16 组完整 status/TV `weiboResolve` 请求带和解析结果**：覆盖成功与失败、画质候选、作者/时长/统计已知零值、TV component POST、status GET/补充、匿名访客挑战/初始化/仅一次重试、可播放内容的错误兜底、请求顺序/方法/Referer/POST 字节。只规范化不参与身份的随机 `_rand`。Go `TestPHPWeiboFullFormatter`、`TestPHPWeiboFullResolveFlow` 全量逐字段对拍，raw JSON URL 来源顺序来自原始字节。最终 CI 包含全包 Go/race/vet/gofmt/build、独立 Schema 和未修改的前端通用回归。

最初 [CI #41252623902](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41252623902) PHP 通过、文件清单失败：新生成的 `.gitignore` 根规则笔误 `//*`；已修正并在最终 CI 验证，无放宽比较。中间被新提交取消的 CI 不作完成证据。

**范围限制**：这仍是内部解析链路，尚未把微博注册进正式 `/api/parse`/`/api/resolve`，也未接入签名来源公开投影与前端下载。当前前端门禁只覆盖通用/此前已注册平台，并非微博正式 HTTP 输出的完整验收。真实微博、CDN、浏览器和生产均未测试。机器摘要见 [weibo-formatter-resolve-checks.json](weibo-formatter-resolve-checks.json)。

## 2026-11-01 微博候选与画质元数据独立 PHP/Go 对拍

本阶段实际代码 Head `ef98b6b662abccba68d1ac30d3c8bc26afe02e9b`，提交了 Go 候选抽取、原 PHP 输入生产器、48 组由真实 PHP 8.3.6 产品函数产生的冻结预期、完整 Go 对拍测试及 CI 来源核验。未更改 PHP 产品、统一 Schema、原前端/Worker/bridge、四项能力保护或生产部署。

| 验证层次 | 当前已核实 |
| --- | --- |
| 冻结 PHP 来源 | `backend/api/weibo.php` 与已发布 `25beaee` 文件哈希一致 |
| PHP 产品基线 | 23 单候选＋13 计数＋12 来源聚合 = **48 组**；PHP 8.3.6、无网络、只读容器中重放并与原件逐字节一致 |
| Go 私有合同 | 48 组 Go 候选、数值、来源合并及标签/质量/排序语义逐字段对拍；另外验证等等级来源的保序 |
| CI | [最终代码 CI #41248677685](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41248677685)：`bilibili-php-oracle`、`offline-contract` **均 completed/success**；其中包含 Go 全包/race、vet、gofmt、build、独立 Schema 和原前端回归 |
| 中间失败 | [#41248574558](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41248574558) 的 PHP job 通过、Go job 因 4 项 typed-nil 比较失败；仅修正测试桥接，PHP 原件和产品代码均未改 |
| 未完成 | 候选代码尚未接正式微博 Parse/Resolve；TV/status formatter、上游 JSON 的源顺序保留、签名来源投影、真实平台/浏览器验收仍待完成 |

机器摘要见 [weibo-candidates-checks.json](weibo-candidates-checks.json)。未将合成 HTTP 或候选推断当作真实平台质量证据，不得标记微博迁移完成。

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
