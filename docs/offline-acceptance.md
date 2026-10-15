# 离线验收与交付边界

本文件将开发计划 A01–A16 对应到已实现测试，并分开记录代码、离线执行和外部验收。设备 HTTP、SNMP、SSH、端口切换、电源和虚拟介质实机访问均未执行；生产环境未部署。`PASS` 只表示列出的本地模拟、契约或 PostgreSQL 测试通过，不能作为开启真实设备能力的授权。

## 可复现检查

在仓库根目录执行。PostgreSQL 测试使用专用测试数据库并自动创建隔离 schema；不得填入生产数据库。`scripts/verify.*` 要求设置 `TEST_DATABASE_URL`，缺失时直接失败，避免跳过数据库测试却误报完成。

```sh
python3 api/check_contract.py
sh scripts/verify.sh
sh scripts/verify-whmcs.sh
```

Windows 等价命令：

```powershell
python api/check_contract.py
./scripts/verify.ps1
./scripts/verify-whmcs.ps1
```

WHMCS 脚本需要 PHP 和 cURL 扩展。可通过 `PHP_BIN`、`PHP_EXTENSION_DIR` 指向便携运行时；Windows 也支持 `-Php` 和 `-ExtensionDirectory`。本次使用的实际命令为：

```powershell
./scripts/verify-whmcs.ps1 -Php ../toolchain/php/php.exe -ExtensionDirectory ../toolchain/php/ext
```

PHP HTTP 测试只启动随机端口的 `127.0.0.1` 临时 fixture，验证真实 cURL 请求、HMAC 和失败处理；结束后清理 fixture 文件和进程。它不是授权 WHMCS 安装实例。PHP 模块部署时只复制 `integrations/whmcs/barepulse/`，不部署 `tests/`。

另有 `TestWHMCSPHPRealHandlerContract` 通过实际 PHP `ApiClient`／cURL 请求真实 Go `httptest` handler 与 PostgreSQL，防止两端各自的 stub 同时通过却互不兼容。它包含在 `go test ./...`；找不到 PHP 时明确 `NOT_RUN`，发现 PHP 但缺 cURL 时失败。CI 应在 Go 测试之前安装 PHP/cURL。Windows 便携工具链的单独复现命令：

```powershell
$env:PHP_BIN = (Resolve-Path ../toolchain/php/php.exe).Path
$env:PHP_EXTENSION_DIR = (Resolve-Path ../toolchain/php/ext).Path
go test ./backend/internal/app -run '^TestWHMCSPHPRealHandlerContract$' -count=1 -v
```

以上仍需已设置专用 `TEST_DATABASE_URL`。fixture 凭据通过子进程 stdin 传递，PHP 拒绝非 loopback 地址；未启动设备 worker。

## 本次实际执行记录

2026-10-15，Windows 本地工作区；后续增量以 [实现进度](implementation-status.md) 中最后一次执行记录为准。

