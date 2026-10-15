# BarePulse 实现进度与续接记录

更新时间：2026-10-15（Asia/Shanghai）。代码、离线验证、实机证据分别记录。

## 当前工作点

- 分支：`codex/barepulse-implementation`；基线 `0127756`。原仓库只有文档且无用户修改，本聊天在 `work/BarePulse` 新克隆。
- 当前增量：首个可运行实现检查点；最近实现提交由本记录所在提交确定（`git log -1`）。
- 第一批已实际浏览器打通：管理员登录 → DEMO-UI-01服务器 → mock_pikvm设备 → 电源/控制台端口绑定 → power_on → succeeded → started/succeeded审计。
- 实际工具：Go 1.27.2、PostgreSQL 18.6、PHP 8.5.11，官方便携包置于聊天 work/toolchain；Go/PHP SHA256核验。PG仅监听127.0.0.1:55432，测试库barepulse_test，每测试独立schema。
- 下一条任务：继续安全审查修复（慢请求撤权及operator边界），完成OpenAPI、浏览器M2/M4/M5验收、备份恢复及统一检查，再更新本文件提交。
- 仅离线代码、本地服务、loopback/模拟测试；没有真实设备访问或生产部署。

## 阶段状态

| 阶段 | 代码 | 离线验证 | 实机/外部 |
| --- | --- | --- | --- |
| M1 | IMPLEMENTED | PostgreSQL与浏览器首链PASS；OpenAPI收尾中 | 不适用 |
| M2 | IMPLEMENTED | 账本/并发/归属/模拟采集/JS模块PASS；完整浏览器操作待验 | BLOCKED：SN2410/VyOS未授权 |
| M3 | IMPLEMENTED：PiKVM/Redfish/APC；IPMI unsupported | 默认禁用、PG调度、loopback契约PASS | BLOCKED：固件/MIB/实际控制未验 |
| M4 | IN_PROGRESS：模拟网关、队列、保护、介质已实现 | PG交接/旧流关闭/重启保护PASS；权限竞态继续修复 | BLOCKED：真实媒体路由和隔离证据不全 |
| M5 | IN_PROGRESS：平台映射/签名/门户/PHP模块已实现 | HMAC/归属/门户/生命周期PG与PHP契约PASS | BLOCKED：无WHMCS/Smarty实例 |
| M6 | IN_PROGRESS | 原生启动/构建PASS；交付资料和恢复演练继续 | BLOCKED：生产/实机未授权 |

## 实现与决定

- 空库管理员初始化、bcrypt、哈希会话、CSRF/Origin、退出撤销和登录限流；无默认口令。
- 资产/设备/用户CRUD、历史绑定/版本、数据库端口唯一与跨power/console物理端口归属；operator默认只读自有范围。
- 持久任务、幂等、设备与服务器串行、执行前版本复核、unknown不自动重发、至少30秒冷却；PDU供电与服务器状态分开。
- 真实电源调用要求双总开关与外部精确设备/server/port/action/时间窗profile；默认关闭。APC不猜OID，IPMI未支持。
- 独立/共享流量账户，原始样本与精确十进制账本、port/VLAN重叠保护、观测不入账、重跑去重、复位/漏采/换绑质量；跨周期不明字节保留未分配。
- KVM整机独占、队列、持久保护、单次票据与短时授权；登记中的旧stream未关闭不交接，键鼠/介质清理有模拟状态证据。真实网关未开放。
- WHMCS按实例/服务/客户/服务器独立校验，HMAC nonce防重放；生命周期只撤销平台访问，保护作业保留。
- Go 1.27.2 / PostgreSQL 18.6 / pgx v5.7.6 / gosnmp v1.45.0 / x/crypto v0.41.0锁定；同源原生ES modules，无npm构建链。秘密通过外部profile/环境变量引用，不存客户响应或审计。
- 收费、超额限速、断网、断电均默认关闭；不推定业务政策。

## 实际检查记录

| 检查 | 实际结果 | 限制 |
| --- | --- | --- |
| 首轮 `go test ./... -count=1` + TEST_DATABASE_URL | PASS（app 29.462s，drivers 1.464s） | 后续新增安全测试发现的问题单独修复，不用旧PASS覆盖 |
| Foundation真实PG全套 | PASS（新增registry后23.389s）；五模拟场景、授权profile/撤销、APC构造回归PASS | 仅loopback及模拟 |
| `go test ./backend/internal/app -run '^TestTraffic' -count=1 -v` | 9主测试与质量子例PASS（12.139s），含12并发去重与port/VLAN SQL互斥 | 真实SNMP未调用 |
| `go test ./backend/internal/drivers` / vet | PASS，11主测试及内层错误子例 | Windows -race因CGO缺失NOT_RUN |
| `go test ./backend/internal/app -run 'Test(Console\|Security\|WHMCS)' -count=1 -v` | PASS（20.899s）；含慢body撤权、旧stream关闭、保护保留 | 新增审查项继续运行 |
| 安全审查追加 | 7主测试+子例PASS（16.396s）：单连接池、新active覆盖旧terminated、旧Terminate重放不误取消新任务等 | operator删除介质/慢body改保护已修复；9安全主测试最终PASS（27.464s） |
| PHP contract.php / http_contract.php | 54断言PASS；HTTP loopback累计69断言PASS，六PHP文件lint PASS | 真WHMCS/Smarty NOT_RUN |
| Go vet/build、migration/bootstrap/serve | PASS；原生应用已启动，浏览器M1完成 | 宿主无Docker，容器验收待CI |
| JS | 模块语法PASS；traffic实际ES module import/图表数据契约PASS | 其余完整浏览器操作继续 |

已发现并修复：PHP任务和门户票据32hex误设→平台48hex；APC IP:161 URL解析panic；慢输入/上传跨权限或服务撤销；portal把operator提升为customer；事务中再取pool导致饥饿；历史terminated误阻新服务、旧Terminate影响新服务。WHMCS重复幂等返回200是既定API，测试从错误预期202校正为200。后续发现项继续如实记录，不删断言修绿。

## 续接与外部阻塞

当前持续执行，未因额度暂停。最近可靠读数为周额度已用15%，仅当时读数，不声明后台持续监控。

- 首次实现正在保存本地提交并尝试push/PR；结果在下一检查点记录，不强推。
- 离线剩余：安全回归、OpenAPI与路由覆盖、浏览器后续模块、PHP统一验证脚本、备份恢复、干净容器CI、最终A01–A16证据矩阵。
- 外部剩余：具体设备/端口/窗口授权、固件/MIB/供电拓扑/媒体隔离、真实WHMCS实例、生产启用授权。
- 恢复先运行 `git status --short --branch`、`git log -3`，读取本文件及AGENTS，继续未完成检查，不重写已有模块。
