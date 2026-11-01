# 单图长度与 MOV 命名验收

2026-11-01，北京时间。开发分支从 PR #5 的 `7694afeba7e2cff120a0e30805d28615e601fd89` 接出。本轮实际产品提交为 `61f65656d175bd8fa96372a6add349231ef4c235`。使用 Edge 156.0.4314.50、原前端、PR #5 的 Go 构建和独立浏览器配置目录。以下线上样本均为同一图集的首张 JPEG。

## 原界面真实请求

| 实际观测 | 旧 Worker `downloads-20261031.2` | 新 Worker `downloads-20261101.1` |
| --- | --- | --- |
| 同一次请求的网络头 | HTTP 200，`Content-Encoding: br`，`Content-Length: 156177`，`Access-Control-Allow-Origin: *`，`Access-Control-Expose-Headers: Content-Length` | 同样的状态和头 |
| Worker 的 Fetch 响应 | 类型为 `cors`，可见长度 156,177，读不到编码头 | 相同。该源没有另外声明文件大小，Worker 记录长度未知 |
| 流及页面终态 | 已交付 131,835 B，下一块 25,107 B。累计读取 156,942 B，在交付下一块前报错。未观察到先行取消、读取异常或 EOF。随后请求被中止，页面和 Worker 均失败 | 读取、交付各 156,942 B，观察到正常 EOF 和网络完成，页面和 Worker 均完成 |
| 实际保存 | 无完整文件 | 原界面单击一次，落盘 156,942 B，JPEG 完整解码为 1080×1440 RGB |

旧实现把压缩响应的长度用作解码字节的上限，并写进浏览器合成附件。`loaded` 在越界判断之后才增加。这解释了 PR #5 首次记录的 133,563 B，以及本次复现的另一分块边界；两个数字都是截至报错时已交付的字节数。真实样本的本地 SHA-256 为 `9298904596f63b14fcca4abfb96c72f1cad4edcb25e5a3cd9abd935dd75edf53`。没有独立的线上参考摘要。

现在先取当前来源自己声明的媒体文件大小。HTTP 长度仅在编码明确为 identity，或响应头未经 CORS 过滤且没有编码时参与校验。两种可信声明如果冲突，该来源不能交付。未知长度时合成附件省略 `Content-Length`，显示实际收到的字节，等待 EOF。实际断流、越界、提前 EOF 或取消各自保留相应终态。来源切换逐 URL 取大小。正常 EOF 描述浏览器读完流，本次 JPEG 的落盘、哈希和解码另行验证。

## MOV 与受控跨域对照

PR #5 的原画文件已有完整保存证据，70,759,716 B，QuickTime `qt  `、2160×3840 HEVC、AAC。原 API 给出 `mov`、`video/quicktime`，原 Worker 按 CDN 的 `video/mp4` 改为 `.mp4`。本轮沿用原文件及原探测。现在从所选候选保留有依据且相互一致的格式信息，在 Worker 的附件 MIME、任务名和安全文件后缀上使用同一结果。缺少可信格式时继续使用响应 MIME，危险扩展不能成为附件后缀。

17 个受控场景使用真实 Chromium、两个 loopback origin 和实际 Worker；用客户端任务查询、附件响应头、磁盘文件名和固定夹具字节分别核对。具体输入见[可复用脚本](../../scripts/test-download-browser.mjs)。

| 场景 | 实际结果 |
| --- | --- |
| CORS gzip 可见 / 隐藏 | 两种响应在线上均为 339 B，浏览器解码并落盘 196,957 B 的 PNG。合成附件均省略长度，文件 SHA-256 与夹具一致 |
| 未编码、identity、未知长度 | basic 且无编码以及显式可见 identity 都保留长度。普通 CORS 无编码头和无长度的流均完整保存 |
| 截断、超长、提前 EOF、取消 | 真实断流在 4,096 B 后失败。超长尾块未交付，提前 EOF 失败。取消在 4,096 B 后为 cancelled |
| MOV 的 CDN MIME 为 MP4 或缺失 | 分别以 `mov-conflict.mov`、`mov-no-mime.mov` 落盘，任务名和附件名一致。两份均为同一 798 B 合成夹具，SHA-256 `910e338ee0723fc2157964ec3d4914a1e3fee8f1ba88a04038c9c225debf21a4` |
| MP4、图片、回退 | 843 B 的 MP4 和 196,957 B 的 PNG 均以正确后缀落盘。无可信格式时使用响应类型，未知 MIME 配危险后缀回退到安全名称；可信 MOV 与明确的图片响应冲突时拒绝交付 |

本机前端测试 317/317，类型检查、生产构建、SSG 和加载预算通过。完整 PHP、Schema、前端消费与 CI 浏览器测试见本 PR 最终 Head 对应的 Actions 运行。Live 整体保存及其他平台跨调用身份仍单列待验。

[首轮 CI](https://github.com/Brilliant666/DokiTool-Go/actions/runs/41274637153) 的六个续签用例触发了新的长度校验。它们使用真实适配器响应，但模拟传输恒定返回 4 B，而相应来源声明的大小分别为 2,500,000、8,000,000 或 16,000,000 B。测试传输现按所选来源提供对应字节，冻结响应和身份断言均保留。修正后本地实际 Go 续签对照 8/8，PHP 8.3.6 与前端集成 13/13。此修正只涉及测试夹具，浏览器实测对应的产品代码相同。

受限证据位于独立仓库的 `.local/download-delivery-closeout/private/`。其中 `original/real-image-acceptance.json` 是真实前后对照，`original/new/outcome.private.json` 保留实际文件路径，`browser-control-final/browser-results.json` 是受控浏览器的逐项回执。签名、真实标题、媒体文件与原始网络记录均仅保存在本机。
