# Go 发布候选：完全隔离的构建与回滚前置核验

## 2026-11-01 UTC / 11-02 JST：dot 反例与 Git replacement 修复

维护者人工转交 dot 对 `b44a21a890b6f634ba3e35c6e9625508a6c3a063` 的独立报告，ZIP SHA-256 `4b7d5739e6600bbd389f6338f0dbf399cd5d3c18c2067cd7172de503bbdd8bcc`，18 份载荷摘要全部核验；接收评论 `6560540222` 是 CHAT_ACK，不是新派单。该报告证明 **b44a21a 的来源完整性仍不通过**，同时独立确认 Linux 0755 与落盘 CLI 执行通过；dot 本轮没有执行 Windows、平台或浏览器测试。不能再用下方历史 CI/开发记录将 b44a21a 的来源完整性标为通过。

根因是普通 Git 命令会解释 replacement 引用。替换 `backend/config` 子树后，HEAD 与普通 status 可保持不变，而 ls-tree 列出的新 blob 自身哈希仍然有效；逐 blob 检查因此不能证明这些 blob 来自原始 commit→tree。Chat 在新的独立 Windows clone 也实际复现：原预检两次均退出 0 且报告 clean，但替换候选包含合成显示名、二进制哈希改变，独立来源断言退出 1。没有证据表明用户实际工作树已被污染。

