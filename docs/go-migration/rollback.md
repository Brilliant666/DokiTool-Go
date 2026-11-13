# 生产回滚流程与历史切流归档

本文档说明当前线上 Go 服务与静态前端的标准快速回滚流程及核验门禁。正向发布流程见 [DEPLOYMENT.md](../DEPLOYMENT.md)，启动参数与环境变量见 [CONFIGURATION.md](../CONFIGURATION.md)。

> **公开仓库安全声明**：本文档仅包含通用回滚步骤与校验门禁，不包含任何服务器地址、主机名、远程连接细节、入站限制规则或密钥取值。

---

## 一、回滚前提与安全不变式

1. **上一版不可变目录完整保留**：每次发布均写入独立的 `<releases_root>/<sha>/` 目录（含编译好的二进制与 `frontend/dist/`），绝不原地覆盖或删除上一活动版本目录。
2. **systemd 配置分层隔离**：版本切换仅通过高序号 drop-in 片段覆盖 `ExecStart`（或独立的守护策略片段），不直接改写基础单元文件或包含密钥/环境变量的独立 drop-in。
3. **浏览器活动下载不中断**：回滚只切换服务端进程与静态目录软链，不清空用户浏览器站点存储，也不强制注销正在传输的下载 Service Worker，让在途任务自然收敛。

---

## 二、标准回滚操作步骤

当新发布版本在切换后冒烟失败或线上发现回归问题需立即回退至上一稳定版本 `<prev_sha>` 时，按以下顺序执行：

### 1. 回退主后端 systemd drop-in 并平滑重启

1. 核对 `dokitool-go.service.d/` 下当前生效的编号配置片段，确认移除本次新增的高序号 `ExecStart` drop-in 后，上一稳定版本 `<prev_sha>` 的 drop-in 将自动恢复为最高优先级。
2. 移除（或移出至受保护备份位置）本次发布新增的 drop-in 片段。
3. 执行 `systemctl daemon-reload`，排空在途请求后停止 `dokitool-go.service`，确认本地回环监听端口释放，随后重新启动服务并核对处于 `active (running)`，且运行二进制 `version` 输出已恢复为 `<prev_sha>`。

### 2. 原子切回静态前端软链

1. 如切换期间已有浏览器加载过新版本首页，可先执行 `cp -n <failed_release>/frontend/dist/assets/* <prev_release>/frontend/dist/assets/` 保留新增哈希静态块（不覆盖 `<prev_sha>` 原有文件）。
2. 在软链同级目录创建指向 `<prev_release>/frontend/dist` 的临时符号链接并原子替换：
   ```sh
   ln -sfn "<prev_release>/frontend/dist" "<frontend_symlink>.tmp"
   mv -T "<frontend_symlink>.tmp" "<frontend_symlink>"
   ```

### 3. 辅助服务同步回退（若涉及）

若本次发布同时更新了上海端 `dokitool-sh-pull.service` 的二进制或 systemd drop-in（参见 [douyin-shanghai.md](douyin-shanghai.md)），同步移除本次新增的上海端 drop-in，执行 `systemctl daemon-reload` 并重启 `dokitool-sh-pull.service`，确认连接与认领循环恢复正常。

---

## 三、回滚后核验门禁

回滚操作完成后必须逐一确认以下指标，全部通过才视为恢复完成，并在原发布 Issue 中追加回滚回执：

1. **进程与版本**：`dokitool-go.service` 状态为 `active (running)`，实际生效 `ExecStart` 二进制路径与 `<binary> version` 输出均为 `<prev_sha>`。
2. **公开接口契约**：本地与公网 `GET /api/platforms?schema_version=1.0` 返回 HTTP `200`、`code: "OK"`，启用平台列表与 `<prev_sha>` 基线一致。
3. **静态资源与安全头**：公网首页与首页引用的全部 `/assets/*` 返回 HTTP `200`；精确 `/downloads/bridge.html` 仍返回唯一 `X-Frame-Options: SAMEORIGIN` 与 `Cache-Control: no-store`；缺失的 `/assets/*` 返回 `404`。
4. **代表性解析验证**：抽查代表性平台公开作品 `/api/parse` 返回 `200 OK`。

---

## 历史

### Go 接替早期 PHP 实例切流前的回滚门槛归档

在早期从 PHP 后端（参考基线 `665bd55134982b9fe107aa1cd00373ba8da8ff3c` 与 `25beaee107f8c1a208ced9deb3c637068445e316`）首次切流至 Go 后端的迁移阶段，曾要求封存完整的 PHP 发布目录、三池 PHP-FPM 配置、Caddy 路由片段与 `release-manifest.json`，并在隔离验证环境完成一次「PHP → Go → 回退至 PHP（含切换前后签发的微博 30 分钟票据双向兼容验证）」的完整演练后才允许实例切流（维护边界见 [MAINTENANCE.md](MAINTENANCE.md)，PHP 时期说明归档见 [../HISTORY.md](../HISTORY.md)）。现网已完成 Go 单栈长期稳定运行，日常版本回退不再涉及 PHP-FPM 池切换。
