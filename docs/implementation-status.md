# BarePulse 实现进度与续接记录

更新时间：2026-10-15（Asia/Shanghai）。代码、离线验证和实机证据分别记录。

## 当前工作点

- 分支：`codex/barepulse-implementation`，基线 `0127756`；工作区 `work/BarePulse`。初始仓库无用户改动。
- 最后实现提交：`225bf575a409430495bd8bd5593368134e18a031`。对应远端快照 `d4a48a16bb376dea09536ccc4fec0fde85d8cda8`，二者 Git tree 完全相同：`d30bdc59cf68e0e19542c9d1b403e4d17e12d0cb`。此前 `9783dda`/`83671d3`、`cb0d7e2`/`bbab42e`、`1319414`/`9e60f9e`、`7afa3c0`/`ac8a4d1` 也已核对同树。
- 草稿 PR：[BarePulse #1](https://github.com/ygandwx/BarePulse/pull/1)。CLI 没有本地 Git 凭据，改用已授权 GitHub connector 发布，未强推；本地提交历史完整保留。
- 最后一批实现：共享成员加入/退出/重入与历史保留、同客户跨账户VLAN观测及覆盖版本校验、迁移006、实际Smarty模板验收、26表备份恢复。当前提交只整理交付证据，不再改动代码。
- 交付状态：约定范围内可执行的离线实现、测试和资料已完成；未提交改动无、未发布代码差异无。CLI不能直接push，但connector发布已完成，保留本地与远端两套提交历史，未覆盖任一历史。最终文档HEAD和归档哈希另记录于交付清单。
- 下一条准确任务：获得下文列明的固件/有效路由/介质撤销材料后，先在双开关false状态实现真实PiKVM网关适配及本地撤销契约，再进入另行明确授权的设备验收。当前不凭标准上游协议猜测定制固件行为。
- 当前仅离线代码、本地服务和 loopback/模拟测试；没有真实设备访问或生产部署。

## 阶段状态

| 阶段 | 代码 | 离线验证 | 实机/外部 |
| --- | --- | --- | --- |
| M1 | IMPLEMENTED | PostgreSQL、浏览器首链、OpenAPI PASS | 不适用 |
| M2 | IMPLEMENTED，含成员历史、同客户跨账户观测和升级迁移 | 三台独立/共享、精确账本、并发去重、质量、成员并发、旧库升级、模拟采集、浏览器 PASS | BLOCKED：SN2410/VyOS未授权 |
| M3 | IMPLEMENTED：PiKVM/Redfish/APC/IPMI，均受明确批准范围约束 | 持久调度、故障与loopback协议契约、IPMI专项及全量原生回归 PASS | BLOCKED：固件/MIB/BMC能力/实际控制未验 |
| M4 | 离线核心IMPLEMENTED：整机租约、模拟网关、输入、私有介质、保护和清理；真实网关未实现开放 | PG交接/旧流关闭/安全回归、进程重启保护、备份恢复 PASS | BLOCKED：真实视频/WebSocket/WebRTC/介质路由及隔离证据不全；实机网关拒绝开放 |
| M5 | IMPLEMENTED：映射、签名、门户、PHP模块、停用/轮换和流量图 | Go/PG与PHP契约、17项PHP→真实Go断言、Smarty4/5实际渲染、管理页面 PASS | BLOCKED：实际WHMCS安装实例未提供 |
| M6 | 离线资料IMPLEMENTED：部署、恢复脚本、运维与验收矩阵、PR及源码交付 | 原生全量验证、26表恢复、Linux race、PHP及容器首链 PASS | BLOCKED：生产/实机未授权 |

## 已实现与关键决定

- 空库管理员初始化、bcrypt、哈希会话、CSRF/Origin、退出撤销和登录限流；没有默认口令。
- 资产/设备/用户 CRUD、历史绑定及版本、数据库端口唯一与跨 power/console 物理端口归属；operator只读自有范围。
- 电源持久任务和幂等、设备/服务器串行、派发前客户/绑定版本复核、unknown不重发、至少30秒冷却；PDU供电与服务器启动分开。
- 真实电源需要双总开关和外部精确设备/server/port/action/时间窗 profile；默认关闭，不猜APC OID，不自动降级IPMI。
- IPMI 固定 ipmitool 1.8.19 与管理员批准的二进制 SHA256、LANplus/cipher17、单次命令尝试；密码仅进最小子进程环境，不经 shell/argv；复位与cycle没有完成证据时保留unknown。容器安装客户端不代表授权可用。
- 流量独立/共享账户、不可变来源、原始样本与精确十进制账本、port/VLAN重叠约束、观测不入账、重放去重、复位/漏采/换绑质量；跨周期不明字节保留未分配。收费、限速、断网、断电关闭。
- 成员变更需要原因和当前服务器版本，保留当期quota/ledger与每次加入/退出区间；拓扑变更和采集持锁串行。VLAN观测可以覆盖同客户不同账户，但必须明确快照成员与版本；变更后停止旧来源，客户看不到范围失效的观测数据。迁移006保留001–005历史数据。
- KVM整机独占、FIFO、跨断线/重启保护、单次票据、短时授权；旧流实际关闭后才交接，键鼠/介质清理有模拟状态证据。未知连接登记不自动过期删除。
- WHMCS实例/服务/客户/服务器独立校验，HMAC nonce防重放；生命周期只改访问，保留保护。实例停用/轮换销毁portal grants，重新启用不能恢复旧票据；票据兑换与轮换持锁串行。
- WHMCS 图表使用API的真实 intervals/samples，最多200区间/100样本；未采集不画为零，恢复/跨周期区间以虚线显示，精确字节不经过浮点额度计算。
- Go 1.27.2 / PostgreSQL 18.6 / pgx v5.7.6 / gosnmp v1.45.0 / x/crypto v0.41.0；前端同源原生ES modules，无npm构建。秘密仅外部profile/环境引用，避免写入响应和审计。

## 实际检查记录

| 检查 | 实际结果 | 限制 |
| --- | --- | --- |
| 最新 `scripts/verify.ps1`，配置PG/PHP/cURL | PASS：gofmt、go vet、全套Go测试（app 50.584s / drivers 1.514s）、build、JS syntax；包含迁移006、成员与观测增量 | Windows race无CGO，单列NOT_RUN；不是实机测试 |
| `api/check_contract.py` | PASS：85操作 / 80路由、48个测试引用、参数/认证/Go JSON字段；5个ES module实际import也通过 | 离线结构检查，不冒充完整OpenAPI元模式验证 |
| `scripts/verify-whmcs.ps1` | PASS：8 PHP文件lint、96 stub断言、HTTP累计112断言（含图表） | 112包含96；实际WHMCS实例NOT_RUN |
| `integrations/whmcs/tests/render_template.php` | PASS：官方Smarty4.5.8及5.8.5各50条实际渲染断言；两次均通过96条共享契约；浏览器核对区间图、未知/失败/空数据及转义 | 独立模板验收，不代表已安装WHMCS；官方源码/哈希与复现方式见模块文档 |
| `TestWHMCSPHPRealHandlerContract` | PASS：17项实际PHP/cURL→Go httptest→PG断言；只1个幂等任务 | 未启动设备worker |
| `TestTrafficPGThree*` | PASS（4.714s）：三独立同VLAN、三共享精确2448字节；重放仍6样本/3账本 | 真实网络拓扑须授权核验 |
| `TestTrafficPGMemberTransferKeepsSharedPeriod`、`TestTrafficPGMemberConcurrentJoinExitAndCollect`、`TestTrafficPGIndependentVLANObservationIsolation`、`TestTrafficPGLegacyMembershipMigrationPreservesData` | PASS：全traffic包13.605s；成员转移保留3348字节账本及500字节调整、并发加入/退出/采集、跨账户观测隐私、真实旧schema升级前后指纹一致并继续入账 | 传输为模拟；旧库升级测试实际执行001–005再006，不只测试空库 |
| `TestIntegrationRevocationRotationPreservesProtection` | PASS：非admin拒绝、缺enabled拒绝、停用/换密钥/旧票据永久撤销、保护保留 | 不把密钥轮换等同设备操作 |
| 浏览器 M1/M2/M4/M5 | PASS：登录→录入→绑定→任务succeeded→审计；流量账户/来源/两次采样/精确差值；进程重启后protected原样保留；实例登记/停用/服务绑定/暂停 | 模拟画面明确标识，浏览器error日志为空；截图保存在聊天outputs |
| 浏览器 WHMCS 客户门户 | PASS：单次URL兑换后清除fragment、私有58368字节媒体上传/挂载、动态模拟画面/F2输入、保护期间拒绝卸载、显式结束后释放并关闭旧流 | 不可启动的离线字节夹具；实际DB确认media ready、目标/按键/挂载均清空；修正释放后页面媒体状态未刷新的显示问题 |
| 首链 HTTP 冒烟脚本 | PASS：本地及CI容器实际login/asset/binding/mock power/completion/idempotency/audit八项；非法origin、重定向和real开关拒绝 | 只访问本机fixture，无生产部署 |
| 主开发库备份恢复 | PASS：`barepulse_dev`→全新`barepulse_restore_m6_20261015_01`，内容指纹、源未变、保护1/旧连接0一致 | 主服务先停写；无媒体文件，因此媒体实字节另用独立fixture验证 |
| 迁移006后主库与媒体恢复 | PASS：17:52停写并恢复到全新`barepulse_restore_m6_20261015_03`；26表完整行指纹一致、源未变、媒体2份哈希一致、protected1；成员历史和2条VLAN覆盖快照均非空 | 原库和所有演练库保留；重启仍用原dev库，浏览器确认原保护状态保留 |
| 浏览器成员变更与VLAN观测 | PASS：重复归属409，退出/重入保留历史；同客户跨两账户观测采集1530376上传/3060753下载字节，billable=false，账户用量0B且quota不变；SQL独立复核 | 本地模拟采集；不把不明流量当零入账 |
| 独立恢复故障fixture | PASS：25表、unknown1/protected1/gateway1/媒体1完整保留；覆盖已有库/未停写/设备开关/媒体损坏均拒绝 | 仅新建本地演练库；没有删除或覆盖现有库 |
| CI 首次执行 | FAIL，已修复：runner拆分单引号health-cmd导致docker create把-U当参数 | 改双引号后后续实际执行通过，没有跳过检查 |
| [CI 38709721860](https://github.com/ygandwx/BarePulse/actions/runs/38709721860) | PASS：完整Linux race（app 255.262s、drivers 1.065s）、Go/PHP/cURL跨语言、OpenAPI、PHP脚本、Compose配置及构建/迁移/bootstrap/首链 | remote 9e60f9e；IPMI后续增量需新CI，不混用结果 |
| IPMI 定向验证 | PASS：驱动TestIPMI（3.240s）、go vet；平台TestRuntimeIPMI/Power（4.200s） | fake transport/受控Go helper子进程及真实PG；未运行真实ipmitool或BMC |
| [IPMI CI 38712627212](https://github.com/ygandwx/BarePulse/actions/runs/38712627212) | PASS：Linux全套race（app 21.169s、drivers 3.273s）、OpenAPI、PHP及实际Compose首链 | remote bbab42e/local cb0d7e2同树；容器已安装ipmitool 1.8.19，未访问BMC |
| 电源页面轮询浏览器回归 | PASS：真实operations.js配本地UI夹具，填入核验说明/勾选、展开审计，再插入新任务并等自动刷新后内容均保留 | 只测试UI轮询，不执行任何电源任务；临时服务与页面已关闭 |
| [图表与轮询CI 38713973740](https://github.com/ygandwx/BarePulse/actions/runs/38713973740) | PASS：Linux race、OpenAPI、PHP及实际Compose首链 | remote83671d3/local9783dda同树；不冒充本次成员增量的CI |
| [最终实现CI 38716675607](https://github.com/ygandwx/BarePulse/actions/runs/38716675607) | PASS：Linux全量race（app25.047s/drivers3.304s）、85/80/48契约、PHP8文件96/112断言、Compose构建/迁移/bootstrap/启动及8项首链 | remote d4a48a1/local225bf57同树；包含成员历史、迁移006和跨账户观测；未跳过失败项 |

最新主开发库 dump SHA256：`5c415e294359b38b4fab1d588f3041e30db107ca2aaf0af0e7761f0cb931e5f5`；私有备份证据在被忽略的 `work/backup-restore-dev-20261015-03/`。此前01/02演练和独立故障fixture均保留。备份含账户/会话及私有媒体，不发布到Git或交付源码包。服务重启实际使用原dev库，没有替换用户数据库。

已修复的问题包括：PHP任务/门户48hex契约；APC endpoint解析panic；慢body跨撤权；portal operator身份提升；事务内再次取pool的饥饿；旧terminated映射误伤新服务；实例更新缺少撤销路径。新增轮换测试首轮因测试把expires_in误解成字符串失败，已修测试解码并真实通过。没有删除失败断言或用固定成功响应修绿。

## 续接与交付边界

当前停止点是所有剩余项均依赖外部资料、实例或实机授权，不是阶段完成、上下文压缩或额度暂停。最近可靠读数周额度已用53%，仅代表该次读取，未声称持续监控。

1. 代码、测试、迁移和资料已保存；关键回归无遗留失败。Windows race因CGO缺失为NOT_RUN，已由真实Linux CI race补足。实际WHMCS宿主与所有真实设备为NOT_RUN，不冒充通过。第一次CI健康命令失败及修复记录保留在上表。
2. 本地Git凭据恢复后，先`git fetch origin`，以普通merge整合`origin/codex/barepulse-implementation`，再push；由于connector快照与本地提交哈希不同，禁止用force覆盖任一历史。
3. 剩余外部条件：具体设备/服务器/端口/动作/窗口授权、固件/MIB/供电拓扑、真实媒体撤销证据、实际WHMCS安装测试实例、生产启用授权。独立Smarty模板已验，不能代替WHMCS宿主兼容性。
4. 真实控制台网关适配尚未完成，入口明确拒绝 `ISOLATION_UNVERIFIED`。需要定制固件的有效认证配置、Nginx include/extras、视频/WebSocket/WebRTC事件、MSD参数/状态及撤销证据；这些材料当前不在仓库，也不能以未授权SSH/HTTP读取补齐。取得材料后可以先继续离线适配开发，真实访问仍须独立授权。IPMI缺少设备和客户端审批时同样拒绝执行。
5. 续接先读本记录、AGENTS并运行`git status --short --branch`、`git log -3`；从未完成增量和交付检查点继续，不重建已实现模块。
6. 本地开发应用保留在`127.0.0.1:8080`，PostgreSQL在`127.0.0.1:55432`，使用原`barepulse_dev`。保留1个明确的模拟protected会话，不为清理环境擅自解除保护；私有备份、媒体和工具链留在忽略目录，未纳入源码交付包。源码ZIP和Git bundle可离线续接，不含口令或数据库备份。
