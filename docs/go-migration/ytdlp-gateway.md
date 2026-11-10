# yt-dlp 任务网关与主机隔离

本修复接续 [#36 兼容任务](https://github.com/Brilliant666/DokiTool-Go/pull/36#issuecomment-6684808322)，属于原 `pr36-acceptance-conditional-release-v1` 的前置修复，不是新发布流程。生产启用仍须补丁和精确规则通过审查、确认新的固定候选，然后完成日本取址及异网浏览器完整保存。网关接通不代表平台或媒体验收通过。

## 配置与任务边界

`DOKITOOL_YTDLP_GATE_PORTS` 为逗号分隔的显式端口集合。仅绑定 `127.0.0.1`，不支持地址、端口范围、重复端口或通配符。集合须与共享 `DOKITOOL_YTDLP_CONCURRENCY` 匹配；默认并发为 2，部署应规划两个空闲专用端口，避开主机临时端口范围和既有监听。具体值由私有部署差异给出，不将示例值当作生产默认值。

各平台通过 `WithTargetPolicy` 共享端口池和并发配额，保留独立目标策略。每次提取租用一个端口，关闭监听、HTTP 处理和 CONNECT 隧道后才归还。端口被占用或池耗尽时明确失败/繁忙；不杀占用者、不连接未知服务、不随机回退、不绕过网关直连。没有配置集合时保留动态回环兼容模式，只适用于明确允许该通信的环境；日本部署必须显式配置受审固定集合。

每个任务使用随机生成的临时代理凭据。HTTP 和 CONNECT 在 DNS/拨号前验证 `Proxy-Authorization`；缺失、错误和上一任务凭据拒绝，代理凭据不转发到上游，不写入日志或 API。取消/结束关闭该网关，复用端口会生成新凭据。凭据随代理参数交给锁定 yt-dlp：不得采集或公开子进程完整参数、调试代理 URL 或请求头。这不是针对 root 或已控制同 UID 进程的隔离，也不替代主机出站策略。

原平台目标白名单、公网 DNS/IP 校验、80/443 端口限制、TLS 验证、逐跳检查保持。`DOKITOOL_YTDLP_PROXY` 继续留空；生产不得启用测试 `DialAddr`。不使用服务器媒体中继。

## 待审最小 nft 差异（占位符，不可直接执行）

以下两条规则须位于现有 UID 隔离链的本地目标拒绝之前。`SERVICE_UID`、`GATE_A`、`GATE_B`、表/链与插入位置须由现场记录替换，并同配置集合逐项核对。

```text
meta skuid SERVICE_UID oifname "lo" ip saddr 127.0.0.1 ip daddr 127.0.0.1 tcp dport { GATE_A, GATE_B } ct direction original ct state { new, established } counter accept comment "dokitool-ytdlp-gate-original"
meta skuid SERVICE_UID oifname "lo" ip saddr 127.0.0.1 ip daddr 127.0.0.1 tcp sport { GATE_A, GATE_B } ct direction reply ct state established counter accept comment "dokitool-ytdlp-gate-reply"
```

第一条仅允许子进程向精确网关端口发起连接及后续流量；第二条仅允许同一回环方向、网关源端口的已建立回复。不能替换为整个 lo、127/8、高端口段或全局 ESTABLISHED 放行。原 API 回复、DNS、内部入口、本地目标/私网/元数据拒绝均保留。Go 和 yt-dlp 不获 CAP_NET_ADMIN，不动态修改规则，不以 root 运行产品。

私有审查清单需包含：完整原规则快照与摘要、所有相关 base chain 的 hook/priority、实测 UID、端口占用/临时范围、精确插入点、持久化文件差异与唯一规则标记。`nft --check` 只证明语法，不证明实际收发。

撤回时先按原发布流程停止使用该网关的候选/新平台，读取当前规则并核对唯一标记、完整表达式及实际 handle，只删除本次两条规则。持久化文件只撤回对应两行，不覆盖之后的未知改动；不要 flush ruleset、删除整表或全局放行。核对原拒绝行为恢复、原 API 回复和既有出站策略不变。候选 API 的回复许可是另一项独立差异，不包含在这两条网关规则中。

## 验证与交付范围

Go 定向测试覆盖配置、共享端口租约、并发忙、HTTP/CONNECT 认证前置、跨任务凭据失效、目标策略和任务收尾。锁定发行版 yt-dlp 的握手使用受控端点，不请求真实平台或媒体。

隔离 Linux 验证在独立网络命名空间内重建拒绝基线，产品/探针以非 root 身份运行，管理进程仅为测试设置命名空间及规则：先复现拒绝，加入精确例外后验证收发及计数，再验证有监听的邻接端口、其他本地地址和私网/元数据仍拒绝，最后撤回并复验。准确执行结果以 PR 的 Head CI 为准，未执行或跳过的部分必须明确列出。

本阶段仅交 Draft PR；不改日本规则、安装生产依赖、重启服务或切流。旧九平台差分、T1/T2/T3、R1–R6/C1/C2 继承并运行受影响回归，不重复九平台现场媒体矩阵。前端、Worker、上海、冻结 PHP、容量与媒体源不变。

## YouTube 助手与 yt-dlp 升级同步要求

升级锁定版本 `yt-dlp` 时，必须同步核对并一起更新 YouTube 助手服务端（`backend/internal/platform/youtube/`）中的 `PinnedEJSVersion`、两个 `ejs` 资产的 SHA-256 哈希（`OfficialEJSLibSHA256`、`OfficialEJSCoreSHA256`）以及 `TVHTML5ClientVersion`（同时保持前端/用户脚本允许的 `TVHTML5_VERSION_RE` 兼容）。
