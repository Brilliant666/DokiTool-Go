# Go 发布候选：完全隔离的构建与回滚前置核验

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
