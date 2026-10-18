# 控制台：实现、离线证据与真实通道边界

更新：2026-10-19。整机独占、客户归属、访问代次、持久保护和旧连接登记由平台维护。模拟设备能验证状态机；它不能证明定制PiKVM的实际视频、键鼠或USB介质隔离。

## 已实现的本地预检

`GET /api/v1/servers/{id}/console-readiness` 只读数据库，先检查服务器归属，返回模式、是否允许申请、可能排队、原因和已实现通道。它不创建租约、不读设备、不返回地址/凭据，也不会因为双设备开关开启而放行真实控制台。

- `simulated`：只有 `mock_events`、`mock_input`、`private_media`，页面明确显示“申请模拟控制台”。
- `real`：当前返回 `ISOLATION_UNVERIFIED`，通道数组为空，按钮禁用，并说明缺少真实通道与撤销证据。
- `unbound`：缺少控制台绑定。维护、设备停用、服务暂停、operator只读身份分别显示对应原因。
- `queue_expected` 是设备已有占用、阻塞或冷却的提示；自己的已有会话可能直接续接。预检不是获得锁的证明，申请接口仍在事务内重新校验。

`TestConsoleReadinessIsScopedAndNeverProbesDevices` 验证跨客户404、模拟/真实区分、即使开关true也不产生设备HTTP请求、无地址/凭据泄漏及无租约副作用。`TestConsoleReadinessTracksMaintenanceBindingAndRole` 覆盖维护、停用、移除绑定及只读角色。

`TestConsoleReadinessExplainsInactiveOwnedService` 验证暂停/终止服务仍可向所属客户解释禁用原因、其它客户仍404、实际会话申请仍拒绝，恢复后重新允许模拟申请。预检只读取必要归属和维护字段，不放宽其它资产或操作接口。独立审查发现原通用服务器查询会提前隐藏暂停服务，已修复。

前端对成功和失败响应都校验请求代次与页面卸载状态，旧服务器的迟到错误不会覆盖新选择。`node scripts/tests/console-readiness.mjs` 实际导入模块，使用可控Promise覆盖旧成功、旧失败、当前失败保持、卸载后成功/失败共5场景；它是组件回归，另有实际浏览器检查未绑定/模拟/真实三种模式与禁用按钮。

## 慢客户端与撤销

模拟SSE流每次写入有5秒截止时间。会话撤销会取消流上下文，并把写截止时间缩短到250毫秒：正常客户端可以收到HTTP结束块，停止读取的客户端也不能无限占住写入。授权复查使用已取消的流上下文，避免撤销后仍等待数据库请求。只有处理器退出并删除旧连接登记后，交接才可继续；删除登记失败时仍保留阻塞，不猜测已清理。

`TestConsoleRevocationInterruptsBlockedHTTPWrite` 使用真实Go HTTP处理器、完整HTTP请求/分块响应及 `net.Pipe` 的确定性背压，客户端在响应头后停止读取；验证撤销中断写入、登记清理和会话释放。既有 `TestConsoleLiveStreamRevokedBeforeHandoff` 保持正常客户端干净EOF验收。首轮立即截止导致旧测试出现unexpected EOF；改为有界250毫秒收尾后两条测试真实通过，没有放宽原断言。

## 真实目标监测的已知契约实现

`drivers.PiKVM.WatchConsoleTarget` 只复用[已确认定制固件契约](../pikvm-integration.md#54-apichassis_status)的 `GET /api/chassis_status`。它要求1–16的绑定端口、已确认的端口映射及既有网络访问策略；默认不能联网。

调用先完成一次目标核验，再以明确的250毫秒至5秒间隔监测（默认1秒）。首次目标漂移、内层错误、缺少字段、超时或未知结果会取消返回的context；撤销是锁存的，后来目标恢复不会自动恢复访问。`Close()`取消在途HTTP读取并等待监测协程退出，不切换目标、不释放持久租约或保护。

这段代码是未来真实网关可复用的目标监测组件，当前没有接入真实客户媒体入口。调用方必须已经持有整台KVM租约，并把所有通道生命周期绑定到该context。轮询存在检测间隔，不能保证带外切换到检测之间没有画面泄漏，因此独占物理管理、实际媒体撤销和有效认证仍是独立门槛。

`TestPiKVMConsoleGuardLatchesDriftAndUnknown` 覆盖真实本机HTTP返回的目标漂移、error加回退1、缺字段，以及撤销后不重连；`TestPiKVMConsoleGuardCloseCancelsInFlightRead` 核验在途请求退出；`TestPiKVMConsoleGuardRejectsBeforeNetwork` 核验访问关闭、映射未确认、端口和频率无效时零请求。

## 尚未实现与必须保留的边界

真实视频转发、WebSocket键鼠事件白名单、MSD挂载/卸载、Janus/WebRTC终止与全路径隔离尚未完成。不得把上述监测组件或模拟SSE当作真实网关。已知 `/api/ws` 与 `/streamer` 前缀不等于已知事件、媒体资源或撤销语义；未确认路径继续拒绝。

缺失材料、已有来源及安全获取方式集中在[外部依赖清单](../external-dependencies.md)的定制PiKVM部分。真实设备验收与生产启用分列于[阶段总览](../implementation-status.md#阶段状态)，不因离线测试通过改成已完成。
