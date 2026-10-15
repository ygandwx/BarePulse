# 离线备份与恢复核验

`scripts/backup-restore-check.ps1` 对停止写入的本地 PostgreSQL 和媒体目录做一致备份，只恢复到**不存在的新数据库**，并核验恢复内容。它不停止应用、不启动恢复后的应用、不修改源数据、不覆盖现有库、不删库，也不访问设备。

本流程补充 [运行说明](operations.md)。它证明数据库和媒体在该次离线演练中可以完整恢复，不证明物理设备已空闲或允许上线。

## 前提

需要 PowerShell 7.4+、与源数据库兼容的 PostgreSQL 客户端目录（`psql.exe`、`pg_dump.exe`、`pg_restore.exe`、`createdb.exe`）、连接本地数据库的读取权限和创建新库权限。脚本只接受 `localhost`、`127.0.0.1` 或 `::1` 的 PostgreSQL URI，避免演练意外指向外部环境。

停止所有应用、worker、采集器、网关及集成写入，保留 PostgreSQL 运行；等待连接池关闭。`-WritersStopped` 是对停写窗口的显式声明，脚本还会检查源库没有其他客户端连接。仅暂停页面操作不算停写。脚本不会替用户停止服务，也不会终止现有数据库客户端。

保持 `ALLOW_REAL_DEVICES=false`、`ALLOW_DEVICE_WRITES=false`。这次任务没有实机授权，不能因恢复成功打开开关。脚本发现这两个环境变量任一为 `true` 就拒绝执行，并在恢复环境文件中明确写入 `false`。

源数据库 URI 不允许密码，也不允许通过查询参数覆盖主机、数据库或用户。仅允许 `sslmode`、`connect_timeout` 参数；口令使用受保护的 `PGPASSFILE`。不要把 `.pgpass`、设备 profile、SNMP／WHMCS 秘密放到媒体目录、命令行或公开日志。

媒体目录必须已经存在，包含常规文件；脚本拒绝符号链接、目录链接和嵌套目录。即使没有媒体，也传入一个现有空目录。备份目录必须是新路径且不能位于源媒体目录中。默认输出在仓库的 `work/backup-restore-<UTC时间>-<随机标识>/`，已被 Git 忽略；备份包含私有媒体和数据库敏感内容，需要限制访问并另行加密保管。

## 调用

先自行停止本次开发服务，再运行；不要对仍在运行的应用直接执行示例。

```powershell
$env:ALLOW_REAL_DEVICES = 'false'
$env:ALLOW_DEVICE_WRITES = 'false'

./scripts/backup-restore-check.ps1 `
  -PostgresBin '../toolchain/pgsql/bin' `
  -SourceDsn 'postgres://barepulse@127.0.0.1:55432/barepulse_dev?sslmode=disable' `
  -RestoreDatabase 'barepulse_restore_20261015_01' `
  -MediaPath 'work/media' `
  -OutputDirectory 'work/backup-restore-dev-20261015-01' `
  -WritersStopped
```

`RestoreDatabase` 必须是 3–63 字符的小写字母、数字及下划线名称，以字母开头；必须不同于源库，且 PostgreSQL 中尚不存在。脚本先查询是否存在，再由 `createdb` 原子创建；并发创建造成冲突时立即失败，不会接管那个库。重复运行应换新的恢复库名及输出目录，原有演练证据保留。

`-CommandTimeoutSeconds` 默认 300，允许 30–3600。超时只停止脚本自己创建的客户端进程，不停止数据库、应用或别人的进程。恢复库一旦创建，无论随后通过或失败都保留，不会自动清理。

## 一致性与核验内容

脚本在源库开启 `REPEATABLE READ READ ONLY` 事务并导出 snapshot；源数据清单和 `pg_dump --snapshot` 共用这一快照，导出事务保留到核验结束。媒体没有数据库事务，因此必须在同一停写窗口复制。脚本会在备份前后重新计算源库清单和源媒体 SHA256；检测到变化即失败。

检查包括全部首期 25 张关键表的行数及完整行内容 SHA256：身份、资产、历史绑定、任务、设备租约、审计、流量账本、额度与调整、控制台、票据、访问授权、虚拟介质、模拟设备、网关连接、集成映射和重放记录。哈希以 UTC 时间及按 C collation 排序的完整 JSON 行计算，避免“行数相等但状态不同”误判。大规模历史表需要足够的 PostgreSQL 内存和停写窗口；该脚本面向首期规模的离线核验，不代替持续归档或大规模增量备份方案。