修复把预检中全部七处 Git 调用统一经 `git_command()` 生成，强制命令级 `git --no-replace-objects`，覆盖根目录/HEAD、前后 status、recipe 登记、递归树枚举、batch blob 和 backend tree 记录。自定义 `GIT_REPLACE_REF_BASE` 同样不能改变来源视图；不要求用户手工设置环境，不删除 replacement refs，不修改全局 Git 配置。原 blob 哈希、只读依赖、dirty 拒绝、Go 环境隔离、发布权限与落盘执行检查不放宽。该选项语义见 [Git 官方 replacement 文档](https://git-scm.com/docs/git-replace.html)。

新增七项定向回归：backend 树、嵌套树、自定义 replacement 命名空间、commit、blob、替换视图掩盖 staged/worktree 改动，以及全部来源调用使用一致策略。修前有效夹具结果为 29 项中 21 通过、6 断言失败、1 blob 哈希拒绝异常、1 POSIX-only 跳过；不是七种已证实安全漏洞。修后 Windows 为 **28 PASS / 1 POSIX-only SKIP**。最初 Windows 测试辅助器以文本 stdin 传递 mktree，产生 CR 文件名，已改为原始字节后重新取得修前失败；首轮日志保留，不把夹具错误计作产品反例。

可执行的完整候选反例在 `scripts/go-migration/probe_git_replacement.py`：仅在新独立 clone 中安装合成 replacement，实际构建/运行/发布基线和替换候选，对比完整二进制哈希及 catalog 内容；原 source 的工作区和 refs 不写入。输出目录必须是新目录，仓库内只允许 `.local/`。开发模式可用 `--recipe` 替换克隆中的预检脚本，明确标记 override 与 `git_clean:false`，不能冒充最终提交。当前开发对照两次预检成功、哈希恢复相同且无替换标记；原始失败和开发结果见 [独立机器摘要](git-replacement-checks.json)。

现有 CI 保留全部原门禁，并新增 Linux 完整候选反例及独立短期 artifact。**最终提交的严格 Windows 对照、Linux 29 项及完整 CI 必须读取对应 SHA 的实际结果；这里不预报成功。** 可复验命令（Go 需为绝对路径，输出目录不得已存在）：

~~~powershell
$sha = git --no-replace-objects rev-parse HEAD
python -X utf8 scripts/go-migration/probe_git_replacement.py --source . --ref $sha --go $go --output-dir .local/git-replacement-check
~~~

边界仍保留：这不是对所有 Git 环境重定向/配置、Git 可执行文件、对象数据库、工具链或依赖缓存的全面安全认证；也不关闭复用发布目录失败后旧 manifest 与新文件组合的待核实问题，下游必须核对本次退出码及每个产物哈希，不能只看 manifest 存在。没有改后端/前端、固定 PHP 及受阻断言/计数，父 variant 身份、真实平台与完整回滚仍待验。人工报告已接收，不声称 dot 自动回传已打通。

## 2026-11-01 接续修复：固定源码与落盘可执行性

对 `ce56768` 的独立审查复现了两个缺口（PR #8 评论 `6560148006`；同轮自动审查 `6560128010` 也报告了第一项）：白名单 `.gitignore` 隐藏的额外 Go 文件会参与工作目录构建；Linux 使用 `copyfile` 发布后会丢失执行位。前者在独立 Git/Go 1.27.3 沙盒中保持 HEAD、git status 和版本输出不变，却实际改变二进制；后者在 Linux 小型可执行夹具上由 0755 变为 0644、执行报 errno 13。它们不是本机已被污染或真实平台故障的证据。

当前预检不再从原工作目录编译。它对固定 HEAD 执行 `ls-tree` 和 `cat-file --batch`，将后端的所有普通文件按原始 Git blob 内容物化到新临时目录，逐个校验对象哈希；符号链接和子模块明确拒绝。未提交/被忽略文件不会加入快照，go:embed 资源也按原始字节保留。归档的 export-ignore/export-subst、Windows 换行转换不能改变构建输入。报告包含固定提交、后端 tree ID 和实际核验文件数。

构建子进程关闭外部 GOENV、GOWORK、GOFLAGS、自动工具链和本地 CGO，保留既有缓存与网络策略；只读模块构建并在构建前后验证模块，拒绝依赖源码树外部的本地 replace。构建后重新检查 HEAD/工作区状态，不修改用户全局配置。**`--allow-dirty` 仅允许开发预检脚本的验证，仍编译已提交 HEAD，不包含未提交的后端改动；报告 git_clean:false 不能作为正式候选。**

发布使用保留文件权限的复制，逐项核对大小对应的字节哈希和 mode；Windows 临时文件保留 `.exe` 后缀。落盘后重新实际执行原生程序的 version/catalog，通过后才写成功 manifest。Linux 执行位用例必须在 Linux 执行，Windows 跳过不能算通过；Windows 交叉构建的 Linux 文件仍不等于 Linux 运行验收。

新增 16 项单测，与原 6 项合计 22 项。本机 Windows 已执行 21 PASS / 1 POSIX-only SKIP，开发模式原生构建、9 个 loopback HTTP 路由、2 项拒绝配置及落盘 CLI 均通过，266 个后端文件逐 blob 校验；Linux 交叉构建只记 build-only。最终干净提交的本机与 Linux CI 结果须以 PR 对应提交和 artifact 为准，不能拿开发运行冒充。开发回执及保留的中间失败见 `preflight-integrity-checks.json`。

第一版实现曾因 git archive 的换行转换与原始 blob 不同而正确拒绝，随后改为直接读 Git 对象；第二版曾因 Windows `.exe.partial` 的扩展名导致 mode 不同而拒绝，随后保留临时 `.exe` 后缀。失败日志保留，没有通过放松对象哈希或 Linux 权限断言解决。

本次仅修复发布预检工具与相关测试，不改产品解析器/前端/身份算法，不触碰此前停止的 PHP 断言和计数维护，不改变父 variant 身份仍未修复的结论。

此步骤只在开发机/CI 进行，**不是生产上线许可，也不是回滚演练**。真实生产切流仍受 [rollback.md](rollback.md) 的完整 PHP 发布包、运行配置、代表性解析/媒体保存和签名票据回滚要求约束。

## 先行身份审查（2026-11-01）

在 PR #8 代码中，FileVariants 使用原平台提供的 private_media_identity 作为稳定父 ID；没有该身份时，private_representation_id 或来源 ID 集合决定父 ID。头条 candidates 将当前版本的 main_url/backup_url/backup_url_1/backup_url_2 排序并哈希作为 private_representation_id；皮皮虾则哈希同个上游视频容器的 url_list/download_list 集合。因此 **新增/删除一个未选备用地址就可能改变父 ID**，即使旧 source 仍存在。现有六组探针记录为新增/改变备源 4/4 保持性失败，纯重排 2/2 正常。

复核固定公开输入（backend/internal/platform/{toutiao,pipixia}/testdata/inputs.json）后，头条有 video_list 槽名及 video_meta 中的 width/height/bitrate/size/codec；皮皮虾有 origin_video_download/video_download/video_high/... 槽名、宽高与 URL 列表。**这些是能力/档位的标签或可变元数据，不是已证实的同一编码内容的不可变表示 ID。** 即使将 definition、档位名或宽高作为候选标识，也还缺少它们等于不可变编码内容身份的证据。不能凭视频槽名、宽高、第一 URL、去掉全部 query 或对另一个 variant 作前端猜配来保持旧身份。公网样本和冻结合成输入也不能互相冒充。

下一步应取得有来源、时间及原始字段的**同一作品两次上游表示**，验证是否存在由平台明确提供、能区分不同编码且持久不变的表示 ID。需要包含同槽不同内容、改变选中源、增加/删除备源、签名 query 更新、重复同档不同编码等反例。只有取得这种证据才修改 ID 算法并增加独立严格断言；否则继续明确失败。现有固定 PHP oracle、字段级对拍及原始 expected/producer/source hash 保持只读。此次不宣称父版本身份已修复。

## 本机候选构建与离线 API 烟测

在本仓库根目录执行：

~~~powershell
$go = "C:\Users\brilliant\Documents\ChatGPT\DokiTool-Go\.tools\go1.27.3\go\bin\go.exe"
python -X utf8 scripts/go-migration/test_preflight_release.py
python -X utf8 scripts/go-migration/preflight_release.py --go $go --output-dir .local/release-preflight/native
~~~

在**干净工作树**上，preflight_release.py 绑定实际 HEAD 到 Go 版本字符串，使用固定 Go 1.27.3 编译原生候选，并且先在随机 loopback 端口启动真实 cmd/dokitool。它验证版本与 CLI/HTTP 目录一致（九平台、不重号），测试合法目录及八种错误/退役路径的 HTTP 状态、JSON code、Cache-Control/CORS/nosniff 安全头，并验证公网监听与未知小红书页面传输模式被拒绝。使用的只有没有分享链接的非法参数和静态目录，**不会发起任何真实平台请求或媒体下载**。

预检使用临时目录构建和测试，全部通过后才将原生二进制和 preflight-manifest.json 存入指定输出目录。候选的 SHA-256、提交、工具链、平台集合、原生烟测逐路由结果保存在报告中。若仓库内存储，输出位置必须在 .local/，不能让编译产物进入公开文件清单。开发过程可使用 --allow-dirty，但报告会明确 git_clean:false，**不能作为发布候选**。

在 Windows 开发机可附带 --linux-amd64 交叉编译 Linux amd64 候选，报告会对两个文件分别计算真实 SHA-256，且标记 Linux 候选 native_smoke_tested:false。**交叉构建通过不代表 Linux 运行、权限、服务集成或生产目标架构已经验证。** 后续仍须在匹配目标机的隔离环境重新执行 smoke、配置和 rollback 演练。不得将本机输出目录当作生产回滚包。

### 未关闭的切流阻断

- Go 目标环境的 OS/CPU、服务账户、真实 Caddy 片段、前端静态产物、签名密钥来源及保密引用尚未在这份本机预检中核实。
- 未在隔离环境用**实际旧 PHP 发布包**完整演练：PHP → Go → PHP，并验证签名票据跨切换兼容、静态下载路由与代表性媒体落盘。
- 头条/皮皮虾父身份问题、动态 PHP 额外入口两项被阻断的断言维护继续独立列出；原测试安全停止边界不解除。
- 本地 HTTP 烟测、受控浏览器与真实平台证据分别存档，不合并成“九平台生产可发布”。

整个流程不修改公开 PHP、不改系统网络配置、不合并、不部署生产。
