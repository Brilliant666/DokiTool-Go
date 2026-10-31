# Go 迁移工作区

后续 Go 开发在独立私有仓库 [Brilliant666/DokiTool-Go](https://github.com/Brilliant666/DokiTool-Go) 进行。公开 [Brilliant666/DokiTool](https://github.com/Brilliant666/DokiTool) 的 `main` 继续保留 PHP 开源版本，不合入私有迁移分支，也不以删除公开 PHP 为迁移目标。

维护、选择性同步及许可边界见 [MAINTENANCE.md](MAINTENANCE.md)。行为对照见 [mapping.md](mapping.md)，未来发布或切流前的回滚门槛见 [rollback.md](rollback.md)，各阶段证据见 [acceptance.md](acceptance.md)。本轮仅建立私库和离线同步，不部署 Go、不改变 PHP 生产，也不重新下载媒体。

## 当前运行方式

Go module 为 `github.com/Brilliant666/DokiTool-Go/backend`，位于 `backend/`；内部 import 随私库路径统一。固定工具链为 Go **1.27.3**，生产代码当前只使用标准库。PHP 应用保留为现行生产入口及冻结参考；Go 使用独立的 loopback 端口做迁移验收，没有 PHP 回退调用。

```sh
cd backend
go test ./...
go test -race ./...
go vet ./...
go build -o /tmp/dokitool-go ./cmd/dokitool
/tmp/dokitool-go catalog
/tmp/dokitool-go serve -listen 127.0.0.1:9082
```

catalog 从原路径 `backend/config/platforms.json` 内嵌，公开字段与九平台 PHP 目录保持一致。目录表示项目的平台注册事实；某平台尚未绑定 Go adapter 时，其解析请求明确返回 `503 SERVICE_NOT_CONFIGURED`，不会把空媒体结果当迁移成功。当前只有抖音绑定了 Go adapter，仍存在 [未关闭的基线合同问题](known-issues.md)，Go 服务不能替代九平台生产入口。

公开 JSON 仍为 `/api/parse`、`/api/resolve`、`/api/platforms`，需要 `schema_version=1.0`。例如检查目录：

```sh
curl 'http://127.0.0.1:9082/api/platforms?schema_version=1.0'
```

不接受公开任意 URL 代理、客户端 Cookie/传输配置或 TLS 参数。平台经 `internal/fetch.Executor` 请求上游，实际执行器统一约束目标、DNS、socket 绑定、逐跳校验、读取限额和 context。首次实现采用标准 `net/http`；替换握手/传输实现时须保留同一策略和对应测试。媒体探测及微博流式媒体各自有更严格/独立的预算，不应直接复用普通 JSON 请求预算。

抖音有界 MP4 探测每次最多 1 MiB、12 个请求、6 秒，限制重定向与文件总大小，不下载整片。严格媒体请求使用标准库 HTTP/1.1 传输，并在标准解析器前观察原始头部和 trailer，防止同值重复 `Content-Length` 被合并而丢失原有拒绝证据；正文仍由 `net/http` 解码。普通页面/API 请求保留标准库 HTTP/2 协商。两条路径都将请求取消绑定到底层连接。

Go 对普通请求新增了逐跳目的域、DNS 与连接约束；Live HEAD 重定向只访问已声明媒体主机。这是已批准的出站边界收紧，与旧 PHP 接受任意重定向的行为不同，真实平台兼容性仍须在后续阶段验证。

## 合同与身份

`internal/unified` 使用保留精度的 JSON tree 表示公开对象：`json.Number` 不先转浮点，缺字段、null、false、0 和空数组分别保留。所有公开媒体结果在完整候选阶段校验，应用来源资格和公共候选策略后再次校验。平台包不能自行实现第二套默认规则或把未经验证的 tree 直接发给前端。

同一规范仍是 `docs/schema/unified-v1/unified-v1.schema.json`。Go 只内嵌自动生成且有字节一致性测试的副本：

```sh
cd backend
go generate ./internal/unified
go test ./internal/unified
```

身份哈希与票据使用显式的 PHP JSON 兼容编码，不能改成普通 Go map 的 JSON 编码。公共 JSON 对象键序不影响比较，但哈希/签名的输入字节有意义。变更身份规则必须单独审查，而不是更新 fixture 以通过测试。

完整离线门禁可一次执行（需要 Go 和既有 Python 校验依赖，无需 PHP）：

```sh
python scripts/go-migration/check.py --race --report /tmp/dokitool-go-checks.json
# 已安装 frontend/package-lock.json 中的依赖时，再加入原前端消费和四项续签检查：
python scripts/go-migration/check.py --race --frontend --report /tmp/dokitool-go-checks.json
```

门禁会运行各项独立检查并保存失败明细，任何未通过的公共响应使总结果为 `passed: false`、进程退出 1。当前 fragment 与极限文件大小的基线冲突保留为失败项，没有跳过或白名单放行。

旧 `--check-php-source` 检查针对 `665bd55` 的来源字节；同步新 PHP 后，旧来源应从固定提交单独导出验证，不以当前工作区的合法产品差异作为旧 fixture 损坏。

## 冻结 PHP 对拍

已发布 PHP `25beaee` 的版本化基线见 [新基线与复现方法](../../backend/testdata/php-25beaee/README.md)。新旧归档分别用 **PHP 8.3.6** 和同一固定时钟执行。旧样本重放一致后按原哈希引用；12 个新场景记录旧/新 PHP 输出及逐字段差异，Go 在真实探针、formatter、adapter、服务端选择和公开投影路径上逐项对拍。

统一门禁会把 24 份新增实际 Go 响应交给既有独立 Schema 和前端消费者；`--check-php-source` 从各自的固定 Git 对象检查源字节，不用新 PHP 工作区核对旧来源哈希。在安装前端锁定依赖后执行：

```sh
python -X utf8 scripts/go-migration/check.py --check-php-source --frontend --report /tmp/dokitool-go-checks.json
```

使用 UTF-8 运行含中文样本的 Python 工具。最新[实际门禁结果](php25-checks.json)保留非零退出及两个既有合同问题；不把 Go 单测通过或目录包含九个平台解释为可部署。

旧 `665bd55` 冻结目录：`backend/testdata/php-baseline/`（公共层）与 `backend/testdata/php-douyin/`（实际抖音 Parse、请求序列、探测及舍入）。这些原始输入、期望、生产器和哈希保持不变。已发布 PHP `25beaee107f8c1a208ced9deb3c637068445e316` 作为新增版本基线，变化须由该提交的 PHP 产品路径生成并逐项说明，不覆盖旧期望，也不使用 Go 输出反写期望。正常 Go 测试只读固定基线，不安装或运行 PHP。

```sh
python scripts/go-migration/verify.py
python scripts/go-migration/verify.py --douyin
python scripts/go-migration/verify.py --integers
```

需要复核 PHP 生成来源时，按该目录 README 从固定 Git commit 重新导出至临时目录，核对差异；旧版 `--source-root` 必须指向 `665bd55` 的归档，不能直接指向已同步新 PHP 的当前工作区。生产代码和普通 Go 验收不依赖这个生成器。测试密钥均明确标为公开合成材料，真实密钥只记录受保护来源引用。

可以将 **实际 Go 输出** 导出给既有独立 Python 检查：

```sh
cd backend
DOKITOOL_GO_BASELINE_OUTPUT=/tmp/dokitool-go-baseline.json \
  go test ./internal/unified -run TestPHPBaseline -count=1
cd ..
python scripts/validate-unified-schema.py /tmp/dokitool-go-baseline.json
```

Python 校验依赖仍由 `scripts/requirements-validation.txt` 声明。导出不覆盖 PHP 期望输出，也不把未知动态字段整批删掉。

抖音实际输出可独立导出后给原前端消费；以下脚本只增加测试生产器，没有修改前端产品代码：

```sh
cd backend
DOKITOOL_GO_DOUYIN_OUTPUT=/tmp/dokitool-go-douyin.json \
  go test ./internal/platform/douyin -run TestPHPDouyinBaseline -count=1
cd ..
node frontend/scripts/test-go-media-integration.mjs /tmp/dokitool-go-douyin.json /tmp/dokitool-frontend-go.json
```

## 阶段顺序

骨架、公共合同、稳定身份、票据与首个抖音实现保留为当前阶段成果。本轮将已发布 PHP 的相关行为与版本化基线同步到私库；其他平台的后续迁移、Go 发布和目标环境切流均另行授权。公开 PHP `main` 持续保留。未来需要切流时，先完成相关行为、客户端消费与可恢复发布产物验收，再执行 [回滚门槛](rollback.md)。DASH 合流执行器及小红书 4K 新传输专项保持后续范围。
