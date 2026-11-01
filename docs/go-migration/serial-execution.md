# 串行写入与冲突恢复

## 2026-11-01 UTC / 11-02 JST：完成明确交接

两条已结束的执行记录分别负责 Git replacement 修复和回滚包审查，登记时间相差约 55 毫秒；单独读取再覆盖 JSON 不能实现互斥。两边均未提交或推送。小时任务 run `621294b1-ca18-8330-a652-cf1448a6a13e` 已由任务接口确认于 16:32:10 UTC completed，维护者随后授权按串行建议继续。只暂停本项目任务的新触发，不修改其他任务或监听。

纠正此前进程归属：PID 289296 的实际命令属于另一个生图项目的预览服务，不是 DokiTool 修复进程；未停止或修改它。占用记录同时领取的竞态有独立时间戳证据，但不能把其他项目的活动终端当成本项目仍有写入者。

旧 `.local/identity-refresh-fix/review` 的 14 个变更文件已逐字节保存并记录 SHA-256，旧工作树不 reset、不 stash、不清理。新的串行集成工作树为 `.local/identity-refresh-fix/serial-review`，本地分支 `chat/serial-closeout-20261101`；它基于实际远端 b44a21a，按审核过的路径和段落单独整合 replacement 修复。该修复提交为 `d99654121ec3fe05ecd1100e745a444f2f19f4d4`。未将小时任务的回滚工具、CI 步骤、清单或乱码文档直接混入该提交。

原回滚审查工作完整留在旧工作树和忽略目录 `.local/identity-refresh-fix/serial-closeout/preserved-mixed-worktree/`，不是已合并代码。其清单只检查自述字段与文件哈希，不能独立证明 b44a21a 候选属于所声明 Git 提交；真正来源证明需原始对象预检和对应提交的反例。后续应独立审查路径规范化、Windows junction、清单信任边界和输出覆盖保护，再选择性整合，不以文件哈希相符代替来源完整性。

## 统一互斥入口

所有本机写入者使用根目录同一个 `.local` 作为 `--state-dir`，不得给每个工作树各建一套锁。运行前仍先核对任务终态、活动进程归属、PR、远端和未提交工作。

`scripts/go-migration/execution_guard.py acquire` 通过一次不允许已存在的目录创建，获得 `dokitool-chat-execution.lock`。创建成功后才检查旧回执 idle 并登记 owner、随机 nonce、worktree 和期限。遇到已有锁（包括空锁）、过期锁或旧 active 记录，明确拒绝；没有自动偷锁或 force 参数。JSON 是回执，不再作为唯一互斥手段。

每次源码/索引写入批次及提交、推送前，使用 `check` 核对 owner、nonce 和有效期限；旧客户端覆盖回执时也会拒绝继续。结束用 `release` 写入准确 HEAD、idle、下一项，再移除自己的锁目录。到期的本人可以释放自己的记录，但到期不授权别人删除锁。异常中断必须先核验原运行及重试终态、相关进程和改动归属，保存旧记录，之后另行审查恢复；不要只看 PID 消失或 lease 过期。

这是协作互斥协议，不是对恶意进程、管理员改文件或不同机器的分布式锁。暂停新定时触发并完成交接，解决旧客户端不遵循新协议的过渡问题；其他写入者必须使用同一入口。

~~~powershell
# $root 是开发电脑上的 DokiTool-Go 根目录，不是某个 worktree。
$state = Join-Path $root '.local'
$work = Join-Path $root '.local/identity-refresh-fix/serial-review'
$guard = Join-Path $work 'scripts/go-migration/execution_guard.py'
python -X utf8 $guard acquire --state-dir $state --owner <本轮唯一owner> --worktree $work --lease-seconds 3600
# 保存上一步返回的 nonce；每批写入、commit 和 push 前检查。
python -X utf8 $guard check --state-dir $state --owner <owner> --nonce <nonce>
# 结束前取得准确 SHA，保留下一项；只能释放本人的锁。
$sha = git --no-replace-objects -C $work rev-parse HEAD
python -X utf8 $guard release --state-dir $state --owner <owner> --nonce <nonce> --head $sha --next-work '<准确下一项>'
~~~

九项独立测试包括六个真实子进程在屏障后竞争同一目录，仅一个成功；空锁、旧 active、过期、伪 owner/nonce、旧客户端改写回执、错误 SHA 与正常交接均有回归。开发 Windows 9/9、隔离 Linux 9/9；最终提交的 Linux CI 仍须按实际 SHA 读取。本轮首次 PowerShell 外层把 unittest 的正常 stderr 误作 NativeCommandError，已改用 Python subprocess 原始字节记录，真实测试退出码和日志摘要单独保存；不把外层失败当作产品测试失败或忽略它。

所有工作停在 Draft、可执行验证与隔离准备，不合并、不部署、不改公开 PHP 或停止的私有/PHP 维护项目。父 variant 身份仍未修复。