特别记录受保护会话、未解决电源任务和登记中的网关连接数量，比较其完整内容，包括保护原因、代次、设备占用和任务结果。脚本保留 `unknown/running/verifying`、保护状态和旧连接登记，绝不把它们自动改为成功或空闲。

对每个非 deleted 媒体资产，核对 `<平台生成的48位ID>.iso` 文件存在、字节数及 SHA256 与数据库一致；复制后再次核验。媒体目录中的额外常规文件一并保留，并报告数量，不猜测其用途或自动删除。恢复数据库使用单事务 `pg_restore --exit-on-error --single-transaction`；源库和恢复库清单必须一致才返回 PASS。演练使用 `--no-owner --no-acl`，对象归创建恢复库的账户所有；它不备份 PostgreSQL 集群角色／口令，也不声称原生产授权规则已经恢复。

## 输出与恢复后处理

| 文件 | 内容 |
| --- | --- |
| `verification.json` | PASS／FAIL、时间、检查结果、备份 SHA256、保护和未知状态计数；不含连接口令 |
| `barepulse.dump` | PostgreSQL custom-format 备份 |
| `source-manifest.json`、`restore-manifest.json` | 表行数和 SHA256、媒体资产摘要 |
| `source-media.json`、`media/` | 源媒体大小／哈希清单和实际副本 |
| `restore.env` | 新库连接、媒体副本路径，设备访问和写入开关为 false；只在 PASS 后生成 |
| `*.stderr.txt` | 私有 PostgreSQL 客户端诊断，用于失败排查 |

PASS 后也不自动启动程序。确认原应用／网关执行者已停止，先核对未知任务、装机／BIOS 保护和 `gateway_connections` 残留，再决定是否使用恢复库继续离线服务。迁移命令可以在设备访问关闭时执行；服务启动后的 worker 会按既定恢复语义处理遗留任务，不应为了“清洁恢复”手动把状态清零。

实机授权、设备状态核验和正式切换不在该脚本内。删除演练库也不在脚本内，需另行确认具体库名；不要把删除命令附加到自动核验流程。发生失败时保留输出和已创建的演练库，修复原因后使用新的名称重试。

## 实际演练记录

2026-10-15 16:40（Asia/Shanghai），PowerShell 7.6.5、PostgreSQL 18.6、仅监听 `127.0.0.1:55432` 的本地实例：

- 语法解析无错误。
- 新建专用源库 `barepulse_backup_fixture_89b87130`，迁移后仅在这个新建库写入明确标注的离线夹具；没有修改 `barepulse_dev`、`barepulse_test` 或停止主服务。
- 恢复到新库 `barepulse_restore_fixture_89b87130`：PASS。25 张关键表的数量／内容指纹一致，1 个 unknown 电源任务、1 个已过普通有效期的 protected 会话及保护原因、1 条旧 gateway connection 登记完整保留。
- 1 份明确标注“不是可启动 ISO”的媒体字节夹具，数据库大小／SHA256、源文件及恢复副本完全一致；模拟挂载与按键状态也随完整行哈希验证。没有模拟成真实装机成功。
- 实际 dump SHA256：`9481d574134ca46804dfef69bb645e256b179e2bf3b44442b4335b71fa98a17f`。
- 再次使用已有恢复库名：按预期拒绝；恢复库仍为 `unknown / protected / 1 media / 1 gateway connection`，没有覆盖。
- 单独测试进程中把设备开关设为 true：脚本在任何数据库／输出创建前拒绝；没有启动应用或发出设备请求。
- 专用源库持有另一个只读客户端连接时：脚本按预期拒绝，没有自行终止连接；测试程序随后仅关闭自己创建的只读客户端，未触及应用进程。
- 媒体内容与数据库 SHA256 不匹配：按预期拒绝，未创建恢复库。首轮先被尚未结束的只读测试查询阻止；该查询结束后换新输出目录重跑，验证了媒体哈希拒绝。所有三个故意拒绝的新库名均确认不存在。

实际完整证据保存在本地被 Git 忽略的 `work/backup-restore-fixture-89b87130/`，失败守卫证据各自使用新输出目录。两个专用演练库和文件均保留，未自动删除。主开发库停写后的演练由主任务单独执行和记录，不能用本夹具结果代替它。
