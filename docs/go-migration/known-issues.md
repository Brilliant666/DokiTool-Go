# Go 迁移：未关闭的基线合同问题

这些记录保留“相同输入与 PHP 相同”和“现有客户端可以使用”的区别。
冻结期望不因 Go 的结果而重写；基线问题仍使独立门禁失败，不能作为已获准的忽略项。

## 1. Live Photo 交付 URL 含 fragment

状态：**已复现、确认来自 PHP 基线，尚未修复；需要确认迁移允许的基线修正边界。**

### 可重复证据

- 输入：`backend/testdata/php-douyin/cases.json` 中 `live/direct-and-still`。
- 该合成上游返回一个以 `#fragment` 结尾的 Live Photo motion URL；已有 PHP formatter 在 fragment 前添加 `mime_type=video_mp4`，随后 adapter 保留原 fragment。
- 固定 PHP commit 的 normal/debug 公共响应与实际 Go 响应逐字段一致；实际 API Handler 对拍也保持一致。
- PHP `backend/lib/media-contract.php::snapValidateSource` 不拒绝这个 fragment，兼容实现 `internal/unified.ValidateSource` 也接受。
- 原前端 `frontend/src/utils/mediaContract.ts::sourceUrl` 拒绝非空 `parsed.hash`；原独立 Python `scripts/validate-unified-schema.py::validate_url` 同样拒绝 fragment。
- 两份成功响应被现有前端整体拒绝，不能只因其他两个图集条目可用就认定该作品可消费。

证据是合成的固定输入，不说明当前线上抖音一定返回这类 URL；它证明原后端合同与客户端合同存在可复现的不一致。

### 当前处理

保留原输入、PHP 期望与 Go 兼容行为；不修改前端、不放宽独立校验、不删掉失败场景。
`scripts/go-migration/check.py` 保存两个失败的 case/view 和原因，并返回非零状态。
`frontend/scripts/test-go-media-integration.mjs` 还用同一前端校验器验证冻结 PHP 响应，明确标记两端相同且同样被拒绝。

### 建议的修复边界（尚未实施）

建议保留前端与独立验证的规则，后端仅对媒体交付 URL 的 literal `#...` 做明确处理；不得解码或改变 `%23`、签名 query、重复参数、顺序或其他 URL 字节。修正必须覆盖递归 delivery 的所有 Source 以及相应的 `default.url`，不能只修主链接，也不能靠整包报错掩盖可用媒体。

原始 URL 参与 source/variant 身份计算，因此建议在**既有身份计算、来源合并和事实聚合完成之后**做交付 URL 投影：保留 item/variant/resource/source ID、数组顺序与来源资格，只修改已识别的 direct Source.url，随后让原默认选择流程从修正后的 source 生成 `default.url`。投影结果不能重新进入 `Sources`/`FileVariants` 计算身份或去重；两个旧 ID 投影到同一 URL 也不顺便合并。

只读复核确认：当前来源合并以 ID 为依据，资格看 availability/expiry；前端 resolve、显式选择和下载快照按 opaque ID 匹配，不会用公开 URL 重新计算 ID。这使后置投影可以保留现有身份规则。具体实施仍须覆盖递归 delivery、与过滤的先后关系，以及原 Handler 在公共投影前进行的完整候选校验；不能先收紧校验而让投影无法到达。

原 PHP oracle 保持不可变，修正另加明确的 URL 字段差异记录及续签回归，不把差异藏在“Go 默认 URL 解析不同”中。

由于用户已经确认固定输入严格等价，本轮没有自行将这个基线缺陷改为新的输出规则。

## 2. 极限文件大小超出原前端的安全整数范围

状态：**大整数转换已与 PHP 对齐；原合同和原前端对公开文件大小的范围不一致尚未修复。**

### 可重复证据

- 输入：`backend/internal/platform/douyin/testdata/php-integer-boundaries.json` 的四个 `source-size/…` 案例，分别用整数 token 和数字字符串提供 `9007199254740993`、`9223372036854775800`。
- 固定 PHP 将这些值发布为 `Source.size_bytes`。Go 使用精确整数路径，实际 JSON 数值与 PHP 一致，不先经 `float64` 丢失精度。
- 原前端 `validateSources` 要求 `Number.isSafeInteger(size_bytes)`；四个案例的 normal/debug 共八份成功响应因“文件大小无效”被拒绝。将原 PHP 响应交给同一前端会得到相同拒绝。
- 这些数值远超正常媒体体量，是整数边界的合成压力输入，不是当前真实平台文件大小或线上回归证据。

### 当前处理与建议（尚未实施）

继续保留原始精确整数和冻结期望。不能把数值截断为 `2^53`、改成浮点近似值，或放宽前端的安全整数检查。

建议经确认后，在公开 Source 投影边界省略超出 `1…Number.MAX_SAFE_INTEGER` 的可选 `size_bytes`，使其作为“公开文件大小未知”处理。保留候选、URL、全部 opaque ID、来源资格、画质与默认指向；debug 公共视图同样满足现有客户端合同，原始输入和对拍证据保留精确值。这样不会把可用完整文件变成 deferred，也不会伪造长度。

这也是对原 PHP 输出的显式修正，需要与纯语言迁移区分记录；本轮没有自行改变公开规则。
