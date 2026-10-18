# BarePulse 实现进度与续接记录

更新时间：2026-10-19（Asia/Shanghai）。实现、离线验证、真实验收与生产启用独立记录。

## 当前工作点

- 分支 `codex/barepulse-implementation`；本轮从干净的本地 `7cfcbd3` 继续，最后旧实现 `225bf57`，保留全部已有代码和本地历史。恢复时远端 `ac1e4db` 与本地同 tree `3a5fc2537dd4a3f86d67a9b5202745d8b308eabb`。
- 草稿 [PR #1](https://github.com/ygandwx/BarePulse/pull/1) 保持开放。开始时最新旧 [CI 38717357757](https://github.com/ygandwx/BarePulse/actions/runs/38717357757) 成功；不把旧 CI 当本轮新代码结果。
- 本轮首个实现提交 `422beb618264b186d5222bc08220022c2befd08b` 已发布为远端 `f308ebdf51bd9f41ffb3f1953fb582ddd2d3ab77`，同tree `cbb60441785de3cc7e0d94f39fc2d3b34e1f03d6`。最后实现提交 `93e1b4b356d2fb8a46fad250d88d488ec2fae7e8`（交付工具/CI）对应远端 `6a891726eda3b22116c3f3626965244305604502`，同tree `b497d25f46b71f4f3e6fc62354b4c66693493131`；两批CI均通过。
- 本轮可执行离线增量已收口；集中27项外部依赖、阶段总览、SNMP算法/UDP契约、控制台预检/撤销、已知目标监测、交付工具及回归已完成。本检查点只补最终证据，准确文档HEAD、远端HEAD和归档哈希写入交付manifest与HANDOFF，避免自引用哈希。
- 本地 PostgreSQL 重启并完成自身崩溃恢复；继续使用原开发库，没有重建/替换。原模拟 protected 会话及私有媒体保留。
- 真实设备访问和写操作仍为 false；没有调用真实设备或部署生产。外部材料只在[集中清单](external-dependencies.md)维护，不要求提交密码、token或私钥。
- 下一条准确任务：按 KV-01–KV-04 取得对应定制固件的最小脱敏静态材料，先固定真实鉴权/有效路由和WS/HID或MSD事件的离线夹具，再实现相应通道与强制撤销；保持双设备开关false。缺少材料只阻塞对应适配，不重复已完成的模拟核心。
- 未提交/未发布说明：实现代码已全部提交且远端同树，最终证据文档随本检查点提交后导出。无待修复的已知失败检查；原始本地commit历史没有被connector同SHA推送，保留在本地和完整bundle中，无强推或PR合并。

## 阶段状态

`IMPLEMENTED` 表示所述代码存在；`PARTIAL` 明确保留未实现部分。`PASS` 仅指链接所列实际测试。`NOT_RUN` 不等于失败，也不等于通过。此表不把模拟控制台或离线包算作生产完成。

| 阶段/范围 | 代码实现 | 离线测试 | 真实设备/外部验收 | 证据 | 阻塞原因 | 下一步 |
| --- | --- | --- | --- | --- | --- | --- |
| M1 资产、登录、绑定、任务审计 | IMPLEMENTED | PG/首链/浏览器 PASS | 本地首链不需设备；生产部署 NOT_RUN | [基础模块](modules/foundation.md)、[A07/A14](offline-acceptance.md)、下方检查记录 | 无离线实现阻塞；生产入口另需部署授权 | 保留首链回归；生产前确认TLS/账户/备份 |
| M2 流量与额度 | IMPLEMENTED：成员历史、不可重复账本、观测隔离、显式SNMPv3算法 | PG并发/升级/精确字节 PASS；UDP多页/认证/异常 PASS | SN2410/VyOS NOT_RUN | [流量](modules/traffic.md)、[SNMP](modules/snmp.md)、[A01–A04](offline-acceptance.md) | SN-01–04、VY-01–03：版本、算法、映射、计数方向/重启证据 | 取得静态证据后建精确采集配置；获授权再核对只读样本 |
| M3 电源驱动 | IMPLEMENTED：PiKVM、APC、Redfish、IPMI；持久unknown与单次派发 | PG幂等/串行、HTTP/UDP/子进程故障 PASS | 所有真实电源动作 NOT_RUN | [驱动](modules/drivers.md)、[IPMI](modules/ipmi.md)、[SNMP](modules/snmp.md)、[A05/A06/A12/A13](offline-acceptance.md) | AP-01–04、BMC-01–05、KV-05/KV-07：MIB/能力/接线/批准范围与物理完成证据 | 使用脱敏材料补精确profile；不补猜测OID，不补发超时动作 |
| M4 模拟共享控制台 | IMPLEMENTED：整机锁、保护、私有介质、输入、可撤销SSE、模式预检 | PG/活连接撤销/背压/断线重启 PASS | 模拟不适用实机结论 | [控制台](modules/console.md)、[A08–A11/A16](offline-acceptance.md) | 无已知离线阻塞；不能据此证明真实隔离 | 保留客户隔离、旧连接结束与持久保护回归 |
| M4 真实共享控制台 | PARTIAL：已知selected_port监测/漂移撤销组件已实现；视频、WS、MSD、Janus网关未完成 | HTTP回环目标监测 PASS；未知通道保持拒绝 | NOT_RUN；入口仍 ISOLATION_UNVERIFIED | [已知固件分析](pikvm-integration.md)、[组件与限制](modules/console.md) | KV-01–04缺有效认证/路由/事件/介质契约；KV-06缺整机隔离和撤销证据 | 先取得对应最小材料，写实际通道离线适配与撤销测试；另行授权后才联调 |
| M5 WHMCS | IMPLEMENTED：签名、映射、生命周期、门户、PHP模块/流量图 | PG、PHP/cURL→Go、Smarty4/5及浏览器 PASS | 实际有许可WHMCS宿主 NOT_RUN | [WHMCS](modules/whmcs.md)、[A14](offline-acceptance.md) | WH-01–04：宿主版本/主题、隔离实例、传输/秘密注入、角色/生命周期 | 在合法测试实例只接模拟资产，执行宿主权限与主题验收 |
| M6 离线交付 | IMPLEMENTED：运维/恢复资料、验收矩阵、源码ZIP/bundle/逐文件哈希导出 | 历史26表/媒体恢复 PASS；新工具13项真实Git测试及主仓库110文件导出 PASS | 不属于生产上线 | [交付工具](modules/delivery.md)、[备份恢复](backup-restore.md)、[离线矩阵](offline-acceptance.md) | 无本轮离线阻塞；源码包不能替代生产备份/验收 | 后续改动继续从干净HEAD导出并核对manifest，不复制工作区秘密 |
| M6 生产上线 | NOT_STARTED，配置默认关闭真实访问及未确认收费/限速/断网/断电 | 本轮配置/Compose独立临时项目检查 PASS | NOT_RUN，未授权 | [运维](operations.md)、[集中清单](external-dependencies.md) | 设备验收、访问范围、上线审批/回滚/运行责任尚未落实 | 上线前确认条目与审批，不由离线通过自动开启 |

## 本轮新增检查（2026-10-19）

| 检查 | 实际结果 | 证据范围与限制 |
| --- | --- | --- |
| SNMP驱动协议测试 | PASS 2.775s；25算法组合，12类SET故障，独立HMAC/密钥扩展核验 | [测试映射](modules/snmp.md)；loopback UDP，非厂商互通 |
| SNMP/流量/电源相关app回归 | PASS 15.166s，使用实际专用PG | 大整数、多页、共享显式配置；真实SNMP未访问 |
| 控制台预检与背压回归 | PASS 7.400s | [预检](../backend/internal/app/console_readiness_test.go)、[真实HTTP背压](../backend/internal/app/console_backpressure_test.go)；保留旧正常EOF断言 |
| PiKVM目标监测 | PASS 3.569s | [HTTP回环测试](../backend/internal/drivers/pikvm_console_guard_test.go)；监测未接入真实客户入口，不能证明物理隔离 |
| 全仓 scripts/verify.ps1（PG、PHP/cURL已配置） | PASS：app44.742s/drivers4.531s，gofmt、vet、build、JS syntax | Windows无CGO race单列NOT_RUN；Linux新CI待执行 |
| 可复现交付工具 | PASS 11/11，54.161s，无skip | [真实Git夹具](../scripts/tests/test_export_offline_package.py)；工具验证归档，应用测试声明单独标记 |
| 集中依赖清单 | PASS：27唯一编号、77本地链接、5 JSON模板、14配对代码围栏 | [缺口及安全取证](external-dependencies.md)；指令仅供另行授权执行，未连设备 |
| 预检审查后回归 | PASS：PG 5.107s，Node 5场景 | 暂停/终止服务解释原因，恢复后可申请且跨客户仍404；旧成功/旧失败/unmount不覆盖当前页面，真实动作权限未放宽 |
| OpenAPI检查 | PASS：86操作/81路由/56验收测试引用 | 新增只读预检JSON与认证契约 |
| 浏览器本轮流程 | PASS：登录、未绑定禁用、模拟模式/占用提示、真实模式拒绝、原protected保留，error日志为空 | 仅新增DEMO-READINESS-REAL和文档示例地址设备的本地绑定；无真实请求。截图barepulse-console-readiness-20261019.png留在交付outputs |
| PHP离线回归 | PASS：8文件lint、96 stub、112累计HTTP断言 | 不代表真实WHMCS宿主通过 |
| [本轮协议/控制台 CI 39149736617](https://github.com/ygandwx/BarePulse/actions/runs/39149736617) | PASS：Linux全套race app25.863s/drivers5.834s，API86/81/56、PHP、Compose构建/迁移/bootstrap与8项首链 | remote f308ebd/local422beb6同树；交付工具接入在下一增量，不能混用为其CI结果 |
| 交付工具CRLF增量与完整回归 | PASS：13/13，60.560s，无skip；新增定向3.825s | 实际复现干净CRLF误判与archive转码，两处均修复；仅保留有效行尾配置白名单，未继承helpers/filters/任意GIT环境，原工作区不改写 |
| 文档与前端导入 | PASS：README/docs全部相对文件链接存在，5个实际ES module import | 不把链接检查当作设备协议证明 |
| [本轮完整实现 CI 39150257321](https://github.com/ygandwx/BarePulse/actions/runs/39150257321) | PASS：Linux全套race app18.492s/drivers5.375s、Node5场景、Git导出13/13（2.334s无skip）、API86/81/56、PHP、Compose八项首链 | remote6a89172/local93e1b4b同树；包含全部本轮实现，无真实设备或生产访问 |
| 主仓库实际源码导出 | PASS：110文件、逐文件blob/SHA256/ZIP CRC、bundle空库unbundle/fsck/HEAD/tree、本地远端同树 | local93e1b4b；保留系统core.autocrlf=true但源码为提交原字节，原工作区干净且未修改。最终文档快照另导出并记录manifest |

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

## 历史检查记录（2026-10-15）

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

2026-10-19 新范围的可执行离线增量已实现并验证；剩余真实通道适配需要集中清单中的定制协议证据，实机/宿主/生产验收还需要相应环境及明确授权。没有因单次提交或上下文压缩停止，也没有为消耗额度扩造任务。

1. 代码、测试、迁移和资料已保存；关键回归无遗留失败。Windows race因CGO缺失为NOT_RUN，已由真实Linux CI race补足。实际WHMCS宿主与所有真实设备为NOT_RUN，不冒充通过。第一次CI健康命令失败及修复记录保留在上表。
2. 本地Git凭据恢复后，先`git fetch origin`，以普通merge整合`origin/codex/barepulse-implementation`，再push；由于connector快照与本地提交哈希不同，禁止用force覆盖任一历史。
3. 剩余外部条件统一由[外部依赖清单](external-dependencies.md)维护。独立Smarty模板已验，不能代替WHMCS宿主兼容性。
4. 真实控制台网关适配尚未完成，入口明确拒绝 `ISOLATION_UNVERIFIED`。需要定制固件的有效认证配置、Nginx include/extras、视频/WebSocket/WebRTC事件、MSD参数/状态及撤销证据；这些材料当前不在仓库，也不能以未授权SSH/HTTP读取补齐。取得材料后可以先继续离线适配开发，真实访问仍须独立授权。IPMI缺少设备和客户端审批时同样拒绝执行。
5. 续接先读本记录、AGENTS并运行`git status --short --branch`、`git log -3`；从未完成增量和交付检查点继续，不重建已实现模块。
6. 本地开发应用保留在`127.0.0.1:8080`，PostgreSQL在`127.0.0.1:55432`，使用原`barepulse_dev`。保留1个明确的模拟protected会话，不为清理环境擅自解除保护；私有备份、媒体和工具链留在忽略目录，未纳入源码交付包。源码ZIP和Git bundle可离线续接，不含口令或数据库备份。
