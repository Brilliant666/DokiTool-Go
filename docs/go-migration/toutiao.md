# 今日头条 Go 迁移

正式 Go `New/Parse` 与 `cmd/dokitool` 注册已接入。固定 PHP 来源为 `25beaee107f8c1a208ced9deb3c637068445e316`，运行时 PHP 8.3.6。完整执行证据见 [验收记录](acceptance.md) 与 [机器报告](takeover-toutiao-weibo-checks.json)。

## 产品行为

保留同域短链最多三跳、精确作品 ID、请求内独立 ttwid、35 秒共享预算和单请求最多 20 秒、RENDER_DATA 原始/百分号 JSON、作者简介、音乐、零统计、主备 base64 地址、原候选顺序。大整数不先转浮点；原入口不使用 JSON_NUMERIC_CHECK。相同画质保持上游顺序，默认仍由共享 unified 层选择，前端产品无需修改。

## 独立验证

48 个场景（20 成功、28 错误）、86 次原 PHP 请求，96 份 normal/debug 公开响应，全部逐字段相等。正式 HTTP 176 场景覆盖解析、请求目标 resolve 与缺失目标拒绝。三个非法入口先被公共网关拦截，单独验证原公共错误与零请求；不把网关预检误当 parser 对拍。

PHP producer 在无网络、只读原源码归档中替换 cURL IO，原 header callback/POST JSON/有界 writer 实际执行；仅 `Accept: */*` 属于已声明的 cURL 显式选项与 Go 线头表示差异，其余请求字段与预算严格对拍。固定 PHP 另确认 VT/FF 不能作为合法 base64 空白；Go 定向回归覆盖该差异。

基线位置：`backend/internal/platform/toutiao/testdata/`。用 `produce-toutiao.py --php <固定PHP8.3.6> --output <临时目录>` 重放，再 `verify-toutiao.py --check-git-source`；CI 与冻结文件逐字节比较。没有真实头条请求、浏览器保存或生产切流证据。