| 检查 | 结果 | 证据范围 |
| --- | --- | --- |
| `go test ./... -count=1`，设置专用 PostgreSQL 测试连接 | PASS | 集成主任务实际执行：app 64.865s、drivers 1.621s；覆盖身份、库存、任务、流量、控制台、WHMCS 和安全回归 |
| `go vet ./...`、构建最新后端二进制 | PASS | 集成主任务实际执行；不代表真实设备协议兼容性 |
| `go test ./backend/internal/drivers`、该包 `go vet` | PASS | 驱动子任务独立执行；全部传输为本地 fixture／mock，无实机请求 |
| `api/check_contract.py` | PASS | 83 个 OpenAPI 操作覆盖 78 个 Go 路由注册；JSON 格式、重复键、本地引用、Go JSON 字段、路径参数、认证组合和验收测试引用一致性 |
| `scripts/verify-whmcs.ps1` | PASS | PHP 8.5.11；7 个 PHP 文件 lint；54 条 stub 断言；HTTP 测试累计 69 条断言（含前述 54 条），不是 123 个独立案例 |
| `TestWHMCSPHPRealHandlerContract` | PASS | 最新定向执行 1.76s（包 4.304s），17 条 PHP→真实 Go/PG 断言；summary／power／traffic 大整数／幂等 power-actions／task／console，以及错误客户和服务拒绝；数据库核验只有 1 条任务和 1 个控制台会话 |
| `go test ./backend/internal/app -run '^TestTrafficPGThree' -count=1 -v` | PASS | 三台独立账户 0.94s、三成员共享账户 0.85s，包 4.714s；同 VLAN 主源拒绝重复、精确合计和重放唯一记账均实际检查 |
| Windows `go test -race` | NOT_RUN | 当前 Windows 工具链没有所需 CGO 编译器；同一代码的 Linux race 已在 CI 实际通过 |
| Linux `scripts/verify.sh`、`scripts/verify-whmcs.sh` | PASS | [CI 38709721860](https://github.com/ygandwx/BarePulse/actions/runs/38709721860)，remote 9e60f9e/local 1319414 相同 tree；完整 race（app 255.262s）、PHP/cURL→Go、7文件lint、54/累计69断言 |
| Docker Compose 构建、迁移、bootstrap、启动及 HTTP 首链 | PASS | 同次 CI 实际执行 `scripts/compose-smoke.sh`；8项首链断言，独立临时项目清理成功。真实访问双开关为 false；IPMI后续增量见最新进度 |
| 浏览器验收、备份恢复演练 | PASS，见实现进度 | 登录与资产／电源、流量、WHMCS门户／介质／保护／释放均实际操作；原开发库及独立故障fixture恢复另行核验 |
| 真实 SN2410／VyOS／PiKVM／APC／BMC、授权 WHMCS 实例 | NOT_RUN | 缺少本轮实机／实例授权；继续保持真实访问默认关闭 |

OpenAPI 文件使用 JSON 语法（合法 YAML 1.2 子集），不增加 YAML 解析依赖。检查器为离线结构与代码一致性检查，不冒充完整 OpenAPI 元模式验证器，也不从静态路由覆盖推断实际响应已全部执行。

## A01–A16 对照

测试名称均可在 `backend/internal/app/*_test.go` 或 `backend/internal/drivers/drivers_test.go` 查到。表中 `PG` 表示实际 PostgreSQL 的事务／约束测试，`fixture` 表示本机受控协议端点。

| 案例 | 代码与离线测试 | 本轮判断及尚需外部验证 |
| --- | --- | --- |
| A01 同 VLAN 三台独立额度 | `TestTrafficPGThreeIndependentAccountsSameVLAN`、`TestTrafficPGObservationOverlapAndBindingDrift`、`TestTrafficPGConstraintsSerializeOverlap` | 精确三台 PG 场景 PASS：同 domain/VLAN 300，三个账户各含一个成员，各端口独立记账，恰好 3 条 ledger；VLAN 主源重复被拒绝，observation 不入账。真实交换机端口/VLAN 归属、收发方向需授权核验 |
| A02 三台共享额度 | `TestTrafficPGThreeSharedMembersExactReplay`、`TestTrafficPGSharedPortsAndBoundaryHold`、`TestTrafficPGAccountingReplayOwnershipAndAdjustments` | 精确三成员 PG 场景 PASS：upload 606、download 1842、used 2448；每个末样本重放 3 次仍只有 6 个样本／3 条 ledger；三个服务器入口指向同账户，跨客户 404、跨 owner 组建 409。实机完整覆盖范围需授权确认 |
| A03 重启／中断／ifIndex 变更 | `TestTrafficCounterQualityAndPrecision`、`TestTrafficPGQualitySequenceAndMockCollector`、`TestTrafficPGMemberVersionRefreshKeepsHistory`、`TestTrafficSNMPReadOnlyContract` | 离线 PASS。复位、discontinuity、重映射分别标质量，重新建立基线，不生成巨额或零流量。真实固件 IF-MIB 响应与映射尚未验证 |
| A04 漏采跨周期／重跑／额度调整 | `TestTrafficCycleBoundaryIsNotInvented`、`TestTrafficPGAccountingReplayOwnershipAndAdjustments`、`TestTrafficPGSharedPortsAndBoundaryHold` | 离线 PASS。重复样本唯一入账，冲突重放拒绝；跨周期差值保留 `boundary_unallocated`，不按时间比例猜分；调整保留原因与幂等键 |
| A05 电源重复／超时／worker 重启 | `TestPowerIdempotencyOwnershipAndExecution`、`TestPowerUnknownBlocksAndRestartDoesNotReplay`、`TestPowerRecoveryAndDeviceSerialization`、`TestPiKVMTimeoutSentOnce`、`TestSNMPWriteFenceAndValidation`、`TestRuntimeIPMIDurableDispatchAndRevocation`、`TestIPMIAmbiguousWriteNeverRetriesOrLeaksOutput` | 离线 PASS。超时为 unknown、保留设备锁且不自动重发；SNMP 写保护禁止库内部重发 SET；IPMI 指令一次尝试、审批撤销阻止派发、uncertain任务幂等及阻断。实机超时后最终物理状态另需授权观察 |
| A06 PiKVM 200 内层失败／缺状态 | `TestPiKVMRejectsInnerFailures`、`TestPiKVMRequestAndCompletion`、`TestPowerMockScenariosAndSupplySemantics` | fixture PASS。HTTP 成功不能替代业务成功；缺失／非法 status、error、res 与返回格式均受检查。定制固件实际响应未调用验证 |
| A07 排队后绑定／归属变化 | `TestPowerBindingChangeAndDeviceChangeRejectDispatch`、`TestInventoryOwnershipConflictsAndHistory`、`TestSecuritySessionListHidesReassignedAssets` | PG PASS。server/device/binding 版本及客户归属在执行前复核，旧任务不能作用到新目标；历史绑定保留 |
| A08 同 PiKVM 两客户竞争 | `TestConsoleExclusiveProtectionRestartAndCleanup` | PG/mock PASS。整机唯一占用覆盖所有端口；第二客户排队，第一客户保护期间不切换。真实视频、键鼠和媒体整机隔离尚未验证 |
| A09 旧连接撤销后才能交接 | `TestConsoleLiveStreamRevokedBeforeHandoff`、`TestConsoleUnknownTargetAndMockReconciliation`、`TestSecurityInputRevalidatesAfterBodyRead` | 模拟 SSE 活连接关闭、generation 失效和事务内输入复核 PASS。真实 WebSocket／视频／WebRTC 尚未实现转发或验证，真实控制台入口保持 `ISOLATION_UNVERIFIED` |
| A10 装机／BIOS 保护跨断线重启 | `TestConsoleExclusiveProtectionRestartAndCleanup`、`TestConsoleMediaIsolationProtectionAndRoleRevocation`、`TestWHMCSPortalSingleUseAndProtectionOnSuspend`、`TestSecuritySuspendedCustomerCannotClearProtection` | PG/mock PASS。到期与 App 重建不自动释放 protected，会话暂停撤权而保留装机保护。不是物理刷 BIOS／装机测试；进程级重启与备份恢复由主任务另记 |
| A11 selected_port 错误回退 1 | `TestPiKVMSelectedPortAndCompleteness`、`TestConsoleUnknownTargetAndMockReconciliation` | fixture/mock PASS。错误不转成端口 1，目标未知时拒绝发票据并阻断交接；真实定制接口尚未验证 |
| A12 管理端口启禁与冷却 | `TestPiKVMRequestAndCompletion`、`TestPowerCooldownAndRealDevicesRemainDisabled`、`TestMockStateAndFaults` | 离线 PASS。客户不得启禁端口，操作含至少 30 秒冷却；超时仍保留冷却／未知占用。物理收敛时间未实测 |
| A13 PDU 供电不等于服务器启动 | `TestAPCProfileAndSupplySeparation`、`TestPowerAcceptedIsNotCompletion`、`TestPowerMockScenariosAndSupplySemantics`、`TestRedfishCapabilitiesAndResetEvidence` | 离线语义 PASS。supply_state 与 power_state 分离，reset 后 ON 不证明完成。双 PSU 线路、供电拓扑、实际开机证据 NOT_RUN |
| A14 伪造服务／端口／头／票据 | `TestWHMCSOwnershipSignatureReplayAndLifecycle`、`TestWHMCSPortalSingleUseAndProtectionOnSuspend`、`TestWHMCSPHPRealHandlerContract`、`TestPowerUnknownFieldsAndOperator`、`TestRuntimePowerApprovalGuards`、`TestRuntimePowerApprovedLoopbackAndSecrets`；PHP `contract.php`／`http_contract.php` | PG、fixture、PHP 与真实 Go handler 跨语言契约 PASS。独立检查 integration/service/client/server 归属，HMAC nonce 与票据单次，浏览器不能选择原始端口或携带设备认证。真实 WHMCS 客户会话／模板渲染尚待授权实例验证 |
| A15 extras／Janus／WebRTC 绕行 | `TestConsoleUnknownTargetAndMockReconciliation`、`TestNetworkDefaultDenyAndRedirect` | 离线 PASS。未实现通道明确 403，重定向和默认网络访问拒绝；没有暴露透明代理。无法据此证明真实 PiKVM 后台服务已隔离 |
| A16 残留介质／键鼠／票据 | `TestConsoleExclusiveProtectionRestartAndCleanup`、`TestConsoleMediaIsolationProtectionAndRoleRevocation`、`TestConsoleLiveStreamRevokedBeforeHandoff`、`TestSecurityOperatorCannotDeleteMedia` | PG/mock PASS。释放前清除模拟挂载与按键、撤销 token/generation 并关闭旧 SSE，私有 ISO 不能跨客户读取。真实 USB gadget、挂载和视频进程撤销证据 NOT_RUN |

## 交付材料与后续实机入口

- [OpenAPI](../api/openapi.yaml) 和 [离线一致性检查器](../api/check_contract.py)：所有已实现 HTTP 路由与认证方式。
- [驱动边界](modules/drivers.md)：PiKVM 定制协议、Redfish 能力发现、APC 明确固件 profile、SNMP 写保护及测试事实；不预填未知 OID。
- [IPMI 源码契约与批准 profile](modules/ipmi.md)：固定 ipmitool 版本与二进制校验、单次尝试、子进程隔离、凭据边界；不以离线 subprocess 测试声称 BMC 已支持。
- [WHMCS 模块说明](modules/whmcs.md) 与 [安装 README](../integrations/whmcs/README.md)：服务映射、HMAC、客户归属、CSRF、异步结果和短期控制台入口。
- [运维说明](operations.md)：部署、备份恢复、未知状态人工处理和保护保留。
- [实现进度／续接检查点](implementation-status.md)：当前分支、最后提交、未提交／未推送内容、准确下一任务与外部阻塞。

进入真实设备阶段必须另有明确测试设备、服务器、端口、动作及时间窗口授权，并获得固件版本、目标映射、APC MIB／OID、Redfish SystemPath 和成功／未知判定证据。当前代码即使存在真实驱动，也不代表允许调用设备；两个访问开关及精确授权 profile 默认不开放，真实控制台继续拒绝访问。未经确认的收费、超额限速、断网和断电策略保持关闭。
