# 生命周期互斥与严格 clean 校验

## 人工报告与范围（2026-11-02 JST）

维护者人工转交 dot run `c33b00f0-d614-11f1-81f1-26e1b4df8de4`，tested_sha `17ae147618051797a1757720fd5f6064c4575e39`。ZIP SHA-256 `a9d34ac1a977dba5c38fe3a9e768f86a9ec73586347369e890c6d45d5c98c044` 与 29 份载荷均核验，CHAT_ACK `6561169369`。

原 replacement 污染候选已获 dot 受控复验通过；Linux 0755 和落盘 CLI 通过。新增的两项失败是迟到重复 release 删除继任锁、replacement 视图进入 index 后严格 clean 误报。后者没有污染候选，不能改写成原来源漏洞复发。此前 CI 确实成功，不覆盖后来发现的反例。

## 一、整个领取/释放状态转换共享短临界区

`execution_guard.py` 的公开 acquire/release 在任何状态核对、写入、长期锁创建/清理前，都先独占创建同一 `dokitool-chat-execution.lifecycle.lock`。从核对 owner 到写 idle、移除 owner marker 和长期锁全部在这个短临界区内；领取和异常回收同样受它保护。它不是在删除前再检查一次。

相同 owner 的第二个 release 若与第一个重叠，会在进入临界区前退出2，不产生状态变更；第一个完成且继任者取得新 nonce 后，旧调用再试会因身份不匹配被拒绝。短临界区繁忙时没有等待/自动重试，调用方重新检查状态后决定下一次动作。只读 check 仍是当时的所有权快照，不代替每批写入前核对。

临界区由本次 mkdir 成功的调用在 finally 清理。异常终止可能留下 lifecycle 目录；遇到已有目录（包括空目录）一律拒绝，不按时间偷锁，不自动删除。恢复仍要求任务/重试终态、实际进程路径、改动归属与旧回执保全。所有写入者必须升级到同一入口；此协议不能阻止旧脚本、管理员直接删文件或其他机器使用不同 state-dir。此次暂停新定时触发并核对旧任务结束后才接管。

新增五项互斥测试，包括 dot 原调度的线程反例、真实子进程暂停在完成旧检查之后再竞争、释放最后清理窗口、繁忙目录不写入及写回执失败后保留 owner。原九项竞争/期限/错误身份检查继续保留，总计14项。测试只用临时state目录，没有用真实开发锁做竞争实验。

## 二、直接对照原始 HEAD 与 index 条目

预检继续保留普通工作区/untracked检查、recipe登记和命令级禁 replacement，同时用 NUL 分隔的 `git --no-replace-objects ls-tree -r -z --full-tree HEAD` 与 `ls-files --stage -z` 比较完整路径、mode、对象ID及stage0。未合并、多余/缺失或不同条目均不能标 clean。路径按原始字节解析，避免依赖 status/diff/write-tree 的缓存树捷径；不 reset/read-tree/修改实际用户 index，不放宽严格模式。

这是针对已观察到的原始对象/index差异的修复。Git缓存树可能解释该现象，但本轮没有把未独立证明的Git内部机制当成确定根因。Git格式依据：[ls-files官方文档](https://git-scm.com/docs/git-ls-files)、[ls-tree官方文档](https://git-scm.com/docs/git-ls-tree)。

新增三项预检测试：replacement→read-tree污染实验index、自定义namespace且移除refs后仍拒绝、status为空时的删除/mode/未合并stage三子情形。保留 `--allow-dirty` 的明确 false 标记与只从原HEAD构建，原blob哈希及来源视图检查不变。总计32项；Windows31 PASS/1 POSIX-only SKIP。

完整 `probe_git_replacement.py` 新增第三个严格场景：只在独立clone写入合成index及工作区，验证普通视图确已替换、原HEAD仍不变，然后要求预检退出1且不产生候选/manifest。原baseline与replacement两份完整候选仍需同哈希。开发 `--recipe` 覆盖模式不声明此严格场景通过，最终提交必须无override执行。CI保留原门禁并留存第三场景日志。

## 已取得的红/绿证据

Windows上修前互斥13项为10 PASS、2 FAIL、1继任锁丢失ERROR；预检32项有5个失败记录（含同一测试的3个子案例）和1跳过。修后扩展互斥14/14，预检31 PASS/1 SKIP。完整旧17ae147探针中三次预检都退出0，脏index却标clean；独立严格拒绝断言退出1。该候选哈希仍等于原HEAD基线。原始失败和哈希见 [开发机器回执](lifecycle-index-checks.json)。

最终新SHA的Windows严格对照、Linux单测与完整CI必须从对应运行获取，不能用本节开发记录预报通过。源码、测试与脱敏证据入库，原附件、开发原始日志留在隔离证据目录。没有改backend/frontend、固定PHP输入/期望/生产器/哈希、停止的断言/计数维护、原10项差异或生产系统。

这是两项有明确反例的局部修复，不是对Git配置/工具链/缓存、恶意宿主或任意锁故障的全面认证。父variant身份、真实平台/浏览器、旧PHP发布包与完整回滚仍未关闭；保持Draft，不合并、不部署。dot针对最终新SHA仍需独立复验；人工报告接收与发布评论都不表示自动回传已打通。
