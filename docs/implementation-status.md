# BarePulse 实现进度与续接记录

更新时间：2026-10-15（Asia/Shanghai）。代码、离线验证和实机证据分别记录。

## 当前工作点

- 分支：`codex/barepulse-implementation`，基线 `0127756`；工作区 `work/BarePulse`。初始仓库无用户改动。
- 已保存实现检查点：`7afa3c0`。对应远端快照 `ac8a4d1`，二者 Git tree 完全相同：`bfba661ad44dcd49cfe04246ad3c48f799d0dc03`。
- 草稿 PR：[BarePulse #1](https://github.com/ygandwx/BarePulse/pull/1)。CLI 没有本地 Git 凭据，改用已授权 GitHub connector 发布，未强推；本地提交历史完整保留。
- 当前增量：OpenAPI、A01–A16验收、备份恢复、PHP→Go契约、集成停用/轮换与页面、三台流量验收、容器首链检查；随本记录提交。
- 下一条准确任务：提交当前增量，以相同 tree 发布远端快照；检查新 CI 的 Linux race/PHP/Compose 首链结果，修复实际失败，再记录交付检查点。
- 当前仅离线代码、本地服务和 loopback/模拟测试；没有真实设备访问或生产部署。

## 阶段状态

| 阶段 | 代码 | 离线验证 | 实机/外部 |
| --- | --- | --- | --- |
| M1 | IMPLEMENTED | PostgreSQL、浏览器首链、OpenAPI PASS | 不适用 |
| M2 | IMPLEMENTED | 三台独立/共享、精确账本、并发去重、质量、模拟采集、浏览器 PASS | BLOCKED：SN2410/VyOS未授权 |
| M3 | IMPLEMENTED：PiKVM/Redfish/APC；IPMI明确unsupported | 持久调度、故障与loopback协议契约 PASS | BLOCKED：固件/MIB/实际控制未验 |
| M4 | IMPLEMENTED：整机租约、模拟网关、输入、私有介质、保护和清理 | PG交接/旧流关闭/安全回归、进程重启保护、备份恢复 PASS | BLOCKED：真实视频/WebSocket/WebRTC/介质路由及隔离证据不全；实机网关拒绝开放 |
| M5 | IMPLEMENTED：映射、签名、门户、PHP模块、停用/轮换 | Go/PG与PHP契约、17项PHP→真实Go断言、管理页面 PASS | BLOCKED：实际WHMCS/Smarty实例未提供 |
| M6 | IN_PROGRESS：部署、恢复脚本、运维与验收矩阵已交付 | 原生全量验证与恢复 PASS；容器CI修复后待重跑 | BLOCKED：生产/实机未授权 |

## 已实现与关键决定

- 空库管理员初始化、bcrypt、哈希会话、CSRF/Origin、退出撤销和登录限流；没有默认口令。
- 资产/设备/用户 CRUD、历史绑定及版本、数据库端口唯一与跨 power/console 物理端口归属；operator只读自有范围。
- 电源持久任务和幂等、设备/服务器串行、派发前客户/绑定版本复核、unknown不重发、至少30秒冷却；PDU供电与服务器启动分开。
- 真实电源需要双总开关和外部精确设备/server/port/action/时间窗 profile；默认关闭，不猜APC OID，不自动降级IPMI。
- 流量独立/共享账户、不可变来源、原始样本与精确十进制账本、port/VLAN重叠约束、观测不入账、重放去重、复位/漏采/换绑质量；跨周期不明字节保留未分配。收费、限速、断网、断电关闭。
- KVM整机独占、FIFO、跨断线/重启保护、单次票据、短时授权；旧流实际关闭后才交接，键鼠/介质清理有模拟状态证据。未知连接登记不自动过期删除。
- WHMCS实例/服务/客户/服务器独立校验，HMAC nonce防重放；生命周期只改访问，保留保护。实例停用/轮换销毁portal grants，重新启用不能恢复旧票据；票据兑换与轮换持锁串行。
- Go 1.27.2 / PostgreSQL 18.6 / pgx v5.7.6 / gosnmp v1.45.0 / x/crypto v0.41.0；前端同源原生ES modules，无npm构建。秘密仅外部profile/环境引用，避免写入响应和审计。

## 实际检查记录

| 检查 | 实际结果 | 限制 |
| --- | --- | --- |
| 最新 `scripts/verify.ps1`，配置PG/PHP/cURL | PASS：gofmt、go vet、全套Go测试（app 60.455s / drivers 1.886s）、build、JS syntax | Windows race无CGO，单列NOT_RUN；不是实机测试 |
| `api/check_contract.py` | PASS：83操作 / 78路由、引用/参数/认证/Go JSON字段与验收测试引用 | 离线结构检查，不冒充完整OpenAPI元模式验证 |
| `scripts/verify-whmcs.ps1` | PASS：7 PHP文件lint、54 stub断言、HTTP累计69断言 | 69包含54；实际WHMCS/Smarty NOT_RUN |
| `TestWHMCSPHPRealHandlerContract` | PASS：17项实际PHP/cURL→Go httptest→PG断言；只1个幂等任务 | 未启动设备worker |
| `TestTrafficPGThree*` | PASS（4.714s）：三独立同VLAN、三共享精确2448字节；重放仍6样本/3账本 | 真实网络拓扑须授权核验 |
| `TestIntegrationRevocationRotationPreservesProtection` | PASS：非admin拒绝、缺enabled拒绝、停用/换密钥/旧票据永久撤销、保护保留 | 不把密钥轮换等同设备操作 |
| 浏览器 M1/M2/M4/M5 | PASS：登录→录入→绑定→任务succeeded→审计；流量账户/来源/两次采样/精确差值；进程重启后protected原样保留；实例登记/停用/服务绑定/暂停 | 模拟画面明确标识，浏览器error日志为空；截图保存在聊天outputs |
| 浏览器 WHMCS 客户门户 | PASS：单次URL兑换后清除fragment、私有58368字节媒体上传/挂载、动态模拟画面/F2输入、保护期间拒绝卸载、显式结束后释放并关闭旧流 | 不可启动的离线字节夹具；实际DB确认media ready、目标/按键/挂载均清空；修正释放后页面媒体状态未刷新的显示问题 |
| 首链 HTTP 冒烟脚本 | PASS：本地实际login/asset/binding/mock power/completion/idempotency/audit八项；非法origin、重定向和real开关拒绝 | Compose编排尚待CI实跑 |
| 主开发库备份恢复 | PASS：`barepulse_dev`→全新`barepulse_restore_m6_20261015_01`，内容指纹、源未变、保护1/旧连接0一致 | 主服务先停写；无媒体文件，因此媒体实字节另用独立fixture验证 |
| 独立恢复故障fixture | PASS：25表、unknown1/protected1/gateway1/媒体1完整保留；覆盖已有库/未停写/设备开关/媒体损坏均拒绝 | 仅新建本地演练库；没有删除或覆盖现有库 |
| CI 首次执行 | FAIL：runner拆分单引号health-cmd导致docker create把-U当参数 | 已改双引号；新CI和Compose首链尚待结果，不能标PASS |

主开发库 dump SHA256：`e963d11d1f20d6673fcc625e54082044b6ae23ede5f01d35cb7fcd162269e986`；私有备份证据在被忽略的 `work/backup-restore-dev-20261015-01/`。恢复库、原库及独立fixture均保留。服务重启实际使用原dev库，没有替换用户数据库。

已修复的问题包括：PHP任务/门户48hex契约；APC endpoint解析panic；慢body跨撤权；portal operator身份提升；事务内再次取pool的饥饿；旧terminated映射误伤新服务；实例更新缺少撤销路径。新增轮换测试首轮因测试把expires_in误解成字符串失败，已修测试解码并真实通过。没有删除失败断言或用固定成功响应修绿。

## 续接与交付边界

当前继续执行，没有因额度暂停。最近可靠读数周额度已用35%，仅代表该次读取。

1. 保存并发布当前增量，核对本地/远端tree，继续CI失败修复；全部离线完成后更新本记录。
2. 本地Git凭据恢复后，先`git fetch origin`，以普通merge整合`origin/codex/barepulse-implementation`，再push；由于connector快照与本地提交哈希不同，禁止用force覆盖任一历史。
3. 剩余外部条件：具体设备/服务器/端口/动作/窗口授权、固件/MIB/供电拓扑、真实媒体撤销证据、实际WHMCS/Smarty测试实例、生产启用授权。
4. 实机网关和IPMI缺少可信能力证据时保持unsupported/blocked；不能用模拟验收声称这些真实链路完成。
5. 续接先读本记录、AGENTS并运行`git status --short --branch`、`git log -3`；从待验CI和交付检查点继续，不重建已实现模块。
